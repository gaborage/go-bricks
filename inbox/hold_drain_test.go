package inbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/scheduler"
)

// fakeHoldJobCtx is the scheduler.JobContext the drain runs under. The embedded
// context satisfies the stdlib half; the rest is field-backed.
type fakeHoldJobCtx struct {
	context.Context
	log logger.Logger
	db  dbtypes.Interface
}

func (c *fakeHoldJobCtx) JobID() string                   { return holdDrainJobID }
func (c *fakeHoldJobCtx) TriggerType() string             { return "scheduled" }
func (c *fakeHoldJobCtx) Logger() logger.Logger           { return c.log }
func (c *fakeHoldJobCtx) DB() dbtypes.Interface           { return c.db }
func (c *fakeHoldJobCtx) Messaging() messaging.AMQPClient { return nil }
func (c *fakeHoldJobCtx) Config() *config.Config          { return nil }

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
	// reloadPanicFor names a consumer whose reload panics: a panic past the
	// drain's per-tenant recover, which only the scheduler catches.
	reloadPanicFor string
}

func (f *fakeHoldReplayer) HoldConsumers() []string { return f.consumers }

func (f *fakeHoldReplayer) Replay(_ context.Context, _ string, msg *app.HeldMessage) error {
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
	if consumer == f.reloadPanicFor {
		panic("reload exploded")
	}
	f.reloaded = append(f.reloaded, consumer)
	return f.reloadErr
}

// newHoldDrain builds a drain over one TestDB and one replayer, with the
// intervals a test can reason about.
func newHoldDrain(db dbtypes.Interface, replayer app.HoldReplayer) *HoldDrain {
	return &HoldDrain{
		resolve: func(context.Context) (dbtypes.Interface, HoldStore, error) {
			return db, mustPostgresHoldStore(), nil
		},
		replayer: func() app.HoldReplayer {
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
	// deferredBackoff is what Defer was told to wait, which is the only place the
	// computed attempt number is observable.
	deferredBackoff time.Duration
	deferErr        error
	deferRefused    bool
	releaseErr      error
	statsErr        error
	// rowsByTenant answers NextRows per tenant. A TestDB expectation is matched by
	// substring, so every tenant's read matches the same registration and cannot
	// carry a tenant's own rows; this can.
	rowsByTenant map[string][]HoldRow
	// contended names the tenants another replica holds the lease on, for the same
	// reason: one TestDB answer cannot lease one tenant and refuse the next.
	contended map[string]bool
	// The remaining knobs fail or fence one store call. A TestDB answer cannot:
	// DeleteRow's and Release's statements both match "DELETE FROM <hold table>".
	acquireErr      error
	nextRowsErr     error
	deleteErr       error
	deleteRefused   bool
	releaseRefused  bool
	releaseLeaseErr error
}

func (r *recordingHoldStore) AcquireLease(ctx context.Context, db dbtypes.Interface,
	consumer, tenant, owner string, lease time.Duration,
) (bool, error) {
	if r.acquireErr != nil {
		return false, r.acquireErr
	}
	if r.contended[tenant] {
		return false, nil
	}
	return r.HoldStore.AcquireLease(ctx, db, consumer, tenant, owner, lease)
}

func (r *recordingHoldStore) DeleteRow(ctx context.Context, db dbtypes.Interface,
	consumer, stream string, offset int64, tenant, owner string,
) (bool, error) {
	if r.deleteErr != nil {
		return false, r.deleteErr
	}
	if r.deleteRefused {
		return false, nil
	}
	return r.HoldStore.DeleteRow(ctx, db, consumer, stream, offset, tenant, owner)
}

func (r *recordingHoldStore) NextRows(ctx context.Context, db dbtypes.Interface,
	consumer, tenant string, limit int,
) ([]HoldRow, error) {
	if r.nextRowsErr != nil {
		return nil, r.nextRowsErr
	}
	if r.rowsByTenant == nil {
		return r.HoldStore.NextRows(ctx, db, consumer, tenant, limit)
	}
	return r.rowsByTenant[tenant], nil
}

func (r *recordingHoldStore) Release(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) (bool, error) {
	r.released++
	if r.releaseErr != nil {
		return false, r.releaseErr
	}
	if r.releaseRefused {
		return false, nil
	}
	return r.HoldStore.Release(ctx, db, consumer, tenant, owner)
}

func (r *recordingHoldStore) Defer(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string,
	backoff time.Duration, lastErr string,
) (bool, error) {
	r.deferredBackoff = backoff
	if r.deferErr != nil {
		return false, r.deferErr
	}
	if r.deferRefused {
		return false, nil
	}
	return r.HoldStore.Defer(ctx, db, consumer, tenant, owner, backoff, lastErr)
}

func (r *recordingHoldStore) Stats(ctx context.Context, db dbtypes.Interface, consumer string) (HoldStats, error) {
	if r.statsErr != nil {
		return HoldStats{}, r.statsErr
	}
	return r.HoldStore.Stats(ctx, db, consumer)
}

func (r *recordingHoldStore) ReleaseLease(ctx context.Context, db dbtypes.Interface, consumer, tenant, owner string) error {
	r.leasesYielded++
	if r.releaseLeaseErr != nil {
		return r.releaseLeaseErr
	}
	return r.HoldStore.ReleaseLease(ctx, db, consumer, tenant, owner)
}

func mustPostgresHoldStore() HoldStore {
	store, err := NewPostgresHoldStore(DefaultHoldTableName)
	if err != nil {
		panic(err)
	}
	return store
}

// fakeHoldSink stands in for the scheduler's Recorder. It fails the test on a
// call the drain's declarations do not allow — the Recorder would only mark the
// run failing as "undeclared", which no unit test sees — and tallies the rest per
// series, Count and Fail apart.
type fakeHoldSink struct {
	t      *testing.T
	counts map[string]int64
	fails  map[string]int64
}

func newFakeHoldSink(t *testing.T) *fakeHoldSink {
	return &fakeHoldSink{t: t, counts: map[string]int64{}, fails: map[string]int64{}}
}

func (s *fakeHoldSink) Count(name string, n int64, attrs ...attribute.KeyValue) {
	s.t.Helper()
	s.counts[s.declaredSeries(name, n, attrs)] += n
}

func (s *fakeHoldSink) Fail(name string, n int64, attrs ...attribute.KeyValue) {
	s.t.Helper()
	s.fails[s.declaredSeries(name, n, attrs)] += n
}

// declaredSeries names a call name{key=value}, after checking it against the Counters
// the drain registers: a declared name, a non-negative n, and each declared key
// once with one of its declared values.
func (s *fakeHoldSink) declaredSeries(name string, n int64, attrs []attribute.KeyValue) string {
	s.t.Helper()
	counters := holdDrainCounters()
	i := slices.IndexFunc(counters, func(c scheduler.Counter) bool { return c.Name == name })
	require.GreaterOrEqual(s.t, i, 0, "Counter %q is not declared", name)
	require.GreaterOrEqual(s.t, n, int64(0), "Counter %q takes a non-negative n", name)

	declared := counters[i].Attrs
	require.Len(s.t, attrs, len(declared), "Counter %q takes each declared key once", name)
	var series strings.Builder
	series.WriteString(name)
	for _, kv := range attrs {
		j := slices.IndexFunc(declared, func(a scheduler.AttrSet) bool { return a.Key == string(kv.Key) })
		require.GreaterOrEqual(s.t, j, 0, "Counter %q does not declare key %q", name, kv.Key)
		require.Equal(s.t, attribute.STRING, kv.Value.Type(), "Counter %q key %q takes a string", name, kv.Key)
		require.Contains(s.t, declared[j].Values, kv.Value.AsString(), "Counter %q key %q", name, kv.Key)
		fmt.Fprintf(&series, "{%s=%s}", kv.Key, kv.Value.AsString())
	}
	return series.String()
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

// TestDrainRunReportsEachTenantOutcome pins what a pass reports: the pass itself
// as active, and each due tenant once, by how its turn ended — a deferral as a
// Fail, a release and a tenant another replica holds as successes.
func TestDrainRunReportsEachTenantOutcome(t *testing.T) {
	const deferredTenant, contendedTenant = otherHoldTenant, "tenant-c"
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery(`next_attempt_at <= NOW()`).WillReturnRows(
		dueTenantRows(time.Now(), testHoldTenant, deferredTenant, contendedTenant))
	db.ExpectExec(`UPDATE ` + holdTenantTable).WillReturnRowsAffected(1) // AcquireLease, Defer
	db.ExpectExec(`DELETE FROM ` + holdTable).WillReturnRowsAffected(1)
	db.ExpectExec(`DELETE FROM ` + holdTenantTable).WillReturnRowsAffected(1) // Release
	db.ExpectQuery(`SELECT tenant_id FROM ` + holdTenantTable).WillReturnRows(
		dbtesting.NewRowSet("tenant_id").AddRow(deferredTenant))
	db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
		dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(1), int64(1), time.Now()))

	replayer := &fakeHoldReplayer{
		consumers: []string{testHoldConsumer},
		failAt:    map[int64]error{7: errors.New("handler said no")},
	}
	store := &recordingHoldStore{
		HoldStore: mustPostgresHoldStore(),
		rowsByTenant: map[string][]HoldRow{
			testHoldTenant: {{Consumer: testHoldConsumer, Stream: testHoldStream, Offset: 3, TenantID: testHoldTenant}},
			deferredTenant: {{Consumer: testHoldConsumer, Stream: testHoldStream, Offset: 7, TenantID: deferredTenant}},
		},
		contended: map[string]bool{contendedTenant: true},
	}
	sink := newFakeHoldSink(t)

	require.NoError(t, drainWithStore(db, replayer, store).run(drainCtx(db), sink))

	assert.Equal(t, map[string]int64{
		"inbox.hold.drain.passes{role=active}":        1,
		"inbox.hold.drain.tenants{outcome=released}":  1,
		"inbox.hold.drain.tenants{outcome=contended}": 1,
	}, sink.counts)
	assert.Equal(t, map[string]int64{"inbox.hold.drain.tenants{outcome=deferred}": 1}, sink.fails,
		"the deferral is the pass's only failing outcome")
}

// leaseSpentAtOnce makes the pass's lease run out before the first row: a lease
// no longer than the deadline's one-second margin leaves no time to replay in.
func leaseSpentAtOnce(d *HoldDrain) {
	now := time.Now()
	d.now = func() time.Time { return now }
	d.cfg.LeaseDuration = time.Second
}

// fullBatch is one batch of held rows for testHoldTenant, the most one read returns.
func fullBatch() []HoldRow {
	rows := make([]HoldRow, holdDrainRowsPerRead)
	for i := range rows {
		rows[i] = HoldRow{Consumer: testHoldConsumer, Stream: testHoldStream, Offset: int64(i + 1), TenantID: testHoldTenant}
	}
	return rows
}

// TestDrainRunReportsOneOutcomePerTerminalBranch pins the outcome of every way a
// tenant's turn can end, one tenant per case: exactly one outcome, Count or Fail
// as its kind says, and none at all when the turn ends in an error the run
// returns instead.
func TestDrainRunReportsOneOutcomePerTerminalBranch(t *testing.T) {
	const activePass = "inbox.hold.drain.passes{role=active}"
	for _, tc := range []struct {
		name  string
		setup func(*recordingHoldStore, *fakeHoldReplayer, *HoldDrain)
		// count and fail name the outcome the tenant must record; wantErr the error
		// a turn that records none returns.
		count, fail string
		wantErr     string
	}{
		{name: "released", setup: func(*recordingHoldStore, *fakeHoldReplayer, *HoldDrain) {}, count: "released"},
		{
			name:  "yielded_when_the_lease_is_spent",
			setup: func(_ *recordingHoldStore, _ *fakeHoldReplayer, d *HoldDrain) { leaseSpentAtOnce(d) },
			count: "yielded",
		},
		{
			name: "yielded_after_a_full_batch",
			setup: func(s *recordingHoldStore, _ *fakeHoldReplayer, _ *HoldDrain) {
				s.rowsByTenant = map[string][]HoldRow{testHoldTenant: fullBatch()}
			},
			count: "yielded",
		},
		{
			// A lost lease and a row a concurrent park added both refuse the release,
			// and the drain cannot tell them apart: normal traffic, not a failing run.
			name:  "yielded_when_the_release_is_refused",
			setup: func(s *recordingHoldStore, _ *fakeHoldReplayer, _ *HoldDrain) { s.releaseRefused = true },
			count: "yielded",
		},
		{
			name: "contended",
			setup: func(s *recordingHoldStore, _ *fakeHoldReplayer, _ *HoldDrain) {
				s.contended = map[string]bool{testHoldTenant: true}
			},
			count: "contended",
		},
		{
			name: "deferred_after_a_replay_error",
			setup: func(_ *recordingHoldStore, r *fakeHoldReplayer, _ *HoldDrain) {
				r.failAt = map[int64]error{3: errors.New("handler said no")}
			},
			fail: "deferred",
		},
		{
			name: "deferred_on_an_undecodable_row",
			setup: func(s *recordingHoldStore, _ *fakeHoldReplayer, _ *HoldDrain) {
				s.rowsByTenant = map[string][]HoldRow{testHoldTenant: {{
					Consumer: testHoldConsumer, Stream: testHoldStream, Offset: 3, TenantID: testHoldTenant,
					Properties: []byte(`{"broken`),
				}}}
			},
			fail: "deferred",
		},
		{
			name:  "lease_lost_when_the_delete_is_fenced",
			setup: func(s *recordingHoldStore, _ *fakeHoldReplayer, _ *HoldDrain) { s.deleteRefused = true },
			fail:  "lease_lost",
		},
		{
			name: "lease_lost_when_the_defer_is_fenced",
			setup: func(s *recordingHoldStore, r *fakeHoldReplayer, _ *HoldDrain) {
				s.deferRefused = true
				r.failAt = map[int64]error{3: errors.New("handler said no")}
			},
			fail: "lease_lost",
		},
		{
			name: "no_outcome_when_the_resolve_fails",
			setup: func(_ *recordingHoldStore, _ *fakeHoldReplayer, d *HoldDrain) {
				d.resolve = func(context.Context) (dbtypes.Interface, HoldStore, error) {
					return nil, nil, errors.New("resolve exploded")
				}
			},
			wantErr: "resolve exploded",
		},
		{
			name: "no_outcome_when_the_lease_cannot_be_taken",
			setup: func(s *recordingHoldStore, _ *fakeHoldReplayer, _ *HoldDrain) {
				s.acquireErr = errors.New("acquire exploded")
			},
			wantErr: "acquire exploded",
		},
		{
			name: "no_outcome_when_the_rows_cannot_be_read",
			setup: func(s *recordingHoldStore, _ *fakeHoldReplayer, _ *HoldDrain) {
				s.nextRowsErr = errors.New("rows exploded")
			},
			wantErr: "rows exploded",
		},
		{
			name: "no_outcome_when_the_delete_fails",
			setup: func(s *recordingHoldStore, _ *fakeHoldReplayer, _ *HoldDrain) {
				s.deleteErr = errors.New("delete exploded")
			},
			wantErr: "delete exploded",
		},
		{
			name: "no_outcome_when_the_defer_fails",
			setup: func(s *recordingHoldStore, r *fakeHoldReplayer, _ *HoldDrain) {
				s.deferErr = errors.New("defer exploded")
				r.failAt = map[int64]error{3: errors.New("handler said no")}
			},
			wantErr: "defer exploded",
		},
		{
			name: "no_outcome_when_the_release_fails",
			setup: func(s *recordingHoldStore, _ *fakeHoldReplayer, _ *HoldDrain) {
				s.releaseErr = errors.New("release exploded")
			},
			wantErr: "release exploded",
		},
		{
			name: "no_outcome_when_a_spent_lease_cannot_be_handed_back",
			setup: func(s *recordingHoldStore, _ *fakeHoldReplayer, d *HoldDrain) {
				leaseSpentAtOnce(d)
				s.releaseLeaseErr = errors.New("handback exploded")
			},
			wantErr: "handback exploded",
		},
		{
			name: "no_outcome_when_a_full_batch_cannot_hand_the_lease_back",
			setup: func(s *recordingHoldStore, _ *fakeHoldReplayer, _ *HoldDrain) {
				s.rowsByTenant = map[string][]HoldRow{testHoldTenant: fullBatch()}
				s.releaseLeaseErr = errors.New("handback exploded")
			},
			wantErr: "handback exploded",
		},
		{
			name:    "no_outcome_when_the_replay_panics",
			setup:   func(_ *recordingHoldStore, r *fakeHoldReplayer, _ *HoldDrain) { r.panicAt = 3 },
			wantErr: "panic (type: string)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := dueTenantDB(t)
			replayer := &fakeHoldReplayer{consumers: []string{testHoldConsumer}}
			store := &recordingHoldStore{HoldStore: mustPostgresHoldStore()}
			drain := drainWithStore(db, replayer, store)
			tc.setup(store, replayer, drain)
			sink := newFakeHoldSink(t)

			err := drain.run(drainCtx(db), sink)

			wantCounts := map[string]int64{activePass: 1}
			wantFails := map[string]int64{}
			if tc.count != "" {
				wantCounts["inbox.hold.drain.tenants{outcome="+tc.count+"}"] = 1
			}
			if tc.fail != "" {
				wantFails["inbox.hold.drain.tenants{outcome="+tc.fail+"}"] = 1
			}
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			} else {
				require.NoError(t, err, "an outcome, failing or not, is not a run error")
			}
			assert.Equal(t, wantCounts, sink.counts)
			assert.Equal(t, wantFails, sink.fails)
		})
	}
}

// TestDrainRunOnStandbyReportsOnlyTheStandbyPass pins the replica with no stream
// consumers: its pass is counted as standby, and it reports nothing else.
func TestDrainRunOnStandbyReportsOnlyTheStandbyPass(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	sink := newFakeHoldSink(t)

	require.NoError(t, newHoldDrain(db, nil).run(drainCtx(db), sink))

	assert.Equal(t, map[string]int64{"inbox.hold.drain.passes{role=standby}": 1}, sink.counts)
	assert.Empty(t, sink.fails)
}

// TestDrainRunKeepsAConsumersOutcomesWhenALaterOnePanics pins when outcomes are
// reported: as each consumer's pass returns, so a panic in a later consumer —
// one the drain does not recover, past its tenants — cannot take the earlier
// consumer's with it.
func TestDrainRunKeepsAConsumersOutcomesWhenALaterOnePanics(t *testing.T) {
	db := dueTenantDB(t)
	replayer := &fakeHoldReplayer{consumers: []string{testHoldConsumer, "audit-processor"}, reloadPanicFor: "audit-processor"}
	sink := newFakeHoldSink(t)

	assert.PanicsWithValue(t, "reload exploded", func() {
		_ = newHoldDrain(db, replayer).run(drainCtx(db), sink)
	})

	assert.Equal(t, map[string]int64{
		"inbox.hold.drain.passes{role=active}":       1,
		"inbox.hold.drain.tenants{outcome=released}": 1,
	}, sink.counts, "the first consumer's release survives the second consumer's panic")
	assert.Empty(t, sink.fails)
}

// TestHoldDrainCountersRegisterWithTheScheduler pins the drain's Counter
// declarations against the scheduler that enforces them: a name or value it
// refuses fails here, not at a deployment's startup.
func TestHoldDrainCountersRegisterWithTheScheduler(t *testing.T) {
	sched := scheduler.NewModule()
	require.NoError(t, sched.Init(&app.ModuleDeps{
		Logger: logger.New("error", false),
		Config: &config.Config{Scheduler: config.SchedulerConfig{
			Timezone: "UTC",
			Timeout:  config.SchedulerTimeoutConfig{Shutdown: time.Second, SlowJob: time.Second},
			Summary:  config.SchedulerSummaryConfig{Window: 5 * time.Minute},
		}},
	}))
	t.Cleanup(func() { assert.NoError(t, sched.Shutdown()) })

	drain := newHoldDrain(nil, nil)
	job := scheduler.Windowed(func(jc scheduler.JobContext, rec *scheduler.Recorder) error {
		return drain.run(jc, rec)
	}, holdDrainCounters()...)

	// Never due inside the test, so the job only registers.
	require.NoError(t, sched.FixedRate(holdDrainJobID, job, time.Hour))
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

// captureDrainLogs returns everything the framework logger wrote while fn ran.
// The logger writes to os.Stdout directly, so this is how a WARN the drain emits
// on a threshold is observed.
func captureDrainLogs(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer func() { os.Stdout = original }()
	defer r.Close()
	os.Stdout = w

	fn()

	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
}

// TestBackoffForDoublesFromTheDrainInterval pins the deferred tenant's schedule
// at every attempt, including the two boundaries: the first attempt waits the
// drain interval, and the series stops at the cap instead of running past it.
func TestBackoffForDoublesFromTheDrainInterval(t *testing.T) {
	drain := &HoldDrain{cfg: config.InboxHoldConfig{DrainInterval: 5 * time.Second, MaxBackoff: time.Minute}}

	for _, tc := range []struct {
		name     string
		attempts int
		want     time.Duration
	}{
		{"first_attempt_waits_one_interval", 1, 5 * time.Second},
		{"second_doubles", 2, 10 * time.Second},
		{"third_doubles_again", 3, 20 * time.Second},
		{"fourth_doubles_again", 4, 40 * time.Second},
		{"fifth_is_capped", 5, time.Minute},
		{"and_stays_capped", 9, time.Minute},
		// A tenant that has been failing for days: the shift that computes the wait
		// must saturate at the cap rather than wrap into a nonsense duration.
		{"an_attempt_count_that_overflows_the_shift_is_capped", 64, time.Minute},
		{"and_one_far_past_it", 4000, time.Minute},
		// Guards the clamp: nothing below the first attempt reaches Saturating as a
		// negative shift, which would wait nothing.
		{"a_zero_attempt_count_waits_one_interval", 0, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, drain.backoffFor(tc.attempts))
		})
	}
}

// TestBackoffForSaturatesAWrappingShiftAtTheCap pins the cap for an interval
// whose overflowing shift wraps to a small positive value instead of going
// non-positive: 2^40+1ns shifted 36 wraps to 2^36ns (~1m8s), not the cap.
func TestBackoffForSaturatesAWrappingShiftAtTheCap(t *testing.T) {
	drain := &HoldDrain{cfg: config.InboxHoldConfig{DrainInterval: 1<<40 + 1, MaxBackoff: 168 * time.Hour}}

	for _, tc := range []struct {
		name     string
		attempts int
	}{
		{"shift_36_wraps_positive", 37},
		{"shift_40_wraps_positive", 41},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, 168*time.Hour, drain.backoffFor(tc.attempts))
		})
	}
}

// TestDrainDefersOnTheNextAttemptNumber pins that a deferred tenant is backed off
// by the attempt it is ABOUT to make, not the one it already made: a tenant with
// two attempts behind it waits the third attempt's interval.
func TestDrainDefersOnTheNextAttemptNumber(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery(`next_attempt_at <= NOW()`).WillReturnRows(
		dbtesting.NewRowSet("consumer", "tenant_id", "held_since", "attempts", "next_attempt_at", "last_error").
			AddRow(testHoldConsumer, testHoldTenant, time.Now(), 2, time.Now(), ""))
	db.ExpectExec(`UPDATE ` + holdTenantTable).WillReturnRowsAffected(1)
	db.ExpectQuery(`ORDER BY stream, stream_offset`).WillReturnRows(heldRows(3))
	db.ExpectQuery(`SELECT tenant_id FROM ` + holdTenantTable).WillReturnRows(
		dbtesting.NewRowSet("tenant_id").AddRow(testHoldTenant))
	db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
		dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(1), int64(1), time.Now()))

	replayer := &fakeHoldReplayer{
		consumers: []string{testHoldConsumer},
		failAt:    map[int64]error{3: errors.New("still failing")},
	}
	store := &recordingHoldStore{HoldStore: mustPostgresHoldStore()}
	drain := newHoldDrain(db, replayer)
	drain.resolve = func(context.Context) (dbtypes.Interface, HoldStore, error) { return db, store, nil }

	require.NoError(t, drain.Execute(drainCtx(db)))

	// Two attempts behind it, so the third: 5s doubled twice.
	assert.Equal(t, 20*time.Second, store.deferredBackoff,
		"the backoff is the NEXT attempt's, not the last one's")
}

// TestWarnIfTooOldFiresOnlyPastTheThreshold pins both sides of the max-age
// boundary: a tenant held exactly the configured age is not yet stuck, and one
// held longer earns the line an operator watches for.
func TestWarnIfTooOldFiresOnlyPastTheThreshold(t *testing.T) {
	now := time.Now()
	drain := &HoldDrain{cfg: config.InboxHoldConfig{MaxAge: time.Hour}, now: func() time.Time { return now }}

	atThreshold := captureDrainLogs(t, func() {
		drain.warnIfTooOld(logger.New("warn", false), &holdPass{consumer: testHoldConsumer}, &HoldTenant{
			TenantID: testHoldTenant, HeldSince: now.Add(-time.Hour),
		})
	})
	assert.NotContains(t, atThreshold, "Hold exceeds max age", "held exactly the max age is not past it")

	pastThreshold := captureDrainLogs(t, func() {
		drain.warnIfTooOld(logger.New("warn", false), &holdPass{consumer: testHoldConsumer}, &HoldTenant{
			TenantID: testHoldTenant, HeldSince: now.Add(-time.Hour - time.Second),
		})
	})
	assert.Contains(t, pastThreshold, "Hold exceeds max age", "a second past it warns")
	assert.Contains(t, pastThreshold, testHoldTenant, "and names the tenant")
}

// TestOwnerIDFallsBackWhenItsSourcesFail pins the two fallbacks in the lease
// owner. Neither is reachable through the real sources, and both matter: an
// owner that collides is two replicas believing they hold one tenant.
func TestOwnerIDFallsBackWhenItsSourcesFail(t *testing.T) {
	okHost := func() (string, error) { return "host-a", nil }
	badHost := func() (string, error) { return "", errors.New("no hostname") }
	okRandom := func(b []byte) (int, error) { return len(b), nil }
	badRandom := func([]byte) (int, error) { return 0, errors.New("no entropy") }

	full := ownerIDFrom(okHost, okRandom)
	assert.Contains(t, full, "host-a")
	assert.Len(t, strings.Split(full, "/"), 3, "host, pid and randomness")

	assert.Contains(t, ownerIDFrom(badHost, okRandom), "unknown-host",
		"a host that cannot name itself still yields an owner")

	degraded := ownerIDFrom(okHost, badRandom)
	assert.Contains(t, degraded, "host-a")
	assert.Len(t, strings.Split(degraded, "/"), 2, "without entropy the owner is host and pid only")
}

// drainWithStore wires a drain over one TestDB, replayer and store, for the
// error-path tests below.
func drainWithStore(db dbtypes.Interface, replayer app.HoldReplayer, store HoldStore) *HoldDrain {
	drain := newHoldDrain(db, replayer)
	drain.resolve = func(context.Context) (dbtypes.Interface, HoldStore, error) { return db, store, nil }
	return drain
}

// dueTenantDB is the fixture every error-path test starts from: one due tenant
// with one row, leased successfully.
func dueTenantDB(t *testing.T) *dbtesting.TestDB {
	t.Helper()
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery(`next_attempt_at <= NOW()`).WillReturnRows(dueTenantRows(time.Now(), testHoldTenant))
	db.ExpectExec(`UPDATE ` + holdTenantTable).WillReturnRowsAffected(1)
	db.ExpectQuery(`ORDER BY stream, stream_offset`).WillReturnRows(heldRows(3))
	db.ExpectExec(`DELETE FROM ` + holdTable).WillReturnRowsAffected(1)
	db.ExpectExec(`DELETE FROM ` + holdTenantTable).WillReturnRowsAffected(1)
	db.ExpectQuery(`SELECT tenant_id FROM ` + holdTenantTable).WillReturnRows(dbtesting.NewRowSet("tenant_id"))
	db.ExpectQuery(`SELECT COUNT`).WillReturnRows(
		dbtesting.NewRowSet("tenants", "rows", "oldest").AddRow(int64(0), int64(0), nil))
	return db
}

// TestDrainReportsEveryLedgerFailure pins that no ledger error is swallowed. A
// pass that hides one reports success while the backlog stops moving, which is
// the failure mode a drain must never have: silent.
func TestDrainReportsEveryLedgerFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		breaks  func(*recordingHoldStore, *fakeHoldReplayer)
		wantErr string
	}{
		{
			name: "a_failed_reload_is_reported",
			breaks: func(_ *recordingHoldStore, r *fakeHoldReplayer) {
				r.reloadErr = errors.New("reload exploded")
			},
			wantErr: "reload exploded",
		},
		{
			name: "a_failed_stats_read_is_reported",
			breaks: func(s *recordingHoldStore, _ *fakeHoldReplayer) {
				s.statsErr = errors.New("stats exploded")
			},
			wantErr: "stats exploded",
		},
		{
			name: "a_failed_release_is_reported",
			breaks: func(s *recordingHoldStore, _ *fakeHoldReplayer) {
				s.releaseErr = errors.New("release exploded")
			},
			wantErr: "release exploded",
		},
		{
			name: "a_failed_defer_is_reported",
			breaks: func(s *recordingHoldStore, r *fakeHoldReplayer) {
				s.deferErr = errors.New("defer exploded")
				r.failAt = map[int64]error{3: errors.New("handler said no")}
			},
			wantErr: "defer exploded",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := dueTenantDB(t)
			replayer := &fakeHoldReplayer{consumers: []string{testHoldConsumer}}
			store := &recordingHoldStore{HoldStore: mustPostgresHoldStore()}
			tc.breaks(store, replayer)

			err := drainWithStore(db, replayer, store).Execute(drainCtx(db))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestLeaseDeadlineStaysInsideTheLease pins the safety margin. The lease expires
// on the DATABASE's clock, and this process can only sample its own after the
// acquire round trip has already spent part of it, so a deadline at the full
// duration lands past the real expiry — where a second replica may already hold
// the tenant.
func TestLeaseDeadlineStaysInsideTheLease(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name  string
		lease time.Duration
		want  time.Duration
	}{
		{"a_minute_lease_keeps_six_seconds_back", time.Minute, 54 * time.Second},
		{"a_ten_second_lease_keeps_one", 10 * time.Second, 9 * time.Second},
		// The margin has a floor: a tenth of a very short lease would not cover a
		// round trip at all.
		{"a_short_lease_keeps_the_floor", 2 * time.Second, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drain := &HoldDrain{
				cfg: config.InboxHoldConfig{LeaseDuration: tc.lease},
				now: func() time.Time { return now },
			}

			deadline := drain.leaseDeadline()

			assert.Equal(t, now.Add(tc.want), deadline)
			assert.True(t, deadline.Before(now.Add(tc.lease)),
				"the deadline is inside the lease, not at its edge")
		})
	}
}

// TestDrainDoesNotReportABackoffItNeverWrote pins the defer's fence: a replica
// that lost the lease mid-pass writes nothing, so logging the backoff it computed
// would describe a schedule the ledger does not have.
func TestDrainDoesNotReportABackoffItNeverWrote(t *testing.T) {
	db := dueTenantDB(t)
	replayer := &fakeHoldReplayer{
		consumers: []string{testHoldConsumer},
		failAt:    map[int64]error{3: errors.New("handler said no")},
	}
	store := &recordingHoldStore{HoldStore: mustPostgresHoldStore(), deferRefused: true}

	out := captureDrainLogs(t, func() {
		// Built inside the capture and at WARN: zerolog binds its writer when the
		// logger is made, and drainCtx's own logger is quieter than this line.
		jobCtx := &fakeHoldJobCtx{Context: context.Background(), log: logger.New("warn", false), db: db}
		require.NoError(t, drainWithStore(db, replayer, store).Execute(jobCtx))
	})

	assert.Contains(t, out, "Hold lease lost before the tenant could be deferred")
	assert.NotContains(t, out, "tenant deferred", "no schedule is claimed that was not written")
	assert.NotContains(t, out, "next_attempt_in", "and no backoff is reported")
}
