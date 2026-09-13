// Package testing_test holds the executable copies of the database/testing
// examples that live in the docs, so a doc edit that diverges from the fake
// breaks the build.
package testing_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtest "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

const (
	docLockSQL   = "SELECT pg_advisory_lock(4242)"
	docUnlockSQL = "SELECT pg_advisory_unlock(4242)"
	docUpdateSQL = "UPDATE ledger SET relayed = true WHERE relayed = false"
)

// relayLedger is the flow the ExpectSession examples describe: pin a session,
// take the advisory lock on it, run the UPDATE inside a transaction begun on
// that session, commit, unlock, and close the session.
func relayLedger(ctx context.Context, db *dbtest.TestDB) (rowsAffected int64, err error) {
	sess, err := db.Session(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := sess.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	if _, err = sess.Exec(ctx, docLockSQL); err != nil {
		return 0, err
	}

	tx, err := sess.Begin(ctx)
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(ctx, docUpdateSQL)
	if err != nil {
		return 0, err
	}
	rowsAffected, err = res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}

	if _, err = sess.Exec(ctx, docUnlockSQL); err != nil {
		return 0, err
	}
	return rowsAffected, nil
}

// TestExpectSessionDocExample pins the ExpectSession snippets published in
// llms.txt ("Dedicated Session Test") and wiki/testing.md ("Dedicated Session
// Testing"): the expectation block below is those snippets verbatim.
func TestExpectSessionDocExample(t *testing.T) {
	ctx := context.Background()

	db := dbtest.NewTestDB(dbtypes.PostgreSQL)
	sess := db.ExpectSession()
	sess.ExpectExec("pg_advisory_lock").WillReturnRowsAffected(1)
	sess.ExpectExec("pg_advisory_unlock").WillReturnRowsAffected(1)
	sess.ExpectTransaction().ExpectExec("UPDATE ledger").WillReturnRowsAffected(3)

	rows, err := relayLedger(ctx, db)

	require.NoError(t, err)
	assert.Equal(t, int64(3), rows)
	dbtest.AssertSessionClosed(t, sess)
	dbtest.AssertTransactionCommitted(t, db)

	execs := sess.ExecLog()
	require.Len(t, execs, 2, "the lock and the unlock run on the session, the UPDATE on its transaction")
	assert.Equal(t, docLockSQL, execs[0].SQL)
	assert.Equal(t, docUnlockSQL, execs[1].SQL)
}
