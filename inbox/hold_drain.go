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

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	dbtypes "github.com/gaborage/go-bricks/database/types"
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

	// now is the Go clock, used only for the age a WARN reports. Every decision
	// the ledger makes uses database time.
	now func() time.Time
}

// holdPass is one consumer's pass: the control-plane database, the store that
// speaks its dialect, the runners to replay through and whose consumer it is —
// settled once rather than threaded through every step separately.
type holdPass struct {
	db       dbtypes.Interface
	store    HoldStore
	replayer app.HoldReplayer
	consumer string
}

// heldTenant is one tenant's turn under a lease: the ledger's row for it, and the
// instant this replica must stop working on it. The two are inseparable — the
// deadline means nothing without the tenant whose lease granted it.
type heldTenant struct {
	tenant   *HoldTenant
	deadline time.Time
}

// Execute runs one drain pass over every holding consumer.
func (d *HoldDrain) Execute(jobCtx scheduler.JobContext) error {
	log := jobCtx.Logger()

	replayer := d.replayer()
	if replayer == nil {
		// The ledger is configured but no stream consumer runs here — another
		// replica's drain owns these holds.
		log.Debug().Msg("Hold drain skipped: no stream consumers on this replica")
		return nil
	}

	db, store, err := d.resolve(jobCtx)
	if err != nil {
		return err
	}

	var errs []error
	for _, consumer := range replayer.HoldConsumers() {
		pass := &holdPass{db: db, store: store, replayer: replayer, consumer: consumer}
		if err := d.drainConsumer(jobCtx, log, pass); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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
		if err := d.drainTenant(ctx, log, pass, &due[i]); err != nil {
			errs = append(errs, err)
		}
	}

	// Reload and snapshot even when a tenant failed: the set and the gauges
	// describe the ledger, not this pass.
	if err := d.reloadAndSnapshot(ctx, pass); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// drainTenant replays one tenant's rows under a lease. A panic in a replay is
// contained here: the tenant that caused it is reported by TYPE (ADR-081) and the
// pass continues with the next one.
func (d *HoldDrain) drainTenant(ctx context.Context, log logger.Logger, pass *holdPass,
	tenant *HoldTenant,
) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("inbox hold drain: tenant %q: panic (type: %T)", tenant.TenantID, recovered)
		}
	}()

	taken, err := pass.store.AcquireLease(ctx, pass.db, pass.consumer, tenant.TenantID, d.owner, d.cfg.LeaseDuration)
	if err != nil {
		return err
	}
	if !taken {
		// Another replica is replaying this tenant; its rows are not ours to touch.
		return nil
	}

	d.warnIfTooOld(log, pass, tenant)
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
) error {
	tenant := held.tenant
	rows, err := pass.store.NextRows(ctx, pass.db, pass.consumer, tenant.TenantID, holdDrainRowsPerRead)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return d.releaseTenant(ctx, log, pass, tenant)
	}

	for i := range rows {
		if !d.now().Before(held.deadline) {
			// The lease ran out with rows still held. Stopping is not a failure — the
			// tenant keeps its place and its backoff — so hand the lease back rather
			// than making every other replica wait for it to expire.
			log.Warn().
				Str("consumer", pass.consumer).
				Str("tenant", tenant.TenantID).
				Int("replayed", i).
				Msg("Hold lease spent mid-batch; the rest stays for the next pass")
			return d.yieldLease(ctx, pass, tenant)
		}

		done, err := d.replayRow(ctx, log, pass, held, &rows[i])
		if err != nil || !done {
			return err
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
func (d *HoldDrain) yieldLease(ctx context.Context, pass *holdPass, tenant *HoldTenant) error {
	return pass.store.ReleaseLease(ctx, pass.db, pass.consumer, tenant.TenantID, d.owner)
}

// replayRow replays one row, reporting whether the tenant may continue. A failure
// defers the tenant: everything behind this row stays parked, which is the order
// the hold exists to keep.
func (d *HoldDrain) replayRow(ctx context.Context, log logger.Logger, pass *holdPass,
	held *heldTenant, row *HoldRow,
) (bool, error) {
	tenant := held.tenant
	msg, err := heldMessageOf(row)
	if err != nil {
		// The row is unreadable, which no retry fixes; deferring keeps it — and the
		// tenant's order — while the WARN names the row an operator has to look at.
		return false, d.deferTenant(ctx, log, pass, tenant, row, err)
	}

	// The handler runs under the lease, not the job: a replay still running after
	// the lease expired is a second drainer's tenant being replayed twice.
	replayCtx, cancel := context.WithDeadline(ctx, held.deadline)
	err = pass.replayer.Replay(replayCtx, pass.consumer, msg)
	cancel()
	if err != nil {
		return false, d.deferTenant(ctx, log, pass, tenant, row, err)
	}

	deleted, err := pass.store.DeleteRow(ctx, pass.db, pass.consumer, row.Stream, row.Offset, tenant.TenantID, d.owner)
	if err != nil {
		return false, err
	}
	if !deleted {
		// The fence refused the write: this replica no longer holds the lease, so
		// the replay's outcome is not ours to record. Another drainer will redo it.
		log.Warn().
			Str("consumer", pass.consumer).
			Str("tenant", tenant.TenantID).
			Int64("offset", row.Offset).
			Msg("Hold lease lost mid-replay; the row stays for the next pass")
		return false, nil
	}
	return true, nil
}

// deferTenant records a failed replay and backs the tenant off.
func (d *HoldDrain) deferTenant(ctx context.Context, log logger.Logger, pass *holdPass,
	tenant *HoldTenant, row *HoldRow, replayErr error,
) error {
	attempt := tenant.Attempts + 1
	backoff := d.backoffFor(attempt)

	deferred, err := pass.store.Defer(ctx, pass.db, pass.consumer, tenant.TenantID, d.owner, backoff, replayErr.Error())
	if err != nil {
		return err
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
		return nil
	}

	log.Warn().
		Str("consumer", pass.consumer).
		Str("tenant", tenant.TenantID).
		Str("stream", row.Stream).
		Int64("offset", row.Offset).
		Int("attempts", attempt).
		Dur("next_attempt_in", backoff).
		Str("error_type", fmt.Sprintf("%T", replayErr)).
		Msg("Hold replay failed; tenant deferred")
	return nil
}

// releaseTenant drops the tenant's marker once its last row is replayed.
func (d *HoldDrain) releaseTenant(ctx context.Context, log logger.Logger, pass *holdPass,
	tenant *HoldTenant,
) error {
	released, err := pass.store.Release(ctx, pass.db, pass.consumer, tenant.TenantID, d.owner)
	if err != nil {
		return err
	}
	if released {
		log.Info().
			Str("consumer", pass.consumer).
			Str("tenant", tenant.TenantID).
			Dur("held_for", d.now().Sub(tenant.HeldSince)).
			Msg("Tenant released from hold")
	}
	return nil
}

// warnIfTooOld reports a tenant held longer than the configured age. One line per
// pass per tenant: an operator watching a stuck tenant needs to see it recur.
func (d *HoldDrain) warnIfTooOld(log logger.Logger, pass *holdPass, tenant *HoldTenant) {
	held := d.now().Sub(tenant.HeldSince)
	if held <= d.cfg.MaxAge {
		return
	}

	log.Warn().
		Str("consumer", pass.consumer).
		Str("tenant", tenant.TenantID).
		Dur("held_for", held).
		Int("attempts", tenant.Attempts).
		Msg("Hold exceeds max age")
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
	// The doubling is a shift, and the count is clamped rather than compared:
	// attempts is a persisted counter with no ceiling, and a shift past 62
	// overflows an int64 duration. min/max bound it without a comparison whose
	// boundary the cap below would swallow — at the cap, doubling once more and
	// clamping give the same answer, so such a boundary is untestable by
	// construction.
	shift := min(max(attempts-1, 0), 62)
	wait := d.cfg.DrainInterval << shift
	if wait <= 0 {
		// The shift carried every bit out of the duration: the cap is the answer,
		// and without this a wrapped value would clamp to itself.
		return d.cfg.MaxBackoff
	}
	return min(wait, d.cfg.MaxBackoff)
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

// holdMeterName is the instrumentation scope the hold's gauges report under.
const holdMeterName = "go-bricks/inbox"

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
