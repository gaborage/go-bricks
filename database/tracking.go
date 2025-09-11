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
	elapsed := time.Since(start)

	// Increment database operation counter for request tracking
	logger.IncrementDBCounter(ctx)
	logger.AddDBElapsed(ctx, elapsed.Nanoseconds())

	// Log query execution details
	logEvent := db.logger.WithContext(ctx).WithFields(map[string]interface{}{
		"vendor":      db.vendor,
		"duration_ms": elapsed.Milliseconds(),
		"duration_ns": elapsed.Nanoseconds(),
		"query":       query,
	})

	if err != nil {
		logEvent.Error().Err(err).Msg("Database query error")
	} else if elapsed > 200*time.Millisecond { // Slow query threshold
		logEvent.Warn().Msgf("Slow query detected (%s)", elapsed)
	} else {
		logEvent.Debug().Msg("Database query executed")
	}
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
	elapsed := time.Since(start)

	// Increment database operation counter for request tracking
	logger.IncrementDBCounter(ctx)
	logger.AddDBElapsed(ctx, elapsed.Nanoseconds())

	// Log statement execution details
	logEvent := s.logger.WithContext(ctx).WithFields(map[string]interface{}{
		"vendor":      s.vendor,
		"operation":   operation,
		"duration_ms": elapsed.Milliseconds(),
		"duration_ns": elapsed.Nanoseconds(),
	})

	if err != nil {
		logEvent.Error().Err(err).Msg("Database prepared statement error")
	} else if elapsed > 200*time.Millisecond { // Slow query threshold
		logEvent.Warn().Msgf("Slow prepared statement detected (%s)", elapsed)
	} else {
		logEvent.Debug().Msg("Database prepared statement executed")
	}
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
	elapsed := time.Since(start)

	// Increment database operation counter for request tracking
	logger.IncrementDBCounter(ctx)
	logger.AddDBElapsed(ctx, elapsed.Nanoseconds())

	// Log transaction query execution details
	logEvent := tx.logger.WithContext(ctx).WithFields(map[string]interface{}{
		"vendor":      tx.vendor,
		"duration_ms": elapsed.Milliseconds(),
		"duration_ns": elapsed.Nanoseconds(),
		"query":       query,
		"context":     "transaction",
	})

	if err != nil {
		logEvent.Error().Err(err).Msg("Database transaction query error")
	} else if elapsed > 200*time.Millisecond { // Slow query threshold
		logEvent.Warn().Msgf("Slow transaction query detected (%s)", elapsed)
	} else {
		logEvent.Debug().Msg("Database transaction query executed")
	}
}
