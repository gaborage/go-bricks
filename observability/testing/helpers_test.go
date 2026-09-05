package testing

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const (
	testCounter          = "test.counter"
	testSpanName         = "span-1"
	httpRequestAttr      = "http-request"
	dbSystemAttr         = "db.system"
	nonExistentMetric    = "does.not.exist"
	testHistogram        = "test.histogram"
	laterProviderCounter = "later.provider.counter"
	dbQuery              = "db.query"
	dbOperationAttr      = "db.operation"
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

// --- otel's permanent first delegate (#1093) ---

const (
	// delegateMeterName is this file's own instrumentation scope for the
	// first-delegate tests, kept distinct from TestTracerName so a datapoint
	// recorded here cannot be confused with one from another test.
	delegateMeterName = "gobricks/1093"
	// firstInstallerHint is the diagnosis a future reorder needs. The positive
	// half below only holds while this test is the FIRST otel.SetMeterProvider in
	// the observability/testing binary: otel binds its delegating wrapper to the
	// first provider installed and never rebinds (internal/global/state.go's
	// sync.Once), so an earlier installer would own the delegate and this test's
	// reader would legitimately see nothing.
	firstInstallerHint = "another test in this binary installed a meter provider first — this test must stay the first installer"
)

var (
	firstDelegateOnce sync.Once
	firstDelegate     *TestMeterProvider
)

// installFirstDelegate installs a TestMeterProvider as the binary's first meter
// provider and immediately restores the previous global WITHOUT shutting it down
// — the cleanup shape this change adopts — returning the provider otel's wrapper
// is now permanently bound to.
//
// The install happens once per PROCESS, not once per call, because that is what
// the mechanism under test is: otel binds its delegate on the first
// SetMeterProvider and never rebinds. A per-call install would make this test
// pass under `go test` and fail under `-count=2`, where the second iteration is
// no longer the first installer and would be reading a provider the global never
// routes to — a false red that says nothing about the code under test.
func installFirstDelegate() *TestMeterProvider {
	firstDelegateOnce.Do(func() {
		prev := otel.GetMeterProvider()
		firstDelegate = NewTestMeterProvider()
		otel.SetMeterProvider(firstDelegate)
		otel.SetMeterProvider(prev)
	})
	return firstDelegate
}

// TestRestoredGlobalStillDeliversAfterCleanup pins the property that makes
// "restore the previous provider, do NOT shut yours down" the correct cleanup
// shape. otel.SetMeterProvider binds the global delegating wrapper to the first
// provider installed in the binary, permanently (internal/global/state.go,
// sync.Once). Restoring `prev` afterwards restores the identity the global
// REPORTS, but every instrument the wrapper already delegates keeps routing into
// the first provider — so that provider has to stay alive. A cleanup that shuts
// it down leaves the wrapper pointing at a corpse and every later otel.Meter call
// in the process silently records nothing, which is exactly the class #1093 fixes.
func TestRestoredGlobalStillDeliversAfterCleanup(t *testing.T) {
	ctx := context.Background()

	t.Run("restored_global_still_reaches_the_first_provider", func(t *testing.T) {
		mp := installFirstDelegate()

		// Assert the DELTA, not the presence of a datapoint: the reader is
		// cumulative and never reset, so under -count=2 the second iteration would
		// find iteration one's residue and pass even if delegation had stopped
		// working.
		before := counterSum(t, mp, testCounter)

		counter, err := otel.Meter(delegateMeterName).Int64Counter(testCounter)
		require.NoError(t, err)
		counter.Add(ctx, 1)

		assert.Equal(t, before+1, counterSum(t, mp, testCounter),
			"a counter created through the restored global recorded nothing into the first-installed provider; %s", firstInstallerHint)
	})

	t.Run("a_later_provider_never_receives_the_globals_traffic", func(t *testing.T) {
		// The mirror image, and the reason the rule names the FIRST provider rather
		// than the most recent one: a provider installed later is never reached
		// through the global at all, because the wrapper is already bound.
		//
		// This one is deliberately NOT shut down. Shutting it down would make
		// later.Reader.Collect return ErrReaderShutdown unconditionally, and an
		// assertion that only runs when Collect succeeds is an assertion that never
		// runs — the subtest would pass even if a later provider HAD received the
		// global's traffic, which is the one thing it exists to rule out.
		prev := otel.GetMeterProvider()
		later := NewTestMeterProvider()
		otel.SetMeterProvider(later)
		otel.SetMeterProvider(prev)

		counter, err := otel.Meter(delegateMeterName).Int64Counter(laterProviderCounter)
		require.NoError(t, err)
		counter.Add(ctx, 1)

		var rm metricdata.ResourceMetrics
		require.NoError(t, later.Reader.Collect(ctx, &rm))
		assert.Nil(t, FindMetric(rm, laterProviderCounter),
			"a provider installed after the first one received traffic from the global; otel's delegate is supposed to be bound once")
	})
}

// counterSum reports the cumulative value of an Int64 counter in mp, or 0 when
// the instrument has not recorded anything yet — so a caller can assert on the
// change across an operation rather than on an absolute total.
func counterSum(t *testing.T, mp *TestMeterProvider, name string) int64 {
	t.Helper()
	found := FindMetric(mp.Collect(t), name)
	if found == nil {
		return 0
	}
	sum, ok := found.Data.(metricdata.Sum[int64])
	require.True(t, ok, "%s is not an Int64 sum", name)
	var total int64
	for i := range sum.DataPoints {
		total += sum.DataPoints[i].Value
	}
	return total
}
