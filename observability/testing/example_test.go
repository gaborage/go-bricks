package testing_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	obtest "github.com/gaborage/go-bricks/observability/testing"
)

const (
	httpMethodAttr       = "http.method"
	dbSystemAttr         = "db.system"
	dbQuery              = "db.query"
	databaseQuerySpan    = "database-query"
	totalRequestsCounter = "requests.total"
	totalRequestDuration = "requests.duration"
	moduleOpsCounter     = "module.operations"
)

// ExampleNewTestTraceProvider demonstrates how to use the test trace provider
// to assert spans created by your code.
func ExampleNewTestTraceProvider() {
	// Create a test trace provider with in-memory exporter
	tp := obtest.NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	// Set as global provider (or use directly)
	otel.SetTracerProvider(tp)

	// Your code that creates spans
	tracer := tp.Tracer("example-service")
	_, span := tracer.Start(context.Background(), "process-request")
	span.SetAttributes(
		attribute.String(httpMethodAttr, "GET"),
		attribute.String("http.url", "/api/users"),
		attribute.Int("http.status_code", 200),
	)
	span.End()

	// Get captured spans for assertions
	spans := tp.Exporter.GetSpans()
	fmt.Printf("Captured %d spans\n", len(spans))
	// Output: Captured 1 spans
}

// ExampleNewTestMeterProvider demonstrates how to use the test meter provider
// to assert metrics created by your code.
func ExampleNewTestMeterProvider() {
	// Create a test meter provider with manual reader
	mp := obtest.NewTestMeterProvider()
	defer mp.Shutdown(context.Background())

	// Set as global provider (or use directly)
	otel.SetMeterProvider(mp)

	// Your code that records metrics
	meter := mp.Meter("example-service")
	counter, _ := meter.Int64Counter("requests.count") // Metric options can be added here

	counter.Add(context.Background(), 1) // Attributes can be added to metric recordings

	counter.Add(context.Background(), 2)

	// Collect metrics for assertions
	// In real usage, you would pass the testing.T instance
	// rm := mp.Collect(t)
	fmt.Println("Metrics collected successfully")
	// Output: Metrics collected successfully
}

// ExampleSpanCollector demonstrates filtering spans by name and attributes.
// Note: In actual tests, use NewSpanCollector with a testing.T parameter for assertions.
func ExampleSpanCollector() {
	tp := obtest.NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("example")

	// Create multiple spans with different attributes
	_, span1 := tracer.Start(context.Background(), databaseQuerySpan)
	span1.SetAttributes(attribute.String(dbSystemAttr, "postgresql"))
	span1.End()

	_, span2 := tracer.Start(context.Background(), "http-request")
	span2.SetAttributes(attribute.String(httpMethodAttr, "POST"))
	span2.End()

	_, span3 := tracer.Start(context.Background(), databaseQuerySpan)
	span3.SetAttributes(attribute.String(dbSystemAttr, "mongodb"))
	span3.End()

	// Get spans and count them by filtering
	spans := tp.Exporter.GetSpans()

	// Count database query spans
	dbCount := 0
	pgCount := 0
	for i := range spans {
		if spans[i].Name == databaseQuerySpan {
			dbCount++
			// Check for PostgreSQL attribute
			for _, attr := range spans[i].Attributes {
				if attr.Key == attribute.Key(dbSystemAttr) && attr.Value.AsString() == "postgresql" {
					pgCount++
					break
				}
			}
		}
	}

	fmt.Printf("Database queries: %d\n", dbCount)
	fmt.Printf("PostgreSQL queries: %d\n", pgCount)

	// Output:
	// Database queries: 2
	// PostgreSQL queries: 1
}

// TestExampleSpanAssertions shows common span assertion patterns.
func TestExampleSpanAssertions(t *testing.T) {
	// Setup: Create test provider
	tp := obtest.NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	// Your code under test
	tracer := tp.Tracer("test-service")
	_, span := tracer.Start(context.Background(), "test-operation")
	span.SetAttributes(
		attribute.String("operation.type", "read"),
		attribute.Int64("record.count", 42),
	)
	span.SetStatus(codes.Ok, "Success")
	span.End()

	// Assertions using helper functions
	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 1, "expected exactly one span")

	// Assert span name
	obtest.AssertSpanName(t, &spans[0], "test-operation")

	// Assert span attributes
	obtest.AssertSpanAttribute(t, &spans[0], "operation.type", "read")
	obtest.AssertSpanAttribute(t, &spans[0], "record.count", int64(42))

	// Assert span status
	obtest.AssertSpanStatus(t, &spans[0], codes.Ok)
}

// TestExampleSpanCollector shows how to use the SpanCollector for filtering.
func TestExampleSpanCollector(t *testing.T) {
	// Setup
	tp := obtest.NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")

	// Create test spans
	_, span1 := tracer.Start(context.Background(), dbQuery)
	span1.SetAttributes(attribute.String(dbSystemAttr, "postgresql"))
	span1.End()

	_, span2 := tracer.Start(context.Background(), "http.request")
	span2.SetAttributes(attribute.String(httpMethodAttr, "GET"))
	span2.End()

	_, span3 := tracer.Start(context.Background(), dbQuery)
	span3.SetAttributes(attribute.String(dbSystemAttr, "mongodb"))
	span3.End()

	// Use SpanCollector for fluent assertions
	collector := obtest.NewSpanCollector(t, tp.Exporter)

	// Assert total count
	collector.AssertCount(3)

	// Filter by name
	dbSpans := collector.WithName(dbQuery)
	dbSpans.AssertCount(2)

	// Filter by attribute
	pgSpans := collector.WithAttribute(dbSystemAttr, "postgresql")
	pgSpans.AssertCount(1)

	// Get first span matching criteria
	firstDBSpan := dbSpans.First()
	obtest.AssertSpanName(t, &firstDBSpan, dbQuery)
}

// TestExampleMetricAssertions shows common metric assertion patterns.
func TestExampleMetricAssertions(t *testing.T) {
	// Setup: Create test provider
	mp := obtest.NewTestMeterProvider()
	defer mp.Shutdown(context.Background())

	// Your code under test
	meter := mp.Meter("test-service")

	// Create and record metrics
	counter, err := meter.Int64Counter(totalRequestsCounter) // Metric description and other options

	require.NoError(t, err)

	histogram, err := meter.Float64Histogram(totalRequestDuration) // Metric description and other options

	require.NoError(t, err)

	// Record some values
	ctx := context.Background()
	counter.Add(ctx, 5)
	counter.Add(ctx, 10)
	histogram.Record(ctx, 123.45)
	histogram.Record(ctx, 67.89)

	// Collect metrics
	rm := mp.Collect(t)

	// Assert metric exists
	obtest.AssertMetricExists(t, rm, totalRequestsCounter)
	obtest.AssertMetricExists(t, rm, totalRequestDuration)

	// Assert metric values
	// For counters, this checks the sum
	obtest.AssertMetricValue(t, rm, totalRequestsCounter, int64(15))

	// For histograms, this checks the count
	obtest.AssertMetricValue(t, rm, totalRequestDuration, uint64(2))

	// Get metric value programmatically
	value, err := obtest.GetMetricSumValue(rm, totalRequestsCounter)
	require.NoError(t, err)
	assert.Equal(t, int64(15), value)

	count, err := obtest.GetMetricHistogramCount(rm, totalRequestDuration)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), count)
}

// TestExampleErrorSpan shows how to assert error spans.
func TestExampleErrorSpan(t *testing.T) {
	tp := obtest.NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")

	// Create an error span
	_, span := tracer.Start(context.Background(), "failing-operation")
	span.SetStatus(codes.Error, "database connection failed")
	span.End()

	// Assert error status
	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 1)

	obtest.AssertSpanError(t, &spans[0], "database connection failed")
}

// TestExampleModuleInstrumentation shows how to test observability in a GoBricks module.
func TestExampleModuleInstrumentation(t *testing.T) {
	// This example demonstrates testing a module method that creates spans

	// Setup test providers
	tp := obtest.NewTestTraceProvider()
	defer tp.Shutdown(context.Background())

	mp := obtest.NewTestMeterProvider()
	defer mp.Shutdown(context.Background())

	// Set global providers (if your code uses global otel.GetTracerProvider())
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	// Or inject providers directly into your module
	// module := NewMyModule(tp.Tracer("my-module"), mp.Meter("my-module"))

	// Run your module code that should create spans and metrics
	tracer := tp.Tracer("my-module")
	meter := mp.Meter("my-module")

	// Simulate module operation
	_, span := tracer.Start(context.Background(), "module.operation")
	span.SetAttributes(attribute.String("operation", "test"))
	span.End()

	counter, err := meter.Int64Counter(moduleOpsCounter)
	require.NoError(t, err)
	counter.Add(context.Background(), 1)

	// Assert spans
	collector := obtest.NewSpanCollector(t, tp.Exporter)
	collector.AssertCount(1)
	collector.WithName("module.operation").AssertCount(1)

	// Assert metrics
	rm := mp.Collect(t)
	obtest.AssertMetricExists(t, rm, moduleOpsCounter)
	obtest.AssertMetricValue(t, rm, moduleOpsCounter, int64(1))
}
