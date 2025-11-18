package testing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAssertCacheHit(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()
	mock.Set(ctx, "key1", []byte("value1"), time.Minute)

	// Should pass
	AssertCacheHit(t, mock, "key1")
}

func TestAssertCacheMiss(t *testing.T) {
	mock := NewMockCache()

	// Should pass
	AssertCacheMiss(t, mock, "missing-key")
}

func TestAssertOperationCount(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	mock.Get(ctx, "key1")
	mock.Get(ctx, "key2")
	mock.Set(ctx, "key1", []byte("value"), time.Minute)

	// Should pass
	AssertOperationCount(t, mock, "Get", 2)
	AssertOperationCount(t, mock, "Set", 1)
}

func TestAssertOperationCountGreaterThan(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	for i := 0; i < 10; i++ {
		mock.Get(ctx, "key")
	}

	// Should pass - old name (deprecated) still works
	AssertOperationCountGreaterThan(t, mock, "Get", 5)

	// New name - clearer semantics
	AssertOperationCountAtLeast(t, mock, "Get", 10)
}

func TestAssertCacheClosed(t *testing.T) {
	mock := NewMockCache()
	mock.Close()

	// Should pass
	AssertCacheClosed(t, mock)
}

func TestAssertCacheOpen(t *testing.T) {
	mock := NewMockCache()

	// Should pass
	AssertCacheOpen(t, mock)
}

func TestAssertKeyExists(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()
	mock.Set(ctx, "key1", []byte("value1"), time.Minute)

	// Should pass
	AssertKeyExists(t, mock, "key1")
}

func TestAssertKeyNotExists(t *testing.T) {
	mock := NewMockCache()

	// Should pass
	AssertKeyNotExists(t, mock, "missing-key")
}

func TestAssertCacheEmpty(t *testing.T) {
	mock := NewMockCache()

	// Should pass
	AssertCacheEmpty(t, mock)
}

func TestAssertCacheSize(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	mock.Set(ctx, "key1", []byte("value1"), time.Minute)
	mock.Set(ctx, "key2", []byte("value2"), time.Minute)

	// Should pass
	AssertCacheSize(t, mock, 2)
}

func TestAssertValue(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()
	mock.Set(ctx, "key1", []byte("expected-value"), time.Minute)

	// Should pass
	AssertValue(t, mock, "key1", []byte("expected-value"))
}

func TestDumpCache(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCacheWithID("test-cache")
	mock.Set(ctx, "key1", []byte("value1"), time.Minute)

	dump := DumpCache(mock)
	assert.Contains(t, dump, "test-cache")
	assert.Contains(t, dump, "key1")
}

func TestResetMock(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	mock.Set(ctx, "key1", []byte("value1"), time.Minute)
	mock.Get(ctx, "key1")

	ResetMock(mock)

	assert.Empty(t, mock.GetAllKeys())
	assert.Equal(t, int64(0), mock.GetOperationCount("Get"))
	assert.Equal(t, int64(0), mock.GetOperationCount("Set"))
}

func TestGetOperationCounts(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	mock.Get(ctx, "key1")
	mock.Set(ctx, "key1", []byte("value"), time.Minute)
	mock.Delete(ctx, "key1")

	counts := GetOperationCounts(mock)
	assert.Equal(t, int64(1), counts["Get"])
	assert.Equal(t, int64(1), counts["Set"])
	assert.Equal(t, int64(1), counts["Delete"])
}

func TestAssertNoOperations(t *testing.T) {
	mock := NewMockCache()

	// Should pass
	AssertNoOperations(t, mock)
}

func TestAssertGetValue(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()
	mock.Set(ctx, "key1", []byte("value1"), time.Minute)

	value := AssertGetValue(t, mock, "key1")
	assert.Equal(t, []byte("value1"), value)
}

func TestAssertError(t *testing.T) {
	customErr := errors.New("custom error")
	mock := NewMockCache().WithGetFailure(customErr)

	// Should pass
	AssertError(t, func() error {
		_, err := mock.Get(context.Background(), "key")
		return err
	}, customErr)
}

func TestAssertNoError(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	// Should pass
	AssertNoError(t, func() error {
		return mock.Set(ctx, "key", []byte("value"), time.Minute)
	})
}

func TestAssertStatsContains(t *testing.T) {
	mock := NewMockCacheWithID("test-cache")
	stats, _ := mock.Stats()

	// Should pass
	AssertStatsContains(t, stats, "id", "test-cache")
}
