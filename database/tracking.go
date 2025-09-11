// Package database provides performance tracking for database operations
package database

import (
	"context"
	"database/sql"
	"time"

	"github.com/gaborage/go-bricks/logger"
)

// TrackedDB wraps sql.DB to provide request-scoped performance tracking
type TrackedDB struct {
	*sql.DB
	logger logger.Logger
	vendor string
}

// NewTrackedDB creates a new tracked database connection
func NewTrackedDB(db *sql.DB, log logger.Logger, vendor string) *TrackedDB {
	return &TrackedDB{
		DB:     db,
		logger: log,
		vendor: vendor,
	}
}

// QueryContext executes a query with context and tracks performance
func (db *TrackedDB) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	start := time.Now()
	rows, err := db.DB.QueryContext(ctx, query, args...)

	// Track performance metrics
	db.trackQuery(ctx, query, start, err)

	return rows, err
}

// QueryRowContext executes a single row query with context and tracks performance
func (db *TrackedDB) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	start := time.Now()
	row := db.DB.QueryRowContext(ctx, query, args...)

	// Track performance metrics (error will be checked when row is scanned)
	db.trackQuery(ctx, query, start, nil)

	return row
}

// ExecContext executes a query without returning rows and tracks performance
func (db *TrackedDB) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	start := time.Now()
	result, err := db.DB.ExecContext(ctx, query, args...)

	// Track performance metrics
	db.trackQuery(ctx, query, start, err)

	return result, err
}

// PrepareContext prepares a statement with context and tracks performance
func (db *TrackedDB) PrepareContext(ctx context.Context, query string) (*TrackedStmt, error) {
	start := time.Now()
	stmt, err := db.DB.PrepareContext(ctx, query)

	// Track performance metrics
	db.trackQuery(ctx, "PREPARE: "+query, start, err)

	if stmt != nil {
		return &TrackedStmt{Stmt: stmt, logger: db.logger, vendor: db.vendor}, nil
	}

	return nil, err
}

// trackQuery records database operation metrics
func (db *TrackedDB) trackQuery(ctx context.Context, query string, start time.Time, err error) {
	trackDBOperation(ctx, db.logger, db.vendor, query, start, err)
}

// TrackedStmt wraps sql.Stmt to provide performance tracking for prepared statements
type TrackedStmt struct {
	*sql.Stmt
	logger logger.Logger
	vendor string
}

// QueryContext executes a prepared query with context and tracks performance
func (s *TrackedStmt) QueryContext(ctx context.Context, args ...interface{}) (*sql.Rows, error) {
	start := time.Now()
	rows, err := s.Stmt.QueryContext(ctx, args...)

	// Track performance metrics
	s.trackStmt(ctx, "STMT_QUERY", start, err)

	return rows, err
}

// QueryRowContext executes a prepared single row query with context and tracks performance
func (s *TrackedStmt) QueryRowContext(ctx context.Context, args ...interface{}) *sql.Row {
	start := time.Now()
	row := s.Stmt.QueryRowContext(ctx, args...)

	// Track performance metrics
	s.trackStmt(ctx, "STMT_QUERY_ROW", start, nil)

	return row
}

// ExecContext executes a prepared statement with context and tracks performance
func (s *TrackedStmt) ExecContext(ctx context.Context, args ...interface{}) (sql.Result, error) {
	start := time.Now()
	result, err := s.Stmt.ExecContext(ctx, args...)

	// Track performance metrics
	s.trackStmt(ctx, "STMT_EXEC", start, err)

	return result, err
}

// trackStmt records prepared statement metrics
func (s *TrackedStmt) trackStmt(ctx context.Context, operation string, start time.Time, err error) {
	trackDBOperation(ctx, s.logger, s.vendor, operation, start, err)
}

// TrackedTx wraps sql.Tx to provide performance tracking for transactions
type TrackedTx struct {
	*sql.Tx
	logger logger.Logger
	vendor string
}

// QueryContext executes a query within a transaction with context and tracks performance
func (tx *TrackedTx) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	start := time.Now()
	rows, err := tx.Tx.QueryContext(ctx, query, args...)

	// Track performance metrics
	tx.trackTx(ctx, query, start, err)

	return rows, err
}

// QueryRowContext executes a single row query within a transaction and tracks performance
func (tx *TrackedTx) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	start := time.Now()
	row := tx.Tx.QueryRowContext(ctx, query, args...)

	// Track performance metrics
	tx.trackTx(ctx, query, start, nil)

	return row
}

// ExecContext executes a query within a transaction and tracks performance
func (tx *TrackedTx) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	start := time.Now()
	result, err := tx.Tx.ExecContext(ctx, query, args...)

	// Track performance metrics
	tx.trackTx(ctx, query, start, err)

	return result, err
}

// trackTx records transaction operation metrics
func (tx *TrackedTx) trackTx(ctx context.Context, query string, start time.Time, err error) {
	trackDBOperation(ctx, tx.logger, tx.vendor, query, start, err)
}

// TrackedConnection wraps database.Interface to provide performance tracking
type TrackedConnection struct {
	conn   Interface
	logger logger.Logger
	vendor string
}

// NewTrackedConnection creates a new tracked database connection that wraps any Interface implementation
func NewTrackedConnection(conn Interface, log logger.Logger) Interface {
	return &TrackedConnection{
		conn:   conn,
		logger: log,
		vendor: conn.DatabaseType(),
	}
}

// Query executes a query that returns rows with tracking
func (tc *TrackedConnection) Query(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	start := time.Now()
	rows, err := tc.conn.Query(ctx, query, args...)

	tc.trackOperation(ctx, query, start, err)
	return rows, err
}

// QueryRow executes a query that returns at most one row with tracking
func (tc *TrackedConnection) QueryRow(ctx context.Context, query string, args ...interface{}) *sql.Row {
	start := time.Now()
	row := tc.conn.QueryRow(ctx, query, args...)

	tc.trackOperation(ctx, query, start, nil)
	return row
}

// Exec executes a query without returning any rows with tracking
func (tc *TrackedConnection) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	start := time.Now()
	result, err := tc.conn.Exec(ctx, query, args...)

	tc.trackOperation(ctx, query, start, err)
	return result, err
}

// Prepare creates a prepared statement with tracking
func (tc *TrackedConnection) Prepare(ctx context.Context, query string) (*sql.Stmt, error) {
	start := time.Now()
	stmt, err := tc.conn.Prepare(ctx, query)

	tc.trackOperation(ctx, "PREPARE: "+query, start, err)
	return stmt, err
}

// Begin starts a transaction (no tracking needed for begin itself)
func (tc *TrackedConnection) Begin(ctx context.Context) (*sql.Tx, error) {
	return tc.conn.Begin(ctx)
}

// BeginTx starts a transaction with options (no tracking needed for begin itself)
func (tc *TrackedConnection) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return tc.conn.BeginTx(ctx, opts)
}

// Health checks database connectivity (no tracking needed)
func (tc *TrackedConnection) Health(ctx context.Context) error {
	return tc.conn.Health(ctx)
}

// Stats returns database connection statistics (no tracking needed)
func (tc *TrackedConnection) Stats() (map[string]interface{}, error) {
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

	tc.trackOperation(ctx, "CREATE_MIGRATION_TABLE", start, err)
	return err
}

// trackOperation records database operation metrics
func (tc *TrackedConnection) trackOperation(ctx context.Context, query string, start time.Time, err error) {
	trackDBOperation(ctx, tc.logger, tc.vendor, query, start, err)
}

// trackDBOperation is a shared function to record database operation metrics
func trackDBOperation(ctx context.Context, log logger.Logger, vendor, query string, start time.Time, err error) {
	elapsed := time.Since(start)

	// Increment database operation counter for request tracking
	logger.IncrementDBCounter(ctx)
	logger.AddDBElapsed(ctx, elapsed.Nanoseconds())

	// Log query execution details
	logEvent := log.WithContext(ctx).WithFields(map[string]interface{}{
		"vendor":      vendor,
		"duration_ms": elapsed.Milliseconds(),
		"duration_ns": elapsed.Nanoseconds(),
		"query":       query,
	})

	if err != nil {
		logEvent.Error().Err(err).Msg("Database operation error")
	} else if elapsed > 200*time.Millisecond { // Slow query threshold
		logEvent.Warn().Msgf("Slow database operation detected (%s)", elapsed)
	} else {
		logEvent.Debug().Msg("Database operation executed")
	}
}
