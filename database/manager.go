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

// TenantStore provides per-key database configurations.
// This interface abstracts where tenant-specific database configs come from.
type TenantStore interface {
	// DBConfig returns the database configuration for the given key.
	// For single-tenant apps, key will be "". For multi-tenant, key will be the tenant ID.
	DBConfig(ctx context.Context, key string) (*config.DatabaseConfig, error)
}

// Connector creates database connections from configuration
type Connector func(*config.DatabaseConfig, logger.Logger) (Interface, error)

// DbManager manages database connections by string keys.
// It provides lazy initialization, LRU eviction, and cleanup for database connections.
// The manager is key-agnostic - it doesn't know about tenants, just manages named connections.
type DbManager struct {
	logger         logger.Logger
	resourceSource TenantStore
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

// dbEntry represents a database connection with metadata
type dbEntry struct {
	conn     Interface
	element  *list.Element // for LRU
	lastUsed time.Time
	key      string
}

// DbManagerOptions configures the DbManager
type DbManagerOptions struct {
	MaxSize int           // Maximum number of connections to keep (0 = no limit)
	IdleTTL time.Duration // Time after which idle connections are cleaned up (0 = no cleanup)
}

// NewDbManager creates a new database manager
func NewDbManager(resourceSource TenantStore, log logger.Logger, opts DbManagerOptions, connector Connector) *DbManager {
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

// Get returns a database connection for the given key.
// For single-tenant, use key "". For multi-tenant, use the tenant ID.
// Connections are created lazily and cached with LRU eviction.
func (m *DbManager) Get(ctx context.Context, key string) (Interface, error) {
	// Try to get existing connection first (fast path)
	if conn := m.getExisting(key); conn != nil {
		return conn, nil
	}

	// Use singleflight to prevent thundering herd on connection creation
	result, err, _ := m.sfg.Do(key, func() (interface{}, error) {
		// Double-check after acquiring singleflight lock
		if conn := m.getExisting(key); conn != nil {
			return conn, nil
		}

		return m.createConnection(ctx, key)
	})

	if err != nil {
		return nil, err
	}

	return result.(Interface), nil
}

// getExisting returns an existing connection and updates LRU, or nil if not found
func (m *DbManager) getExisting(key string) Interface {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, exists := m.conns[key]
	if !exists {
		return nil
	}

	// Update LRU and last used time
	entry.lastUsed = time.Now()
	m.lru.MoveToFront(entry.element)

	return entry.conn
}

// createConnection creates a new database connection for the given key
func (m *DbManager) createConnection(ctx context.Context, key string) (Interface, error) {
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
	defer m.mu.Unlock()

	// Check if connection was created by another goroutine while we were waiting
	if existing, exists := m.conns[key]; exists {
		// Close our new connection and return the existing one
		conn.Close()
		existing.lastUsed = time.Now()
		m.lru.MoveToFront(existing.element)
		return existing.conn, nil
	}

	// Ensure we don't exceed max size
	m.evictIfNeeded()

	// Add to cache
	element := m.lru.PushFront(key)
	entry := &dbEntry{
		conn:     conn,
		element:  element,
		lastUsed: time.Now(),
		key:      key,
	}
	m.conns[key] = entry

	m.logger.Info().
		Str("key", key).
		Str("db_type", dbConfig.Type).
		Msg("Created new database connection")

	return conn, nil
}

// evictIfNeeded removes the least recently used connection if at capacity
func (m *DbManager) evictIfNeeded() {
	if len(m.conns) < m.maxSize {
		return
	}

	// Remove the least recently used connection
	oldest := m.lru.Back()
	if oldest == nil {
		return
	}

	key := oldest.Value.(string)
	entry := m.conns[key]

	// Close the connection
	if err := entry.conn.Close(); err != nil {
		m.logger.Error().
			Err(err).
			Str("key", key).
			Msg("Error closing evicted database connection")
	}

	// Remove from cache
	delete(m.conns, key)
	m.lru.Remove(oldest)

	m.logger.Debug().
		Str("key", key).
		Msg("Evicted database connection due to LRU limit")
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

// cleanupIdleConnections removes connections that have been idle longer than idleTTL
func (m *DbManager) cleanupIdleConnections() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var toRemove []string

	// Find connections to remove
	for key, entry := range m.conns {
		if now.Sub(entry.lastUsed) > m.idleTTL {
			toRemove = append(toRemove, key)
		}
	}

	// Remove idle connections
	for _, key := range toRemove {
		entry := m.conns[key]

		// Close the connection
		if err := entry.conn.Close(); err != nil {
			m.logger.Error().
				Err(err).
				Str("key", key).
				Msg("Error closing idle database connection")
		}

		// Remove from cache
		delete(m.conns, key)
		m.lru.Remove(entry.element)

		m.logger.Debug().
			Str("key", key).
			Dur("idle_time", now.Sub(entry.lastUsed)).
			Msg("Cleaned up idle database connection")
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
