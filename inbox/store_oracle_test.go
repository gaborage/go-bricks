package inbox

import (
	"errors"
	"testing"
	"time"

	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	oranet "github.com/sijms/go-ora/v2/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const oracleTestTable = "gobricks_inbox"

func newOracleTestStore(t *testing.T) *oracleStore {
	t.Helper()
	store, err := NewOracleStore(oracleTestTable)
	require.NoError(t, err)
	return store.(*oracleStore)
}

func TestOracleStoreMarkProcessedInserted(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).
		WillReturnRowsAffected(1)

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)

	inserted, err := store.MarkProcessed(t.Context(), tx, sampleRecord())
	require.NoError(t, err)
	assert.True(t, inserted)
}

func TestOracleStoreMarkProcessedDuplicate(t *testing.T) {
	store := newOracleTestStore(t)
	// ORA-00001 unique violation -> caught as a duplicate, not an error.
	oraDup := &oranet.OracleError{ErrCode: 1}
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).
		WillReturnError(oraDup)

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)

	inserted, err := store.MarkProcessed(t.Context(), tx, sampleRecord())
	require.NoError(t, err, "ORA-00001 must be caught, not surfaced")
	assert.False(t, inserted, "duplicate reports inserted=false")
}

func TestOracleStoreMarkProcessedNonUniqueError(t *testing.T) {
	store := newOracleTestStore(t)
	wantErr := errors.New("ORA-12541 no listener")
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).
		WillReturnError(wantErr)

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)

	_, err = store.MarkProcessed(t.Context(), tx, sampleRecord())
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "mark processed failed")
}

func TestOracleStoreDeleteProcessed(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectExec(`DELETE FROM gobricks_inbox`).WillReturnRowsAffected(2)

	n, err := store.DeleteProcessed(t.Context(), db, time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
}

func TestOracleStoreCreateTable(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectExec(`CREATE TABLE gobricks_inbox`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_gobricks_inbox_processed`).WillReturnRowsAffected(0)

	require.NoError(t, store.CreateTable(t.Context(), db))
}
