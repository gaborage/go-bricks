package observability

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const (
	testMeterName   = "test-meter"
	testAPIEndpoint = "/api/users"
)

func TestCreateCounter(t *testing.T) {
	// Create a manual reader for testing
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter(testMeterName)

	// Create a counter using the helper function
	counter, err := CreateCounter(meter, "test.counter", "Test counter description")
	require.NoError(t, err)
	assert.NotNil(t, counter)

	// Record some values
	ctx := context.Background()
	counter.Add(ctx, 5, metric.WithAttributes(attribute.String("key", "value1")))
	counter.Add(ctx, 10, metric.WithAttributes(attribute.String("key", "value2")))

	// Collect metrics
	var rm metricdata.ResourceMetrics
	err = reader.Collect(ctx, &rm)
	require.NoError(t, err)

	// Verify we got metrics
	require.Len(t, rm.ScopeMetrics, 1)
	require.Len(t, rm.ScopeMetrics[0].Metrics, 1)

	// Verify metric name and description
	metricRecord := rm.ScopeMetrics[0].Metrics[0]
	assert.Equal(t, "test.counter", metricRecord.Name)
	assert.Equal(t, "Test counter description", metricRecord.Description)

	// Verify data type
	sum, ok := metricRecord.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data type")

	// Verify monotonicity (counters are always monotonic)
	assert.True(t, sum.IsMonotonic)

	// Verify data points
 	// Verify data points
 	require.Len(t, sum.DataPoints, 2)
-	assert.Equal(t, int64(5), sum.DataPoints[0].Value)
	valuesByKey := make(map[string]int64, len(sum.DataPoints))
	for _, dp := range sum.DataPoints {
		attrVal, ok := dp.Attributes.Value("key")
		require.True(t, ok, "missing expected attribute 'key'")
		valuesByKey[attrVal.AsString()] = dp.Value
	}

	assert.Equal(t, int64(5), valuesByKey["value1"])
	assert.Equal(t, int64(10), valuesByKey["value2"])
 }

func TestCreateHistogram(t *testing.T) {
	// Create a manual reader for testing
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter(testMeterName)

	// Create a histogram using the helper function
	histogram, err := CreateHistogram(meter, "test.histogram", "Test histogram description")
	require.NoError(t, err)
	assert.NotNil(t, histogram)

	// Record some values
	ctx := context.Background()
	histogram.Record(ctx, 10.5, metric.WithAttributes(attribute.String("endpoint", testAPIEndpoint)))
	histogram.Record(ctx, 25.3, metric.WithAttributes(attribute.String("endpoint", testAPIEndpoint)))
	histogram.Record(ctx, 15.7, metric.WithAttributes(attribute.String("endpoint", testAPIEndpoint)))

	// Collect metrics
	var rm metricdata.ResourceMetrics
	err = reader.Collect(ctx, &rm)
	require.NoError(t, err)

	// Verify we got metrics
	require.Len(t, rm.ScopeMetrics, 1)
	require.Len(t, rm.ScopeMetrics[0].Metrics, 1)

	// Verify metric name and description
	metricRecord := rm.ScopeMetrics[0].Metrics[0]
	assert.Equal(t, "test.histogram", metricRecord.Name)
	assert.Equal(t, "Test histogram description", metricRecord.Description)

	// Verify data type
	hist, ok := metricRecord.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "expected Histogram[float64] data type")

	// Verify data points
	require.Len(t, hist.DataPoints, 1)
	dp := hist.DataPoints[0]
	assert.Equal(t, uint64(3), dp.Count) // 3 recordings
	assert.InDelta(t, 51.5, dp.Sum, 0.1) // Sum of values

	// Check min and max values
	minVal, minDefined := dp.Min.Value()
	assert.True(t, minDefined)
	assert.InDelta(t, 10.5, minVal, 0.1) // Min value

	maxVal, maxDefined := dp.Max.Value()
	assert.True(t, maxDefined)
	assert.InDelta(t, 25.3, maxVal, 0.1) // Max value
}

func TestCreateUpDownCounter(t *testing.T) {
	// Create a manual reader for testing
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter(testMeterName)

	// Create an up-down counter using the helper function
	upDownCounter, err := CreateUpDownCounter(meter, "test.updown", "Test up-down counter description")
	require.NoError(t, err)
	assert.NotNil(t, upDownCounter)

	// Record some values (can be positive or negative)
	ctx := context.Background()
	upDownCounter.Add(ctx, 10) // Increment
	upDownCounter.Add(ctx, -3) // Decrement
	upDownCounter.Add(ctx, 5)  // Increment

	// Collect metrics
	var rm metricdata.ResourceMetrics
	err = reader.Collect(ctx, &rm)
	require.NoError(t, err)

	// Verify we got metrics
	require.Len(t, rm.ScopeMetrics, 1)
	require.Len(t, rm.ScopeMetrics[0].Metrics, 1)

	// Verify metric name and description
	metricRecord := rm.ScopeMetrics[0].Metrics[0]
	assert.Equal(t, "test.updown", metricRecord.Name)
	assert.Equal(t, "Test up-down counter description", metricRecord.Description)

	// Verify data type
	sum, ok := metricRecord.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data type")

	// Verify non-monotonicity (up-down counters are not monotonic)
	assert.False(t, sum.IsMonotonic)

	// Verify data points (sum should be 12)
	require.Len(t, sum.DataPoints, 1)
	assert.Equal(t, int64(12), sum.DataPoints[0].Value)
}

// TestCreateObservableGauge tests observable gauge creation.
// Note: Observable instruments use callbacks and are tested via integration tests.
func TestCreateObservableGauge(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter(testMeterName)

	// Create an observable gauge
	_, err := meter.Int64ObservableGauge(
		"test.gauge",
		metric.WithDescription("Test gauge description"),
	)
	require.NoError(t, err)
}

// TestCreateObservableCounter tests observable counter creation.
// Note: Observable instruments use callbacks and are tested via integration tests.
func TestCreateObservableCounter(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := provider.Meter(testMeterName)

	// Create an observable counter
	_, err := meter.Float64ObservableCounter(
		"test.observable.counter",
		metric.WithDescription("Test observable counter description"),
	)
	require.NoError(t, err)
}

func TestMeterProviderIntegration(t *testing.T) {
	// Test that MeterProvider is properly initialized with metrics enabled
	cfg := &Config{
		Enabled:     true,
		ServiceName: testServiceName,
		Metrics: MetricsConfig{
			Enabled:  true,
			Endpoint: "stdout",
			Interval: 1 * time.Second,
		},
		Trace: TraceConfig{
			Enabled: false, // Disable tracing for this test
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	defer provider.Shutdown(context.Background())

	// Verify MeterProvider is available
	meterProvider := provider.MeterProvider()
	assert.NotNil(t, meterProvider)

	// Create a meter and verify it works
	meter := meterProvider.Meter("test-integration")
	counter, err := meter.Int64Counter("integration.test.counter")
	require.NoError(t, err)
	counter.Add(context.Background(), 1)
}

func TestMeterProviderWithMetricsDisabled(t *testing.T) {
	// Test that when metrics are disabled, we get a no-op MeterProvider
	cfg := &Config{
		Enabled:     true,
		ServiceName: testServiceName,
		Metrics: MetricsConfig{
			Enabled: false, // Metrics disabled
		},
		Trace: TraceConfig{
			Enabled: false,
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	defer provider.Shutdown(context.Background())

	// Verify MeterProvider is available (but it's a no-op)
	meterProvider := provider.MeterProvider()
	assert.NotNil(t, meterProvider)

	// Create a meter (should work without error, but won't record anything)
	meter := meterProvider.Meter("test-noop")
	counter, err := meter.Int64Counter("noop.counter")
	require.NoError(t, err)
	counter.Add(context.Background(), 1) // Should not panic
}

func TestMeterProviderShutdown(t *testing.T) {
	cfg := &Config{
		Enabled:     true,
		ServiceName: testServiceName,
		Metrics: MetricsConfig{
			Enabled:  true,
			Endpoint: "stdout",
		},
		Trace: TraceConfig{
			Enabled: false,
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)

	// Shutdown should work without error
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = provider.Shutdown(ctx)
	assert.NoError(t, err)
}

func TestMeterProviderForceFlush(t *testing.T) {
	cfg := &Config{
		Enabled:     true,
		ServiceName: testServiceName,
		Metrics: MetricsConfig{
			Enabled:  true,
			Endpoint: "stdout",
		},
		Trace: TraceConfig{
			Enabled: false,
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	defer provider.Shutdown(context.Background())

	// Create and record some metrics
	meterProvider := provider.MeterProvider()
	meter := meterProvider.Meter("test-flush")
	counter, err := meter.Int64Counter("flush.test.counter")
	require.NoError(t, err)
	counter.Add(context.Background(), 10)

	// ForceFlush should work without error
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = provider.ForceFlush(ctx)
	assert.NoError(t, err)
}
