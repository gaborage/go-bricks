package cache

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gaborage/go-bricks/config"
	"golang.org/x/sync/singleflight"
)

// ConfigProvider provides per-key cache configurations.
// This interface abstracts where tenant-specific cache configs come from.
type ConfigProvider interface {
	// CacheConfig returns the cache configuration for the given key.
	// For single-tenant apps, key will be "". For multi-tenant, key will be the tenant ID.
	CacheConfig(ctx context.Context, key string) (*config.CacheConfig, error)
}

// ManagerStats provides metrics about the cache manager's state.
type ManagerStats struct {
	ActiveCaches int   // Current number of active cache instances
	TotalCreated int   // Total caches created since manager start
	Evictions    int   // Total evictions due to LRU policy
	IdleCleanups int   // Total cleanups due to idle timeout
	Errors       int   // Total initialization and close errors
	MaxSize      int   // Maximum allowed active caches
	IdleTTL      int64 // Idle timeout duration in seconds
}

// Connector is a function that creates a new cache instance for a given key.
// This abstraction allows dependency injection for testing.
type Connector func(ctx context.Context, key string) (Cache, error)

// ReleaseFunc releases a lease obtained from Get. Callers must invoke it (typically
// deferred) when finished with the cache for the current unit of work. It is idempotent.
// Release does NOT close the shared cache instance; it signals this borrower is done, so
// a cache instance evicted while leased is closed only once its last lease is released.
// See ADR-032.
type ReleaseFunc func()

// maxGetAttempts bounds the rare retry where a freshly resolved entry is evicted before
// the caller can take a lease (only under extreme pool churn). A new entry is inserted at
// the LRU front, so in practice the first attempt always succeeds.
const maxGetAttempts = 4

// cacheEntry represents a cache instance in the manager's LRU.
// refs, detached, and closed are guarded by CacheManager.mu.
type cacheEntry struct {
	cache    Cache
	key      string
	lastUsed time.Time
	element  *list.Element // Position in LRU list

	// refs counts outstanding leases (current borrowers); a cache with refs > 0 is in use.
	refs int
	// seedHeld is true when one of refs is an unclaimed "seed" lease taken at creation. The
	// seed keeps a brand-new entry alive (refs >= 1) through the window before its first Get
	// caller claims it, so a concurrent evict/Remove can only detach (never close) it. The
	// first claimOrAcquire takes the seed; later callers increment refs normally.
	seedHeld bool
	// detached marks an entry removed from the map+LRU whose Close() was deferred because
	// a lease was still outstanding.
	detached bool
	// closed guards against a double Close() once the deferred close has run.
	closed bool
}

// CacheManager implements the Manager interface for multi-tenant cache instances.
//
//nolint:revive // CacheManager is intentional - Manager is the interface name
type CacheManager struct {
	mu     sync.RWMutex
	caches map[string]*cacheEntry
	lru    *list.List
	sfg    singleflight.Group

	maxSize   int
	idleTTL   time.Duration
	connector Connector

	// Statistics
	totalCreated int
	evictions    int
	idleCleanups int
	errors       int

	// Lifecycle
	closeCh   chan struct{}
	closeOnce sync.Once
	// closed flips to true the moment Close() begins. Read on the hot path of
	// Get/Remove so callers immediately see ErrManagerClosed instead of getting
	// a handle to a cache instance that is about to be torn down. Atomic so we
	// don't need to take m.mu on every operation just to consult shutdown state.
	closed atomic.Bool
}

// ManagerConfig configures the cache manager's behavior.
type ManagerConfig struct {
	MaxSize         int           // Maximum number of active cache instances (0 = unlimited)
	IdleTTL         time.Duration // Idle timeout for cache instances (0 = never timeout)
	CleanupInterval time.Duration // How often to run idle cleanup (default: 5m)
}

// DefaultManagerConfig returns sensible defaults for the cache manager.
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		MaxSize:         100,
		IdleTTL:         15 * time.Minute,
		CleanupInterval: 5 * time.Minute,
	}
}

// NewCacheManager creates a new cache manager with the given configuration.
// The connector function is called to create new cache instances on demand.
func NewCacheManager(cfg ManagerConfig, connector Connector) (*CacheManager, error) {
	if connector == nil {
		return nil, fmt.Errorf("connector function is required")
	}

	if cfg.MaxSize < 0 {
		return nil, fmt.Errorf("maxsize cannot be negative")
	}

	if cfg.IdleTTL < 0 {
		return nil, fmt.Errorf("idlettl cannot be negative")
	}

	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 5 * time.Minute
	}

	m := &CacheManager{
		caches:    make(map[string]*cacheEntry),
		lru:       list.New(),
		maxSize:   cfg.MaxSize,
		idleTTL:   cfg.IdleTTL,
		connector: connector,
		closeCh:   make(chan struct{}),
	}

	// Start background cleanup goroutine if idle TTL is configured
	if cfg.IdleTTL > 0 {
		go m.cleanupLoop(cfg.CleanupInterval)
	}

	return m, nil
}

// Get retrieves or creates a cache instance for the given key, plus a ReleaseFunc the
// caller must invoke when finished with it for the current unit of work (typically
// deferred). Returns ErrManagerClosed if Close() has been called. The lease prevents a
// cache instance evicted while in use from being closed under an active caller (the #606
// race). On error the returned ReleaseFunc is nil — check err first.
func (m *CacheManager) Get(ctx context.Context, key string) (Cache, ReleaseFunc, error) {
	for attempt := 0; attempt < maxGetAttempts; attempt++ {
		// Re-check on every iteration (not just once up front): a concurrent Close() must not
		// be raced into recreating an instance on a shut-down manager. createCache also
		// re-checks under the lock to close the window fully.
		if m.closed.Load() {
			return nil, nil, ErrManagerClosed
		}

		// Fast path: getExisting increments the refcount atomically with the lookup, so the
		// cache cannot be evicted-and-closed before the lease is taken.
		if entry := m.getExisting(key); entry != nil {
			return entry.cache, m.makeRelease(entry), nil
		}

		// Slow path: singleflight collapses concurrent creates for the same key into one. It
		// returns the shared entry (freshly created with a seed lease, or an existing one);
		// every caller then takes its own lease on that pointer via claimOrAcquire — the first
		// claims the seed, the rest increment — so each concurrent borrower is counted.
		v, err, _ := m.sfg.Do(key, func() (any, error) {
			if e := m.peek(key); e != nil {
				return e, nil
			}
			return m.createCache(ctx, key)
		})
		if err != nil {
			m.mu.Lock()
			m.errors++
			m.mu.Unlock()
			return nil, nil, err
		}

		entry := v.(*cacheEntry)
		if m.claimOrAcquire(entry) {
			return entry.cache, m.makeRelease(entry), nil
		}
		// The reused entry was closed in the window between lookup and claim (a concurrent
		// evict/Remove of an unleased entry); loop to create a fresh one. The create path
		// always succeeds because a new entry carries a seed lease, so this converges.
	}

	return nil, nil, fmt.Errorf("failed to acquire cache for key %q after %d attempts (pool churn)", key, maxGetAttempts)
}

// claimOrAcquire takes one lease on entry, operating on the shared pointer so it can never
// "miss" via a map lookup. It returns false only when the entry has already been fully closed
// (a reused entry that lost a race), signaling the caller to retry with a fresh entry.
func (m *CacheManager) claimOrAcquire(entry *cacheEntry) bool {
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

// getExisting retrieves an existing entry with a lease acquired (refcount incremented) and
// updates LRU position, or nil if not found. The refcount increment happens under the same
// lock as the lookup so the entry cannot be evicted-and-closed before the lease is taken.
func (m *CacheManager) getExisting(key string) *cacheEntry {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, exists := m.caches[key]
	if !exists {
		return nil
	}

	// Update LRU position (move to front)
	m.lru.MoveToFront(entry.element)
	entry.lastUsed = time.Now()
	entry.refs++

	return entry
}

// peek reports whether an entry exists for the key without taking a lease or touching LRU.
func (m *CacheManager) peek(key string) *cacheEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.caches[key]
}

// makeRelease returns an idempotent ReleaseFunc bound to a single lease on entry.
func (m *CacheManager) makeRelease(entry *cacheEntry) ReleaseFunc {
	var once sync.Once
	return func() {
		once.Do(func() { m.releaseEntry(entry) })
	}
}

// releaseEntry drops one lease. If the entry was detached (evicted/idle-cleaned) while
// leased and this was the final lease, it closes the cache now — outside the lock.
func (m *CacheManager) releaseEntry(entry *cacheEntry) {
	m.mu.Lock()
	entry.refs--
	shouldClose := entry.detached && entry.refs <= 0 && !entry.closed
	if shouldClose {
		entry.closed = true
	}
	m.mu.Unlock()

	if shouldClose {
		if err := entry.cache.Close(); err != nil {
			m.mu.Lock()
			m.errors++
			m.mu.Unlock()
		}
	}
}

// createCache creates a new cache instance and adds it to the manager with a single seed
// lease (refs == 1, seedHeld). The seed keeps the entry alive through the window before the
// caller claims it via claimOrAcquire, so a concurrent evict/Remove can only detach it.
func (m *CacheManager) createCache(ctx context.Context, key string) (*cacheEntry, error) {
	// Create cache using connector function
	cache, err := m.connector(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cache for key %q: %w", key, err)
	}

	m.mu.Lock()
	// If Close() ran between the caller's m.closed check and here, do not resurrect the
	// (cleared) map with a new instance that nothing would ever close. Close the just-created
	// instance and report the manager as closed.
	if m.closed.Load() {
		m.mu.Unlock()
		_ = cache.Close()
		return nil, ErrManagerClosed
	}

	// Evict LRU cache if at capacity (returns entry to close outside lock)
	evicted := m.evictIfNeeded()

	// Add to LRU front (most recently used) with a seed lease.
	entry := &cacheEntry{
		cache:    cache,
		key:      key,
		lastUsed: time.Now(),
		refs:     1,
		seedHeld: true,
	}
	entry.element = m.lru.PushFront(entry)
	m.caches[key] = entry
	m.totalCreated++
	m.mu.Unlock()

	// Close evicted cache outside the lock (ignore errors during eviction)
	if evicted != nil {
		_ = evicted.cache.Close()
	}

	return entry, nil
}

// evictIfNeeded removes the least recently used cache if at capacity. Must be called with
// mu held. It detaches the entry and returns it for the caller to close OUTSIDE the lock —
// but ONLY when the entry has no outstanding leases. If the LRU victim is still leased, its
// Close() is deferred to the final lease release (the #606 race). Returns nil when nothing
// should be closed now.
func (m *CacheManager) evictIfNeeded() *cacheEntry {
	if m.maxSize <= 0 || len(m.caches) < m.maxSize {
		return nil
	}

	// Remove oldest entry (back of LRU list)
	oldest := m.lru.Back()
	if oldest == nil {
		return nil
	}

	entry := oldest.Value.(*cacheEntry)
	m.removeEntryLocked(entry.key)
	m.evictions++

	if entry.refs > 0 {
		return nil // still leased — defer the close to the final lease release
	}
	entry.closed = true
	return entry
}

// Remove explicitly removes a cache instance from the manager.
// Returns ErrManagerClosed if Close() has been called.
func (m *CacheManager) Remove(key string) error {
	if m.closed.Load() {
		return ErrManagerClosed
	}
	m.mu.Lock()
	entry := m.removeEntryLocked(key)
	shouldClose := entry != nil && entry.refs <= 0 && !entry.closed
	if shouldClose {
		entry.closed = true
	}
	m.mu.Unlock()

	if entry == nil {
		return nil // Already removed
	}

	// A still-leased entry is detached now but its Close() is deferred to the final lease
	// release, so an in-use cache is never closed (the #606 race).
	if !shouldClose {
		return nil
	}

	// Close the cache instance outside the lock
	if err := entry.cache.Close(); err != nil {
		return fmt.Errorf("failed to close cache %q: %w", key, err)
	}

	return nil
}

// removeEntryLocked removes bookkeeping for a cache (must be called with mu lock held).
// Returns the removed entry or nil if not found.
// The caller is responsible for closing the returned entry's cache.
func (m *CacheManager) removeEntryLocked(key string) *cacheEntry {
	entry, exists := m.caches[key]
	if !exists {
		return nil
	}

	// Remove from tracking structures (always cleanup, even if close fails later)
	m.lru.Remove(entry.element)
	delete(m.caches, key)
	entry.detached = true

	return entry
}

// cleanupLoop periodically removes idle cache instances.
func (m *CacheManager) cleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.cleanupIdleCaches()
		case <-m.closeCh:
			return
		}
	}
}

// cleanupIdleCaches removes cache instances that have been idle beyond the TTL.
func (m *CacheManager) cleanupIdleCaches() {
	if m.idleTTL <= 0 {
		return
	}

	// Collect idle entries under lock
	m.mu.Lock()
	now := time.Now()
	var toClose []*cacheEntry

	for key, entry := range m.caches {
		if now.Sub(entry.lastUsed) > m.idleTTL {
			if removed := m.removeEntryLocked(key); removed != nil {
				m.idleCleanups++
				if removed.refs > 0 {
					continue // still leased — defer close to the final lease release
				}
				removed.closed = true
				toClose = append(toClose, removed)
			}
		}
	}
	m.mu.Unlock()

	// Close collected caches outside the lock
	for _, entry := range toClose {
		if err := entry.cache.Close(); err != nil {
			// Increment error counter on close failures
			m.mu.Lock()
			m.errors++
			m.mu.Unlock()
		}
	}
}

// Close shuts down all managed cache instances and stops the cleanup goroutine.
// After Close() returns, subsequent calls to Get() and Remove() return ErrManagerClosed.
func (m *CacheManager) Close() error {
	var closeErr error

	m.closeOnce.Do(func() {
		// Flip closed BEFORE doing any teardown work so concurrent Get/Remove
		// callers immediately see ErrManagerClosed rather than racing against
		// half-torn-down state (e.g. getting a cache instance that's already
		// in the toClose slice below). atomic.Bool is the right primitive here:
		// it's lock-free on the hot path of every Get, and the memory ordering
		// guarantees that any subsequent Load reads the final true value.
		m.closed.Store(true)

		// Signal cleanup goroutine to stop
		close(m.closeCh)

		// Collect all cache entries under lock. Mark each closed under the lock so a
		// concurrent lease release cannot also close it (avoids a double Close()).
		m.mu.Lock()
		var toClose []*cacheEntry
		for key := range m.caches {
			if entry := m.removeEntryLocked(key); entry != nil {
				entry.closed = true
				toClose = append(toClose, entry)
			}
		}
		m.mu.Unlock()

		// Close all caches outside the lock
		for _, entry := range toClose {
			if err := entry.cache.Close(); err != nil {
				// Track errors and return first error encountered
				m.mu.Lock()
				m.errors++
				m.mu.Unlock()

				if closeErr == nil {
					closeErr = err
				}
			}
		}
	})

	return closeErr
}

// Stats returns current manager statistics.
func (m *CacheManager) Stats() ManagerStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return ManagerStats{
		ActiveCaches: len(m.caches),
		TotalCreated: m.totalCreated,
		Evictions:    m.evictions,
		IdleCleanups: m.idleCleanups,
		Errors:       m.errors,
		MaxSize:      m.maxSize,
		IdleTTL:      int64(m.idleTTL.Seconds()),
	}
}
