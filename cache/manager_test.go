package cache_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	tenantOne   = "tenant-1"
	tenantTwo   = "tenant-2"
	tenantThree = "tenant-3"
)

// mockCache implements cache.Cache for testing.
type mockCache struct {
	id     string
	closed atomic.Bool
}

func newMockCache(id string) *mockCache {
	return &mockCache{id: id}
}

// trackableMockCache wraps mockCache and tracks closures.
type trackableMockCache struct {
	*mockCache
	onClose func(string)
}

func newTrackableMockCache(id string, onClose func(string)) *trackableMockCache {
	return &trackableMockCache{
		mockCache: newMockCache(id),
		onClose:   onClose,
	}
}

func (t *trackableMockCache) Close() error {
	err := t.mockCache.Close()
	if err == nil && t.onClose != nil {
		t.onClose(t.id)
	}
	return err
}

func (m *mockCache) Get(ctx context.Context, key string) ([]byte, error) {
	if m.closed.Load() {
		return nil, cache.ErrClosed
	}
	return []byte(fmt.Sprintf("%s:%s", m.id, key)), nil
}

func (m *mockCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if m.closed.Load() {
		return cache.ErrClosed
	}
	return nil
}

func (m *mockCache) GetOrSet(ctx context.Context, key string, value []byte, ttl time.Duration) ([]byte, bool, error) {
	if m.closed.Load() {
		return nil, false, cache.ErrClosed
	}
	return value, true, nil
}

func (m *mockCache) CompareAndSet(ctx context.Context, key string, expected, new []byte, ttl time.Duration) (bool, error) {
	if m.closed.Load() {
		return false, cache.ErrClosed
	}
	return true, nil
}

func (m *mockCache) Delete(ctx context.Context, key string) error {
	if m.closed.Load() {
		return cache.ErrClosed
	}
	return nil
}

func (m *mockCache) Health(ctx context.Context) error {
	if m.closed.Load() {
		return cache.ErrClosed
	}
	return nil
}

func (m *mockCache) Stats() (map[string]any, error) {
	if m.closed.Load() {
		return nil, cache.ErrClosed
	}
	return map[string]any{"id": m.id}, nil
}

func (m *mockCache) Close() error {
	if !m.closed.CompareAndSwap(false, true) {
		return cache.ErrClosed
	}
	return nil
}

// TestNewCacheManager tests manager creation with various configs.
func TestNewCacheManager(t *testing.T) {
	t.Run("ValidConfig", func(t *testing.T) {
		connector := func(ctx context.Context, key string) (cache.Cache, error) {
			return newMockCache(key), nil
		}

		mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
		require.NoError(t, err)
		require.NotNil(t, mgr)
		defer mgr.Close()

		stats := mgr.Stats()
		assert.Equal(t, 0, stats["active_caches"])
		assert.Equal(t, 100, stats["max_size"])
		assert.Equal(t, (15 * time.Minute).String(), stats["idle_ttl"])
	})

	t.Run("NilConnector", func(t *testing.T) {
		mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), nil)
		assert.Error(t, err)
		assert.Nil(t, mgr)
		assert.Contains(t, err.Error(), "connector function is required")
	})

	t.Run("NegativeMaxSize", func(t *testing.T) {
		connector := func(ctx context.Context, key string) (cache.Cache, error) {
			return newMockCache(key), nil
		}

		config := cache.DefaultManagerConfig()
		config.MaxSize = -1

		mgr, err := cache.NewCacheManager(config, connector)
		assert.Error(t, err)
		assert.Nil(t, mgr)
		assert.Contains(t, err.Error(), "max_size cannot be negative")
	})

	t.Run("NegativeIdleTTL", func(t *testing.T) {
		connector := func(ctx context.Context, key string) (cache.Cache, error) {
			return newMockCache(key), nil
		}

		config := cache.DefaultManagerConfig()
		config.IdleTTL = -1 * time.Second

		mgr, err := cache.NewCacheManager(config, connector)
		assert.Error(t, err)
		assert.Nil(t, mgr)
		assert.Contains(t, err.Error(), "idle_ttl cannot be negative")
	})

	t.Run("ZeroMaxSize_Unlimited", func(t *testing.T) {
		connector := func(ctx context.Context, key string) (cache.Cache, error) {
			return newMockCache(key), nil
		}

		config := cache.DefaultManagerConfig()
		config.MaxSize = 0 // Unlimited

		mgr, err := cache.NewCacheManager(config, connector)
		require.NoError(t, err)
		require.NotNil(t, mgr)
		defer mgr.Close()

		stats := mgr.Stats()
		assert.Equal(t, 0, stats["max_size"])
	})
}

// TestCacheManagerGet tests lazy initialization and retrieval.
func TestCacheManagerGet(t *testing.T) {
	t.Run("LazyInitialization", func(t *testing.T) {
		var creationCount atomic.Int32
		connector := func(ctx context.Context, key string) (cache.Cache, error) {
			creationCount.Add(1)
			return newMockCache(key), nil
		}

		mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
		require.NoError(t, err)
		defer mgr.Close()

		ctx := context.Background()

		// First call creates cache
		c1, err := mgr.Get(ctx, tenantOne)
		require.NoError(t, err)
		require.NotNil(t, c1)
		assert.Equal(t, int32(1), creationCount.Load())

		// Second call reuses existing cache
		c2, err := mgr.Get(ctx, tenantOne)
		require.NoError(t, err)
		assert.Same(t, c1, c2)
		assert.Equal(t, int32(1), creationCount.Load()) // No new creation

		stats := mgr.Stats()
		assert.Equal(t, 1, stats["active_caches"])
		assert.Equal(t, 1, stats["total_created"])
	})

	t.Run("MultipleTenants", func(t *testing.T) {
		connector := func(ctx context.Context, key string) (cache.Cache, error) {
			return newMockCache(key), nil
		}

		mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
		require.NoError(t, err)
		defer mgr.Close()

		ctx := context.Background()

		// Create caches for different tenants
		c1, err := mgr.Get(ctx, tenantOne)
		require.NoError(t, err)

		c2, err := mgr.Get(ctx, tenantTwo)
		require.NoError(t, err)

		c3, err := mgr.Get(ctx, tenantThree)
		require.NoError(t, err)

		// Ensure all are different instances
		assert.NotSame(t, c1, c2)
		assert.NotSame(t, c2, c3)
		assert.NotSame(t, c1, c3)

		stats := mgr.Stats()
		assert.Equal(t, 3, stats["active_caches"])
		assert.Equal(t, 3, stats["total_created"])
	})

	t.Run("ConnectorError", func(t *testing.T) {
		expectedErr := errors.New("connection failed")
		connector := func(ctx context.Context, key string) (cache.Cache, error) {
			return nil, expectedErr
		}

		mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
		require.NoError(t, err)
		defer mgr.Close()

		ctx := context.Background()
		c, err := mgr.Get(ctx, tenantOne)
		assert.Error(t, err)
		assert.Nil(t, c)
		assert.Contains(t, err.Error(), "failed to create cache")
		assert.Contains(t, err.Error(), "connection failed")

		stats := mgr.Stats()
		assert.Equal(t, 0, stats["active_caches"])
		assert.Equal(t, 1, stats["errors"])
	})
}

// TestCacheManagerSingleflight tests concurrent requests for same key.
func TestCacheManagerSingleflight(t *testing.T) {
	var creationCount atomic.Int32
	var creationInProgress atomic.Bool

	connector := func(ctx context.Context, key string) (cache.Cache, error) {
		// Detect concurrent calls (should never happen due to singleflight)
		if !creationInProgress.CompareAndSwap(false, true) {
			t.Error("concurrent cache creation detected - singleflight failed")
		}
		defer creationInProgress.Store(false)

		creationCount.Add(1)
		time.Sleep(50 * time.Millisecond) // Simulate slow creation
		return newMockCache(key), nil
	}

	mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
	require.NoError(t, err)
	defer mgr.Close()

	ctx := context.Background()
	workers := 10

	// Channel to collect results
	type result struct {
		cache cache.Cache
		err   error
	}
	results := make(chan result, workers)

	// Spawn concurrent requests for same tenant
	for i := 0; i < workers; i++ {
		go func() {
			c, err := mgr.Get(ctx, tenantOne)
			results <- result{cache: c, err: err}
		}()
	}

	// Collect results
	var caches []cache.Cache
	for i := 0; i < workers; i++ {
		res := <-results
		require.NoError(t, res.err)
		caches = append(caches, res.cache)
	}

	// All goroutines should receive the same cache instance
	for i := 1; i < len(caches); i++ {
		assert.Same(t, caches[0], caches[i], "all concurrent requests should receive same cache instance")
	}

	// Only one creation should have occurred
	assert.Equal(t, int32(1), creationCount.Load(), "singleflight should prevent duplicate creation")

	stats := mgr.Stats()
	assert.Equal(t, 1, stats["active_caches"])
	assert.Equal(t, 1, stats["total_created"])
}

// TestCacheManagerLRUEviction tests eviction when at capacity.
func TestCacheManagerLRUEviction(t *testing.T) {
	var closedCaches sync.Map // Track which caches were closed

	connector := func(ctx context.Context, key string) (cache.Cache, error) {
		return newTrackableMockCache(key, func(id string) {
			closedCaches.Store(id, true)
		}), nil
	}

	config := cache.DefaultManagerConfig()
	config.MaxSize = 3 // Small capacity to test eviction

	mgr, err := cache.NewCacheManager(config, connector)
	require.NoError(t, err)
	defer mgr.Close()

	ctx := context.Background()

	// Fill to capacity
	c1, _ := mgr.Get(ctx, tenantOne)
	c2, _ := mgr.Get(ctx, tenantTwo)
	_, _ = mgr.Get(ctx, tenantThree)

	stats := mgr.Stats()
	assert.Equal(t, 3, stats["active_caches"])
	assert.Equal(t, 0, stats["evictions"])

	// Access tenant-1 and tenant-2 to refresh LRU (tenant-3 becomes oldest)
	_, _ = mgr.Get(ctx, tenantOne)
	_, _ = mgr.Get(ctx, tenantTwo)

	// Add tenant-4, should evict tenant-3 (oldest)
	c4, err := mgr.Get(ctx, "tenant-4")
	require.NoError(t, err)
	require.NotNil(t, c4)

	stats = mgr.Stats()
	assert.Equal(t, 3, stats["active_caches"])
	assert.Equal(t, 1, stats["evictions"])

	// Verify tenant-3 was closed
	_, closed := closedCaches.Load(tenantThree)
	assert.True(t, closed, "tenant-3 should have been evicted and closed")

	// Verify tenant-1, tenant-2, tenant-4 still active
	c1Again, _ := mgr.Get(ctx, tenantOne)
	assert.Same(t, c1, c1Again, "tenant-1 should still be active")

	c2Again, _ := mgr.Get(ctx, tenantTwo)
	assert.Same(t, c2, c2Again, "tenant-2 should still be active")

	c4Again, _ := mgr.Get(ctx, "tenant-4")
	assert.Same(t, c4, c4Again, "tenant-4 should still be active")
}

// TestCacheManagerIdleCleanup tests automatic cleanup of idle caches.
func TestCacheManagerIdleCleanup(t *testing.T) {
	connector := func(ctx context.Context, key string) (cache.Cache, error) {
		return newMockCache(key), nil
	}

	config := cache.DefaultManagerConfig()
	config.IdleTTL = 100 * time.Millisecond
	config.CleanupInterval = 50 * time.Millisecond

	mgr, err := cache.NewCacheManager(config, connector)
	require.NoError(t, err)
	defer mgr.Close()

	ctx := context.Background()

	// Create caches
	c1, _ := mgr.Get(ctx, tenantOne)
	c2, _ := mgr.Get(ctx, tenantTwo)
	require.NotNil(t, c1)
	require.NotNil(t, c2)

	stats := mgr.Stats()
	assert.Equal(t, 2, stats["active_caches"])

	// Keep tenant-1 active by accessing it
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				_, _ = mgr.Get(ctx, tenantOne)
			case <-done:
				return
			}
		}
	}()

	// Wait for cleanup to occur (should remove tenant-2, keep tenant-1)
	time.Sleep(300 * time.Millisecond)
	close(done)

	stats = mgr.Stats()
	assert.Equal(t, 1, stats["active_caches"], "only tenant-1 should remain active")
	assert.Equal(t, 1, stats["idle_cleanups"], "tenant-2 should have been cleaned up")

	// Verify tenant-1 still accessible
	c1Again, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	assert.Same(t, c1, c1Again)

	// Verify tenant-2 was removed (new instance created)
	c2Again, err := mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)
	assert.NotSame(t, c2, c2Again, "tenant-2 should be a new instance after cleanup")
}

// TestCacheManagerRemove tests explicit cache removal.
func TestCacheManagerRemove(t *testing.T) {
	connector := func(ctx context.Context, key string) (cache.Cache, error) {
		return newMockCache(key), nil
	}

	mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
	require.NoError(t, err)
	defer mgr.Close()

	ctx := context.Background()

	// Create cache
	c1, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	require.NotNil(t, c1)

	stats := mgr.Stats()
	assert.Equal(t, 1, stats["active_caches"])

	// Remove cache
	err = mgr.Remove(tenantOne)
	require.NoError(t, err)

	stats = mgr.Stats()
	assert.Equal(t, 0, stats["active_caches"])

	// Verify cache was closed
	mock := c1.(*mockCache)
	assert.True(t, mock.closed.Load())

	// Get should create new instance
	c2, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	assert.NotSame(t, c1, c2, "should create new cache after removal")

	stats = mgr.Stats()
	assert.Equal(t, 1, stats["active_caches"])
	assert.Equal(t, 2, stats["total_created"])
}

// TestCacheManagerRemoveNonexistent tests removing a cache that doesn't exist.
func TestCacheManagerRemoveNonexistent(t *testing.T) {
	connector := func(ctx context.Context, key string) (cache.Cache, error) {
		return newMockCache(key), nil
	}

	mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
	require.NoError(t, err)
	defer mgr.Close()

	// Remove nonexistent cache (should not error)
	err = mgr.Remove("nonexistent")
	assert.NoError(t, err)
}

// TestCacheManagerClose tests manager shutdown.
func TestCacheManagerClose(t *testing.T) {
	var closedCaches sync.Map

	connector := func(ctx context.Context, key string) (cache.Cache, error) {
		return newTrackableMockCache(key, func(id string) {
			closedCaches.Store(id, true)
		}), nil
	}

	mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
	require.NoError(t, err)

	ctx := context.Background()

	// Create multiple caches
	_, _ = mgr.Get(ctx, tenantOne)
	_, _ = mgr.Get(ctx, tenantTwo)
	_, _ = mgr.Get(ctx, tenantThree)

	stats := mgr.Stats()
	assert.Equal(t, 3, stats["active_caches"])

	// Close manager
	err = mgr.Close()
	require.NoError(t, err)

	// Verify all caches were closed
	_, closed1 := closedCaches.Load(tenantOne)
	_, closed2 := closedCaches.Load(tenantTwo)
	_, closed3 := closedCaches.Load(tenantThree)
	assert.True(t, closed1)
	assert.True(t, closed2)
	assert.True(t, closed3)

	// Stats should reflect empty manager
	stats = mgr.Stats()
	assert.Equal(t, 0, stats["active_caches"])

	// Close should be idempotent
	err = mgr.Close()
	assert.NoError(t, err)
}

// TestCacheManagerStats tests statistics reporting.
func TestCacheManagerStats(t *testing.T) {
	connector := func(ctx context.Context, key string) (cache.Cache, error) {
		return newMockCache(key), nil
	}

	config := cache.DefaultManagerConfig()
	config.MaxSize = 2

	mgr, err := cache.NewCacheManager(config, connector)
	require.NoError(t, err)
	defer mgr.Close()

	ctx := context.Background()

	// Initial state
	stats := mgr.Stats()
	assert.Equal(t, 0, stats["active_caches"])
	assert.Equal(t, 0, stats["total_created"])
	assert.Equal(t, 0, stats["evictions"])
	assert.Equal(t, 0, stats["idle_cleanups"])
	assert.Equal(t, 0, stats["errors"])
	assert.Equal(t, 2, stats["max_size"])
	assert.Equal(t, (15 * time.Minute).String(), stats["idle_ttl"])

	// Create caches
	_, _ = mgr.Get(ctx, tenantOne)
	_, _ = mgr.Get(ctx, tenantTwo)

	stats = mgr.Stats()
	assert.Equal(t, 2, stats["active_caches"])
	assert.Equal(t, 2, stats["total_created"])

	// Trigger eviction
	_, _ = mgr.Get(ctx, tenantThree)

	stats = mgr.Stats()
	assert.Equal(t, 2, stats["active_caches"])
	assert.Equal(t, 3, stats["total_created"])
	assert.Equal(t, 1, stats["evictions"])
}

// TestCacheManagerThreadSafety tests concurrent access to manager.
func TestCacheManagerThreadSafety(t *testing.T) {
	connector := func(ctx context.Context, key string) (cache.Cache, error) {
		time.Sleep(10 * time.Millisecond) // Simulate slow creation
		return newMockCache(key), nil
	}

	mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
	require.NoError(t, err)
	defer mgr.Close()

	ctx := context.Background()
	workers := 20
	operations := 50

	var wg sync.WaitGroup
	wg.Add(workers)

	// Spawn workers performing concurrent operations
	for i := 0; i < workers; i++ {
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < operations; j++ {
				tenantID := fmt.Sprintf("tenant-%d", j%5) // 5 different tenants

				// Get cache
				c, err := mgr.Get(ctx, tenantID)
				if err != nil {
					t.Errorf("worker %d: Get failed: %v", workerID, err)
					continue
				}

				// Use cache
				_, err = c.Get(ctx, "test-key")
				if err != nil && err != cache.ErrClosed {
					t.Errorf("worker %d: cache operation failed: %v", workerID, err)
				}

				// Occasionally remove cache
				if j%20 == 0 {
					_ = mgr.Remove(tenantID)
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify stats are consistent
	stats := mgr.Stats()
	totalCreated := stats["total_created"].(int)
	activeCaches := stats["active_caches"].(int)
	evictions := stats["evictions"].(int)
	idleCleanups := stats["idle_cleanups"].(int)

	assert.GreaterOrEqual(t, totalCreated, activeCaches)
	assert.GreaterOrEqual(t, totalCreated, evictions+idleCleanups)
}
