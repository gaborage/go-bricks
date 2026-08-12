package testing

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
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

	assert.Empty(t, mock.AllKeys())
	assert.Equal(t, int64(0), mock.OperationCount("Get"))
	assert.Equal(t, int64(0), mock.OperationCount("Set"))
}

func TestOperationCounts(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	mock.Get(ctx, "key1")
	mock.Set(ctx, "key1", []byte("value"), time.Minute)
	mock.Delete(ctx, "key1")

	counts := OperationCounts(mock)
	assert.Equal(t, int64(1), counts["Get"])
	assert.Equal(t, int64(1), counts["Set"])
	assert.Equal(t, int64(1), counts["Delete"])
}

// TestOperationCountsCoversEveryCacheMethod generalizes the guard below: every future
// cache.Cache method must land in OperationCounts, or AssertNoOperations silently stops
// covering it. Reflecting over the interface catches the next one without a new test.
func TestOperationCountsCoversEveryCacheMethod(t *testing.T) {
	iface := reflect.TypeOf((*cache.Cache)(nil)).Elem()
	counts := OperationCounts(NewMockCache())

	for i := range iface.NumMethod() {
		name := iface.Method(i).Name
		_, ok := counts[name]
		assert.True(t, ok, "OperationCounts is missing %s; AssertNoOperations would silently stop covering it", name)
	}
}

// TestOperationCountsIncludesCompareAndDelete guards AssertNoOperations, which sums
// exactly this map: a missing key reads as 0, so omitting the entry would let "the cache
// was never touched" pass despite a CompareAndDelete call. The reflection test above pins
// the key's presence; this one pins that the counter actually increments.
func TestOperationCountsIncludesCompareAndDelete(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	_, err := mock.CompareAndDelete(ctx, "key1", []byte("token"))
	require.NoError(t, err)

	counts := OperationCounts(mock)
	assert.Equal(t, int64(1), counts["CompareAndDelete"])
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
	stats, err := mock.Stats()
	require.NoError(t, err)

	// Should pass
	AssertStatsContains(t, stats, "id", "test-cache")
}
