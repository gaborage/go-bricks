package tracking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/gaborage/go-bricks/internal/testutil"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

const (
	dbSelectMetric     = "db.select"
	dbInsertMetric     = "db.insert"
	oracleDBVendorName = "oracle.db" // OTel spec requires .db suffix
)

// setupTestTracerProvider creates an in-memory tracer provider for testing
func setupTestTracerProvider(t *testing.T) (exporter *tracetest.InMemoryExporter, cleanup func()) {
	t.Helper()

	// Save original global state
	originalTP := otel.GetTracerProvider()
	originalPropagator := otel.GetTextMapPropagator()

	// Create in-memory exporter
	exporter = tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Return cleanup function
	cleanup = func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Logf("Failed to shutdown test tracer provider: %v", err)
		}
		otel.SetTracerProvider(originalTP)
		otel.SetTextMapPropagator(originalPropagator)
	}

	return exporter, cleanup
}

func TestCreateDBSpanSpanCreation(t *testing.T) {
	exporter, cleanup := setupTestTracerProvider(t)
	defer cleanup()

	log := newDisabledTestLogger()
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	ctx := context.Background()
	start := time.Now().Add(-50 * time.Millisecond) // Simulate operation that started 50ms ago

	createDBSpan(ctx, tc, TestQuerySelectUsersParams, start, nil)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1, "Should create exactly one span")

	span := spans[0]
	assert.Equal(t, dbSelectMetric, span.Name, "Span name should be db.select")
	assert.Equal(t, codes.Unset, span.Status.Code, "Success queries should have Unset status")
}

func TestCreateDBSpanSpanAttributes(t *testing.T) {
	exporter, cleanup := setupTestTracerProvider(t)
	defer cleanup()

	log := newDisabledTestLogger()
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	ctx := context.Background()
	query := "INSERT INTO users (name, email) VALUES ($1, $2)"
	start := time.Now().Add(-25 * time.Millisecond) // Simulate operation that started 25ms ago

	createDBSpan(ctx, tc, query, start, nil)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	// Extract attributes into map for easier testing
	attrs := spans[0].Attributes
	attrMap := make(map[string]interface{})
	for _, attr := range attrs {
		attrMap[string(attr.Key)] = attr.Value.AsInterface()
	}

	// Verify standard database attributes per OTel v1.32.0
	assert.Equal(t, "postgresql", attrMap["db.system.name"], "Should have db.system.name attribute per OTel spec")
	assert.Equal(t, query, attrMap["db.query.text"], "Should have db.query.text attribute")
	assert.Equal(t, "insert", attrMap["db.operation.name"], "Should have db.operation.name attribute")
	assert.Equal(t, "users", attrMap["db.collection.name"], "Should have db.collection.name attribute (table name)")
}

func TestCreateDBSpanErrorRecording(t *testing.T) {
	tests := []struct {
		name            string
		err             error
		expectedStatus  codes.Code
		shouldRecordErr bool
	}{
		{
			name:            "no_error",
			err:             nil,
			expectedStatus:  codes.Unset,
			shouldRecordErr: false,
		},
		{
			name:            "sql_err_no_rows",
			err:             sql.ErrNoRows,
			expectedStatus:  codes.Unset, // Not an error, just empty result
			shouldRecordErr: false,
		},
		{
			name:            "actual_error",
			err:             errors.New(testutil.TestConnectionRefused),
			expectedStatus:  codes.Error,
			shouldRecordErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exporter, cleanup := setupTestTracerProvider(t)
			defer cleanup()

			log := newDisabledTestLogger()
			tc := &Context{
				Logger:   log,
				Vendor:   "postgresql",
				Settings: NewSettings(nil),
			}

			ctx := context.Background()
			query := TestQuerySelectUsers
			start := time.Now().Add(-10 * time.Millisecond) // Simulate operation that started 10ms ago

			createDBSpan(ctx, tc, query, start, tt.err)

			spans := exporter.GetSpans()
			require.Len(t, spans, 1)

			span := spans[0]
			assert.Equal(t, tt.expectedStatus, span.Status.Code,
				"Span status should be %v", tt.expectedStatus)

			if tt.shouldRecordErr {
				assert.NotEmpty(t, span.Events, "Error should be recorded as span event")
			}
		})
	}
}

func TestExtractDBOperation(t *testing.T) {
	tests := []struct {
		query    string
		expected string
	}{
		{TestQuerySelectUsers, "select"},
		{"INSERT INTO users (name) VALUES ($1)", "insert"},
		{"UPDATE users SET name = $1 WHERE id = $2", "update"},
		{"DELETE FROM users WHERE id = $1", "delete"},
		{"CREATE TABLE users (id INT PRIMARY KEY)", "create"},
		{"DROP TABLE users", "drop"},
		{"ALTER TABLE users ADD COLUMN email VARCHAR(255)", "alter"},
		{"TRUNCATE TABLE users", "truncate"},
		{"BEGIN", "begin"},
		{"BEGIN_TX", "begin"},
		{"COMMIT", "commit"},
		{"ROLLBACK", "rollback"},
		{"PREPARE: SELECT * FROM users WHERE id = $1", "prepare"},
		{"CREATE_MIGRATION_TABLE", "create_table"},
		{"  select  * from users", "select"}, // Leading whitespace
		{"", "query"},                        // Empty query
		{"UNKNOWN_COMMAND", "query"},         // Unknown command
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			result := extractDBOperation(tt.query)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNormalizeDBVendor(t *testing.T) {
	tests := []struct {
		vendor   string
		expected string
	}{
		{"postgres", "postgresql"},
		{"postgresql", "postgresql"},
		{"Postgres", "postgresql"},
		{"POSTGRESQL", "postgresql"},
		{"oracle", oracleDBVendorName},           // OTel spec requires .db suffix
		{"Oracle", oracleDBVendorName},           // OTel spec requires .db suffix
		{oracleDBVendorName, oracleDBVendorName}, // Already normalized
		{"mysql", "mysql"},
		{"MySQL", "mysql"},
		{"sqlite", "sqlite"},
		{"sqlite3", "sqlite"},
		{"custom_db", "custom_db"}, // Unknown vendor passed through
	}

	for _, tt := range tests {
		t.Run(tt.vendor, func(t *testing.T) {
			result := normalizeDBVendor(tt.vendor)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCreateDBSpanQueryTruncation(t *testing.T) {
	exporter, cleanup := setupTestTracerProvider(t)
	defer cleanup()

	log := newDisabledTestLogger()
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	// Create a very long query
	longQuery := "SELECT * FROM users WHERE id IN ("
	for i := 0; i < 1000; i++ {
		if i > 0 {
			longQuery += ","
		}
		longQuery += fmt.Sprintf("$%d", i+1)
	}
	longQuery += ")"

	ctx := context.Background()
	start := time.Now().Add(-10 * time.Millisecond)

	createDBSpan(ctx, tc, longQuery, start, nil)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	// Get the db.query.text attribute
	attrs := spans[0].Attributes
	var queryAttr string
	for _, attr := range attrs {
		if string(attr.Key) == "db.query.text" {
			queryAttr = attr.Value.AsString()
		}
	}

	// Query should be truncated
	assert.True(t, len(queryAttr) <= 2000, "Query should be truncated to max 2000 characters")
	assert.True(t, len(queryAttr) < len(longQuery), "Query should be truncated")
	if len(longQuery) > 2000 {
		assert.Contains(t, queryAttr, "...", "Truncated query should contain ellipsis")
	}
}

func TestCreateDBSpanDifferentVendors(t *testing.T) {
	vendors := []struct {
		vendor         string
		expectedSystem string
	}{
		{"postgresql", "postgresql"},
		{"postgres", "postgresql"},
		{"oracle", oracleDBVendorName}, // OTel spec requires .db suffix
	}

	for _, v := range vendors {
		t.Run(v.vendor, func(t *testing.T) {
			exporter, cleanup := setupTestTracerProvider(t)
			defer cleanup()

			log := newDisabledTestLogger()
			tc := &Context{
				Logger:   log,
				Vendor:   v.vendor,
				Settings: NewSettings(nil),
			}

			ctx := context.Background()
			query := "SELECT 1"
			start := time.Now().Add(-5 * time.Millisecond)

			createDBSpan(ctx, tc, query, start, nil)

			spans := exporter.GetSpans()
			require.Len(t, spans, 1)

			// Find db.system.name attribute (per OTel spec v1.32.0)
			attrs := spans[0].Attributes
			var systemAttr string
			for _, attr := range attrs {
				if string(attr.Key) == "db.system.name" {
					systemAttr = attr.Value.AsString()
				}
			}

			assert.Equal(t, v.expectedSystem, systemAttr,
				"Vendor %s should normalize to %s in db.system.name attribute", v.vendor, v.expectedSystem)
		})
	}
}

func TestCreateDBSpanOperationTypes(t *testing.T) {
	operations := []struct {
		query        string
		expectedName string
	}{
		{TestQuerySelectUsers, dbSelectMetric},
		{"INSERT INTO users VALUES (1)", dbInsertMetric},
		{"UPDATE users SET name = 'test'", "db.update"},
		{"DELETE FROM users WHERE id = 1", "db.delete"},
		{"BEGIN", "db.begin"},
		{"COMMIT", "db.commit"},
		{"ROLLBACK", "db.rollback"},
		{"CREATE TABLE test (id INT)", "db.create"},
	}

	for _, op := range operations {
		t.Run(op.expectedName, func(t *testing.T) {
			exporter, cleanup := setupTestTracerProvider(t)
			defer cleanup()

			log := newDisabledTestLogger()
			tc := &Context{
				Logger:   log,
				Vendor:   "postgresql",
				Settings: NewSettings(nil),
			}

			ctx := context.Background()
			start := time.Now().Add(-5 * time.Millisecond)

			createDBSpan(ctx, tc, op.query, start, nil)

			spans := exporter.GetSpans()
			require.Len(t, spans, 1)

			assert.Equal(t, op.expectedName, spans[0].Name,
				"Span name should match operation type")
		})
	}
}

// setupTestObservabilityProviders creates both trace and meter providers for testing
func setupTestObservabilityProviders(t *testing.T) (
	traceExporter *tracetest.InMemoryExporter,
	meterProvider *obtest.TestMeterProvider,
	cleanup func(),
) {
	t.Helper()

	// Save original global state
	originalTP := otel.GetTracerProvider()
	originalMP := otel.GetMeterProvider()
	originalPropagator := otel.GetTextMapPropagator()

	// Create trace provider
	traceExporter = tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(traceExporter),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Create meter provider
	meterProvider = obtest.NewTestMeterProvider()
	otel.SetMeterProvider(meterProvider)

	// Reset meter initialization to pick up test provider
	meterOnce = sync.Once{}
	dbMeter = nil
	dbDurationHistogram = nil

	// Return cleanup function
	cleanup = func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Logf("Failed to shutdown test tracer provider: %v", err)
		}
		if err := meterProvider.Shutdown(context.Background()); err != nil {
			t.Logf("Failed to shutdown test meter provider: %v", err)
		}
		otel.SetTracerProvider(originalTP)
		otel.SetMeterProvider(originalMP)
		otel.SetTextMapPropagator(originalPropagator)
		// Reset state for other tests
		meterOnce = sync.Once{}
		dbMeter = nil
		dbDurationHistogram = nil
	}

	return traceExporter, meterProvider, cleanup
}

func TestTrackDBOperationCreatesSpanAndMetrics(t *testing.T) {
	traceExporter, meterProvider, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	log := newDisabledTestLogger()
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	ctx := context.Background()
	start := time.Now().Add(-25 * time.Millisecond)

	// Track the operation
	TrackDBOperation(ctx, tc, TestQuerySelectUsersParams, nil, start, 0, nil)

	// Verify span was created
	spans := traceExporter.GetSpans()
	require.Len(t, spans, 1, "Should create exactly one span")
	assert.Equal(t, dbSelectMetric, spans[0].Name, "Span name should be db.select")

	// Verify metrics were recorded
	rm := meterProvider.Collect(t)
	// Calls counter removed per OTel spec - only duration histogram remains
	obtest.AssertMetricExists(t, rm, metricDBDuration)
	obtest.AssertMetricExists(t, rm, metricDBDuration)
}

func TestTrackDBOperationWithError(t *testing.T) {
	traceExporter, meterProvider, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	log := newDisabledTestLogger()
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	ctx := context.Background()
	query := "INSERT INTO users (name) VALUES ($1)"
	start := time.Now().Add(-15 * time.Millisecond)
	testErr := errors.New("duplicate key violation")

	// Track the operation with error
	TrackDBOperation(ctx, tc, query, nil, start, 0, testErr)

	// Verify span was created with error status
	spans := traceExporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, dbInsertMetric, spans[0].Name)
	assert.Equal(t, codes.Error, spans[0].Status.Code, "Span should have error status")

	// Verify metrics were recorded
	rm := meterProvider.Collect(t)
	// Calls counter removed per OTel spec - only duration histogram remains
	obtest.AssertMetricExists(t, rm, metricDBDuration)

	// Note: Error tracking moved to spans only per OTel spec v1.32.0
	// Metrics focus on performance (duration), errors are tracked in span status
	// The test above already verified span status is Error
}

func TestTrackDBOperationSQLErrNoRows(t *testing.T) {
	traceExporter, meterProvider, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	log := newDisabledTestLogger()
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	ctx := context.Background()
	start := time.Now().Add(-10 * time.Millisecond)

	// Track the operation with sql.ErrNoRows
	TrackDBOperation(ctx, tc, TestQuerySelectUsersParams, nil, start, 0, sql.ErrNoRows)

	// Verify span was created without error status (sql.ErrNoRows is not an error)
	spans := traceExporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Unset, spans[0].Status.Code, "sql.ErrNoRows should not set error status")

	// Verify metrics were recorded with error=false
	rm := meterProvider.Collect(t)
	// Calls counter removed per OTel spec - only duration histogram remains
	obtest.AssertMetricExists(t, rm, metricDBDuration)

	// Note: Error tracking moved to spans only per OTel spec v1.32.0
	// Metrics focus on performance (duration), errors are in span status
}

func TestTrackDBOperationMultipleOperations(t *testing.T) {
	traceExporter, meterProvider, cleanup := setupTestObservabilityProviders(t)
	defer cleanup()

	log := newDisabledTestLogger()
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	ctx := context.Background()
	start := time.Now()

	// Track multiple different operations
	TrackDBOperation(ctx, tc, TestQuerySelectUsers, nil, start, 0, nil)
	TrackDBOperation(ctx, tc, "INSERT INTO users VALUES (1)", nil, start, 0, nil)
	TrackDBOperation(ctx, tc, "UPDATE users SET name = 'test'", nil, start, 0, nil)

	// Verify 3 spans were created
	spans := traceExporter.GetSpans()
	require.Len(t, spans, 3, "Should create three spans")

	spanNames := []string{spans[0].Name, spans[1].Name, spans[2].Name}
	assert.Contains(t, spanNames, dbSelectMetric)
	assert.Contains(t, spanNames, dbInsertMetric)
	assert.Contains(t, spanNames, "db.update")

	// Verify metrics were recorded for all operations
	rm := meterProvider.Collect(t)
	// Calls counter removed per OTel spec - only duration histogram remains
	obtest.AssertMetricExists(t, rm, metricDBDuration)
	obtest.AssertMetricExists(t, rm, metricDBDuration)
}
