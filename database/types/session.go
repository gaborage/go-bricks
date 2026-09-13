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
// Session(ctx context.Context) (Session, error) — e.g. *postgresql.Connection
// or *oracle.Connection. Always Close it to return the physical connection
// to the pool. Once Close has been called, or once the underlying driver
// reports the physical connection dead, every Session method returns an
// error satisfying errors.Is(err, sql.ErrConnDone).
type Session interface {
	Querier
	Transactor

	// Close releases the pinned physical connection back to the pool.
	Close() error
}
