//go:build integration

package postgresql

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionAdvisoryLockVisibleInPgLocks confirms a pg_advisory_lock taken on
// a dedicated Session is visible in pg_locks for that session's own
// pg_backend_pid() — proof the lock lives on one pinned physical connection,
// not wherever the pool happens to route the next statement.
func TestSessionAdvisoryLockVisibleInPgLocks(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	sess, err := conn.Session(ctx)
	require.NoError(t, err, "Session should acquire a pinned connection")
	defer sess.Close()

	var pid int
	require.NoError(t, sess.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid))

	_, err = sess.Exec(ctx, "SELECT pg_advisory_lock(424242)")
	require.NoError(t, err, "advisory lock should be acquired on the session's backend")
	defer func() {
		_, _ = sess.Exec(ctx, "SELECT pg_advisory_unlock(424242)")
	}()

	var held bool
	row := conn.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND pid = $1)", pid)
	require.NoError(t, row.Scan(&held))
	assert.True(t, held, "advisory lock should be visible in pg_locks for the session's backend pid")
}

// TestSessionBeginTxPidMatchesSessionPid confirms a transaction begun on a
// Session runs on the SAME physical backend as the session itself — the
// property that makes advisory locks, SET, and temp tables reliable across a
// Begin/Commit boundary.
func TestSessionBeginTxPidMatchesSessionPid(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	sess, err := conn.Session(ctx)
	require.NoError(t, err)
	defer sess.Close()

	var sessionPid int
	require.NoError(t, sess.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&sessionPid))

	tx, err := sess.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	var txPid int
	require.NoError(t, tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&txPid))

	assert.Equal(t, sessionPid, txPid,
		"a transaction begun on the session must run on the same physical backend as the session")
}

// TestSessionCloseReleasesPoolConnection confirms Close returns the pinned
// physical connection to the pool (Stats().InUse drops) and that every
// Session call after Close fails with sql.ErrConnDone.
func TestSessionCloseReleasesPoolConnection(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	baseline := conn.DB.Stats().InUse

	sess, err := conn.Session(ctx)
	require.NoError(t, err)
	assert.Equal(t, baseline+1, conn.DB.Stats().InUse,
		"an open session should hold exactly one additional pooled connection")

	require.NoError(t, sess.Close())
	assert.Equal(t, baseline, conn.DB.Stats().InUse,
		"closing the session should return its connection to the pool")

	_, err = sess.Exec(ctx, "SELECT 1")
	require.ErrorIs(t, err, sql.ErrConnDone, "a session call after Close must fail with sql.ErrConnDone")
}

// TestSessionTerminatedBackendErrorsWhilePoolSurvives confirms that killing a
// session's backend from OUTSIDE (pg_terminate_backend, e.g. an operator or
// PostgreSQL itself under load) fails the session loudly — instead of silently
// handing the next statement to a different, unaware physical connection —
// while leaving the rest of the pool healthy.
//
// It pins the NARROWED contract documented on types.Session, one call at a
// time: the call that OBSERVES the death may return the driver's own error
// (pgx reports a raw pgconn FATAL, which database/sql does not classify as a
// dead connection), and only the call AFTER it is guaranteed to satisfy
// errors.Is(err, sql.ErrConnDone). No polling wraps either assertion: the
// two-argument pg_terminate_backend waits for the backend to actually exit
// before returning, so the ordering is established by PostgreSQL itself.
func TestSessionTerminatedBackendErrorsWhilePoolSurvives(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	sess, err := conn.Session(ctx)
	require.NoError(t, err)
	defer sess.Close()

	var pid int
	require.NoError(t, sess.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid))

	// The second argument is a timeout in ms: pg_terminate_backend returns true
	// only once the backend has actually gone away (PostgreSQL 14+), so the
	// session is provably dead before the first assertion below runs.
	var terminated bool
	require.NoError(t, conn.QueryRow(ctx, "SELECT pg_terminate_backend($1, 5000)", pid).Scan(&terminated))
	require.True(t, terminated, "pg_terminate_backend should confirm the backend exited")

	// FIRST post-termination call: it must error, but the contract deliberately
	// does not promise sql.ErrConnDone here.
	_, firstErr := sess.Exec(ctx, "SELECT 1")
	require.Error(t, firstErr, "the first call after the backend dies must fail, not silently succeed")
	// Diagnostics only — deliberately NOT asserted, so a driver that starts
	// classifying this error does not fail the test. Observed on pgx v5 /
	// PostgreSQL 18: "FATAL: terminating connection due to administrator
	// command (SQLSTATE 57P01)", which is not sql.ErrConnDone.
	t.Logf("first post-termination error: %v", firstErr)

	// SECOND call: this is the one the contract pins.
	_, secondErr := sess.Exec(ctx, "SELECT 1")
	require.ErrorIs(t, secondErr, sql.ErrConnDone,
		"every call after the death has been observed must satisfy errors.Is(err, sql.ErrConnDone)")

	require.NoError(t, conn.Health(ctx), "the pool itself must remain healthy after a session backend is killed")
}
