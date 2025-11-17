package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
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

// setupTestRedis creates a miniredis server and client for testing.
func setupTestRedis(t *testing.T) (*Client, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)

	cfg := &Config{
		Host:     mr.Host(),
		Port:     mr.Server().Addr().Port,
		Database: 0,
		PoolSize: 10,
	}

	client, err := NewClient(cfg)
	require.NoError(t, err)
	require.NotNil(t, client)

	return client, mr
}

func TestNewClient(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		client, _ := setupTestRedis(t)
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
		client, _ := setupTestRedis(t)
		defer client.Close()

		ctx := context.Background()
		result, err := client.Get(ctx, "nonexistent")
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.True(t, errors.Is(err, cache.ErrNotFound))
	})

	t.Run("Closed", func(t *testing.T) {
		client, _ := setupTestRedis(t)
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
		value, _ := mr.Get(testKey1)
		assert.Equal(t, "value", value)
	})

	t.Run("ZeroTTL_NoExpiration", func(t *testing.T) {
		client, _ := setupTestRedis(t)
		defer client.Close()

		ctx := context.Background()
		// TTL=0 means no expiration (should succeed)
		err := client.Set(ctx, testKey1, []byte("value"), 0)
		assert.NoError(t, err)
	})

	t.Run("NegativeTTL", func(t *testing.T) {
		client, _ := setupTestRedis(t)
		defer client.Close()

		ctx := context.Background()
		err := client.Set(ctx, testKey1, []byte("value"), -1*time.Second)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, cache.ErrInvalidTTL))
	})

	t.Run("Closed", func(t *testing.T) {
		client, _ := setupTestRedis(t)
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

		mr.Set(testKey1, "value")

		ctx := context.Background()
		err := client.Delete(ctx, testKey1)
		require.NoError(t, err)

		// Verify deletion
		assert.False(t, mr.Exists(testKey1))
	})

	t.Run("NonexistentKey", func(t *testing.T) {
		client, _ := setupTestRedis(t)
		defer client.Close()

		ctx := context.Background()
		err := client.Delete(ctx, "nonexistent")
		assert.NoError(t, err) // Delete of nonexistent key is not an error
	})

	t.Run("Closed", func(t *testing.T) {
		client, _ := setupTestRedis(t)
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
		stored, _ := mr.Get(testKey1)
		assert.Equal(t, testNewValue, stored)
	})

	t.Run("ExistingKey_GetSuccess", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		defer client.Close()

		mr.Set(testKey1, testExistingValue)

		ctx := context.Background()
		value, wasSet, err := client.GetOrSet(ctx, testKey1, []byte(testNewValue), 5*time.Minute)
		require.NoError(t, err)
		assert.False(t, wasSet)
		assert.Equal(t, []byte(testExistingValue), value)

		// Verify original value unchanged
		stored, _ := mr.Get(testKey1)
		assert.Equal(t, testExistingValue, stored)
	})

	t.Run("ZeroTTL_NoExpiration", func(t *testing.T) {
		client, _ := setupTestRedis(t)
		defer client.Close()

		ctx := context.Background()
		// TTL=0 means no expiration (should succeed)
		value, wasSet, err := client.GetOrSet(ctx, testKey1, []byte("value"), 0)
		assert.NoError(t, err)
		assert.True(t, wasSet)
		assert.Equal(t, []byte("value"), value)
	})

	t.Run("Closed", func(t *testing.T) {
		client, _ := setupTestRedis(t)
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
		stored, _ := mr.Get(testKey1)
		assert.Equal(t, testWorker, stored)
	})

	t.Run("AcquireLock_AlreadyHeld", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		defer client.Close()

		mr.Set(testKey1, testWorker)

		ctx := context.Background()
		success, err := client.CompareAndSet(ctx, testKey1, nil, []byte("worker-2"), 5*time.Minute)
		require.NoError(t, err)
		assert.False(t, success)

		// Verify original value unchanged
		stored, _ := mr.Get(testKey1)
		assert.Equal(t, testWorker, stored)
	})

	t.Run("UpdateValue_Success", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		defer client.Close()

		mr.Set(testKey1, "old-value")

		ctx := context.Background()
		success, err := client.CompareAndSet(ctx, testKey1, []byte("old-value"), []byte(testNewValue), 5*time.Minute)
		require.NoError(t, err)
		assert.True(t, success)

		// Verify updated value
		stored, _ := mr.Get(testKey1)
		assert.Equal(t, testNewValue, stored)
	})

	t.Run("UpdateValue_Mismatch", func(t *testing.T) {
		client, mr := setupTestRedis(t)
		defer client.Close()

		mr.Set(testKey1, "current-value")

		ctx := context.Background()
		success, err := client.CompareAndSet(ctx, testKey1, []byte("wrong-value"), []byte(testNewValue), 5*time.Minute)
		require.NoError(t, err)
		assert.False(t, success)

		// Verify original value unchanged
		stored, _ := mr.Get(testKey1)
		assert.Equal(t, "current-value", stored)
	})

	t.Run("ZeroTTL_NoExpiration", func(t *testing.T) {
		client, _ := setupTestRedis(t)
		defer client.Close()

		ctx := context.Background()
		// TTL=0 means no expiration (should succeed)
		success, err := client.CompareAndSet(ctx, testKey1, nil, []byte("value"), 0)
		assert.NoError(t, err)
		assert.True(t, success)
	})

	t.Run("Closed", func(t *testing.T) {
		client, _ := setupTestRedis(t)
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
		client, _ := setupTestRedis(t)
		defer client.Close()

		ctx := context.Background()
		err := client.Health(ctx)
		assert.NoError(t, err)
	})

	t.Run("Closed", func(t *testing.T) {
		client, _ := setupTestRedis(t)
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
		client, _ := setupTestRedis(t)
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
		client, _ := setupTestRedis(t)
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
		client, _ := setupTestRedis(t)

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
		client, _ := setupTestRedis(t)
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
	client, _ := setupTestRedis(t)
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
