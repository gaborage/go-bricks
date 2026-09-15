//go:build integration

package oracle

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Oracle twins of the PostgreSQL session tests, written with only what the
// test schema's CONNECT/RESOURCE grants allow: no V$ view, no DBMS_LOCK, no
// ALTER SYSTEM.
//
// There are three subtests here against four PostgreSQL tests: there is no
// Oracle twin of the killed-backend property, because ALTER SYSTEM KILL
// SESSION needs privileges the test schema lacks and exposing the container's
// SYSTEM handle is out of scope. That contract is pinned vendor-neutrally by
// the sqlmock ErrBadConn -> ErrConnDone test
// (database/internal/wrapper/session_test.go) and on PostgreSQL by
// TestSessionTerminatedBackendErrorsWhilePoolSurvives.
//
// All three share ONE Oracle schema, because a per-test schema costs a full
// CREATE USER / pool / DROP USER CASCADE cycle and that is the dominant cost
// of this file. They are subtests of one parent so the shared schema's
// cleanup outlives them all; none calls t.Parallel(), each opens its own
// Session, and each drops the objects it creates, so no subtest can observe
// another's state.

// sessionIdentityQuery renders the Oracle session identity available to an
// unprivileged schema: no V$ view is readable by the test user, so the pair
// SID + SESSIONID (the audit session id) from SYS_CONTEXT is the identity.
// Both live in USERENV and both are per-session, so the concatenation
// distinguishes two concurrently live sessions even if one half were reused.
const sessionIdentityQuery = `SELECT SYS_CONTEXT('USERENV','SID') || '-' || SYS_CONTEXT('USERENV','SESSIONID') FROM DUAL`

// TestSessionOracle covers the dedicated-session door against a real Oracle
// instance: identity pinning, transactions on the pinned session, and
// session-scoped visibility.
func TestSessionOracle(t *testing.T) {
	conn, ctx := setupTestSchema(t)

	t.Run("identity_is_stable_and_distinct_from_pool", func(t *testing.T) {
		// Two Oracle sessions alive at the same instant cannot share a SID, and
		// while a Session is open its connection is checked OUT of the pool, so
		// the pool must answer on a different one. The pool-accounting and
		// post-Close assertions are the vendor-neutral half of the contract
		// (link 1 pins it against sqlmock, PostgreSQL pins it in its own twin);
		// this is its only live-Oracle observation.
		baseline := conn.DB.Stats().InUse

		sess, err := conn.Session(ctx)
		require.NoError(t, err, "Session should acquire a pinned connection")

		// Close is not idempotent, and a require FailNow below would otherwise
		// leave the pinned connection checked out for the remaining subtests and
		// the schema teardown. A bool guard (rather than tolerating
		// sql.ErrConnDone) keeps the asserted close the only one on the happy
		// path, so a genuine double-close would still be a test failure.
		closed := false
		t.Cleanup(func() {
			if !closed {
				_ = sess.Close()
			}
		})

		assert.Equal(t, baseline+1, conn.DB.Stats().InUse,
			"an open session should hold exactly one additional pooled connection")

		var first, second string
		require.NoError(t, sess.QueryRow(ctx, sessionIdentityQuery).Scan(&first))
		require.NoError(t, sess.QueryRow(ctx, sessionIdentityQuery).Scan(&second))
		assert.Equal(t, first, second,
			"two statements on one session must run on the same Oracle session")

		var poolIdentity string
		require.NoError(t, conn.QueryRow(ctx, sessionIdentityQuery).Scan(&poolIdentity))
		assert.NotEqual(t, first, poolIdentity,
			"the pool must serve a different Oracle session while the pinned session is open")

		// The asserted Close is the only one on the happy path; the guard above
		// disarms the cleanup fallback, exactly as the PostgreSQL twin does it.
		require.NoError(t, sess.Close())
		closed = true
		assert.Equal(t, baseline, conn.DB.Stats().InUse,
			"closing the session should return its connection to the pool")

		_, err = sess.Exec(ctx, "SELECT 1 FROM DUAL")
		require.ErrorIs(t, err, sql.ErrConnDone, "a session call after Close must fail with sql.ErrConnDone")
	})

	t.Run("begin_runs_on_the_same_session", func(t *testing.T) {
		// Session-scoped state (ALTER SESSION, temp tables, DBMS_LOCK in a
		// privileged deployment) survives a Begin/Commit boundary only if the
		// transaction runs on the Session's own Oracle session.
		sess, err := conn.Session(ctx)
		require.NoError(t, err)
		defer sess.Close()

		var sessionIdentity string
		require.NoError(t, sess.QueryRow(ctx, sessionIdentityQuery).Scan(&sessionIdentity))

		tx, err := sess.Begin(ctx)
		require.NoError(t, err, startTransactionSucceedMsg)
		defer tx.Rollback(ctx)

		var txIdentity string
		require.NoError(t, tx.QueryRow(ctx, sessionIdentityQuery).Scan(&txIdentity))

		assert.Equal(t, sessionIdentity, txIdentity,
			"a transaction begun on the session must run on the same Oracle session")
	})

	t.Run("temp_table_rows_are_invisible_to_the_pool", func(t *testing.T) {
		// A global temporary table with ON COMMIT PRESERVE ROWS holds its rows
		// for the lifetime of the inserting Oracle session only, so it is the
		// grant-free Oracle equivalent of the PostgreSQL advisory-lock-in-
		// pg_locks property: no V$ view, no DBMS_LOCK, no ALTER SYSTEM.
		//
		// SESSION_CONTROL pins the premise. The same Session also writes one row
		// into an ORDINARY table that the pool MUST see; without that control,
		// poolRows == 0 would hold just as well if the session's writes were
		// merely uncommitted in an implicit transaction, and the test would
		// silently stop testing session scope.
		const gttCountQuery = "SELECT COUNT(*) FROM SESSION_GTT"

		_, err := conn.Exec(ctx, "CREATE GLOBAL TEMPORARY TABLE SESSION_GTT (ID NUMBER) ON COMMIT PRESERVE ROWS")
		require.NoError(t, err, shouldCreateTableMsg)
		t.Cleanup(func() {
			_, _ = conn.Exec(ctx, "DROP TABLE SESSION_GTT")
		})

		_, err = conn.Exec(ctx, "CREATE TABLE SESSION_CONTROL (ID NUMBER)")
		require.NoError(t, err, shouldCreateTableMsg)
		t.Cleanup(func() {
			_, _ = conn.Exec(ctx, "DROP TABLE SESSION_CONTROL")
		})

		sess, err := conn.Session(ctx)
		require.NoError(t, err)
		defer sess.Close()
		// Oracle refuses to drop a global temporary table while any session still
		// holds rows in it (ORA-14452), and returning the pinned connection to the
		// pool does NOT end its Oracle session — the rows would outlive the
		// subtest and defeat the t.Cleanup DROP above, which the shared schema
		// makes load-bearing. Defers run LIFO, so this empties the table before
		// sess.Close(), and both run before the cleanups.
		defer func() { _, _ = sess.Exec(ctx, "DELETE FROM SESSION_GTT") }()

		_, err = sess.Exec(ctx, "INSERT INTO SESSION_GTT (ID) VALUES (:1)", 1)
		require.NoError(t, err, "insert on the pinned session should succeed")
		_, err = sess.Exec(ctx, "INSERT INTO SESSION_CONTROL (ID) VALUES (:1)", 1)
		require.NoError(t, err, "control insert on the pinned session should succeed")

		var sessionRows int
		require.NoError(t, sess.QueryRow(ctx, gttCountQuery).Scan(&sessionRows))
		assert.Equal(t, 1, sessionRows, "the session must see the row it inserted")

		var controlRows int
		require.NoError(t, conn.QueryRow(ctx, "SELECT COUNT(*) FROM SESSION_CONTROL").Scan(&controlRows))
		assert.Equal(t, 1, controlRows,
			"the pool must see the session's ordinary-table row, proving the session's writes are committed")

		var poolRows int
		require.NoError(t, conn.QueryRow(ctx, gttCountQuery).Scan(&poolRows))
		assert.Equal(t, 0, poolRows,
			"temporary-table rows written on the pinned session must be invisible to the pool")
	})
}
