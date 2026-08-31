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
	consumers []string
	replayed  []int64
	reloaded  map[string][]string
	failAt    map[int64]error
	panicAt   int64
	panicked  bool
}

func (f *fakeHoldReplayer) HoldConsumers() []string { return f.consumers }

func (f *fakeHoldReplayer) Replay(_ context.Context, _ string, msg *streams.HeldMessage) error {
	if msg.Offset == f.panicAt && !f.panicked {
		f.panicked = true
		panic("replay exploded")
	}
	f.replayed = append(f.replayed, msg.Offset)
	return f.failAt[msg.Offset]
}

func (f *fakeHoldReplayer) ReloadHeld(consumer string, tenants []string) {
	if f.reloaded == nil {
		f.reloaded = map[string][]string{}
	}
	f.reloaded[consumer] = tenants
}

// newHoldDrain builds a drain over one TestDB and one replayer, with the
// intervals a test can reason about.
func newHoldDrain(db dbtypes.Interface, replayer streams.HoldReplayer) *HoldDrain {
	return &HoldDrain{
		storeFor: func(context.Context) (HoldStore, error) { return mustPostgresHoldStore(), nil },
		getDB:    func(context.Context) (dbtypes.Interface, error) { return db, nil },
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
	assert.Equal(t, map[string][]string{testHoldConsumer: nil}, replayer.reloaded,
		"the runners are told the tenant is no longer held")
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
	drain := newHoldDrain(db, replayer)

	require.NoError(t, drain.Execute(drainCtx(db)), "a deferred tenant is not a failed pass")

	assert.Equal(t, []int64{3, 4}, replayer.replayed, "offset 5 stays parked behind offset 4")
	assert.Equal(t, map[string][]string{testHoldConsumer: {testHoldTenant}}, replayer.reloaded,
		"and the tenant is still held")
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
	db.ExpectQuery(`ORDER BY stream, stream_offset`).WillReturnRows(heldRows(3, 4, 5))
	db.ExpectExec(`DELETE FROM ` + holdTable).WillReturnRowsAffected(1)
	db.ExpectExec(`DELETE FROM ` + holdTenantTable).WillReturnRowsAffected(1)
	db.ExpectQuery(`SELECT tenant_id FROM ` + holdTenantTable).WillReturnRows(
		dbtesting.NewRowSet("tenant_id").AddRow(testHoldTenant))
	db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
		dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(1), int64(3), time.Now()))

	// The panic hits the first replay, which belongs to the first due tenant.
	replayer := &fakeHoldReplayer{consumers: []string{testHoldConsumer}, panicAt: 3}
	drain := newHoldDrain(db, replayer)

	err := drain.Execute(drainCtx(db))

	require.Error(t, err)
	assert.Contains(t, err.Error(), testHoldTenant, "the panicking tenant is named")
	assert.Equal(t, []int64{3, 4, 5}, replayer.replayed,
		"the tenant behind the panic still drained, in order")
}
