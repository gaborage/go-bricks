// Package types contains the core database interface definitions for go-bricks.
//
//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

// Session represents a handle pinned to a single physical database
// connection, for session-scoped state — PostgreSQL advisory locks, SET,
// temp tables — that a shared pool connection can silently lose when the
// pool hands the next statement to a different physical backend. Session
// composes Querier and Transactor with Close; unlike Interface it has no
// Prepare, Health, Stats, or migration-table methods, none of which are
// meaningful on a single pinned connection.
//
// Obtain a Session from a database.Interface implementation exposing
// Session(ctx context.Context) (Session, error); Interface itself does not
// declare Session, so reaching it means type-asserting to the anonymous
// interface { Session(ctx context.Context) (Session, error) } — satisfied by
// both the vendor connections (postgresql.Connection, oracle.Connection) and
// the tracking wrapper the framework actually hands back from
// database.NewConnection / deps.DB(ctx). The assertion succeeds whenever the
// handle exposes the method — including the tracking wrapper, which always
// does — so an unsupported underlying connection reports that as an error
// from the call, not from the assertion. Always Close it to return the
// physical connection to the pool.
//
// A Session holds no tenant lease of its own, so it must not outlive the
// request or job scope in which it was acquired — the tenant's underlying
// pool may be closed out from under it once that lease is released (ADR-032).
//
// Error semantics once the physical connection is gone: the call that OBSERVES
// the death may return the driver's own error rather than a translated one — a
// PostgreSQL backend killed after the statement went out reports a raw FATAL
// error (SQLSTATE 57P01) that database/sql does not classify as a dead
// connection. Every SUBSEQUENT call returns an error satisfying
// errors.Is(err, sql.ErrConnDone). After Close, every call does so
// immediately. One exception: a failure that only surfaces while iterating the
// *sql.Rows returned by Query reaches the caller RAW through rows.Next and
// rows.Err, and is never translated.
//
// A Session must not be used concurrently: database/sql does not serialize
// statements on a single pinned connection, so concurrent calls on one Session
// race with each other.
//
// Any *sql.Rows obtained from a Session that is still open makes Session.Close
// block until that Rows is closed, since database/sql holds the pinned
// connection's closing mutex for as long as the Rows stays open.
//
// Close is not idempotent: a second Close returns sql.ErrConnDone.
type Session interface {
	Querier
	Transactor

	// Close releases the pinned physical connection back to the pool.
	Close() error
}
