package tracking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
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

	// Connection pool metrics
	metricPoolActive = "db.connection.pool.active"
	metricPoolIdle   = "db.connection.pool.idle"
	metricPoolTotal  = "db.connection.pool.total"

	// I/O metrics
	metricRowsAffected = "db.rows.affected"
)

var (
	// Singleton meter initialization
	dbMeter     metric.Meter
	meterOnce   sync.Once
	meterInitMu sync.Mutex

	// Metric instruments
	dbCallsCounter        metric.Int64Counter
	dbDurationHistogram   metric.Float64Histogram
	dbRowsAffectedCounter metric.Int64Counter

	// Connection pool metrics are registered per-connection via callbacks
	// They use ObservableGauges which are registered externally
)

// logMetricError logs a metric initialization or registration error to stderr.
// This is a best-effort operation - metrics failures should not break the application.
func logMetricError(metricName string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: Failed to initialize metric %s: %v\n", metricName, err)
	}
}

// noOpCleanup returns a no-op cleanup function for use when metric registration fails.
// The returned function can be safely called but performs no operation, allowing callers
// to use a consistent cleanup pattern regardless of whether metrics were successfully registered.
func noOpCleanup() func() {
	// Return empty function - nothing to clean up if registration failed
	return func() {}
}

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
	logMetricError(metricDBCalls, err)

	// Initialize histogram for operation duration
	dbDurationHistogram, err = dbMeter.Float64Histogram(
		metricDBDuration,
		metric.WithDescription("Duration of database operations in milliseconds"),
		metric.WithUnit("ms"),
	)
	logMetricError(metricDBDuration, err)

	// Initialize counter for rows affected (write operations)
	dbRowsAffectedCounter, err = dbMeter.Int64Counter(
		metricRowsAffected,
		metric.WithDescription("Number of rows affected by database operations"),
	)
	logMetricError(metricRowsAffected, err)
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
// - db.rows.affected: Counter of rows affected by write operations (0 for read operations)
//
// The function is non-blocking and handles errors gracefully - metric recording failures
// will not impact database operation execution.
func recordDBMetrics(ctx context.Context, tc *Context, query string, duration time.Duration, rowsAffected int64, err error) {
	// Ensure meter is initialized
	meter := getDBMeter()
	if meter == nil {
		return
	}

	// Extract operation type, table name, and normalize vendor
	operation := extractDBOperation(query)
	table := extractTableName(query)
	vendor := normalizeDBVendor(tc.Vendor)

	// Determine if operation resulted in error (excluding sql.ErrNoRows which is not an error)
	isError := err != nil && !isSQLNoRowsError(err)

	// Common attributes for both metrics
	commonAttrs := []attribute.KeyValue{
		attribute.String("db.system", vendor),
		attribute.String("db.operation.name", operation),
		attribute.String("table", table),
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

	// Record rows affected counter (only for successful operations with row count > 0)
	if dbRowsAffectedCounter != nil && rowsAffected > 0 && !isError {
		dbRowsAffectedCounter.Add(ctx, rowsAffected, metric.WithAttributes(commonAttrs...))
	}
}

// isSQLNoRowsError checks if the error is sql.ErrNoRows, which is not treated as a failure.
// sql.ErrNoRows indicates an empty result set, which is a normal query outcome.
func isSQLNoRowsError(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

var (
	// Regex patterns for extracting table names from SQL queries
	// These patterns handle common DML operations and account for quoted identifiers
	// They also handle schema-qualified tables (schema.table) by capturing the table name after the dot
	selectTableRegex = regexp.MustCompile(`(?i)FROM\s+(?:["']?\w+["']?\.)?["']?(\w+)["']?`)
	insertTableRegex = regexp.MustCompile(`(?i)INSERT\s+INTO\s+(?:["']?\w+["']?\.)?["']?(\w+)["']?`)
	updateTableRegex = regexp.MustCompile(`(?i)UPDATE\s+(?:["']?\w+["']?\.)?["']?(\w+)["']?`)
	deleteTableRegex = regexp.MustCompile(`(?i)DELETE\s+FROM\s+(?:["']?\w+["']?\.)?["']?(\w+)["']?`)
)

// extractTableName attempts to extract the primary table name from a SQL query.
// It uses regex patterns to identify tables in SELECT, INSERT, UPDATE, and DELETE statements.
//
// For multi-table queries (e.g., JOINs), it returns the first table encountered.
// For queries where the table cannot be determined, it returns "unknown".
//
// This is a lightweight parser optimized for common cases - it's not a full SQL parser.
func extractTableName(query string) string {
	// Normalize whitespace for easier parsing
	query = strings.TrimSpace(query)
	if query == "" {
		return "unknown"
	}

	// Try each pattern based on likely query type
	queryUpper := strings.ToUpper(query)

	// SELECT queries
	if strings.HasPrefix(queryUpper, "SELECT") {
		if matches := selectTableRegex.FindStringSubmatch(query); len(matches) > 1 {
			return strings.ToLower(matches[1])
		}
	}

	// INSERT queries
	if strings.HasPrefix(queryUpper, "INSERT") {
		if matches := insertTableRegex.FindStringSubmatch(query); len(matches) > 1 {
			return strings.ToLower(matches[1])
		}
	}

	// UPDATE queries
	if strings.HasPrefix(queryUpper, "UPDATE") {
		if matches := updateTableRegex.FindStringSubmatch(query); len(matches) > 1 {
			return strings.ToLower(matches[1])
		}
	}

	// DELETE queries
	if strings.HasPrefix(queryUpper, "DELETE") {
		if matches := deleteTableRegex.FindStringSubmatch(query); len(matches) > 1 {
			return strings.ToLower(matches[1])
		}
	}

	// For DDL, transactions, and other operations, return "unknown"
	// These operations are typically less frequent and table-specific metrics aren't as critical
	return "unknown"
}

// RegisterConnectionPoolMetrics registers ObservableGauges for connection pool metrics.
// This function should be called once per database connection during initialization.
//
// It creates three gauges that report:
// - db.connection.pool.active: Number of connections currently in use
// - db.connection.pool.idle: Number of idle connections in the pool
// - db.connection.pool.total: Maximum number of connections configured
//
// The gauges are updated automatically when metrics are collected (typically every 30s).
// Returns a cleanup function that can be called to unregister the metrics (optional).
//
// This function uses graceful degradation - if any gauge fails to register, it continues
// with the remaining gauges. Only gauges that were successfully created will be updated.
func RegisterConnectionPoolMetrics(conn interface {
	Stats() (map[string]any, error)
}, vendor string) func() {
	meter := getDBMeter()
	if meter == nil {
		return noOpCleanup() // No-op cleanup if meter not initialized
	}

	// Normalize vendor name for consistent labeling
	dbSystem := normalizeDBVendor(vendor)
	attrs := []attribute.KeyValue{
		attribute.String("db.system", dbSystem),
	}

	// Register all three gauges, logging errors but continuing on failure
	activeGauge, err := meter.Int64ObservableGauge(
		metricPoolActive,
		metric.WithDescription("Number of active database connections"),
	)
	logMetricError(metricPoolActive, err)

	idleGauge, err := meter.Int64ObservableGauge(
		metricPoolIdle,
		metric.WithDescription("Number of idle database connections"),
	)
	logMetricError(metricPoolIdle, err)

	totalGauge, err := meter.Int64ObservableGauge(
		metricPoolTotal,
		metric.WithDescription("Maximum number of database connections configured"),
	)
	logMetricError(metricPoolTotal, err)

	// Collect successfully created gauges for callback registration
	var instruments []metric.Observable
	if activeGauge != nil {
		instruments = append(instruments, activeGauge)
	}
	if idleGauge != nil {
		instruments = append(instruments, idleGauge)
	}
	if totalGauge != nil {
		instruments = append(instruments, totalGauge)
	}

	// If no gauges were created successfully, return no-op cleanup
	if len(instruments) == 0 {
		return noOpCleanup()
	}

	// Register callback that reads pool stats and updates gauges
	registration, err := meter.RegisterCallback(
		func(_ context.Context, observer metric.Observer) error {
			stats, err := conn.Stats()
			if err != nil {
				// Log error but don't fail - metrics are best-effort
				return nil
			}

			// Extract pool statistics
			var inUse, idle, maxOpen int64
			if val, ok := stats["in_use"].(int); ok {
				inUse = int64(val)
			}
			if val, ok := stats["idle"].(int); ok {
				idle = int64(val)
			}
			if val, ok := stats["max_open_connections"].(int); ok {
				maxOpen = int64(val)
			}

			// Update only the successfully created gauges
			if activeGauge != nil {
				observer.ObserveInt64(activeGauge, inUse, metric.WithAttributes(attrs...))
			}
			if idleGauge != nil {
				observer.ObserveInt64(idleGauge, idle, metric.WithAttributes(attrs...))
			}
			if totalGauge != nil {
				observer.ObserveInt64(totalGauge, maxOpen, metric.WithAttributes(attrs...))
			}

			return nil
		},
		instruments...,
	)

	if err != nil {
		logMetricError("pool_metrics_callback", err)
		return noOpCleanup()
	}

	// Return cleanup function to unregister the callback
	return func() {
		if err := registration.Unregister(); err != nil {
			logMetricError("pool_metrics_unregister", err)
		}
	}
}
