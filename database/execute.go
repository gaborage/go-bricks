package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Executor is the minimal query/exec surface shared by a database connection
// (Interface) and a transaction (Tx), so the Execute* helpers run identically
// inside or outside a transaction. Executor is a closed 2-method seam: future
// capabilities (batch, prepared statements) arrive as separate interfaces,
// never as additional methods here — adding a method to a shipped exported
// interface is apidiff-INCOMPATIBLE.
type Executor interface {
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Both the connection interface and transactions satisfy Executor.
var (
	_ Executor = Interface(nil)
	_ Executor = Tx(nil)
)

// SQLProvider is the contract of a complete, executable statement: anything
// that can render itself to SQL + args. Every query builder returned by
// QueryBuilder (Select/Insert/Update/Delete) already satisfies it; Raw adapts
// hand-written SQL. A types.Filter/types.JoinFilter WHERE fragment also
// satisfies SQLProvider structurally (it embeds squirrel.Sqlizer, which
// declares the same ToSQL()-shaped ToSql()) but is NOT a complete statement
// and must never be passed to the Execute* helpers directly — buildQuery
// detects and rejects it at StageBuild instead of letting the driver fail on
// a bare WHERE fragment.
type SQLProvider interface {
	ToSQL() (string, []any, error)
}

// Raw adapts hand-written SQL (UNION, vendor tricks) to SQLProvider
// so it reuses the same Execute* helpers as builder output.
//
// SECURITY: Raw is an escape hatch on par with Filter.Raw, and broader — the
// SQL string replaces the whole statement, bypassing the builder's identifier
// validation entirely. Never concatenate user input into the SQL; carry
// values in args. Every call site requires an adjacent
// "// SECURITY: Manual SQL review completed - <what was verified>" comment,
// exactly as f.Raw()/jf.Raw() do.
func Raw(query string, args ...any) SQLProvider { return rawQuery{query: query, args: args} }

type rawQuery struct {
	query string
	args  []any
}

func (q rawQuery) ToSQL() (query string, args []any, err error) { return q.query, q.args, nil }

// ErrNoRows reports a zero-row outcome: a SELECT that matched nothing
// (ExecuteQuerySingle) or an UPDATE/DELETE expected to match exactly one row
// but matched none (ExecuteUpdateOne). It wraps sql.ErrNoRows so
// errors.Is(err, sql.ErrNoRows) and IsNotFound(err) also match.
var ErrNoRows = fmt.Errorf("database: %w", sql.ErrNoRows)

// ExecStage identifies where inside a helper an infrastructure failure occurred.
type ExecStage string

const (
	StageBuild   ExecStage = "build"
	StageExec    ExecStage = "exec"
	StageScan    ExecStage = "scan"
	StageIterate ExecStage = "iterate"
	StageClose   ExecStage = "close"
	// StageRowsAffected covers two distinct failures at the same point in the
	// pipeline: the driver's RowsAffected() call itself erroring, or (in
	// ExecuteUpdateOne only) RowsAffected() succeeding with a count that isn't
	// the exactly-one cardinality the helper promises.
	StageRowsAffected ExecStage = "rows_affected"
)

// ExecError is an infrastructure failure from an Execute* helper: the SQL
// could not be built, executed, scanned, or iterated. Zero-row outcomes are
// NOT ExecErrors — they are returned as op-labeled wraps of ErrNoRows.
type ExecError struct {
	Op    string // caller-supplied operation label
	Stage ExecStage
	Err   error
}

func (e *ExecError) Error() string { return fmt.Sprintf("%s: %s: %v", e.Op, e.Stage, e.Err) }

func (e *ExecError) Unwrap() error { return e.Err }

func buildQuery(q SQLProvider, op string) (query string, args []any, err error) {
	// types.Filter/JoinFilter embed squirrel.Sqlizer and so declare ToSql() as well as
	// ToSQL(); the statement builders declare only ToSQL(). A WHERE fragment therefore
	// satisfies SQLProvider structurally but is not a complete statement — catch it here
	// rather than letting the driver reject the SQL with an opaque StageExec error.
	if _, isFragment := q.(interface {
		ToSql() (string, []any, error)
	}); isFragment {
		return "", nil, &ExecError{
			Op:    op,
			Stage: StageBuild,
			Err:   errors.New("database: a WHERE fragment (Filter/JoinFilter) is not a complete statement; pass a query builder result or database.Raw"),
		}
	}

	query, args, err = q.ToSQL()
	if err != nil {
		return "", nil, &ExecError{Op: op, Stage: StageBuild, Err: err}
	}
	return query, args, nil
}

// ExecuteQuerySingle runs q and scans the first row into dest. It returns an
// op-labeled ErrNoRows wrap when the query matches no rows, and *ExecError for
// infrastructure failures. Extra rows beyond the first are ignored (sql.Row
// parity): after a successful scan, the rows are closed explicitly (mirroring
// sql.Row.Scan) so a driver error that only surfaces at Close — a truncated
// result, a connection fault mid-statement — is not silently swallowed; that
// failure is reported at StageClose. op labels errors only — it does not feed
// metrics or tracing; use database.WithRepositoryMethod(ctx, ...) for
// attribution. Typical mapping in an app:
//
//	err := database.ExecuteQuerySingle(ctx, tx, q, "ownership", &row.ID, &row.Name)
//	switch {
//	case errors.Is(err, database.ErrNoRows):
//		return domain.ErrResourceNotFound // business 404
//	case err != nil:
//		return fmt.Errorf("load ownership: %w", err) // infra 500
//	}
func ExecuteQuerySingle(ctx context.Context, exec Executor, q SQLProvider, op string, dest ...any) error {
	query, args, err := buildQuery(q, op)
	if err != nil {
		return err
	}
	rows, err := exec.Query(ctx, query, args...)
	if err != nil {
		return &ExecError{Op: op, Stage: StageExec, Err: err}
	}
	defer rows.Close()

	if !rows.Next() {
		if iterErr := rows.Err(); iterErr != nil {
			return &ExecError{Op: op, Stage: StageIterate, Err: iterErr}
		}
		return fmt.Errorf("%s: %w", op, ErrNoRows)
	}
	if err := rows.Scan(dest...); err != nil {
		return &ExecError{Op: op, Stage: StageScan, Err: err}
	}
	// sql.Row.Scan surfaces the Close error after a successful scan so the query is
	// known to have completed; Close is idempotent, so the deferred Close above
	// remains the early-return safety net.
	if err := rows.Close(); err != nil {
		return &ExecError{Op: op, Stage: StageClose, Err: err}
	}
	return nil
}

// ExecuteQueryMany runs q and invokes scan once per row. Zero rows is not an
// error (the caller sees an empty result). A scan callback error aborts
// iteration and is wrapped at StageScan. A nil scan is rejected up front at
// StageBuild rather than left to panic once a row arrives: unlike a nil q or
// exec (which fail loudly at the call site, in the caller's own frame), a nil
// scan is data-dependent — it only panics once the query returns a row, so a
// zero-row result would let the mistake pass silently in dev and blow up
// later in prod.
func ExecuteQueryMany(ctx context.Context, exec Executor, q SQLProvider, op string, scan func(rows *sql.Rows) error) error {
	if scan == nil {
		return &ExecError{Op: op, Stage: StageBuild, Err: errors.New("database: ExecuteQueryMany requires a non-nil scan callback")}
	}

	query, args, err := buildQuery(q, op)
	if err != nil {
		return err
	}
	rows, err := exec.Query(ctx, query, args...)
	if err != nil {
		return &ExecError{Op: op, Stage: StageExec, Err: err}
	}
	defer rows.Close()

	for rows.Next() {
		if err := scan(rows); err != nil {
			return &ExecError{Op: op, Stage: StageScan, Err: err}
		}
	}
	if err := rows.Err(); err != nil {
		return &ExecError{Op: op, Stage: StageIterate, Err: err}
	}
	return nil
}

// ExecuteUpdate runs an UPDATE/DELETE and returns the affected-row count. It
// does not interpret zero: a legitimately-zero write (e.g. an UPDATE guarded
// by a WHERE clause that may match nothing, such as an idempotent state
// transition, or a bulk statement with no matching rows) returns (0, nil).
// Use ExecuteUpdateOne when zero affected rows should surface as ErrNoRows.
func ExecuteUpdate(ctx context.Context, exec Executor, q SQLProvider, op string) (int64, error) {
	query, args, err := buildQuery(q, op)
	if err != nil {
		return 0, err
	}
	res, err := exec.Exec(ctx, query, args...)
	if err != nil {
		return 0, &ExecError{Op: op, Stage: StageExec, Err: err}
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, &ExecError{Op: op, Stage: StageRowsAffected, Err: err}
	}
	return n, nil
}

// ExecuteUpdateOne runs an UPDATE/DELETE expected to match exactly one row and
// enforces that cardinality: zero rows affected returns an op-labeled
// ErrNoRows wrap, so "not found" surfaces uniformly with ExecuteQuerySingle;
// more than one row affected returns *ExecError at StageRowsAffected instead
// of silently reporting success, because a broader-than-intended WHERE
// predicate updating several rows is a data-integrity failure, not a "found
// it" outcome; exactly one row affected returns nil. Use ExecuteUpdate when a
// non-singular match is legitimate (bulk or idempotent writes), or when only
// the caller can tell "absent" from "already in the target state".
func ExecuteUpdateOne(ctx context.Context, exec Executor, q SQLProvider, op string) error {
	n, err := ExecuteUpdate(ctx, exec, q, op)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", op, ErrNoRows)
	}
	if n > 1 {
		return &ExecError{
			Op:    op,
			Stage: StageRowsAffected,
			Err:   fmt.Errorf("expected exactly 1 affected row, got %d", n),
		}
	}
	return nil
}

// ExecuteInsert runs an INSERT. It does not inspect RowsAffected: a successful
// INSERT is success, and vendor differences around LastInsertId/RETURNING are
// out of scope here.
func ExecuteInsert(ctx context.Context, exec Executor, q SQLProvider, op string) error {
	query, args, err := buildQuery(q, op)
	if err != nil {
		return err
	}
	if _, err := exec.Exec(ctx, query, args...); err != nil {
		return &ExecError{Op: op, Stage: StageExec, Err: err}
	}
	return nil
}
