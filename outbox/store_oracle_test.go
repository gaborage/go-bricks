package outbox

import (
	"errors"
	"testing"
	"time"

	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const oracleTestTable = "GOBRICKS_OUTBOX"

// newOracleTestStore builds a concrete *oracleStore for direct method invocation.
func newOracleTestStore(t *testing.T) *oracleStore {
	t.Helper()
	store, err := NewOracleStore(oracleTestTable)
	require.NoError(t, err)
	return store.(*oracleStore)
}

// --- Insert -----------------------------------------------------------------

func TestOracleStoreInsertSuccess(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO GOBRICKS_OUTBOX`).
		WillReturnRowsAffected(1)

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)

	require.NoError(t, store.Insert(t.Context(), tx, sampleRecord()))
}

func TestOracleStoreInsertExecError(t *testing.T) {
	store := newOracleTestStore(t)
	wantErr := errors.New("ORA-00001 unique constraint")
	tx := &errorTx{execErr: wantErr}

	err := store.Insert(t.Context(), tx, sampleRecord())
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "insert failed")
}

// --- FetchPending -----------------------------------------------------------

func TestOracleStoreFetchPendingSuccess(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)

	createdAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rows := dbtesting.NewRowSet(
		"id", "event_type", "aggregate_id", "payload", "headers",
		"exchange", "routing_key", "status", "retry_count", "created_at",
	).
		AddRow("evt-1", "order.created", "order-1", []byte(`{}`), []byte(`{}`),
			"orders", "orders.created", StatusPending, int64(0), createdAt)

	db.ExpectQuery(`SELECT id, event_type`).WillReturnRows(rows)

	out, err := store.FetchPending(t.Context(), db, 10, 3)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "evt-1", out[0].ID)
}

func TestOracleStoreFetchPendingEmpty(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)

	rows := dbtesting.NewRowSet(
		"id", "event_type", "aggregate_id", "payload", "headers",
		"exchange", "routing_key", "status", "retry_count", "created_at",
	)
	db.ExpectQuery(`SELECT id, event_type`).WillReturnRows(rows)

	out, err := store.FetchPending(t.Context(), db, 10, 3)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestOracleStoreFetchPendingQueryError(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)

	wantErr := errors.New("ORA-12541 TNS no listener")
	db.ExpectQuery(`SELECT id, event_type`).WillReturnError(wantErr)

	_, err := store.FetchPending(t.Context(), db, 10, 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "fetch pending failed")
}

// --- MarkPublished ----------------------------------------------------------

func TestOracleStoreMarkPublishedSuccess(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectExec(`UPDATE GOBRICKS_OUTBOX SET status`).WillReturnRowsAffected(1)

	require.NoError(t, store.MarkPublished(t.Context(), db, "evt-1"))
}

func TestOracleStoreMarkPublishedExecError(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	wantErr := errors.New("ORA-08177 serialization failure")
	db.ExpectExec(`UPDATE GOBRICKS_OUTBOX SET status`).WillReturnError(wantErr)

	err := store.MarkPublished(t.Context(), db, "evt-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "mark published failed")
}

// --- MarkFailed -------------------------------------------------------------

func TestOracleStoreMarkFailedSuccess(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectExec(`UPDATE GOBRICKS_OUTBOX SET retry_count`).WillReturnRowsAffected(1)

	require.NoError(t, store.MarkFailed(t.Context(), db, "evt-1", "broker offline"))
}

func TestOracleStoreMarkFailedExecError(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	wantErr := errors.New("ORA-01400 cannot insert NULL")
	db.ExpectExec(`UPDATE GOBRICKS_OUTBOX SET retry_count`).WillReturnError(wantErr)

	err := store.MarkFailed(t.Context(), db, "evt-1", "broker offline")
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "mark failed failed")
}

// --- DeletePublished --------------------------------------------------------

func TestOracleStoreDeletePublishedSuccess(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectExec(`DELETE FROM GOBRICKS_OUTBOX`).WillReturnRowsAffected(3)

	count, err := store.DeletePublished(t.Context(), db, time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestOracleStoreDeletePublishedExecError(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	wantErr := errors.New("ORA-00942 table or view does not exist")
	db.ExpectExec(`DELETE FROM GOBRICKS_OUTBOX`).WillReturnError(wantErr)

	_, err := store.DeletePublished(t.Context(), db, time.Now())
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "delete published failed")
}

// --- CreateTable ------------------------------------------------------------

func TestOracleStoreCreateTableSuccess(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	// Oracle DDL does NOT include "IF NOT EXISTS"; the table and indexes are
	// created unconditionally and ORA-00955 is treated as a warning by the caller.
	db.ExpectExec(`CREATE TABLE GOBRICKS_OUTBOX`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_GOBRICKS_OUTBOX_pending`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_GOBRICKS_OUTBOX_published`).WillReturnRowsAffected(0)

	require.NoError(t, store.CreateTable(t.Context(), db))
}

func TestOracleStoreCreateTableErrorOnTable(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	wantErr := errors.New("ORA-01031 insufficient privileges")
	db.ExpectExec(`CREATE TABLE GOBRICKS_OUTBOX`).WillReturnError(wantErr)

	err := store.CreateTable(t.Context(), db)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "create table failed")
}

func TestOracleStoreCreateTableErrorOnPendingIndex(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	wantErr := errors.New("ORA-01408 index already exists")
	db.ExpectExec(`CREATE TABLE GOBRICKS_OUTBOX`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_GOBRICKS_OUTBOX_pending`).WillReturnError(wantErr)

	err := store.CreateTable(t.Context(), db)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "create pending index failed")
}

func TestOracleStoreCreateTableErrorOnPublishedIndex(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	wantErr := errors.New("ORA-01408 index already exists")
	db.ExpectExec(`CREATE TABLE GOBRICKS_OUTBOX`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_GOBRICKS_OUTBOX_pending`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_GOBRICKS_OUTBOX_published`).WillReturnError(wantErr)

	err := store.CreateTable(t.Context(), db)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "create published index failed")
}

// Compile-time guard: ensure oracleStore satisfies the Store interface.
var _ Store = (*oracleStore)(nil)
