package testing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
)

func TestNewMockCache(t *testing.T) {
	mock := NewMockCache()
	assert.NotNil(t, mock)
	assert.Equal(t, "mock", mock.ID())
	assert.False(t, mock.IsClosed())
}

func TestNewMockCacheWithID(t *testing.T) {
	mock := NewMockCacheWithID("custom-id")
	assert.NotNil(t, mock)
	assert.Equal(t, "custom-id", mock.ID())
}

func TestMockCacheGetSet(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	// Set value
	err := mock.Set(ctx, "key1", []byte("value1"), time.Minute)
	require.NoError(t, err)

	// Get value
	value, err := mock.Get(ctx, "key1")
	require.NoError(t, err)
	assert.Equal(t, []byte("value1"), value)

	// Get non-existent key
	_, err = mock.Get(ctx, "missing")
	assert.ErrorIs(t, err, cache.ErrNotFound)
}

func TestMockCacheDelete(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	// Set and delete
	mock.Set(ctx, "key1", []byte("value1"), time.Minute)
	err := mock.Delete(ctx, "key1")
	require.NoError(t, err)

	// Verify deleted
	_, err = mock.Get(ctx, "key1")
	assert.ErrorIs(t, err, cache.ErrNotFound)
}

func TestMockCacheGetOrSet(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	// First call - should set
	value1, wasSet1, err := mock.GetOrSet(ctx, "key1", []byte("value1"), time.Minute)
	require.NoError(t, err)
	assert.True(t, wasSet1)
	assert.Equal(t, []byte("value1"), value1)

	// Second call - should load existing
	value2, wasSet2, err := mock.GetOrSet(ctx, "key1", []byte("different"), time.Minute)
	require.NoError(t, err)
	assert.False(t, wasSet2)
	assert.Equal(t, []byte("value1"), value2)
}

func TestMockCacheCompareAndSet(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	t.Run("SetIfNotExists", func(t *testing.T) {
		// Set only if key doesn't exist (expectedValue = nil)
		swapped, err := mock.CompareAndSet(ctx, "key1", nil, []byte("value1"), time.Minute)
		require.NoError(t, err)
		assert.True(t, swapped)

		// Try again - should fail
		swapped, err = mock.CompareAndSet(ctx, "key1", nil, []byte("different"), time.Minute)
		require.NoError(t, err)
		assert.False(t, swapped)
	})

	t.Run("CompareAndSwap", func(t *testing.T) {
		mock := NewMockCache()
		mock.Set(ctx, "key2", []byte("old-value"), time.Minute)

		// CAS with correct old value
		swapped, err := mock.CompareAndSet(ctx, "key2", []byte("old-value"), []byte("new-value"), time.Minute)
		require.NoError(t, err)
		assert.True(t, swapped)

		// Verify new value
		value, _ := mock.Get(ctx, "key2")
		assert.Equal(t, []byte("new-value"), value)

		// CAS with wrong old value
		swapped, err = mock.CompareAndSet(ctx, "key2", []byte("old-value"), []byte("another"), time.Minute)
		require.NoError(t, err)
		assert.False(t, swapped)
	})
}

func TestMockCacheTTLExpiration(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	// Set with short TTL
	mock.Set(ctx, "key1", []byte("value1"), 10*time.Millisecond)

	// Should exist immediately
	_, err := mock.Get(ctx, "key1")
	assert.NoError(t, err)

	// Wait for expiration
	time.Sleep(20 * time.Millisecond)

	// Should be expired
	_, err = mock.Get(ctx, "key1")
	assert.ErrorIs(t, err, cache.ErrNotFound)
}

func TestMockCacheInvalidTTL(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	// Zero TTL (no expiration) - should succeed
	err := mock.Set(ctx, "key1", []byte("value1"), 0)
	assert.NoError(t, err)

	// Negative TTL - should return error
	err = mock.Set(ctx, "key2", []byte("value2"), -1*time.Second)
	assert.ErrorIs(t, err, cache.ErrInvalidTTL)
}

func TestMockCacheHealth(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	err := mock.Health(ctx)
	assert.NoError(t, err)
}

func TestMockCacheStats(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCacheWithID("test-cache")

	// Perform some operations
	mock.Set(ctx, "key1", []byte("value1"), time.Minute)
	mock.Set(ctx, "key2", []byte("value2"), time.Minute)
	mock.Get(ctx, "key1")

	stats, err := mock.Stats()
	require.NoError(t, err)

	assert.Equal(t, "test-cache", stats["id"])
	assert.Equal(t, 2, stats["entry_count"])
	assert.Equal(t, int64(1), stats["get_calls"])
	assert.Equal(t, int64(2), stats["set_calls"])
}

func TestMockCacheClose(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	// Set some data
	mock.Set(ctx, "key1", []byte("value1"), time.Minute)

	// Close
	err := mock.Close()
	require.NoError(t, err)
	assert.True(t, mock.IsClosed())

	// Operations should fail after close
	_, err = mock.Get(ctx, "key1")
	assert.ErrorIs(t, err, cache.ErrClosed)

	err = mock.Set(ctx, "key2", []byte("value2"), time.Minute)
	assert.ErrorIs(t, err, cache.ErrClosed)

	// Close again should fail
	err = mock.Close()
	assert.ErrorIs(t, err, cache.ErrClosed)
}

func TestMockCacheCloseCallback(t *testing.T) {
	var closedID string
	mock := NewMockCacheWithID("test-cache").WithCloseCallback(func(id string) {
		closedID = id
	})

	mock.Close()
	assert.Equal(t, "test-cache", closedID)
}

func TestMockCacheWithDelay(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache().WithDelay(50 * time.Millisecond)

	start := time.Now()
	mock.Get(ctx, "key")
	duration := time.Since(start)

	assert.GreaterOrEqual(t, duration, 50*time.Millisecond)
}

func TestMockCacheWithGetFailure(t *testing.T) {
	ctx := context.Background()
	customErr := errors.New("custom get error")
	mock := NewMockCache().WithGetFailure(customErr)

	_, err := mock.Get(ctx, "key")
	assert.ErrorIs(t, err, customErr)
}

func TestMockCacheWithSetFailure(t *testing.T) {
	ctx := context.Background()
	customErr := errors.New("custom set error")
	mock := NewMockCache().WithSetFailure(customErr)

	err := mock.Set(ctx, "key", []byte("value"), time.Minute)
	assert.ErrorIs(t, err, customErr)
}

func TestMockCacheWithDeleteFailure(t *testing.T) {
	ctx := context.Background()
	customErr := errors.New("custom delete error")
	mock := NewMockCache().WithDeleteFailure(customErr)

	err := mock.Delete(ctx, "key")
	assert.ErrorIs(t, err, customErr)
}

func TestMockCacheWithGetOrSetFailure(t *testing.T) {
	ctx := context.Background()
	customErr := errors.New("custom getorset error")
	mock := NewMockCache().WithGetOrSetFailure(customErr)

	_, _, err := mock.GetOrSet(ctx, "key", []byte("value"), time.Minute)
	assert.ErrorIs(t, err, customErr)
}

func TestMockCacheWithCompareAndSetFailure(t *testing.T) {
	ctx := context.Background()
	customErr := errors.New("custom cas error")
	mock := NewMockCache().WithCompareAndSetFailure(customErr)

	_, err := mock.CompareAndSet(ctx, "key", nil, []byte("value"), time.Minute)
	assert.ErrorIs(t, err, customErr)
}

func TestMockCacheWithHealthFailure(t *testing.T) {
	ctx := context.Background()
	customErr := errors.New("custom health error")
	mock := NewMockCache().WithHealthFailure(customErr)

	err := mock.Health(ctx)
	assert.ErrorIs(t, err, customErr)
}

func TestMockCacheWithStatsFailure(t *testing.T) {
	customErr := errors.New("custom stats error")
	mock := NewMockCache().WithStatsFailure(customErr)

	_, err := mock.Stats()
	assert.ErrorIs(t, err, customErr)
}

func TestMockCacheWithCloseFailure(t *testing.T) {
	customErr := errors.New("custom close error")
	mock := NewMockCache().WithCloseFailure(customErr)

	err := mock.Close()
	assert.ErrorIs(t, err, customErr)
}

func TestMockCacheOperationCounts(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	// Perform operations
	mock.Get(ctx, "key1")
	mock.Get(ctx, "key2")
	mock.Set(ctx, "key1", []byte("value"), time.Minute)
	mock.Delete(ctx, "key1")
	mock.GetOrSet(ctx, "key2", []byte("value"), time.Minute)
	mock.CompareAndSet(ctx, "key3", nil, []byte("value"), time.Minute)
	mock.Health(ctx)
	mock.Stats()

	assert.Equal(t, int64(2), mock.OperationCount("Get"))
	assert.Equal(t, int64(1), mock.OperationCount("Set"))
	assert.Equal(t, int64(1), mock.OperationCount("Delete"))
	assert.Equal(t, int64(1), mock.OperationCount("GetOrSet"))
	assert.Equal(t, int64(1), mock.OperationCount("CompareAndSet"))
	assert.Equal(t, int64(1), mock.OperationCount("CAS")) // Alias
	assert.Equal(t, int64(1), mock.OperationCount("Health"))
	assert.Equal(t, int64(1), mock.OperationCount("Stats"))
}

func TestMockCacheResetCounters(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	// Perform operations
	mock.Get(ctx, "key")
	mock.Set(ctx, "key", []byte("value"), time.Minute)

	// Reset
	mock.ResetCounters()

	assert.Equal(t, int64(0), mock.OperationCount("Get"))
	assert.Equal(t, int64(0), mock.OperationCount("Set"))
}

func TestMockCacheClear(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	// Add data
	mock.Set(ctx, "key1", []byte("value1"), time.Minute)
	mock.Set(ctx, "key2", []byte("value2"), time.Minute)

	// Clear
	mock.Clear()

	keys := mock.AllKeys()
	assert.Empty(t, keys)
}

func TestMockCacheHas(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	assert.False(t, mock.Has("key1"))

	mock.Set(ctx, "key1", []byte("value"), time.Minute)
	assert.True(t, mock.Has("key1"))

	mock.Delete(ctx, "key1")
	assert.False(t, mock.Has("key1"))
}

func TestMockCacheAllKeys(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	mock.Set(ctx, "key1", []byte("value1"), time.Minute)
	mock.Set(ctx, "key2", []byte("value2"), time.Minute)

	keys := mock.AllKeys()
	assert.Len(t, keys, 2)
	assert.Contains(t, keys, "key1")
	assert.Contains(t, keys, "key2")
}

func TestMockCacheDump(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCacheWithID("test-cache")

	mock.Set(ctx, "key1", []byte("value1"), time.Minute)

	dump := mock.Dump()
	assert.Contains(t, dump, "test-cache")
	assert.Contains(t, dump, "key1")
	assert.Contains(t, dump, "value1")
}

func TestMockCacheContextCancellation(t *testing.T) {
	mock := NewMockCache().WithDelay(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := mock.Get(ctx, "key")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestMockCacheChainedConfiguration(t *testing.T) {
	customErr := errors.New("test error")

	mock := NewMockCacheWithID("chained").
		WithDelay(10 * time.Millisecond).
		WithGetFailure(customErr).
		WithCloseCallback(func(id string) {
			assert.Equal(t, "chained", id)
		})

	assert.Equal(t, "chained", mock.ID())
	assert.Equal(t, 10*time.Millisecond, mock.delay)
	assert.Equal(t, customErr, mock.getError)
}
