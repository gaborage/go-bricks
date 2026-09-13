package tracking

import (
	"context"
	"database/sql"
	"time"

	"github.com/gaborage/go-bricks/database/internal/rowtracker"
	"github.com/gaborage/go-bricks/database/types"
)

// stmtExecutor is the Query/QueryRow/Exec subset of types.Querier — the part
// worth instrumenting. Both types.Tx (which has no DatabaseType) and
// types.Session satisfy it.
type stmtExecutor interface {
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) types.Row
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// stmtTracker instruments the three statement-executing methods of a
// stmtExecutor with TrackDBOperation. Transaction and Session embed it: their
// Query/QueryRow/Exec bodies were token-for-token identical, differing only in
// the delegate and the tracking Context.
type stmtTracker struct {
	q  stmtExecutor
	tc *Context
}

// Query executes a query on the wrapped querier with performance tracking.
func (s *stmtTracker) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := s.q.Query(ctx, query, args...)

	TrackDBOperation(ctx, s.tc, query, args, start, 0, err) // Read operations don't have rows affected

	return rows, err
}

// QueryRow executes a single row query on the wrapped querier with performance
// tracking. The operation is recorded once the row is scanned, not before.
func (s *stmtTracker) QueryRow(ctx context.Context, query string, args ...any) types.Row {
	start := time.Now()
	row := s.q.QueryRow(ctx, query, args...)

	return rowtracker.Wrap(row, func(err error) {
		TrackDBOperation(ctx, s.tc, query, args, start, 0, err) // Read operations don't have rows affected
	})
}

// Exec executes a statement on the wrapped querier with performance tracking.
func (s *stmtTracker) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := s.q.Exec(ctx, query, args...)

	TrackDBOperation(ctx, s.tc, query, args, start, extractRowsAffected(result, err), err)

	return result, err
}

// trackBegin times a transaction start, records it under op, and wraps the
// resulting Tx with tracking. Shared by Connection and Session, whose
// Begin/BeginTx bodies were otherwise identical.
func trackBegin(ctx context.Context, tc *Context, op string, begin func(context.Context) (types.Tx, error)) (types.Tx, error) {
	start := time.Now()
	tx, err := begin(ctx)

	TrackDBOperation(ctx, tc, op, nil, start, 0, err) // Beginning a transaction doesn't affect rows

	if err != nil {
		return nil, err
	}
	return NewTransaction(tx, tc.Logger, tc.Vendor, tc.Settings), nil
}
