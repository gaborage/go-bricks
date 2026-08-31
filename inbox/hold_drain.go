package inbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gaborage/go-bricks/config"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/streams"
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
	store    HoldStore
	getDB    func(context.Context) (dbtypes.Interface, error)
	replayer func() streams.HoldReplayer
	cfg      config.InboxHoldConfig
	// owner identifies this replica in the lease. Minted once, because a lease
	// taken under one name must be renewable and releasable under the same one.
	owner string

	// stats is the snapshot the gauges publish, per consumer. The map is guarded:
	// an observable-gauge callback reads it on the exporter's schedule, which is
	// not this goroutine.
	statsMu sync.RWMutex
	stats   map[string]*atomic.Pointer[HoldStats]

	// now is the Go clock, used only for the age a WARN reports. Every decision
	// the ledger makes uses database time.
	now func() time.Time
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

	db, err := d.getDB(jobCtx)
	if err != nil {
		return fmt.Errorf("inbox hold drain: resolve database failed: %w", err)
	}

	var errs []error
	for _, consumer := range replayer.HoldConsumers() {
		if err := d.drainConsumer(jobCtx, log, db, replayer, consumer); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// drainConsumer runs one consumer's pass and then tells its runners what the
// ledger still holds — the reload every replica depends on to learn about
// releases another replica made.
func (d *HoldDrain) drainConsumer(ctx context.Context, log logger.Logger, db dbtypes.Interface,
	replayer streams.HoldReplayer, consumer string) error {
	due, err := d.store.DueTenants(ctx, db, consumer, holdDrainTenantsPerPass)
	if err != nil {
		return err
	}

	var errs []error
	for i := range due {
		if err := d.drainTenant(ctx, log, db, replayer, consumer, &due[i]); err != nil {
			errs = append(errs, err)
		}
	}

	// Reload and snapshot even when a tenant failed: the set and the gauges
	// describe the ledger, not this pass.
	if err := d.reloadAndSnapshot(ctx, db, replayer, consumer); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// drainTenant replays one tenant's rows under a lease. A panic in a replay is
// contained here: the tenant that caused it is reported by TYPE (ADR-081) and the
// pass continues with the next one.
func (d *HoldDrain) drainTenant(ctx context.Context, log logger.Logger, db dbtypes.Interface,
	replayer streams.HoldReplayer, consumer string, tenant *HoldTenant) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("inbox hold drain: tenant %q: panic (type: %T)", tenant.TenantID, recovered)
		}
	}()

	taken, err := d.store.AcquireLease(ctx, db, consumer, tenant.TenantID, d.owner, d.cfg.LeaseDuration)
	if err != nil {
		return err
	}
	if !taken {
		// Another replica is replaying this tenant; its rows are not ours to touch.
		return nil
	}

	d.warnIfTooOld(log, consumer, tenant)
	return d.replayTenantRows(ctx, log, db, replayer, consumer, tenant)
}

// replayTenantRows replays at most one batch of a tenant's rows, in ledger order.
// One batch per pass, deliberately: a tenant with thousands of held rows must not
// own the drain for one long job run, and the next pass picks up where this one
// stopped. A short batch means the tenant is drained, so it is released.
func (d *HoldDrain) replayTenantRows(ctx context.Context, log logger.Logger, db dbtypes.Interface,
	replayer streams.HoldReplayer, consumer string, tenant *HoldTenant) error {
	rows, err := d.store.NextRows(ctx, db, consumer, tenant.TenantID, holdDrainRowsPerRead)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return d.releaseTenant(ctx, log, db, consumer, tenant)
	}

	for i := range rows {
		done, err := d.replayRow(ctx, log, db, replayer, consumer, tenant, &rows[i])
		if err != nil || !done {
			return err
		}
	}

	if len(rows) < holdDrainRowsPerRead {
		// The batch was not full, so nothing is left behind it.
		return d.releaseTenant(ctx, log, db, consumer, tenant)
	}
	return nil
}

// replayRow replays one row, reporting whether the tenant may continue. A failure
// defers the tenant: everything behind this row stays parked, which is the order
// the hold exists to keep.
func (d *HoldDrain) replayRow(ctx context.Context, log logger.Logger, db dbtypes.Interface,
	replayer streams.HoldReplayer, consumer string, tenant *HoldTenant, row *HoldRow) (bool, error) {
	if err := replayer.Replay(ctx, consumer, heldMessageOf(row)); err != nil {
		return false, d.deferTenant(ctx, log, db, consumer, tenant, row, err)
	}

	deleted, err := d.store.DeleteRow(ctx, db, consumer, row.Stream, row.Offset, tenant.TenantID, d.owner)
	if err != nil {
		return false, err
	}
	if !deleted {
		// The fence refused the write: this replica no longer holds the lease, so
		// the replay's outcome is not ours to record. Another drainer will redo it.
		log.Warn().
			Str("consumer", consumer).
			Str("tenant", tenant.TenantID).
			Int64("offset", row.Offset).
			Msg("Hold lease lost mid-replay; the row stays for the next pass")
		return false, nil
	}
	return true, nil
}

// deferTenant records a failed replay and backs the tenant off.
func (d *HoldDrain) deferTenant(ctx context.Context, log logger.Logger, db dbtypes.Interface,
	consumer string, tenant *HoldTenant, row *HoldRow, replayErr error) error {
	backoff := d.backoffFor(tenant.Attempts + 1)

	if _, err := d.store.Defer(ctx, db, consumer, tenant.TenantID, d.owner, backoff, replayErr.Error()); err != nil {
		return err
	}

	log.Warn().
		Str("consumer", consumer).
		Str("tenant", tenant.TenantID).
		Str("stream", row.Stream).
		Int64("offset", row.Offset).
		Int("attempts", tenant.Attempts+1).
		Dur("next_attempt_in", backoff).
		Str("error_type", fmt.Sprintf("%T", replayErr)).
		Msg("Hold replay failed; tenant deferred")
	return nil
}

// releaseTenant drops the tenant's marker once its last row is replayed.
func (d *HoldDrain) releaseTenant(ctx context.Context, log logger.Logger, db dbtypes.Interface,
	consumer string, tenant *HoldTenant) error {
	released, err := d.store.Release(ctx, db, consumer, tenant.TenantID, d.owner)
	if err != nil {
		return err
	}
	if released {
		log.Info().
			Str("consumer", consumer).
			Str("tenant", tenant.TenantID).
			Dur("held_for", d.now().Sub(tenant.HeldSince)).
			Msg("Tenant released from hold")
	}
	return nil
}

// warnIfTooOld reports a tenant held longer than the configured age. One line per
// pass per tenant: an operator watching a stuck tenant needs to see it recur.
func (d *HoldDrain) warnIfTooOld(log logger.Logger, consumer string, tenant *HoldTenant) {
	held := d.now().Sub(tenant.HeldSince)
	if held <= d.cfg.MaxAge {
		return
	}

	log.Warn().
		Str("consumer", consumer).
		Str("tenant", tenant.TenantID).
		Dur("held_for", held).
		Int("attempts", tenant.Attempts).
		Msg("Hold exceeds max age")
}

// reloadAndSnapshot tells the runners what the ledger holds and refreshes what
// the gauges report.
func (d *HoldDrain) reloadAndSnapshot(ctx context.Context, db dbtypes.Interface,
	replayer streams.HoldReplayer, consumer string) error {
	held, err := d.store.HeldTenants(ctx, db, consumer)
	if err != nil {
		return err
	}
	replayer.ReloadHeld(consumer, held)

	stats, err := d.store.Stats(ctx, db, consumer)
	if err != nil {
		return err
	}
	d.snapshot(consumer).Store(&stats)
	return nil
}

// snapshot is this consumer's stats cell, created on first use. The map is
// guarded because a gauge callback reads it on the exporter's own schedule.
func (d *HoldDrain) snapshot(consumer string) *atomic.Pointer[HoldStats] {
	d.statsMu.RLock()
	cell, ok := d.stats[consumer]
	d.statsMu.RUnlock()
	if ok {
		return cell
	}

	d.statsMu.Lock()
	defer d.statsMu.Unlock()
	if cell, ok := d.stats[consumer]; ok {
		return cell
	}
	if d.stats == nil {
		d.stats = map[string]*atomic.Pointer[HoldStats]{}
	}
	cell = &atomic.Pointer[HoldStats]{}
	d.stats[consumer] = cell
	return cell
}

// backoffFor is the wait before a deferred tenant's next attempt: the drain
// interval doubled per attempt, capped. Saturating, like the lane's own series.
func (d *HoldDrain) backoffFor(attempts int) time.Duration {
	wait := d.cfg.DrainInterval
	for range attempts - 1 {
		if wait >= d.cfg.MaxBackoff {
			return d.cfg.MaxBackoff
		}
		wait *= 2
	}
	if wait > d.cfg.MaxBackoff {
		return d.cfg.MaxBackoff
	}
	return wait
}

// heldMessageOf renders a ledger row as the lane's held message.
func heldMessageOf(row *HoldRow) *streams.HeldMessage {
	return &streams.HeldMessage{
		Consumer: row.Consumer,
		Stream:   row.Stream,
		Offset:   row.Offset,
		TenantID: row.TenantID,
		Data:     row.Data,
		HeldAt:   row.HeldAt,
	}
}
