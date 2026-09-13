//go:build integration

package oracle

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionIdentityQuery renders the Oracle session identity available to an
// unprivileged schema: no V$ view is readable by the test user, so the pair
// SID + SESSIONID (the audit session id) from SYS_CONTEXT is the identity.
// Both live in USERENV and both are per-session, so the concatenation
// distinguishes two concurrently live sessions even if one half were reused.
const sessionIdentityQuery = `SELECT SYS_CONTEXT('USERENV','SID') || '-' || SYS_CONTEXT('USERENV','SESSIONID') FROM DUAL`

// TestSessionOracleIdentityIsStableAndDistinctFromPool confirms a dedicated
// Session stays pinned to one Oracle session across statements, and that the
// pool handle speaks to a different one while that Session is open.
//
// The pool assertion cannot pass by luck: while a Session is open, its
// physical connection is checked OUT of database/sql's pool, so the pool
// cannot hand the same *driver* connection to conn.QueryRow — and two Oracle
// sessions that are alive at the same instant necessarily carry different
// SIDs. A regression that let Session share the pool's connection would
// therefore have to make the two identities equal, which is impossible here
// unless the pinning is gone.
//
// It also folds in the two vendor-neutral pool-accounting properties, which
// link 1 pins only against sqlmock (database/internal/wrapper/session_test.go)
// and on PostgreSQL: an open Session holds exactly one extra pooled
// connection, Close returns it, and every call after Close reports
// sql.ErrConnDone.
func TestSessionOracleIdentityIsStableAndDistinctFromPool(t *testing.T) {
	conn, ctx := setupTestSchema(t)

	baseline := conn.DB.Stats().InUse

	sess, err := conn.Session(ctx)
	require.NoError(t, err, "Session should acquire a pinned connection")
	defer sess.Close()

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

	require.NoError(t, sess.Close())
	assert.Equal(t, baseline, conn.DB.Stats().InUse,
		"closing the session should return its connection to the pool")

	_, err = sess.Exec(ctx, "SELECT 1 FROM DUAL")
	require.ErrorIs(t, err, sql.ErrConnDone, "a session call after Close must fail with sql.ErrConnDone")
}

// TestSessionOracleBeginRunsOnTheSameSession confirms a transaction begun on a
// Session runs on the SAME Oracle session as the Session itself — the property
// that makes session-scoped state (SET, temp tables, DBMS_LOCK in a privileged
// deployment) survive a Begin/Commit boundary.
func TestSessionOracleBeginRunsOnTheSameSession(t *testing.T) {
	conn, ctx := setupTestSchema(t)

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
}

// TestSessionOracleTempTableRowsAreInvisibleToThePool confirms session-scoped
// state written through a Session is invisible to the pool handle. A global
// temporary table with ON COMMIT PRESERVE ROWS holds its rows for the lifetime
// of the inserting Oracle session only, so it is the grant-free Oracle
// equivalent of the PostgreSQL advisory-lock property: no V$ view, no
// DBMS_LOCK, no ALTER SYSTEM.
func TestSessionOracleTempTableRowsAreInvisibleToThePool(t *testing.T) {
	conn, ctx := setupTestSchema(t)

	const (
		createGTT   = "CREATE GLOBAL TEMPORARY TABLE SESSION_GTT (ID NUMBER) ON COMMIT PRESERVE ROWS"
		dropGTT     = "DROP TABLE SESSION_GTT"
		insertGTT   = "INSERT INTO SESSION_GTT (ID) VALUES (:1)"
		deleteGTT   = "DELETE FROM SESSION_GTT"
		countQuery  = "SELECT COUNT(*) FROM SESSION_GTT"
		insertLabel = "insert on the pinned session should succeed"
	)

	_, err := conn.Exec(ctx, createGTT)
	require.NoError(t, err, shouldCreateTableMsg)
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, dropGTT)
	})

	sess, err := conn.Session(ctx)
	require.NoError(t, err)
	defer sess.Close()
	// Oracle refuses to drop a global temporary table while any session still
	// holds rows in it (ORA-14452), and returning the pinned connection to the
	// pool does NOT end its Oracle session — the rows would outlive the test.
	// Defers run LIFO, so this empties the table before sess.Close().
	defer func() { _, _ = sess.Exec(ctx, deleteGTT) }()

	_, err = sess.Exec(ctx, insertGTT, 1)
	require.NoError(t, err, insertLabel)

	var sessionRows int
	require.NoError(t, sess.QueryRow(ctx, countQuery).Scan(&sessionRows))
	assert.Equal(t, 1, sessionRows, "the session must see the row it inserted")

	var poolRows int
	require.NoError(t, conn.QueryRow(ctx, countQuery).Scan(&poolRows))
	assert.Equal(t, 0, poolRows,
		"temporary-table rows written on the pinned session must be invisible to the pool")
}
