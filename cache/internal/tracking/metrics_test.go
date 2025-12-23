package tracking

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func setupTestMeterProvider(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	ResetForTesting()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)

	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		ResetForTesting()
	})

	return reader
}

func TestRecordCacheOperationDuration(t *testing.T) {
	reader := setupTestMeterProvider(t)

	// Record a cache operation
	RecordCacheOperation(context.Background(), OpGet, 50*time.Millisecond, true, nil, "0")

	// Collect metrics
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	// Find duration histogram
	var foundDuration bool
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != cacheMeterName {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != metricCacheOperationDuration {
				continue
			}
			foundDuration = true
			hist, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok, "expected histogram data")
			require.NotEmpty(t, hist.DataPoints, "expected at least one data point")

			// Verify attributes
			dp := hist.DataPoints[0]
			attrs := dp.Attributes.ToSlice()
			assertAttribute(t, attrs, attrDBSystem, "redis")
			assertAttribute(t, attrs, attrDBOperation, "get")
			assertAttribute(t, attrs, attrDBNamespace, "0")
		}
	}

	assert.True(t, foundDuration, "expected to find db.client.operation.duration metric")
}

func TestRecordCacheHitMiss(t *testing.T) {
	reader := setupTestMeterProvider(t)

	// Record cache hits and misses
	RecordCacheOperation(context.Background(), OpGet, 10*time.Millisecond, true, nil, "")
	RecordCacheOperation(context.Background(), OpGet, 10*time.Millisecond, true, nil, "")
	RecordCacheOperation(context.Background(), OpGet, 10*time.Millisecond, false, nil, "")
	RecordCacheOperation(context.Background(), OpGetOrSet, 10*time.Millisecond, true, nil, "")
	RecordCacheOperation(context.Background(), OpGetOrSet, 10*time.Millisecond, false, nil, "")

	// Collect metrics
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	// Find hit and miss counters
	var hitCount, missCount int64
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != cacheMeterName {
			continue
		}
		for _, m := range sm.Metrics {
			switch m.Name {
			case metricCacheHit:
				sum, ok := m.Data.(metricdata.Sum[int64])
				require.True(t, ok)
				for _, dp := range sum.DataPoints {
					hitCount += dp.Value
				}
			case metricCacheMiss:
				sum, ok := m.Data.(metricdata.Sum[int64])
				require.True(t, ok)
				for _, dp := range sum.DataPoints {
					missCount += dp.Value
				}
			}
		}
	}

	assert.Equal(t, int64(3), hitCount, "expected 3 cache hits")
	assert.Equal(t, int64(2), missCount, "expected 2 cache misses")
}

func TestRecordCacheOperationWithError(t *testing.T) {
	reader := setupTestMeterProvider(t)

	// Record an operation with error
	testErr := errors.New("connection refused")
	RecordCacheOperation(context.Background(), OpSet, 100*time.Millisecond, false, testErr, "")

	// Collect metrics
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	// Find duration histogram with error attribute
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != cacheMeterName {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != metricCacheOperationDuration {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok)
			require.NotEmpty(t, hist.DataPoints)

			attrs := hist.DataPoints[0].Attributes.ToSlice()
			assertAttribute(t, attrs, attrErrorType, "connection_error")
			return
		}
	}
	t.Fatal("expected to find duration metric with error attribute")
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{name: "nil_error", err: nil, expected: ""},
		{name: "connection_error", err: errors.New("connection refused"), expected: "connection_error"},
		{name: "timeout_error", err: errors.New("context deadline exceeded timeout"), expected: "timeout"},
		{name: "closed_error", err: errors.New("cache closed"), expected: "closed"},
		{name: "not_found", err: errors.New("key not found"), expected: "not_found"},
		{name: "generic_error", err: errors.New("something went wrong"), expected: "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRegisterManagerMetrics(t *testing.T) {
	reader := setupTestMeterProvider(t)

	// Create a mock stats provider
	stats := ManagerMetricsStats{
		ActiveCaches: 5,
		TotalCreated: 10,
		Evictions:    2,
		IdleCleanups: 3,
		Errors:       1,
	}

	cleanup := RegisterManagerMetrics(func() ManagerMetricsStats {
		return stats
	}, "test-pool")
	defer cleanup()

	// Collect metrics
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	// Verify manager metrics
	foundMetrics := make(map[string]int64)
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != cacheMeterName {
			continue
		}
		for _, m := range sm.Metrics {
			switch m.Name {
			case metricCacheManagerActiveCaches,
				metricCacheManagerTotalCreated,
				metricCacheManagerEvictions,
				metricCacheManagerIdleCleanups,
				metricCacheManagerErrors:
				sum, ok := m.Data.(metricdata.Sum[int64])
				require.True(t, ok)
				if len(sum.DataPoints) > 0 {
					foundMetrics[m.Name] = sum.DataPoints[0].Value
				}
			}
		}
	}

	assert.Equal(t, int64(5), foundMetrics[metricCacheManagerActiveCaches])
	assert.Equal(t, int64(10), foundMetrics[metricCacheManagerTotalCreated])
	assert.Equal(t, int64(2), foundMetrics[metricCacheManagerEvictions])
	assert.Equal(t, int64(3), foundMetrics[metricCacheManagerIdleCleanups])
	assert.Equal(t, int64(1), foundMetrics[metricCacheManagerErrors])
}

func TestIsInitialized(t *testing.T) {
	ResetForTesting()
	assert.False(t, IsInitialized(), "should not be initialized after reset")

	ensureCacheMeterInitialized()
	assert.True(t, IsInitialized(), "should be initialized after ensureCacheMeterInitialized")
}

func TestNonGetOperationsDoNotRecordHitMiss(t *testing.T) {
	reader := setupTestMeterProvider(t)

	// Record Set and Delete operations (should not record hit/miss)
	RecordCacheOperation(context.Background(), OpSet, 10*time.Millisecond, true, nil, "")
	RecordCacheOperation(context.Background(), OpDelete, 10*time.Millisecond, false, nil, "")

	// Collect metrics
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	// Verify no hit/miss counters for non-Get operations
	var hitCount, missCount int64
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != cacheMeterName {
			continue
		}
		for _, m := range sm.Metrics {
			switch m.Name {
			case metricCacheHit:
				sum, ok := m.Data.(metricdata.Sum[int64])
				require.True(t, ok)
				for _, dp := range sum.DataPoints {
					hitCount += dp.Value
				}
			case metricCacheMiss:
				sum, ok := m.Data.(metricdata.Sum[int64])
				require.True(t, ok)
				for _, dp := range sum.DataPoints {
					missCount += dp.Value
				}
			}
		}
	}

	// Hit/miss should not be recorded for Set/Delete
	assert.Equal(t, int64(0), hitCount, "expected no cache hits for non-Get operations")
	assert.Equal(t, int64(0), missCount, "expected no cache misses for non-Get operations")
}

// assertAttribute checks that an attribute with the given key and value exists.
func assertAttribute(t *testing.T, attrs []attribute.KeyValue, key string, expectedValue any) {
	t.Helper()
	for _, kv := range attrs {
		if string(kv.Key) == key {
			switch ev := expectedValue.(type) {
			case int64:
				assert.Equal(t, ev, kv.Value.AsInt64(), "attribute %s value mismatch", key)
			case int:
				assert.Equal(t, int64(ev), kv.Value.AsInt64(), "attribute %s value mismatch", key)
			case string:
				assert.Equal(t, ev, kv.Value.AsString(), "attribute %s value mismatch", key)
			default:
				t.Errorf("unsupported expected value type for attribute %s", key)
			}
			return
		}
	}
	t.Errorf("attribute %s not found in %v", key, attrs)
}
