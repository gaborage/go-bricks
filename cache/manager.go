package cache

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

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

// cacheEntry represents a cache instance in the manager's LRU.
type cacheEntry struct {
	cache    Cache
	key      string
	lastUsed time.Time
	element  *list.Element // Position in LRU list
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
func NewCacheManager(config ManagerConfig, connector Connector) (*CacheManager, error) {
	if connector == nil {
		return nil, fmt.Errorf("connector function is required")
	}

	if config.MaxSize < 0 {
		return nil, fmt.Errorf("max_size cannot be negative")
	}

	if config.IdleTTL < 0 {
		return nil, fmt.Errorf("idle_ttl cannot be negative")
	}

	if config.CleanupInterval <= 0 {
		config.CleanupInterval = 5 * time.Minute
	}

	m := &CacheManager{
		caches:    make(map[string]*cacheEntry),
		lru:       list.New(),
		maxSize:   config.MaxSize,
		idleTTL:   config.IdleTTL,
		connector: connector,
		closeCh:   make(chan struct{}),
	}

	// Start background cleanup goroutine if idle TTL is configured
	if config.IdleTTL > 0 {
		go m.cleanupLoop(config.CleanupInterval)
	}

	return m, nil
}

// Get retrieves or creates a cache instance for the given key.
func (m *CacheManager) Get(ctx context.Context, key string) (Cache, error) {
	// Fast path: check if cache already exists
	if cache := m.getExisting(key); cache != nil {
		return cache, nil
	}

	// Slow path: use singleflight to prevent thundering herd
	result, err, _ := m.sfg.Do(key, func() (any, error) {
		// Double-check after acquiring singleflight lock
		if cache := m.getExisting(key); cache != nil {
			return cache, nil
		}

		// Create new cache instance
		return m.createCache(ctx, key)
	})

	if err != nil {
		m.mu.Lock()
		m.errors++
		m.mu.Unlock()
		return nil, err
	}

	return result.(Cache), nil
}

// getExisting retrieves an existing cache and updates LRU position.
func (m *CacheManager) getExisting(key string) Cache {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, exists := m.caches[key]
	if !exists {
		return nil
	}

	// Update LRU position (move to front)
	m.lru.MoveToFront(entry.element)
	entry.lastUsed = time.Now()

	return entry.cache
}

// createCache creates a new cache instance and adds it to the manager.
func (m *CacheManager) createCache(ctx context.Context, key string) (Cache, error) {
	// Create cache using connector function
	cache, err := m.connector(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cache for key %q: %w", key, err)
	}

	m.mu.Lock()
	// Evict LRU cache if at capacity (returns entry to close outside lock)
	evicted := m.evictIfNeeded()

	// Add to LRU front (most recently used)
	entry := &cacheEntry{
		cache:    cache,
		key:      key,
		lastUsed: time.Now(),
	}
	entry.element = m.lru.PushFront(entry)
	m.caches[key] = entry
	m.totalCreated++
	m.mu.Unlock()

	// Close evicted cache outside the lock (ignore errors during eviction)
	if evicted != nil {
		_ = evicted.cache.Close()
	}

	return cache, nil
}

// evictIfNeeded removes the least recently used cache if at capacity.
// Must be called with mu lock held. Returns the evicted entry (caller must close).
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
	evicted := m.removeEntryLocked(entry.key)
	m.evictions++
	return evicted
}

// Remove explicitly removes a cache instance from the manager.
func (m *CacheManager) Remove(key string) error {
	m.mu.Lock()
	entry := m.removeEntryLocked(key)
	m.mu.Unlock()

	if entry == nil {
		return nil // Already removed
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
				toClose = append(toClose, removed)
				m.idleCleanups++
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
func (m *CacheManager) Close() error {
	var closeErr error

	m.closeOnce.Do(func() {
		// Signal cleanup goroutine to stop
		close(m.closeCh)

		// Collect all cache entries under lock
		m.mu.Lock()
		var toClose []*cacheEntry
		for key := range m.caches {
			if entry := m.removeEntryLocked(key); entry != nil {
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
