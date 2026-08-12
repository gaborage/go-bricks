package redis

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
)

const (
	testKey1          = "test:key:1"
	testNewValue      = "new-value"
	testExistingValue = "existing-value"
	testWorker        = "worker-1"
)

// testConfig returns a valid Config pointing at the given miniredis server.
func testConfig(mr *miniredis.Miniredis) *Config {
	return &Config{
		Host:     mr.Host(),
		Port:     mr.Server().Addr().Port,
		Database: 0,
		PoolSize: 10,
	}
}

// setupTestRedis creates a miniredis server and client for testing.
func setupTestRedis(t *testing.T) (*Client, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)

	client, err := NewClient(testConfig(mr))
	require.NoError(t, err)
	require.NotNil(t, client)

	return client, mr
}

func TestNewClient(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		defer client.Close()

		assert.NotNil(t, client)
		assert.NotNil(t, client.client)
		assert.False(t, client.closed.Load())
	})

	t.Run("InvalidConfig", func(t *testing.T) {
		cfg := &Config{
			Host: "", // Missing host
			Port: 6379,
		}

		client, err := NewClient(cfg)
		assert.Error(t, err)
		assert.Nil(t, client)

		var configErr *cache.ConfigError
		assert.True(t, errors.As(err, &configErr))
	})

	t.Run("ConnectionFailed", func(t *testing.T) {
		cfg := &Config{
			Host:        "localhost",
			Port:        99999, // Invalid port
			Database:    0,
			DialTimeout: 100 * time.Millisecond,
		}

		client, err := NewClient(cfg)
		assert.Error(t, err)
		assert.Nil(t, client)
	})
}

// TestClientGet tests the Get method of the Redis client.
func TestClientGet(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		defer client.Close()

		mr.Set(testKey1, "test-value")

		ctx := context.Background()
		result, err := client.Get(ctx, testKey1)
		require.NoError(t, err)
		assert.Equal(t, []byte("test-value"), result)
	})

	t.Run("NotFound", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		defer client.Close()

		ctx := context.Background()
		result, err := client.Get(ctx, "nonexistent")
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.True(t, errors.Is(err, cache.ErrNotFound))
	})

	t.Run("Closed", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		client.Close()

		ctx := context.Background()
		result, err := client.Get(ctx, testKey1)
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.True(t, errors.Is(err, cache.ErrClosed))
	})
}

// TestClientSet tests the Set method of the Redis client.
func TestClientSet(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		defer client.Close()

		ctx := context.Background()
		err := client.Set(ctx, testKey1, []byte("value"), 5*time.Minute)
		require.NoError(t, err)

		// Verify with miniredis
		assert.True(t, mr.Exists(testKey1))
		value, err := mr.Get(testKey1)
		require.NoError(t, err)
		assert.Equal(t, "value", value)
	})

	t.Run("ZeroTTL_NoExpiration", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		defer client.Close()

		ctx := context.Background()
		// TTL=0 means no expiration (should succeed)
		err := client.Set(ctx, testKey1, []byte("value"), 0)
		assert.NoError(t, err)
	})

	t.Run("NegativeTTL", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		defer client.Close()

		ctx := context.Background()
		err := client.Set(ctx, testKey1, []byte("value"), -1*time.Second)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, cache.ErrInvalidTTL))
	})

	t.Run("Closed", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		client.Close()

		ctx := context.Background()
		err := client.Set(ctx, testKey1, []byte("value"), 5*time.Minute)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, cache.ErrClosed))
	})
}

// TestClientDelete tests the Delete method of the Redis client.
func TestClientDelete(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		defer client.Close()

		require.NoError(t, mr.Set(testKey1, "value"))

		ctx := context.Background()
		err := client.Delete(ctx, testKey1)
		require.NoError(t, err)

		// Verify deletion
		assert.False(t, mr.Exists(testKey1))
	})

	t.Run("NonexistentKey", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		defer client.Close()

		ctx := context.Background()
		err := client.Delete(ctx, "nonexistent")
		assert.NoError(t, err) // Delete of nonexistent key is not an error
	})

	t.Run("Closed", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		client.Close()

		ctx := context.Background()
		err := client.Delete(ctx, testKey1)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, cache.ErrClosed))
	})
}

// TestClientGetOrSet tests the GetOrSet method of the Redis client.
func TestClientGetOrSet(t *testing.T) {
	t.Run("NewKey_SetSuccess", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		defer client.Close()

		ctx := context.Background()
		value, wasSet, err := client.GetOrSet(ctx, testKey1, []byte(testNewValue), 5*time.Minute)
		require.NoError(t, err)
		assert.True(t, wasSet)
		assert.Equal(t, []byte(testNewValue), value)

		// Verify in Redis
		assert.True(t, mr.Exists(testKey1))
		stored, err := mr.Get(testKey1)
		require.NoError(t, err)
		assert.Equal(t, testNewValue, stored)
	})

	t.Run("ExistingKey_GetSuccess", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		defer client.Close()

		require.NoError(t, mr.Set(testKey1, testExistingValue))

		ctx := context.Background()
		value, wasSet, err := client.GetOrSet(ctx, testKey1, []byte(testNewValue), 5*time.Minute)
		require.NoError(t, err)
		assert.False(t, wasSet)
		assert.Equal(t, []byte(testExistingValue), value)

		// Verify original value unchanged
		stored, err := mr.Get(testKey1)
		require.NoError(t, err)
		assert.Equal(t, testExistingValue, stored)
	})

	t.Run("ZeroTTL_NoExpiration", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		defer client.Close()

		ctx := context.Background()
		// TTL=0 means no expiration (should succeed)
		value, wasSet, err := client.GetOrSet(ctx, testKey1, []byte("value"), 0)
		assert.NoError(t, err)
		assert.True(t, wasSet)
		assert.Equal(t, []byte("value"), value)
	})

	t.Run("Closed", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		client.Close()

		ctx := context.Background()
		value, wasSet, err := client.GetOrSet(ctx, testKey1, []byte("value"), 5*time.Minute)
		assert.Error(t, err)
		assert.False(t, wasSet)
		assert.Nil(t, value)
		assert.True(t, errors.Is(err, cache.ErrClosed))
	})
}

// TestClientCompareAndSet tests the CompareAndSet method of the Redis client.
func TestClientCompareAndSet(t *testing.T) {
	t.Run("AcquireLock_Success", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		defer client.Close()

		ctx := context.Background()
		success, err := client.CompareAndSet(ctx, testKey1, nil, []byte(testWorker), 5*time.Minute)
		require.NoError(t, err)
		assert.True(t, success)

		// Verify in Redis
		assert.True(t, mr.Exists(testKey1))
		stored, err := mr.Get(testKey1)
		require.NoError(t, err)
		assert.Equal(t, testWorker, stored)
	})

	t.Run("AcquireLock_AlreadyHeld", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		defer client.Close()

		require.NoError(t, mr.Set(testKey1, testWorker))

		ctx := context.Background()
		success, err := client.CompareAndSet(ctx, testKey1, nil, []byte("worker-2"), 5*time.Minute)
		require.NoError(t, err)
		assert.False(t, success)

		// Verify original value unchanged
		stored, err := mr.Get(testKey1)
		require.NoError(t, err)
		assert.Equal(t, testWorker, stored)
	})

	t.Run("UpdateValue_Success", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		defer client.Close()

		require.NoError(t, mr.Set(testKey1, "old-value"))

		ctx := context.Background()
		success, err := client.CompareAndSet(ctx, testKey1, []byte("old-value"), []byte(testNewValue), 5*time.Minute)
		require.NoError(t, err)
		assert.True(t, success)

		// Verify updated value
		stored, err := mr.Get(testKey1)
		require.NoError(t, err)
		assert.Equal(t, testNewValue, stored)
	})

	t.Run("UpdateValue_Mismatch", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		defer client.Close()

		require.NoError(t, mr.Set(testKey1, "current-value"))

		ctx := context.Background()
		success, err := client.CompareAndSet(ctx, testKey1, []byte("wrong-value"), []byte(testNewValue), 5*time.Minute)
		require.NoError(t, err)
		assert.False(t, success)

		// Verify original value unchanged
		stored, err := mr.Get(testKey1)
		require.NoError(t, err)
		assert.Equal(t, "current-value", stored)
	})

	t.Run("ZeroTTL_NoExpiration", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		defer client.Close()

		ctx := context.Background()
		// TTL=0 means no expiration (should succeed)
		success, err := client.CompareAndSet(ctx, testKey1, nil, []byte("value"), 0)
		assert.NoError(t, err)
		assert.True(t, success)
	})

	t.Run("Closed", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		client.Close()

		ctx := context.Background()
		success, err := client.CompareAndSet(ctx, testKey1, nil, []byte("value"), 5*time.Minute)
		assert.Error(t, err)
		assert.False(t, success)
		assert.True(t, errors.Is(err, cache.ErrClosed))
	})
}

// TestClientHealth tests the Health method of the Redis client.
func TestClientHealth(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		defer client.Close()

		ctx := context.Background()
		err := client.Health(ctx)
		assert.NoError(t, err)
	})

	t.Run("Closed", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		client.Close()

		ctx := context.Background()
		err := client.Health(ctx)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, cache.ErrClosed))
	})
}

// TestClientStats tests the Stats method of the Redis client.
func TestClientStats(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		defer client.Close()

		stats, err := client.Stats()
		require.NoError(t, err)
		assert.NotNil(t, stats)

		// Verify expected fields
		assert.Contains(t, stats, "redis_info")
		assert.Contains(t, stats, "pool_hits")
		assert.Contains(t, stats, "pool_misses")
		assert.Contains(t, stats, "pool_timeouts")
		assert.Contains(t, stats, "pool_total_conns")
		assert.Contains(t, stats, "pool_idle_conns")
		assert.Contains(t, stats, "pool_stale_conns")
	})

	t.Run("Closed", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		client.Close()

		stats, err := client.Stats()
		assert.Error(t, err)
		assert.Nil(t, stats)
		assert.True(t, errors.Is(err, cache.ErrClosed))
	})
}

// TestClientClose tests the Close method of the Redis client.
func TestClientClose(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test

		err := client.Close()
		assert.NoError(t, err)
		assert.True(t, client.closed.Load())

		// Verify operations fail after close
		ctx := context.Background()
		_, err = client.Get(ctx, testKey1)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, cache.ErrClosed))
	})

	t.Run("AlreadyClosed", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		_ = mr // miniredis instance not needed for this test
		client.Close()

		err := client.Close()
		assert.Error(t, err)
		assert.True(t, errors.Is(err, cache.ErrClosed))
	})
}

// TestConfigValidate tests the Validate method of the Config struct.
func TestConfigValidate(t *testing.T) {
	t.Run("ValidConfig", func(t *testing.T) {
		cfg := &Config{
			Host:     "localhost",
			Port:     6379,
			Database: 0,
			PoolSize: 10,
		}

		err := cfg.Validate()
		assert.NoError(t, err)
	})

	t.Run("MissingHost", func(t *testing.T) {
		cfg := &Config{
			Host: "",
			Port: 6379,
		}

		err := cfg.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "host is required")
	})

	t.Run("InvalidPort", func(t *testing.T) {
		tests := []struct {
			name string
			port int
		}{
			{"ZeroPort", 0},
			{"NegativePort", -1},
			{"PortTooHigh", 70000},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cfg := &Config{
					Host: "localhost",
					Port: tt.port,
				}

				err := cfg.Validate()
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "invalid port")
			})
		}
	})

	t.Run("InvalidDatabase", func(t *testing.T) {
		tests := []struct {
			name string
			db   int
		}{
			{"NegativeDB", -1},
			{"DBTooHigh", 16},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cfg := &Config{
					Host:     "localhost",
					Port:     6379,
					Database: tt.db,
				}

				err := cfg.Validate()
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "invalid database number")
			})
		}
	})

	t.Run("InvalidPoolSize", func(t *testing.T) {
		cfg := &Config{
			Host:     "localhost",
			Port:     6379,
			PoolSize: 0,
		}

		err := cfg.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid pool size")
	})
}

// TestConfigAddress tests the Address method of the Config struct.
func TestConfigAddress(t *testing.T) {
	cfg := &Config{
		Host: "redis.example.com",
		Port: 6379,
	}

	assert.Equal(t, "redis.example.com:6379", cfg.Address())
}

// TestDistributedLockRaceCondition tests lock acquisition under concurrent load.
func TestDistributedLockRaceCondition(t *testing.T) {
	client, mr := setupTestRedis(t)
	_ = mr // miniredis instance not needed for this test
	defer client.Close()

	const (
		workers   = 10
		lockKey   = "test:lock"
		lockValue = "acquired"
	)

	ctx := context.Background()

	// Channel to collect results from goroutines
	type result struct {
		success bool
		err     error
	}
	results := make(chan result, workers)

	// Simulate concurrent workers trying to acquire the same lock
	for i := 0; i < workers; i++ {
		go func() {
			success, err := client.CompareAndSet(ctx, lockKey, nil, []byte(lockValue), 1*time.Minute)
			results <- result{success: success, err: err}
		}()
	}

	// Collect results in main goroutine (avoid race on successCount and invalid require from goroutines)
	successCount := 0
	for i := 0; i < workers; i++ {
		res := <-results
		require.NoError(t, res.err)
		if res.success {
			successCount++
		}
	}

	// Only ONE worker should have successfully acquired the lock
	assert.Equal(t, 1, successCount, "expected exactly one worker to acquire lock")
}

// TestCompareAndSetEmptyExpectedMissingKey pins that a non-nil empty expected value is a
// real compare, not an NX acquire: an absent key must not be granted.
func TestCompareAndSetEmptyExpectedMissingKey(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer client.Close()

	ctx := context.Background()
	success, err := client.CompareAndSet(ctx, testKey1, []byte{}, []byte(testNewValue), 5*time.Minute)
	require.NoError(t, err)
	assert.False(t, success, "empty expected value must not grant an absent key")
	assert.False(t, mr.Exists(testKey1))
}

// TestCompareAndSetEmptyExpectedEmptyStored verifies compare-against-empty succeeds when
// the stored value really is empty.
func TestCompareAndSetEmptyExpectedEmptyStored(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer client.Close()

	ctx := context.Background()
	require.NoError(t, client.Set(ctx, testKey1, []byte{}, 5*time.Minute))

	success, err := client.CompareAndSet(ctx, testKey1, []byte{}, []byte(testNewValue), 5*time.Minute)
	require.NoError(t, err)
	assert.True(t, success)

	stored, err := mr.Get(testKey1)
	require.NoError(t, err)
	assert.Equal(t, testNewValue, stored)
}

// TestCompareAndSetNilExpectedStillNX pins the preserved acquire-if-absent path.
func TestCompareAndSetNilExpectedStillNX(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer client.Close()

	ctx := context.Background()

	success, err := client.CompareAndSet(ctx, testKey1, nil, []byte(testWorker), 5*time.Minute)
	require.NoError(t, err)
	assert.True(t, success, "nil expected must acquire an absent key")

	success, err = client.CompareAndSet(ctx, testKey1, nil, []byte("worker-2"), 5*time.Minute)
	require.NoError(t, err)
	assert.False(t, success, "nil expected must not acquire a held key")

	stored, err := mr.Get(testKey1)
	require.NoError(t, err)
	assert.Equal(t, testWorker, stored)
}

func TestRedisVersionTooOld(t *testing.T) {
	// An INFO body carrying sections but no version line must fail open.
	const noVersionLine = "# Clients\r\nconnected_clients:1\r\n\r\n# Stats\r\ntotal_connections_received:1\r\n"

	tests := []struct {
		name    string
		info    string
		tooOld  bool
		version string
	}{
		{name: "redis_6_2_is_too_old", info: "redis_version:6.2.14\r\n", tooOld: true, version: "6.2.14"},
		{name: "redis_7_0_is_the_floor", info: "redis_version:7.0.0", tooOld: false, version: "7.0.0"},
		{name: "redis_7_2_is_accepted", info: "redis_version:7.2.4", tooOld: false, version: "7.2.4"},
		{name: "double_digit_major_is_accepted", info: "redis_version:10.0.1", tooOld: false, version: "10.0.1"},
		{name: "version_line_among_other_fields", info: "# Server\r\nredis_version:6.0.20\r\nos:Linux\r\n", tooOld: true, version: "6.0.20"},
		{name: "unparseable_major_fails_open", info: "redis_version:unknown\r\n", tooOld: false, version: "unknown"},
		{name: "empty_info_fails_open", info: "", tooOld: false, version: ""},
		{name: "garbage_fails_open", info: "garbage", tooOld: false, version: ""},
		{name: "no_version_line_fails_open", info: noVersionLine, tooOld: false, version: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tooOld, version := redisVersionTooOld(tt.info)
			assert.Equal(t, tt.tooOld, tooOld)
			assert.Equal(t, tt.version, version)
		})
	}
}

// TestNewClientRejectsOldRedis drives the version floor through the readServerInfo seam.
// miniredis answers INFO server with "section (server) is not supported", so the seam is the
// only way to reach the too-old branch. This test mutates package state, so neither it nor
// its subtests may call t.Parallel().
func TestNewClientRejectsOldRedis(t *testing.T) {
	t.Run("too_old_fails_construction", func(t *testing.T) {
		original := readServerInfo
		t.Cleanup(func() { readServerInfo = original })
		readServerInfo = func(_ context.Context, _ *redis.Client) (string, error) {
			return "redis_version:6.2.14\r\n", nil
		}

		client, err := NewClient(testConfig(miniredis.RunT(t)))

		require.Error(t, err)
		assert.Nil(t, client)
		assert.Contains(t, err.Error(), "7.0")
		assert.Contains(t, err.Error(), "6.2.14")
	})

	t.Run("info_error_fails_open", func(t *testing.T) {
		original := readServerInfo
		t.Cleanup(func() { readServerInfo = original })
		readServerInfo = func(_ context.Context, _ *redis.Client) (string, error) {
			return "", errors.New("NOPERM this user has no permissions to run the 'info' command")
		}

		client, err := NewClient(testConfig(miniredis.RunT(t)))

		require.NoError(t, err)
		require.NotNil(t, client)
		defer client.Close()
	})
}

// TestCompareAndSetTTLClamp pins the TTL conversion at CompareAndSet's Eval
// site. The pre-existing TestClientCompareAndSet/ZeroTTL_NoExpiration subtest
// asserts only that the call succeeds, so it cannot distinguish "no expiry"
// from "1ms expiry"; these cases assert the stored TTL itself.
func TestCompareAndSetTTLClamp(t *testing.T) {
	tests := []struct {
		name        string
		ttl         time.Duration
		expectedTTL time.Duration
	}{
		{name: "sub_millisecond_ttl_clamps_to_one_millisecond", ttl: 500 * time.Microsecond, expectedTTL: time.Millisecond},
		{name: "zero_ttl_means_no_expiry", ttl: 0, expectedTTL: 0},
		{name: "millisecond_ttl_is_unchanged", ttl: 5 * time.Millisecond, expectedTTL: 5 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, mr := setupTestRedis(t)
			defer client.Close()

			ctx := context.Background()
			success, err := client.CompareAndSet(ctx, testKey1, nil, []byte(testWorker), tt.ttl)
			require.NoError(t, err)
			require.True(t, success)
			require.True(t, mr.Exists(testKey1))

			// miniredis reports 0 for "no TTL set" (direct.go: TTL godoc).
			assert.Equal(t, tt.expectedTTL, mr.TTL(testKey1))
		})
	}
}

// TestCompareAndSetSubMillisecondTTLExpires proves the clamped key actually
// goes away rather than merely carrying a TTL field.
func TestCompareAndSetSubMillisecondTTLExpires(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer client.Close()

	ctx := context.Background()
	success, err := client.CompareAndSet(ctx, testKey1, nil, []byte(testWorker), 500*time.Microsecond)
	require.NoError(t, err)
	require.True(t, success)
	require.True(t, mr.Exists(testKey1))

	mr.FastForward(2 * time.Millisecond)
	assert.False(t, mr.Exists(testKey1))
}

// TestCompareAndSetCASModeSubMillisecondTTLClamps covers the script's second
// SET branch: the table above only exercises the nx (acquire-if-absent) path.
func TestCompareAndSetCASModeSubMillisecondTTLClamps(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer client.Close()

	require.NoError(t, mr.Set(testKey1, testExistingValue))

	ctx := context.Background()
	success, err := client.CompareAndSet(ctx, testKey1, []byte(testExistingValue), []byte(testNewValue), 500*time.Microsecond)
	require.NoError(t, err)
	require.True(t, success)
	assert.Equal(t, time.Millisecond, mr.TTL(testKey1))
}

func TestClientCompareAndDeleteMatchingValueDeletes(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer client.Close()

	require.NoError(t, mr.Set(testKey1, testWorker))

	ctx := context.Background()
	deleted, err := client.CompareAndDelete(ctx, testKey1, []byte(testWorker))
	require.NoError(t, err)
	assert.True(t, deleted)
	assert.False(t, mr.Exists(testKey1))
}

func TestClientCompareAndDeleteMismatchLeavesKey(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer client.Close()

	require.NoError(t, mr.Set(testKey1, testExistingValue))

	ctx := context.Background()
	deleted, err := client.CompareAndDelete(ctx, testKey1, []byte("worker-2"))
	require.NoError(t, err)
	assert.False(t, deleted)

	stored, err := mr.Get(testKey1)
	require.NoError(t, err)
	assert.Equal(t, testExistingValue, stored)
}

func TestClientCompareAndDeleteAbsentKey(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer client.Close()

	ctx := context.Background()
	deleted, err := client.CompareAndDelete(ctx, testKey1, []byte(testWorker))
	require.NoError(t, err)
	assert.False(t, deleted)
	assert.False(t, mr.Exists(testKey1))
}

// TestClientCompareAndDeleteNilExpectedIsRejected pins both halves of the guard: the
// sentinel, and that nothing reached Redis. Without the round-trip half it passes even
// with the guard removed — go-redis renders a nil []byte as a zero-length bulk string,
// which would match the pre-seeded empty value and delete the key.
func TestClientCompareAndDeleteNilExpectedIsRejected(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer client.Close()

	require.NoError(t, mr.Set(testKey1, ""))

	ctx := context.Background()
	before := mr.CommandCount()

	deleted, err := client.CompareAndDelete(ctx, testKey1, nil)
	assert.False(t, deleted)
	assert.ErrorIs(t, err, cache.ErrNilExpectedValue)
	assert.Equal(t, before, mr.CommandCount())
	assert.True(t, mr.Exists(testKey1))
}

func TestClientCompareAndDeleteEmptySliceComparesAgainstEmptyString(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer client.Close()

	require.NoError(t, mr.Set(testKey1, ""))

	ctx := context.Background()
	deleted, err := client.CompareAndDelete(ctx, testKey1, []byte{})
	require.NoError(t, err)
	assert.True(t, deleted)
	assert.False(t, mr.Exists(testKey1))
}

// TestClientCompareAndDeleteConcurrentSingleWinner proves the release is atomic: EVAL
// runs to completion in miniredis as in real Redis, so exactly one racer observes the
// token and deletes it.
func TestClientCompareAndDeleteConcurrentSingleWinner(t *testing.T) {
	client, mr := setupTestRedis(t)
	defer client.Close()

	require.NoError(t, mr.Set(testKey1, testWorker))

	const racers = 8

	ctx := context.Background()
	var wins atomic.Int64
	var wg sync.WaitGroup

	wg.Add(racers)
	for range racers {
		go func() {
			defer wg.Done()

			deleted, err := client.CompareAndDelete(ctx, testKey1, []byte(testWorker))
			assert.NoError(t, err)
			if deleted {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(1), wins.Load())
	assert.False(t, mr.Exists(testKey1))
}

func TestClientCompareAndDeleteAfterCloseFailsClosed(t *testing.T) {
	client, mr := setupTestRedis(t)
	_ = mr // miniredis instance not needed for this test
	client.Close()

	ctx := context.Background()
	deleted, err := client.CompareAndDelete(ctx, testKey1, []byte(testWorker))
	assert.False(t, deleted)
	assert.ErrorIs(t, err, cache.ErrClosed)
}
