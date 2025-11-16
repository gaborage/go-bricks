//go:build integration

package redis

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/testing/containers"
)

const (
	noErrExpectedMsg   = "cleanup should succeed, no error expected"
	getOrSetSucceedMsg = "GetOrSet should succeed"
)

// setupRealRedis creates a real Redis container and client for integration testing.
func setupRealRedis(t *testing.T) (*Client, context.Context) {
	t.Helper()

	// Create context with timeout to prevent indefinite hangs
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)

	// Register cleanup to cancel context
	t.Cleanup(func() {
		cancel()
	})

	// Start Redis container with default configuration
	redisContainer := containers.MustStartRedisContainer(ctx, t, nil).WithCleanup(t)

	// Create Redis client config
	cfg := &Config{
		Host:     redisContainer.Host(),
		Port:     redisContainer.Port(),
		Database: 0,
		PoolSize: 10,
	}

	// Create Redis client
	client, err := NewClient(cfg)
	require.NoError(t, err, "Failed to create Redis client")

	return client, ctx
}

// =============================================================================
// TTL Expiration Tests (Real Redis Behavior)
// =============================================================================

func TestRealRedisTTLExpiration(t *testing.T) {
	client, ctx := setupRealRedis(t)
	defer client.Close()

	key := "test:ttl:expiration"
	value := []byte("expires-soon")

	// Set key with 2-second TTL
	err := client.Set(ctx, key, value, 2*time.Second)
	require.NoError(t, err, "Set should succeed")

	// Immediately retrieve - should exist
	retrieved, err := client.Get(ctx, key)
	assert.NoError(t, err, "Get should succeed immediately after Set")
	assert.Equal(t, value, retrieved, "Retrieved value should match")

	// Wait for expiration with polling (more reliable than fixed sleep in CI)
	require.Eventually(t, func() bool {
		_, err := client.Get(ctx, key)
		return errors.Is(err, cache.ErrNotFound)
	}, 5*time.Second, 100*time.Millisecond, "Key should expire after TTL")
}

// =============================================================================
// Invalid TTL Handling Tests
// =============================================================================
func TestRealRedisTTLInvalidZeroValue(t *testing.T) {
	client, ctx := setupRealRedis(t)
	defer client.Close()

	key := "test:ttl:zero"
	value := []byte("test-value")

	// Zero TTL should return ErrInvalidTTL
	err := client.Set(ctx, key, value, 0)
	assert.ErrorIs(t, err, cache.ErrInvalidTTL, "Set with zero TTL should return ErrInvalidTTL")

	// Negative TTL should also return ErrInvalidTTL
	err = client.Set(ctx, key, value, -1*time.Second)
	assert.ErrorIs(t, err, cache.ErrInvalidTTL, "Set with negative TTL should return ErrInvalidTTL")
}

// =============================================================================
// Connection Pool Tests
// =============================================================================

func TestRealRedisConnectionPoolConcurrency(t *testing.T) {
	client, ctx := setupRealRedis(t)
	defer client.Close()

	const (
		numGoroutines = 50
		numOperations = 20
	)

	var wg sync.WaitGroup
	errChan := make(chan error, numGoroutines*numOperations)

	// Spawn goroutines to stress test connection pool
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				key := fmt.Sprintf("test:pool:worker%d:op%d", workerID, j)
				value := []byte(fmt.Sprintf("data-%d-%d", workerID, j))

				// Set
				if err := client.Set(ctx, key, value, 5*time.Second); err != nil {
					errChan <- fmt.Errorf("worker %d op %d Set failed: %w", workerID, j, err)
					continue
				}

				// Get
				retrieved, err := client.Get(ctx, key)
				if err != nil {
					errChan <- fmt.Errorf("worker %d op %d Get failed: %w", workerID, j, err)
					continue
				}

				if string(retrieved) != string(value) {
					errChan <- fmt.Errorf("worker %d op %d value mismatch: got %s, want %s",
						workerID, j, string(retrieved), string(value))
				}

				// Delete
				if err := client.Delete(ctx, key); err != nil {
					errChan <- fmt.Errorf("worker %d op %d Delete failed: %w", workerID, j, err)
				}
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// Check for errors
	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

	assert.Empty(t, errors, "No errors should occur during concurrent operations")
}

// =============================================================================
// Large Payload Tests
// =============================================================================

func TestRealRedisLargePayload(t *testing.T) {
	client, ctx := setupRealRedis(t)
	defer client.Close()

	key := "test:large:payload"

	// Create 2MB payload (larger than typical CBOR messages)
	largeValue := make([]byte, 2*1024*1024)
	for i := range largeValue {
		largeValue[i] = byte(i % 256)
	}

	// Set large payload
	err := client.Set(ctx, key, largeValue, 10*time.Second)
	require.NoError(t, err, "Set should succeed with large payload")

	// Get large payload
	retrieved, err := client.Get(ctx, key)
	require.NoError(t, err, "Get should succeed with large payload")
	assert.Equal(t, largeValue, retrieved, "Retrieved large payload should match")

	// Cleanup
	err = client.Delete(ctx, key)
	assert.NoError(t, err, noErrExpectedMsg)
}

// =============================================================================
// Lua Script Tests (GetOrSet, CompareAndSet)
// =============================================================================

func TestRealRedisGetOrSetLuaScript(t *testing.T) {
	client, ctx := setupRealRedis(t)
	defer client.Close()

	key := "test:lua:getorset"

	t.Run("first call creates value", func(t *testing.T) {
		value := []byte("initial-value")

		data, wasSet, err := client.GetOrSet(ctx, key, value, 10*time.Second)
		require.NoError(t, err, getOrSetSucceedMsg)
		assert.True(t, wasSet, "First call should set value (wasSet=true)")
		assert.Equal(t, value, data, "Returned value should match provided value")
	})

	t.Run("second call returns existing value", func(t *testing.T) {
		newValue := []byte("different-value")

		data, wasSet, err := client.GetOrSet(ctx, key, newValue, 10*time.Second)
		require.NoError(t, err, getOrSetSucceedMsg)
		assert.False(t, wasSet, "Second call should NOT set value (wasSet=false)")
		assert.Equal(t, []byte("initial-value"), data, "Returned value should be original, not new value")
	})

	// Cleanup
	err := client.Delete(ctx, key)
	assert.NoError(t, err, noErrExpectedMsg)
}

func TestRealRedisGetOrSetConcurrentDeduplication(t *testing.T) {
	client, ctx := setupRealRedis(t)
	defer client.Close()

	key := "test:lua:concurrent"
	value := []byte("deduplicated-value")

	const numGoroutines = 20
	var wg sync.WaitGroup
	results := make([]bool, numGoroutines) // Track which goroutines set vs loaded

	// Spawn concurrent GetOrSet calls
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, wasSet, err := client.GetOrSet(ctx, key, value, 10*time.Second)
			require.NoError(t, err, getOrSetSucceedMsg)
			results[idx] = wasSet
		}(i)
	}

	wg.Wait()

	// Exactly ONE goroutine should have set (wasSet=true), rest should have loaded (wasSet=false)
	setCount := 0
	loadCount := 0
	for _, wasSet := range results {
		if wasSet {
			setCount++
		} else {
			loadCount++
		}
	}

	assert.Equal(t, 1, setCount, "Exactly one goroutine should set the value")
	assert.Equal(t, numGoroutines-1, loadCount, "Other goroutines should load existing value")

	// Cleanup
	err := client.Delete(ctx, key)
	assert.NoError(t, err, noErrExpectedMsg)
}

func TestRealRedisCompareAndSetLuaScript(t *testing.T) {
	client, ctx := setupRealRedis(t)
	defer client.Close()

	key := "test:lua:cas"

	t.Run("CAS succeeds when old value matches", func(t *testing.T) {
		// Set initial value
		oldValue := []byte("old-value")
		err := client.Set(ctx, key, oldValue, 10*time.Second)
		require.NoError(t, err, "Set should succeed")

		// CAS with matching old value
		newValue := []byte("new-value")
		swapped, err := client.CompareAndSet(ctx, key, oldValue, newValue, 10*time.Second)
		require.NoError(t, err, "CompareAndSet should succeed")
		assert.True(t, swapped, "CAS should succeed when old value matches")

		// Verify new value was set
		retrieved, err := client.Get(ctx, key)
		require.NoError(t, err, "Get should succeed")
		assert.Equal(t, newValue, retrieved, "Value should be updated to new value")
	})

	t.Run("CAS fails when old value does not match", func(t *testing.T) {
		// Current value is "new-value" from previous test
		wrongOldValue := []byte("wrong-old-value")
		anotherNewValue := []byte("another-new-value")

		swapped, err := client.CompareAndSet(ctx, key, wrongOldValue, anotherNewValue, 10*time.Second)
		require.NoError(t, err, "CompareAndSet should not error on mismatch")
		assert.False(t, swapped, "CAS should fail when old value does not match")

		// Verify value was NOT updated
		retrieved, err := client.Get(ctx, key)
		require.NoError(t, err, "Get should succeed")
		assert.Equal(t, []byte("new-value"), retrieved, "Value should remain unchanged")
	})

	// Cleanup
	err := client.Delete(ctx, key)
	assert.NoError(t, err, noErrExpectedMsg)
}

// =============================================================================
// Health Check Tests
// =============================================================================

func TestRealRedisHealth(t *testing.T) {
	client, ctx := setupRealRedis(t)
	defer client.Close()

	err := client.Health(ctx)
	assert.NoError(t, err, "Health check should succeed on running Redis")
}

func TestRealRedisStats(t *testing.T) {
	client, _ := setupRealRedis(t)
	defer client.Close()

	stats, err := client.Stats()
	require.NoError(t, err, "Stats should succeed")
	assert.NotNil(t, stats, "Stats should not be nil")

	// Verify expected stats fields (pool stats and Redis INFO)
	assert.Contains(t, stats, "redis_info", "Stats should contain redis_info")
	assert.Contains(t, stats, "pool_hits", "Stats should contain pool_hits")
	assert.Contains(t, stats, "pool_misses", "Stats should contain pool_misses")
	assert.Contains(t, stats, "pool_total_conns", "Stats should contain pool_total_conns")
	assert.Contains(t, stats, "pool_idle_conns", "Stats should contain pool_idle_conns")
}

// =============================================================================
// Error Handling Tests
// =============================================================================

func TestRealRedisContextCancellation(t *testing.T) {
	client, setupCtx := setupRealRedis(t)
	defer client.Close()

	// Create separate context for this test
	ctx, cancel := context.WithCancel(setupCtx)
	cancel()

	// Operations should fail with context error
	_, err := client.Get(ctx, "test:key")
	assert.Error(t, err, "Get should fail with cancelled context")
	assert.Contains(t, err.Error(), "context canceled", "Error should mention context cancellation")
}

func TestRealRedisContextTimeout(t *testing.T) {
	client, _ := setupRealRedis(t)
	defer client.Close()

	// Create context with immediate timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	// Wait for timeout
	time.Sleep(1 * time.Millisecond)

	// Operations should fail with timeout
	_, err := client.Get(ctx, "test:key")
	assert.Error(t, err, "Get should fail with timed-out context")
}
