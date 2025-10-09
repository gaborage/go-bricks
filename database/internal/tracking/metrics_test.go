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
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gaborage/go-bricks/logger"
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
	recordDBMetrics(ctx, tc, testQuerySelect, duration, nil)

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
	query := "INSERT INTO users (name) VALUES ($1)"
	duration := 50 * time.Millisecond

	// Record metrics
	recordDBMetrics(ctx, tc, query, duration, nil)

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
		{"SELECT * FROM users", "select"},
		{"INSERT INTO users VALUES (1)", "insert"},
		{"UPDATE users SET name = 'test'", "update"},
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
			recordDBMetrics(ctx, tc, op.query, duration, nil)

			// Collect metrics
			rm := mp.Collect(t)

			// Find the counter metric and verify operation attribute
			var foundOperation bool
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					if m.Name == metricDBCalls {
						sumData, ok := m.Data.(metricdata.Sum[int64])
						require.True(t, ok, "Expected sum data type for counter")

						for _, dp := range sumData.DataPoints {
							// Check if this data point has the expected operation
							for _, attr := range dp.Attributes.ToSlice() {
								if string(attr.Key) == "db.operation.name" && attr.Value.AsString() == op.expectedOperation {
									foundOperation = true
									break
								}
							}
						}
					}
				}
			}

			assert.True(t, foundOperation, "Should find metric with operation %s", op.expectedOperation)
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
			query := "SELECT 1"
			duration := 5 * time.Millisecond

			// Record metrics
			recordDBMetrics(ctx, tc, query, duration, nil)

			// Collect metrics
			rm := mp.Collect(t)

			// Verify db.system attribute
			var foundSystem bool
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					if m.Name == metricDBCalls {
						sumData, ok := m.Data.(metricdata.Sum[int64])
						require.True(t, ok)

						for _, dp := range sumData.DataPoints {
							for _, attr := range dp.Attributes.ToSlice() {
								if string(attr.Key) == "db.system" && attr.Value.AsString() == v.expectedSystem {
									foundSystem = true
									break
								}
							}
						}
					}
				}
			}

			assert.True(t, foundSystem, "Should find metric with db.system=%s", v.expectedSystem)
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
			query := "SELECT * FROM users"
			duration := 10 * time.Millisecond

			// Record metrics
			recordDBMetrics(ctx, tc, query, duration, tt.err)

			// Collect metrics
			rm := mp.Collect(t)

			// Verify error attribute on counter
			var foundErrorAttr bool
			var errorAttrValue bool
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					if m.Name == metricDBCalls {
						sumData, ok := m.Data.(metricdata.Sum[int64])
						require.True(t, ok)

						for _, dp := range sumData.DataPoints {
							for _, attr := range dp.Attributes.ToSlice() {
								if string(attr.Key) == attrKeyError {
									foundErrorAttr = true
									errorAttrValue = attr.Value.AsBool()
									break
								}
							}
						}
					}
				}
			}

			require.True(t, foundErrorAttr, "Error attribute should be present on counter")
			assert.Equal(t, tt.expectedError, errorAttrValue, "Error attribute value should match expected")
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
	recordDBMetrics(ctx, tc, "SELECT * FROM users", 10*time.Millisecond, nil)
	recordDBMetrics(ctx, tc, "INSERT INTO users VALUES (1)", 20*time.Millisecond, nil)
	recordDBMetrics(ctx, tc, "SELECT * FROM orders", 15*time.Millisecond, nil)

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
		recordDBMetrics(ctx, tc, "SELECT 1", 10*time.Millisecond, nil)
	})
}
