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

	"github.com/gaborage/go-bricks/internal/testutil"
	obtest "github.com/gaborage/go-bricks/observability/testing"
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
		dbDurationHistogram = nil
	}

	return mp, cleanup
}

func TestRecordDBMetricsCounterIncrement(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	log := newDisabledTestLogger()
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	ctx := context.Background()
	duration := 25 * time.Millisecond

	// Record metrics for successful operation
	recordDBMetrics(ctx, tc, TestQuerySelectUsersParams, duration, 0, nil)

	// Collect metrics
	rm := mp.Collect(t)

	// Assert duration histogram exists (calls counter was removed per OTel spec)
	obtest.AssertMetricExists(t, rm, metricDBDuration)
}

func TestRecordDBMetricsHistogramRecording(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	log := newDisabledTestLogger()
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	ctx := context.Background()
	query := TestQueryInsertUsersParams
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

			// Verify duration value (50ms = 0.050 seconds per OTel spec)
			dp := histogramData.DataPoints[0]
			expectedDurationSec := 0.050 // 50ms in seconds
			assert.InDelta(t, expectedDurationSec, dp.Sum, 0.001, "Duration should be approximately 0.050 seconds")
		}
	}

	require.True(t, histogramFound, "Histogram metric should be found")
}

func TestRecordDBMetricsWithDifferentOperations(t *testing.T) {
	operations := []struct {
		query             string
		expectedOperation string
	}{
		{TestQuerySelectUsers, "select"},
		{TestQueryInsertUsers, "insert"},
		{TestQueryUpdateUsers, "update"},
		{"DELETE FROM users WHERE id = 1", "delete"},
		{"BEGIN", "begin"},
		{"COMMIT", "commit"},
	}

	for _, op := range operations {
		t.Run(op.expectedOperation, func(t *testing.T) {
			mp, cleanup := setupTestMeterProvider(t)
			defer cleanup()

			log := newDisabledTestLogger()
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
			assertMetricHasAttribute(t, rm, metricDBDuration, "db.operation.name", op.expectedOperation)
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
		{"oracle", "oracle.db"}, // OTel spec requires .db suffix
	}

	for _, v := range vendors {
		t.Run(v.vendor, func(t *testing.T) {
			mp, cleanup := setupTestMeterProvider(t)
			defer cleanup()

			log := newDisabledTestLogger()
			tc := &Context{
				Logger:   log,
				Vendor:   v.vendor,
				Settings: NewSettings(nil),
			}

			ctx := context.Background()
			query := TestQuerySelectOne
			duration := 5 * time.Millisecond

			// Record metrics
			recordDBMetrics(ctx, tc, query, duration, 0, nil)

			// Collect metrics
			rm := mp.Collect(t)

			// Verify db.system.name attribute using helper (per OTel spec)
			assertMetricHasAttribute(t, rm, metricDBDuration, "db.system.name", v.expectedSystem)
		})
	}
}

// TestRecordDBMetricsErrorAttribute removed - error tracking no longer in metrics per OTel spec.
// Errors are tracked in spans only. Metrics focus on performance (duration) per OTel v1.32.0.

func TestRecordDBMetricsMultipleOperations(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	log := newDisabledTestLogger()
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	ctx := context.Background()

	// Record multiple operations
	recordDBMetrics(ctx, tc, TestQuerySelectUsers, 10*time.Millisecond, 0, nil)
	recordDBMetrics(ctx, tc, TestQueryInsertUsers, 20*time.Millisecond, 0, nil)
	recordDBMetrics(ctx, tc, TestQuerySelectOrders, 15*time.Millisecond, 0, nil)

	// Collect metrics
	rm := mp.Collect(t)

	// Duration histogram should exist with multiple data points
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
			err:      errors.New(testutil.TestConnectionRefused),
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

	log := newDisabledTestLogger()
	tc := &Context{
		Logger:   log,
		Vendor:   "postgresql",
		Settings: NewSettings(nil),
	}

	// recordDBMetrics requires a valid (non-nil) context for OpenTelemetry metric recording
	// TrackDBOperation guards against nil context before calling recordDBMetrics
	ctx := context.TODO()
	assert.NotPanics(t, func() {
		recordDBMetrics(ctx, tc, TestQuerySelectOne, 10*time.Millisecond, 0, nil)
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
			query:         TestQuerySelectUsers,
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
			query:         TestQuerySelectOne,
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

	log := newDisabledTestLogger()
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

	// Verify table attribute is present with new OTel attribute name
	var foundTableAttr bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == metricDBDuration {
				histData, ok := m.Data.(metricdata.Histogram[float64])
				require.True(t, ok)

				for _, dp := range histData.DataPoints {
					for _, attr := range dp.Attributes.ToSlice() {
						if string(attr.Key) == attrDBCollectionName && attr.Value.AsString() == "users" {
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

// assertMetricHasAttribute asserts that the specified metric has an attribute with the expected value.
// This helper reduces complexity by encapsulating the nested iteration logic.
func assertMetricHasAttribute(t *testing.T, rm metricdata.ResourceMetrics, metricName, attrKey, expectedValue string) {
	t.Helper()
	value, found := findMetricAttributeValue(rm, metricName, attrKey)
	require.True(t, found, "Metric %s should have attribute %s", metricName, attrKey)
	assert.Equal(t, expectedValue, value, "Attribute %s should have expected value", attrKey)
}

// TestAsInt64 tests the asInt64() helper function with all supported numeric types.
func TestAsInt64(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected int64
		ok       bool
	}{
		// Signed integers
		{"int", int(42), 42, true},
		{"int8", int8(127), 127, true},
		{"int8_negative", int8(-128), -128, true},
		{"int16", int16(32767), 32767, true},
		{"int32", int32(2147483647), 2147483647, true},
		{"int64", int64(9223372036854775807), 9223372036854775807, true},
		{"int_negative", int(-999), -999, true},
		{"int_zero", int(0), 0, true},

		// Unsigned integers
		{"uint8", uint8(255), 255, true},
		{"uint16", uint16(65535), 65535, true},
		{"uint32", uint32(4294967295), 4294967295, true},
		{"uint64_safe", uint64(1000), 1000, true},
		{"uint64_max_safe", uint64(9223372036854775807), 9223372036854775807, true}, // MaxInt64
		{"uint64_overflow", uint64(9223372036854775808), 0, false},                  // MaxInt64 + 1
		{"uint64_max", uint64(18446744073709551615), 0, false},                      // MaxUint64
		{"uint_safe", uint(1000), 1000, true},
		{"uint_zero", uint(0), 0, true},

		// Floating-point (truncation)
		{"float32", float32(99.7), 99, true},
		{"float32_negative", float32(-99.7), -99, true},
		{"float64", float64(123.456), 123, true},
		{"float64_large", float64(999999.999), 999999, true},
		{"float64_zero", float64(0.0), 0, true},

		// Non-numeric types (should fail)
		{"string", "42", 0, false},
		{"bool", true, 0, false},
		{"nil", nil, 0, false},
		{"struct", struct{ x int }{42}, 0, false},
		{"slice", []int{1, 2, 3}, 0, false},
		{"map", map[string]int{"key": 42}, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := asInt64(tt.input)
			assert.Equal(t, tt.ok, ok, "Conversion success should match expected")
			if tt.ok {
				assert.Equal(t, tt.expected, result, "Converted value should match expected")
			}
		})
	}
}

// TestRegisterConnectionPoolMetricsWithDifferentNumericTypes tests that pool metrics
// correctly handle various numeric types that different database drivers might return.
func TestRegisterConnectionPoolMetricsWithDifferentNumericTypes(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	// Test cases with different numeric types for pool stats
	testCases := []struct {
		name            string
		stats           map[string]any
		expectedInUse   int64
		expectedIdle    int64
		expectedIdleMax int64
		expectedMax     int64
	}{
		{
			name: "int_types",
			stats: map[string]any{
				"in_use":               int(5),
				"idle":                 int(10),
				"max_idle_connections": int(15),
				"max_open_connections": int(25),
			},
			expectedInUse:   5,
			expectedIdle:    10,
			expectedIdleMax: 15,
			expectedMax:     25,
		},
		{
			name: "int64_types",
			stats: map[string]any{
				"in_use":               int64(15),
				"idle":                 int64(20),
				"max_idle_connections": int64(35),
				"max_open_connections": int64(50),
			},
			expectedInUse:   15,
			expectedIdle:    20,
			expectedIdleMax: 35,
			expectedMax:     50,
		},
		{
			name: "uint32_types",
			stats: map[string]any{
				"in_use":               uint32(8),
				"idle":                 uint32(12),
				"max_idle_connections": uint32(18),
				"max_open_connections": uint32(30),
			},
			expectedInUse:   8,
			expectedIdle:    12,
			expectedIdleMax: 18,
			expectedMax:     30,
		},
		{
			name: "float64_types_truncated",
			stats: map[string]any{
				"in_use":               float64(7.9),
				"idle":                 float64(13.2),
				"max_idle_connections": float64(25.7),
				"max_open_connections": float64(40.8),
			},
			expectedInUse:   7,
			expectedIdle:    13,
			expectedIdleMax: 25,
			expectedMax:     40,
		},
		{
			name: "mixed_types",
			stats: map[string]any{
				"in_use":               int(3),
				"idle":                 uint64(15),
				"max_idle_connections": int64(18),
				"max_open_connections": float64(20.0),
			},
			expectedInUse:   3,
			expectedIdle:    15,
			expectedIdleMax: 18,
			expectedMax:     20,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a mock connection that returns specific stat types
			mockConn := &mockStatsConnection{stats: tc.stats}

			// Register pool metrics with server metadata
			connCleanup := RegisterConnectionPoolMetrics(mockConn, "postgresql", "localhost", 5432, "testdb.public")
			defer connCleanup()

			// Force metrics collection by collecting from meter provider
			rm := mp.Collect(t)

			// Verify gauges exist
			// Verify new OTel-compliant pool metrics
			obtest.AssertMetricExists(t, rm, metricDBConnectionCount)   // with state attribute
			obtest.AssertMetricExists(t, rm, metricDBConnectionIdleMax) // max configured idle
			obtest.AssertMetricExists(t, rm, metricDBConnectionMax)     // max configured connections

			// Verify the idle-max gauge has the correct value from max_idle_connections
			obtest.AssertMetricValue(t, rm, metricDBConnectionIdleMax, tc.expectedIdleMax)
		})
	}
}

// mockStatsConnection is a mock implementation of the Stats() interface for testing.
type mockStatsConnection struct {
	stats map[string]any
	err   error
}

func (m *mockStatsConnection) Stats() (map[string]any, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.stats, nil
}

// TestRegisterConnectionPoolMetricsWithInvalidTypes tests graceful handling of non-numeric types.
func TestRegisterConnectionPoolMetricsWithInvalidTypes(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	// Stats with non-numeric types (should result in 0 values, not crashes)
	mockConn := &mockStatsConnection{
		stats: map[string]any{
			"in_use":               "not a number",
			"idle":                 true,
			"max_open_connections": struct{}{},
		},
	}

	// Should not panic - test with minimal server metadata
	connCleanup := RegisterConnectionPoolMetrics(mockConn, "postgresql", "", 0, "")
	defer connCleanup()

	// Collect metrics - should succeed without panic
	rm := mp.Collect(t)

	// Gauges should exist (even if values are 0 due to conversion failure)
	// Verify new OTel-compliant pool metrics
	obtest.AssertMetricExists(t, rm, metricDBConnectionCount)   // with state attribute
	obtest.AssertMetricExists(t, rm, metricDBConnectionIdleMax) // max configured idle
	obtest.AssertMetricExists(t, rm, metricDBConnectionMax)     // max configured connections
}

// TestRegisterConnectionPoolMetricsStatsError tests handling of Stats() errors.
func TestRegisterConnectionPoolMetricsStatsError(t *testing.T) {
	mp, cleanup := setupTestMeterProvider(t)
	defer cleanup()

	// Mock connection that returns an error
	mockConn := &mockStatsConnection{
		err: errors.New("database connection closed"),
	}

	// Should not panic during registration or collection, even with empty server metadata
	assert.NotPanics(t, func() {
		connCleanup := RegisterConnectionPoolMetrics(mockConn, "postgresql", "localhost", 5432, "")
		defer connCleanup()

		// Collect metrics - should succeed without panic (callback logs error but doesn't fail)
		_ = mp.Collect(t)
	})
}

// TestLogMetricErrorOutput tests that logMetricError writes to stderr.
func TestLogMetricErrorOutput(t *testing.T) {
	// Test with non-nil error
	err := errors.New("test metric error")
	// logMetricError writes to stderr - we can't easily capture this in test
	// but we can verify it doesn't panic
	assert.NotPanics(t, func() {
		logMetricError("test.metric", err)
	})

	// Test with nil error (should not log)
	assert.NotPanics(t, func() {
		logMetricError("test.metric", nil)
	})
}

// TestNoOpCleanup tests that noOpCleanup returns a safe, callable function.
func TestNoOpCleanup(t *testing.T) {
	cleanup := noOpCleanup()
	require.NotNil(t, cleanup, "noOpCleanup should return a non-nil function")

	// Should be safe to call multiple times
	assert.NotPanics(t, func() {
		cleanup()
		cleanup()
		cleanup()
	})
}

// TestExtractTableNameWithMySQLBackticks tests that table extraction works with MySQL backtick-quoted identifiers.
func TestExtractTableNameWithMySQLBackticks(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected string
	}{
		// MySQL backtick-quoted table names
		{"mysql_select_backticks", "SELECT * FROM `users`", "users"},
		{"mysql_insert_backticks", "INSERT INTO `orders` VALUES (1)", "orders"},
		{"mysql_update_backticks", "UPDATE `products` SET price = 100", "products"},
		{"mysql_delete_backticks", "DELETE FROM `sessions` WHERE id = 1", "sessions"},

		// MySQL schema-qualified with backticks
		{"mysql_schema_table_backticks", "SELECT * FROM `mydb`.`users`", "users"},
		{"mysql_schema_only_backticks", "SELECT * FROM mydb.`users`", "users"},
		{"mysql_table_only_backticks", "SELECT * FROM `mydb`.users", "users"},

		// Mixed quote styles (PostgreSQL double quotes)
		{"postgres_double_quotes", "SELECT * FROM \"users\"", "users"},
		{"postgres_schema_table", "SELECT * FROM \"public\".\"users\"", "users"},

		// ANSI SQL single quotes (less common but valid)
		{"ansi_single_quotes", "SELECT * FROM 'users'", "users"},

		// No quotes (most common)
		{"no_quotes_select", "SELECT * FROM users", "users"},
		{"no_quotes_insert", "INSERT INTO orders VALUES (1)", "orders"},

		// Case insensitivity
		{"mysql_uppercase_backticks", "select * from `USERS`", "users"},
		{"mysql_mixed_case", "SELECT * FROM `UserAccounts`", "useraccounts"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTableName(tt.query)
			assert.Equal(t, tt.expected, result, "Table name should be correctly extracted from query")
		})
	}
}

// TestExtractTableNameBackwardCompatibility ensures existing patterns still work after MySQL backtick support.
func TestExtractTableNameBackwardCompatibility(t *testing.T) {
	// Verify all existing test cases from TestRecordDBMetricsWithTableAttribute still work
	legacyQueries := []struct {
		query    string
		expected string
	}{
		{"SELECT * FROM users", "users"},
		{"INSERT INTO orders VALUES (1)", "orders"},
		{"UPDATE products SET price = 100", "products"},
		{"DELETE FROM sessions WHERE id = 1", "sessions"},
		{"select * from USERS", "users"}, // case insensitive
		{"BEGIN TRANSACTION", "unknown"}, // non-DML
	}

	for _, tc := range legacyQueries {
		result := extractTableName(tc.query)
		assert.Equal(t, tc.expected, result, "Legacy query should still extract table correctly: %s", tc.query)
	}
}
