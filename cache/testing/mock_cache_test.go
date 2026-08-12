package testing

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/cache/redis"
	"github.com/gaborage/go-bricks/internal/testutil"
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
		value, err := mock.Get(ctx, "key2")
		require.NoError(t, err)
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

// TestMockCacheWithCompareAndDeleteFailure pins the builder's field wiring and the
// precedence CompareAndDelete's doc promises: a nil expectedValue would otherwise return
// ErrNilExpectedValue, so seeing customErr proves the configured error is checked first.
func TestMockCacheWithCompareAndDeleteFailure(t *testing.T) {
	ctx := context.Background()
	customErr := errors.New("custom cad error")
	mock := NewMockCache().WithCompareAndDeleteFailure(customErr)

	_, err := mock.CompareAndDelete(ctx, "key", nil)
	assert.ErrorIs(t, err, customErr)
	assert.NotErrorIs(t, err, cache.ErrNilExpectedValue)
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
	customErr := errors.New(testutil.TestError)

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

// concurrencyRounds is the number of independent races each single-winner test runs; one
// round is not reliably enough to catch a check-then-act window.
const concurrencyRounds = 200

// seedExpired stores an entry that is already past its expiration, bypassing Set so the
// test does not depend on platform clock granularity.
func seedExpired(mock *MockCache, key string, value []byte) {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	mock.store(key, &cacheEntry{value: value, expiration: time.Now().Add(-time.Minute)})
}

// countConcurrentWinners releases op on every CPU at once through a yield barrier, so the
// callers are already running when they enter the mock rather than being woken one at a time,
// and reports how many succeeded. One CPU is left to the coordinator's wait loops.
func countConcurrentWinners(op func() (bool, error)) (winners int, firstErr error) {
	goroutines := runtime.GOMAXPROCS(0) - 1
	if goroutines < 4 {
		goroutines = 4
	}

	var ready atomic.Int64
	var release atomic.Bool
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ready.Add(1)
			for !release.Load() {
				runtime.Gosched()
			}
			won, err := op()

			mu.Lock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if won {
				winners++
			}
			mu.Unlock()
		}()
	}

	for ready.Load() < int64(goroutines) {
		runtime.Gosched()
	}
	release.Store(true)

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	return winners, firstErr
}

// TestMockCacheGetOrSetExpiredConcurrentSingleWinner pins that replacing an expired entry
// grants exactly one caller — jose replay detection relies on wasSet being a real winner flag.
func TestMockCacheGetOrSetExpiredConcurrentSingleWinner(t *testing.T) {
	const key = "expired-key"

	for round := 0; round < concurrencyRounds; round++ {
		mock := NewMockCache()
		seedExpired(mock, key, []byte("stale"))

		winners, err := countConcurrentWinners(func() (bool, error) {
			_, wasSet, opErr := mock.GetOrSet(context.Background(), key, []byte("fresh"), time.Minute)
			return wasSet, opErr
		})

		require.NoError(t, err)
		require.Equal(t, 1, winners, "round %d: exactly one caller may observe wasSet on an expired key", round)
	}
}

// TestMockCacheCompareAndSetConcurrentSingleWinner pins that the compare path is a real CAS,
// not check-then-act.
func TestMockCacheCompareAndSetConcurrentSingleWinner(t *testing.T) {
	ctx := context.Background()
	const key = "cas-key"
	seeded := []byte("seeded")

	for round := 0; round < concurrencyRounds; round++ {
		mock := NewMockCache()
		require.NoError(t, mock.Set(ctx, key, seeded, time.Minute))

		var next atomic.Int64
		winners, err := countConcurrentWinners(func() (bool, error) {
			newValue := []byte(fmt.Sprintf("replacement-%d", next.Add(1)))
			return mock.CompareAndSet(ctx, key, seeded, newValue, time.Minute)
		})

		require.NoError(t, err)
		require.Equal(t, 1, winners, "round %d: exactly one caller may win the compare-and-swap", round)
	}
}

// TestMockCacheCompareAndSetEmptyVsNilMatchesRedis pins the mock against the Redis client's
// nil-vs-empty semantics; a failure here means one of the two drifted.
func TestMockCacheCompareAndSetEmptyVsNilMatchesRedis(t *testing.T) {
	ctx := context.Background()

	t.Run("empty_expected_missing_key", func(t *testing.T) {
		mock := NewMockCache()
		swapped, err := mock.CompareAndSet(ctx, "key", []byte{}, []byte("new"), time.Minute)
		require.NoError(t, err)
		assert.False(t, swapped, "empty expected value must not grant an absent key")
		assert.False(t, mock.Has("key"))
	})

	t.Run("empty_expected_empty_stored", func(t *testing.T) {
		mock := NewMockCache()
		require.NoError(t, mock.Set(ctx, "key", []byte{}, time.Minute))

		swapped, err := mock.CompareAndSet(ctx, "key", []byte{}, []byte("new"), time.Minute)
		require.NoError(t, err)
		assert.True(t, swapped)

		stored, err := mock.Get(ctx, "key")
		require.NoError(t, err)
		assert.Equal(t, []byte("new"), stored)
	})

	t.Run("nil_expected_still_nx", func(t *testing.T) {
		mock := NewMockCache()

		swapped, err := mock.CompareAndSet(ctx, "key", nil, []byte("worker-1"), time.Minute)
		require.NoError(t, err)
		assert.True(t, swapped)

		swapped, err = mock.CompareAndSet(ctx, "key", nil, []byte("worker-2"), time.Minute)
		require.NoError(t, err)
		assert.False(t, swapped)

		stored, err := mock.Get(ctx, "key")
		require.NoError(t, err)
		assert.Equal(t, []byte("worker-1"), stored)
	})
}

// TestMockCacheCompareAndDeleteParityWithRedis is the anti-drift device for
// CompareAndDelete: the mock and the real client must agree on (deleted, err) and on
// whether the key survives. The expired_key case is the class an isolated-only suite
// cannot catch — the mock's CompareAndSet NX branch already diverges from Redis there.
func TestMockCacheCompareAndDeleteParityWithRedis(t *testing.T) {
	const (
		parityKey   = "lock:job:parity"
		parityToken = "worker-1"
	)

	tests := []struct {
		name        string
		seedAbsent  bool
		seedValue   string
		seedTTL     time.Duration
		expire      bool
		closeFirst  bool
		expected    []byte
		wantDeleted bool
		wantErr     error
		// wantGetErr is what a follow-up Get must report: nil means the key survived.
		wantGetErr error
	}{
		{name: "matching_token_deletes", seedValue: parityToken, seedTTL: time.Minute, expected: []byte(parityToken), wantDeleted: true, wantGetErr: cache.ErrNotFound},
		{name: "mismatched_token_leaves_key", seedValue: "worker-2", seedTTL: time.Minute, expected: []byte(parityToken)},
		{name: "absent_key", seedAbsent: true, expected: []byte(parityToken), wantGetErr: cache.ErrNotFound},
		{name: "expired_key", seedValue: parityToken, seedTTL: 10 * time.Millisecond, expire: true, expected: []byte(parityToken), wantGetErr: cache.ErrNotFound},
		{name: "empty_slice_compares_against_empty_string", seedValue: "", seedTTL: time.Minute, expected: []byte{}, wantDeleted: true, wantGetErr: cache.ErrNotFound},
		{name: "nil_expected_rejected", seedValue: parityToken, seedTTL: time.Minute, expected: nil, wantErr: cache.ErrNilExpectedValue},
		{name: "after_close", seedValue: parityToken, seedTTL: time.Minute, closeFirst: true, expected: []byte(parityToken), wantErr: cache.ErrClosed, wantGetErr: cache.ErrClosed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mr := miniredis.RunT(t)
			client, err := redis.NewClient(&redis.Config{
				Host:     mr.Host(),
				Port:     mr.Server().Addr().Port,
				PoolSize: 10,
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })

			subjects := []struct {
				name  string
				cache cache.Cache
				// lapse advances the subject past ttl. The mock reads the wall clock, so
				// only the client can fast-forward.
				lapse func(ttl time.Duration)
			}{
				{name: "mock", cache: NewMockCache(), lapse: func(ttl time.Duration) { time.Sleep(ttl + 10*time.Millisecond) }},
				{name: "redis", cache: client, lapse: func(ttl time.Duration) { mr.FastForward(ttl + 10*time.Millisecond) }},
			}

			for _, subject := range subjects {
				t.Run(subject.name, func(t *testing.T) {
					ctx := context.Background()

					if !tt.seedAbsent {
						require.NoError(t, subject.cache.Set(ctx, parityKey, []byte(tt.seedValue), tt.seedTTL))
					}
					if tt.expire {
						subject.lapse(tt.seedTTL)
					}
					if tt.closeFirst {
						require.NoError(t, subject.cache.Close())
					}

					deleted, err := subject.cache.CompareAndDelete(ctx, parityKey, tt.expected)
					if tt.wantErr != nil {
						assert.ErrorIs(t, err, tt.wantErr)
					} else {
						require.NoError(t, err)
					}
					assert.Equal(t, tt.wantDeleted, deleted)

					_, getErr := subject.cache.Get(ctx, parityKey)
					if tt.wantGetErr != nil {
						assert.ErrorIs(t, getErr, tt.wantGetErr)
					} else {
						assert.NoError(t, getErr)
					}
				})
			}
		})
	}
}
