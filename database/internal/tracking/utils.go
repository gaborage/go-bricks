package tracking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/logger"
)

const (
	// Default operation type for unidentified queries
	defaultOperation = "query"

	// Database vendor normalization constants matching OTel semantic conventions
	dbVendorPostgreSQL = "postgresql"
	dbVendorOracle     = "oracle.db" // OTel spec requires "oracle.db" not "oracle"
	dbVendorMySQL      = "mysql"
	dbVendorSQLite     = "sqlite"

	// OpenTelemetry instrumentation constants
	dbTracerName      = "go-bricks/database" // Tracer name for database operations
	maxDBQueryAttrLen = 2000                 // Maximum length for db.query.text attribute
)

// TrackDBOperation logs database operation performance metrics and errors.
// It provides centralized tracking for all database operations including queries,
// statements, and transactions. The function handles slow query detection,
// TrackDBOperation records metrics and emits a log event for a completed database operation.
//
// TrackDBOperation is a no-op if tc or its Logger is nil. It records the operation's duration to
// request-scoped metrics, clamps the query string to the configured maximum length, and — when
// enabled — includes a sanitized form of parameters suitable for logging. If err is non-nil the
// error is logged (with sql.ErrNoRows logged at debug level); if there is no error and the duration
// exceeds the configured slow-query threshold a warning is emitted, otherwise a debug message is
// emitted.
//
// The rowsAffected parameter represents the number of rows affected by write operations (INSERT, UPDATE, DELETE).
// For read operations (SELECT), pass 0.
func TrackDBOperation(ctx context.Context, tc *Context, query string, args []any, start time.Time, rowsAffected int64, err error) {
	// Guard against nil tracking context or logger with no-op default
	if tc == nil || tc.Logger == nil {
		return
	}

	elapsed := time.Since(start)

	// Increment database operation counter for request tracking
	if ctx != nil {
		logger.IncrementDBCounter(ctx)
		logger.AddDBElapsed(ctx, elapsed.Nanoseconds())
	}

	// Create OpenTelemetry span for database operation with accurate timing
	if ctx != nil {
		createDBSpan(ctx, tc, query, start, err)
	}

	// Record OpenTelemetry metrics for database operation
	if ctx != nil {
		recordDBMetrics(ctx, tc, query, elapsed, rowsAffected, err)
	}

	// Truncate query string to safe max length to avoid unbounded payloads
	truncatedQuery := query
	if tc.Settings.MaxQueryLength() > 0 && len(query) > tc.Settings.MaxQueryLength() {
		truncatedQuery = TruncateString(query, tc.Settings.MaxQueryLength())
	}

	// Log query execution details
	logEvent := tc.Logger.WithContext(ctx).WithFields(map[string]any{
		"vendor":      tc.Vendor,
		"duration_ms": elapsed.Milliseconds(),
		"duration_ns": elapsed.Nanoseconds(),
		"query":       truncatedQuery,
	})

	if tc.Settings.LogQueryParameters() && len(args) > 0 {
		logEvent = logEvent.WithFields(map[string]any{
			"args": SanitizeArgs(args, tc.Settings.MaxQueryLength()),
		})
	}

	if err != nil {
		// Treat sql.ErrNoRows specially - not an actual error, log as debug
		if errors.Is(err, sql.ErrNoRows) {
			logEvent.Debug().Msg("Database operation returned no rows")
		} else {
			logEvent.Error().Err(err).Msg("Database operation error")
		}
	} else if elapsed > tc.Settings.SlowQueryThreshold() {
		logEvent.Warn().Msgf("Slow database operation detected (%s)", elapsed)
	} else {
		logEvent.Debug().Msg("Database operation executed")
	}
}

// extractRowsAffected safely extracts the number of rows affected from a sql.Result.
// Returns 0 if the result is nil, an error occurred during query execution, or
// RowsAffected() fails. This is a best-effort helper for I/O metrics tracking.
func extractRowsAffected(result sql.Result, err error) int64 {
	if result == nil || err != nil {
		return 0
	}

	affected, affErr := result.RowsAffected()
	if affErr != nil {
		return 0
	}

	return affected
}

// TruncateString returns value truncated to at most maxLen characters.
// If maxLen <= 0 or value is already shorter than or equal to maxLen, the
// original string is returned. When maxLen <= 3 the function returns the
// first maxLen characters (no ellipsis); otherwise it returns the first
// TruncateString truncates value to at most maxLen runes, adding "..." when space allows to indicate truncation.
//
// If maxLen <= 0 the original value is returned unchanged. If the string's rune count is less than or equal to
// maxLen the original value is returned. When maxLen <= 3 the function returns the first maxLen runes without an
// ellipsis. For maxLen > 3 the result contains the first (maxLen-3) runes followed by "...". Multi-byte characters
// are handled safely by operating on runes.
func TruncateString(value string, maxLen int) string {
	if maxLen <= 0 {
		return value
	}
	r := []rune(value)
	if len(r) <= maxLen {
		return value
	}
	// Handle multi-byte characters correctly
	if maxLen <= 3 {
		return string(r[:maxLen])
	}
	return string(r[:maxLen-3]) + "..."
}

// SanitizeArgs returns a sanitized copy of the input argument slice suitable for logging.
//
// For string values the returned element is truncated to at most maxLen runes. For []byte
// values the returned element is the placeholder "<bytes len=N>" where N is the byte length.
// For all other values the element is formatted with "%v" and then truncated to at most
// maxLen runes. The returned slice preserves the input order and length. If args is empty,
// nil is returned.
func SanitizeArgs(args []any, maxLen int) []any {
	if len(args) == 0 {
		return nil
	}
	sanitized := make([]any, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case string:
			sanitized[i] = TruncateString(v, maxLen)
		case []byte:
			sanitized[i] = fmt.Sprintf("<bytes len=%d>", len(v))
		default:
			sanitized[i] = TruncateString(fmt.Sprintf("%v", v), maxLen)
		}
	}
	return sanitized
}

// createDBSpan creates an OpenTelemetry span for a database operation.
// It adds standard database semantic attributes per OTel spec v1.32.0 and records errors.
// createDBSpan starts an OpenTelemetry span for a database operation using the provided start time.
// It sets standard DB and network attributes (including `db.system.name`, `db.query.text`, `db.operation.name`,
// `db.collection.name`, `db.namespace`, `server.address`, and `server.port`) when available, records errors
// (excluding `sql.ErrNoRows`) on the span, and ends the span.
func createDBSpan(ctx context.Context, tc *Context, query string, start time.Time, err error) {
	tracer := otel.Tracer(dbTracerName)

	// Determine operation type and table/collection name from query
	operation := extractDBOperation(query)
	table := extractTableName(query)
	spanName := fmt.Sprintf("db.%s", operation)

	// Start span with the actual operation start time for accurate timing
	_, span := tracer.Start(ctx, spanName,
		trace.WithTimestamp(start),
		trace.WithSpanKind(trace.SpanKindClient),
	)

	// Add database semantic attributes per OTel v1.32.0 spec
	// Truncate query for safety (span attributes should be reasonable size)
	truncatedQuery := query
	if len(query) > maxDBQueryAttrLen {
		truncatedQuery = TruncateString(query, maxDBQueryAttrLen)
	}

	// Required and recommended attributes per OTel spec
	attrs := []attribute.KeyValue{
		attribute.String("db.system.name", normalizeDBVendor(tc.Vendor)), // db.system.name (required)
		semconv.DBQueryText(truncatedQuery),                              // db.query.text (recommended)
	}

	// Add operation name if identified
	if operation != defaultOperation {
		attrs = append(attrs, semconv.DBOperationName(operation)) // db.operation.name (recommended)
	}

	// Add collection/table name if identified
	if table != "" && table != "unknown" {
		attrs = append(attrs, semconv.DBCollectionName(table)) // db.collection.name (recommended)
	}

	// Add namespace if available (conditionally required per OTel spec)
	if tc.Namespace != "" {
		attrs = append(attrs, semconv.DBNamespace(tc.Namespace)) // db.namespace
	}

	// Add server connection info if available (recommended per OTel spec)
	if tc.ServerAddress != "" {
		attrs = append(attrs, semconv.ServerAddress(tc.ServerAddress)) // server.address
	}
	if tc.ServerPort > 0 {
		attrs = append(attrs, semconv.ServerPort(tc.ServerPort)) // server.port
	}

	span.SetAttributes(attrs...)

	// Record error status
	if err != nil {
		// sql.ErrNoRows is not an actual error - it's a normal empty result
		if !errors.Is(err, sql.ErrNoRows) {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}

	// End the span (will use current time, giving us the correct duration)
	span.End()
}

// extractDBOperation extracts the operation type from a SQL query.
// extractDBOperation determines the database operation name from the given SQL query.
// It returns a lowercase operation such as "select", "insert", "update", "delete",
// "create", "drop", "alter", or "truncate". If the query is empty or the first token
// is not a recognized operation, it returns defaultOperation. Special cases are also
// handled for PREPARE:, BEGIN/BEGIN_TX -> "begin", COMMIT -> "commit", ROLLBACK -> "rollback",
// and CREATE_MIGRATION_TABLE -> "create_table".
func extractDBOperation(query string) string {
	// Trim whitespace and get first word
	q := strings.TrimSpace(query)
	if q == "" {
		return defaultOperation
	}

	// Handle special operations
	q = strings.TrimSuffix(q, ";")
	upper := strings.ToUpper(q)

	// Handle PREPARE: prefix
	if strings.HasPrefix(upper, "PREPARE:") {
		return "prepare"
	}

	switch upper {
	case "BEGIN", "BEGIN_TX":
		return "begin"
	case "COMMIT":
		return "commit"
	case "ROLLBACK":
		return "rollback"
	case "CREATE_MIGRATION_TABLE":
		return "create_table"
	}

	// Extract first word (SQL command)
	parts := strings.FieldsFunc(q, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '(', ';':
			return true
		default:
			return false
		}
	})

	if len(parts) == 0 {
		return defaultOperation
	}

	operation := strings.ToLower(parts[0])
	switch operation {
	case "select", "insert", "update", "delete", "create", "drop", "alter", "truncate":
		return operation
	default:
		return defaultOperation
	}
}

// normalizeDBVendor normalizes the database vendor name to match OTel semantic conventions.
// Returns vendor-specific db.system.name values per OTel spec:
// - "postgresql" for PostgreSQL
// - "oracle.db" for Oracle (note the .db suffix required by spec)
// normalizeDBVendor maps common database vendor identifiers to the OpenTelemetry
// `db.system.name` values.
// It lowercases the input and maps known aliases (for example: "postgres" or
// "postgresql" → "postgresql", "oracle" → "oracle.db",
// "mysql" → "mysql", "sqlite" or "sqlite3" → "sqlite"). If no mapping
// applies, the lowercased input is returned unchanged.
func normalizeDBVendor(vendor string) string {
	vendor = strings.ToLower(vendor)
	switch vendor {
	case "postgres", dbVendorPostgreSQL:
		return dbVendorPostgreSQL
	case "oracle", dbVendorOracle:
		return dbVendorOracle // Returns "oracle.db" per OTel spec
	case dbVendorMySQL:
		return dbVendorMySQL
	case dbVendorSQLite, "sqlite3":
		return dbVendorSQLite
	default:
		return vendor
	}
}

// BuildPostgreSQLNamespace builds the db.namespace attribute for PostgreSQL.
// Per OTel spec, this combines database and schema name as "{database}.{schema}".
// Returns empty string when schema is unknown (don't assume defaults like "public").
// Only returns "{database}.{schema}" when both database and schema are provided.
func BuildPostgreSQLNamespace(database, schema string) string {
	if database == "" || schema == "" {
		return ""
	}
	return database + "." + schema
}

// BuildOracleNamespace builds the db.namespace attribute for Oracle.
// Per OTel spec, format is "{service_name}|{sid}|{database}".
// Returns empty string only when all inputs are empty. When any value is provided,
// returns the full format with empty placeholders for missing values.
// Examples: "PRODDB||", "|ORCL|", "||mydb", "PRODDB|ORCL|mydb"
func BuildOracleNamespace(serviceName, sid, database string) string {
	// Return empty only if all values are empty
	if serviceName == "" && sid == "" && database == "" {
		return ""
	}
	// Return full format with all values (empty strings for missing ones)
	return serviceName + "|" + sid + "|" + database
}
