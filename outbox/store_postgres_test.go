package outbox

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
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
		"exchange", "routing_key", "lane", "stream", "partition_key",
		"status", "retry_count", "created_at", "seq",
	).
		AddRow("evt-1", "order.created", "order-1", []byte(`{}`), []byte(`{}`),
			"orders", "orders.created", LaneAMQP, "", "", StatusPending, int64(0), createdAt, int64(1)).
		AddRow("evt-2", "order.shipped", "order-2", []byte(`{}`), []byte(nil),
			"orders", "orders.shipped", LaneAMQP, "", "", StatusPending, int64(1), createdAt, int64(2))

	db.ExpectQuery(`SELECT id, event_type`).WillReturnRows(rows)

	out, err := store.FetchPending(t.Context(), db, 10)
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
		"exchange", "routing_key", "lane", "stream", "partition_key",
		"status", "retry_count", "created_at", "seq",
	)
	db.ExpectQuery(`SELECT id, event_type`).WillReturnRows(rows)

	out, err := store.FetchPending(t.Context(), db, 10)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestPostgresStoreFetchPendingQueryError(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)

	wantErr := errors.New("connection refused")
	db.ExpectQuery(`SELECT id, event_type`).WillReturnError(wantErr)

	_, err := store.FetchPending(t.Context(), db, 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "fetch pending failed")
}

// TestPostgresStoreFetchPendingSelectsByStatusOnly pins the parking-semantics
// change: the relay fetch must NOT filter by retry_count anymore, so an
// outage-inflated count can never freeze a healthy pending event. Parking is now
// driven solely by status='failed' (set by MarkDeadLettered).
func TestPostgresStoreFetchPendingSelectsByStatusOnly(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	rows := dbtesting.NewRowSet(
		"id", "event_type", "aggregate_id", "payload", "headers",
		"exchange", "routing_key", "lane", "stream", "partition_key",
		"status", "retry_count", "created_at", "seq",
	)
	db.ExpectQuery(`SELECT id, event_type`).WillReturnRows(rows)

	_, err := store.FetchPending(t.Context(), db, 10)
	require.NoError(t, err)

	q := db.QueryLog()
	require.Len(t, q, 1)
	assert.NotContains(t, q[0].SQL, "retry_count <", "fetch must not gate on retry_count")
	assert.Contains(t, q[0].SQL, "WHERE status")
	assert.Equal(t, []any{StatusPending, 10}, q[0].Args)
}

// --- MarkDeadLettered -------------------------------------------------------

func TestPostgresStoreMarkDeadLetteredSetsFailedStatus(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec(`UPDATE gobricks_outbox SET retry_count`).WillReturnRowsAffected(1)

	require.NoError(t, store.MarkDeadLettered(t.Context(), db, "evt-1", "poison: nacked"))

	execs := db.ExecLog()
	require.Len(t, execs, 1)
	assert.Contains(t, execs[0].SQL, "retry_count = retry_count + 1")
	assert.Contains(t, execs[0].SQL, "status =")
	assert.Equal(t, []any{StatusFailed, "poison: nacked", "evt-1"}, execs[0].Args)
}

func TestPostgresStoreMarkDeadLetteredExecError(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	wantErr := errors.New("update failed")
	db.ExpectExec(`UPDATE gobricks_outbox SET retry_count`).WillReturnError(wantErr)

	err := store.MarkDeadLettered(t.Context(), db, "evt-1", "poison")
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "mark dead-lettered failed")
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
	db.ExpectExec(`CREATE TABLE IF NOT EXISTS gobricks_outbox_leader`).WillReturnRowsAffected(0)
	db.ExpectExec(`INSERT INTO gobricks_outbox_leader`).WillReturnRowsAffected(1)

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

func TestPostgresStoreCreateTableSchemaQualified(t *testing.T) {
	store, err := NewPostgresStore("myschema.outbox_events")
	require.NoError(t, err)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	// Index NAMES derive from the last segment ("outbox_events"); a dotted index
	// name like "idx_myschema.outbox_events_pending" is invalid SQL.
	db.ExpectExec(`CREATE TABLE IF NOT EXISTS myschema.outbox_events`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_outbox_events_pending`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_outbox_events_published`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE TABLE IF NOT EXISTS myschema.outbox_events_leader`).WillReturnRowsAffected(0)
	db.ExpectExec(`INSERT INTO myschema.outbox_events_leader`).WillReturnRowsAffected(1)

	require.NoError(t, store.CreateTable(t.Context(), db))
	// Both index ON clauses must reference the full schema-qualified name.
	execs := db.ExecLog()
	require.Len(t, execs, 5) // table, pending index, published index, leader table, leader seed
	assert.Contains(t, execs[1].SQL, "ON myschema.outbox_events", "pending index must target the qualified table")
	assert.Contains(t, execs[2].SQL, "ON myschema.outbox_events", "published index must target the qualified table")
}

// Compile-time guard: ensure postgresStore satisfies the Store interface.
var _ Store = (*postgresStore)(nil)

// --- lane, stream, partition key, sequence ----------------------------------

func TestPostgresStoreInsertWritesLaneAndStreamColumns(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	tx := db.ExpectTransaction()
	tx.ExpectExec(`INSERT INTO gobricks_outbox`).WillReturnRowsAffected(1)

	begun, err := db.Begin(t.Context())
	require.NoError(t, err)

	record := sampleRecord()
	record.Exchange = ""
	record.RoutingKey = ""
	record.Lane = LaneStream
	record.Stream = "orders"
	record.PartitionKey = "acme"
	require.NoError(t, store.Insert(t.Context(), begun, record))

	log := tx.ExecLog()
	require.Len(t, log, 1)
	assert.Contains(t, log[0].SQL, "lane")
	assert.Contains(t, log[0].SQL, "stream")
	assert.Contains(t, log[0].SQL, "partition_key")
	assert.NotContains(t, log[0].SQL, "seq", "seq is assigned by the database, never written by Insert")
	assert.Equal(t, []any{LaneStream, "orders", "acme"}, log[0].Args[7:10],
		"lane, stream and partition_key follow routing_key in argument order")
}

func TestPostgresStoreInsertAMQPRowDefaultsLane(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	tx := db.ExpectTransaction()
	tx.ExpectExec(`INSERT INTO gobricks_outbox`).WillReturnRowsAffected(1)

	begun, err := db.Begin(t.Context())
	require.NoError(t, err)

	record := sampleRecord()
	record.Lane = ""
	require.NoError(t, store.Insert(t.Context(), begun, record))

	log := tx.ExecLog()
	require.Len(t, log, 1)
	assert.Equal(t, LaneAMQP, log[0].Args[7], "the store fills an empty lane, so no row carries one")
}

func TestPostgresStoreFetchPendingOrdersBySeq(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)

	createdAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rows := dbtesting.NewRowSet(
		"id", "event_type", "aggregate_id", "payload", "headers",
		"exchange", "routing_key", "lane", "stream", "partition_key",
		"status", "retry_count", "created_at", "seq",
	).
		AddRow("evt-1", "order.created", "order-1", []byte(`{}`), []byte(`{}`),
			"orders", "orders.created", LaneAMQP, "", "", StatusPending, int64(0), createdAt, int64(7)).
		AddRow("evt-2", "customer.created", "cust-1", []byte(`{}`), []byte(`{}`),
			"", "", LaneStream, "customers", "acme", StatusPending, int64(0), createdAt, int64(8))

	db.ExpectQuery(`SELECT id, event_type`).WillReturnRows(rows)

	out, err := store.FetchPending(t.Context(), db, 10)
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, int64(7), out[0].Seq)
	assert.Equal(t, int64(8), out[1].Seq)
	assert.Equal(t, LaneStream, out[1].Lane)
	assert.Equal(t, "customers", out[1].Stream)
	assert.Equal(t, "acme", out[1].PartitionKey)

	log := db.QueryLog()
	require.Len(t, log, 1)
	assert.Contains(t, log[0].SQL, "seq")
	assert.Contains(t, log[0].SQL, "ORDER BY seq ASC")
}

func TestPostgresStoreCreateTableCreatesLeaderAndSeqIndex(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec(`CREATE TABLE IF NOT EXISTS gobricks_outbox`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_pending`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_published`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE TABLE IF NOT EXISTS gobricks_outbox_leader`).WillReturnRowsAffected(0)
	db.ExpectExec(`INSERT INTO gobricks_outbox_leader`).WillReturnRowsAffected(1)

	require.NoError(t, store.CreateTable(t.Context(), db))

	log := db.ExecLog()
	require.Len(t, log, 5)
	assert.Contains(t, log[0].SQL, "seq")
	assert.Contains(t, log[0].SQL, "lane")
	assert.Contains(t, log[1].SQL, "(seq)")
	assert.Contains(t, log[3].SQL, "gobricks_outbox_leader")
	assert.Contains(t, log[4].SQL, "ON CONFLICT")
}

func TestPostgresStoreCreateTableSeedErrorIsReported(t *testing.T) {
	store := newPostgresTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	wantErr := errors.New("seed failed")
	db.ExpectExec(`CREATE TABLE IF NOT EXISTS gobricks_outbox`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_pending`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_gobricks_outbox_published`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE TABLE IF NOT EXISTS gobricks_outbox_leader`).WillReturnRowsAffected(0)
	db.ExpectExec(`INSERT INTO gobricks_outbox_leader`).WillReturnError(wantErr)

	err := store.CreateTable(t.Context(), db)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "leader")
}
