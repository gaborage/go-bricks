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
	// Note: Changed from Gauge to UpDownCounter per OTEL semconv recommendation
	metricDBConnectionCount          = "db.client.connection.count"           // UpDownCounter with state attribute
	metricDBConnectionIdleMax        = "db.client.connection.idle.max"        // Max configured idle connections
	metricDBConnectionIdleConfigured = "db.client.connection.idle.configured" // Configured idle connections target (Go's sql.DB does not enforce a minimum)
	metricDBConnectionMax            = "db.client.connection.max"             // Max configured connections

	// New pool saturation metrics per OTEL semconv
	metricDBConnectionWaitCount    = "db.client.connection.wait_count"    // Counter: cumulative wait count
	metricDBConnectionTimeouts     = "db.client.connection.timeouts"      // Counter: timeout events
	metricDBConnectionPendingCount = "db.client.connection.pending_count" // UpDownCounter: currently waiting

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
//
// Purpose: Records operation duration as an OpenTelemetry histogram alongside traces and logs
// emitted by TrackDBOperation.
//
// OTel Metric (per semantic conventions v1.32.0):
// - Name: db.client.operation.duration
// - Type: Histogram
// - Units: seconds
//
// Attributes Added:
// - db.system.name: Database vendor (postgresql, oracle.db, mongodb) - always included
// - db.operation.name: Operation type (select, insert, update, delete, etc.) - always included
// - db.collection.name: Table/collection name - optional, included when available
// - db.namespace: Vendor-specific namespace format - optional, included when available
//
// Non-blocking Behavior:
// If the global meter or histogram is not initialized, the function is a no-op and returns
// immediately without error. Metric recording failures will not impact database operation
// execution.
//
// Note: The rowsAffected and error parameters are currently unused and retained for future
// instrumentation enhancements.
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

// createObservableUpDownCounter creates an observable up-down counter and logs errors without failing.
// Returns the created counter or nil if creation failed.
// Note: Changed from Gauge to UpDownCounter per OTEL semconv recommendation for pool metrics.
func createObservableUpDownCounter(meter metric.Meter, name, description string) metric.Int64ObservableUpDownCounter {
	counter, err := meter.Int64ObservableUpDownCounter(name, metric.WithDescription(description))
	logMetricError(name, err)
	return counter
}

// createObservableCounter creates an observable counter and logs errors without failing.
// Returns the created counter or nil if creation failed.
func createObservableCounter(meter metric.Meter, name, description string) metric.Int64ObservableCounter {
	counter, err := meter.Int64ObservableCounter(name, metric.WithDescription(description))
	logMetricError(name, err)
	return counter
}

// collectObservables collects non-nil observable instruments into a slice.
// This helper eliminates repetitive nil-checking code.
func collectObservables(instruments ...metric.Observable) []metric.Observable {
	var result []metric.Observable
	for _, inst := range instruments {
		if inst != nil {
			result = append(result, inst)
		}
	}
	return result
}

// poolStats holds extracted connection pool statistics.
type poolStats struct {
	InUse          int64
	Idle           int64
	MaxIdle        int64
	ConfiguredIdle int64 // Configured idle connections target (Go's sql.DB does not enforce a minimum)
	MaxOpen        int64
	WaitCount      int64
	WaitDuration   time.Duration
	Timeouts       int64
	PendingCount   int64
}

// extractPoolStats extracts connection counts from a stats map.
// Missing or non-numeric entries are treated as zero and values are converted using asInt64.
func extractPoolStats(stats map[string]any) poolStats {
	var ps poolStats

	if val, ok := asInt64(stats["in_use"]); ok {
		ps.InUse = val
	}
	if val, ok := asInt64(stats["idle"]); ok {
		ps.Idle = val
	}
	if val, ok := asInt64(stats["max_idle_connections"]); ok {
		ps.MaxIdle = val
	}
	if val, ok := asInt64(stats["configured_idle_connections"]); ok {
		ps.ConfiguredIdle = val
	}
	if val, ok := asInt64(stats["max_open_connections"]); ok {
		ps.MaxOpen = val
	}
	if val, ok := asInt64(stats["wait_count"]); ok {
		ps.WaitCount = val
	}
	if val, ok := asInt64(stats["timeouts"]); ok {
		ps.Timeouts = val
	}
	if val, ok := asInt64(stats["pending_count"]); ok {
		ps.PendingCount = val
	}
	// Parse wait_duration from string format
	if durStr, ok := stats["wait_duration"].(string); ok {
		if dur, err := time.ParseDuration(durStr); err == nil {
			ps.WaitDuration = dur
		}
	}

	return ps
}

// poolMetricsRegistration encapsulates pool metrics state and observation logic.
// This struct implements OTel semantic conventions using state attribute for connection counts.
// Note: Changed from Gauge to UpDownCounter per OTEL semconv recommendation.
type poolMetricsRegistration struct {
	conn interface {
		Stats() (map[string]any, error)
	}
	// Connection state metrics (UpDownCounter per OTEL spec)
	connectionCountCounter metric.Int64ObservableUpDownCounter // Current connections with state attribute
	idleMaxCounter         metric.Int64ObservableUpDownCounter // Max configured idle connections
	idleConfiguredCounter  metric.Int64ObservableUpDownCounter // Configured idle connections target (not enforced as minimum)
	maxCounter             metric.Int64ObservableUpDownCounter // Max configured connections

	// Pool saturation metrics (Counters for cumulative values)
	waitCountCounter metric.Int64ObservableCounter // Cumulative wait count
	timeoutsCounter  metric.Int64ObservableCounter // Cumulative timeout count

	// Pool pressure metric
	pendingCountCounter metric.Int64ObservableUpDownCounter // Current number of pending requests

	// Base attributes for all metrics
	baseAttrs []attribute.KeyValue
}

// observePoolStats reads connection pool statistics and updates metrics per OTel spec.
// This method is called automatically during metrics collection (typically every 30s).
func (r *poolMetricsRegistration) observePoolStats(_ context.Context, observer metric.Observer) error {
	stats, err := r.conn.Stats()
	if err != nil {
		return nil // Best-effort - don't fail metrics collection
	}

	ps := extractPoolStats(stats)

	// Record connection count with state attribute per OTel spec
	if r.connectionCountCounter != nil {
		// state=used for active connections
		usedAttrs := make([]attribute.KeyValue, len(r.baseAttrs)+1)
		copy(usedAttrs, r.baseAttrs)
		usedAttrs[len(r.baseAttrs)] = attribute.String(attrConnectionState, "used")
		observer.ObserveInt64(r.connectionCountCounter, ps.InUse, metric.WithAttributes(usedAttrs...))

		// state=idle for idle connections
		idleAttrs := make([]attribute.KeyValue, len(r.baseAttrs)+1)
		copy(idleAttrs, r.baseAttrs)
		idleAttrs[len(r.baseAttrs)] = attribute.String(attrConnectionState, "idle")
		observer.ObserveInt64(r.connectionCountCounter, ps.Idle, metric.WithAttributes(idleAttrs...))
	}

	// Record configuration limits
	if r.idleMaxCounter != nil {
		observer.ObserveInt64(r.idleMaxCounter, ps.MaxIdle, metric.WithAttributes(r.baseAttrs...))
	}
	if r.idleConfiguredCounter != nil {
		observer.ObserveInt64(r.idleConfiguredCounter, ps.ConfiguredIdle, metric.WithAttributes(r.baseAttrs...))
	}
	if r.maxCounter != nil {
		observer.ObserveInt64(r.maxCounter, ps.MaxOpen, metric.WithAttributes(r.baseAttrs...))
	}

	// Record cumulative wait count (shows pool saturation over time)
	if r.waitCountCounter != nil {
		observer.ObserveInt64(r.waitCountCounter, ps.WaitCount, metric.WithAttributes(r.baseAttrs...))
	}

	// Record cumulative timeout count
	if r.timeoutsCounter != nil {
		observer.ObserveInt64(r.timeoutsCounter, ps.Timeouts, metric.WithAttributes(r.baseAttrs...))
	}

	// Record current pending request count
	if r.pendingCountCounter != nil {
		observer.ObserveInt64(r.pendingCountCounter, ps.PendingCount, metric.WithAttributes(r.baseAttrs...))
	}

	return nil
}

// RegisterConnectionPoolMetrics registers observable metrics for connection pool metrics
// following OpenTelemetry semantic conventions v1.32.0.
//
// This function should be called once per database connection during initialization.
//
// Metrics registered per OTel spec (using UpDownCounter per OTEL semconv recommendation):
// - db.client.connection.count{state="used"}: Active connections in use
// - db.client.connection.count{state="idle"}: Idle connections in pool
// - db.client.connection.idle.max: Maximum configured idle connections
// - db.client.connection.idle.min: Minimum configured idle connections
// - db.client.connection.max: Maximum configured connections
// - db.client.connection.wait_count: Cumulative count of waits for connections from pool
// - db.client.connection.timeouts: Cumulative count of connection wait timeouts
// - db.client.connection.pending_count: Number of pending connection requests
//
// Server Metadata Attributes:
// All metrics include server.address, server.port, and db.namespace (when available) in addition
// to db.system.name for full OTel compliance and correlation with operation metrics.
//
// The metrics are updated automatically when collected (typically every 30s).
// Returns a cleanup function that can be called to unregister the metrics (optional).
//
// This function uses graceful degradation - if any metric fails to register, it continues
// with others and provides partial metrics coverage.
func RegisterConnectionPoolMetrics(conn interface {
	Stats() (map[string]any, error)
}, vendor, serverAddress string, serverPort int, namespace string) func() {
	meter := getDBMeter()
	if meter == nil {
		return noOpCleanup()
	}

	// Create registration state with normalized vendor and server metadata attributes
	baseAttrs := []attribute.KeyValue{
		attribute.String(attrDBSystem, normalizeDBVendor(vendor)),
	}

	// Add server metadata attributes for correlation with operation metrics
	if serverAddress != "" {
		baseAttrs = append(baseAttrs, attribute.String("server.address", serverAddress))
	}
	if serverPort > 0 {
		baseAttrs = append(baseAttrs, attribute.Int("server.port", serverPort))
	}
	if namespace != "" {
		baseAttrs = append(baseAttrs, attribute.String(attrDBNamespace, namespace))
	}

	reg := &poolMetricsRegistration{
		conn:      conn,
		baseAttrs: baseAttrs,
	}

	// Create UpDownCounters per OTel semantic conventions (changed from Gauge per OTEL spec)
	reg.connectionCountCounter = createObservableUpDownCounter(meter, metricDBConnectionCount,
		"Number of connections that are currently in state described by the state attribute")
	reg.idleMaxCounter = createObservableUpDownCounter(meter, metricDBConnectionIdleMax,
		"The maximum number of idle open connections allowed")
	reg.idleConfiguredCounter = createObservableUpDownCounter(meter, metricDBConnectionIdleConfigured,
		"The configured idle connections target (Go's sql.DB does not enforce a minimum)")
	reg.maxCounter = createObservableUpDownCounter(meter, metricDBConnectionMax,
		"The maximum number of open connections allowed")

	// Create counter for cumulative wait count (shows pool saturation over time)
	reg.waitCountCounter = createObservableCounter(meter, metricDBConnectionWaitCount,
		"The total number of times a connection was requested from the pool")

	// Create counter for cumulative wait timeouts
	reg.timeoutsCounter = createObservableCounter(meter, metricDBConnectionTimeouts,
		"The total number of times a connection request timed out")

	// Create up-down counter for current pending requests
	reg.pendingCountCounter = createObservableUpDownCounter(meter, metricDBConnectionPendingCount,
		"The number of connection requests currently waiting for a connection")

	// Collect non-nil instruments for callback registration
	instruments := collectObservables(
		reg.connectionCountCounter,
		reg.idleMaxCounter,
		reg.idleConfiguredCounter,
		reg.maxCounter,
		reg.waitCountCounter,
		reg.timeoutsCounter,
		reg.pendingCountCounter,
	)
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
