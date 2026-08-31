package inbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/messaging/streams"
)

// fakeHoldJobCtx is the scheduler.JobContext the drain runs under. The embedded
// context satisfies the stdlib half; the rest is field-backed.
type fakeHoldJobCtx struct {
	context.Context
	log logger.Logger
	db  dbtypes.Interface
}

func (c *fakeHoldJobCtx) JobID() string               { return holdDrainJobID }
func (c *fakeHoldJobCtx) TriggerType() string         { return "scheduled" }
func (c *fakeHoldJobCtx) Logger() logger.Logger       { return c.log }
func (c *fakeHoldJobCtx) DB() dbtypes.Interface       { return c.db }
func (c *fakeHoldJobCtx) Messaging() messaging.Client { return nil }
func (c *fakeHoldJobCtx) Config() *config.Config      { return nil }

// fakeHoldReplayer records what the drain replayed and what it was told to
// reload, and can fail or panic for a chosen offset.
type fakeHoldReplayer struct {
	consumers  []string
	replayed   []int64
	properties []map[string]any
	tenants    []string
	reloaded   []string
	reloadErr  error
	failAt     map[int64]error
	panicAt    int64
	panicked   bool
}

func (f *fakeHoldReplayer) HoldConsumers() []string { return f.consumers }

func (f *fakeHoldReplayer) Replay(_ context.Context, _ string, msg *streams.HeldMessage) error {
	if msg.Offset == f.panicAt && !f.panicked {
		f.panicked = true
		panic("replay exploded")
	}
	f.replayed = append(f.replayed, msg.Offset)
	f.properties = append(f.properties, msg.Properties)
	f.tenants = append(f.tenants, msg.TenantID)
	return f.failAt[msg.Offset]
}

// ReloadHeld records that the drain asked for a reload. The listing itself is no
// longer the drain's to pass: the generation guarding the held set has to be read
// before the ledger is, so the manager owns that read.
func (f *fakeHoldReplayer) ReloadHeld(_ context.Context, consumer string) error {
	f.reloaded = append(f.reloaded, consumer)
	return f.reloadErr
}

// newHoldDrain builds a drain over one TestDB and one replayer, with the
// intervals a test can reason about.
func newHoldDrain(db dbtypes.Interface, replayer streams.HoldReplayer) *HoldDrain {
	return &HoldDrain{
		resolve: func(context.Context) (dbtypes.Interface, HoldStore, error) {
			return db, mustPostgresHoldStore(), nil
		},
		replayer: func() streams.HoldReplayer {
			if replayer == nil {
				return nil
			}
			return replayer
		},
		cfg: config.InboxHoldConfig{
			Enabled: true, TableName: DefaultHoldTableName, DrainInterval: 5 * time.Second,
			MaxBackoff: 5 * time.Minute, MaxAge: time.Hour, LeaseDuration: time.Minute,
		},
		owner: "replica-1",
		now:   time.Now,
	}
}

// recordingHoldStore counts the calls a missing statement would otherwise hide:
// an unmatched TestDB expectation only fires when a query RUNS, so "never called"
// and "called correctly" look identical from the database side.
type recordingHoldStore struct {
	HoldStore
	leasesYielded int
	released      int
	// rowsByTenant answers NextRows per tenant. A TestDB expectation is matched by
	// substring, so every tenant's read matches the same registration and cannot
	// carry a tenant's own rows; this can.
	rowsByTenant map[string][]HoldRow
}

func (r *recordingHoldStore) NextRows(ctx context.Context, db dbtypes.Interface,
	consumer, tenant string, limit int,
) ([]HoldRow, error) {
	if r.rowsByTenant == nil {
		return r.HoldStore.NextRows(ctx, db, consumer, tenant, limit)
	}
	return r.rowsByTenant[tenant], nil
}

func (r *recordingHoldStore) Release(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) (bool, error) {
	r.released++
	return r.HoldStore.Release(ctx, db, consumer, tenant, owner)
}

func (r *recordingHoldStore) ReleaseLease(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) error {
	r.leasesYielded++
	return r.HoldStore.ReleaseLease(ctx, db, consumer, tenant, owner)
}

func mustPostgresHoldStore() HoldStore {
	store, err := NewPostgresHoldStore(DefaultHoldTableName)
	if err != nil {
		panic(err)
	}
	return store
}

func drainCtx(db dbtypes.Interface) *fakeHoldJobCtx {
	return &fakeHoldJobCtx{Context: context.Background(), log: logger.New("error", false), db: db}
}

// dueTenantRows is what DueTenants answers with, one row per tenant in the order
// the drain will take them.
func dueTenantRows(heldSince time.Time, tenants ...string) *dbtesting.RowSet {
	rows := dbtesting.NewRowSet("consumer", "tenant_id", "held_since", "attempts", "next_attempt_at", "last_error")
	for _, tenant := range tenants {
		rows.AddRow(testHoldConsumer, tenant, heldSince, 0, heldSince, "")
	}
	return rows
}

// heldRows is what NextRows answers with, in offset order.
// heldRows is what NextRows answers with. A TestDB expectation is matched by
// substring and never consumed, so every tenant in a pass reads this same set —
// which is why the rows carry one tenant and tests assert on offsets.
func heldRows(offsets ...int64) *dbtesting.RowSet {
	rows := dbtesting.NewRowSet("consumer", "stream", "stream_offset", "tenant_id", "data", "properties", "held_at")
	for _, offset := range offsets {
		rows.AddRow(testHoldConsumer, testHoldStream, offset, testHoldTenant, []byte("payload"), nil, time.Now())
	}
	return rows
}

// TestDrainReplaysInOrderAndReleases pins the whole happy pass: the tenant is
// leased, its rows replay oldest-first, each is deleted as it succeeds, and the
// tenant is released once nothing remains — then the runners are told what the
// ledger still holds.
func TestDrainReplaysInOrderAndReleases(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery(`next_attempt_at <= NOW()`).WillReturnRows(dueTenantRows(time.Now(), testHoldTenant))
	db.ExpectExec(`UPDATE ` + holdTenantTable).WillReturnRowsAffected(1) // AcquireLease
	db.ExpectQuery(`ORDER BY stream, stream_offset`).WillReturnRows(heldRows(3, 4, 5))
	db.ExpectExec(`DELETE FROM ` + holdTable).WillReturnRowsAffected(1)
	db.ExpectExec(`DELETE FROM ` + holdTenantTable).WillReturnRowsAffected(1) // Release
	db.ExpectQuery(`SELECT tenant_id FROM ` + holdTenantTable).WillReturnRows(dbtesting.NewRowSet("tenant_id"))
	db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
		dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(0), int64(0), nil))

	replayer := &fakeHoldReplayer{consumers: []string{testHoldConsumer}}
	drain := newHoldDrain(db, replayer)

	require.NoError(t, drain.Execute(drainCtx(db)))

	assert.Equal(t, []int64{3, 4, 5}, replayer.replayed, "oldest offset first")
	assert.Equal(t, []string{testHoldConsumer}, replayer.reloaded,
		"the runners are asked to refresh what the ledger still holds")
}

// TestDrainStopsTheTenantAtTheFirstFailure pins the ordering guarantee: a replay
// that fails leaves everything behind it parked, and the tenant is deferred
// rather than released.
func TestDrainStopsTheTenantAtTheFirstFailure(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery(`next_attempt_at <= NOW()`).WillReturnRows(dueTenantRows(time.Now(), testHoldTenant))
	db.ExpectExec(`UPDATE ` + holdTenantTable).WillReturnRowsAffected(1)
	db.ExpectQuery(`ORDER BY stream, stream_offset`).WillReturnRows(heldRows(3, 4, 5))
	db.ExpectExec(`DELETE FROM ` + holdTable).WillReturnRowsAffected(1)
	db.ExpectQuery(`SELECT tenant_id FROM ` + holdTenantTable).WillReturnRows(
		dbtesting.NewRowSet("tenant_id").AddRow(testHoldTenant))
	db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
		dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(1), int64(2), time.Now()))

	replayer := &fakeHoldReplayer{
		consumers: []string{testHoldConsumer},
		failAt:    map[int64]error{4: errors.New("still failing")},
	}
	store := &recordingHoldStore{HoldStore: mustPostgresHoldStore()}
	drain := newHoldDrain(db, replayer)
	drain.resolve = func(context.Context) (dbtypes.Interface, HoldStore, error) { return db, store, nil }

	require.NoError(t, drain.Execute(drainCtx(db)), "a deferred tenant is not a failed pass")

	assert.Equal(t, []int64{3, 4}, replayer.replayed, "offset 5 stays parked behind offset 4")
	assert.Zero(t, store.released, "and the tenant is not released with rows still behind the failure")
}

// TestDrainSkipsATenantAnotherReplicaHolds pins the lease: losing it means this
// pass does no work for that tenant at all.
func TestDrainSkipsATenantAnotherReplicaHolds(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery(`next_attempt_at <= NOW()`).WillReturnRows(dueTenantRows(time.Now(), testHoldTenant))
	db.ExpectExec(`UPDATE ` + holdTenantTable).WillReturnRowsAffected(0) // the lease is taken
	db.ExpectQuery(`SELECT tenant_id FROM ` + holdTenantTable).WillReturnRows(
		dbtesting.NewRowSet("tenant_id").AddRow(testHoldTenant))
	db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
		dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(1), int64(3), time.Now()))

	replayer := &fakeHoldReplayer{consumers: []string{testHoldConsumer}}
	drain := newHoldDrain(db, replayer)

	require.NoError(t, drain.Execute(drainCtx(db)))
	assert.Empty(t, replayer.replayed, "another replica owns this tenant's replay")
}

// TestDrainWithoutStreamsDoesNothing pins the case where the ledger exists but no
// stream consumer runs here: there is nothing to replay through, and no SQL is
// worth running.
func TestDrainWithoutStreamsDoesNothing(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	drain := newHoldDrain(db, nil)

	require.NoError(t, drain.Execute(drainCtx(db)))
	assert.Empty(t, db.QueryLog(), "no replayer, no pass")
	assert.Empty(t, db.ExecLog())
}

// TestDrainSurvivesAPanicInOneTenant pins the isolation: a panicking replay is
// reported by TYPE and the other tenants still drain.
func TestDrainSurvivesAPanicInOneTenant(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery(`next_attempt_at <= NOW()`).WillReturnRows(dueTenantRows(time.Now(), testHoldTenant))
	db.ExpectExec(`UPDATE ` + holdTenantTable).WillReturnRowsAffected(1)
	db.ExpectQuery(`ORDER BY stream, stream_offset`).WillReturnRows(heldRows(3))
	db.ExpectQuery(`SELECT tenant_id FROM ` + holdTenantTable).WillReturnRows(
		dbtesting.NewRowSet("tenant_id").AddRow(testHoldTenant))
	db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
		dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(1), int64(1), time.Now()))

	replayer := &fakeHoldReplayer{consumers: []string{testHoldConsumer}, panicAt: 3}
	drain := newHoldDrain(db, replayer)

	err := drain.Execute(drainCtx(db))

	require.Error(t, err)
	assert.Contains(t, err.Error(), testHoldTenant)
	assert.Contains(t, err.Error(), "panic (type: string)", "the panic value is reported by type")
}

// TestDrainContinuesAfterATenantPanics pins that one tenant's panic is contained
// to that tenant: the pass still drains the tenants behind it, which is the whole
// point of holding per tenant rather than stalling the partition.
func TestDrainContinuesAfterATenantPanics(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery(`next_attempt_at <= NOW()`).WillReturnRows(
		dueTenantRows(time.Now(), testHoldTenant, otherHoldTenant))
	db.ExpectExec(`UPDATE ` + holdTenantTable).WillReturnRowsAffected(1)
	db.ExpectExec(`DELETE FROM ` + holdTable).WillReturnRowsAffected(1)
	db.ExpectExec(`DELETE FROM ` + holdTenantTable).WillReturnRowsAffected(1)
	db.ExpectQuery(`SELECT tenant_id FROM ` + holdTenantTable).WillReturnRows(
		dbtesting.NewRowSet("tenant_id").AddRow(testHoldTenant))
	db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
		dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(1), int64(3), time.Now()))

	// The panic hits the first replay, which belongs to the first due tenant.
	replayer := &fakeHoldReplayer{consumers: []string{testHoldConsumer}, panicAt: 3}
	store := &recordingHoldStore{
		HoldStore: mustPostgresHoldStore(),
		rowsByTenant: map[string][]HoldRow{
			testHoldTenant:  {{Consumer: testHoldConsumer, Stream: testHoldStream, Offset: 3, TenantID: testHoldTenant}},
			otherHoldTenant: {{Consumer: testHoldConsumer, Stream: testHoldStream, Offset: 7, TenantID: otherHoldTenant}},
		},
	}
	drain := newHoldDrain(db, replayer)
	drain.resolve = func(context.Context) (dbtypes.Interface, HoldStore, error) { return db, store, nil }

	err := drain.Execute(drainCtx(db))

	require.Error(t, err)
	assert.Contains(t, err.Error(), testHoldTenant, "the panicking tenant is named")
	assert.Equal(t, []int64{7}, replayer.replayed, "the tenant behind the panic still drained")
	assert.Equal(t, []string{otherHoldTenant}, replayer.tenants,
		"and it was replayed as ITS own tenant, not the panicking one")
}

// heldRowsWithProperties is a batch whose rows carry the JSON blob Park writes.
func heldRowsWithProperties(properties []byte, offsets ...int64) *dbtesting.RowSet {
	rows := dbtesting.NewRowSet("consumer", "stream", "stream_offset", "tenant_id", "data", "properties", "held_at")
	for _, offset := range offsets {
		rows.AddRow(testHoldConsumer, testHoldStream, offset, testHoldTenant, []byte("payload"), properties, time.Now())
	}
	return rows
}

// TestDrainReplaysTheParkedProperties pins that a replayed message is the message
// that was parked: Park stores the producer's properties as JSON, and the replay
// has to decode them — they carry the trace carrier the lane reads.
func TestDrainReplaysTheParkedProperties(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery(`next_attempt_at <= NOW()`).WillReturnRows(dueTenantRows(time.Now(), testHoldTenant))
	db.ExpectExec(`UPDATE ` + holdTenantTable).WillReturnRowsAffected(1)
	db.ExpectQuery(`ORDER BY stream, stream_offset`).WillReturnRows(
		heldRowsWithProperties([]byte(`{"traceparent":"00-abc-def-01"}`), 3))
	db.ExpectExec(`DELETE FROM ` + holdTable).WillReturnRowsAffected(1)
	db.ExpectExec(`DELETE FROM ` + holdTenantTable).WillReturnRowsAffected(1)
	db.ExpectQuery(`SELECT tenant_id FROM ` + holdTenantTable).WillReturnRows(dbtesting.NewRowSet("tenant_id"))
	db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
		dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(0), int64(0), nil))

	replayer := &fakeHoldReplayer{consumers: []string{testHoldConsumer}}
	drain := newHoldDrain(db, replayer)

	require.NoError(t, drain.Execute(drainCtx(db)))

	require.Len(t, replayer.properties, 1)
	assert.Equal(t, map[string]any{"traceparent": "00-abc-def-01"}, replayer.properties[0],
		"the parked properties reach the handler")
}

// TestDrainDefersAnUndecodableRow pins that a row whose properties cannot be
// decoded defers the tenant rather than replaying a message missing them: no
// retry fixes the blob, and the tenant's order must survive the operator's look.
func TestDrainDefersAnUndecodableRow(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery(`next_attempt_at <= NOW()`).WillReturnRows(dueTenantRows(time.Now(), testHoldTenant))
	db.ExpectExec(`UPDATE ` + holdTenantTable).WillReturnRowsAffected(1)
	db.ExpectQuery(`ORDER BY stream, stream_offset`).WillReturnRows(
		heldRowsWithProperties([]byte(`{"broken`), 3))
	db.ExpectQuery(`SELECT tenant_id FROM ` + holdTenantTable).WillReturnRows(
		dbtesting.NewRowSet("tenant_id").AddRow(testHoldTenant))
	db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
		dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(1), int64(1), time.Now()))

	replayer := &fakeHoldReplayer{consumers: []string{testHoldConsumer}}
	drain := newHoldDrain(db, replayer)

	require.NoError(t, drain.Execute(drainCtx(db)))

	assert.Empty(t, replayer.replayed, "an undecodable row is never handed to the handler")
}

// TestDrainYieldsTheLeaseAfterAFullBatch pins that a pass which stops with rows
// still held hands the lease back. Holding it until it expires would make every
// other replica — and this one's next pass — wait out the lease for no reason.
func TestDrainYieldsTheLeaseAfterAFullBatch(t *testing.T) {
	offsets := make([]int64, holdDrainRowsPerRead)
	for i := range offsets {
		offsets[i] = int64(i + 1)
	}

	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery(`next_attempt_at <= NOW()`).WillReturnRows(dueTenantRows(time.Now(), testHoldTenant))
	// Matched on the clock expression only AcquireLease writes, so the lease
	// handback below needs its own expectation rather than borrowing this one.
	db.ExpectExec(`lease_until = NOW() +`).WillReturnRowsAffected(1)
	db.ExpectQuery(`ORDER BY stream, stream_offset`).WillReturnRows(heldRows(offsets...))
	db.ExpectExec(`DELETE FROM ` + holdTable).WillReturnRowsAffected(1)
	db.ExpectExec(`SET lease_owner = $1, lease_until = $2`).WillReturnRowsAffected(1)
	db.ExpectQuery(`SELECT tenant_id FROM ` + holdTenantTable).WillReturnRows(
		dbtesting.NewRowSet("tenant_id").AddRow(testHoldTenant))
	db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
		dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(1), int64(1), time.Now()))

	replayer := &fakeHoldReplayer{consumers: []string{testHoldConsumer}}
	store := &recordingHoldStore{HoldStore: mustPostgresHoldStore()}
	drain := newHoldDrain(db, replayer)
	drain.resolve = func(context.Context) (dbtypes.Interface, HoldStore, error) { return db, store, nil }

	require.NoError(t, drain.Execute(drainCtx(db)))

	assert.Len(t, replayer.replayed, holdDrainRowsPerRead, "the whole batch replayed")
	assert.Equal(t, 1, store.leasesYielded, "the lease goes back while rows remain")
}

// TestDrainStopsWhenTheLeaseRunsOut pins the lease as the bound on the work: once
// it is spent, the pass stops with rows still held and hands the lease back,
// rather than replaying a tenant another replica may already have taken.
func TestDrainStopsWhenTheLeaseRunsOut(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery(`next_attempt_at <= NOW()`).WillReturnRows(dueTenantRows(time.Now(), testHoldTenant))
	db.ExpectExec(`lease_until = NOW() +`).WillReturnRowsAffected(1)
	db.ExpectQuery(`ORDER BY stream, stream_offset`).WillReturnRows(heldRows(3, 4, 5))
	db.ExpectExec(`DELETE FROM ` + holdTable).WillReturnRowsAffected(1)
	db.ExpectExec(`SET lease_owner = $1, lease_until = $2`).WillReturnRowsAffected(1)
	db.ExpectQuery(`SELECT tenant_id FROM ` + holdTenantTable).WillReturnRows(
		dbtesting.NewRowSet("tenant_id").AddRow(testHoldTenant))
	db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
		dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(1), int64(2), time.Now()))

	replayer := &fakeHoldReplayer{consumers: []string{testHoldConsumer}}
	store := &recordingHoldStore{HoldStore: mustPostgresHoldStore()}
	drain := newHoldDrain(db, replayer)
	drain.resolve = func(context.Context) (dbtypes.Interface, HoldStore, error) { return db, store, nil }
	// A clock the first row spends: the deadline check passes once, then every
	// later reading is past the lease.
	start := time.Now()
	checks := 0
	drain.now = func() time.Time {
		checks++
		if checks <= 3 {
			return start
		}
		return start.Add(2 * drain.cfg.LeaseDuration)
	}

	require.NoError(t, drain.Execute(drainCtx(db)))

	assert.Equal(t, []int64{3}, replayer.replayed,
		"the pass stops at the deadline instead of draining the batch")
	assert.Equal(t, 1, store.leasesYielded, "a spent lease is handed back, not left to expire")
}
