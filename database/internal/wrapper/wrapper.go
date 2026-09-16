// Package wrapper provides driver-agnostic wrappers around database/sql.Stmt
// and database/sql.Tx that implement the types.Statement and types.Tx
// interfaces. Extracted from byte-identical code in database/postgresql/ and
// database/oracle/ so a future vendor (or a behavior change to the wrapping
// logic) lives in one place.
//
// The wrapped sql.Stmt/sql.Tx values are stored unexported. Callers should
// construct via NewStatement/NewTransaction; both vendor packages re-export
// the wrapper types via type aliases to preserve their public API.
package wrapper

import (
	"context"
	"database/sql"

	"github.com/gaborage/go-bricks/database/types"
)

// Statement wraps sql.Stmt to implement types.Statement.
type Statement struct {
	stmt *sql.Stmt
}

// NewStatement wraps a sql.Stmt as a types.Statement implementation.
func NewStatement(stmt *sql.Stmt) *Statement {
	return &Statement{stmt: stmt}
}

// Query executes a prepared query with arguments.
func (s *Statement) Query(ctx context.Context, args ...any) (*sql.Rows, error) {
	return s.stmt.QueryContext(ctx, args...)
}

// QueryRow executes a prepared query that returns a single row.
func (s *Statement) QueryRow(ctx context.Context, args ...any) types.Row {
	return types.NewRowFromSQL(s.stmt.QueryRowContext(ctx, args...))
}

// Exec executes a prepared statement with arguments.
func (s *Statement) Exec(ctx context.Context, args ...any) (sql.Result, error) {
	return s.stmt.ExecContext(ctx, args...)
}

// Close closes the prepared statement.
func (s *Statement) Close() error {
	return s.stmt.Close()
}

// Transaction wraps sql.Tx to implement types.Tx.
type Transaction struct {
	tx *sql.Tx
	// session is the Session this transaction was begun on, if any.
	// NewTransaction leaves it nil so the pooled Connection.Begin path is
	// unchanged. A Session-begun Tx reports Rows and driver.ErrBadConn back
	// to that Session's dead-backend detection.
	session *Session
}

// NewTransaction wraps a sql.Tx as a types.Tx implementation.
func NewTransaction(tx *sql.Tx) *Transaction {
	return &Transaction{tx: tx}
}

// newSessionTransaction wraps a sql.Tx begun on s so Query/QueryRow/Exec/
// Commit/Rollback feed s's dead-backend detection. NewTransaction stays the
// nil-session constructor.
func newSessionTransaction(tx *sql.Tx, s *Session) *Transaction {
	return &Transaction{tx: tx, session: s}
}

func (t *Transaction) sessionDone() bool {
	return t.session != nil && t.session.connDone()
}

func (t *Transaction) wrapSessionErr(err error) error {
	if t.session == nil {
		return err
	}
	return t.session.markDead(err)
}

// Query executes a query within the transaction.
func (t *Transaction) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if t.sessionDone() {
		return nil, sql.ErrConnDone
	}
	rows, err := t.tx.QueryContext(ctx, query, args...)
	if t.session == nil {
		return rows, err
	}
	if err != nil {
		return nil, t.session.markDead(err)
	}
	t.session.open = append(t.session.open, rows)
	return rows, nil
}

// QueryRow executes a query that returns a single row within the transaction.
func (t *Transaction) QueryRow(ctx context.Context, query string, args ...any) types.Row {
	if t.session == nil {
		return types.NewRowFromSQL(t.tx.QueryRowContext(ctx, query, args...))
	}
	if t.session.connDone() {
		return &sessionRow{s: t.session, err: sql.ErrConnDone}
	}
	return &sessionRow{s: t.session, row: types.NewRowFromSQL(t.tx.QueryRowContext(ctx, query, args...))}
}

// Exec executes a query without returning rows within the transaction.
func (t *Transaction) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if t.sessionDone() {
		return nil, sql.ErrConnDone
	}
	result, err := t.tx.ExecContext(ctx, query, args...)
	return result, t.wrapSessionErr(err)
}

// Prepare creates a prepared statement within the transaction.
func (t *Transaction) Prepare(ctx context.Context, query string) (types.Statement, error) {
	stmt, err := t.tx.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return NewStatement(stmt), nil
}

// Commit commits the transaction.
// Note: database/sql's Tx.Commit doesn't accept context; it's atomic and
// non-cancellable. The context parameter maintains interface consistency for
// databases that support cancellable commit (if a future vendor adds one).
func (t *Transaction) Commit(_ context.Context) error {
	return t.wrapSessionErr(t.tx.Commit())
}

// Rollback rolls back the transaction.
// Note: database/sql's Tx.Rollback doesn't accept context; it's atomic and
// non-cancellable. The context parameter maintains interface consistency.
func (t *Transaction) Rollback(_ context.Context) error {
	return t.wrapSessionErr(t.tx.Rollback())
}
