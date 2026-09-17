package wrapper

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/gaborage/go-bricks/database/types"
)

// Session wraps a *sql.Conn pinned to a single physical database connection, so
// every statement runs on the same backend. See types.Session for the error and
// concurrency contract.
type Session struct {
	conn   *sql.Conn
	vendor string
	// open holds the Rows handed out by Query that were not yet seen closed.
	open []*sql.Rows
	// dead records a driver.ErrBadConn that database/sql did not act on: a
	// Rows closes with its Close error, never its iteration error, so the
	// pinned *sql.Conn stays usable after a mid-stream ErrBadConn.
	dead bool
}

// Compile-time interface check.
var _ types.Session = (*Session)(nil)

// OpenSession acquires a dedicated physical connection from the pool via
// (*sql.DB).Conn, pinned for the caller until Close.
func (c *Connection) OpenSession(ctx context.Context, vendor string) (types.Session, error) {
	conn, err := c.DB.Conn(ctx)
	if err != nil {
		// Interface return type, so this is a genuinely nil types.Session
		// rather than one wrapping a nil *Session.
		return nil, err
	}
	return &Session{conn: conn, vendor: vendor}, nil
}

// wrapConnErr translates a driver.ErrBadConn failure into an error that also
// satisfies errors.Is(err, sql.ErrConnDone). database/sql discards the
// physical connection as soon as a driver reports ErrBadConn, so every LATER
// call on this *sql.Conn already returns sql.ErrConnDone natively — this only
// upgrades the FIRST failure, whose error otherwise carries just the raw
// driver error.
func wrapConnErr(err error) error {
	if err != nil && errors.Is(err, driver.ErrBadConn) {
		return fmt.Errorf("%w: %w", sql.ErrConnDone, err)
	}
	return err
}

// connDone reports whether a failure database/sql did not act on already showed
// the connection dead. Rows.Columns is the non-mutating probe: it returns nil
// while the Rows is open, and its iteration error once closed.
func (s *Session) connDone() bool {
	kept := s.open[:0]
	for _, rows := range s.open {
		_, err := rows.Columns()
		switch {
		case err == nil:
			kept = append(kept, rows)
		case errors.Is(err, driver.ErrBadConn):
			s.dead = true
		}
	}
	clear(s.open[len(kept):])
	s.open = kept
	return s.dead
}

func (s *Session) markDead(err error) error {
	if errors.Is(err, driver.ErrBadConn) {
		s.dead = true
	}
	return wrapConnErr(err)
}

// Query executes a query on the pinned connection.
func (s *Session) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if s.connDone() {
		return nil, sql.ErrConnDone
	}
	rows, err := s.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapConnErr(err)
	}
	s.open = append(s.open, rows)
	return rows, nil
}

// QueryRow executes a query expected to return at most one row on the pinned connection.
func (s *Session) QueryRow(ctx context.Context, query string, args ...any) types.Row {
	if s.connDone() {
		return &sessionRow{s: s, err: sql.ErrConnDone}
	}
	return &sessionRow{s: s, row: types.NewRowFromSQL(s.conn.QueryRowContext(ctx, query, args...))}
}

// Exec executes a statement on the pinned connection.
func (s *Session) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if s.connDone() {
		return nil, sql.ErrConnDone
	}
	result, err := s.conn.ExecContext(ctx, query, args...)
	return result, wrapConnErr(err)
}

// Begin starts a transaction on the pinned connection with default options.
func (s *Session) Begin(ctx context.Context) (types.Tx, error) {
	return s.BeginTx(ctx, nil)
}

// BeginTx starts a transaction on the pinned connection with explicit options.
func (s *Session) BeginTx(ctx context.Context, opts *sql.TxOptions) (types.Tx, error) {
	if s.connDone() {
		return nil, sql.ErrConnDone
	}
	tx, err := s.conn.BeginTx(ctx, opts) // NOSONAR S8168: transaction factory - the Tx is returned to the caller, so rollback is the caller's
	if err != nil {
		return nil, wrapConnErr(err)
	}
	return newSessionTransaction(tx, s), nil
}

// Close releases the pinned physical connection back to the pool. A connection
// already shown dead is discarded instead: (*sql.Conn).Close releases with a nil
// error, which would return it to the pool, while Raw releasing with
// driver.ErrBadConn makes database/sql drop it.
func (s *Session) Close() error {
	if !s.connDone() {
		return s.conn.Close()
	}
	s.open = nil
	err := s.conn.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) {
		return nil
	}
	return err
}

// DatabaseType returns the vendor identifier this Session was opened with.
func (s *Session) DatabaseType() string {
	return s.vendor
}

// sessionRow adds the sql.ErrConnDone translation to a types.Row: a
// driver.ErrBadConn failure on a pinned connection is deferred until Scan/Err
// (QueryRowContext never returns an error directly), so it needs the same
// wrapConnErr treatment as Query/Exec/BeginTx. Its ErrBadConn may come from the
// row read rather than the query, so it also marks the Session dead.
type sessionRow struct {
	s   *Session
	row types.Row
	err error
}

func (r *sessionRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return r.s.markDead(r.row.Scan(dest...))
}

func (r *sessionRow) Err() error {
	if r.err != nil {
		return r.err
	}
	return r.s.markDead(r.row.Err())
}
