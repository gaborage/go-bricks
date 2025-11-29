package testing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gaborage/go-bricks/cache"
)

// AssertCacheHit asserts that a key exists in the cache and can be retrieved successfully.
//
// Example:
//
//	mock := NewMockCache()
//	mock.Set(ctx, "user:123", []byte("data"), time.Minute)
//	AssertCacheHit(t, mock, "user:123")
func AssertCacheHit(t *testing.T, c cache.Cache, key string) {
	t.Helper()

	ctx := context.Background()
	_, err := c.Get(ctx, key)
	if err != nil {
		t.Errorf("expected cache hit for key %q, got error: %v", key, err)
	}
}

// AssertCacheMiss asserts that a key does not exist in the cache.
//
// Example:
//
//	mock := NewMockCache()
//	AssertCacheMiss(t, mock, "missing:key")
func AssertCacheMiss(t *testing.T, c cache.Cache, key string) {
	t.Helper()

	ctx := context.Background()
	_, err := c.Get(ctx, key)
	if !errors.Is(err, cache.ErrNotFound) {
		t.Errorf("expected cache miss (ErrNotFound) for key %q, got: %v", key, err)
	}
}

// AssertOperationCount asserts that a specific operation was called a certain number of times.
// Only works with MockCache instances.
//
// Supported operations: "Get", "Set", "Delete", "GetOrSet", "CompareAndSet", "Health", "Stats", "Close"
//
// Example:
//
//	mock := NewMockCache()
//	// ... perform operations ...
//	AssertOperationCount(t, mock, "Get", 5)
func AssertOperationCount(t *testing.T, mock *MockCache, operation string, expected int64) {
	t.Helper()

	actual := mock.OperationCount(operation)
	if actual != expected {
		t.Errorf("expected %d %s operations, got %d", expected, operation, actual)
	}
}

// AssertOperationCountAtLeast asserts that a specific operation was called at least a certain number of times.
// Only works with MockCache instances.
//
// Example:
//
//	mock := NewMockCache()
//	// ... perform operations ...
//	AssertOperationCountAtLeast(t, mock, "Get", 10)
func AssertOperationCountAtLeast(t *testing.T, mock *MockCache, operation string, minimum int64) {
	t.Helper()

	actual := mock.OperationCount(operation)
	if actual < minimum {
		t.Errorf("expected at least %d %s operations, got %d", minimum, operation, actual)
	}
}

// Deprecated: Use AssertOperationCountAtLeast instead.
// AssertOperationCountGreaterThan is kept for backward compatibility.
func AssertOperationCountGreaterThan(t *testing.T, mock *MockCache, operation string, minimum int64) {
	AssertOperationCountAtLeast(t, mock, operation, minimum)
}

// AssertCacheClosed asserts that the cache has been closed.
// Only works with MockCache instances.
//
// Example:
//
//	mock := NewMockCache()
//	mock.Close()
//	AssertCacheClosed(t, mock)
func AssertCacheClosed(t *testing.T, mock *MockCache) {
	t.Helper()

	if !mock.IsClosed() {
		t.Error("expected cache to be closed, but it is still open")
	}
}

// AssertCacheOpen asserts that the cache has NOT been closed.
// Only works with MockCache instances.
//
// Example:
//
//	mock := NewMockCache()
//	AssertCacheOpen(t, mock)
func AssertCacheOpen(t *testing.T, mock *MockCache) {
	t.Helper()

	if mock.IsClosed() {
		t.Error("expected cache to be open, but it is closed")
	}
}

// AssertKeyExists asserts that a key exists in the cache storage (ignoring expiration).
// Only works with MockCache instances.
//
// Example:
//
//	mock := NewMockCache()
//	mock.Set(ctx, "key", []byte("value"), time.Nanosecond)
//	time.Sleep(time.Millisecond)
//	AssertKeyExists(t, mock, "key") // Still exists even if expired
func AssertKeyExists(t *testing.T, mock *MockCache, key string) {
	t.Helper()

	if !mock.Has(key) {
		t.Errorf("expected key %q to exist in cache storage, but it was not found\nCache dump:\n%s",
			key, mock.Dump())
	}
}

// AssertKeyNotExists asserts that a key does not exist in cache storage.
// Only works with MockCache instances.
//
// Example:
//
//	mock := NewMockCache()
//	AssertKeyNotExists(t, mock, "missing:key")
func AssertKeyNotExists(t *testing.T, mock *MockCache, key string) {
	t.Helper()

	if mock.Has(key) {
		t.Errorf("expected key %q to not exist in cache storage, but it was found\nCache dump:\n%s",
			key, mock.Dump())
	}
}

// AssertCacheEmpty asserts that the cache contains no entries.
// Only works with MockCache instances.
//
// Example:
//
//	mock := NewMockCache()
//	mock.Clear()
//	AssertCacheEmpty(t, mock)
func AssertCacheEmpty(t *testing.T, mock *MockCache) {
	t.Helper()

	keys := mock.AllKeys()
	if len(keys) > 0 {
		t.Errorf("expected cache to be empty, but found %d keys: %v\nCache dump:\n%s",
			len(keys), keys, mock.Dump())
	}
}

// AssertCacheSize asserts that the cache contains a specific number of entries.
// Only works with MockCache instances.
//
// Example:
//
//	mock := NewMockCache()
//	mock.Set(ctx, "key1", []byte("value1"), time.Minute)
//	mock.Set(ctx, "key2", []byte("value2"), time.Minute)
//	AssertCacheSize(t, mock, 2)
func AssertCacheSize(t *testing.T, mock *MockCache, expected int) {
	t.Helper()

	keys := mock.AllKeys()
	actual := len(keys)
	if actual != expected {
		t.Errorf("expected cache to contain %d entries, got %d\nKeys: %v\nCache dump:\n%s",
			expected, actual, keys, mock.Dump())
	}
}

// AssertValue asserts that a cache key contains a specific value.
//
// Example:
//
//	mock := NewMockCache()
//	mock.Set(ctx, "user:123", []byte("Alice"), time.Minute)
//	AssertValue(t, mock, "user:123", []byte("Alice"))
func AssertValue(t *testing.T, c cache.Cache, key string, expected []byte) {
	t.Helper()

	ctx := context.Background()
	actual, err := c.Get(ctx, key)
	if err != nil {
		t.Errorf("failed to get value for key %q: %v", key, err)
		return
	}

	if !bytes.Equal(actual, expected) {
		t.Errorf("expected value %q for key %q, got %q", string(expected), key, string(actual))
	}
}

// DumpCache returns a formatted string of cache contents for debugging.
// Only works with MockCache instances.
//
// Example:
//
//	mock := NewMockCache()
//	// ... test operations ...
//	t.Log(DumpCache(mock))  // Print cache state
func DumpCache(mock *MockCache) string {
	return mock.Dump()
}

// ResetMock resets a MockCache to initial state (clears data and counters).
// Only works with MockCache instances.
//
// Example:
//
//	mock := NewMockCache()
//	// ... test operations ...
//	ResetMock(mock)  // Clean slate for next test
func ResetMock(mock *MockCache) {
	mock.Clear()
	mock.ResetCounters()
}

// OperationCounts returns a map of all operation counts for debugging.
// Only works with MockCache instances.
//
// Example:
//
//	mock := NewMockCache()
//	// ... test operations ...
//	counts := OperationCounts(mock)
//	fmt.Printf("Operations: %+v\n", counts)
func OperationCounts(mock *MockCache) map[string]int64 {
	return map[string]int64{
		"Get":           mock.OperationCount("Get"),
		"Set":           mock.OperationCount("Set"),
		"Delete":        mock.OperationCount("Delete"),
		"GetOrSet":      mock.OperationCount("GetOrSet"),
		"CompareAndSet": mock.OperationCount("CompareAndSet"),
		"Health":        mock.OperationCount("Health"),
		"Stats":         mock.OperationCount("Stats"),
		"Close":         mock.OperationCount("Close"),
	}
}

// AssertNoOperations asserts that no cache operations were performed.
// Only works with MockCache instances.
//
// Example:
//
//	mock := NewMockCache()
//	// ... test code that should NOT use cache ...
//	AssertNoOperations(t, mock)
func AssertNoOperations(t *testing.T, mock *MockCache) {
	t.Helper()

	counts := OperationCounts(mock)
	total := int64(0)
	for _, count := range counts {
		total += count
	}

	if total > 0 {
		t.Errorf("expected no cache operations, but found %d operations: %+v",
			total, counts)
	}
}

// AssertGetValue is a convenience function that combines Get and value assertion.
//
// Example:
//
//	mock := NewMockCache()
//	mock.Set(ctx, "key", []byte("value"), time.Minute)
//	value := AssertGetValue(t, mock, "key")
//	// Use value for further assertions...
func AssertGetValue(t *testing.T, c cache.Cache, key string) []byte {
	t.Helper()

	ctx := context.Background()
	value, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("failed to get value for key %q: %v", key, err)
	}

	return value
}

// AssertError asserts that a cache operation returns a specific error.
//
// Example:
//
//	mock := NewMockCache().WithGetFailure(cache.ErrConnectionError)
//	AssertError(t, func() error {
//	    _, err := mock.Get(context.Background(), "key")
//	    return err
//	}, cache.ErrConnectionError)
func AssertError(t *testing.T, operation func() error, expected error) {
	t.Helper()

	err := operation()
	if !errors.Is(err, expected) {
		t.Errorf("expected error %v, got %v", expected, err)
	}
}

// AssertNoError asserts that a cache operation succeeds without error.
//
// Example:
//
//	mock := NewMockCache()
//	AssertNoError(t, func() error {
//	    return mock.Set(context.Background(), "key", []byte("value"), time.Minute)
//	})
func AssertNoError(t *testing.T, operation func() error) {
	t.Helper()

	err := operation()
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

// AssertStats asserts that cache stats contain expected key-value pairs.
// Only works with MockCache instances.
//
// Example:
//
//	mock := NewMockCacheWithID("test-cache")
//	stats, _ := mock.Stats()
//	AssertStatsContains(t, stats, "id", "test-cache")
func AssertStatsContains(t *testing.T, stats map[string]any, key string, expected any) {
	t.Helper()

	actual, ok := stats[key]
	if !ok {
		t.Errorf("expected stats to contain key %q, but it was not found\nStats: %+v", key, stats)
		return
	}

	if fmt.Sprintf("%v", actual) != fmt.Sprintf("%v", expected) {
		t.Errorf("expected stats[%q] = %v, got %v", key, expected, actual)
	}
}
