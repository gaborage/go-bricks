package tracking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
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

	// Metric names following OpenTelemetry semantic conventions v1.32.0
	metricDBDuration = "db.client.operation.duration" // Histogram in seconds

	// Connection pool metrics per OTel spec
	metricDBConnectionCount   = "db.client.connection.count"    // Gauge with state attribute
	metricDBConnectionIdleMax = "db.client.connection.idle.max" // Max configured idle connections
	metricDBConnectionMax     = "db.client.connection.max"      // Max configured connections

	// Attribute keys per OTel semantic conventions
	attrDBSystem         = "db.system.name"
	attrDBOperation      = "db.operation.name"
	attrDBCollectionName = "db.collection.name"
	attrDBNamespace      = "db.namespace"
	attrConnectionState  = "state" // Values: "idle", "used"

	// Special values for table/collection name
	unknownTable = "unknown"
)

var (
	// Singleton meter initialization
	dbMeter     metric.Meter
	meterOnce   sync.Once
	meterInitMu sync.Mutex

	// Metric instruments following OTel semantic conventions
	dbDurationHistogram metric.Float64Histogram // Duration in seconds

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

// asInt64 attempts to convert various numeric types to int64 safely.
// It handles signed and unsigned integers, as well as floating-point numbers.
//
//nolint:gocyclo
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
		// Convert to uint64 to avoid 32-bit overflow on constants
		u := uint64(val)
		const maxInt64U = uint64(1<<63 - 1)
		if u <= maxInt64U {
			return int64(u), true
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
		const maxInt64U = uint64(1<<63 - 1)
		if val <= maxInt64U {
			return int64(val), true
		}
		return 0, false

	// Floating-point (truncate to int64) with range/NaN/Inf checks
	case float32:
		f := float64(val)
		const (
			maxInt64F = float64(1<<63 - 1)
			minInt64F = -float64(1 << 63)
		)
		if math.IsNaN(f) || math.IsInf(f, 0) || f < minInt64F || f > maxInt64F {
			return 0, false
		}
		return int64(f), true
	case float64:
		const (
			maxInt64F = float64(1<<63 - 1)
			minInt64F = -float64(1 << 63)
		)
		if math.IsNaN(val) || math.IsInf(val, 0) || val < minInt64F || val > maxInt64F {
			return 0, false
		}
		return int64(val), true

	// Unsupported type
	default:
		return 0, false
	}
}

// initDBMeter initializes the OpenTelemetry meter and metric instruments.
// This function is called lazily and only once using sync.Once to ensure
// initDBMeter initializes the package-level OpenTelemetry meter and the database
// operation duration histogram.
//
// The function is thread-safe and idempotent: it can be called multiple times but
// performs initialization only once. The duration histogram is configured to
// record operation durations in seconds. Initialization errors are logged but
// do not cause a panic.
func initDBMeter() {
	meterInitMu.Lock()
	defer meterInitMu.Unlock()

	// Prevent re-initialization if already set
	if dbMeter != nil {
		return
	}

	// Get meter from global meter provider
	dbMeter = otel.Meter(dbMeterName)

	// Initialize histogram for operation duration per OTel spec (in seconds, not milliseconds)
	var err error
	dbDurationHistogram, err = dbMeter.Float64Histogram(
		metricDBDuration,
		metric.WithDescription("Duration of database client operations"),
		metric.WithUnit("s"), // OTel spec requires seconds, not milliseconds
	)
	logMetricError(metricDBDuration, err)
}

// getDBMeter returns the initialized database meter, initializing it if necessary.
func getDBMeter() metric.Meter {
	meterOnce.Do(initDBMeter)
	return dbMeter
}

// recordDBMetrics records OpenTelemetry metrics for a database operation.
// This function is called by TrackDBOperation to emit metrics alongside traces and logs.
//
// Metrics recorded per OTel semantic conventions v1.32.0:
// - db.client.operation.duration: Histogram of operation durations in seconds
//
// Attributes per OTel spec:
// - db.system.name: Database vendor (postgresql, oracle.db, mongodb)
// - db.operation.name: Operation type (select, insert, update, etc.)
// - db.collection.name: Table/collection name
// - db.namespace: Vendor-specific namespace format
//
// The function is non-blocking and handles errors gracefully - metric recording failures
// will not impact database operation execution.
//
// recordDBMetrics records a database operation duration to the configured OpenTelemetry histogram using OTLP semantic attributes.
//
// It attaches `db.system.name` (vendor) and `db.operation.name` (operation). If present, it also adds `db.collection.name` (table)
// and `db.namespace` (tc.Namespace). Duration is recorded in seconds. If the global meter or histogram is not initialized, the call
// is a no-op. The `rowsAffected` and `error` parameters are currently unused and retained for future instrumentation.
func recordDBMetrics(ctx context.Context, tc *Context, query string, duration time.Duration, _ int64, _ error) {
	// Ensure meter is initialized
	meter := getDBMeter()
	if meter == nil {
		return
	}

	// Extract operation type, table name, and normalize vendor
	operation := extractDBOperation(query)
	table := extractTableName(query)
	vendor := normalizeDBVendor(tc.Vendor)

	// Build attributes per OTel semantic conventions
	attrs := []attribute.KeyValue{
		attribute.String(attrDBSystem, vendor),
		attribute.String(attrDBOperation, operation),
	}

	// Add optional attributes if available
	if table != "" && table != unknownTable {
		attrs = append(attrs, attribute.String(attrDBCollectionName, table))
	}
	if tc.Namespace != "" {
		attrs = append(attrs, attribute.String(attrDBNamespace, tc.Namespace))
	}

	// Record histogram with duration in seconds (not milliseconds)
	if dbDurationHistogram != nil {
		durationSec := float64(duration.Nanoseconds()) / 1e9 // Convert ns to seconds per OTel spec
		dbDurationHistogram.Record(ctx, durationSec, metric.WithAttributes(attrs...))
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

// extractTableName returns the table or collection name referenced by a common SQL DML
// statement (SELECT, INSERT, UPDATE, DELETE) in the provided query, or unknownTable if
// a table cannot be determined.
// It is a lightweight extractor optimized for common cases and is not a full SQL parser.
func extractTableName(query string) string {
	// Normalize whitespace for easier parsing
	query = strings.TrimSpace(query)
	if query == "" {
		return unknownTable
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
	return unknownTable
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

// extractPoolStats extracts connection counts from a stats map and returns
// the number of in-use (active) connections, idle connections, and the
// configured maximum open connections. Missing or non-numeric entries are
// treated as zero and values are converted using asInt64.
func extractPoolStats(stats map[string]any) (inUse, idle, maxIdle, maxOpen int64) {
	if val, ok := asInt64(stats["in_use"]); ok {
		inUse = val
	}
	if val, ok := asInt64(stats["idle"]); ok {
		idle = val
	}
	if val, ok := asInt64(stats["max_idle_connections"]); ok {
		maxIdle = val
	}
	if val, ok := asInt64(stats["max_open_connections"]); ok {
		maxOpen = val
	}
	return
}

// poolMetricsRegistration encapsulates pool metrics gauge state and observation logic.
// This struct implements OTel semantic conventions using state attribute for connection counts.
type poolMetricsRegistration struct {
	conn interface {
		Stats() (map[string]any, error)
	}
	connectionCountGauge metric.Int64ObservableGauge // Single gauge with state attribute
	idleMaxGauge         metric.Int64ObservableGauge // Max configured idle connections
	maxGauge             metric.Int64ObservableGauge // Max configured connections
	baseAttrs            []attribute.KeyValue        // db.system.name attribute
}

// observePoolStats reads connection pool statistics and updates gauges per OTel spec.
// This method is called automatically during metrics collection (typically every 30s).
func (r *poolMetricsRegistration) observePoolStats(_ context.Context, observer metric.Observer) error {
	stats, err := r.conn.Stats()
	if err != nil {
		return nil // Best-effort - don't fail metrics collection
	}

	inUse, idle, maxIdle, maxOpen := extractPoolStats(stats)

	// Record connection count with state attribute per OTel spec
	if r.connectionCountGauge != nil {
		// state=used for active connections
		usedAttrs := make([]attribute.KeyValue, len(r.baseAttrs)+1)
		copy(usedAttrs, r.baseAttrs)
		usedAttrs[len(r.baseAttrs)] = attribute.String(attrConnectionState, "used")
		observer.ObserveInt64(r.connectionCountGauge, inUse, metric.WithAttributes(usedAttrs...))

		// state=idle for idle connections
		idleAttrs := make([]attribute.KeyValue, len(r.baseAttrs)+1)
		copy(idleAttrs, r.baseAttrs)
		idleAttrs[len(r.baseAttrs)] = attribute.String(attrConnectionState, "idle")
		observer.ObserveInt64(r.connectionCountGauge, idle, metric.WithAttributes(idleAttrs...))
	}

	// Record configuration limits
	if r.idleMaxGauge != nil {
		observer.ObserveInt64(r.idleMaxGauge, maxIdle, metric.WithAttributes(r.baseAttrs...))
	}
	if r.maxGauge != nil {
		observer.ObserveInt64(r.maxGauge, maxOpen, metric.WithAttributes(r.baseAttrs...))
	}

	return nil
}

// RegisterConnectionPoolMetrics registers ObservableGauges for connection pool metrics
// following OpenTelemetry semantic conventions v1.32.0.
//
// This function should be called once per database connection during initialization.
//
// Metrics registered per OTel spec:
// - db.client.connection.count{state="used"}: Active connections in use
// - db.client.connection.count{state="idle"}: Idle connections in pool
// - db.client.connection.idle.max: Maximum configured idle connections
// - db.client.connection.max: Maximum configured connections
//
// The gauges are updated automatically when metrics are collected (typically every 30s).
// Returns a cleanup function that can be called to unregister the metrics (optional).
//
// This function uses graceful degradation - if any gauge fails to register, it continues
// with others and provides partial metrics coverage.
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
		baseAttrs: []attribute.KeyValue{
			attribute.String(attrDBSystem, normalizeDBVendor(vendor)),
		},
	}

	// Create gauges per OTel semantic conventions
	reg.connectionCountGauge = createGauge(meter, metricDBConnectionCount,
		"Number of connections that are currently in state described by the state attribute")
	reg.idleMaxGauge = createGauge(meter, metricDBConnectionIdleMax,
		"The maximum number of idle open connections allowed")
	reg.maxGauge = createGauge(meter, metricDBConnectionMax,
		"The maximum number of open connections allowed")

	// Collect non-nil gauges for callback registration
	instruments := collectInstruments(reg.connectionCountGauge, reg.idleMaxGauge, reg.maxGauge)
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
