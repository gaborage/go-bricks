package tracking

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/database/internal/rowtracker"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

// Session wraps a types.Session to provide the same performance tracking
// (span/metric/log via TrackDBOperation) that Connection applies to pool
// statements and Transaction applies to transaction statements. Session has
// no Prepare/Health/Stats/MigrationTable methods to track, mirroring the
// smaller types.Session surface.
type Session struct {
	sess     types.Session
	logger   logger.Logger
	vendor   string
	settings Settings
	tc       *Context // cached context for tracking
}

// NewSession wraps sess with the same per-operation tracking Connection and
// Transaction apply, and initializes the internal Context used for tracking.
func NewSession(sess types.Session, log logger.Logger, vendor string, settings Settings) types.Session {
	s := &Session{sess: sess, logger: log, vendor: vendor, settings: settings}
	s.tc = &Context{Logger: log, Vendor: vendor, Settings: settings}
	return s
}

// Compile-time check
var _ types.Session = (*Session)(nil)

// Query executes a query on the pinned session connection with performance tracking.
func (s *Session) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := s.sess.Query(ctx, query, args...)

	TrackDBOperation(ctx, s.tc, query, args, start, 0, err) // Read operations don't have rows affected
	return rows, err
}

// QueryRow executes a single row query on the pinned session connection with performance tracking.
func (s *Session) QueryRow(ctx context.Context, query string, args ...any) types.Row {
	start := time.Now()
	row := s.sess.QueryRow(ctx, query, args...)

	return rowtracker.Wrap(row, func(err error) {
		TrackDBOperation(ctx, s.tc, query, args, start, 0, err) // Read operations don't have rows affected
	})
}

// Exec executes a statement on the pinned session connection with performance tracking.
func (s *Session) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := s.sess.Exec(ctx, query, args...)

	TrackDBOperation(ctx, s.tc, query, args, start, extractRowsAffected(result, err), err)
	return result, err
}

// Begin starts a transaction on the pinned session connection with performance tracking.
func (s *Session) Begin(ctx context.Context) (types.Tx, error) {
	start := time.Now()
	tx, err := s.sess.Begin(ctx)
	TrackDBOperation(ctx, s.tc, "BEGIN", nil, start, 0, err) // BEGIN doesn't affect rows
	if err != nil {
		return nil, err
	}
	return NewTransaction(tx, s.logger, s.vendor, s.settings), nil
}

// BeginTx starts a transaction with options on the pinned session connection with performance tracking.
func (s *Session) BeginTx(ctx context.Context, opts *sql.TxOptions) (types.Tx, error) {
	start := time.Now()
	tx, err := s.sess.BeginTx(ctx, opts)
	TrackDBOperation(ctx, s.tc, "BEGIN_TX", nil, start, 0, err) // BEGIN_TX doesn't affect rows
	if err != nil {
		return nil, err
	}
	return NewTransaction(tx, s.logger, s.vendor, s.settings), nil
}

// Close releases the pinned session connection back to the pool (no tracking needed).
func (s *Session) Close() error {
	return s.sess.Close()
}

// DatabaseType returns the database type (no tracking needed).
func (s *Session) DatabaseType() string {
	return s.sess.DatabaseType()
}

// sessionOpener is implemented by vendor connections (postgresql.Connection,
// oracle.Connection) that support opening a dedicated Session. Declared as an
// unexported capability check here because types.Interface itself does not
// declare Session — link 1 of #1009 keeps the door additive-only; wiring
// Session into types.Interface is link 2.
type sessionOpener interface {
	Session(ctx context.Context) (types.Session, error)
}

// Session acquires a dedicated, pinned session from the underlying connection
// and wraps it with the same tracking Begin/BeginTx apply. The acquisition
// itself is tracked as operation "SESSION", the way Begin/BeginTx track
// "BEGIN"/"BEGIN_TX".
func (tc *Connection) Session(ctx context.Context) (types.Session, error) {
	opener, ok := tc.conn.(sessionOpener)
	if !ok {
		return nil, fmt.Errorf("database: %T does not support dedicated sessions", tc.conn)
	}

	start := time.Now()
	sess, err := opener.Session(ctx)
	tc.trackOperation(ctx, "SESSION", nil, start, 0, err)
	if err != nil {
		return nil, err
	}
	return NewSession(sess, tc.logger, tc.vendor, tc.settings), nil
}
