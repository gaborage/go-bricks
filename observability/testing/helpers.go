// Package testing provides utilities for testing OpenTelemetry instrumentation
// in GoBricks applications.
//
// This package offers in-memory exporters and helpers for asserting spans and metrics
// in unit tests without requiring external collectors or backends.
//
// Usage:
//
//	// Create test trace provider and install it globally, restoring the
//	// previous provider afterwards. Never Shutdown a provider you installed:
//	// otel binds its global delegate to the first one installed in the binary
//	// and never rebinds (internal/global/state.go sync.Once), so shutting it
//	// down silences every later otel.Tracer call in the process.
//	tp := NewTestTraceProvider()
//	prev := otel.GetTracerProvider()
//	defer otel.SetTracerProvider(prev)
//	otel.SetTracerProvider(tp)
//
//	// Run your code that creates spans
//	tracer := tp.TestTracer()
//	_, span := tracer.Start(context.Background(), "test-span")
//	span.End()
//
//	// Assert spans
//	spans := tp.Exporter.GetSpans()
//	require.Len(t, spans, 1)
//	AssertSpanName(t, spans[0], "test-span")
package testing

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/observability"
)

const (
	attrValueMismatchErrMsg   = "attribute %s value mismatch"
	metricNotFoundErrMsg      = "metric %s not found"
	metricValueMismatchErrMsg = "metric %s value mismatch"
	noDataPointsErrMsg        = "no data points for metric %s"

	// TestTracerName is the default tracer name used in observability tests,
	// avoiding a hardcoded "test" string literal at each call site.
	TestTracerName = "test"
)

// TestTraceProvider wraps the SDK TracerProvider and in-memory exporter for testing.
type TestTraceProvider struct {
	*sdktrace.TracerProvider
	Exporter *tracetest.InMemoryExporter
}

// NewTestTraceProvider creates a TracerProvider with an in-memory exporter for testing.
// The returned provider captures all spans in memory for assertion without sending them
// to an external backend.
//
// Example:
//
//	tp := NewTestTraceProvider()
//
//	// Install it globally and restore the previous provider afterwards; do not
//	// Shutdown it (see the package doc — otel's delegate binds once).
//	prev := otel.GetTracerProvider()
//	defer otel.SetTracerProvider(prev)
//	otel.SetTracerProvider(tp)
//
//	// Later, get spans for assertions
//	spans := tp.Exporter.GetSpans()
func NewTestTraceProvider() *TestTraceProvider {
	exporter := tracetest.NewInMemoryExporter()
	// Same provider policy as production (observability.FrameworkTracerProviderOptions),
	// so a test written against this seam observes what the service will do. Without
	// it the SDK's default panic recording applies here and not in production, and a
	// test asserting a panic value never reaches a span would disagree with reality.
	provider := sdktrace.NewTracerProvider(
		append(observability.FrameworkTracerProviderOptions(),
			sdktrace.WithSyncer(exporter),
		)...,
	)

	return &TestTraceProvider{
		TracerProvider: provider,
		Exporter:       exporter,
	}
}

// TestTracer returns a tracer with the standard test name.
// This is a convenience method that eliminates the need to pass TestTracerName
// to the Tracer() method in every test.
//
// Example:
//
//	tp := NewTestTraceProvider()
//	tracer := tp.TestTracer()
//	_, span := tracer.Start(context.Background(), "operation")
func (ttp *TestTraceProvider) TestTracer() trace.Tracer {
	return ttp.Tracer(TestTracerName)
}

// TestMeterProvider wraps the SDK MeterProvider and manual reader for testing.
type TestMeterProvider struct {
	*sdkmetric.MeterProvider
	Reader *sdkmetric.ManualReader
}

// NewTestMeterProvider creates a MeterProvider with a manual reader for testing.
// The manual reader allows collecting metrics on-demand for assertions without
// periodic exports.
//
// Example:
//
//	mp := NewTestMeterProvider()
//
//	// Install it globally and restore the previous provider afterwards; do not
//	// Shutdown it (see the package doc — otel's delegate binds once).
//	prev := otel.GetMeterProvider()
//	defer otel.SetMeterProvider(prev)
//	otel.SetMeterProvider(mp)
//	meter := mp.Meter("test")
//	counter, _ := meter.Int64Counter("test.counter")
//	counter.Add(context.Background(), 1)
//
//	// Collect metrics for assertions
//	rm := mp.Collect(t)
func NewTestMeterProvider() *TestMeterProvider {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
	)

	return &TestMeterProvider{
		MeterProvider: provider,
		Reader:        reader,
	}
}

// Collect reads all metrics from the provider and returns them as ResourceMetrics.
// This is a convenience wrapper around Reader.Collect() with error handling.
func (tmp *TestMeterProvider) Collect(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	err := tmp.Reader.Collect(context.Background(), &rm)
	require.NoError(t, err, "failed to collect metrics")
	return rm
}

// SpanCollector provides a fluent API for filtering and asserting on captured spans.
type SpanCollector struct {
	t     *testing.T
	spans tracetest.SpanStubs
}

// NewSpanCollector creates a span collector from an in-memory exporter.
func NewSpanCollector(t *testing.T, exporter *tracetest.InMemoryExporter) *SpanCollector {
	t.Helper()
	return &SpanCollector{
		t:     t,
		spans: exporter.GetSpans(),
	}
}

// Len returns the number of collected spans.
func (sc *SpanCollector) Len() int {
	return len(sc.spans)
}

// Get returns the span at the given index.
// Fails the test if the index is out of bounds.
func (sc *SpanCollector) Get(index int) tracetest.SpanStub {
	sc.t.Helper()
	require.GreaterOrEqual(sc.t, index, 0, "span index out of bounds")
	require.Less(sc.t, index, len(sc.spans), "span index out of bounds")
	return sc.spans[index]
}

// WithName filters spans by name and returns a new collector.
func (sc *SpanCollector) WithName(name string) *SpanCollector {
	sc.t.Helper()
	filtered := make(tracetest.SpanStubs, 0)
	for i := range sc.spans {
		if sc.spans[i].Name == name {
			filtered = append(filtered, sc.spans[i])
		}
	}
	return &SpanCollector{
		t:     sc.t,
		spans: filtered,
	}
}

// WithAttribute filters spans by attribute key-value pair and returns a new collector.
func (sc *SpanCollector) WithAttribute(key string, value any) *SpanCollector {
	sc.t.Helper()
	filtered := make(tracetest.SpanStubs, 0)
	for i := range sc.spans {
		for _, attr := range sc.spans[i].Attributes {
			if attr.Key == attribute.Key(key) {
				if matchesValue(attr.Value, value) {
					filtered = append(filtered, sc.spans[i])
					break
				}
			}
		}
	}
	return &SpanCollector{
		t:     sc.t,
		spans: filtered,
	}
}

// First returns the first span in the collection.
// Fails the test if the collection is empty.
func (sc *SpanCollector) First() tracetest.SpanStub {
	sc.t.Helper()
	require.NotEmpty(sc.t, sc.spans, "no spans in collection")
	return sc.spans[0]
}

// AssertCount asserts the number of collected spans.
func (sc *SpanCollector) AssertCount(expected int) *SpanCollector {
	sc.t.Helper()
	assert.Len(sc.t, sc.spans, expected, "unexpected number of spans")
	return sc
}

// AssertEmpty asserts that the collection is empty.
func (sc *SpanCollector) AssertEmpty() *SpanCollector {
	sc.t.Helper()
	assert.Empty(sc.t, sc.spans, "expected no spans, but got %d", len(sc.spans))
	return sc
}

// matchesValue checks if an attribute value matches the expected value.
func matchesValue(attrValue attribute.Value, expected any) bool {
	switch v := expected.(type) {
	case string:
		return attrValue.AsString() == v
	case int:
		return attrValue.AsInt64() == int64(v)
	case int64:
		return attrValue.AsInt64() == v
	case float64:
		return attrValue.AsFloat64() == v
	case bool:
		return attrValue.AsBool() == v
	default:
		return false
	}
}

// AssertSpanName asserts the name of a span.
func AssertSpanName(t *testing.T, span *tracetest.SpanStub, expected string) {
	t.Helper()
	assert.Equal(t, expected, span.Name, "span name mismatch")
}

// AssertSpanAttribute asserts that a span has a specific attribute with the expected value.
func AssertSpanAttribute(t *testing.T, span *tracetest.SpanStub, key string, expected any) {
	t.Helper()
	for _, attr := range span.Attributes {
		if string(attr.Key) == key {
			switch v := expected.(type) {
			case string:
				assert.Equal(t, v, attr.Value.AsString(), attrValueMismatchErrMsg, key)
			case int:
				assert.Equal(t, int64(v), attr.Value.AsInt64(), attrValueMismatchErrMsg, key)
			case int64:
				assert.Equal(t, v, attr.Value.AsInt64(), attrValueMismatchErrMsg, key)
			case float64:
				assert.Equal(t, v, attr.Value.AsFloat64(), attrValueMismatchErrMsg, key)
			case bool:
				assert.Equal(t, v, attr.Value.AsBool(), attrValueMismatchErrMsg, key)
			default:
				t.Fatalf("unsupported attribute value type: %T", expected)
			}
			return
		}
	}
	t.Errorf("attribute %s not found in span", key)
}

// AssertSpanStatus asserts the status of a span.
func AssertSpanStatus(t *testing.T, span *tracetest.SpanStub, expectedCode codes.Code) {
	t.Helper()
	assert.Equal(t, expectedCode, span.Status.Code, "span status code mismatch")
}

// AssertSpanStatusDescription asserts the status description of a span.
func AssertSpanStatusDescription(t *testing.T, span *tracetest.SpanStub, expectedDesc string) {
	t.Helper()
	assert.Equal(t, expectedDesc, span.Status.Description, "span status description mismatch")
}

// AssertSpanError asserts that a span has an error status with the expected description.
func AssertSpanError(t *testing.T, span *tracetest.SpanStub, expectedDesc string) {
	t.Helper()
	assert.Equal(t, codes.Error, span.Status.Code, "expected error status")
	if expectedDesc != "" {
		assert.Equal(t, expectedDesc, span.Status.Description, "span error description mismatch")
	}
}

// LeakCanary is the string an ADR-083 test plants inside an error message before
// driving a framework span sink, so one constant decides what every such test
// looks for. Neither the name nor the value is credential-shaped: this file ships
// in the module, so a `password=`-style literal would match the generic-password
// rule of every secret scanner run against a consumer that vendors go-bricks, and
// a `Secret`-prefixed identifier trips gosec G101 here.
const LeakCanary = "gobricks-span-leak-canary-9f13"

// AssertExceptionTypeOnly asserts that span carries exactly one exception event,
// that it names wantType under exception.type, and that it carries NOTHING else
// — exception.message above all (ADR-083).
func AssertExceptionTypeOnly(t TB, span *tracetest.SpanStub, wantType string) {
	t.Helper()

	var found int
	for i := range span.Events {
		if span.Events[i].Name != string(semconv.ExceptionEventName) {
			continue
		}
		found++
		require.Len(t, span.Events[i].Attributes, 1,
			"the exception event carries exception.type and nothing else")
		assert.Equal(t, string(semconv.ExceptionTypeKey), string(span.Events[i].Attributes[0].Key))
		assert.Equal(t, wantType, span.Events[i].Attributes[0].Value.AsString())
	}
	assert.Equal(t, 1, found, "expected exactly one exception event")
}

// AssertNoExceptionEvent is the negative of AssertExceptionTypeOnly: a span that
// recorded no error must carry no exception event.
func AssertNoExceptionEvent(t TB, span *tracetest.SpanStub) {
	t.Helper()

	for i := range span.Events {
		assert.NotEqual(t, string(semconv.ExceptionEventName), span.Events[i].Name,
			"span records an exception event it should not have")
	}
}

// AssertNoSpanMarkers asserts that no marker reaches ANY span sink: the status
// description, the span name, the span attributes, the event names, or the event
// attributes — keys as well as values, since a key is exported too.
func AssertNoSpanMarkers(t TB, span *tracetest.SpanStub, markers ...string) {
	t.Helper()

	for _, marker := range markers {
		assert.NotContains(t, span.Status.Description, marker, "span status description")
		assert.NotContains(t, span.Name, marker, "span name")
		for _, kv := range span.Attributes {
			assert.NotContains(t, string(kv.Key), marker, "span attribute key")
			assert.NotContains(t, kv.Value.String(), marker, "span attribute %s", kv.Key)
		}
		for i := range span.Events {
			assert.NotContains(t, span.Events[i].Name, marker, "event name")
			for _, kv := range span.Events[i].Attributes {
				assert.NotContains(t, string(kv.Key), marker, "event attribute key")
				assert.NotContains(t, kv.Value.String(), marker, "event attribute %s", kv.Key)
			}
		}
	}
}

// AssertMetricValue finds a metric by name and asserts its value.
// For Sum and Gauge metrics (counters, up-down counters, gauges), it asserts the first data point value.
// For Histogram metrics, it asserts the count.
func AssertMetricValue(t *testing.T, rm metricdata.ResourceMetrics, metricName string, expectedValue any) {
	t.Helper()

	// Find the metric
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == metricName {
				assertMetricData(t, m, expectedValue)
				return
			}
		}
	}

	require.Fail(t, "metric not found", metricNotFoundErrMsg, metricName)
}

// assertMetricData asserts metric data based on its type.
func assertMetricData(t *testing.T, m metricdata.Metrics, expectedValue any) {
	t.Helper()

	switch data := m.Data.(type) {
	case metricdata.Sum[int64]:
		assertSumInt64(t, m.Name, data, expectedValue)
	case metricdata.Sum[float64]:
		assertSumFloat64(t, m.Name, data, expectedValue)
	case metricdata.Histogram[int64]:
		assertHistogramInt64(t, m.Name, data, expectedValue)
	case metricdata.Histogram[float64]:
		assertHistogramFloat64(t, m.Name, data, expectedValue)
	case metricdata.Gauge[int64]:
		assertGaugeInt64(t, m.Name, data, expectedValue)
	case metricdata.Gauge[float64]:
		assertGaugeFloat64(t, m.Name, data, expectedValue)
	default:
		t.Fatalf("unsupported metric data type: %T", m.Data)
	}
}

func assertSumInt64(t *testing.T, name string, data metricdata.Sum[int64], expectedValue any) {
	t.Helper()
	require.NotEmpty(t, data.DataPoints, "no data points for Sum[int64] metric %s", name)
	switch v := expectedValue.(type) {
	case int:
		assert.Equal(t, int64(v), data.DataPoints[0].Value, metricValueMismatchErrMsg, name)
	case int64:
		assert.Equal(t, v, data.DataPoints[0].Value, metricValueMismatchErrMsg, name)
	default:
		t.Fatalf("unsupported expected value type %T for Sum[int64] metric", expectedValue)
	}
}

func assertSumFloat64(t *testing.T, name string, data metricdata.Sum[float64], expectedValue any) {
	t.Helper()
	require.NotEmpty(t, data.DataPoints, "no data points for Sum[float64] metric %s", name)
	if v, ok := expectedValue.(float64); ok {
		assert.InDelta(t, v, data.DataPoints[0].Value, 0.001, metricValueMismatchErrMsg, name)
	} else {
		t.Fatalf("unsupported expected value type %T for Sum[float64] metric", expectedValue)
	}
}

func assertHistogramInt64(t *testing.T, name string, data metricdata.Histogram[int64], expectedValue any) {
	t.Helper()
	require.NotEmpty(t, data.DataPoints, "no data points for Histogram[int64] metric %s", name)
	assertHistogramCount(t, name, data.DataPoints[0].Count, expectedValue, "Histogram[int64]")
}

func assertHistogramFloat64(t *testing.T, name string, data metricdata.Histogram[float64], expectedValue any) {
	t.Helper()
	require.NotEmpty(t, data.DataPoints, "no data points for Histogram[float64] metric %s", name)
	assertHistogramCount(t, name, data.DataPoints[0].Count, expectedValue, "Histogram[float64]")
}

func assertHistogramCount(t *testing.T, name string, actualCount uint64, expectedValue any, metricType string) {
	t.Helper()
	switch v := expectedValue.(type) {
	case uint64:
		assert.Equal(t, v, actualCount, "metric %s count mismatch", name)
	case int:
		if v < 0 {
			t.Fatalf("negative count value %d for histogram metric", v)
		}
		assert.Equal(t, uint64(v), actualCount, "metric %s count mismatch", name) //#nosec G115 -- negative check on line above ensures safe conversion
	default:
		t.Fatalf("unsupported expected value type %T for %s metric", expectedValue, metricType)
	}
}

func assertGaugeInt64(t *testing.T, name string, data metricdata.Gauge[int64], expectedValue any) {
	t.Helper()
	require.NotEmpty(t, data.DataPoints, "no data points for Gauge[int64] metric %s", name)
	switch v := expectedValue.(type) {
	case int:
		assert.Equal(t, int64(v), data.DataPoints[0].Value, metricValueMismatchErrMsg, name)
	case int64:
		assert.Equal(t, v, data.DataPoints[0].Value, metricValueMismatchErrMsg, name)
	default:
		t.Fatalf("unsupported expected value type %T for Gauge[int64] metric", expectedValue)
	}
}

func assertGaugeFloat64(t *testing.T, name string, data metricdata.Gauge[float64], expectedValue any) {
	t.Helper()
	require.NotEmpty(t, data.DataPoints, "no data points for Gauge[float64] metric %s", name)
	if v, ok := expectedValue.(float64); ok {
		assert.InDelta(t, v, data.DataPoints[0].Value, 0.001, metricValueMismatchErrMsg, name)
	} else {
		t.Fatalf("unsupported expected value type %T for Gauge[float64] metric", expectedValue)
	}
}

// FindMetric finds a metric by name in the ResourceMetrics.
// Returns nil if not found.
func FindMetric(rm metricdata.ResourceMetrics, metricName string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == metricName {
				return &m
			}
		}
	}
	return nil
}

// AssertMetricExists asserts that a metric with the given name exists.
func AssertMetricExists(t *testing.T, rm metricdata.ResourceMetrics, metricName string) {
	t.Helper()
	metric := FindMetric(rm, metricName)
	require.NotNil(t, metric, metricNotFoundErrMsg, metricName)
}

// AssertMetricCount asserts the total number of metrics collected.
func AssertMetricCount(t *testing.T, rm metricdata.ResourceMetrics, expected int) {
	t.Helper()
	total := 0
	for _, sm := range rm.ScopeMetrics {
		total += len(sm.Metrics)
	}
	assert.Equal(t, expected, total, "unexpected number of metrics")
}

// AssertMetricDescription asserts the description of a metric.
func AssertMetricDescription(t *testing.T, rm metricdata.ResourceMetrics, metricName, expectedDesc string) {
	t.Helper()
	metric := FindMetric(rm, metricName)
	require.NotNil(t, metric, metricNotFoundErrMsg, metricName)
	assert.Equal(t, expectedDesc, metric.Description, "metric %s description mismatch", metricName)
}

// GetMetricSumValue gets the sum value for a Sum[int64] or Sum[float64] metric.
// Returns error if metric not found or wrong type.
func GetMetricSumValue(rm metricdata.ResourceMetrics, metricName string) (any, error) {
	metric := FindMetric(rm, metricName)
	if metric == nil {
		return nil, fmt.Errorf(metricNotFoundErrMsg, metricName)
	}

	switch data := metric.Data.(type) {
	case metricdata.Sum[int64]:
		if len(data.DataPoints) == 0 {
			return nil, fmt.Errorf(noDataPointsErrMsg, metricName)
		}
		return data.DataPoints[0].Value, nil
	case metricdata.Sum[float64]:
		if len(data.DataPoints) == 0 {
			return nil, fmt.Errorf(noDataPointsErrMsg, metricName)
		}
		return data.DataPoints[0].Value, nil
	default:
		return nil, fmt.Errorf("metric %s is not a Sum type", metricName)
	}
}

// GetMetricHistogramCount gets the count for a Histogram metric.
// Returns error if metric not found or wrong type.
func GetMetricHistogramCount(rm metricdata.ResourceMetrics, metricName string) (uint64, error) {
	metric := FindMetric(rm, metricName)
	if metric == nil {
		return 0, fmt.Errorf(metricNotFoundErrMsg, metricName)
	}

	switch data := metric.Data.(type) {
	case metricdata.Histogram[int64]:
		if len(data.DataPoints) == 0 {
			return 0, fmt.Errorf(noDataPointsErrMsg, metricName)
		}
		return data.DataPoints[0].Count, nil
	case metricdata.Histogram[float64]:
		if len(data.DataPoints) == 0 {
			return 0, fmt.Errorf(noDataPointsErrMsg, metricName)
		}
		return data.DataPoints[0].Count, nil
	default:
		return 0, fmt.Errorf("metric %s is not a Histogram type", metricName)
	}
}

// TB is what the exemplar helpers need from *testing.T. It is an interface so a
// caller that already abstracts over the test handle can pass its own.
type TB interface {
	require.TestingT
	Helper()
}

// HistogramExemplars returns the exemplars attached to every data point of a
// histogram metric.
func HistogramExemplars[N int64 | float64](t TB, rm metricdata.ResourceMetrics, metricName string) []metricdata.Exemplar[N] {
	t.Helper()
	return exemplarsOf(t, rm, metricName, "histogram", histogramExemplars[N])
}

// SumExemplars is HistogramExemplars for a counter.
func SumExemplars[N int64 | float64](t TB, rm metricdata.ResourceMetrics, metricName string) []metricdata.Exemplar[N] {
	t.Helper()
	return exemplarsOf(t, rm, metricName, "sum", sumExemplars[N])
}

func histogramExemplars[N int64 | float64](agg metricdata.Aggregation) ([]metricdata.Exemplar[N], bool) {
	data, ok := agg.(metricdata.Histogram[N])
	if !ok {
		return nil, false
	}
	var out []metricdata.Exemplar[N]
	for i := range data.DataPoints {
		out = append(out, data.DataPoints[i].Exemplars...)
	}
	return out, true
}

func sumExemplars[N int64 | float64](agg metricdata.Aggregation) ([]metricdata.Exemplar[N], bool) {
	data, ok := agg.(metricdata.Sum[N])
	if !ok {
		return nil, false
	}
	var out []metricdata.Exemplar[N]
	for i := range data.DataPoints {
		out = append(out, data.DataPoints[i].Exemplars...)
	}
	return out, true
}

// exemplarsOf looks the metric up and hands its aggregation to extract. It fails
// the test when the metric is absent or a different shape, rather than returning
// empty: a helper that silently yields nothing turns "the exemplar is missing"
// into "I guessed the shape wrong", and both read as a passing zero-length
// result at the call site.
func exemplarsOf[N int64 | float64](
	t TB,
	rm metricdata.ResourceMetrics,
	metricName, shape string,
	extract func(metricdata.Aggregation) ([]metricdata.Exemplar[N], bool),
) []metricdata.Exemplar[N] {
	t.Helper()

	metric := FindMetric(rm, metricName)
	require.NotNil(t, metric, "metric %s not found", metricName)

	out, ok := extract(metric.Data)
	require.True(t, ok, "metric %s is %T, not a %s of the requested type", metricName, metric.Data, shape)
	return out
}
