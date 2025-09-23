// Package database provides performance tracking for database operations
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/database"
	"github.com/gaborage/go-bricks/logger"
)

const (
	defaultSlowQueryThreshold = 200 * time.Millisecond
	defaultMaxQueryLength     = 1000
)

type trackingSettings struct {
	slowQueryThreshold time.Duration
	maxQueryLength     int
	logQueryParameters bool
}

// TrackingContext groups tracking-related parameters to reduce function parameter count
type TrackingContext struct {
	Logger   logger.Logger
	Vendor   string
	Settings trackingSettings
}

// newTrackingSettings creates trackingSettings populated from cfg.
// If cfg is nil or a numeric field is non-positive, sensible defaults are used:
// `defaultSlowQueryThreshold` for slowQueryThreshold and `defaultMaxQueryLength` for maxQueryLength.
// The LogQueryParameters flag from cfg is copied into logQueryParameters.
func newTrackingSettings(cfg *config.DatabaseConfig) trackingSettings {
	settings := trackingSettings{
		slowQueryThreshold: defaultSlowQueryThreshold,
		maxQueryLength:     defaultMaxQueryLength,
		logQueryParameters: false,
	}

	if cfg == nil {
		return settings
	}

	if cfg.Query.Slow.Threshold > 0 {
		settings.slowQueryThreshold = cfg.Query.Slow.Threshold
	}
	if cfg.Query.Log.MaxLength > 0 {
		settings.maxQueryLength = cfg.Query.Log.MaxLength
	}
	settings.logQueryParameters = cfg.Query.Log.Parameters

	return settings
}

// TrackedDB wraps sql.DB to provide request-scoped performance tracking
type TrackedDB struct {
	*sql.DB
	logger   logger.Logger
	vendor   string
	settings trackingSettings
}

// NewTrackedDB creates a TrackedDB that wraps the provided *sql.DB to record query and
// execution metrics (durations, truncated queries, optional parameter logging).
// It uses log for emitting structured logs, vendor to identify the database type, and
// derives per-connection tracking settings from cfg (when non-nil) to override defaults.
func NewTrackedDB(db *sql.DB, log logger.Logger, vendor string, cfg *config.DatabaseConfig) *TrackedDB {
	return &TrackedDB{
		DB:       db,
		logger:   log,
		vendor:   vendor,
		settings: newTrackingSettings(cfg),
	}
}

// QueryContext executes a query with context and tracks performance
func (db *TrackedDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := db.DB.QueryContext(ctx, query, args...)

	// Track performance metrics
	db.trackQuery(ctx, query, args, start, err)

	return rows, err
}

// QueryRowContext executes a single row query with context and tracks performance
func (db *TrackedDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	start := time.Now()
	row := db.DB.QueryRowContext(ctx, query, args...)

	// Track performance metrics (error will be checked when row is scanned)
	db.trackQuery(ctx, query, args, start, nil)

	return row
}

// ExecContext executes a query without returning rows and tracks performance
func (db *TrackedDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := db.DB.ExecContext(ctx, query, args...)

	// Track performance metrics
	db.trackQuery(ctx, query, args, start, err)

	return result, err
}

// BasicStatement wraps sql.Stmt to implement database.Statement
type BasicStatement struct {
	*sql.Stmt
}

func (s *BasicStatement) Query(ctx context.Context, args ...any) (*sql.Rows, error) {
	return s.QueryContext(ctx, args...)
}

func (s *BasicStatement) QueryRow(ctx context.Context, args ...any) *sql.Row {
	return s.QueryRowContext(ctx, args...)
}

func (s *BasicStatement) Exec(ctx context.Context, args ...any) (sql.Result, error) {
	return s.ExecContext(ctx, args...)
}

func (s *BasicStatement) Close() error {
	return s.Stmt.Close()
}

// PrepareContext prepares a statement with context and tracks performance
func (db *TrackedDB) PrepareContext(ctx context.Context, query string) (database.Statement, error) {
	start := time.Now()
	stmt, err := db.DB.PrepareContext(ctx, query)

	// Track performance metrics
	db.trackQuery(ctx, "PREPARE: "+query, nil, start, err)

	if stmt != nil {
		return &TrackedStmt{Statement: &BasicStatement{stmt}, logger: db.logger, vendor: db.vendor, settings: db.settings, query: query}, nil
	}

	return nil, err
}

// trackQuery records database operation metrics
func (db *TrackedDB) trackQuery(ctx context.Context, query string, args []any, start time.Time, err error) {
	tc := &TrackingContext{
		Logger:   db.logger,
		Vendor:   db.vendor,
		Settings: db.settings,
	}
	trackDBOperation(ctx, tc, query, args, start, err)
}

// TrackedStmt wraps database.Statement to provide performance tracking for prepared statements
type TrackedStmt struct {
	database.Statement
	logger   logger.Logger
	vendor   string
	settings trackingSettings
	query    string
}

// Query executes a prepared query with context and tracks performance
func (s *TrackedStmt) Query(ctx context.Context, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := s.Statement.Query(ctx, args...)

	// Track performance metrics
	s.trackStmt(ctx, "STMT_QUERY", args, start, err)

	return rows, err
}

// QueryRow executes a prepared single row query with context and tracks performance
func (s *TrackedStmt) QueryRow(ctx context.Context, args ...any) *sql.Row {
	start := time.Now()
	row := s.Statement.QueryRow(ctx, args...)

	// Track performance metrics
	s.trackStmt(ctx, "STMT_QUERY_ROW", args, start, nil)

	return row
}

// Exec executes a prepared statement with context and tracks performance
func (s *TrackedStmt) Exec(ctx context.Context, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := s.Statement.Exec(ctx, args...)

	// Track performance metrics
	s.trackStmt(ctx, "STMT_EXEC", args, start, err)

	return result, err
}

// trackStmt records prepared statement metrics
func (s *TrackedStmt) trackStmt(ctx context.Context, operation string, args []any, start time.Time, err error) {
	op := operation
	if s.query != "" {
		op = operation + ": " + s.query
	}
	tc := &TrackingContext{
		Logger:   s.logger,
		Vendor:   s.vendor,
		Settings: s.settings,
	}
	trackDBOperation(ctx, tc, op, args, start, err)
}

// TrackedTx wraps sql.Tx to provide performance tracking for transactions
type TrackedTx struct {
	*sql.Tx
	logger   logger.Logger
	vendor   string
	settings trackingSettings
}

// QueryContext executes a query within a transaction with context and tracks performance
func (tx *TrackedTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := tx.Tx.QueryContext(ctx, query, args...)

	// Track performance metrics
	tx.trackTx(ctx, query, args, start, err)

	return rows, err
}

// QueryRowContext executes a single row query within a transaction and tracks performance
func (tx *TrackedTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	start := time.Now()
	row := tx.Tx.QueryRowContext(ctx, query, args...)

	// Track performance metrics
	tx.trackTx(ctx, query, args, start, nil)

	return row
}

// ExecContext executes a query within a transaction and tracks performance
func (tx *TrackedTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := tx.Tx.ExecContext(ctx, query, args...)

	// Track performance metrics
	tx.trackTx(ctx, query, args, start, err)

	return result, err
}

// trackTx records transaction operation metrics
func (tx *TrackedTx) trackTx(ctx context.Context, query string, args []any, start time.Time, err error) {
	tc := &TrackingContext{
		Logger:   tx.logger,
		Vendor:   tx.vendor,
		Settings: tx.settings,
	}
	trackDBOperation(ctx, tc, query, args, start, err)
}

// TrackedConnection wraps database.Interface to provide performance tracking
type TrackedConnection struct {
	conn     database.Interface
	logger   logger.Logger
	vendor   string
	settings trackingSettings
}

// NewTrackedConnection returns a database.Interface that wraps conn and records query/operation
// metrics and logs. The wrapper delegates all calls to the provided conn, uses conn.DatabaseType()
// as the vendor identifier, and derives per-connection tracking settings from cfg via newTrackingSettings.
func NewTrackedConnection(conn database.Interface, log logger.Logger, cfg *config.DatabaseConfig) database.Interface {
	return &TrackedConnection{
		conn:     conn,
		logger:   log,
		vendor:   conn.DatabaseType(),
		settings: newTrackingSettings(cfg),
	}
}

// Query executes a query that returns rows with tracking
func (tc *TrackedConnection) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := tc.conn.Query(ctx, query, args...)

	tc.trackOperation(ctx, query, args, start, err)
	return rows, err
}

// QueryRow executes a query that returns at most one row with tracking
func (tc *TrackedConnection) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	start := time.Now()
	row := tc.conn.QueryRow(ctx, query, args...)

	tc.trackOperation(ctx, query, args, start, nil)
	return row
}

// Exec executes a query without returning any rows with tracking
func (tc *TrackedConnection) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := tc.conn.Exec(ctx, query, args...)

	tc.trackOperation(ctx, query, args, start, err)
	return result, err
}

// Prepare creates a prepared statement with tracking
func (tc *TrackedConnection) Prepare(ctx context.Context, query string) (database.Statement, error) {
	start := time.Now()
	stmt, err := tc.conn.Prepare(ctx, query)

	tc.trackOperation(ctx, "PREPARE: "+query, nil, start, err)

	if err != nil {
		return nil, err
	}

	return &TrackedStatement{
		stmt:     stmt,
		logger:   tc.logger,
		vendor:   tc.vendor,
		settings: tc.settings,
		query:    query,
	}, nil
}

// Begin starts a transaction with tracking wrapper
func (tc *TrackedConnection) Begin(ctx context.Context) (database.Tx, error) {
	tx, err := tc.conn.Begin(ctx)
	if err != nil {
		return nil, err
	}

	return &TrackedTransaction{
		tx:       tx,
		logger:   tc.logger,
		vendor:   tc.vendor,
		settings: tc.settings,
	}, nil
}

// BeginTx starts a transaction with options and tracking wrapper
func (tc *TrackedConnection) BeginTx(ctx context.Context, opts *sql.TxOptions) (database.Tx, error) {
	tx, err := tc.conn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}

	return &TrackedTransaction{
		tx:       tx,
		logger:   tc.logger,
		vendor:   tc.vendor,
		settings: tc.settings,
	}, nil
}

// Health checks database connectivity (no tracking needed)
func (tc *TrackedConnection) Health(ctx context.Context) error {
	return tc.conn.Health(ctx)
}

// Stats returns database connection statistics (no tracking needed)
func (tc *TrackedConnection) Stats() (map[string]any, error) {
	return tc.conn.Stats()
}

// Close closes the database connection (no tracking needed)
func (tc *TrackedConnection) Close() error {
	return tc.conn.Close()
}

// DatabaseType returns the database type (no tracking needed)
func (tc *TrackedConnection) DatabaseType() string {
	return tc.conn.DatabaseType()
}

// GetMigrationTable returns the migration table name (no tracking needed)
func (tc *TrackedConnection) GetMigrationTable() string {
	return tc.conn.GetMigrationTable()
}

// CreateMigrationTable creates the migration table if it doesn't exist with tracking
func (tc *TrackedConnection) CreateMigrationTable(ctx context.Context) error {
	start := time.Now()
	err := tc.conn.CreateMigrationTable(ctx)

	tc.trackOperation(ctx, "CREATE_MIGRATION_TABLE", nil, start, err)
	return err
}

// trackOperation records database operation metrics
func (tc *TrackedConnection) trackOperation(ctx context.Context, query string, args []any, start time.Time, err error) {
	trackingCtx := &TrackingContext{
		Logger:   tc.logger,
		Vendor:   tc.vendor,
		Settings: tc.settings,
	}
	trackDBOperation(ctx, trackingCtx, query, args, start, err)
}

// trackDBOperation records timing and outcome metrics for a database operation and emits a structured log entry.
// It is a no-op if tc or tc.Logger is nil. The function:
// - measures elapsed time since start and increments global DB counters with the elapsed nanoseconds,
// - truncates the query to tc.Settings.maxQueryLength and, when enabled, attaches sanitized query parameters,
// - logs sql.ErrNoRows at Debug level (not treated as an error), other errors at Error level,
// - logs a Warn when the elapsed time exceeds tc.Settings.slowQueryThreshold, otherwise logs a Debug completion message.
func trackDBOperation(ctx context.Context, tc *TrackingContext, query string, args []any, start time.Time, err error) {
	// Guard against nil tracking context or logger with no-op default
	if tc == nil || tc.Logger == nil {
		return
	}

	elapsed := time.Since(start)

	// Increment database operation counter for request tracking
	logger.IncrementDBCounter(ctx)
	logger.AddDBElapsed(ctx, elapsed.Nanoseconds())

	// Truncate query string to safe max length to avoid unbounded payloads
	truncatedQuery := query
	if tc.Settings.maxQueryLength > 0 && len(query) > tc.Settings.maxQueryLength {
		truncatedQuery = truncateString(query, tc.Settings.maxQueryLength)
	}

	// Log query execution details
	logEvent := tc.Logger.WithContext(ctx).WithFields(map[string]any{
		"vendor":      tc.Vendor,
		"duration_ms": elapsed.Milliseconds(),
		"duration_ns": elapsed.Nanoseconds(),
		"query":       truncatedQuery,
	})

	if tc.Settings.logQueryParameters && len(args) > 0 {
		logEvent = logEvent.WithFields(map[string]any{
			"args": sanitizeArgs(args, tc.Settings.maxQueryLength),
		})
	}

	if err != nil {
		// Treat sql.ErrNoRows specially - not an actual error, log as debug
		if errors.Is(err, sql.ErrNoRows) {
			logEvent.Debug().Msg("Database operation returned no rows")
		} else {
			logEvent.Error().Err(err).Msg("Database operation error")
		}
	} else if elapsed > tc.Settings.slowQueryThreshold {
		logEvent.Warn().Msgf("Slow database operation detected (%s)", elapsed)
	} else {
		logEvent.Debug().Msg("Database operation executed")
	}
}

// truncateString returns value truncated to at most maxLen characters.
// If maxLen <= 0 or value is already shorter than or equal to maxLen, the
// original string is returned. When maxLen <= 3 the function returns the
// first maxLen characters (no ellipsis); otherwise it returns the first
// maxLen-3 characters followed by "..." to indicate truncation.
func truncateString(value string, maxLen int) string {
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	if maxLen <= 3 {
		return value[:maxLen]
	}
	return value[:maxLen-3] + "..."
}

// sanitizeArgs returns a sanitized copy of the provided argument slice suitable for logging.
//
// If args is empty, it returns nil. String values are truncated using truncateString with
// maxLen; byte slices are replaced with the placeholder "<bytes len=N>"; all other values
// are formatted with "%v" and then truncated using truncateString. The returned slice has
// the same length and element order as the input (unless args is empty).
func sanitizeArgs(args []any, maxLen int) []any {
	if len(args) == 0 {
		return nil
	}
	sanitized := make([]any, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case string:
			sanitized[i] = truncateString(v, maxLen)
		case []byte:
			sanitized[i] = fmt.Sprintf("<bytes len=%d>", len(v))
		default:
			sanitized[i] = truncateString(fmt.Sprintf("%v", v), maxLen)
		}
	}
	return sanitized
}

// TrackedStatement wraps database.Statement to provide performance tracking
type TrackedStatement struct {
	stmt     database.Statement
	logger   logger.Logger
	vendor   string
	query    string
	settings trackingSettings
}

// Query executes a prepared query with tracking
func (ts *TrackedStatement) Query(ctx context.Context, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := ts.stmt.Query(ctx, args...)

	tc := &TrackingContext{Logger: ts.logger, Vendor: ts.vendor, Settings: ts.settings}
	trackDBOperation(ctx, tc, "STMT_QUERY: "+ts.query, args, start, err)
	return rows, err
}

// QueryRow executes a prepared query that returns a single row with tracking
func (ts *TrackedStatement) QueryRow(ctx context.Context, args ...any) *sql.Row {
	start := time.Now()
	row := ts.stmt.QueryRow(ctx, args...)

	tc := &TrackingContext{Logger: ts.logger, Vendor: ts.vendor, Settings: ts.settings}
	trackDBOperation(ctx, tc, "STMT_QUERY_ROW: "+ts.query, args, start, nil)
	return row
}

// Exec executes a prepared statement with tracking
func (ts *TrackedStatement) Exec(ctx context.Context, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := ts.stmt.Exec(ctx, args...)

	tc := &TrackingContext{Logger: ts.logger, Vendor: ts.vendor, Settings: ts.settings}
	trackDBOperation(ctx, tc, "STMT_EXEC: "+ts.query, args, start, err)
	return result, err
}

// Close closes the prepared statement (no tracking needed)
func (ts *TrackedStatement) Close() error {
	return ts.stmt.Close()
}

// TrackedTransaction wraps database.Tx to provide performance tracking
type TrackedTransaction struct {
	tx       database.Tx
	logger   logger.Logger
	vendor   string
	settings trackingSettings
}

// Query executes a query within the transaction with tracking
func (tt *TrackedTransaction) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := tt.tx.Query(ctx, query, args...)

	tc := &TrackingContext{Logger: tt.logger, Vendor: tt.vendor, Settings: tt.settings}
	trackDBOperation(ctx, tc, "TX_QUERY: "+query, args, start, err)
	return rows, err
}

// QueryRow executes a query that returns a single row within the transaction with tracking
func (tt *TrackedTransaction) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	start := time.Now()
	row := tt.tx.QueryRow(ctx, query, args...)

	tc := &TrackingContext{Logger: tt.logger, Vendor: tt.vendor, Settings: tt.settings}
	trackDBOperation(ctx, tc, "TX_QUERY_ROW: "+query, args, start, nil)
	return row
}

// Exec executes a query without returning rows within the transaction with tracking
func (tt *TrackedTransaction) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := tt.tx.Exec(ctx, query, args...)

	tc := &TrackingContext{Logger: tt.logger, Vendor: tt.vendor, Settings: tt.settings}
	trackDBOperation(ctx, tc, "TX_EXEC: "+query, args, start, err)
	return result, err
}

// Prepare creates a prepared statement within the transaction with tracking
func (tt *TrackedTransaction) Prepare(ctx context.Context, query string) (database.Statement, error) {
	start := time.Now()
	stmt, err := tt.tx.Prepare(ctx, query)

	tc := &TrackingContext{Logger: tt.logger, Vendor: tt.vendor, Settings: tt.settings}
	trackDBOperation(ctx, tc, "TX_PREPARE: "+query, nil, start, err)

	if err != nil {
		return nil, err
	}

	return &TrackedStatement{
		stmt:     stmt,
		logger:   tt.logger,
		vendor:   tt.vendor,
		query:    query,
		settings: tt.settings,
	}, nil
}

// Commit commits the transaction (no tracking needed)
func (tt *TrackedTransaction) Commit() error {
	return tt.tx.Commit()
}

// Rollback rolls back the transaction (no tracking needed)
func (tt *TrackedTransaction) Rollback() error {
	return tt.tx.Rollback()
}
