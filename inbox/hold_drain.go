package inbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/backoff"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/scheduler"
)

// holdDrainJobID is the scheduled job that replays held messages.
const holdDrainJobID = "inbox-hold-drain"

// holdDrainTenantsPerPass and holdDrainRowsPerRead bound one pass, so a large
// hold is drained across passes instead of in one long job run.
const (
	holdDrainTenantsPerPass = 100
	holdDrainRowsPerRead    = 50
)

// The drain's Counters: the scheduler reports them on its window line and as
// metrics. None carries a tenant or consumer attribute — both are unbounded sets
// at runtime, so they stay values on the event lines.
const (
	holdDrainPassesCounter     = "inbox.hold.drain.passes"
	holdDrainTenantsCounter    = "inbox.hold.drain.tenants"
	holdDrainOverMaxAgeCounter = "inbox.hold.drain.over_max_age"

	holdRoleKey     = "role"
	holdRoleActive  = "active"
	holdRoleStandby = "standby"
	holdOutcomeKey  = "outcome"
)

// holdOutcome is how one due tenant's turn in a pass ended. The zero value is no
// outcome: a turn that ended in an error reports the error, not an outcome.
type holdOutcome int

const (
	holdOutcomeNone holdOutcome = iota
	holdOutcomeReleased
	holdOutcomeYielded
	holdOutcomeContended
	holdOutcomeDeferred
	holdOutcomeLeaseLost
)

// holdOutcomes names each outcome on the tenants Counter and whether it fails
// the run.
var holdOutcomes = [...]struct {
	value string
	fails bool
}{
	holdOutcomeReleased:  {"released", false},
	holdOutcomeYielded:   {"yielded", false},
	holdOutcomeContended: {"contended", false},
	holdOutcomeDeferred:  {"deferred", true},
	holdOutcomeLeaseLost: {"lease_lost", true},
}

// holdDrainCounters declares the Counters the drain's run reports against. The
// scheduler refuses a call they do not allow, so every call names these.
func holdDrainCounters() []scheduler.Counter {
	outcomes := make([]string, 0, len(holdOutcomes))
	for _, o := range holdOutcomes[holdOutcomeReleased:] {
		outcomes = append(outcomes, o.value)
	}
	return []scheduler.Counter{
		{Name: holdDrainPassesCounter, Attrs: []scheduler.AttrSet{{Key: holdRoleKey, Values: []string{holdRoleActive, holdRoleStandby}}}},
		{Name: holdDrainTenantsCounter, Attrs: []scheduler.AttrSet{{Key: holdOutcomeKey, Values: outcomes}}},
		{Name: holdDrainOverMaxAgeCounter},
	}
}

// holdSink is what a pass reports its outcomes to: the scheduler's Recorder in
// production.
type holdSink interface {
	Count(name string, n int64, attrs ...attribute.KeyValue)
	Fail(name string, n int64, attrs ...attribute.KeyValue)
}

// HoldDrain replays held messages, tenant by tenant, in the order they were
// parked. One pass leases a tenant, replays its rows oldest-first, and either
// releases the tenant when nothing remains or defers it behind a backoff.
//
// Replays go through the streams lane, so the drain runs where the consumers do;
// the ledger is the control-plane database, so every replica sees the same holds
// and the lease is what stops them replaying the same tenant at once.
type HoldDrain struct {
	// resolve hands back the pass's control-plane database and its store together,
	// on first use: the vendor is not known until a connection is, the same reason
	// the inbox ledger's store is lazy. One resolver rather than two, because the
	// ledger's own calls already take the pair from a single seam.
	resolve  func(context.Context) (dbtypes.Interface, HoldStore, error)
	replayer func() app.HoldReplayer
	cfg      config.InboxHoldConfig
	// owner identifies this replica in the lease. Minted once, because a lease
	// taken under one name must be renewable and releasable under the same one.
	owner string

	// stats is the snapshot the gauges publish, per consumer. The map is guarded:
	// an observable-gauge callback reads it on the exporter's schedule, which is
	// not this goroutine.
	statsMu sync.Mutex
	stats   map[string]*HoldStats

	// now is the Go clock: the ages the drain reports, the deadline it replays
	// under, and the summary window it is in. Every decision the ledger makes
	// uses database time.
	now func() time.Time

	// window is scheduler.summary.window. The over-age WARN is written at most
	// once per window, in the scheduler's UTC-aligned windows.
	window time.Duration
	// warnedWindow is the index of the window the last over-age WARN was written
	// in, math.MinInt64 before the first. Runs never overlap — the scheduler skips
	// a trigger while the job runs — so it needs no lock.
	warnedWindow int64
}

// holdPass is one consumer's pass: the control-plane database, the store that
// speaks its dialect, the runners to replay through and whose consumer it is —
// settled once rather than threaded through every step separately.
type holdPass struct {
	db       dbtypes.Interface
	store    HoldStore
	replayer app.HoldReplayer
	consumer string

	// outcomes counts the pass's tenants by how their turn ended, and overAge the
	// leased tenants it found held past maxage; run reports both.
	outcomes [len(holdOutcomes)]int64
	overAge  overAgeTally
}

// overAgeTally counts the leased tenants found held past maxage, and keeps the
// oldest of them for the WARN.
type overAgeTally struct {
	count    int64
	consumer string
	tenant   string
	heldFor  time.Duration
	attempts int
}

// merge adds other's sightings, keeping whichever oldest tenant is older.
func (t *overAgeTally) merge(other *overAgeTally) {
	t.count += other.count
	if other.heldFor > t.heldFor {
		t.consumer, t.tenant, t.heldFor, t.attempts = other.consumer, other.tenant, other.heldFor, other.attempts
	}
}

// heldTenant is one tenant's turn under a lease: the ledger's row for it, and the
// instant this replica must stop working on it. The two are inseparable — the
// deadline means nothing without the tenant whose lease granted it.
type heldTenant struct {
	tenant   *HoldTenant
	deadline time.Time
}

// Run is the drain's scheduled job: one pass over every holding consumer, its
// outcomes reported to the scheduler's Recorder.
func (d *HoldDrain) Run(jc scheduler.JobContext, rec *scheduler.Recorder) error {
	return d.run(jc, rec)
}

// run is one pass. Each consumer's outcomes reach sink as soon as its pass
// returns, so a later consumer's panic keeps them; a panic in a consumer's own
// reload or snapshot, after its tenants drained, loses that consumer's.
func (d *HoldDrain) run(jc scheduler.JobContext, sink holdSink) error {
	log := jc.Logger()

	replayer := d.replayer()
	if replayer == nil {
		// The ledger is configured but no stream consumer runs here — another
		// replica's drain owns these holds.
		log.Debug().Msg("Hold drain skipped: no stream consumers on this replica")
		sink.Count(holdDrainPassesCounter, 1, attribute.String(holdRoleKey, holdRoleStandby))
		return nil
	}
	sink.Count(holdDrainPassesCounter, 1, attribute.String(holdRoleKey, holdRoleActive))

	db, store, err := d.resolve(jc)
	if err != nil {
		return err
	}

	var errs []error
	var overAge overAgeTally
	for _, consumer := range replayer.HoldConsumers() {
		pass := &holdPass{db: db, store: store, replayer: replayer, consumer: consumer}
		if err := d.drainConsumer(jc, log, pass); err != nil {
			errs = append(errs, err)
		}

		for outcome := holdOutcomeReleased; outcome < holdOutcome(len(holdOutcomes)); outcome++ {
			n := pass.outcomes[outcome]
			if n == 0 {
				continue
			}
			attr := attribute.String(holdOutcomeKey, holdOutcomes[outcome].value)
			if holdOutcomes[outcome].fails {
				sink.Fail(holdDrainTenantsCounter, n, attr)
			} else {
				sink.Count(holdDrainTenantsCounter, n, attr)
			}
		}
		if pass.overAge.count > 0 {
			sink.Fail(holdDrainOverMaxAgeCounter, pass.overAge.count)
		}
		overAge.merge(&pass.overAge)
	}

	if overAge.count > 0 && d.overAgeWarnDue() {
		log.Warn().
			Str("consumer", overAge.consumer).
			Str("tenant", overAge.tenant).
			Dur("held_for", overAge.heldFor).
			Int("attempts", overAge.attempts).
			Int64("over_max_age_tenants", overAge.count).
			Msg("Hold exceeds max age")
	}
	return errors.Join(errs...)
}

// overAgeWarnDue reports whether the over-age WARN is due: the first pass in a
// summary window to find a tenant over age claims the window and writes it. A
// drain without a window — never registered with the scheduler, which refuses
// one — writes it on every such pass rather than dividing by zero.
func (d *HoldDrain) overAgeWarnDue() bool {
	if d.window <= 0 {
		return true
	}
	window := d.now().UnixNano() / int64(d.window)
	if window == d.warnedWindow {
		return false
	}
	d.warnedWindow = window
	return true
}

// drainConsumer runs one consumer's pass and then tells its runners what the
// ledger still holds — the reload every replica depends on to learn about
// releases another replica made.
func (d *HoldDrain) drainConsumer(ctx context.Context, log logger.Logger, pass *holdPass) error {
	due, err := pass.store.DueTenants(ctx, pass.db, pass.consumer, holdDrainTenantsPerPass)
	if err != nil {
		return err
	}

	var errs []error
	for i := range due {
		outcome, err := d.drainTenant(ctx, log, pass, &due[i])
		if err != nil {
			errs = append(errs, err)
		}
		pass.outcomes[outcome]++
	}

	// Reload and snapshot even when a tenant failed: the set and the gauges
	// describe the ledger, not this pass.
	if err := d.reloadAndSnapshot(ctx, pass); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// drainTenant replays one tenant's rows under a lease and reports how its turn
// ended. A panic in a replay is contained here: the tenant that caused it is
// reported by TYPE (ADR-081), with no outcome, and the pass continues with the
// next one.
func (d *HoldDrain) drainTenant(ctx context.Context, log logger.Logger, pass *holdPass,
	tenant *HoldTenant,
) (outcome holdOutcome, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome, err = holdOutcomeNone, fmt.Errorf("inbox hold drain: tenant %q: panic (type: %T)", tenant.TenantID, recovered)
		}
	}()

	taken, err := pass.store.AcquireLease(ctx, pass.db, pass.consumer, tenant.TenantID, d.owner, d.cfg.LeaseDuration)
	if err != nil {
		return holdOutcomeNone, err
	}
	if !taken {
		// Another replica is replaying this tenant; its rows are not ours to touch.
		return holdOutcomeContended, nil
	}

	d.tallyIfTooOld(pass, tenant)
	return d.replayTenantRows(ctx, log, pass, &heldTenant{tenant: tenant, deadline: d.leaseDeadline()})
}

// leaseDeadline is how long this replica may keep replaying the tenant it just
// leased. The lease is what makes this replica the tenant's only drainer, so it
// is also the bound on the work: past its expiry another replica may take the
// tenant, and two handlers replaying one tenant's rows is the ordering guarantee
// gone.
//
// The expiry itself is the DATABASE's — `lease_until` was computed from the
// ledger's clock — and this process can only sample its own, after the acquire
// round trip has already spent part of the lease. A local now plus the full
// duration therefore lands AFTER the real expiry by the round trip plus whatever
// the two clocks disagree by. The margin below buys back both, so the deadline
// stays inside the lease rather than just past it.
func (d *HoldDrain) leaseDeadline() time.Time {
	margin := max(d.cfg.LeaseDuration/10, time.Second)
	return d.now().Add(d.cfg.LeaseDuration - margin)
}

// replayTenantRows replays at most one batch of a tenant's rows, in ledger order.
// One batch per pass, deliberately: a tenant with thousands of held rows must not
// own the drain for one long job run, and the next pass picks up where this one
// stopped. A short batch means the tenant is drained, so it is released.
func (d *HoldDrain) replayTenantRows(ctx context.Context, log logger.Logger, pass *holdPass,
	held *heldTenant,
) (holdOutcome, error) {
	tenant := held.tenant
	rows, err := pass.store.NextRows(ctx, pass.db, pass.consumer, tenant.TenantID, holdDrainRowsPerRead)
	if err != nil {
		return holdOutcomeNone, err
	}
	if len(rows) == 0 {
		return d.releaseTenant(ctx, log, pass, tenant)
	}

	for i := range rows {
		if !d.now().Before(held.deadline) {
			// The lease ran out with rows still held. Stopping is not a failure — the
			// tenant keeps its place and its backoff — so hand the lease back rather
			// than making every other replica wait for it to expire.
			log.Debug().
				Str("consumer", pass.consumer).
				Str("tenant", tenant.TenantID).
				Int("replayed", i).
				Msg("Hold lease spent mid-batch; the rest stays for the next pass")
			return d.yieldLease(ctx, pass, tenant)
		}

		outcome, err := d.replayRow(ctx, log, pass, held, &rows[i])
		if err != nil || outcome != holdOutcomeNone {
			return outcome, err
		}
	}

	if len(rows) < holdDrainRowsPerRead {
		// The batch was not full, so nothing is left behind it.
		return d.releaseTenant(ctx, log, pass, tenant)
	}
	// A full batch leaves rows behind. The next batch is any replica's to take, so
	// the lease goes back now instead of idling until it expires.
	return d.yieldLease(ctx, pass, tenant)
}

// yieldLease hands the tenant back while it is still held, so the next pass — on
// this replica or another — starts immediately rather than after the lease runs
// out.
func (d *HoldDrain) yieldLease(ctx context.Context, pass *holdPass, tenant *HoldTenant) (holdOutcome, error) {
	if err := pass.store.ReleaseLease(ctx, pass.db, pass.consumer, tenant.TenantID, d.owner); err != nil {
		return holdOutcomeNone, err
	}
	return holdOutcomeYielded, nil
}

// replayRow replays one row. No outcome means the row is done and the tenant may
// continue; any outcome ends the tenant's turn. A failure defers the tenant:
// everything behind this row stays parked, which is the order the hold exists to
// keep.
func (d *HoldDrain) replayRow(ctx context.Context, log logger.Logger, pass *holdPass,
	held *heldTenant, row *HoldRow,
) (holdOutcome, error) {
	tenant := held.tenant
	msg, err := heldMessageOf(row)
	if err != nil {
		// The row is unreadable, which no retry fixes; deferring keeps it — and the
		// tenant's order — while the WARN names the row an operator has to look at.
		return d.deferTenant(ctx, log, pass, tenant, row, err)
	}

	// The handler runs under the lease, not the job: a replay still running after
	// the lease expired is a second drainer's tenant being replayed twice.
	replayCtx, cancel := context.WithDeadline(ctx, held.deadline)
	err = pass.replayer.Replay(replayCtx, pass.consumer, msg)
	cancel()
	if err != nil {
		return d.deferTenant(ctx, log, pass, tenant, row, err)
	}

	deleted, err := pass.store.DeleteRow(ctx, pass.db, pass.consumer, row.Stream, row.Offset, tenant.TenantID, d.owner)
	if err != nil {
		return holdOutcomeNone, err
	}
	if !deleted {
		// The fence refused the write: this replica no longer holds the lease, so
		// the replay's outcome is not ours to record. Another drainer will redo it.
		log.Warn().
			Str("consumer", pass.consumer).
			Str("tenant", tenant.TenantID).
			Int64("offset", row.Offset).
			Msg("Hold lease lost mid-replay; the row stays for the next pass")
		return holdOutcomeLeaseLost, nil
	}
	return holdOutcomeNone, nil
}

// deferTenant records a failed replay and backs the tenant off.
func (d *HoldDrain) deferTenant(ctx context.Context, log logger.Logger, pass *holdPass,
	tenant *HoldTenant, row *HoldRow, replayErr error,
) (holdOutcome, error) {
	attempt := tenant.Attempts + 1
	wait := d.backoffFor(attempt)

	deferred, err := pass.store.Defer(ctx, pass.db, pass.consumer, tenant.TenantID, d.owner, wait, replayErr.Error())
	if err != nil {
		return holdOutcomeNone, err
	}
	if !deferred {
		// The fence refused the write, as in replayRow: this replica no longer holds
		// the lease, so the backoff below was never persisted and logging it would
		// describe a schedule that does not exist.
		log.Warn().
			Str("consumer", pass.consumer).
			Str("tenant", tenant.TenantID).
			Int64("offset", row.Offset).
			Msg("Hold lease lost before the tenant could be deferred")
		return holdOutcomeLeaseLost, nil
	}

	log.Warn().
		Str("consumer", pass.consumer).
		Str("tenant", tenant.TenantID).
		Str("stream", row.Stream).
		Int64("offset", row.Offset).
		Int("attempts", attempt).
		Dur("next_attempt_in", wait).
		Str("error_type", fmt.Sprintf("%T", replayErr)).
		Msg("Hold replay failed; tenant deferred")
	return holdOutcomeDeferred, nil
}

// releaseTenant drops the tenant's marker once its last row is replayed. A
// refused release — a lost lease, or rows a concurrent park added — leaves the
// tenant held for the next pass, which is a yield, not a failure.
func (d *HoldDrain) releaseTenant(ctx context.Context, log logger.Logger, pass *holdPass,
	tenant *HoldTenant,
) (holdOutcome, error) {
	released, err := pass.store.Release(ctx, pass.db, pass.consumer, tenant.TenantID, d.owner)
	if err != nil {
		return holdOutcomeNone, err
	}
	if !released {
		return holdOutcomeYielded, nil
	}
	log.Info().
		Str("consumer", pass.consumer).
		Str("tenant", tenant.TenantID).
		Dur("held_for", d.now().Sub(tenant.HeldSince)).
		Msg("Tenant released from hold")
	return holdOutcomeReleased, nil
}

// tallyIfTooOld counts a leased tenant held longer than the configured age on
// the pass's tally, which run turns into a Counter and at most one WARN per
// window: a tenant that yields stays due, and is seen again, every pass.
func (d *HoldDrain) tallyIfTooOld(pass *holdPass, tenant *HoldTenant) {
	held := d.now().Sub(tenant.HeldSince)
	if held <= d.cfg.MaxAge {
		return
	}
	pass.overAge.merge(&overAgeTally{
		count: 1, consumer: pass.consumer, tenant: tenant.TenantID, heldFor: held, attempts: tenant.Attempts,
	})
}

// reloadAndSnapshot tells the runners what the ledger holds and refreshes what
// the gauges report.
func (d *HoldDrain) reloadAndSnapshot(ctx context.Context, pass *holdPass) error {
	if err := pass.replayer.ReloadHeld(ctx, pass.consumer); err != nil {
		return err
	}

	stats, err := pass.store.Stats(ctx, pass.db, pass.consumer)
	if err != nil {
		return err
	}
	d.setSnapshot(pass.consumer, &stats)
	return nil
}

// snapshots is what the gauges read: one reading per consumer the drain has
// visited. Copied under the lock, because the caller iterates it on the
// exporter's own goroutine while a pass may be adding a consumer.
func (d *HoldDrain) snapshots() map[string]*HoldStats {
	d.statsMu.Lock()
	defer d.statsMu.Unlock()

	return maps.Clone(d.stats)
}

// setSnapshot publishes this consumer's latest reading. Once per pass per
// consumer, against a read on the exporter's schedule — a plain mutex is the
// whole synchronization this needs.
func (d *HoldDrain) setSnapshot(consumer string, stats *HoldStats) {
	d.statsMu.Lock()
	defer d.statsMu.Unlock()

	if d.stats == nil {
		d.stats = map[string]*HoldStats{}
	}
	d.stats[consumer] = stats
}

// backoffFor is the wait before a deferred tenant's next attempt: the drain
// interval doubled per attempt, capped. Saturating, like the lane's own series.
func (d *HoldDrain) backoffFor(attempts int) time.Duration {
	return backoff.Saturating(d.cfg.DrainInterval, d.cfg.MaxBackoff, max(attempts-1, 0))
}

// heldMessageOf renders a ledger row as the lane's held message.
func heldMessageOf(row *HoldRow) (*app.HeldMessage, error) {
	// Park stored the producer's properties as JSON. They carry the message's
	// application properties — the trace carrier among them — so a replay without
	// them is not the message that was parked.
	var properties map[string]any
	if len(row.Properties) > 0 {
		if err := json.Unmarshal(row.Properties, &properties); err != nil {
			return nil, fmt.Errorf("inbox hold drain: decode properties failed: %w", err)
		}
	}

	return &app.HeldMessage{
		Consumer:   row.Consumer,
		Stream:     row.Stream,
		Offset:     row.Offset,
		TenantID:   row.TenantID,
		Data:       row.Data,
		Properties: properties,
		HeldAt:     row.HeldAt,
	}, nil
}

// inboxMeterName is the instrumentation scope the inbox's instruments — the
// hold's gauges and the dedup-hit counter — report under.
const inboxMeterName = "go-bricks/inbox"

// holdOwnerID identifies this replica in a lease: the host it runs on, its
// process, and enough randomness that two processes on one host, or a restart
// reusing a pid, never share an owner. A lease is only safe if its owner is
// unique to the drainer holding it.
func holdOwnerID() string {
	return ownerIDFrom(os.Hostname, rand.Read)
}

// ownerIDFrom is holdOwnerID with its two sources named, because both fallbacks
// below are unreachable through the real ones: a host that cannot name itself
// and a crypto/rand that cannot read do not happen on demand.
func ownerIDFrom(hostname func() (string, error), readRandom func([]byte) (int, error)) string {
	host, err := hostname()
	if err != nil {
		host = "unknown-host"
	}

	var random [8]byte
	if _, err := readRandom(random[:]); err != nil {
		// crypto/rand does not fail in practice; if it ever does, the pid and host
		// still separate replicas, and a duplicate owner costs a redundant replay
		// rather than a lost message.
		return fmt.Sprintf("%s/%d", host, os.Getpid())
	}
	return fmt.Sprintf("%s/%d/%s", host, os.Getpid(), hex.EncodeToString(random[:]))
}
