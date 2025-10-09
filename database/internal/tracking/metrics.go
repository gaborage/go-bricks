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

	metricDbSQLTable  = "db.sql.table"
	metricDbOperation = "db.operation.name"
	metricDbSystem    = "db.system"

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
	return func() {
		// No-op
	}
}

// asInt64 safely converts various Go numeric types to int64.
// This function handles all common numeric kinds that database drivers might return in Stats() maps.
//
// Supported types:
//   - Signed integers: int, int8, int16, int32, int64
//   - Unsigned integers: uint, uint8, uint16, uint32, uint64 (with overflow check)
//   - Floating-point: float32, float64 (truncated to int64)
//
// Returns (value, true) on successful conversion, (0, false) for:
//   - Non-numeric types (string, bool, struct, nil, etc.)
//   - uint64 values exceeding math.MaxInt64 (overflow)
//
// Examples:
//
//	asInt64(int(42))        // (42, true)
//	asInt64(int64(100))     // (100, true)
//	asInt64(uint32(50))     // (50, true)
//	asInt64(float64(99.7))  // (99, true) - truncated
//	asInt64("42")           // (0, false) - non-numeric
//	asInt64(nil)            // (0, false) - nil value
//
//nolint:gocyclo // Type switch for numeric conversion requires many cases by nature
func asInt64(v any) (int64, bool) {
	if v == nil {
		return 0, false
	}

	switch val := v.(type) {
	// Signed integers
	case int:
		return int64(val), true
	case int8:
		return int64(val), true
	case int16:
		return int64(val), true
	case int32:
		return int64(val), true
	case int64:
		return val, true

	// Unsigned integers (with overflow check for uint64)
	case uint:
		// uint might be 32 or 64 bits depending on platform
		// Safe conversion since uint max is either 2^32-1 or 2^64-1
		if val <= uint(9223372036854775807) { // math.MaxInt64
			return int64(val), true
		}
		return 0, false
	case uint8:
		return int64(val), true
	case uint16:
		return int64(val), true
	case uint32:
		return int64(val), true
	case uint64:
		// Check for overflow: uint64 can exceed int64 max value
		if val <= 9223372036854775807 { // math.MaxInt64
			return int64(val), true
		}
		return 0, false

	// Floating-point (truncate to int64)
	case float32:
		return int64(val), true
	case float64:
		return int64(val), true

	// Unsupported type
	default:
		return 0, false
	}
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
		attribute.String(metricDbSystem, vendor),
		attribute.String(metricDbOperation, operation),
		attribute.String(metricDbSQLTable, table),
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
	// Supports PostgreSQL/Oracle (double quotes), MySQL (backticks), and ANSI SQL (single quotes)
	// They also handle schema-qualified tables (schema.table) by capturing the table name after the dot
	selectTableRegex = regexp.MustCompile("(?i)FROM\\s+(?:[`\"']?\\w+[`\"']?\\.)?[`\"']?(\\w+)[`\"']?")
	insertTableRegex = regexp.MustCompile("(?i)INSERT\\s+INTO\\s+(?:[`\"']?\\w+[`\"']?\\.)?[`\"']?(\\w+)[`\"']?")
	updateTableRegex = regexp.MustCompile("(?i)UPDATE\\s+(?:[`\"']?\\w+[`\"']?\\.)?[`\"']?(\\w+)[`\"']?")
	deleteTableRegex = regexp.MustCompile("(?i)DELETE\\s+FROM\\s+(?:[`\"']?\\w+[`\"']?\\.)?[`\"']?(\\w+)[`\"']?")
)

// tryExtractTable attempts to extract a table name from the query using the provided regex.
// Returns the lowercase table name if found, empty string otherwise.
func tryExtractTable(pattern *regexp.Regexp, query string) string {
	if matches := pattern.FindStringSubmatch(query); len(matches) > 1 {
		return strings.ToLower(matches[1])
	}
	return ""
}

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

	// Try each pattern based on query type
	queryUpper := strings.ToUpper(query)

	// SELECT queries
	if strings.HasPrefix(queryUpper, "SELECT") {
		if table := tryExtractTable(selectTableRegex, query); table != "" {
			return table
		}
	}

	// INSERT queries
	if strings.HasPrefix(queryUpper, "INSERT") {
		if table := tryExtractTable(insertTableRegex, query); table != "" {
			return table
		}
	}

	// UPDATE queries
	if strings.HasPrefix(queryUpper, "UPDATE") {
		if table := tryExtractTable(updateTableRegex, query); table != "" {
			return table
		}
	}

	// DELETE queries
	if strings.HasPrefix(queryUpper, "DELETE") {
		if table := tryExtractTable(deleteTableRegex, query); table != "" {
			return table
		}
	}

	// For DDL, transactions, and other operations, return "unknown"
	// These operations are typically less frequent and table-specific metrics aren't as critical
	return "unknown"
}

// createGauge creates an observable gauge and logs errors without failing.
// Returns the created gauge or nil if creation failed.
func createGauge(meter metric.Meter, name, description string) metric.Int64ObservableGauge {
	gauge, err := meter.Int64ObservableGauge(name, metric.WithDescription(description))
	logMetricError(name, err)
	return gauge
}

// collectInstruments collects non-nil observable instruments into a slice.
// This helper eliminates repetitive nil-checking code.
func collectInstruments(gauges ...metric.Int64ObservableGauge) []metric.Observable {
	var instruments []metric.Observable
	for _, g := range gauges {
		if g != nil {
			instruments = append(instruments, g)
		}
	}
	return instruments
}

// extractPoolStats extracts integer pool statistics from the stats map using type-safe conversion.
// Returns three values: inUse (active connections), idle (idle connections), maxOpen (maximum configured).
func extractPoolStats(stats map[string]any) (inUse, idle, maxOpen int64) {
	if val, ok := asInt64(stats["in_use"]); ok {
		inUse = val
	}
	if val, ok := asInt64(stats["idle"]); ok {
		idle = val
	}
	if val, ok := asInt64(stats["max_open_connections"]); ok {
		maxOpen = val
	}
	return
}

// poolMetricsRegistration encapsulates pool metrics gauge state and observation logic.
// This struct reduces complexity by isolating the callback implementation.
type poolMetricsRegistration struct {
	conn interface {
		Stats() (map[string]any, error)
	}
	activeGauge metric.Int64ObservableGauge
	idleGauge   metric.Int64ObservableGauge
	totalGauge  metric.Int64ObservableGauge
	attrs       []attribute.KeyValue
}

// observePoolStats reads connection pool statistics and updates gauges.
// This method is called automatically during metrics collection (typically every 30s).
func (r *poolMetricsRegistration) observePoolStats(_ context.Context, observer metric.Observer) error {
	stats, err := r.conn.Stats()
	if err != nil {
		return nil // Best-effort - don't fail metrics collection
	}

	inUse, idle, maxOpen := extractPoolStats(stats)

	if r.activeGauge != nil {
		observer.ObserveInt64(r.activeGauge, inUse, metric.WithAttributes(r.attrs...))
	}
	if r.idleGauge != nil {
		observer.ObserveInt64(r.idleGauge, idle, metric.WithAttributes(r.attrs...))
	}
	if r.totalGauge != nil {
		observer.ObserveInt64(r.totalGauge, maxOpen, metric.WithAttributes(r.attrs...))
	}

	return nil
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
		return noOpCleanup()
	}

	// Create registration state with normalized vendor attributes
	reg := &poolMetricsRegistration{
		conn: conn,
		attrs: []attribute.KeyValue{
			attribute.String("db.system", normalizeDBVendor(vendor)),
		},
	}

	// Create gauges using helper function
	reg.activeGauge = createGauge(meter, metricPoolActive, "Number of active database connections")
	reg.idleGauge = createGauge(meter, metricPoolIdle, "Number of idle database connections")
	reg.totalGauge = createGauge(meter, metricPoolTotal, "Maximum number of database connections configured")

	// Collect non-nil gauges for callback registration
	instruments := collectInstruments(reg.activeGauge, reg.idleGauge, reg.totalGauge)
	if len(instruments) == 0 {
		return noOpCleanup()
	}

	// Register callback with extracted method
	registration, err := meter.RegisterCallback(reg.observePoolStats, instruments...)
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
