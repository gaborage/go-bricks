package outbox

import (
	"errors"
	"testing"
	"time"

	oranet "github.com/sijms/go-ora/v2/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
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
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO GOBRICKS_OUTBOX`).
		WillReturnError(wantErr)

	tx, err := db.Begin(t.Context())
	require.NoError(t, err)

	err = store.Insert(t.Context(), tx, sampleRecord())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insert failed")
	require.ErrorIs(t, err, wantErr)
}

// TestOracleCreateTableDDLAllowsEmptyExchange guards issue #586: the exchange/routing_key
// columns must be nullable. In Oracle ” IS NULL, so a DEFAULT ” NOT NULL constraint is
// self-contradictory and rejects default-exchange events with ORA-01400.
func TestOracleCreateTableDDLAllowsEmptyExchange(t *testing.T) {
	assert.NotContains(t, oracleCreateTableSQL, "exchange      VARCHAR2(255) DEFAULT '' NOT NULL",
		"exchange must be nullable on Oracle ('' is NULL)")
	assert.NotContains(t, oracleCreateTableSQL, "routing_key   VARCHAR2(255) DEFAULT '' NOT NULL",
		"routing_key must be nullable on Oracle ('' is NULL)")
	assert.Contains(t, oracleCreateTableSQL, "exchange      VARCHAR2(255),")
	assert.Contains(t, oracleCreateTableSQL, "routing_key   VARCHAR2(255),")
}

// --- FetchPending -----------------------------------------------------------

func TestOracleStoreFetchPendingSuccess(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)

	createdAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rows := dbtesting.NewRowSet(
		"id", "event_type", "aggregate_id", "payload", "headers",
		"exchange", "routing_key", "lane", "stream", "partition_key",
		"status", "retry_count", "created_at", "seq",
	).
		AddRow("evt-1", "order.created", "order-1", []byte(`{}`), []byte(`{}`),
			"orders", "orders.created", LaneAMQP, "", "", StatusPending, int64(0), createdAt, int64(1))

	db.ExpectQuery(`SELECT id, event_type`).WillReturnRows(rows)

	out, err := store.FetchPending(t.Context(), db, 10)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "evt-1", out[0].ID)
}

// TestOracleStoreFetchPendingHandlesNullExchangeAndRoutingKey guards issue #586: on Oracle
// an empty exchange/routing_key (the AMQP default exchange) is stored as NULL (” IS NULL in
// Oracle), so FetchPending must scan those columns NULL-tolerantly and map NULL -> "" rather
// than failing with "converting NULL to string is unsupported".
func TestOracleStoreFetchPendingHandlesNullExchangeAndRoutingKey(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)

	createdAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rows := dbtesting.NewRowSet(
		"id", "event_type", "aggregate_id", "payload", "headers",
		"exchange", "routing_key", "lane", "stream", "partition_key",
		"status", "retry_count", "created_at", "seq",
	).
		AddRow("evt-default-ex", "order.created", "order-1", []byte(`{}`), []byte(`{}`),
			nil, nil, LaneAMQP, nil, nil, StatusPending, int64(0), createdAt, int64(1)) // NULL exchange + routing_key

	db.ExpectQuery(`SELECT id, event_type`).WillReturnRows(rows)

	out, err := store.FetchPending(t.Context(), db, 10)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Empty(t, out[0].Exchange, "NULL exchange must map to the empty (default) exchange")
	assert.Empty(t, out[0].RoutingKey, "NULL routing_key must map to empty")
}

func TestOracleStoreFetchPendingEmpty(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)

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

func TestOracleStoreFetchPendingQueryError(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)

	wantErr := errors.New("ORA-12541 TNS no listener")
	db.ExpectQuery(`SELECT id, event_type`).WillReturnError(wantErr)

	_, err := store.FetchPending(t.Context(), db, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch pending failed")
	require.ErrorIs(t, err, wantErr)
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
	assert.Contains(t, err.Error(), "mark published failed")
	require.ErrorIs(t, err, wantErr)
}

// TestOracleStoreFetchPendingSelectsByStatusOnly mirrors the Postgres test: the
// fetch is status-gated (no retry_count filter) so an outage cannot freeze events.
func TestOracleStoreFetchPendingSelectsByStatusOnly(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
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
	assert.Contains(t, q[0].SQL, "FETCH NEXT 10 ROWS ONLY", "the batch size renders as a literal, not a bound argument")
	assert.Equal(t, []any{StatusPending}, q[0].Args)
}

// --- MarkDeadLettered -------------------------------------------------------

func TestOracleStoreMarkDeadLetteredSetsFailedStatus(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectExec(`UPDATE GOBRICKS_OUTBOX SET retry_count`).WillReturnRowsAffected(1)

	require.NoError(t, store.MarkDeadLettered(t.Context(), db, "evt-1", "poison: nacked"))

	execs := db.ExecLog()
	require.Len(t, execs, 1)
	assert.Contains(t, execs[0].SQL, "retry_count = retry_count + 1")
	assert.Contains(t, execs[0].SQL, "status =")
	// Oracle's error column is error_msg (a reserved-word rename), NOT error.
	assert.Contains(t, execs[0].SQL, "error_msg =")
	assert.Equal(t, []any{StatusFailed, "poison: nacked", "evt-1"}, execs[0].Args)
}

func TestOracleStoreMarkDeadLetteredExecError(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	wantErr := errors.New("ORA-08177 serialization failure")
	db.ExpectExec(`UPDATE GOBRICKS_OUTBOX SET retry_count`).WillReturnError(wantErr)

	err := store.MarkDeadLettered(t.Context(), db, "evt-1", "poison")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mark dead-lettered failed")
	require.ErrorIs(t, err, wantErr)
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
	assert.Contains(t, err.Error(), "mark failed failed")
	require.ErrorIs(t, err, wantErr)
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
	assert.Contains(t, err.Error(), "delete published failed")
	require.ErrorIs(t, err, wantErr)
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
	db.ExpectExec(`CREATE TABLE GOBRICKS_OUTBOX_leader`).WillReturnRowsAffected(0)
	db.ExpectExec(`MERGE INTO GOBRICKS_OUTBOX_leader`).WillReturnRowsAffected(1)

	require.NoError(t, store.CreateTable(t.Context(), db))
}

func TestOracleStoreCreateTableSchemaQualified(t *testing.T) {
	store, err := NewOracleStore("MYSCHEMA.OUTBOX_EVENTS")
	require.NoError(t, err)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	// Index NAMES derive from the last segment; a dotted index name is invalid SQL.
	db.ExpectExec(`CREATE TABLE MYSCHEMA.OUTBOX_EVENTS`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_OUTBOX_EVENTS_pending`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_OUTBOX_EVENTS_published`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE TABLE MYSCHEMA.OUTBOX_EVENTS_leader`).WillReturnRowsAffected(0)
	db.ExpectExec(`MERGE INTO MYSCHEMA.OUTBOX_EVENTS_leader`).WillReturnRowsAffected(1)

	require.NoError(t, store.CreateTable(t.Context(), db))
	// Both index ON clauses must reference the full schema-qualified name.
	execs := db.ExecLog()
	require.Len(t, execs, 5) // table, pending index, published index, leader table, leader seed
	assert.Contains(t, execs[1].SQL, "ON MYSCHEMA.OUTBOX_EVENTS", "pending index must target the qualified table")
	assert.Contains(t, execs[2].SQL, "ON MYSCHEMA.OUTBOX_EVENTS", "published index must target the qualified table")
}

func TestOracleStoreCreateTableErrorOnTable(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	wantErr := errors.New("ORA-01031 insufficient privileges")
	db.ExpectExec(`CREATE TABLE GOBRICKS_OUTBOX`).WillReturnError(wantErr)

	err := store.CreateTable(t.Context(), db)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create table failed")
	require.ErrorIs(t, err, wantErr)
}

func TestOracleStoreCreateTableErrorOnPendingIndex(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	wantErr := errors.New("ORA-01408 index already exists")
	db.ExpectExec(`CREATE TABLE GOBRICKS_OUTBOX`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_GOBRICKS_OUTBOX_pending`).WillReturnError(wantErr)

	err := store.CreateTable(t.Context(), db)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create pending index failed")
	require.ErrorIs(t, err, wantErr)
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
	assert.Contains(t, err.Error(), "create published index failed")
	require.ErrorIs(t, err, wantErr)
}

// Compile-time guard: ensure oracleStore satisfies the Store interface.
var _ Store = (*oracleStore)(nil)

// --- lane, stream, partition key, sequence ----------------------------------

func TestOracleStoreInsertWritesLaneAndStreamColumns(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	tx := db.ExpectTransaction()
	tx.ExpectExec(`INSERT INTO GOBRICKS_OUTBOX`).WillReturnRowsAffected(1)

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

func TestOracleStoreInsertAMQPRowDefaultsLane(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	tx := db.ExpectTransaction()
	tx.ExpectExec(`INSERT INTO GOBRICKS_OUTBOX`).WillReturnRowsAffected(1)

	begun, err := db.Begin(t.Context())
	require.NoError(t, err)

	record := sampleRecord()
	record.Lane = ""
	require.NoError(t, store.Insert(t.Context(), begun, record))

	log := tx.ExecLog()
	require.Len(t, log, 1)
	assert.Equal(t, LaneAMQP, log[0].Args[7], "the store fills an empty lane, so no row carries one")
}

func TestOracleStoreFetchPendingOrdersBySeq(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)

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

// TestOracleStoreFetchPendingMapsNullStreamToEmpty pins the Oracle ” IS NULL
// handling for the stream-lane columns, matching exchange/routing_key (issue #586).
func TestOracleStoreFetchPendingMapsNullStreamToEmpty(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)

	rows := dbtesting.NewRowSet(
		"id", "event_type", "aggregate_id", "payload", "headers",
		"exchange", "routing_key", "lane", "stream", "partition_key",
		"status", "retry_count", "created_at", "seq",
	).
		AddRow("evt-1", "order.created", "order-1", []byte(`{}`), []byte(`{}`),
			"orders", "orders.created", LaneAMQP, nil, nil,
			StatusPending, int64(0), time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), int64(1))

	db.ExpectQuery(`SELECT id, event_type`).WillReturnRows(rows)

	out, err := store.FetchPending(t.Context(), db, 10)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Empty(t, out[0].Stream)
	assert.Empty(t, out[0].PartitionKey)
}

func TestOracleStoreCreateTableCreatesLeaderAndSeqIndex(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	db.ExpectExec(`CREATE TABLE GOBRICKS_OUTBOX`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_GOBRICKS_OUTBOX_pending`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE INDEX idx_GOBRICKS_OUTBOX_published`).WillReturnRowsAffected(0)
	db.ExpectExec(`CREATE TABLE GOBRICKS_OUTBOX_leader`).WillReturnRowsAffected(0)
	db.ExpectExec(`MERGE INTO GOBRICKS_OUTBOX_leader`).WillReturnRowsAffected(1)

	require.NoError(t, store.CreateTable(t.Context(), db))

	log := db.ExecLog()
	require.Len(t, log, 5)
	assert.Contains(t, log[0].SQL, "seq")
	assert.Contains(t, log[0].SQL, "lane")
	assert.Contains(t, log[1].SQL, "THEN seq END")
	assert.Contains(t, log[3].SQL, "GOBRICKS_OUTBOX_leader")
	assert.Contains(t, log[4].SQL, "MERGE INTO")
}

// TestOracleStoreFetchPendingEmptyStreamIsAMQPLane is the Oracle half of C1's regression.
// Oracle keeps both columns NULLABLE — ” IS NULL there, so NOT NULL DEFAULT ” would reject
// every AMQP-lane insert with ORA-01400 (issue #586) — and maps NULL back to "" on scan.
func TestOracleStoreFetchPendingEmptyStreamIsAMQPLane(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	rows := dbtesting.NewRowSet(
		"id", "event_type", "aggregate_id", "payload", "headers",
		"exchange", "routing_key", "lane", "stream", "partition_key",
		"status", "retry_count", "created_at", "seq",
	).AddRow("evt-1", "order.created", "order-1", []byte(`{}`), []byte(`{}`),
		"orders", "orders.created", LaneAMQP, nil, nil,
		StatusPending, int64(0), time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), int64(1))
	db.ExpectQuery(`SELECT id, event_type`).WillReturnRows(rows)

	out, err := store.FetchPending(t.Context(), db, 10)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, LaneAMQP, out[0].Lane)
	assert.Empty(t, out[0].Stream)
	assert.Empty(t, out[0].PartitionKey)
}

// --- Lead --------------------------------------------------------------------

func TestOracleStoreLeadAcquiresRow(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	tx := db.ExpectTransaction()
	tx.ExpectQuery(`FOR UPDATE NOWAIT`).WillReturnRows(dbtesting.NewRowSet("id").AddRow(int64(1)))
	tx.ExpectExec(`SELECT 1 FROM dual`).WillReturnRowsAffected(0)

	lead, err := store.Lead(t.Context(), db)
	require.NoError(t, err)
	require.NotNil(t, lead)

	log := tx.QueryLog()
	require.Len(t, log, 1)
	assert.Contains(t, log[0].SQL, "GOBRICKS_OUTBOX_leader")
	assert.Contains(t, log[0].SQL, "FOR UPDATE NOWAIT")

	require.NoError(t, lead.Probe(t.Context()))
	require.NoError(t, lead.Release(t.Context()))
	dbtesting.AssertRolledBack(t, tx)
}

func TestOracleStoreLeadNotLeader(t *testing.T) {
	store := newOracleTestStore(t)
	db := dbtesting.NewTestDB(dbtypes.Oracle)
	tx := db.ExpectTransaction()
	tx.ExpectQuery(`FOR UPDATE NOWAIT`).WillReturnError(&oranet.OracleError{ErrCode: 54})

	lead, err := store.Lead(t.Context(), db)
	require.Error(t, err)
	assert.Nil(t, lead)
	dbtesting.AssertRolledBack(t, tx)
	assert.ErrorIs(t, err, ErrNotLeader)
}
