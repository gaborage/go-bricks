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

// Query executes a query on the pinned connection.
func (s *Session) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := s.conn.QueryContext(ctx, query, args...)
	return rows, wrapConnErr(err)
}

// QueryRow executes a query expected to return at most one row on the pinned connection.
func (s *Session) QueryRow(ctx context.Context, query string, args ...any) types.Row {
	return &sessionRow{row: types.NewRowFromSQL(s.conn.QueryRowContext(ctx, query, args...))}
}

// Exec executes a statement on the pinned connection.
func (s *Session) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := s.conn.ExecContext(ctx, query, args...)
	return result, wrapConnErr(err)
}

// Begin starts a transaction on the pinned connection with default options.
func (s *Session) Begin(ctx context.Context) (types.Tx, error) {
	return s.BeginTx(ctx, nil)
}

// BeginTx starts a transaction on the pinned connection with explicit options.
func (s *Session) BeginTx(ctx context.Context, opts *sql.TxOptions) (types.Tx, error) {
	tx, err := s.conn.BeginTx(ctx, opts)
	if err != nil {
		return nil, wrapConnErr(err)
	}
	return NewTransaction(tx), nil
}

// Close releases the pinned physical connection back to the pool.
func (s *Session) Close() error {
	return s.conn.Close()
}

// DatabaseType returns the vendor identifier this Session was opened with.
func (s *Session) DatabaseType() string {
	return s.vendor
}

// sessionRow adds the sql.ErrConnDone translation to a types.Row: a
// driver.ErrBadConn failure on a pinned connection is deferred until Scan/Err
// (QueryRowContext never returns an error directly), so it needs the same
// wrapConnErr treatment as Query/Exec/BeginTx.
type sessionRow struct {
	row types.Row
}

func (r *sessionRow) Scan(dest ...any) error {
	return wrapConnErr(r.row.Scan(dest...))
}

func (r *sessionRow) Err() error {
	return wrapConnErr(r.row.Err())
}
