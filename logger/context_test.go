package logger

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

type testContextKey string

func TestWithAMQPCounter(t *testing.T) {
	existingKey := testContextKey("existing_key")

	tests := []struct {
		name string
		ctx  context.Context
	}{
		{
			name: "with_background_context",
			ctx:  context.Background(),
		},
		{
			name: "with_existing_context_values",
			ctx:  context.WithValue(context.Background(), existingKey, "existing_value"),
		},
		{
			name: "with_nil_context",
			ctx:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ctx context.Context
			if tt.ctx != nil {
				ctx = WithAMQPCounter(tt.ctx)
			} else {
				ctx = WithAMQPCounter(context.Background())
			}

			// Verify counter is initialized to 0
			assert.Equal(t, int64(0), GetAMQPCounter(ctx))
			assert.Equal(t, int64(0), GetAMQPElapsed(ctx))

			// Verify existing context values are preserved
			if tt.name == "with_existing_context_values" {
				assert.Equal(t, "existing_value", ctx.Value(existingKey))
			}
		})
	}
}

func TestWithDBCounter(t *testing.T) {
	existingKey := testContextKey("existing_key")

	tests := []struct {
		name string
		ctx  context.Context
	}{
		{
			name: "with_background_context",
			ctx:  context.Background(),
		},
		{
			name: "with_existing_context_values",
			ctx:  context.WithValue(context.Background(), existingKey, "existing_value"),
		},
		{
			name: "with_nil_context",
			ctx:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ctx context.Context
			if tt.ctx != nil {
				ctx = WithDBCounter(tt.ctx)
			} else {
				ctx = WithDBCounter(context.Background())
			}

			// Verify counter is initialized to 0
			assert.Equal(t, int64(0), GetDBCounter(ctx))
			assert.Equal(t, int64(0), GetDBElapsed(ctx))

			// Verify existing context values are preserved
			if tt.name == "with_existing_context_values" {
				assert.Equal(t, "existing_value", ctx.Value(existingKey))
			}
		})
	}
}

func TestAMQPCounterOperations(t *testing.T) {
	ctx := WithAMQPCounter(context.Background())

	// Test initial state
	assert.Equal(t, int64(0), GetAMQPCounter(ctx))

	// Test single increment
	IncrementAMQPCounter(ctx)
	assert.Equal(t, int64(1), GetAMQPCounter(ctx))

	// Test multiple increments
	IncrementAMQPCounter(ctx)
	IncrementAMQPCounter(ctx)
	IncrementAMQPCounter(ctx)
	assert.Equal(t, int64(4), GetAMQPCounter(ctx))
}

func TestDBCounterOperations(t *testing.T) {
	ctx := WithDBCounter(context.Background())

	// Test initial state
	assert.Equal(t, int64(0), GetDBCounter(ctx))

	// Test single increment
	IncrementDBCounter(ctx)
	assert.Equal(t, int64(1), GetDBCounter(ctx))

	// Test multiple increments
	IncrementDBCounter(ctx)
	IncrementDBCounter(ctx)
	IncrementDBCounter(ctx)
	assert.Equal(t, int64(4), GetDBCounter(ctx))
}

func TestAMQPElapsedOperations(t *testing.T) {
	ctx := WithAMQPCounter(context.Background())

	// Test initial state
	assert.Equal(t, int64(0), GetAMQPElapsed(ctx))

	// Test single addition
	AddAMQPElapsed(ctx, 1000000) // 1ms in nanoseconds
	assert.Equal(t, int64(1000000), GetAMQPElapsed(ctx))

	// Test multiple additions
	AddAMQPElapsed(ctx, 500000)                          // 0.5ms
	AddAMQPElapsed(ctx, 2000000)                         // 2ms
	assert.Equal(t, int64(3500000), GetAMQPElapsed(ctx)) // Total: 3.5ms

	// Test adding negative values
	AddAMQPElapsed(ctx, -1000000)                        // Subtract 1ms
	assert.Equal(t, int64(2500000), GetAMQPElapsed(ctx)) // Total: 2.5ms
}

func TestDBElapsedOperations(t *testing.T) {
	ctx := WithDBCounter(context.Background())

	// Test initial state
	assert.Equal(t, int64(0), GetDBElapsed(ctx))

	// Test single addition
	AddDBElapsed(ctx, 2000000) // 2ms in nanoseconds
	assert.Equal(t, int64(2000000), GetDBElapsed(ctx))

	// Test multiple additions
	AddDBElapsed(ctx, 800000)                          // 0.8ms
	AddDBElapsed(ctx, 1200000)                         // 1.2ms
	assert.Equal(t, int64(4000000), GetDBElapsed(ctx)) // Total: 4ms

	// Test adding negative values
	AddDBElapsed(ctx, -500000)                         // Subtract 0.5ms
	assert.Equal(t, int64(3500000), GetDBElapsed(ctx)) // Total: 3.5ms
}

func TestCombinedCounters(t *testing.T) {
	// Test that both AMQP and DB counters can coexist
	ctx := WithAMQPCounter(context.Background())
	ctx = WithDBCounter(ctx)

	// Verify all counters are initialized
	assert.Equal(t, int64(0), GetAMQPCounter(ctx))
	assert.Equal(t, int64(0), GetDBCounter(ctx))
	assert.Equal(t, int64(0), GetAMQPElapsed(ctx))
	assert.Equal(t, int64(0), GetDBElapsed(ctx))

	// Test AMQP operations
	IncrementAMQPCounter(ctx)
	IncrementAMQPCounter(ctx)
	AddAMQPElapsed(ctx, 1500000) // 1.5ms

	// Test DB operations
	IncrementDBCounter(ctx)
	IncrementDBCounter(ctx)
	IncrementDBCounter(ctx)
	AddDBElapsed(ctx, 2500000) // 2.5ms

	// Verify independent operation
	assert.Equal(t, int64(2), GetAMQPCounter(ctx))
	assert.Equal(t, int64(3), GetDBCounter(ctx))
	assert.Equal(t, int64(1500000), GetAMQPElapsed(ctx))
	assert.Equal(t, int64(2500000), GetDBElapsed(ctx))
}

func TestCountersWithoutInitialization(t *testing.T) {
	// Test operations on context without proper initialization
	ctx := context.Background()

	// All operations should be safe and return 0
	assert.Equal(t, int64(0), GetAMQPCounter(ctx))
	assert.Equal(t, int64(0), GetDBCounter(ctx))
	assert.Equal(t, int64(0), GetAMQPElapsed(ctx))
	assert.Equal(t, int64(0), GetDBElapsed(ctx))

	// Increment operations should be safe no-ops
	IncrementAMQPCounter(ctx)
	IncrementDBCounter(ctx)
	AddAMQPElapsed(ctx, 1000000)
	AddDBElapsed(ctx, 2000000)

	// Values should still be 0
	assert.Equal(t, int64(0), GetAMQPCounter(ctx))
	assert.Equal(t, int64(0), GetDBCounter(ctx))
	assert.Equal(t, int64(0), GetAMQPElapsed(ctx))
	assert.Equal(t, int64(0), GetDBElapsed(ctx))
}

func TestConcurrentAMQPOperations(t *testing.T) {
	ctx := WithAMQPCounter(context.Background())

	// Number of goroutines and operations per goroutine
	numGoroutines := 100
	numOperationsPerGoroutine := 50
	expectedCount := int64(numGoroutines * numOperationsPerGoroutine)
	expectedElapsed := int64(numGoroutines * numOperationsPerGoroutine * 1000) // 1000ns per operation

	var wg sync.WaitGroup

	// Start goroutines for counter increments
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOperationsPerGoroutine; j++ {
				IncrementAMQPCounter(ctx)
				AddAMQPElapsed(ctx, 1000) // Add 1000ns
			}
		}()
	}

	wg.Wait()

	// Verify final counts
	assert.Equal(t, expectedCount, GetAMQPCounter(ctx))
	assert.Equal(t, expectedElapsed, GetAMQPElapsed(ctx))
}

func TestConcurrentDBOperations(t *testing.T) {
	ctx := WithDBCounter(context.Background())

	// Number of goroutines and operations per goroutine
	numGoroutines := 100
	numOperationsPerGoroutine := 50
	expectedCount := int64(numGoroutines * numOperationsPerGoroutine)
	expectedElapsed := int64(numGoroutines * numOperationsPerGoroutine * 2000) // 2000ns per operation

	var wg sync.WaitGroup

	// Start goroutines for counter increments
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOperationsPerGoroutine; j++ {
				IncrementDBCounter(ctx)
				AddDBElapsed(ctx, 2000) // Add 2000ns
			}
		}()
	}

	wg.Wait()

	// Verify final counts
	assert.Equal(t, expectedCount, GetDBCounter(ctx))
	assert.Equal(t, expectedElapsed, GetDBElapsed(ctx))
}

func TestConcurrentMixedOperations(t *testing.T) {
	ctx := WithAMQPCounter(context.Background())
	ctx = WithDBCounter(ctx)

	numGoroutines := 50
	numOperationsPerGoroutine := 25
	expectedCount := int64(numGoroutines * numOperationsPerGoroutine)

	var wg sync.WaitGroup

	// Start goroutines for AMQP operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOperationsPerGoroutine; j++ {
				IncrementAMQPCounter(ctx)
				AddAMQPElapsed(ctx, 1000)
			}
		}()
	}

	// Start goroutines for DB operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOperationsPerGoroutine; j++ {
				IncrementDBCounter(ctx)
				AddDBElapsed(ctx, 2000)
			}
		}()
	}

	wg.Wait()

	// Verify final counts for both counters
	assert.Equal(t, expectedCount, GetAMQPCounter(ctx))
	assert.Equal(t, expectedCount, GetDBCounter(ctx))
	assert.Equal(t, expectedCount*1000, GetAMQPElapsed(ctx))
	assert.Equal(t, expectedCount*2000, GetDBElapsed(ctx))
}

func TestContextKeyUniqueness(t *testing.T) {
	// Verify that our context keys don't collide with user keys
	userKey1 := testContextKey("amqp_message_counter")
	userKey2 := testContextKey("db_operation_counter")
	userKey3 := testContextKey("amqp_elapsed_nanos")
	userKey4 := testContextKey("db_elapsed_nanos")

	ctx := context.Background()
	ctx = context.WithValue(ctx, userKey1, "user_value")
	ctx = context.WithValue(ctx, userKey2, "user_value")
	ctx = context.WithValue(ctx, userKey3, "user_value")
	ctx = context.WithValue(ctx, userKey4, "user_value")

	// Add our counters
	ctx = WithAMQPCounter(ctx)
	ctx = WithDBCounter(ctx)

	// User values should be preserved
	assert.Equal(t, "user_value", ctx.Value(userKey1))
	assert.Equal(t, "user_value", ctx.Value(userKey2))
	assert.Equal(t, "user_value", ctx.Value(userKey3))
	assert.Equal(t, "user_value", ctx.Value(userKey4))

	// Our counters should work independently
	assert.Equal(t, int64(0), GetAMQPCounter(ctx))
	assert.Equal(t, int64(0), GetDBCounter(ctx))
	assert.Equal(t, int64(0), GetAMQPElapsed(ctx))
	assert.Equal(t, int64(0), GetDBElapsed(ctx))
}

func TestLargeElapsedValues(t *testing.T) {
	ctx := WithAMQPCounter(context.Background())
	ctx = WithDBCounter(ctx)

	// Test with very large values (simulating long-running operations)
	largeValue := int64(9223372036854775807) // Max int64 value

	AddAMQPElapsed(ctx, largeValue)
	AddDBElapsed(ctx, largeValue)

	assert.Equal(t, largeValue, GetAMQPElapsed(ctx))
	assert.Equal(t, largeValue, GetDBElapsed(ctx))

	// Test overflow behavior by adding 1 more (should wrap around)
	AddAMQPElapsed(ctx, 1)
	AddDBElapsed(ctx, 1)

	// Values should wrap around to negative due to int64 overflow
	assert.Equal(t, int64(-9223372036854775808), GetAMQPElapsed(ctx))
	assert.Equal(t, int64(-9223372036854775808), GetDBElapsed(ctx))
}
