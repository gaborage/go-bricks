package database

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// DBConfigProvider provides per-key database configurations.
// This interface abstracts where tenant-specific database configs come from.
type DBConfigProvider interface {
	// DBConfig returns the database configuration for the given key.
	// For single-tenant apps, key will be "". For multi-tenant, key will be the tenant ID.
	DBConfig(ctx context.Context, key string) (*config.DatabaseConfig, error)
}

// Connector creates database connections from configuration
type Connector func(*config.DatabaseConfig, logger.Logger) (Interface, error)

// ReleaseFunc releases a lease obtained from Get. Callers must invoke it (typically
// deferred) when they are finished with the connection for the current unit of work.
// It is idempotent: calling it more than once is a safe no-op. The connection itself
// is a long-lived, shared pool — Release does NOT close it; it only signals that this
// borrower is done, so a connection evicted while leased can be closed once its last
// lease is released. See ADR-032.
type ReleaseFunc func()

// maxGetAttempts bounds the rare retry where a freshly resolved entry is evicted before
// the caller can acquire a lease on it (only reachable under extreme pool churn). A new
// entry is inserted at the LRU front, so in practice the first attempt always succeeds.
const maxGetAttempts = 4

// DbManager manages database connections by string keys.
// It provides lazy initialization, LRU eviction, and cleanup for database connections.
// The manager is key-agnostic - it doesn't know about tenants, just manages named connections.
type DbManager struct {
	logger         logger.Logger
	resourceSource DBConfigProvider
	connector      Connector // Injected for testability

	// Connection management
	mu    sync.RWMutex
	conns map[string]*dbEntry

	// LRU management
	lru     *list.List
	maxSize int

	// Cleanup management
	idleTTL   time.Duration
	cleanupMu sync.Mutex
	cleanupCh chan struct{}

	// Singleflight for concurrent initialization
	sfg singleflight.Group
}

// dbEntry represents a database connection with metadata.
// refs, detached, and closed are guarded by DbManager.mu.
type dbEntry struct {
	conn     Interface
	element  *list.Element // for LRU
	lastUsed time.Time
	key      string

	// refs counts outstanding leases (current borrowers). A connection with refs > 0
	// is in use and must not be closed.
	refs int
	// seedHeld is true when one of refs is an unclaimed "seed" lease taken at creation. The
	// seed keeps a brand-new entry alive (refs >= 1) through the window before its first Get
	// caller claims it, so a concurrent evict/idle-cleanup can only detach (never close) it.
	// The first claimOrAcquire takes the seed; later callers increment refs normally.
	seedHeld bool
	// detached is set when the entry has been removed from the map+LRU (evicted or
	// idle-cleaned) but its Close() was deferred because a lease was still outstanding.
	detached bool
	// closed guards against a double Close() once the deferred close has run.
	closed bool
}

// DbManagerOptions configures the DbManager
type DbManagerOptions struct {
	MaxSize int           // Cached-connection cap; <=0 uses a default (not unlimited).
	IdleTTL time.Duration // Idle-connection lifetime; <=0 uses a default (not disabled).
}

// NewDbManager creates a new database manager
func NewDbManager(resourceSource DBConfigProvider, log logger.Logger, opts DbManagerOptions, connector Connector) *DbManager {
	if opts.MaxSize <= 0 {
		opts.MaxSize = 100 // sensible default
	}
	if opts.IdleTTL <= 0 {
		opts.IdleTTL = 30 * time.Minute // sensible default
	}

	// Default to real connection factory if none provided
	if connector == nil {
		connector = NewConnection
	}

	return &DbManager{
		logger:         log,
		resourceSource: resourceSource,
		connector:      connector,
		conns:          make(map[string]*dbEntry),
		lru:            list.New(),
		maxSize:        opts.MaxSize,
		idleTTL:        opts.IdleTTL,
	}
}

// Get returns a database connection for the given key plus a ReleaseFunc the caller must
// invoke when finished with it for the current unit of work (typically deferred). For
// single-tenant, use key "". For multi-tenant, use the tenant ID. Connections are created
// lazily and cached with LRU eviction; the lease prevents a connection that is evicted
// while in use from being closed under an active caller (the #606 race). On error the
// returned ReleaseFunc is nil — check err first.
func (m *DbManager) Get(ctx context.Context, key string) (Interface, ReleaseFunc, error) {
	for attempt := 0; attempt < maxGetAttempts; attempt++ {
		// Fast path: getExisting increments the refcount atomically with the lookup, so the
		// connection cannot be evicted-and-closed before the lease is taken.
		if entry := m.getExisting(key); entry != nil {
			return entry.conn, m.makeRelease(entry), nil
		}

		// Slow path: singleflight collapses concurrent creates for the same key into one. It
		// returns the shared entry (freshly created with a seed lease, or an existing one);
		// every caller then takes its own lease on that pointer via claimOrAcquire — the first
		// claims the seed, the rest increment — so each concurrent borrower is counted.
		v, err, _ := m.sfg.Do(key, func() (any, error) {
			if e := m.peek(key); e != nil {
				return e, nil
			}
			return m.createConnection(ctx, key)
		})
		if err != nil {
			return nil, nil, err
		}

		entry := v.(*dbEntry)
		if m.claimOrAcquire(entry) {
			return entry.conn, m.makeRelease(entry), nil
		}
		// The reused entry was closed in the window between lookup and claim (a concurrent
		// idle-cleanup of an unleased entry); loop to create a fresh one. The create path
		// always succeeds because a new entry carries a seed lease, so this converges.
	}

	return nil, nil, fmt.Errorf("failed to acquire database connection for key %s after %d attempts (pool churn)", key, maxGetAttempts)
}

// claimOrAcquire takes one lease on entry, operating on the shared pointer so it can never
// "miss" via a map lookup. It returns false only when the entry has already been fully closed
// (a reused entry that lost a race), signaling the caller to retry with a fresh entry.
func (m *DbManager) claimOrAcquire(entry *dbEntry) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry.closed {
		return false
	}
	if entry.seedHeld {
		entry.seedHeld = false // claim the seed: that ref becomes this caller's lease
	} else {
		entry.refs++
	}
	return true
}

// getExisting returns an existing entry with a lease acquired (refcount incremented) and
// updates LRU, or nil if not found. The refcount increment happens under the same lock as
// the lookup so the entry cannot be evicted-and-closed before the lease is taken.
func (m *DbManager) getExisting(key string) *dbEntry {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, exists := m.conns[key]
	if !exists {
		return nil
	}

	// Update LRU and last used time
	entry.lastUsed = time.Now()
	m.lru.MoveToFront(entry.element)
	entry.refs++

	return entry
}

// peek reports whether an entry exists for the key without taking a lease or touching LRU.
// Used inside the singleflight callback to decide whether a new connection is needed.
func (m *DbManager) peek(key string) *dbEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.conns[key]
}

// makeRelease returns an idempotent ReleaseFunc bound to a single lease on entry.
func (m *DbManager) makeRelease(entry *dbEntry) ReleaseFunc {
	var once sync.Once
	return func() {
		once.Do(func() { m.releaseEntry(entry) })
	}
}

// releaseEntry drops one lease. If the entry was detached (evicted/idle-cleaned) while
// leased and this was the final lease, it closes the connection now — outside the lock.
func (m *DbManager) releaseEntry(entry *dbEntry) {
	m.mu.Lock()
	entry.refs--
	shouldClose := entry.detached && entry.refs <= 0 && !entry.closed
	if shouldClose {
		entry.closed = true
	}
	m.mu.Unlock()

	if shouldClose {
		m.closeEvicted(entry, "Error closing evicted database connection (deferred until lease release)")
	}
}

// createConnection creates a new database connection for the given key and registers it
// with a single seed lease (refs == 1, seedHeld). The seed keeps the entry alive through
// the window before the caller claims it via claimOrAcquire, so a concurrent evict/idle
// cleanup can only detach it. Returns an existing entry unchanged on a double-create race.
func (m *DbManager) createConnection(ctx context.Context, key string) (*dbEntry, error) {
	// Get configuration for this key
	dbConfig, err := m.resourceSource.DBConfig(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to get database config for key %s: %w", key, err)
	}

	// Create the connection using injected connector
	conn, err := m.connector(dbConfig, m.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create database connection for key %s: %w", key, err)
	}

	// Store in cache with LRU tracking
	m.mu.Lock()

	// Check if connection was created by another goroutine while we were waiting
	if existing, exists := m.conns[key]; exists {
		existing.lastUsed = time.Now()
		m.lru.MoveToFront(existing.element)
		m.mu.Unlock()

		// Close our new connection outside the lock and return the existing one.
		if err := conn.Close(); err != nil {
			m.logger.Error().
				Err(err).
				Str("key", key).
				Msg("Error closing redundant database connection")
		}
		return existing, nil
	}

	// Ensure we don't exceed max size (returns the evicted entry to close outside the lock)
	evicted := m.evictIfNeeded()

	// Add to cache with a seed lease.
	element := m.lru.PushFront(key)
	entry := &dbEntry{
		conn:     conn,
		element:  element,
		lastUsed: time.Now(),
		key:      key,
		refs:     1,
		seedHeld: true,
	}
	m.conns[key] = entry
	m.mu.Unlock()

	// Close the evicted connection outside the lock so a slow Close() on the LRU
	// victim does not block concurrent Get() calls for other keys.
	if evicted != nil {
		m.closeEvicted(evicted, "Error closing evicted database connection")
	}

	m.logger.Info().
		Str("key", key).
		Str("db_type", dbConfig.Type).
		Msg("Created new database connection")

	return entry, nil
}

// evictIfNeeded removes the least recently used connection from the manager's
// bookkeeping if at capacity. Must be called with m.mu held. It detaches the entry
// (removing it from the map+LRU) and returns it for the caller to close OUTSIDE the
// lock — but ONLY when the entry has no outstanding leases. If the LRU victim is still
// leased, its Close() is deferred (detached=true) until the last lease is released, so
// an in-use connection is never closed (the #606 race). Returns nil when nothing should
// be closed now.
func (m *DbManager) evictIfNeeded() *dbEntry {
	if len(m.conns) < m.maxSize {
		return nil
	}

	// Remove the least recently used connection
	oldest := m.lru.Back()
	if oldest == nil {
		return nil
	}

	key := oldest.Value.(string)
	entry := m.conns[key]

	// Remove from cache (close happens outside the lock, and only when unleased)
	delete(m.conns, key)
	m.lru.Remove(oldest)
	entry.detached = true

	m.logger.Debug().
		Str("key", key).
		Msg("Evicted database connection due to LRU limit")

	if entry.refs > 0 {
		// Still in use — defer the close to the final lease release.
		return nil
	}
	entry.closed = true
	return entry
}

// closeEvicted closes an evicted/idle connection and logs any close error.
// It is always called WITHOUT m.mu held so a slow Close() cannot block other callers.
func (m *DbManager) closeEvicted(entry *dbEntry, logMsg string) {
	if err := entry.conn.Close(); err != nil {
		m.logger.Error().
			Err(err).
			Str("key", entry.key).
			Msg(logMsg)
	}
}

// StartCleanup starts the background cleanup routine for idle connections
func (m *DbManager) StartCleanup(interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute // default cleanup interval
	}

	m.cleanupMu.Lock()
	if m.cleanupCh != nil {
		m.cleanupMu.Unlock()
		return
	}
	done := make(chan struct{})
	m.cleanupCh = done
	m.cleanupMu.Unlock()

	go m.cleanupLoop(interval, done)
}

// StopCleanup stops the background cleanup routine
func (m *DbManager) StopCleanup() {
	m.cleanupMu.Lock()
	if m.cleanupCh == nil {
		m.cleanupMu.Unlock()
		return
	}
	close(m.cleanupCh)
	m.cleanupCh = nil
	m.cleanupMu.Unlock()
}

// cleanupLoop runs the periodic cleanup of idle connections
func (m *DbManager) cleanupLoop(interval time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.cleanupIdleConnections()
		case <-done:
			return
		}
	}
}

// cleanupIdleConnections removes connections that have been idle longer than idleTTL.
// Idle entries are detached from the manager's bookkeeping under the lock, then their
// Close() calls run outside the lock so a slow teardown cannot block concurrent Get()s.
func (m *DbManager) cleanupIdleConnections() {
	m.mu.Lock()

	now := time.Now()
	var toClose []*dbEntry

	// Detach idle connections from bookkeeping under the lock. A still-leased idle
	// connection is detached but its Close() is deferred to the final lease release,
	// so an in-use connection is never closed (the #606 race).
	for key, entry := range m.conns {
		if now.Sub(entry.lastUsed) <= m.idleTTL {
			continue
		}
		delete(m.conns, key)
		m.lru.Remove(entry.element)
		entry.detached = true

		m.logger.Debug().
			Str("key", key).
			Dur("idle_time", now.Sub(entry.lastUsed)).
			Msg("Cleaned up idle database connection")

		if entry.refs > 0 {
			continue // still leased — defer close to release
		}
		entry.closed = true
		toClose = append(toClose, entry)
	}
	m.mu.Unlock()

	// Close detached connections outside the lock.
	for _, entry := range toClose {
		m.closeEvicted(entry, "Error closing idle database connection")
	}
}

// Close closes all database connections and stops cleanup
func (m *DbManager) Close() error {
	m.StopCleanup()

	m.mu.Lock()
	defer m.mu.Unlock()

	var errors []error

	for key, entry := range m.conns {
		if err := entry.conn.Close(); err != nil {
			errors = append(errors, fmt.Errorf("error closing connection for key %s: %w", key, err))
		}
	}

	// Clear the cache
	m.conns = make(map[string]*dbEntry)
	m.lru.Init()

	if len(errors) > 0 {
		return fmt.Errorf("errors closing database connections: %v", errors)
	}

	return nil
}

// Size returns the number of active connections
func (m *DbManager) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.conns)
}

// Stats returns statistics about the connection pool
func (m *DbManager) Stats() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make(map[string]any)
	stats["active_connections"] = len(m.conns)
	stats["max_connections"] = m.maxSize
	stats["idle_ttl_seconds"] = int(m.idleTTL.Seconds())

	// Connection details
	connections := make([]map[string]any, 0, len(m.conns))
	now := time.Now()

	for key, entry := range m.conns {
		connStats := map[string]any{
			"key":           key,
			"last_used":     entry.lastUsed.Format(time.RFC3339),
			"idle_duration": int(now.Sub(entry.lastUsed).Seconds()),
		}
		connections = append(connections, connStats)
	}

	stats["connections"] = connections
	return stats
}
