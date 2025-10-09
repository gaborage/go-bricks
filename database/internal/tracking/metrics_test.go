package tracking

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gaborage/go-bricks/logger"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// Test query constants to avoid string literal duplication
const (
	testQuerySelectUsers       = "SELECT * FROM users"
	testQueryInsertUsers       = "INSERT INTO users VALUES (1)"
	testQueryInsertUsersParams = "INSERT INTO users (name) VALUES ($1)"
	testQuerySelectOrders      = "SELECT * FROM orders"
	testQueryUpdateUsers       = "UPDATE users SET name = 'test'"
	testQuerySelectOne         = "SELECT 1"
)

// setupTestMeterProvider creates an in-memory meter provider for testing
func setupTestMeterProvider(t *testing.T) (mp *obtest.TestMeterProvider, cleanup func()) {
	t.Helper()

	// Save original global state
	originalMP := otel.GetMeterProvider()

	// Create test meter provider
	mp = obtest.NewTestMeterProvider()
	otel.SetMeterProvider(mp)

	// Reset meter initialization to pick up test provider
	meterOnce = sync.Once{}
	dbMeter = nil
	dbCallsCounter = nil
	dbDurationHistogram = nil

	// Return cleanup function
	cleanup = func() {
		if err := mp.Shutdown(context.Background()); err != nil {
			t.Logf("Failed to shutdown test meter provider: %v", err)
		}
		otel.SetMeterProvider(originalMP)
		// Reset state for other tests
		meterOnce = sync.Once{}
		dbMeter = nil
		dbCallsCounter = nil
		dbDurationHistogram = nil
	}

	return mp, cleanup
}

func TestRecordDBMetricsCounterIncrement(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	log := logger.New("disabled", false)
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	ctx := context.Background()
	duration := 25 * time.Millisecond

	// Record metrics for successful operation
	recordDBMetrics(ctx, tc, testQuerySelect, duration, 0, nil)

	// Collect metrics
	rm := mp.Collect(t)

	// Assert counter exists and has correct value
	obtest.AssertMetricExists(t, rm, metricDBCalls)
	obtest.AssertMetricValue(t, rm, metricDBCalls, int64(1))
}

func TestRecordDBMetricsHistogramRecording(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	log := logger.New("disabled", false)
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	ctx := context.Background()
	query := testQueryInsertUsersParams
	duration := 50 * time.Millisecond

	// Record metrics
	recordDBMetrics(ctx, tc, query, duration, 0, nil)

	// Collect metrics
	rm := mp.Collect(t)

	// Assert histogram exists
	obtest.AssertMetricExists(t, rm, metricDBDuration)

	// Get histogram data
	var histogramFound bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metricDBDuration {
				continue
			}
			histogramFound = true
			// Verify it's a histogram
			histogramData, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok, "Expected histogram data type")
			require.NotEmpty(t, histogramData.DataPoints, "Expected at least one data point")

			// Verify duration value (50ms)
			dp := histogramData.DataPoints[0]
			expectedDurationMs := 50.0
			assert.InDelta(t, expectedDurationMs, dp.Sum, 1.0, "Duration should be approximately 50ms")
		}
	}

	require.True(t, histogramFound, "Histogram metric should be found")
}

func TestRecordDBMetricsWithDifferentOperations(t *testing.T) {
	operations := []struct {
		query             string
		expectedOperation string
	}{
		{testQuerySelectUsers, "select"},
		{testQueryInsertUsers, "insert"},
		{testQueryUpdateUsers, "update"},
		{"DELETE FROM users WHERE id = 1", "delete"},
		{"BEGIN", "begin"},
		{"COMMIT", "commit"},
	}

	for _, op := range operations {
		t.Run(op.expectedOperation, func(t *testing.T) {
			mp, cleanup := setupTestMeterProvider(t)
			defer cleanup()

			log := logger.New("disabled", false)
			tc := &Context{
				Logger:   log,
				Vendor:   "postgresql",
				Settings: NewSettings(nil),
			}

			ctx := context.Background()
			duration := 10 * time.Millisecond

			// Record metrics
			recordDBMetrics(ctx, tc, op.query, duration, 0, nil)

			// Collect metrics
			rm := mp.Collect(t)

			// Verify operation attribute using helper
			assertMetricHasAttribute(t, rm, metricDBCalls, "db.operation.name", op.expectedOperation)
		})
	}
}

func TestRecordDBMetricsWithDifferentVendors(t *testing.T) {
	vendors := []struct {
		vendor         string
		expectedSystem string
	}{
		{"postgresql", "postgresql"},
		{"postgres", "postgresql"},
		{"oracle", "oracle"},
		{"mongodb", "mongodb"},
		{"mongo", "mongodb"},
	}

	for _, v := range vendors {
		t.Run(v.vendor, func(t *testing.T) {
			mp, cleanup := setupTestMeterProvider(t)
			defer cleanup()

			log := logger.New("disabled", false)
			tc := &Context{
				Logger:   log,
				Vendor:   v.vendor,
				Settings: NewSettings(nil),
			}

			ctx := context.Background()
			query := testQuerySelectOne
			duration := 5 * time.Millisecond

			// Record metrics
			recordDBMetrics(ctx, tc, query, duration, 0, nil)

			// Collect metrics
			rm := mp.Collect(t)

			// Verify db.system attribute using helper
			assertMetricHasAttribute(t, rm, metricDBCalls, "db.system", v.expectedSystem)
		})
	}
}

func TestRecordDBMetricsErrorAttribute(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		expectedError bool
	}{
		{
			name:          "no_error",
			err:           nil,
			expectedError: false,
		},
		{
			name:          "sql_err_no_rows_not_an_error",
			err:           sql.ErrNoRows,
			expectedError: false,
		},
		{
			name:          "actual_error",
			err:           errors.New("connection refused"),
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mp, cleanup := setupTestMeterProvider(t)
			defer cleanup()

			log := logger.New("disabled", false)
			tc := &Context{
				Logger:   log,
				Vendor:   "postgresql",
				Settings: NewSettings(nil),
			}

			ctx := context.Background()
			query := testQuerySelectUsers
			duration := 10 * time.Millisecond

			// Record metrics
			recordDBMetrics(ctx, tc, query, duration, 0, tt.err)

			// Collect metrics
			rm := mp.Collect(t)

			// Verify error attribute using helper
			assertMetricHasBoolAttribute(t, rm, metricDBCalls, attrKeyError, tt.expectedError)
		})
	}
}

func TestRecordDBMetricsMultipleOperations(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	log := logger.New("disabled", false)
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	ctx := context.Background()

	// Record multiple operations
	recordDBMetrics(ctx, tc, testQuerySelectUsers, 10*time.Millisecond, 0, nil)
	recordDBMetrics(ctx, tc, testQueryInsertUsers, 20*time.Millisecond, 0, nil)
	recordDBMetrics(ctx, tc, testQuerySelectOrders, 15*time.Millisecond, 0, nil)

	// Collect metrics
	rm := mp.Collect(t)

	// Counter should have 3 total calls
	// Note: We can't directly sum across different attribute sets, so we check that metric exists
	obtest.AssertMetricExists(t, rm, metricDBCalls)
	obtest.AssertMetricExists(t, rm, metricDBDuration)
}

func TestIsSQLNoRowsError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil_error",
			err:      nil,
			expected: false,
		},
		{
			name:     "sql_err_no_rows",
			err:      sql.ErrNoRows,
			expected: true,
		},
		{
			name:     "wrapped_sql_err_no_rows",
			err:      errors.New("query failed: " + sql.ErrNoRows.Error()),
			expected: false, // errors.New doesn't wrap, so errors.Is returns false
		},
		{
			name:     "other_error",
			err:      errors.New("connection refused"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isSQLNoRowsError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetDBMeterInitialization(t *testing.T) {
	_, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	// First call should initialize
	meter1 := getDBMeter()
	require.NotNil(t, meter1, "Meter should be initialized")

	// Second call should return same instance
	meter2 := getDBMeter()
	assert.Equal(t, meter1, meter2, "Should return same meter instance")
}

func TestRecordDBMetricsNilContext(t *testing.T) {
	_, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	log := logger.New("disabled", false)
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	// recordDBMetrics requires a valid (non-nil) context for OpenTelemetry metric recording
	// TrackDBOperation guards against nil context before calling recordDBMetrics
	ctx := context.TODO()
	assert.NotPanics(t, func() {
		recordDBMetrics(ctx, tc, testQuerySelectOne, 10*time.Millisecond, 0, nil)
	})
}

func TestExtractTableName(t *testing.T) {
	tests := []struct {
		name          string
		query         string
		expectedTable string
	}{
		// SELECT statements
		{
			name:          "simple_select",
			query:         testQuerySelectUsers,
			expectedTable: "users",
		},
		{
			name:          "select_with_where",
			query:         "SELECT id, name FROM customers WHERE id = 1",
			expectedTable: "customers",
		},
		{
			name:          "select_with_join",
			query:         "SELECT u.id FROM users u JOIN orders o ON u.id = o.user_id",
			expectedTable: "users", // Returns first table
		},
		{
			name:          "select_quoted_table",
			query:         `SELECT * FROM "users"`,
			expectedTable: "users",
		},
		{
			name:          "select_lowercase_from",
			query:         "select * from products",
			expectedTable: "products",
		},
		{
			name:          "select_with_schema",
			query:         "SELECT * FROM public.users", // Extracts table without schema
			expectedTable: "users",
		},

		// INSERT statements
		{
			name:          "simple_insert",
			query:         "INSERT INTO users (name, email) VALUES ('John', 'john@example.com')",
			expectedTable: "users",
		},
		{
			name:          "insert_quoted",
			query:         `INSERT INTO "customers" VALUES (1, 'Alice')`,
			expectedTable: "customers",
		},
		{
			name:          "insert_lowercase",
			query:         "insert into products (name) values ('Widget')",
			expectedTable: "products",
		},

		// UPDATE statements
		{
			name:          "simple_update",
			query:         "UPDATE users SET name = 'Jane' WHERE id = 1",
			expectedTable: "users",
		},
		{
			name:          "update_quoted",
			query:         `UPDATE "customers" SET email = 'new@example.com'`,
			expectedTable: "customers",
		},
		{
			name:          "update_lowercase",
			query:         "update products set price = 99.99",
			expectedTable: "products",
		},

		// DELETE statements
		{
			name:          "simple_delete",
			query:         "DELETE FROM users WHERE id = 1",
			expectedTable: "users",
		},
		{
			name:          "delete_quoted",
			query:         `DELETE FROM "customers" WHERE active = false`,
			expectedTable: "customers",
		},
		{
			name:          "delete_lowercase",
			query:         "delete from products where discontinued = true",
			expectedTable: "products",
		},

		// Edge cases
		{
			name:          "empty_query",
			query:         "",
			expectedTable: "unknown",
		},
		{
			name:          "whitespace_only",
			query:         "   ",
			expectedTable: "unknown",
		},
		{
			name:          "begin_transaction",
			query:         "BEGIN",
			expectedTable: "unknown",
		},
		{
			name:          "commit",
			query:         "COMMIT",
			expectedTable: "unknown",
		},
		{
			name:          "create_table",
			query:         "CREATE TABLE users (id INT PRIMARY KEY)",
			expectedTable: "unknown", // DDL operations return unknown
		},
		{
			name:          "drop_table",
			query:         "DROP TABLE users",
			expectedTable: "unknown",
		},
		{
			name:          "select_without_from",
			query:         testQuerySelectOne,
			expectedTable: "unknown",
		},
		{
			name:          "complex_subquery",
			query:         "SELECT * FROM (SELECT id FROM users) AS subquery",
			expectedTable: "users", // Extracts from main FROM clause
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTableName(tt.query)
			assert.Equal(t, tt.expectedTable, result, "Table name should match expected")
		})
	}
}

func TestRecordDBMetricsWithTableAttribute(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	log := logger.New("disabled", false)
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	ctx := context.Background()
	query := "SELECT * FROM users WHERE id = $1"
	duration := 25 * time.Millisecond

	// Record metrics
	recordDBMetrics(ctx, tc, query, duration, 0, nil)

	// Collect metrics
	rm := mp.Collect(t)

	// Verify table attribute is present
	var foundTableAttr bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == metricDBCalls {
				sumData, ok := m.Data.(metricdata.Sum[int64])
				require.True(t, ok)

				for _, dp := range sumData.DataPoints {
					for _, attr := range dp.Attributes.ToSlice() {
						if string(attr.Key) == "table" && attr.Value.AsString() == "users" {
							foundTableAttr = true
							break
						}
					}
				}
			}
		}
	}

	assert.True(t, foundTableAttr, "Should have table attribute with value 'users'")
}

// attributeFinder is a function type that extracts a value from an attribute.
// It returns the extracted value and true if the attribute matches the key.
type attributeFinder func(attr attribute.KeyValue, attrKey string) (any, bool)

// findMetricAttribute is a generic helper that searches for an attribute in the specified metric.
// It uses the provided finder function to extract and match attributes.
func findMetricAttribute(rm metricdata.ResourceMetrics, metricName, attrKey string, finder attributeFinder) (any, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metricName {
				continue
			}

			// Handle different metric data types
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range data.DataPoints {
					for _, attr := range dp.Attributes.ToSlice() {
						if value, found := finder(attr, attrKey); found {
							return value, true
						}
					}
				}
			case metricdata.Histogram[float64]:
				for _, dp := range data.DataPoints {
					for _, attr := range dp.Attributes.ToSlice() {
						if value, found := finder(attr, attrKey); found {
							return value, true
						}
					}
				}
			}
		}
	}
	return nil, false
}

// findMetricAttributeValue searches for a string attribute in the specified metric.
// It iterates through all ScopeMetrics, Metrics, and DataPoints to find the attribute.
// Returns the attribute value and true if found, empty string and false otherwise.
func findMetricAttributeValue(rm metricdata.ResourceMetrics, metricName, attrKey string) (value string, found bool) {
	stringFinder := func(attr attribute.KeyValue, key string) (any, bool) {
		if string(attr.Key) == key {
			return attr.Value.AsString(), true
		}
		return "", false
	}

	result, found := findMetricAttribute(rm, metricName, attrKey, stringFinder)
	if found {
		return result.(string), true
	}
	return "", false
}

// findMetricBoolAttribute searches for a boolean attribute in the specified metric.
// It iterates through all ScopeMetrics, Metrics, and DataPoints to find the attribute.
// Returns the attribute value and true if found, false and false otherwise.
func findMetricBoolAttribute(rm metricdata.ResourceMetrics, metricName, attrKey string) (value, found bool) {
	boolFinder := func(attr attribute.KeyValue, key string) (any, bool) {
		if string(attr.Key) == key {
			return attr.Value.AsBool(), true
		}
		return false, false
	}

	result, found := findMetricAttribute(rm, metricName, attrKey, boolFinder)
	if found {
		return result.(bool), true
	}
	return false, false
}

// assertMetricHasAttribute asserts that the specified metric has an attribute with the expected value.
// This helper reduces complexity by encapsulating the nested iteration logic.
func assertMetricHasAttribute(t *testing.T, rm metricdata.ResourceMetrics, metricName, attrKey, expectedValue string) {
	t.Helper()
	value, found := findMetricAttributeValue(rm, metricName, attrKey)
	require.True(t, found, "Metric %s should have attribute %s", metricName, attrKey)
	assert.Equal(t, expectedValue, value, "Attribute %s should have expected value", attrKey)
}

// assertMetricHasBoolAttribute asserts that the specified metric has a boolean attribute with the expected value.
// This helper reduces complexity by encapsulating the nested iteration logic.
func assertMetricHasBoolAttribute(t *testing.T, rm metricdata.ResourceMetrics, metricName, attrKey string, expectedValue bool) {
	t.Helper()
	value, found := findMetricBoolAttribute(rm, metricName, attrKey)
	require.True(t, found, "Metric %s should have boolean attribute %s", metricName, attrKey)
	assert.Equal(t, expectedValue, value, "Boolean attribute %s should have expected value", attrKey)
}
