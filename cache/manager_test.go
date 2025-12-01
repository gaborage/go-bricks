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
	tenantOne      = "tenant-1"
	tenantTwo      = "tenant-2"
	tenantThree    = "tenant-3"
	closeFailedMsg = "close failed"
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

func (m *mockCache) Get(_ context.Context, key string) ([]byte, error) {
	if m.closed.Load() {
		return nil, cache.ErrClosed
	}
	return []byte(fmt.Sprintf("%s:%s", m.id, key)), nil
}

func (m *mockCache) Set(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	if m.closed.Load() {
		return cache.ErrClosed
	}
	return nil
}

func (m *mockCache) GetOrSet(_ context.Context, _ string, value []byte, _ time.Duration) (storedValue []byte, wasSet bool, err error) {
	if m.closed.Load() {
		return nil, false, cache.ErrClosed
	}
	return value, true, nil
}

func (m *mockCache) CompareAndSet(_ context.Context, _ string, _, _ []byte, _ time.Duration) (bool, error) {
	if m.closed.Load() {
		return false, cache.ErrClosed
	}
	return true, nil
}

func (m *mockCache) Delete(_ context.Context, _ string) error {
	if m.closed.Load() {
		return cache.ErrClosed
	}
	return nil
}

func (m *mockCache) Health(_ context.Context) error {
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
		connector := func(_ context.Context, key string) (cache.Cache, error) {
			return newMockCache(key), nil
		}

		mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
		require.NoError(t, err)
		require.NotNil(t, mgr)
		defer mgr.Close()

		stats := mgr.Stats()
		assert.Equal(t, 0, stats.ActiveCaches)
		assert.Equal(t, 100, stats.MaxSize)
		assert.Equal(t, int64(900), stats.IdleTTL) // 15 minutes = 900 seconds
	})

	t.Run("NilConnector", func(t *testing.T) {
		mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), nil)
		assert.Error(t, err)
		assert.Nil(t, mgr)
		assert.Contains(t, err.Error(), "connector function is required")
	})

	t.Run("NegativeMaxSize", func(t *testing.T) {
		connector := func(_ context.Context, key string) (cache.Cache, error) {
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
		connector := func(_ context.Context, key string) (cache.Cache, error) {
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
		connector := func(_ context.Context, key string) (cache.Cache, error) {
			return newMockCache(key), nil
		}

		config := cache.DefaultManagerConfig()
		config.MaxSize = 0 // Unlimited

		mgr, err := cache.NewCacheManager(config, connector)
		require.NoError(t, err)
		require.NotNil(t, mgr)
		defer mgr.Close()

		stats := mgr.Stats()
		assert.Equal(t, 0, stats.MaxSize)
	})
}

// TestCacheManagerGet tests lazy initialization and retrieval.
func TestCacheManagerGet(t *testing.T) {
	t.Run("LazyInitialization", func(t *testing.T) {
		var creationCount atomic.Int32
		connector := func(_ context.Context, key string) (cache.Cache, error) {
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
		assert.Equal(t, 1, stats.ActiveCaches)
		assert.Equal(t, 1, stats.TotalCreated)
	})

	t.Run("MultipleTenants", func(t *testing.T) {
		connector := func(_ context.Context, key string) (cache.Cache, error) {
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
		assert.Equal(t, 3, stats.ActiveCaches)
		assert.Equal(t, 3, stats.TotalCreated)
	})

	t.Run("ConnectorError", func(t *testing.T) {
		expectedErr := errors.New("connection failed")
		connector := func(_ context.Context, _ string) (cache.Cache, error) {
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
		assert.Equal(t, 0, stats.ActiveCaches)
		assert.Equal(t, 1, stats.Errors)
	})
}

// TestCacheManagerSingleflight tests concurrent requests for same key.
func TestCacheManagerSingleflight(t *testing.T) {
	var creationCount atomic.Int32
	var creationInProgress atomic.Bool

	connector := func(_ context.Context, key string) (cache.Cache, error) {
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
	assert.Equal(t, 1, stats.ActiveCaches)
	assert.Equal(t, 1, stats.TotalCreated)
}

// TestCacheManagerLRUEviction tests eviction when at capacity.
func TestCacheManagerLRUEviction(t *testing.T) {
	var closedCaches sync.Map // Track which caches were closed

	connector := func(_ context.Context, key string) (cache.Cache, error) {
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
	c1, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	c2, err := mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)
	_, err = mgr.Get(ctx, tenantThree)
	require.NoError(t, err)

	stats := mgr.Stats()
	assert.Equal(t, 3, stats.ActiveCaches)
	assert.Equal(t, 0, stats.Evictions)

	// Access tenant-1 and tenant-2 to refresh LRU (tenant-3 becomes oldest)
	_, err = mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	_, err = mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)

	// Add tenant-4, should evict tenant-3 (oldest)
	c4, err := mgr.Get(ctx, "tenant-4")
	require.NoError(t, err)
	require.NotNil(t, c4)

	stats = mgr.Stats()
	assert.Equal(t, 3, stats.ActiveCaches)
	assert.Equal(t, 1, stats.Evictions)

	// Verify tenant-3 was closed
	_, closed := closedCaches.Load(tenantThree)
	assert.True(t, closed, "tenant-3 should have been evicted and closed")

	// Verify tenant-1, tenant-2, tenant-4 still active
	c1Again, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	assert.Same(t, c1, c1Again, "tenant-1 should still be active")

	c2Again, err := mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)
	assert.Same(t, c2, c2Again, "tenant-2 should still be active")

	c4Again, err := mgr.Get(ctx, "tenant-4")
	require.NoError(t, err)
	assert.Same(t, c4, c4Again, "tenant-4 should still be active")
}

// TestCacheManagerIdleCleanup tests automatic cleanup of idle caches.
func TestCacheManagerIdleCleanup(t *testing.T) {
	connector := func(_ context.Context, key string) (cache.Cache, error) {
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
	c1, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	c2, err := mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)
	require.NotNil(t, c1)
	require.NotNil(t, c2)

	stats := mgr.Stats()
	assert.Equal(t, 2, stats.ActiveCaches)

	// Keep tenant-1 active by accessing it
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				_, err := mgr.Get(ctx, tenantOne)
				if err != nil {
					return // Manager may be closed during test cleanup
				}
			case <-done:
				return
			}
		}
	}()

	// Wait for cleanup to occur (should remove tenant-2, keep tenant-1)
	time.Sleep(300 * time.Millisecond)
	close(done)

	stats = mgr.Stats()
	assert.Equal(t, 1, stats.ActiveCaches, "only tenant-1 should remain active")
	assert.Equal(t, 1, stats.IdleCleanups, "tenant-2 should have been cleaned up")

	// Verify tenant-1 still accessible
	c1Again, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	assert.Same(t, c1, c1Again)

	// Verify tenant-2 was removed (new instance created)
	c2Again, err := mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)
	assert.NotSame(t, c2, c2Again, "tenant-2 should be a new instance after cleanup")
}

// TestCacheManagerIdleCleanupWithCloseError tests that idle cleanup
// increments error counter when Close() fails, but still cleans up the entry.
func TestCacheManagerIdleCleanupWithCloseError(t *testing.T) {
	closeErr := errors.New(closeFailedMsg)
	connector := func(_ context.Context, key string) (cache.Cache, error) {
		return &failingCloseCache{
			mockCache: newMockCache(key),
			closeErr:  closeErr,
		}, nil
	}

	config := cache.DefaultManagerConfig()
	config.IdleTTL = 100 * time.Millisecond
	config.CleanupInterval = 50 * time.Millisecond

	mgr, err := cache.NewCacheManager(config, connector)
	require.NoError(t, err)
	defer mgr.Close()

	ctx := context.Background()

	// Create cache that will become idle
	c1, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	require.NotNil(t, c1)

	stats := mgr.Stats()
	assert.Equal(t, 1, stats.ActiveCaches)
	assert.Equal(t, 0, stats.Errors, "no errors initially")

	// Wait for cleanup to occur
	time.Sleep(300 * time.Millisecond)

	stats = mgr.Stats()
	assert.Equal(t, 0, stats.ActiveCaches, "cache should be cleaned up")
	assert.Equal(t, 1, stats.IdleCleanups, "cleanup should have occurred")
	assert.Equal(t, 1, stats.Errors, "error should be recorded from failed close")
}

// TestCacheManagerRemove tests explicit cache removal.
func TestCacheManagerRemove(t *testing.T) {
	connector := func(_ context.Context, key string) (cache.Cache, error) {
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
	assert.Equal(t, 1, stats.ActiveCaches)

	// Remove cache
	err = mgr.Remove(tenantOne)
	require.NoError(t, err)

	stats = mgr.Stats()
	assert.Equal(t, 0, stats.ActiveCaches)

	// Verify cache was closed
	mock := c1.(*mockCache)
	assert.True(t, mock.closed.Load())

	// Get should create new instance
	c2, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	assert.NotSame(t, c1, c2, "should create new cache after removal")

	stats = mgr.Stats()
	assert.Equal(t, 1, stats.ActiveCaches)
	assert.Equal(t, 2, stats.TotalCreated)
}

// TestCacheManagerRemoveNonexistent tests removing a cache that doesn't exist.
func TestCacheManagerRemoveNonexistent(t *testing.T) {
	connector := func(_ context.Context, key string) (cache.Cache, error) {
		return newMockCache(key), nil
	}

	mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
	require.NoError(t, err)
	defer mgr.Close()

	// Remove nonexistent cache (should not error)
	err = mgr.Remove("nonexistent")
	assert.NoError(t, err)
}

// failingCloseCache is a mock cache that fails to close.
type failingCloseCache struct {
	*mockCache
	closeErr error
}

func (f *failingCloseCache) Close() error {
	f.closed.Store(true) // Mark as closed for tracking
	return f.closeErr
}

// TestCacheManagerRemoveWithCloseError tests that entries are removed from
// bookkeeping even when Close() fails.
func TestCacheManagerRemoveWithCloseError(t *testing.T) {
	closeErr := errors.New(closeFailedMsg)
	connector := func(_ context.Context, key string) (cache.Cache, error) {
		return &failingCloseCache{
			mockCache: newMockCache(key),
			closeErr:  closeErr,
		}, nil
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
	assert.Equal(t, 1, stats.ActiveCaches)

	// Remove cache (Close() will fail)
	err = mgr.Remove(tenantOne)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), closeFailedMsg)

	// CRITICAL: Entry should still be removed from bookkeeping
	stats = mgr.Stats()
	assert.Equal(t, 0, stats.ActiveCaches, "entry should be removed from manager despite close error")

	// Get should create a NEW instance (proving old entry was removed)
	c2, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	assert.NotSame(t, c1, c2, "should create new cache after removal despite close error")

	stats = mgr.Stats()
	assert.Equal(t, 1, stats.ActiveCaches)
	assert.Equal(t, 2, stats.TotalCreated)
}

// TestCacheManagerClose tests manager shutdown.
func TestCacheManagerClose(t *testing.T) {
	var closedCaches sync.Map

	connector := func(_ context.Context, key string) (cache.Cache, error) {
		return newTrackableMockCache(key, func(id string) {
			closedCaches.Store(id, true)
		}), nil
	}

	mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
	require.NoError(t, err)

	ctx := context.Background()

	// Create multiple caches
	_, err = mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	_, err = mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)
	_, err = mgr.Get(ctx, tenantThree)
	require.NoError(t, err)

	stats := mgr.Stats()
	assert.Equal(t, 3, stats.ActiveCaches)

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
	assert.Equal(t, 0, stats.ActiveCaches)

	// Close should be idempotent
	err = mgr.Close()
	assert.NoError(t, err)
}

// TestCacheManagerStats tests statistics reporting.
func TestCacheManagerStats(t *testing.T) {
	connector := func(_ context.Context, key string) (cache.Cache, error) {
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
	assert.Equal(t, 0, stats.ActiveCaches)
	assert.Equal(t, 0, stats.TotalCreated)
	assert.Equal(t, 0, stats.Evictions)
	assert.Equal(t, 0, stats.IdleCleanups)
	assert.Equal(t, 0, stats.Errors)
	assert.Equal(t, 2, stats.MaxSize)
	assert.Equal(t, int64(900), stats.IdleTTL) // 15 minutes = 900 seconds

	// Create caches
	_, err = mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	_, err = mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)

	stats = mgr.Stats()
	assert.Equal(t, 2, stats.ActiveCaches)
	assert.Equal(t, 2, stats.TotalCreated)

	// Trigger eviction
	_, err = mgr.Get(ctx, tenantThree)
	require.NoError(t, err)

	stats = mgr.Stats()
	assert.Equal(t, 2, stats.ActiveCaches)
	assert.Equal(t, 3, stats.TotalCreated)
	assert.Equal(t, 1, stats.Evictions)
}

// TestCacheManagerThreadSafety tests concurrent access to manager.
func TestCacheManagerThreadSafety(t *testing.T) {
	connector := func(_ context.Context, key string) (cache.Cache, error) {
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

				// Occasionally remove cache (ignore error - removal may race with close)
				if j%20 == 0 {
					_ = mgr.Remove(tenantID)
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify stats are consistent
	stats := mgr.Stats()
	totalCreated := stats.TotalCreated
	activeCaches := stats.ActiveCaches
	evictions := stats.Evictions
	idleCleanups := stats.IdleCleanups

	assert.GreaterOrEqual(t, totalCreated, activeCaches)
	assert.GreaterOrEqual(t, totalCreated, evictions+idleCleanups)
}

// slowCloseCache is a mock cache with configurable close delay.
type slowCloseCache struct {
	*mockCache
	closeDelay time.Duration
}

func (s *slowCloseCache) Close() error {
	time.Sleep(s.closeDelay) // Simulate slow I/O operation
	return s.mockCache.Close()
}

// TestCacheManagerConcurrentAccessDuringClose verifies that Get() operations
// are not blocked while caches are being closed (lock-free close pattern).
func TestCacheManagerConcurrentAccessDuringClose(t *testing.T) {
	connector := func(_ context.Context, key string) (cache.Cache, error) {
		return &slowCloseCache{
			mockCache:  newMockCache(key),
			closeDelay: 200 * time.Millisecond, // Slow close
		}, nil
	}

	config := cache.DefaultManagerConfig()
	config.MaxSize = 2 // Small capacity to trigger evictions

	mgr, err := cache.NewCacheManager(config, connector)
	require.NoError(t, err)
	defer mgr.Close()

	ctx := context.Background()

	// Create caches up to capacity
	_, err = mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	_, err = mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)

	// Channel to signal when Get() operations complete
	getChan := make(chan time.Duration, 1)

	// Start a goroutine that will trigger eviction (slow close)
	go func() {
		// Triggers eviction of tenant-1 (200ms close) - error intentionally ignored in background goroutine
		if _, err := mgr.Get(ctx, tenantThree); err != nil {
			// Log but don't fail - background operation
			t.Logf("background Get error (may be expected during cleanup): %v", err)
		}
	}()

	// Give eviction time to start (50ms should be enough to acquire lock and start close)
	time.Sleep(50 * time.Millisecond)

	// While tenant-1 is being closed (outside lock), verify Get() is NOT blocked
	start := time.Now()
	c, err := mgr.Get(ctx, tenantTwo) // Should NOT wait for close to complete
	elapsed := time.Since(start)
	getChan <- elapsed

	require.NoError(t, err)
	require.NotNil(t, c)

	// CRITICAL ASSERTION: Get() should complete quickly (<50ms), NOT wait for close (200ms)
	getElapsed := <-getChan
	assert.Less(t, getElapsed, 100*time.Millisecond,
		"Get() should not be blocked by slow Close() operation")

	// Wait for eviction to complete
	time.Sleep(250 * time.Millisecond)

	// Verify eviction completed successfully
	stats := mgr.Stats()
	assert.Equal(t, 2, stats.ActiveCaches)
	assert.Equal(t, 1, stats.Evictions)
}

// TestCacheManagerConcurrentRemoveDuringGet verifies that Stats() and Get()
// operations continue to work while Remove() closes caches in background.
func TestCacheManagerConcurrentRemoveDuringGet(t *testing.T) {
	connector := func(_ context.Context, key string) (cache.Cache, error) {
		return &slowCloseCache{
			mockCache:  newMockCache(key),
			closeDelay: 150 * time.Millisecond, // Slow close
		}, nil
	}

	mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
	require.NoError(t, err)
	defer mgr.Close()

	ctx := context.Background()

	// Create multiple caches
	_, err = mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	_, err = mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)
	_, err = mgr.Get(ctx, tenantThree)
	require.NoError(t, err)

	// Start removing tenant-1 (slow close in background)
	removeDone := make(chan error, 1)
	go func() {
		removeDone <- mgr.Remove(tenantOne)
	}()

	// Give removal time to start close operation
	time.Sleep(50 * time.Millisecond)

	// While tenant-1 is closing, verify other operations are NOT blocked
	operations := 10
	var wg sync.WaitGroup
	wg.Add(operations)

	for i := 0; i < operations; i++ {
		go func(_ int) {
			defer wg.Done()

			start := time.Now()

			// These should all complete quickly - errors ignored as concurrent operations may race
			stats := mgr.Stats()
			_ = stats // intentionally unused - just checking non-blocking behavior
			c2, _ := mgr.Get(ctx, tenantTwo)
			_ = c2 // intentionally unused
			c3, _ := mgr.Get(ctx, tenantThree)
			_ = c3 // intentionally unused

			elapsed := time.Since(start)
			assert.Less(t, elapsed, 100*time.Millisecond,
				"operations should not be blocked by slow close")
		}(i)
	}

	wg.Wait()

	// Wait for removal to complete
	err = <-removeDone
	require.NoError(t, err)

	// Verify final state
	stats := mgr.Stats()
	assert.Equal(t, 2, stats.ActiveCaches)
}
