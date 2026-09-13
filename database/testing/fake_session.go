package testing

import (
	"context"
	"database/sql"
	"errors"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// TestSession is an in-memory fake pinned session created by
// TestDB.ExpectSession(). It carries its OWN query, exec and transaction
// expectations, matching none of the parent TestDB's pool expectations, and
// after Close every call returns sql.ErrConnDone, a second Close included.
type TestSession struct {
	expectationSet
	txs    []*TxExpectation
	closed bool
}

// Compile-time interface check.
var _ dbtypes.Session = (*TestSession)(nil)

// newTestSession builds a session fake whose expectations belong to parent. A
// closed session refuses every statement, so the expectation machinery consults
// closedLocked before it resolves one.
func newTestSession(parent *TestDB) *TestSession {
	sess := &TestSession{expectationSet: expectationSet{parent: parent, scope: "session"}}
	sess.guard = sess.closedLocked
	return sess
}

// closedLocked reports sql.ErrConnDone once the session is closed. It runs with
// the session's mutex already held.
func (s *TestSession) closedLocked() error {
	if s.closed {
		return sql.ErrConnDone
	}
	return nil
}

// ExpectQuery sets up an expectation for Query or QueryRow calls on the session.
// Returns the TestSession for method chaining.
func (s *TestSession) ExpectQuery(sqlPattern string) *TestSession {
	s.addQuery(sqlPattern)
	return s
}

// ExpectExec sets up an expectation for Exec calls on the session.
// Returns the TestSession for method chaining.
func (s *TestSession) ExpectExec(sqlPattern string) *TestSession {
	s.addExec(sqlPattern)
	return s
}

// WillReturnRows configures the last ExpectQuery to return the specified rows.
// Returns the TestSession for method chaining.
func (s *TestSession) WillReturnRows(rows *RowSet) *TestSession {
	s.setRows(rows)
	return s
}

// WillReturnRowsAffected configures the last ExpectExec to return the specified
// rows affected count. Returns the TestSession for method chaining.
func (s *TestSession) WillReturnRowsAffected(n int64) *TestSession {
	s.setRowsAffected(n)
	return s
}

// WillReturnError configures the most-recently-added expectation (Query or Exec)
// to return the specified error, the way TestTx.WillReturnError does.
// Returns the TestSession for method chaining.
func (s *TestSession) WillReturnError(err error) *TestSession {
	s.setError(err)
	return s
}

// ExpectTransaction sets up an expectation for Begin()/BeginTx() calls on the
// session. Returns a TestTx that can be configured with query/exec expectations.
func (s *TestSession) ExpectTransaction() *TestTx {
	tx := newTestTx(s.parent)
	txExp := &TxExpectation{parent: s.parent, tx: tx}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.txs = append(s.txs, txExp)
	return tx
}

// IsClosed reports whether Close was called on this session.
func (s *TestSession) IsClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

// QueryLog returns all Query/QueryRow calls made on this session.
func (s *TestSession) QueryLog() []QueryCall {
	return s.queryCalls()
}

// ExecLog returns all Exec calls made on this session.
func (s *TestSession) ExecLog() []ExecCall {
	return s.execCalls()
}

// Query implements dbtypes.Querier.Query.
//
// IMPORTANT: Callers MUST call defer rows.Close() immediately after Query() to
// prevent resource leaks, exactly as with TestDB.Query.
func (s *TestSession) Query(_ context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.runQuery(query, args)
}

// QueryRow implements dbtypes.Querier.QueryRow.
func (s *TestSession) QueryRow(_ context.Context, query string, args ...any) dbtypes.Row {
	return s.runQueryRow(query, args)
}

// Exec implements dbtypes.Querier.Exec.
func (s *TestSession) Exec(_ context.Context, query string, args ...any) (sql.Result, error) {
	return s.runExec(query, args)
}

// DatabaseType implements dbtypes.Querier.DatabaseType.
func (s *TestSession) DatabaseType() string {
	return s.parent.DatabaseType()
}

// Begin implements dbtypes.Transactor.Begin, popping the session's own queued
// transactions in declaration order.
func (s *TestSession) Begin(_ context.Context) (dbtypes.Tx, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, sql.ErrConnDone
	}
	if len(s.txs) == 0 {
		return nil, errors.New("unexpected Begin() call on session (use ExpectTransaction)")
	}

	txExp := s.txs[0]
	s.txs = s.txs[1:]
	s.parent.registerStartedTransaction(txExp)

	if txExp.shouldErr != nil {
		return nil, txExp.shouldErr
	}
	return txExp.tx, nil
}

// BeginTx implements dbtypes.Transactor.BeginTx.
func (s *TestSession) BeginTx(ctx context.Context, _ *sql.TxOptions) (dbtypes.Tx, error) {
	// For test purposes, delegate to Begin (ignore opts)
	return s.Begin(ctx)
}

// Close implements dbtypes.Session.Close. It is not idempotent: a second Close
// returns sql.ErrConnDone, the way a real pinned connection does.
func (s *TestSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return sql.ErrConnDone
	}
	s.closed = true
	return nil
}
