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

const pgTestTable = "gobricks_outbox"

// newPostgresTestStore builds a concrete *postgresStore for direct method invocation.
// Tests use the concrete type (not the Store interface) so they don't depend on the
// lazyStore wrapper in module.go.
func newPostgresTestStore(t *testing.T) *postgresStore {
	t.Helper()
	store, err := NewPostgresStore(pgTestTable)
	require.NoError(t, err)
	return store.(*postgresStore)
}

// sampleRecord returns a fully-populated outbox Record fixture for Insert tests.
func sampleRecord() *Record {
	return &Record{
		ID:          "evt-1",
		EventType:   "order.created",
		AggregateID: "order-42",
		Payload:     []byte(`{"orderId":42}`),
		Headers:     []byte(`{"x":"1"}`),
		Exchange:    "orders",
		RoutingKey:  "orders.created",
		Status:      StatusPending,
		CreatedAt:   time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
}

// --- Insert -----------------------------------------------------------------

func TestPostgresStoreInsertSuccess(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_outbox`).
		WillReturnRowsAffected(1)

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)

	require.NoError(t, store.Insert(t.Context(), tx, sampleRecord()))
}

func TestPostgresStoreInsertExecError(t *testing.T) {
	store := newPostgresTestStore(t)
	wantErr := errors.New("constraint violation")
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_outbox`).
		WillReturnError(wantErr)

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)

	err = store.Insert(t.Context(), tx, sampleRecord())
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "insert failed")
}

// --- FetchPending -----------------------------------------------------------

func TestPostgresStoreFetchPendingSuccess(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)

	createdAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rows := dbtesting.NewRowSet(
		"id", "event_type", "aggregate_id", "payload", "headers",
		"exchange", "routing_key", "status", "retry_count", "created_at",
	).
		AddRow("evt-1", "order.created", "order-1", []byte(`{}`), []byte(`{}`),
			"orders", "orders.created", StatusPending, int64(0), createdAt).
		AddRow("evt-2", "order.shipped", "order-2", []byte(`{}`), []byte(nil),
			"orders", "orders.shipped", StatusPending, int64(1), createdAt)

	db.ExpectQuery(`SELECT id, event_type`).WillReturnRows(rows)

	out, err := store.FetchPending(t.Context(), db, 10, 3)
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "evt-1", out[0].ID)
	assert.Equal(t, "evt-2", out[1].ID)
	assert.Equal(t, 1, out[1].RetryCount)
}

func TestPostgresStoreFetchPendingEmpty(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)

	rows := dbtesting.NewRowSet(
		"id", "event_type", "aggregate_id", "payload", "headers",
		"exchange", "routing_key", "status", "retry_count", "created_at",
	)
	db.ExpectQuery(`SELECT id, event_type`).WillReturnRows(rows)

	out, err := store.FetchPending(t.Context(), db, 10, 3)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestPostgresStoreFetchPendingQueryError(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)

	wantErr := errors.New("connection refused")
	db.ExpectQuery(`SELECT id, event_type`).WillReturnError(wantErr)

	_, err := store.FetchPending(t.Context(), db, 10, 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "fetch pending failed")
}

// --- MarkPublished ----------------------------------------------------------

func TestPostgresStoreMarkPublishedSuccess(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec(`UPDATE gobricks_outbox SET status`).WillReturnRowsAffected(1)

	require.NoError(t, store.MarkPublished(t.Context(), db, "evt-1"))
}

func TestPostgresStoreMarkPublishedExecError(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	wantErr := errors.New("update failed")
	db.ExpectExec(`UPDATE gobricks_outbox SET status`).WillReturnError(wantErr)

	err := store.MarkPublished(t.Context(), db, "evt-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "mark published failed")
}

// --- MarkFailed -------------------------------------------------------------

func TestPostgresStoreMarkFailedSuccess(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec(`UPDATE gobricks_outbox SET retry_count`).WillReturnRowsAffected(1)

	require.NoError(t, store.MarkFailed(t.Context(), db, "evt-1", "broker offline"))
}

func TestPostgresStoreMarkFailedExecError(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	wantErr := errors.New("update failed")
	db.ExpectExec(`UPDATE gobricks_outbox SET retry_count`).WillReturnError(wantErr)

	err := store.MarkFailed(t.Context(), db, "evt-1", "broker offline")
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "mark failed failed")
}

// --- DeletePublished --------------------------------------------------------

func TestPostgresStoreDeletePublishedSuccess(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec(`DELETE FROM gobricks_outbox`).WillReturnRowsAffected(7)

	count, err := store.DeletePublished(t.Context(), db, time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(7), count)
}

func TestPostgresStoreDeletePublishedExecError(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	wantErr := errors.New("delete failed")
	db.ExpectExec(`DELETE FROM gobricks_outbox`).WillReturnError(wantErr)

	_, err := store.DeletePublished(t.Context(), db, time.Now())
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "delete published failed")
}

// --- CreateTable ------------------------------------------------------------

func TestPostgresStoreCreateTableSuccess(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec(`CREATE TABLE IF NOT EXISTS gobricks_outbox`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_pending`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_published`).WillReturnRowsAffected(0)

	require.NoError(t, store.CreateTable(t.Context(), db))
}

func TestPostgresStoreCreateTableErrorOnTable(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	wantErr := errors.New("table create failed")
	db.ExpectExec(`CREATE TABLE IF NOT EXISTS gobricks_outbox`).WillReturnError(wantErr)

	err := store.CreateTable(t.Context(), db)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "create table failed")
}

func TestPostgresStoreCreateTableErrorOnPendingIndex(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	wantErr := errors.New("pending index failed")
	db.ExpectExec(`CREATE TABLE IF NOT EXISTS gobricks_outbox`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_pending`).WillReturnError(wantErr)

	err := store.CreateTable(t.Context(), db)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "create pending index failed")
}

func TestPostgresStoreCreateTableErrorOnPublishedIndex(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	wantErr := errors.New("published index failed")
	db.ExpectExec(`CREATE TABLE IF NOT EXISTS gobricks_outbox`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_pending`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_published`).WillReturnError(wantErr)

	err := store.CreateTable(t.Context(), db)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "create published index failed")
}

// Compile-time guard: ensure postgresStore satisfies the Store interface.
var _ Store = (*postgresStore)(nil)
