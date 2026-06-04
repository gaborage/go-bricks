package database_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/gaborage/go-bricks/database"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithTxCommitsOnSuccess(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().ExpectExec(`INSERT INTO t`).WillReturnRowsAffected(1)

	var captured dbtypes.Tx
	err := database.WithTx(t.Context(), db, func(ctx context.Context, tx dbtypes.Tx) error {
		captured = tx
		_, e := tx.Exec(ctx, "INSERT INTO t VALUES (1)")
		return e
	})
	require.NoError(t, err)

	tt, ok := captured.(*dbtesting.TestTx)
	require.True(t, ok)
	assert.True(t, tt.IsCommitted(), "tx should be committed")
	assert.False(t, tt.IsRolledBack(), "tx should not be rolled back after commit")
}

func TestWithTxRollsBackAndReturnsFnError(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction()

	sentinel := errors.New("boom")
	var captured dbtypes.Tx
	err := database.WithTx(t.Context(), db, func(_ context.Context, tx dbtypes.Tx) error {
		captured = tx
		return sentinel
	})
	assert.ErrorIs(t, err, sentinel, "WithTx must return the original fn error")
	assert.True(t, captured.(*dbtesting.TestTx).IsRolledBack(), "tx should be rolled back on fn error")
}

func TestWithTxRollsBackAndRepanics(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction()

	var captured dbtypes.Tx
	assert.PanicsWithValue(t, "kaboom", func() {
		_ = database.WithTx(t.Context(), db, func(_ context.Context, tx dbtypes.Tx) error {
			captured = tx
			panic("kaboom")
		})
	})
	assert.True(t, captured.(*dbtesting.TestTx).IsRolledBack(), "tx should be rolled back on panic")
}

func TestWithTxReturnsBeginError(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	// No ExpectTransaction queued -> Begin returns an error.

	called := false
	err := database.WithTx(t.Context(), db, func(_ context.Context, _ dbtypes.Tx) error {
		called = true
		return nil
	})
	require.Error(t, err)
	assert.False(t, called, "fn must not run when Begin fails")
}

// panicRollbackTx wraps a Tx and panics on Rollback, to exercise the case where
// a rollback during panic recovery itself panics.
type panicRollbackTx struct {
	dbtypes.Tx
}

func (panicRollbackTx) Rollback(context.Context) error { panic("rollback boom") }

// panicRollbackDB wraps an Interface so Begin yields a panicRollbackTx.
type panicRollbackDB struct {
	dbtypes.Interface
}

func (d panicRollbackDB) Begin(ctx context.Context) (dbtypes.Tx, error) {
	tx, err := d.Interface.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return panicRollbackTx{tx}, nil
}

func TestWithTxRepanicsOriginalEvenWhenRollbackPanics(t *testing.T) {
	base := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	base.ExpectTransaction()
	db := panicRollbackDB{Interface: base}

	// The original panic must propagate, not the rollback's "rollback boom".
	assert.PanicsWithValue(t, "original", func() {
		_ = database.WithTx(t.Context(), db, func(_ context.Context, _ dbtypes.Tx) error {
			panic("original")
		})
	})
}

func TestWithTxOptionsCommitsOnSuccess(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().ExpectExec(`INSERT INTO t`).WillReturnRowsAffected(1)

	opts := &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: false}
	var captured dbtypes.Tx
	err := database.WithTxOptions(t.Context(), db, opts, func(ctx context.Context, tx dbtypes.Tx) error {
		captured = tx
		_, e := tx.Exec(ctx, "INSERT INTO t VALUES (1)")
		return e
	})
	require.NoError(t, err)
	assert.True(t, captured.(*dbtesting.TestTx).IsCommitted())
}
