package tracking

import (
	"context"
	"database/sql"
	"time"

	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

// BasicStatement wraps sql.Stmt to implement types.Statement interface.
// It provides a simple adapter between sql.Stmt and the types.Statement interface
// without any performance tracking (tracking is handled at higher levels).
type BasicStatement struct {
	*sql.Stmt
}

// Query executes the prepared statement as a query
func (s *BasicStatement) Query(ctx context.Context, args ...any) (*sql.Rows, error) {
	return s.QueryContext(ctx, args...)
}

// QueryRow executes the prepared statement as a single row query
func (s *BasicStatement) QueryRow(ctx context.Context, args ...any) *sql.Row {
	return s.QueryRowContext(ctx, args...)
}

// Exec executes the prepared statement without returning rows
func (s *BasicStatement) Exec(ctx context.Context, args ...any) (sql.Result, error) {
	return s.ExecContext(ctx, args...)
}

// Close closes the prepared statement
func (s *BasicStatement) Close() error {
	return s.Stmt.Close()
}

// Statement wraps types.Statement to provide performance tracking for prepared statements.
// It intercepts all statement operations and logs performance metrics,
// slow queries, and errors using structured logging.
type Statement struct {
	stmt     types.Statement
	logger   logger.Logger
	vendor   string
	query    string
	settings Settings
}

// NewStatement creates a Statement wrapper that tracks performance for prepared statements.
// It wraps the provided statement and records execution metrics for all operations.
func NewStatement(stmt types.Statement, log logger.Logger, vendor, query string, settings Settings) types.Statement {
	return &Statement{
		stmt:     stmt,
		logger:   log,
		vendor:   vendor,
		query:    query,
		settings: settings,
	}
}

// Query executes the prepared statement as a query with tracking
func (s *Statement) Query(ctx context.Context, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := s.stmt.Query(ctx, args...)

	s.trackStmt(ctx, "STMT_QUERY", args, start, err)
	return rows, err
}

// QueryRow executes the prepared statement as a single row query with tracking
func (s *Statement) QueryRow(ctx context.Context, args ...any) *sql.Row {
	start := time.Now()
	row := s.stmt.QueryRow(ctx, args...)

	s.trackStmt(ctx, "STMT_QUERY_ROW", args, start, nil)
	return row
}

// Exec executes the prepared statement without returning rows with tracking
func (s *Statement) Exec(ctx context.Context, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := s.stmt.Exec(ctx, args...)

	s.trackStmt(ctx, "STMT_EXEC", args, start, err)
	return result, err
}

// Close closes the prepared statement
func (s *Statement) Close() error {
	return s.stmt.Close()
}

// trackStmt tracks prepared statement performance
func (s *Statement) trackStmt(ctx context.Context, operation string, args []any, start time.Time, err error) {
	op := operation
	if s.query != "" {
		op = operation + ": " + s.query
	}
	tc := &Context{
		Logger:   s.logger,
		Vendor:   s.vendor,
		Settings: s.settings,
	}
	TrackDBOperation(ctx, tc, op, args, start, err)
}
