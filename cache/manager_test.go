package cache_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/cache/internal/tracking"
	"github.com/gaborage/go-bricks/logger"
	obtest "github.com/gaborage/go-bricks/observability/testing"
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

func (m *mockCache) CompareAndDelete(_ context.Context, _ string, expectedValue []byte) (bool, error) {
	if m.closed.Load() {
		return false, cache.ErrClosed
	}
	if expectedValue == nil {
		return false, cache.ErrNilExpectedValue
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
		assert.Contains(t, err.Error(), "maxsize cannot be negative")
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
		assert.Contains(t, err.Error(), "idlettl cannot be negative")
	})

	// NewCacheManager's own contract, whatever path config took to reach it: a non-positive
	// cleanupinterval is accepted and substituted, where the two keys above reject. Both
	// spellings of "non-positive" are driven, since a disabled cache can deliver either.
	//
	// The substituted VALUE (5m) is NOT covered, here or anywhere: deleting the fallback in
	// NewCacheManager leaves ./cache/... ./app ./config all green (verified by hand). No
	// exported surface reports a pool's cleanup interval, and 5m outlasts any test, so the
	// only honest ways to close it are an accessor or the WarnIfCleanupIntervalTooLate call
	// database and messaging already make and cache does not — both wider than this change.
	// What makes the substitution matter IS pinned, one layer down: resourcepool's
	// TestPoolStartCleanupNoOpConditions fixes that a non-positive interval starts NO cleanup
	// loop, so passing the raw value through would silently disable idle cleanup rather than
	// fail. This case pins the half it can see: the manager is built, usable, closes cleanly.
	for _, interval := range []time.Duration{-1 * time.Second, 0} {
		t.Run(fmt.Sprintf("NonPositiveCleanupInterval_%s", interval), func(t *testing.T) {
			connector := func(_ context.Context, key string) (cache.Cache, error) {
				return newMockCache(key), nil
			}

			config := cache.DefaultManagerConfig()
			config.CleanupInterval = interval

			mgr, err := cache.NewCacheManager(config, connector)
			require.NoError(t, err)
			require.NotNil(t, mgr)
			defer mgr.Close()

			instance, release, err := mgr.Get(context.Background(), tenantOne)
			require.NoError(t, err, "a manager built with a non-positive cleanupinterval must still lease")
			require.NotNil(t, instance)
			release()

			assert.Equal(t, 1, mgr.Stats().ActiveCaches)
			require.NoError(t, mgr.Close())
		})
	}

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
		c1, _, err := mgr.Get(ctx, tenantOne)
		require.NoError(t, err)
		require.NotNil(t, c1)
		assert.Equal(t, int32(1), creationCount.Load())

		// Second call reuses existing cache
		c2, _, err := mgr.Get(ctx, tenantOne)
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
		c1, _, err := mgr.Get(ctx, tenantOne)
		require.NoError(t, err)

		c2, _, err := mgr.Get(ctx, tenantTwo)
		require.NoError(t, err)

		c3, _, err := mgr.Get(ctx, tenantThree)
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
		c, _, err := mgr.Get(ctx, tenantOne)
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
			c, _, err := mgr.Get(ctx, tenantOne)
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
	c1, _, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	c2, _, err := mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)
	_, relThree, err := mgr.Get(ctx, tenantThree)
	require.NoError(t, err)
	// Release tenant-3's lease so its later eviction can actually close it
	// (a leased cache's close is deferred until lease release — ADR-032).
	relThree()

	stats := mgr.Stats()
	assert.Equal(t, 3, stats.ActiveCaches)
	assert.Equal(t, 0, stats.Evictions)

	// Access tenant-1 and tenant-2 to refresh LRU (tenant-3 becomes oldest)
	_, _, err = mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	_, _, err = mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)

	// Add tenant-4, should evict tenant-3 (oldest)
	c4, _, err := mgr.Get(ctx, "tenant-4")
	require.NoError(t, err)
	require.NotNil(t, c4)

	stats = mgr.Stats()
	assert.Equal(t, 3, stats.ActiveCaches)
	assert.Equal(t, 1, stats.Evictions)

	// Verify tenant-3 was closed
	_, closed := closedCaches.Load(tenantThree)
	assert.True(t, closed, "tenant-3 should have been evicted and closed")

	// Verify tenant-1, tenant-2, tenant-4 still active
	c1Again, _, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	assert.Same(t, c1, c1Again, "tenant-1 should still be active")

	c2Again, _, err := mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)
	assert.Same(t, c2, c2Again, "tenant-2 should still be active")

	c4Again, _, err := mgr.Get(ctx, "tenant-4")
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
	c1, _, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	c2, _, err := mgr.Get(ctx, tenantTwo)
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
				_, _, gerr := mgr.Get(ctx, tenantOne)
				if gerr != nil {
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
	c1Again, _, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	assert.Same(t, c1, c1Again)

	// Verify tenant-2 was removed (new instance created)
	c2Again, _, err := mgr.Get(ctx, tenantTwo)
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
	c1, relOne, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	require.NotNil(t, c1)
	// Release the lease so idle cleanup may close it (deferred-until-release, ADR-032).
	relOne()

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
	c1, relOne, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	require.NotNil(t, c1)
	// Release the lease so Remove may close it (deferred-until-release, ADR-032).
	relOne()

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
	c2, _, err := mgr.Get(ctx, tenantOne)
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
	c1, relOne, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	require.NotNil(t, c1)
	// Release the lease so Remove may attempt the (failing) close (deferred-until-release, ADR-032).
	relOne()

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
	c2, _, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	assert.NotSame(t, c1, c2, "should create new cache after removal despite close error")

	stats = mgr.Stats()
	assert.Equal(t, 1, stats.ActiveCaches)
	assert.Equal(t, 2, stats.TotalCreated)
}

// TestCacheManagerStatsSurfacesDeferredCloseErrors pins that a deferred-close failure — a
// cache instance still borrowed when Close runs, closed only at its final release (ADR-032,
// C581.3) — is not silently dropped: PoolStats.Errors must reach Stats().Errors so callers
// can observe it, since it is deliberately excluded from Close()'s returned error. This is
// cache's sibling of the equivalent DbManager/messaging.Manager pins; unlike those two,
// CacheManager.Stats() already wired ps.Errors through before this pin was added.
func TestCacheManagerStatsSurfacesDeferredCloseErrors(t *testing.T) {
	closeErr := errors.New(closeFailedMsg)
	connector := func(_ context.Context, key string) (cache.Cache, error) {
		return &failingCloseCache{mockCache: newMockCache(key), closeErr: closeErr}, nil
	}

	mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
	require.NoError(t, err)

	ctx := context.Background()
	_, release, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	assert.Equal(t, 0, mgr.Stats().Errors, "no close attempted yet")

	// Close leaves the still-borrowed cache open (liveLeases > 0); the deferred close
	// attempt — and its failure — only happens once the lease is released.
	require.NoError(t, mgr.Close(), "Close must not surface a deferred close failure")
	release()

	assert.Equal(t, 1, mgr.Stats().Errors, "deferred close failure must be counted and surfaced")
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
	_, rel1, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	_, rel2, err := mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)
	_, rel3, err := mgr.Get(ctx, tenantThree)
	require.NoError(t, err)
	rel1()
	rel2()
	rel3()

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

// TestCacheManagerGetAfterCloseReturnsErrManagerClosed locks in the W3-C
// closed-state guard. Pre-fix, Get() could race against Close() and return a
// cache instance that was about to be torn down (still in m.caches but
// queued in Close's toClose slice). Post-fix, Get() consults the atomic
// `closed` flag at the very top and returns ErrManagerClosed immediately.
func TestCacheManagerGetAfterCloseReturnsErrManagerClosed(t *testing.T) {
	connector := func(_ context.Context, key string) (cache.Cache, error) {
		return newMockCache(key), nil
	}
	mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
	require.NoError(t, err)

	require.NoError(t, mgr.Close())

	_, _, err = mgr.Get(context.Background(), tenantOne)
	assert.ErrorIs(t, err, cache.ErrManagerClosed)
}

// TestCacheManagerRemoveAfterCloseReturnsErrManagerClosed mirrors the Get
// test for the Remove() operational path.
func TestCacheManagerRemoveAfterCloseReturnsErrManagerClosed(t *testing.T) {
	connector := func(_ context.Context, key string) (cache.Cache, error) {
		return newMockCache(key), nil
	}
	mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
	require.NoError(t, err)

	// Seed an entry first so we can prove the closed-state check fires BEFORE
	// the lookup — without the guard, Remove would happily delete from a
	// half-torn-down map.
	_, _, err = mgr.Get(context.Background(), tenantOne)
	require.NoError(t, err)

	require.NoError(t, mgr.Close())

	err = mgr.Remove(tenantOne)
	assert.ErrorIs(t, err, cache.ErrManagerClosed)
}

// TestCacheManagerGetRacesCloseReturnsErrOrSuccess stress-tests the
// closed-state guard under concurrent Close + Get. Each Get must either
// succeed (closed wasn't yet observed) or return ErrManagerClosed — it
// must NEVER return a different error or panic with a use-after-close.
// Run under -race to catch any unsynchronized access to the manager's
// internal maps during shutdown.
func TestCacheManagerGetRacesCloseReturnsErrOrSuccess(t *testing.T) {
	connector := func(_ context.Context, key string) (cache.Cache, error) {
		return newMockCache(key), nil
	}
	mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
	require.NoError(t, err)

	const N = 64
	results := make(chan error, N)
	start := make(chan struct{})

	for i := 0; i < N; i++ {
		go func(id int) {
			<-start
			_, _, err := mgr.Get(context.Background(), fmt.Sprintf("tenant-%d", id))
			results <- err
		}(i)
	}

	// Fire concurrent Close from another goroutine; let them race.
	go func() {
		<-start
		_ = mgr.Close()
	}()

	close(start)

	for i := 0; i < N; i++ {
		err := <-results
		if err != nil && !errors.Is(err, cache.ErrManagerClosed) {
			t.Errorf("Get during Close returned unexpected error: %v (want nil or ErrManagerClosed)", err)
		}
	}
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
	_, _, err = mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	_, _, err = mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)

	stats = mgr.Stats()
	assert.Equal(t, 2, stats.ActiveCaches)
	assert.Equal(t, 2, stats.TotalCreated)

	// Trigger eviction
	_, _, err = mgr.Get(ctx, tenantThree)
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
			runThreadSafetyWorker(ctx, t, mgr, workerID, operations)
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

// runThreadSafetyWorker performs a Get/use/occasional-Remove cycle against the
// shared CacheManager. Errors are reported via t.Errorf — safe to call from a
// worker goroutine since t.Errorf does not Goexit.
func runThreadSafetyWorker(ctx context.Context, t *testing.T, mgr *cache.CacheManager, workerID, operations int) {
	for j := 0; j < operations; j++ {
		tenantID := fmt.Sprintf("tenant-%d", j%5) // 5 different tenants

		c, _, err := mgr.Get(ctx, tenantID)
		if err != nil {
			t.Errorf("worker %d: Get failed: %v", workerID, err)
			continue
		}

		_, err = c.Get(ctx, "test-key")
		if err != nil && !errors.Is(err, cache.ErrClosed) {
			t.Errorf("worker %d: cache operation failed: %v", workerID, err)
		}

		// Occasionally remove cache (ignore error - removal may race with close)
		if j%20 == 0 {
			_ = mgr.Remove(tenantID)
		}
	}
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
	_, _, err = mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	_, _, err = mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)

	// Channel to signal when Get() operations complete
	getChan := make(chan time.Duration, 1)

	// Start a goroutine that will trigger eviction (slow close)
	go func() {
		// Triggers eviction of tenant-1 (200ms close) - error intentionally ignored in background goroutine
		if _, _, gerr := mgr.Get(ctx, tenantThree); gerr != nil {
			// Log but don't fail - background operation
			t.Logf("background Get error (may be expected during cleanup): %v", gerr)
		}
	}()

	// Give eviction time to start (50ms should be enough to acquire lock and start close)
	time.Sleep(50 * time.Millisecond)

	// While tenant-1 is being closed (outside lock), verify Get() is NOT blocked
	start := time.Now()
	c, _, err := mgr.Get(ctx, tenantTwo) // Should NOT wait for close to complete
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
	_, _, err = mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	_, _, err = mgr.Get(ctx, tenantTwo)
	require.NoError(t, err)
	_, _, err = mgr.Get(ctx, tenantThree)
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

	for i := range operations {
		go func(_ int) {
			defer wg.Done()

			start := time.Now()

			// These should all complete quickly - errors ignored as concurrent operations may race
			stats := mgr.Stats()
			_ = stats // intentionally unused - just checking non-blocking behavior
			c2, _, gerr := mgr.Get(ctx, tenantTwo)
			if gerr != nil {
				t.Fail()
			}
			_ = c2 // intentionally unused

			c3, _, gerr := mgr.Get(ctx, tenantThree)
			if gerr != nil {
				t.Fail()
			}

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

// --- Lease/refcount: eviction-while-in-use race (issue #606, ADR-032) ---

func leasedClosableConnector(closed *sync.Map) cache.Connector {
	return func(_ context.Context, key string) (cache.Cache, error) {
		return newTrackableMockCache(key, func(id string) { closed.Store(id, true) }), nil
	}
}

func TestCacheManagerGetReturnsNonNilReleaseFunc(t *testing.T) {
	var closed sync.Map
	cfg := cache.DefaultManagerConfig()
	cfg.MaxSize = 5
	mgr, err := cache.NewCacheManager(cfg, leasedClosableConnector(&closed))
	require.NoError(t, err)
	defer mgr.Close()

	c, release, err := mgr.Get(context.Background(), tenantOne)
	require.NoError(t, err)
	require.NotNil(t, c)
	require.NotNil(t, release, "Get must return a non-nil release so callers can always defer it")

	release() // releasing a live cached cache must NOT close it
	_, isClosed := closed.Load(tenantOne)
	assert.False(t, isClosed, "releasing a lease on a live cached cache must not close it")
	assert.Equal(t, 1, mgr.Stats().ActiveCaches)
}

func TestCacheManagerEvictionWhileLeasedDefersCloseUntilRelease(t *testing.T) {
	var closed sync.Map
	cfg := cache.DefaultManagerConfig()
	cfg.MaxSize = 1 // getting a second key evicts the first
	mgr, err := cache.NewCacheManager(cfg, leasedClosableConnector(&closed))
	require.NoError(t, err)
	defer mgr.Close()
	ctx := context.Background()

	_, releaseA, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)

	_, releaseB, err := mgr.Get(ctx, tenantTwo) // evicts tenant-1, which is still leased
	require.NoError(t, err)
	defer releaseB()

	_, closedWhileLeased := closed.Load(tenantOne)
	assert.False(t, closedWhileLeased,
		"an evicted-but-leased cache must not be closed while a lease is held (the #606 race)")

	releaseA()
	_, closedAfterRelease := closed.Load(tenantOne)
	assert.True(t, closedAfterRelease,
		"an evicted cache must be closed once its last lease is released")
}

func TestCacheManagerTwoLeasesKeepCacheAliveUntilBothReleased(t *testing.T) {
	var closed sync.Map
	cfg := cache.DefaultManagerConfig()
	cfg.MaxSize = 1
	mgr, err := cache.NewCacheManager(cfg, leasedClosableConnector(&closed))
	require.NoError(t, err)
	defer mgr.Close()
	ctx := context.Background()

	_, release1, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	_, release2, err := mgr.Get(ctx, tenantOne) // same key, second borrower → refcount 2
	require.NoError(t, err)

	_, releaseB, err := mgr.Get(ctx, tenantTwo) // evict tenant-1
	require.NoError(t, err)
	defer releaseB()

	release1()
	_, closedAfterFirst := closed.Load(tenantOne)
	assert.False(t, closedAfterFirst, "cache must stay open while a second lease is outstanding")

	release2()
	_, closedAfterSecond := closed.Load(tenantOne)
	assert.True(t, closedAfterSecond, "cache must close when the final lease is released")
}

func TestCacheManagerRemoveWhileLeasedDefersClose(t *testing.T) {
	var closed sync.Map
	cfg := cache.DefaultManagerConfig()
	cfg.MaxSize = 5
	mgr, err := cache.NewCacheManager(cfg, leasedClosableConnector(&closed))
	require.NoError(t, err)
	defer mgr.Close()

	_, release, err := mgr.Get(context.Background(), tenantOne)
	require.NoError(t, err)

	require.NoError(t, mgr.Remove(tenantOne)) // explicit removal while leased → defers close
	_, closedWhileLeased := closed.Load(tenantOne)
	assert.False(t, closedWhileLeased, "Remove must not close a leased cache")

	release()
	_, closedAfterRelease := closed.Load(tenantOne)
	assert.True(t, closedAfterRelease, "removed cache closes when its last lease is released")
}

func TestCacheManagerIdleCleanupWhileLeasedDefersClose(t *testing.T) {
	var closed sync.Map
	cfg := cache.DefaultManagerConfig()
	cfg.MaxSize = 5
	cfg.IdleTTL = 50 * time.Millisecond
	cfg.CleanupInterval = 25 * time.Millisecond
	mgr, err := cache.NewCacheManager(cfg, leasedClosableConnector(&closed))
	require.NoError(t, err)
	defer mgr.Close()

	_, release, err := mgr.Get(context.Background(), tenantOne)
	require.NoError(t, err)

	// Wait for at least one idle-cleanup cycle to detach the idle (but leased) entry.
	require.Eventually(t, func() bool { return mgr.Stats().IdleCleanups >= 1 }, time.Second, 10*time.Millisecond)

	_, closedWhileLeased := closed.Load(tenantOne)
	assert.False(t, closedWhileLeased, "idle cleanup must not close a leased cache")

	release()
	require.Eventually(t, func() bool { _, c := closed.Load(tenantOne); return c }, time.Second, 10*time.Millisecond,
		"idle-cleaned cache closes when its last lease is released")
}

func TestCacheManagerReleaseIsIdempotent(t *testing.T) {
	var closed sync.Map
	cfg := cache.DefaultManagerConfig()
	cfg.MaxSize = 1
	mgr, err := cache.NewCacheManager(cfg, leasedClosableConnector(&closed))
	require.NoError(t, err)
	defer mgr.Close()
	ctx := context.Background()

	_, releaseA, err := mgr.Get(ctx, tenantOne)
	require.NoError(t, err)
	_, releaseB, err := mgr.Get(ctx, tenantTwo) // evict tenant-1
	require.NoError(t, err)
	defer releaseB()

	assert.NotPanics(t, func() {
		releaseA()
		releaseA() // double release must be a safe no-op
	})
}

// TestCacheManagerCreateRacingCloseDoesNotResurrect drives Close() into the window between
// Get's top-of-loop closed check and createCache taking the lock: the connector closes the
// manager before returning. createCache must re-check under the lock, close the just-created
// instance, and report ErrManagerClosed rather than resurrecting the cleared map (a leak).
func TestCacheManagerCreateRacingCloseDoesNotResurrect(t *testing.T) {
	var mgr *cache.CacheManager
	var once sync.Once
	var instanceClosed atomic.Bool

	cfg := cache.DefaultManagerConfig()
	cfg.IdleTTL = 0 // no background cleanup goroutine
	mgr, err := cache.NewCacheManager(cfg, func(_ context.Context, key string) (cache.Cache, error) {
		once.Do(func() { _ = mgr.Close() }) // Close lands before createCache takes the lock
		return newTrackableMockCache(key, func(string) { instanceClosed.Store(true) }), nil
	})
	require.NoError(t, err)

	_, _, gErr := mgr.Get(context.Background(), tenantOne)
	assert.ErrorIs(t, gErr, cache.ErrManagerClosed, "Get must report closed, not resurrect the map")
	assert.True(t, instanceClosed.Load(), "the just-created instance must be closed, not leaked")
	assert.Equal(t, 0, mgr.Stats().ActiveCaches, "the map must not be resurrected with a new entry")
}

// TestCacheManagerZeroValueMethodsAreSafe pins that a zero-value CacheManager (never built via
// NewCacheManager — the lightweight stand-in shape the DbManager/messaging.Manager siblings
// already support) does not panic on any of Get/Remove/Stats/Close.
func TestCacheManagerZeroValueMethodsAreSafe(t *testing.T) {
	m := &cache.CacheManager{}

	inst, release, getErr := m.Get(context.Background(), tenantOne)
	assert.ErrorIs(t, getErr, cache.ErrManagerClosed, "zero-value Get must fail closed, not panic")
	assert.Nil(t, inst)
	assert.Nil(t, release)

	assert.ErrorIs(t, m.Remove(tenantOne), cache.ErrManagerClosed, "zero-value Remove must fail closed, not panic")

	assert.Equal(t, cache.ManagerStats{}, m.Stats(), "zero-value Stats must report empty stats, not panic")

	assert.NoError(t, m.Close(), "closing a never-initialized manager is a no-op")
}

const (
	metricManagerActiveCaches = "cache.manager.active_caches"
	metricManagerTotalCreated = "cache.manager.total_created"
	metricManagerEvictions    = "cache.manager.evictions"
	metricManagerIdleCleanups = "cache.manager.idle_cleanups"
	metricManagerErrors       = "cache.manager.errors"
)

// setupManagerMetricsProvider installs a manual-reader MeterProvider globally and
// clears the cache tracking package's memoized meter so the next
// RegisterManagerMetrics call binds to it. Mirrors httpclient/client_test.go:1134.
func setupManagerMetricsProvider(t *testing.T) *obtest.TestMeterProvider {
	t.Helper()
	prev := otel.GetMeterProvider()
	mp := obtest.NewTestMeterProvider()
	otel.SetMeterProvider(mp)
	tracking.ResetForTesting()
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		tracking.ResetForTesting()
		require.NoError(t, mp.Shutdown(context.Background()))
	})
	return mp
}

func TestNewCacheManagerRegistersManagerMetrics(t *testing.T) {
	mp := setupManagerMetricsProvider(t)

	connector := func(_ context.Context, key string) (cache.Cache, error) {
		return newMockCache(key), nil
	}
	mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
	require.NoError(t, err)
	defer mgr.Close()

	for _, tenant := range []string{tenantOne, tenantTwo, tenantThree} {
		_, release, gerr := mgr.Get(context.Background(), tenant)
		require.NoError(t, gerr)
		release()
	}

	rm := mp.Collect(t)
	obtest.AssertMetricValue(t, rm, metricManagerActiveCaches, int64(3))
	obtest.AssertMetricValue(t, rm, metricManagerTotalCreated, int64(3))
	obtest.AssertMetricValue(t, rm, metricManagerEvictions, int64(0))
	obtest.AssertMetricValue(t, rm, metricManagerIdleCleanups, int64(0))
	obtest.AssertMetricValue(t, rm, metricManagerErrors, int64(0))
}

func TestCacheManagerCloseUnregistersManagerMetrics(t *testing.T) {
	mp := setupManagerMetricsProvider(t)

	connector := func(_ context.Context, key string) (cache.Cache, error) {
		return newMockCache(key), nil
	}
	mgr, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
	require.NoError(t, err)

	_, release, err := mgr.Get(context.Background(), tenantOne)
	require.NoError(t, err)
	release()

	// Positive control: without this, the post-Close assertion below passes
	// vacuously when nothing was ever registered.
	obtest.AssertMetricValue(t, mp.Collect(t), metricManagerTotalCreated, int64(1))

	require.NoError(t, mgr.Close())
	require.NoError(t, mgr.Close()) // exactly-once: a second cleanup must not run

	rm := mp.Collect(t)
	if m := obtest.FindMetric(rm, metricManagerTotalCreated); m != nil {
		sum, ok := m.Data.(metricdata.Sum[int64])
		require.True(t, ok, "cache.manager.total_created should be Sum[int64]")
		assert.Empty(t, sum.DataPoints,
			"cache.manager.* must stop being observed after Close")
	}
}

// warnRecorder is a logger.Logger double that captures every Warn() event's message and
// fields. Other levels are discarded; NewCacheManager logs nothing but this WARN.
type warnRecorder struct {
	msgs   []string
	fields []map[string]any
}

func (r *warnRecorder) Info() logger.LogEvent  { return &recordedEvent{} }
func (r *warnRecorder) Error() logger.LogEvent { return &recordedEvent{} }
func (r *warnRecorder) Debug() logger.LogEvent { return &recordedEvent{} }
func (r *warnRecorder) Fatal() logger.LogEvent { return &recordedEvent{} }
func (r *warnRecorder) Warn() logger.LogEvent {
	return &recordedEvent{sink: r, fields: map[string]any{}}
}
func (r *warnRecorder) WithContext(any) logger.Logger           { return r }
func (r *warnRecorder) WithFields(map[string]any) logger.Logger { return r }

type recordedEvent struct {
	sink   *warnRecorder
	fields map[string]any
}

func (e *recordedEvent) Msg(msg string) {
	if e.sink != nil {
		e.sink.msgs = append(e.sink.msgs, msg)
		e.sink.fields = append(e.sink.fields, e.fields)
	}
}

func (e *recordedEvent) set(k string, v any) logger.LogEvent {
	if e.fields != nil {
		e.fields[k] = v
	}
	return e
}

func (e *recordedEvent) Msgf(format string, args ...any)               { e.Msg(fmt.Sprintf(format, args...)) }
func (e *recordedEvent) Err(error) logger.LogEvent                     { return e }
func (e *recordedEvent) Str(k, v string) logger.LogEvent               { return e.set(k, v) }
func (e *recordedEvent) Int(string, int) logger.LogEvent               { return e }
func (e *recordedEvent) Int64(string, int64) logger.LogEvent           { return e }
func (e *recordedEvent) Uint64(string, uint64) logger.LogEvent         { return e }
func (e *recordedEvent) Dur(k string, v time.Duration) logger.LogEvent { return e.set(k, v) }
func (e *recordedEvent) Interface(string, any) logger.LogEvent         { return e }
func (e *recordedEvent) Bytes(string, []byte) logger.LogEvent          { return e }
func (e *recordedEvent) Bool(string, bool) logger.LogEvent             { return e }
func (e *recordedEvent) Enabled() bool                                 { return true }

// TestNewCacheManagerWarnsWhenCleanupIntervalIsNotBelowIdleTTL pins that the cache manager
// emits the same advisory as the database and messaging managers, under cache.manager keys,
// and that the 5m fallback for a non-positive CleanupInterval is applied BEFORE the check —
// a raw 0 would be below any TTL and stay silent, so removing the fallback turns this red.
// The predicate itself is exhausted in internal/resourcepool/cleanup_warning_test.go.
func TestNewCacheManagerWarnsWhenCleanupIntervalIsNotBelowIdleTTL(t *testing.T) {
	tests := []struct {
		name            string
		cleanupInterval time.Duration
		idleTTL         time.Duration
		wantWarn        bool
	}{
		{name: "interval_below_ttl_silent", cleanupInterval: time.Minute, idleTTL: 10 * time.Minute, wantWarn: false},
		{name: "defaults_silent", cleanupInterval: 5 * time.Minute, idleTTL: 15 * time.Minute, wantWarn: false},
		{name: "unset_interval_takes_the_default_and_warns_against_a_short_ttl", cleanupInterval: 0, idleTTL: time.Minute, wantWarn: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &warnRecorder{}
			m, err := cache.NewCacheManager(cache.ManagerConfig{
				MaxSize:         5,
				IdleTTL:         tc.idleTTL,
				CleanupInterval: tc.cleanupInterval,
				Logger:          rec,
			}, func(context.Context, string) (cache.Cache, error) { return newMockCache("c"), nil })
			require.NoError(t, err)
			defer func() { _ = m.Close() }()

			if !tc.wantWarn {
				assert.Empty(t, rec.msgs, "a sweep that outpaces the TTL must not WARN")
				return
			}
			require.Len(t, rec.msgs, 1, "the advisory must fire exactly once per manager")
			assert.Contains(t, rec.msgs[0], "cache.manager.cleanupinterval is >= cache.manager.idlettl")
			assert.Equal(t, "cache.manager", rec.fields[0]["resource"])
			assert.Equal(t, 5*time.Minute, rec.fields[0]["cleanupinterval"], "the WARN must report the applied fallback, not the raw 0")
			assert.Equal(t, time.Minute, rec.fields[0]["idlettl"])
		})
	}
}

// TestNewCacheManagerNilLoggerSkipsTheWarn pins the documented nil-safe default: no Logger
// means no WARN and no panic, even for a late cleanup interval.
func TestNewCacheManagerNilLoggerSkipsTheWarn(t *testing.T) {
	m, err := cache.NewCacheManager(cache.ManagerConfig{
		MaxSize: 5, IdleTTL: time.Minute, CleanupInterval: 0,
	}, func(context.Context, string) (cache.Cache, error) { return newMockCache("c"), nil })
	require.NoError(t, err)
	require.NoError(t, m.Close())
}
