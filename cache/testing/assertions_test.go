package testing

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
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
	AssertOperationCount(t, mock, OpGet, 2)
	AssertOperationCount(t, mock, OpSet, 1)
}

// recordingT captures Errorf output so a helper's failure path can be asserted without
// failing the test that observes it.
type recordingT struct{ errors []string }

func (r *recordingT) Helper() {}

func (r *recordingT) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

var _ testReporter = (*recordingT)(nil)

// TestAssertOperationCountRejectsUnknownOperation pins the footgun #1298 closes: an
// unrecognized name used to read as zero calls, so `AssertOperationCount(t, m, "delete", 0)`
// passed without ever counting anything. Both helpers must fail the test and name every
// valid operation.
func TestAssertOperationCountRejectsUnknownOperation(t *testing.T) {
	tests := []struct {
		name   string
		assert func(tb testReporter, mock *MockCache)
	}{
		{name: "count", assert: func(tb testReporter, mock *MockCache) { assertOperationCount(tb, mock, "delete", 0) }},
		{name: "at_least", assert: func(tb testReporter, mock *MockCache) { assertOperationCountAtLeast(tb, mock, "delete", 0) }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingT{}
			tc.assert(rec, NewMockCache())

			require.Len(t, rec.errors, 1, "an unknown operation must fail the test, not read as zero")
			assert.Contains(t, rec.errors[0], `unknown cache operation "delete"`)
			for _, op := range operations {
				assert.Contains(t, rec.errors[0], op, "the message must list every valid operation")
			}
		})
	}
}

// TestOperationCountAcceptsLegacyAliases pins the compatibility half: "CAS" and "CAD" are
// shorthand for real counters, not misspellings, so they must keep resolving — dropping
// them would break a consumer's test with no migration path.
func TestOperationCountAcceptsLegacyAliases(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	_, err := mock.CompareAndSet(ctx, "key", nil, []byte("value"), time.Minute)
	require.NoError(t, err)
	_, err = mock.CompareAndDelete(ctx, "key", []byte("value"))
	require.NoError(t, err)

	assert.Equal(t, mock.OperationCount(OpCompareAndSet), mock.OperationCount("CAS"))
	assert.Equal(t, mock.OperationCount(OpCompareAndDelete), mock.OperationCount("CAD"))
	assert.Equal(t, int64(1), mock.OperationCount("CAS"))
	assert.Equal(t, int64(1), mock.OperationCount("CAD"))
}

// TestOperationCountPanicsOnUnknownOperation pins the raw accessor's half of #1298: outside
// an assertion there is no t to fail, so a misspelled name panics with the same message
// instead of reading as zero. The expected text is a literal, not the helper's output.
func TestOperationCountPanicsOnUnknownOperation(t *testing.T) {
	mock := NewMockCache()

	assert.PanicsWithValue(t,
		`cache/testing: unknown cache operation "delete"; valid operations: Get, Set, Delete, GetOrSet, CompareAndSet, CompareAndDelete, Health, Stats, Close`,
		func() { mock.OperationCount("delete") })
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
	AssertOperationCountAtLeast(t, mock, OpGet, 10)
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
	assert.Equal(t, int64(0), mock.OperationCount(OpGet))
	assert.Equal(t, int64(0), mock.OperationCount(OpSet))
}

func TestOperationCounts(t *testing.T) {
	ctx := context.Background()
	mock := NewMockCache()

	mock.Get(ctx, "key1")
	mock.Set(ctx, "key1", []byte("value"), time.Minute)
	mock.Delete(ctx, "key1")

	counts := OperationCounts(mock)
	assert.Equal(t, int64(1), counts[OpGet])
	assert.Equal(t, int64(1), counts[OpSet])
	assert.Equal(t, int64(1), counts[OpDelete])
}

// TestOperationCountsCoversEveryCacheMethod pins the counted set against cache.Cache in
// both directions: a method without an entry cannot be asserted on and AssertNoOperations,
// which sums OperationCounts, silently stops covering it; an entry without a method is a
// name nothing can ever count. Reflecting over the interface catches the next method
// without a new test.
func TestOperationCountsCoversEveryCacheMethod(t *testing.T) {
	iface := reflect.TypeOf((*cache.Cache)(nil)).Elem()
	methods := make([]string, 0, iface.NumMethod())
	for i := range iface.NumMethod() {
		methods = append(methods, iface.Method(i).Name)
	}

	assert.ElementsMatch(t, methods, operations)
	assert.ElementsMatch(t, methods, slices.Collect(maps.Keys(OperationCounts(NewMockCache()))))
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
	assert.Equal(t, int64(1), counts[OpCompareAndDelete])
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
