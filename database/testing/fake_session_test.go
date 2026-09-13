package testing

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

const (
	sessionSelectSQL = "SELECT pg_advisory_lock($1)"
	sessionExecSQL   = "SET statement_timeout = 0"
)

func TestTestDBSessionWithoutExpectationErrors(t *testing.T) {
	db := NewTestDB(dbtypes.PostgreSQL)

	sess, err := db.Session(t.Context())
	require.Error(t, err)
	assert.Nil(t, sess)
}

func TestTestDBSessionExpectationOrdering(t *testing.T) {
	db := NewTestDB(dbtypes.PostgreSQL)
	first := db.ExpectSession().ExpectExec("SET first").WillReturnRowsAffected(1)
	second := db.ExpectSession().ExpectExec("SET second").WillReturnRowsAffected(2)

	gotFirst, err := db.Session(t.Context())
	require.NoError(t, err)
	assert.Same(t, first, gotFirst)

	gotSecond, err := db.Session(t.Context())
	require.NoError(t, err)
	assert.Same(t, second, gotSecond)

	_, err = db.Session(t.Context())
	require.Error(t, err, "the queue is drained in order and then strict again")
}

func TestTestSessionQueryExecAndDatabaseType(t *testing.T) {
	db := NewTestDB(dbtypes.PostgreSQL)
	db.ExpectSession().
		ExpectQuery(sessionSelectSQL).WillReturnRows(NewRowSet("locked").AddRow(true)).
		ExpectExec(sessionExecSQL).WillReturnRowsAffected(0)

	sess, err := db.Session(t.Context())
	require.NoError(t, err)

	func() {
		rows, err := sess.Query(t.Context(), sessionSelectSQL, 42)
		require.NoError(t, err)
		defer rows.Close()
	}()

	result, err := sess.Exec(t.Context(), sessionExecSQL)
	require.NoError(t, err)
	affected, err := result.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(0), affected)

	assert.Equal(t, dbtypes.PostgreSQL, sess.DatabaseType())
}

func TestTestSessionQueryRowScansExpectedRow(t *testing.T) {
	db := NewTestDB(dbtypes.PostgreSQL)
	db.ExpectSession().
		ExpectQuery(sessionSelectSQL).WillReturnRows(NewRowSet("locked").AddRow(true))

	sess, err := db.Session(t.Context())
	require.NoError(t, err)

	var locked bool
	require.NoError(t, sess.QueryRow(t.Context(), sessionSelectSQL, 42).Scan(&locked))
	assert.True(t, locked)
}

func TestTestSessionUnexpectedStatementsError(t *testing.T) {
	db := NewTestDB(dbtypes.PostgreSQL)
	db.ExpectSession()

	sess, err := db.Session(t.Context())
	require.NoError(t, err)

	rows, err := sess.Query(t.Context(), sessionSelectSQL)
	if rows != nil {
		defer rows.Close()
	}
	require.Error(t, err)
	require.Error(t, sess.QueryRow(t.Context(), sessionSelectSQL).Err())
	_, err = sess.Exec(t.Context(), sessionExecSQL)
	require.Error(t, err)
}

func TestTestSessionWillReturnErrorTargetsMostRecent(t *testing.T) {
	wantQueryErr := errors.New("lock timeout")
	wantExecErr := errors.New("set rejected")
	db := NewTestDB(dbtypes.PostgreSQL)
	db.ExpectSession().
		ExpectQuery(sessionSelectSQL).WillReturnError(wantQueryErr).
		ExpectExec(sessionExecSQL).WillReturnError(wantExecErr)

	sess, err := db.Session(t.Context())
	require.NoError(t, err)

	rows, err := sess.Query(t.Context(), sessionSelectSQL)
	if rows != nil {
		defer rows.Close()
	}
	require.ErrorIs(t, err, wantQueryErr)
	_, err = sess.Exec(t.Context(), sessionExecSQL)
	require.ErrorIs(t, err, wantExecErr)
}

func TestTestSessionTransactionsPopInOrder(t *testing.T) {
	db := NewTestDB(dbtypes.PostgreSQL)
	sessExp := db.ExpectSession()
	tx := sessExp.ExpectTransaction().ExpectExec("INSERT INTO events").WillReturnRowsAffected(1)

	sess, err := db.Session(t.Context())
	require.NoError(t, err)

	gotTx, err := sess.Begin(t.Context())
	require.NoError(t, err)
	assert.Same(t, tx, gotTx)

	_, err = gotTx.Exec(t.Context(), "INSERT INTO events VALUES (1)")
	require.NoError(t, err)
	require.NoError(t, gotTx.Commit(t.Context()))
	AssertCommitted(t, tx)

	_, err = sess.Begin(t.Context())
	require.Error(t, err, "a session without a queued transaction is strict too")
}

func TestTestSessionBeginTxUsesSameQueue(t *testing.T) {
	db := NewTestDB(dbtypes.PostgreSQL)
	tx := db.ExpectSession().ExpectTransaction()

	sess, err := db.Session(t.Context())
	require.NoError(t, err)

	gotTx, err := sess.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	require.NoError(t, err)
	assert.Same(t, tx, gotTx)
}

func TestTestSessionClosedSessionRejectsEveryCall(t *testing.T) {
	db := NewTestDB(dbtypes.PostgreSQL)
	db.ExpectSession().
		ExpectQuery(sessionSelectSQL).WillReturnRows(NewRowSet("locked").AddRow(true)).
		ExpectExec(sessionExecSQL).WillReturnRowsAffected(0)
	db.ExpectSession().ExpectTransaction()

	sess, err := db.Session(t.Context())
	require.NoError(t, err)
	require.NoError(t, sess.Close())

	rows, err := sess.Query(t.Context(), sessionSelectSQL)
	if rows != nil {
		defer rows.Close()
	}
	require.ErrorIs(t, err, sql.ErrConnDone)
	require.ErrorIs(t, sess.QueryRow(t.Context(), sessionSelectSQL).Err(), sql.ErrConnDone)
	_, err = sess.Exec(t.Context(), sessionExecSQL)
	require.ErrorIs(t, err, sql.ErrConnDone)
	_, err = sess.Begin(t.Context())
	require.ErrorIs(t, err, sql.ErrConnDone)
	_, err = sess.BeginTx(t.Context(), nil)
	require.ErrorIs(t, err, sql.ErrConnDone)
	require.ErrorIs(t, sess.Close(), sql.ErrConnDone, "Close is not idempotent")
}

func TestTestSessionLogsRecordStatements(t *testing.T) {
	db := NewTestDB(dbtypes.PostgreSQL)
	sessExp := db.ExpectSession().
		ExpectQuery(sessionSelectSQL).WillReturnRows(NewRowSet("locked").AddRow(true)).
		ExpectExec(sessionExecSQL).WillReturnRowsAffected(0)

	sess, err := db.Session(t.Context())
	require.NoError(t, err)

	func() {
		rows, err := sess.Query(t.Context(), sessionSelectSQL, 42)
		require.NoError(t, err)
		defer rows.Close()
	}()
	_, err = sess.Exec(t.Context(), sessionExecSQL)
	require.NoError(t, err)

	queryLog := sessExp.QueryLog()
	require.Len(t, queryLog, 1)
	assert.Equal(t, sessionSelectSQL, queryLog[0].SQL)
	assert.Equal(t, []any{42}, queryLog[0].Args)

	execLog := sessExp.ExecLog()
	require.Len(t, execLog, 1)
	assert.Equal(t, sessionExecSQL, execLog[0].SQL)
}

func TestAssertSessionClosedPassesOnClosedSession(t *testing.T) {
	db := NewTestDB(dbtypes.PostgreSQL)
	sessExp := db.ExpectSession()

	sess, err := db.Session(t.Context())
	require.NoError(t, err)
	require.NoError(t, sess.Close())

	AssertSessionClosed(t, sessExp)
}

func TestAssertSessionClosedReportsOpenSession(t *testing.T) {
	db := NewTestDB(dbtypes.PostgreSQL)
	sessExp := db.ExpectSession()

	_, err := db.Session(t.Context())
	require.NoError(t, err)

	recorder := &testing.T{}
	AssertSessionClosed(recorder, sessExp)
	assert.True(t, recorder.Failed(), "an open session must fail the assertion")
}
