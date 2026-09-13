package testing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// TestSession is an in-memory fake pinned session that implements
// dbtypes.Session. It carries its OWN query, exec and transaction
// expectations — nothing it runs matches the parent TestDB's pool
// expectations, the way a real session runs on its own physical connection.
//
// TestSession is created via TestDB.ExpectSession(); TestDB.Session() pops the
// queued sessions in the order they were declared and errors once the queue is
// empty. After Close every call returns sql.ErrConnDone, including a second
// Close, matching the dbtypes.Session contract.
//
// Usage example:
//
//	db := NewTestDB(dbtypes.PostgreSQL)
//	sess := db.ExpectSession().
//	    ExpectExec("SELECT pg_advisory_lock").WillReturnRowsAffected(1)
//
//	// ... execute test code that opens a session ...
//
//	AssertSessionClosed(t, sess)
type TestSession struct {
	parent      *TestDB
	queries     []*QueryExpectation
	execs       []*ExecExpectation
	lastQuery   *QueryExpectation
	lastExec    *ExecExpectation
	lastWasExec bool
	queryLog    []QueryCall
	execLog     []ExecCall
	txs         []*TestTx
	closed      bool
	mu          sync.RWMutex
}

// Compile-time interface check.
var _ dbtypes.Session = (*TestSession)(nil)

// ExpectQuery sets up an expectation for Query or QueryRow calls on the session.
// Returns the TestSession for method chaining.
func (s *TestSession) ExpectQuery(sqlPattern string) *TestSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp := &QueryExpectation{sql: sqlPattern}
	s.queries = append(s.queries, exp)
	s.lastQuery = exp
	s.lastWasExec = false
	return s
}

// ExpectExec sets up an expectation for Exec calls on the session.
// Returns the TestSession for method chaining.
func (s *TestSession) ExpectExec(sqlPattern string) *TestSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp := &ExecExpectation{sql: sqlPattern}
	s.execs = append(s.execs, exp)
	s.lastExec = exp
	s.lastWasExec = true
	return s
}

// WillReturnRows configures the last ExpectQuery to return the specified rows.
// Returns the TestSession for method chaining.
func (s *TestSession) WillReturnRows(rows *RowSet) *TestSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastQuery != nil {
		s.lastQuery.rows = rows
	}
	return s
}

// WillReturnRowsAffected configures the last ExpectExec to return the specified
// rows affected count. Returns the TestSession for method chaining.
func (s *TestSession) WillReturnRowsAffected(n int64) *TestSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastExec != nil {
		s.lastExec.rowsAffected = n
	}
	return s
}

// WillReturnError configures the most-recently-added expectation (Query or Exec)
// to return the specified error, the way TestTx.WillReturnError does.
// Returns the TestSession for method chaining.
func (s *TestSession) WillReturnError(err error) *TestSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastWasExec {
		if s.lastExec != nil {
			s.lastExec.err = err
		}
	} else if s.lastQuery != nil {
		s.lastQuery.err = err
	}
	return s
}

// ExpectTransaction sets up an expectation for Begin()/BeginTx() calls on the
// session. Returns a TestTx that can be configured with query/exec expectations.
func (s *TestSession) ExpectTransaction() *TestTx {
	tx := &TestTx{parent: s.parent}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.txs = append(s.txs, tx)
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]QueryCall{}, s.queryLog...)
}

// ExecLog returns all Exec calls made on this session.
func (s *TestSession) ExecLog() []ExecCall {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ExecCall{}, s.execLog...)
}

// resolveQuery records the call and returns the matching query expectation, or
// the error a closed session, an unmatched statement or an unconfigured
// expectation produces.
func (s *TestSession) resolveQuery(query string, args []any) (*QueryExpectation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, sql.ErrConnDone
	}
	s.queryLog = append(s.queryLog, QueryCall{SQL: query, Args: args})

	for _, exp := range s.queries {
		if !s.parent.matchSQL(exp.sql, query) {
			continue
		}
		if exp.err != nil {
			return nil, exp.err
		}
		if exp.rows == nil {
			return nil, fmt.Errorf("session query expectation for %q has no rows configured", query)
		}
		return exp, nil
	}
	return nil, fmt.Errorf("unexpected query in session: %s (no matching expectation)", query)
}

// Query implements dbtypes.Querier.Query.
//
// IMPORTANT: Callers MUST call defer rows.Close() immediately after Query() to
// prevent resource leaks, exactly as with TestDB.Query.
func (s *TestSession) Query(_ context.Context, query string, args ...any) (*sql.Rows, error) {
	exp, err := s.resolveQuery(query, args)
	if err != nil {
		return nil, err
	}
	return exp.rows.toSQLRows()
}

// QueryRow implements dbtypes.Querier.QueryRow.
func (s *TestSession) QueryRow(_ context.Context, query string, args ...any) dbtypes.Row {
	exp, err := s.resolveQuery(query, args)
	if err != nil {
		return &testRow{err: err}
	}
	if len(exp.rows.rows) == 0 {
		return &testRow{err: sql.ErrNoRows}
	}
	normalized, normErr := exp.rows.normalizeRow(0)
	if normErr != nil {
		return &testRow{err: fmt.Errorf("failed to normalize row: %w", normErr)}
	}
	return &testRow{values: normalized}
}

// Exec implements dbtypes.Querier.Exec.
func (s *TestSession) Exec(_ context.Context, query string, args ...any) (sql.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, sql.ErrConnDone
	}
	s.execLog = append(s.execLog, ExecCall{SQL: query, Args: args})

	for _, exp := range s.execs {
		if !s.parent.matchSQL(exp.sql, query) {
			continue
		}
		if exp.err != nil {
			return nil, exp.err
		}
		return &testResult{rowsAffected: exp.rowsAffected}, nil
	}
	return nil, fmt.Errorf("unexpected exec in session: %s (no matching expectation)", query)
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

	tx := s.txs[0]
	s.txs = s.txs[1:]
	return tx, nil
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
