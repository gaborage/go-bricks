//go:build integration

package redis

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/cache"
)

// Note: These benchmarks require a running Redis instance on localhost:6379
// To run benchmarks: docker run -d -p 6379:6379 redis:7-alpine
// Or use: make test-integration (which starts containers automatically for tests)

// Benchmark constants
var (
	benchmarkValue = []byte("benchmark-value")
)

// setupBenchmarkRedis creates a Redis client for benchmarking.
// Assumes Redis is already running on localhost:6379.
func setupBenchmarkRedis(b *testing.B) (*Client, context.Context) {
	b.Helper()

	ctx := context.Background()

	cfg := &Config{
		Host:     "localhost",
		Port:     6379,
		Database: 0,
		PoolSize: 100, // Higher pool size for concurrent benchmarks
	}

	client, err := NewClient(cfg)
	if err != nil {
		b.Skip("Redis not available on localhost:6379, skipping benchmark")
		return nil, nil
	}

	// Test connection
	if client.Health(ctx) != nil {
		b.Skip("Redis not healthy, skipping benchmark")
		return nil, nil
	}

	return client, ctx
}

// =============================================================================
// Basic Operations Benchmarks
// =============================================================================

func BenchmarkRealRedisGet(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	defer client.Close()

	// Pre-populate key
	key := "benchmark:get:key"
	client.Set(ctx, key, benchmarkValue, 10*time.Minute)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := client.Get(ctx, key)
		if err != nil {
			b.Fatalf("Get failed: %v", err)
		}
	}
}

func BenchmarkRealRedisSet(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	defer client.Close()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("benchmark:set:key:%d", i)
		err := client.Set(ctx, key, benchmarkValue, time.Minute)
		if err != nil {
			b.Fatalf("Set failed: %v", err)
		}
	}
}

func BenchmarkRealRedisDelete(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	defer client.Close()

	// Pre-populate keys
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("benchmark:delete:key:%d", i)
		client.Set(ctx, key, benchmarkValue, 10*time.Minute)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("benchmark:delete:key:%d", i)
		err := client.Delete(ctx, key)
		if err != nil {
			b.Fatalf("Delete failed: %v", err)
		}
	}
}

// =============================================================================
// Atomic Operations Benchmarks
// =============================================================================

func BenchmarkRealRedisGetOrSet(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	defer client.Close()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("benchmark:getorset:key:%d", i)
		_, _, err := client.GetOrSet(ctx, key, benchmarkValue, time.Minute)
		if err != nil {
			b.Fatalf("GetOrSet failed: %v", err)
		}
	}
}

func BenchmarkRealRedisGetOrSetExisting(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	defer client.Close()

	// Pre-populate key
	key := "benchmark:getorset:existing"
	value := []byte("existing-value")
	client.Set(ctx, key, value, 10*time.Minute)

	newValue := []byte(testNewValue)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _, err := client.GetOrSet(ctx, key, newValue, time.Minute)
		if err != nil {
			b.Fatalf("GetOrSet failed: %v", err)
		}
	}
}

func BenchmarkRealRedisCompareAndSet(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	defer client.Close()

	// Pre-populate keys
	oldValue := []byte("old-value")
	newValue := []byte(testNewValue)

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("benchmark:cas:key:%d", i)
		client.Set(ctx, key, oldValue, 10*time.Minute)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("benchmark:cas:key:%d", i)
		_, err := client.CompareAndSet(ctx, key, oldValue, newValue, time.Minute)
		if err != nil {
			b.Fatalf("CompareAndSet failed: %v", err)
		}
	}
}

func BenchmarkRealRedisCompareAndSetLock(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	defer client.Close()

	value := []byte("lock-value")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("benchmark:cas:lock:%d", i)
		// Acquire lock (expectedValue = nil)
		_, err := client.CompareAndSet(ctx, key, nil, value, time.Minute)
		if err != nil {
			b.Fatalf("CompareAndSet lock failed: %v", err)
		}
	}
}

// =============================================================================
// Concurrent Throughput Benchmarks
// =============================================================================

func BenchmarkRealRedisParallelGet(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	defer client.Close()

	// Pre-populate keys
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("benchmark:parallel:key:%d", i)
		client.Set(ctx, key, []byte("value"), 10*time.Minute)
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("benchmark:parallel:key:%d", i%1000)
			_, err := client.Get(ctx, key)
			if err != nil {
				b.Fatalf("Parallel Get failed: %v", err)
			}
			i++
		}
	})
}

func BenchmarkRealRedisParallelSet(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	defer client.Close()

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("benchmark:parallel:set:%d", i)
			err := client.Set(ctx, key, benchmarkValue, time.Minute)
			if err != nil {
				b.Fatalf("Parallel Set failed: %v", err)
			}
			i++
		}
	})
}

func BenchmarkRealRedisParallelGetSet(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	defer client.Close()

	// Pre-populate some keys
	for i := 0; i < 500; i++ {
		key := fmt.Sprintf("benchmark:parallel:getset:%d", i)
		client.Set(ctx, key, []byte("value"), 10*time.Minute)
	}

	value := []byte(testNewValue)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("benchmark:parallel:getset:%d", i%1000)

			// 50% reads, 50% writes
			if i%2 == 0 {
				_, err := client.Get(ctx, key)
				if err != nil && !errors.Is(err, cache.ErrNotFound) {
					b.Fatalf("Parallel Get failed: %v", err)
				}
			} else {
				err := client.Set(ctx, key, value, time.Minute)
				if err != nil {
					b.Fatalf("Parallel Set failed: %v", err)
				}
			}
			i++
		}
	})
}

// =============================================================================
// Connection Pool Benchmarks
// =============================================================================

func BenchmarkRealRedisConnectionPool(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	defer client.Close()

	value := []byte("pool-test-value")

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("benchmark:pool:key:%d", i)

			// Mix of operations to stress pool
			var err error
			switch i % 4 {
			case 0:
				err = client.Set(ctx, key, value, time.Minute)
			case 1:
				_, err = client.Get(ctx, key)
			case 2:
				err = client.Delete(ctx, key)
			case 3:
				_, _, err = client.GetOrSet(ctx, key, value, time.Minute)
			}

			if err != nil {
				b.Errorf("operation %d failed: %v", i%4, err)
			}

			i++
		}
	})
}

// =============================================================================
// Large Payload Benchmarks
// =============================================================================

func BenchmarkRealRedisSetLargePayload(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	defer client.Close()

	// 100KB payload
	largeValue := make([]byte, 100*1024)
	for i := range largeValue {
		largeValue[i] = byte(i % 256)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("benchmark:large:key:%d", i)
		err := client.Set(ctx, key, largeValue, time.Minute)
		if err != nil {
			b.Fatalf("Set large payload failed: %v", err)
		}
	}
}

func BenchmarkRealRedisGetLargePayload(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	defer client.Close()

	// 100KB payload
	largeValue := make([]byte, 100*1024)
	for i := range largeValue {
		largeValue[i] = byte(i % 256)
	}

	key := "benchmark:large:get"
	client.Set(ctx, key, largeValue, 10*time.Minute)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := client.Get(ctx, key)
		if err != nil {
			b.Fatalf("Get large payload failed: %v", err)
		}
	}
}

// =============================================================================
// Health & Stats Benchmarks
// =============================================================================

func BenchmarkRealRedisHealth(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	defer client.Close()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		err := client.Health(ctx)
		if err != nil {
			b.Fatalf("Health check failed: %v", err)
		}
	}
}

func BenchmarkRealRedisStats(b *testing.B) {
	client, ctx := setupBenchmarkRedis(b)
	_ = ctx // ctx not needed for Stats benchmark
	defer client.Close()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := client.Stats()
		if err != nil {
			b.Fatalf("Stats failed: %v", err)
		}
	}
}
