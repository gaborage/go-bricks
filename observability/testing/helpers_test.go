package testing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const (
	testCounter       = "test.counter"
	testSpanName      = "span-1"
	httpRequestAttr   = "http-request"
	dbSystemAttr      = "db.system"
	nonExistentMetric = "does.not.exist"
	testHistogram     = "test.histogram"
	dbQuery           = "db.query"
	dbOperationAttr   = "db.operation"
)

func TestNewTestTraceProvider(t *testing.T) {
	tp := NewTestTraceProvider()
	require.NotNil(t, tp)
	require.NotNil(t, tp.TracerProvider)
	require.NotNil(t, tp.Exporter)

	// Verify we can create a tracer
	tracer := tp.TestTracer()
	require.NotNil(t, tracer)

	// Verify we can create spans
	_, span := tracer.Start(context.Background(), "test-span")
	require.NotNil(t, span)
	span.End()

	// Verify spans are captured
	spans := tp.Exporter.GetSpans()
	assert.Len(t, spans, 1)
	assert.Equal(t, "test-span", spans[0].Name)

	// Cleanup
	err := tp.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestNewTestMeterProvider(t *testing.T) {
	mp := NewTestMeterProvider()
	require.NotNil(t, mp)
	require.NotNil(t, mp.MeterProvider)
	require.NotNil(t, mp.Reader)

	// Verify we can create a meter
	meter := mp.Meter("test")
	require.NotNil(t, meter)

	// Verify we can create instruments
	counter, err := meter.Int64Counter(testCounter)
	require.NoError(t, err)
	require.NotNil(t, counter)

	// Verify we can record metrics
	counter.Add(context.Background(), 1)

	// Verify metrics can be collected
	rm := mp.Collect(t)
	assert.NotNil(t, rm)

	// Cleanup
	err = mp.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestTestMeterProviderCollect(t *testing.T) {
	mp := NewTestMeterProvider()
	defer mp.Shutdown(context.Background())

	meter := mp.Meter("test")
	counter, err := meter.Int64Counter(testCounter)
	require.NoError(t, err)

	counter.Add(context.Background(), 5)

	rm := mp.Collect(t)
	require.Len(t, rm.ScopeMetrics, 1)
	require.Len(t, rm.ScopeMetrics[0].Metrics, 1)
	assert.Equal(t, testCounter, rm.ScopeMetrics[0].Metrics[0].Name)
}

func TestSpanCollectorBasics(t *testing.T) {
	tp := NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.TestTracer()

	// Create test spans
	_, span1 := tracer.Start(context.Background(), testSpanName)
	span1.End()

	_, span2 := tracer.Start(context.Background(), "span-2")
	span2.End()

	_, span3 := tracer.Start(context.Background(), "span-3")
	span3.End()

	// Test collector
	collector := NewSpanCollector(t, tp.Exporter)
	assert.Equal(t, 3, collector.Len())

	// Test Get
	span := collector.Get(0)
	assert.Equal(t, testSpanName, span.Name)

	// Test First
	first := collector.First()
	assert.Equal(t, testSpanName, first.Name)
}

func TestSpanCollectorWithName(t *testing.T) {
	tp := NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.TestTracer()

	// Create spans with different names
	_, span1 := tracer.Start(context.Background(), "query")
	span1.End()

	_, span2 := tracer.Start(context.Background(), httpRequestAttr)
	span2.End()

	_, span3 := tracer.Start(context.Background(), "query")
	span3.End()

	collector := NewSpanCollector(t, tp.Exporter)

	// Filter by name
	querySpans := collector.WithName("query")
	assert.Equal(t, 2, querySpans.Len())

	httpSpans := collector.WithName(httpRequestAttr)
	assert.Equal(t, 1, httpSpans.Len())

	nonExistentSpans := collector.WithName("does-not-exist")
	assert.Equal(t, 0, nonExistentSpans.Len())
}

func TestSpanCollectorWithAttribute(t *testing.T) {
	tp := NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.TestTracer()

	// Create spans with different attributes
	_, span1 := tracer.Start(context.Background(), "db-query")
	span1.SetAttributes(attribute.String(dbSystemAttr, "postgresql"))
	span1.End()

	_, span2 := tracer.Start(context.Background(), "db-query")
	span2.SetAttributes(attribute.String(dbSystemAttr, "mysql"))
	span2.End()

	_, span3 := tracer.Start(context.Background(), httpRequestAttr)
	span3.SetAttributes(attribute.Int("http.status_code", 200))
	span3.End()

	collector := NewSpanCollector(t, tp.Exporter)

	// Filter by string attribute
	pgSpans := collector.WithAttribute(dbSystemAttr, "postgresql")
	assert.Equal(t, 1, pgSpans.Len())

	// Filter by int attribute
	httpSpans := collector.WithAttribute("http.status_code", 200)
	assert.Equal(t, 1, httpSpans.Len())
}

func TestSpanCollectorAssertCount(t *testing.T) {
	tp := NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.TestTracer()

	_, span := tracer.Start(context.Background(), "test")
	span.End()

	collector := NewSpanCollector(t, tp.Exporter)

	// This should pass
	collector.AssertCount(1)

	// Test empty collection
	empty := collector.WithName("does-not-exist")
	empty.AssertEmpty()
}

func TestAssertSpanName(t *testing.T) {
	tp := NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.TestTracer()
	_, span := tracer.Start(context.Background(), "my-operation")
	span.End()

	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 1)

	// This should pass
	AssertSpanName(t, &spans[0], "my-operation")
}

func TestAssertSpanAttribute(t *testing.T) {
	tp := NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.TestTracer()
	_, span := tracer.Start(context.Background(), "test")
	span.SetAttributes(
		attribute.String("string.key", "value"),
		attribute.Int("int.key", 42),
		attribute.Int64("int64.key", 123),
		attribute.Float64("float.key", 3.14),
		attribute.Bool("bool.key", true),
	)
	span.End()

	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 1)

	// Test different attribute types
	AssertSpanAttribute(t, &spans[0], "string.key", "value")
	AssertSpanAttribute(t, &spans[0], "int.key", 42)
	AssertSpanAttribute(t, &spans[0], "int64.key", int64(123))
	AssertSpanAttribute(t, &spans[0], "float.key", 3.14)
	AssertSpanAttribute(t, &spans[0], "bool.key", true)
}

func TestAssertSpanStatus(t *testing.T) {
	tp := NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.TestTracer()

	// Create span with OK status
	_, span1 := tracer.Start(context.Background(), "success")
	span1.SetStatus(codes.Ok, "")
	span1.End()

	// Create span with Error status
	_, span2 := tracer.Start(context.Background(), "failure")
	span2.SetStatus(codes.Error, "something went wrong")
	span2.End()

	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 2)

	AssertSpanStatus(t, &spans[0], codes.Ok)
	AssertSpanStatus(t, &spans[1], codes.Error)
}

func TestAssertSpanStatusDescription(t *testing.T) {
	tp := NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.TestTracer()
	_, span := tracer.Start(context.Background(), "test")
	span.SetStatus(codes.Error, "connection timeout")
	span.End()

	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 1)

	AssertSpanStatusDescription(t, &spans[0], "connection timeout")
}

func TestAssertSpanError(t *testing.T) {
	tp := NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.TestTracer()
	_, span := tracer.Start(context.Background(), "test")
	span.SetStatus(codes.Error, "database error")
	span.End()

	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 1)

	AssertSpanError(t, &spans[0], "database error")
	AssertSpanError(t, &spans[0], "") // Empty description means any error
}

func TestFindMetric(t *testing.T) {
	mp := NewTestMeterProvider()
	defer mp.Shutdown(context.Background())

	meter := mp.Meter("test")
	counter, err := meter.Int64Counter(testCounter)
	require.NoError(t, err)

	counter.Add(context.Background(), 1)

	rm := mp.Collect(t)

	// Should find existing metric
	foundMetric := FindMetric(rm, testCounter)
	require.NotNil(t, foundMetric)
	assert.Equal(t, testCounter, foundMetric.Name)

	// Should return nil for non-existent metric
	notFound := FindMetric(rm, nonExistentMetric)
	assert.Nil(t, notFound)
}

func TestAssertMetricExists(t *testing.T) {
	mp := NewTestMeterProvider()
	defer mp.Shutdown(context.Background())

	meter := mp.Meter("test")
	counter, err := meter.Int64Counter("my.counter")
	require.NoError(t, err)

	counter.Add(context.Background(), 1)

	rm := mp.Collect(t)

	// Should pass for existing metric
	AssertMetricExists(t, rm, "my.counter")
}

func TestAssertMetricCount(t *testing.T) {
	mp := NewTestMeterProvider()
	defer mp.Shutdown(context.Background())

	meter := mp.Meter("test")

	// Create multiple metrics
	counter1, _ := meter.Int64Counter("counter.1")
	counter2, _ := meter.Int64Counter("counter.2")
	histogram, _ := meter.Float64Histogram("histogram.1")

	counter1.Add(context.Background(), 1)
	counter2.Add(context.Background(), 1)
	histogram.Record(context.Background(), 1.0)

	rm := mp.Collect(t)

	AssertMetricCount(t, rm, 3)
}

func TestAssertMetricDescription(t *testing.T) {
	mp := NewTestMeterProvider()
	defer mp.Shutdown(context.Background())

	meter := mp.Meter("test")
	counter, err := meter.Int64Counter(testCounter,
		metric.WithDescription("A test counter"),
	)
	require.NoError(t, err)

	counter.Add(context.Background(), 1)

	rm := mp.Collect(t)

	AssertMetricDescription(t, rm, testCounter, "A test counter")
}

func TestAssertMetricValueInt64Sum(t *testing.T) {
	mp := NewTestMeterProvider()
	defer mp.Shutdown(context.Background())

	meter := mp.Meter("test")
	counter, err := meter.Int64Counter(testCounter)
	require.NoError(t, err)

	counter.Add(context.Background(), 10)
	counter.Add(context.Background(), 20)

	rm := mp.Collect(t)

	AssertMetricValue(t, rm, testCounter, int64(30))
	AssertMetricValue(t, rm, testCounter, 30) // Also works with int
}

func TestAssertMetricValueFloat64Histogram(t *testing.T) {
	mp := NewTestMeterProvider()
	defer mp.Shutdown(context.Background())

	meter := mp.Meter("test")
	histogram, err := meter.Float64Histogram(testHistogram)
	require.NoError(t, err)

	histogram.Record(context.Background(), 1.5)
	histogram.Record(context.Background(), 2.5)
	histogram.Record(context.Background(), 3.5)

	rm := mp.Collect(t)

	// For histograms, AssertMetricValue checks the count
	AssertMetricValue(t, rm, testHistogram, uint64(3))
}

func TestGetMetricSumValue(t *testing.T) {
	mp := NewTestMeterProvider()
	defer mp.Shutdown(context.Background())

	meter := mp.Meter("test")

	// Int64 counter
	intCounter, _ := meter.Int64Counter("int.counter")
	intCounter.Add(context.Background(), 42)

	// Float64 counter
	floatCounter, _ := meter.Float64Counter("float.counter")
	floatCounter.Add(context.Background(), 3.14)

	rm := mp.Collect(t)

	// Get int64 sum
	intValue, err := GetMetricSumValue(rm, "int.counter")
	require.NoError(t, err)
	assert.Equal(t, int64(42), intValue)

	// Get float64 sum
	floatValue, err := GetMetricSumValue(rm, "float.counter")
	require.NoError(t, err)
	assert.InDelta(t, 3.14, floatValue, 0.001)

	// Non-existent metric should error
	_, err = GetMetricSumValue(rm, nonExistentMetric)
	assert.Error(t, err)
}

func TestGetMetricHistogramCount(t *testing.T) {
	mp := NewTestMeterProvider()
	defer mp.Shutdown(context.Background())

	meter := mp.Meter("test")
	histogram, err := meter.Float64Histogram(testHistogram)
	require.NoError(t, err)

	histogram.Record(context.Background(), 1.0)
	histogram.Record(context.Background(), 2.0)
	histogram.Record(context.Background(), 3.0)

	rm := mp.Collect(t)

	count, err := GetMetricHistogramCount(rm, testHistogram)
	require.NoError(t, err)
	assert.Equal(t, uint64(3), count)

	// Non-existent metric should error
	_, err = GetMetricHistogramCount(rm, nonExistentMetric)
	assert.Error(t, err)
}

func TestMatchesValueAllTypes(t *testing.T) {
	tests := []struct {
		name      string
		attrValue attribute.Value
		expected  any
		matches   bool
	}{
		{"string match", attribute.StringValue("hello"), "hello", true},
		{"string mismatch", attribute.StringValue("hello"), "world", false},
		{"int match", attribute.Int64Value(42), 42, true},
		{"int64 match", attribute.Int64Value(42), int64(42), true},
		{"int mismatch", attribute.Int64Value(42), 99, false},
		{"float64 match", attribute.Float64Value(3.14), 3.14, true},
		{"float64 mismatch", attribute.Float64Value(3.14), 2.71, false},
		{"bool true match", attribute.BoolValue(true), true, true},
		{"bool false match", attribute.BoolValue(false), false, true},
		{"bool mismatch", attribute.BoolValue(true), false, false},
		{"unsupported type", attribute.StringValue("test"), struct{}{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matchesValue(tt.attrValue, tt.expected)
			assert.Equal(t, tt.matches, result)
		})
	}
}

func TestAssertMetricValueUpDownCounter(t *testing.T) {
	mp := NewTestMeterProvider()
	defer mp.Shutdown(context.Background())

	meter := mp.Meter("test")
	upDown, err := meter.Int64UpDownCounter("test.updown")
	require.NoError(t, err)

	upDown.Add(context.Background(), 10)
	upDown.Add(context.Background(), -3)
	upDown.Add(context.Background(), 5)

	rm := mp.Collect(t)

	// Net value should be 12
	AssertMetricValue(t, rm, "test.updown", int64(12))
}

func TestSpanCollectorChaining(t *testing.T) {
	tp := NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.TestTracer()

	// Create spans
	_, span1 := tracer.Start(context.Background(), dbQuery)
	span1.SetAttributes(
		attribute.String(dbSystemAttr, "postgresql"),
		attribute.String(dbOperationAttr, "select"),
	)
	span1.End()

	_, span2 := tracer.Start(context.Background(), dbQuery)
	span2.SetAttributes(
		attribute.String(dbSystemAttr, "postgresql"),
		attribute.String(dbOperationAttr, "insert"),
	)
	span2.End()

	_, span3 := tracer.Start(context.Background(), "http.request")
	span3.SetAttributes(attribute.String("http.method", "GET"))
	span3.End()

	collector := NewSpanCollector(t, tp.Exporter)

	// Chain filters
	pgSelects := collector.
		WithName(dbQuery).
		WithAttribute(dbSystemAttr, "postgresql").
		WithAttribute(dbOperationAttr, "select")

	pgSelects.AssertCount(1)
}

func TestAssertMetricValueGauge(t *testing.T) {
	// Manual reader setup to test gauges
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := mp.Meter("test")

	// Create an observable gauge
	_, err := meter.Int64ObservableGauge("test.gauge",
		metric.WithInt64Callback(func(_ context.Context, observer metric.Int64Observer) error {
			observer.Observe(100)
			return nil
		}),
	)
	require.NoError(t, err)

	// Collect metrics
	var rm metricdata.ResourceMetrics
	err = reader.Collect(context.Background(), &rm)
	require.NoError(t, err)

	// Assert gauge value
	AssertMetricValue(t, rm, "test.gauge", int64(100))
}
