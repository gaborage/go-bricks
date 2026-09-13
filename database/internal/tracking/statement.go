package tracking

import (
	"context"
	"database/sql"
	"time"

	"github.com/gaborage/go-bricks/database/internal/rowtracker"
	"github.com/gaborage/go-bricks/database/types"
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
func (s *BasicStatement) QueryRow(ctx context.Context, args ...any) types.Row {
	return types.NewRowFromSQL(s.QueryRowContext(ctx, args...))
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
// The tracking Context (tc) is the single source of logger, vendor, settings and
// server metadata — trackStmt reads them through it rather than from copies.
type Statement struct {
	stmt  types.Statement
	tc    *Context
	query string
}

// NewStatement wraps the provided statement with a tracking implementation that records execution
// metrics for Query, QueryRow, and Exec.
//
// tc is the preparing caller's tracking Context — pass the PREPARING connection's,
// session's or transaction's context (see Connection.trackingContext) so statement
// executions carry the same server.address / server.port / db.namespace attributes
// as the PREPARE span. query is the optional statement text used to label operations.
func NewStatement(stmt types.Statement, tc *Context, query string) types.Statement {
	return &Statement{
		stmt:  stmt,
		tc:    tc,
		query: query,
	}
}

// Query executes the prepared statement as a query with tracking
func (s *Statement) Query(ctx context.Context, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := s.stmt.Query(ctx, args...)

	s.trackStmt(ctx, "STMT_QUERY", args, start, 0, err) // Read operations don't have rows affected
	return rows, err
}

// QueryRow executes the prepared statement as a single row query with tracking

func (s *Statement) QueryRow(ctx context.Context, args ...any) types.Row {
	start := time.Now()
	row := s.stmt.QueryRow(ctx, args...)

	return rowtracker.Wrap(row, func(err error) {
		s.trackStmt(ctx, "STMT_QUERY_ROW", args, start, 0, err) // Read operations don't have rows affected
	})
}

// Exec executes the prepared statement without returning rows with tracking
func (s *Statement) Exec(ctx context.Context, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := s.stmt.Exec(ctx, args...)

	s.trackStmt(ctx, "STMT_EXEC", args, start, extractRowsAffected(result, err), err)
	return result, err
}

// Close closes the prepared statement
func (s *Statement) Close() error {
	return s.stmt.Close()
}

// trackStmt tracks prepared statement performance
func (s *Statement) trackStmt(ctx context.Context, operation string, args []any, start time.Time, rowsAffected int64, err error) {
	op := operation
	if s.query != "" {
		op = operation + ": " + s.query
	}
	TrackDBOperation(ctx, s.tc, op, args, start, rowsAffected, err)
}
