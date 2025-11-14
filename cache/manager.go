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
	ActiveCaches int           // Current number of active cache instances
	TotalCreated int           // Total caches created since manager start
	Evictions    int           // Total evictions due to LRU policy
	IdleCleanups int           // Total cleanups due to idle timeout
	Errors       int           // Total initialization errors
	MaxSize      int           // Maximum allowed active caches
	IdleTTL      time.Duration // Idle timeout duration
}

// CacheConnector is a function that creates a new cache instance for a given key.
// This abstraction allows dependency injection for testing.
type CacheConnector func(ctx context.Context, key string) (Cache, error)

// cacheEntry represents a cache instance in the manager's LRU.
type cacheEntry struct {
	cache    Cache
	key      string
	lastUsed time.Time
	element  *list.Element // Position in LRU list
}

// CacheManager implements the Manager interface.
type CacheManager struct {
	mu     sync.RWMutex
	caches map[string]*cacheEntry
	lru    *list.List
	sfg    singleflight.Group

	maxSize   int
	idleTTL   time.Duration
	connector CacheConnector

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
func NewCacheManager(config ManagerConfig, connector CacheConnector) (*CacheManager, error) {
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
	defer m.mu.Unlock()

	// Evict LRU cache if at capacity
	m.evictIfNeeded()

	// Add to LRU front (most recently used)
	entry := &cacheEntry{
		cache:    cache,
		key:      key,
		lastUsed: time.Now(),
	}
	entry.element = m.lru.PushFront(entry)
	m.caches[key] = entry
	m.totalCreated++

	return cache, nil
}

// evictIfNeeded removes the least recently used cache if at capacity.
// Must be called with mu lock held.
func (m *CacheManager) evictIfNeeded() {
	if m.maxSize <= 0 || len(m.caches) < m.maxSize {
		return
	}

	// Remove oldest entry (back of LRU list)
	oldest := m.lru.Back()
	if oldest == nil {
		return
	}

	entry := oldest.Value.(*cacheEntry)
	m.removeLocked(entry.key)
	m.evictions++
}

// Remove explicitly removes a cache instance from the manager.
func (m *CacheManager) Remove(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.removeLocked(key)
}

// removeLocked removes a cache instance (must be called with mu lock held).
func (m *CacheManager) removeLocked(key string) error {
	entry, exists := m.caches[key]
	if !exists {
		return nil // Already removed
	}

	// Close the cache instance
	if err := entry.cache.Close(); err != nil {
		return fmt.Errorf("failed to close cache %q: %w", key, err)
	}

	// Remove from tracking structures
	m.lru.Remove(entry.element)
	delete(m.caches, key)

	return nil
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

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var toRemove []string

	for key, entry := range m.caches {
		if now.Sub(entry.lastUsed) > m.idleTTL {
			toRemove = append(toRemove, key)
		}
	}

	for _, key := range toRemove {
		_ = m.removeLocked(key)
		m.idleCleanups++
	}
}

// Close shuts down all managed cache instances and stops the cleanup goroutine.
func (m *CacheManager) Close() error {
	var closeErr error

	m.closeOnce.Do(func() {
		// Signal cleanup goroutine to stop
		close(m.closeCh)

		m.mu.Lock()
		defer m.mu.Unlock()

		// Close all cache instances
		for key := range m.caches {
			if err := m.removeLocked(key); err != nil && closeErr == nil {
				closeErr = err
			}
		}
	})

	return closeErr
}

// Stats returns current manager statistics.
func (m *CacheManager) Stats() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return map[string]any{
		"active_caches": len(m.caches),
		"total_created": m.totalCreated,
		"evictions":     m.evictions,
		"idle_cleanups": m.idleCleanups,
		"errors":        m.errors,
		"max_size":      m.maxSize,
		"idle_ttl":      m.idleTTL.String(),
	}
}
