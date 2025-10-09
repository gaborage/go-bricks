package tracking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	// Meter name for database metrics instrumentation
	dbMeterName = "go-bricks/database"

	// Metric names following OpenTelemetry semantic conventions
	metricDBCalls    = "db.client.calls"
	metricDBDuration = "db.client.operation.duration"
)

var (
	// Singleton meter initialization
	dbMeter     metric.Meter
	meterOnce   sync.Once
	meterInitMu sync.Mutex

	// Metric instruments
	dbCallsCounter      metric.Int64Counter
	dbDurationHistogram metric.Float64Histogram
)

// initDBMeter initializes the OpenTelemetry meter and metric instruments.
// This function is called lazily and only once using sync.Once to ensure
// thread-safe initialization.
func initDBMeter() {
	meterInitMu.Lock()
	defer meterInitMu.Unlock()

	// Prevent re-initialization if already set
	if dbMeter != nil {
		return
	}

	// Get meter from global meter provider
	dbMeter = otel.Meter(dbMeterName)

	// Initialize counter for database calls
	var err error
	dbCallsCounter, err = dbMeter.Int64Counter(
		metricDBCalls,
		metric.WithDescription("Total number of database client calls"),
	)
	if err != nil {
		// Non-fatal: metrics are best-effort, log to stderr but don't fail
		fmt.Fprintf(os.Stderr, "WARNING: Failed to create database metric %s: %v\n", metricDBCalls, err)
	}

	// Initialize histogram for operation duration
	dbDurationHistogram, err = dbMeter.Float64Histogram(
		metricDBDuration,
		metric.WithDescription("Duration of database operations in milliseconds"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		// Non-fatal: metrics are best-effort, log to stderr but don't fail
		fmt.Fprintf(os.Stderr, "WARNING: Failed to create database metric %s: %v\n", metricDBDuration, err)
	}
}

// getDBMeter returns the initialized database meter, initializing it if necessary.
func getDBMeter() metric.Meter {
	meterOnce.Do(initDBMeter)
	return dbMeter
}

// recordDBMetrics records OpenTelemetry metrics for a database operation.
// This function is called by TrackDBOperation to emit metrics alongside traces and logs.
//
// Metrics recorded:
// - db.client.calls: Counter of total operations (with db.system, db.operation.name, error attributes)
// - db.client.operation.duration: Histogram of operation durations in milliseconds
//
// The function is non-blocking and handles errors gracefully - metric recording failures
// will not impact database operation execution.
func recordDBMetrics(ctx context.Context, tc *Context, query string, duration time.Duration, err error) {
	// Ensure meter is initialized
	meter := getDBMeter()
	if meter == nil {
		return
	}

	// Extract operation type and normalize vendor
	operation := extractDBOperation(query)
	vendor := normalizeDBVendor(tc.Vendor)

	// Determine if operation resulted in error (excluding sql.ErrNoRows which is not an error)
	isError := err != nil && !isSQLNoRowsError(err)

	// Common attributes for both metrics
	commonAttrs := []attribute.KeyValue{
		attribute.String("db.system", vendor),
		attribute.String("db.operation.name", operation),
	}

	// Record counter with error attribute
	if dbCallsCounter != nil {
		counterAttrs := make([]attribute.KeyValue, 0, len(commonAttrs)+1)
		counterAttrs = append(counterAttrs, commonAttrs...)
		counterAttrs = append(counterAttrs, attribute.Bool("error", isError))
		dbCallsCounter.Add(ctx, 1, metric.WithAttributes(counterAttrs...))
	}

	// Record histogram with duration in milliseconds
	if dbDurationHistogram != nil {
		durationMs := float64(duration.Nanoseconds()) / 1e6 // Convert ns to ms
		dbDurationHistogram.Record(ctx, durationMs, metric.WithAttributes(commonAttrs...))
	}
}

// isSQLNoRowsError checks if the error is sql.ErrNoRows, which is not treated as a failure.
// sql.ErrNoRows indicates an empty result set, which is a normal query outcome.
func isSQLNoRowsError(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
