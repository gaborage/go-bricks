package tracking

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/database/types"
)

// Session wraps a types.Session with the same per-operation tracking Connection
// applies to pool statements; Query/QueryRow/Exec come from the embedded
// stmtTracker, shared with Transaction.
type Session struct {
	stmtTracker
	sess types.Session
}

// NewSession wraps sess with the same per-operation tracking Connection and
// Transaction apply. tc is the caller's tracking Context — pass the ACQUIRING
// connection's context (see Connection.trackingContext) so session statements
// carry the same server.address / server.port / db.namespace attributes as the
// acquisition span.
func NewSession(sess types.Session, tc *Context) types.Session {
	return &Session{
		stmtTracker: stmtTracker{q: sess, tc: tc},
		sess:        sess,
	}
}

// Compile-time check
var _ types.Session = (*Session)(nil)

// Begin starts a transaction on the pinned session connection with performance tracking.
func (s *Session) Begin(ctx context.Context) (types.Tx, error) {
	return trackBegin(ctx, s.tc, "BEGIN", s.sess.Begin)
}

// BeginTx starts a transaction with options on the pinned session connection with performance tracking.
func (s *Session) BeginTx(ctx context.Context, opts *sql.TxOptions) (types.Tx, error) {
	return trackBegin(ctx, s.tc, "BEGIN_TX", func(ctx context.Context) (types.Tx, error) {
		return s.sess.BeginTx(ctx, opts)
	})
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
// declare Session.
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

	trackingCtx := tc.trackingContext()

	start := time.Now()
	sess, err := opener.Session(ctx)
	TrackDBOperation(ctx, trackingCtx, "SESSION", nil, start, 0, err)
	if err != nil {
		return nil, err
	}
	return NewSession(sess, trackingCtx), nil
}
