package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/gaborage/go-bricks/config"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/leasescope"
	"github.com/gaborage/go-bricks/internal/ledgererr"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/scheduler"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// Relay is a scheduler.Executor that polls for pending outbox events
// and hands them to the shipper for their lane.
//
// The relay runs as a scheduled job (registered via scheduler.FixedRate),
// getting overlapping prevention, panic recovery, and OTel metrics for free.
//
// Resources are resolved through the tenant-aware getDB resolver and each
// shipper's own (the module's deps.DB/deps.Messaging) rather than the scheduler
// JobContext, because the scheduler builds the JobContext from a tenant-less
// context — so in multi-tenant mode the relay must inject each tenant into the
// context itself before resolving that tenant's database and broker.
type Relay struct {
	store  Store
	config config.OutboxConfig
	getDB  func(context.Context) (dbtypes.Interface, error)

	// shippers holds one adapter per lane, keyed by the lane a row names. Looking a
	// record's lane up here is the ONLY place the empty legacy lane resolves, and a lane
	// with no entry is poison. The module writes this map once at registration.
	shippers map[string]shipper

	// laneOrder is the registered lanes sorted once at construction, so a cycle probes and
	// logs them the same way every time without sorting per cycle. laneLogFields holds each
	// of those lanes' four cycle-log field names, built once for the same reason: logCycle
	// runs every cycle and would otherwise concatenate four strings per lane per cycle.
	laneOrder     []string
	laneLogFields []laneFields

	// tenants lists the tenant keys to relay each cycle. Always non-empty: a single
	// "" entry for single-tenant mode (multitenant.SetTenant with "" is a no-op) and
	// for shared (control-plane) tenancy, or the configured static multitenant tenant
	// IDs. In per-tenant tenancy, dynamic multi-tenant sources are rejected at module
	// Init (their tenant set is not enumerable at registration time).
	tenants []string
}

// laneFields is one lane's precomputed cycle-log field names.
type laneFields struct {
	published, failed, deadlettered, parked string
}

// newRelay builds a relay over its lanes, precomputing everything a cycle would otherwise
// derive from the shipper map on every tick.
func newRelay(store Store, cfg *config.OutboxConfig, getDB func(context.Context) (dbtypes.Interface, error), tenants []string, shippers map[string]shipper) *Relay {
	lanes := make([]string, 0, len(shippers))
	for lane := range shippers {
		lanes = append(lanes, lane)
	}
	sort.Strings(lanes)

	fields := make([]laneFields, len(lanes))
	for i, lane := range lanes {
		fields[i] = laneFields{
			published:    "published_" + lane,
			failed:       "failed_" + lane,
			deadlettered: "deadlettered_" + lane,
			parked:       "parked_" + lane,
		}
	}

	return &Relay{
		store:         store,
		config:        *cfg,
		getDB:         getDB,
		shippers:      shippers,
		laneOrder:     lanes,
		laneLogFields: fields,
		tenants:       tenants,
	}
}

// Execute runs one relay cycle per configured tenant. In single-tenant mode this is a
// single pass with no tenant in context; in static multi-tenant mode it fans out across
// the configured tenants, resolving each tenant's database and broker independently.
// Per-tenant failures are collected so one unhealthy tenant does not block the others.
func (r *Relay) Execute(jobCtx scheduler.JobContext) error {
	log := jobCtx.Logger()
	var errs []error
	for _, tenantID := range r.tenants {
		ctx := multitenant.SetTenant(jobCtx, tenantID)
		if err := r.relayTenant(ctx, log, tenantID); err != nil {
			errs = append(errs, fmt.Errorf("outbox relay: tenant %q: %w", tenantID, err))
		}
	}
	return errors.Join(errs...)
}

// publishOutcome is the per-record result the relay loop uses to decide bookkeeping
// and whether to continue, stop, or count the record.
type publishOutcome int

const (
	outcomePublished           publishOutcome = iota // delivered and recorded
	outcomePublishedUnrecorded                       // delivered, but MarkPublished failed (no retry_count bump)
	outcomeFailed                                    // failed; retry_count advanced, record stays pending
	outcomeDeadLettered                              // poison exhausted MaxRetries; parked as status=failed
)

// relayTenant runs a single relay cycle for the given (tenant-scoped) context.
func (r *Relay) relayTenant(ctx context.Context, log logger.Logger, tenantID string) error {
	// Install a per-tenant lease scope so this tenant's DB and messaging handles are released
	// at the end of its relay cycle rather than pinned until the whole fan-out job ends
	// (ADR-032). This bounds the working set to ~one tenant, so a large multi-tenant relay
	// cannot hold every tenant's connection open at once and exceed the pool's MaxSize. The
	// per-tenant scope shadows the job-level scope installed by the scheduler.
	ctx, scope := leasescope.Install(ctx)
	defer scope.ReleaseAll()

	db, err := r.getDB(ctx)
	if err != nil {
		return fmt.Errorf("database not available: %w", err)
	}
	if db == nil {
		return errors.New("database not available")
	}

	// One relay instance per ledger drains at a time: the leader row is held FOR UPDATE
	// NOWAIT in a transaction that lives for this cycle. Taken BEFORE the fetch so a
	// non-leader does no work at all, and covering the outage path too, whose marks are
	// writes.
	lead, err := r.store.Lead(ctx, db)
	if err != nil {
		if errors.Is(err, ErrNotLeader) {
			log.Debug().Msg("Outbox relay: another instance leads this ledger; skipping cycle")
			return nil
		}
		return fmt.Errorf("leader: %w", err)
	}
	defer func() { _ = lead.Release(ctx) }()

	records, err := r.store.FetchPending(ctx, db, r.config.BatchSize)
	if err != nil {
		return fmt.Errorf("fetch failed: %w", err)
	}
	if len(records) == 0 {
		// No undelivered work — an idle relay is not a failure even if a lane is down.
		return nil
	}

	// Pre-flight each lane ONCE. A lane that reports itself unusable takes only its own
	// rows: the lanes are separate connections and one being down says nothing about the
	// other, and a row on a lane this build does not know needs no broker at all.
	downLanes, laneErr := r.preflight(ctx)

	var res relayBatchResult
	// Outage path: a lane that is unreachable/not-ready still has pending events on it.
	// Advance every such record's retry_count (the operator's "still retrying" signal)
	// without a publish call, so an outage no longer freezes the count, and report the
	// failure at the job level rather than silently succeeding forever. With no lane down
	// the split hands every row back as runnable and outages nothing.
	runnable, outaged := splitOnDownLanes(records, downLanes)
	r.markOutage(ctx, log, db, lead, outaged, &res)
	if len(runnable) == 0 {
		r.logCycle(log, tenantID, &res, len(records))
		// Leadership loss first, same priority the mixed-lane return below gives it: it is
		// a database-side failure and wrapping it as the lane's outage would send an
		// operator to the wrong system.
		if res.leadershipErr != nil {
			return res.leadershipErr
		}
		return laneErr
	}

	r.runRelayLoop(ctx, log, db, lead, runnable, &res)

	r.logCycle(log, tenantID, &res, len(records))
	// Leadership loss first: it is a database-side failure, and wrapping it as a broker
	// outage would send an operator to the wrong system. The next tick re-leads.
	if res.leadershipErr != nil {
		return res.leadershipErr
	}
	if laneErr != nil {
		return laneErr
	}
	if res.outageErr != nil {
		return brokerUnavailableErr(res.outageErr)
	}
	return nil
}

// preflight asks each lane whether it is usable this cycle, returning the lanes that are
// not and the error the cycle reports for them.
func (r *Relay) preflight(ctx context.Context) (down map[string]struct{}, laneErr error) {
	var errs []error
	for _, lane := range r.laneOrder {
		if err := r.shippers[lane].Ready(ctx); err != nil {
			if down == nil {
				down = make(map[string]struct{}, len(r.shippers))
			}
			down[lane] = struct{}{}
			errs = append(errs, err)
		}
	}
	return down, errors.Join(errs...)
}

// splitOnDownLanes separates the rows a down lane owns from the rest, keeping each
// group's sequence order. A row naming a lane this build does not know is never in a down
// group: it needs no broker, since the loop dead-letters it as poison, and routing it
// through the outage path would instead bump its retry_count every cycle until the lane
// returned.
func splitOnDownLanes(records []Record, down map[string]struct{}) (runnable, outaged []Record) {
	for i := range records {
		if _, isDown := down[laneOrDefault(records[i].Lane)]; isDown {
			outaged = append(outaged, records[i])
		} else {
			runnable = append(runnable, records[i])
		}
	}
	return runnable, outaged
}

// laneCounts is one lane's share of a cycle, so a summary says WHICH lane the work
// happened on: an aggregate failure count cannot distinguish a stalled super stream from
// a broker outage, which are different pages for whoever is holding it. The cycle's own
// totals are the same four numbers summed across lanes (relayBatchResult.totals).
type laneCounts struct {
	published, failed, deadlettered, parked int
}

// relayBatchResult holds the per-record bookkeeping counts from one relay cycle, plus
// (if the batch stopped early on a mid-batch broker drop) the outage error the caller
// surfaces at the job level.
// The four lane-keyed counters are the ONLY counters: a cycle total is their sum, so the
// two can never disagree about what happened. unrecorded is the one outcome with no lane
// share (see apply), so it stays a plain field.
type relayBatchResult struct {
	byLane     map[string]laneCounts
	unrecorded int
	// stallWait is the longest a single shipment waited before reporting its lane or its
	// scope down — the cost the cycle actually paid to discover the stall.
	stallWait time.Duration
	outageErr error
	// leadershipErr is set when the claim on the leader row was lost mid-cycle. It is kept
	// apart from outageErr because its cause is the DATABASE, not the broker: reporting it
	// as a broker outage would point an operator at the wrong system.
	leadershipErr error
}

// totals sums every lane's share, including the lanes this build has no shipper for, whose
// rows are still counted (as poison) and must still show up in the cycle's own numbers.
func (res *relayBatchResult) totals() laneCounts {
	var t laneCounts
	for _, counts := range res.byLane {
		t.published += counts.published
		t.failed += counts.failed
		t.deadlettered += counts.deadlettered
		t.parked += counts.parked
	}
	return t
}

// count folds one increment into a lane's share. The counts are values, so a lane is read,
// bumped and written back rather than handed out for mutation.
func (res *relayBatchResult) count(lane string, bump func(*laneCounts)) {
	if res.byLane == nil {
		res.byLane = make(map[string]laneCounts, 2)
	}
	counts := res.byLane[lane]
	bump(&counts)
	res.byLane[lane] = counts
}

// apply folds one record's outcome into its lane's share.
func (res *relayBatchResult) apply(lane string, outcome publishOutcome) {
	switch outcome {
	case outcomePublished:
		res.count(lane, func(c *laneCounts) { c.published++ })
	case outcomePublishedUnrecorded:
		// Delivered but not recorded: it re-delivers next cycle, so it is neither a
		// success nor a failure of this lane's share.
		res.unrecorded++
	case outcomeFailed:
		res.count(lane, func(c *laneCounts) { c.failed++ })
	case outcomeDeadLettered:
		res.count(lane, func(c *laneCounts) { c.deadlettered++ })
	}
}

func (res *relayBatchResult) markParked(lane string) {
	res.count(lane, func(c *laneCounts) { c.parked++ })
}

// relayCycle is one tenant's pass over one batch: the handles every step needs, the
// bookkeeping it folds into, and the two hold-back sets it accumulates as it goes.
type relayCycle struct {
	relay *Relay
	log   logger.Logger
	db    dbtypes.Interface
	lead  Leadership
	res   *relayBatchResult
	// cur is the record currently being planned and shipped. It is a FIELD, not a local,
	// so &c.cur points into the cycle's one heap allocation: the lane takes the shipment
	// by pointer (80 bytes) and no per-record allocation is made for it.
	cur shipment
	// parked holds keys whose head failed this cycle. A later row of a parked key is left
	// untouched — not even its retry_count moves — so the key keeps its order across cycles.
	parked map[string]struct{}
	// down holds scopes whose target proved unready this cycle. Held per scope, not
	// batch-wide: one stalled super stream says nothing about the others, which are
	// separate producers.
	down map[string]struct{}
}

// runRelayLoop ships each pending record in order, stopping early on shutdown/cancel, an
// aborted ship, or a mid-batch lane drop.
func (r *Relay) runRelayLoop(ctx context.Context, log logger.Logger, db dbtypes.Interface, lead Leadership, records []Record, res *relayBatchResult) {
	cycle := &relayCycle{
		relay: r, log: log, db: db, lead: lead, res: res,
		parked: make(map[string]struct{}),
		down:   make(map[string]struct{}),
	}
	for i := range records {
		// Stop cleanly on shutdown/cancel: leave the rest pending for the next startup
		// rather than bumping their retry_count on the way down.
		if ctx.Err() != nil {
			return
		}
		// A deposed leader must not publish another row: another instance may already be
		// draining the same ledger.
		if err := lead.Probe(ctx); err != nil {
			log.Warn().Err(err).Msg("Outbox relay lost its leader row mid-cycle; stopping")
			res.leadershipErr = fmt.Errorf("%w: lost the leader row mid-cycle, database unreachable or the transaction was ended: %w", ErrNotLeader, err)
			return
		}

		record := &records[i]
		lane := laneOrDefault(record.Lane)
		laneShipper := cycle.plan(record, lane)
		ship := &cycle.cur
		if _, isDown := cycle.down[ship.Scope]; isDown {
			// That scope's target is not carrying messages; its remaining rows wait for
			// the next cycle untouched rather than each paying the publish bound. Rows
			// aimed at a healthy scope are unaffected.
			res.markParked(lane)
			continue
		}
		if _, isParked := cycle.parked[ship.Key]; isParked {
			res.markParked(lane)
			continue
		}
		if ship.Poison != "" {
			// Judged only once the row is known NOT to be held back: dead-lettering is a
			// ledger write, and a held-back row's retry_count must not move (ADR-088).
			// Planning above wrote nothing, so a poison row behind a still-pending head of
			// its key waits for that head exactly as a shippable row does.
			cycle.deadLetter(ctx, record, lane, ship.Key, ship.Poison)
			continue
		}

		if cycle.ship(ctx, laneShipper, lane, records[i+1:]) {
			return
		}
	}
}

// plan decodes the row's headers, injects the outbox metadata every consumer dedups on,
// and asks the lane to plan it. It is PURE — nothing is written to the ledger here — and
// leaves the shipment in c.cur, whose Poison names why no lane can carry the row when it is
// set: undecodable headers, a lane this build does not know, or config drift the lane itself
// can see without a client. The shipper it returns is nil exactly when the lane is unknown.
func (c *relayCycle) plan(record *Record, lane string) shipper {
	headers, decodeErr := decodeHeaders(record.Headers)
	laneShipper, known := c.relay.shippers[lane]
	if !known {
		// A lane this build does not know is message-intrinsic: it will read the same way
		// every cycle, so it parks rather than retrying forever. It is also the one row no
		// lane can key, so it keys under its own id, which shares with nothing and so parks
		// nothing — including when its headers are corrupt too.
		reason := fmt.Sprintf("unknown lane %q", record.Lane)
		if decodeErr != nil {
			reason = decodeErr.Error()
		}
		c.cur = shipment{Record: record, Key: "unknown:" + record.ID, Poison: reason}
		return nil
	}
	// Corrupt headers are deterministic, broker-independent corruption (poison), so they
	// dead-letter at MaxRetries rather than retrying forever. The lane is still asked to
	// plan the row — with nil headers, since nothing could be read from them — because a
	// row that stays PENDING must hold its key's order like any other.
	if decodeErr != nil {
		c.cur = laneShipper.Plan(record, nil)
		c.cur.Poison = decodeErr.Error()
		return laneShipper
	}

	// Inject outbox metadata headers for consumer idempotency.
	if headers == nil {
		headers = make(map[string]any)
	}
	headers[HeaderEventID] = record.ID
	headers[HeaderEventType] = record.EventType

	c.cur = laneShipper.Plan(record, headers)
	return laneShipper
}

// deadLetter dead-letters a record and, below the ceiling, holds its key. A row that only
// advanced its retry_count is still PENDING, so the later rows of its key must wait for it
// or they ship ahead of it and the key loses the order ADR-088 promises.
func (c *relayCycle) deadLetter(ctx context.Context, record *Record, lane, key, reason string) {
	outcome := c.relay.deadLetterPoison(ctx, c.log, c.db, record, reason)
	c.res.apply(lane, outcome)
	if outcome != outcomeFailed {
		// Dead-lettered is terminal: nothing waits behind it.
		return
	}
	c.parked[key] = struct{}{}
}

// fail advances a record's retry_count and holds its key. Connectivity never dead-letters
// the row itself (at-least-once, however high the count climbs), but the row stays PENDING,
// so its key keeps its order exactly as a below-the-ceiling poison row's does.
func (c *relayCycle) fail(ctx context.Context, record *Record, lane, key string, err error) {
	c.relay.markRecordFailed(ctx, c.log, c.db, record.ID, err.Error())
	c.res.apply(lane, outcomeFailed)
	c.parked[key] = struct{}{}
}

// ship makes one bounded delivery attempt and folds its verdict into the cycle's
// bookkeeping. It reports true when the cycle must stop: a shutdown-aborted ship, or a
// lane that dropped mid-batch, whose unattempted remainder takes the outage path.
func (c *relayCycle) ship(ctx context.Context, laneShipper shipper, lane string, remainder []Record) bool {
	ship := &c.cur
	// Rehydrate the originating trace context (persisted by Publish) into the publish
	// context. The relay job runs detached with no ambient trace, so without this the
	// downstream preparePublishing would stamp a freshly generated CorrelationId — which
	// the consumer's failure-path logger and consume span surface — breaking trace
	// continuity on the error path.
	pubCtx := gobrickstrace.ExtractFromHeaders(ctx, &mapHeaderAccessor{headers: ship.Headers})
	// Move the row's tenant onto the publish context. The framework is the stamp's ONLY
	// writer (ADR-087), and a stamp replayed out of storage is caller-supplied from a
	// publisher's point of view — so it must travel by context, never as a header.
	if ship.Stamp != "" {
		pubCtx = multitenant.SetTenant(pubCtx, ship.Stamp)
	}

	// Bound this single ship so one stuck record cannot block the whole cycle and starve
	// the rest of the batch. cancel() is called immediately; every Mark* below uses the
	// parent ctx, never the (possibly expired) recCtx.
	recCtx, cancel := context.WithTimeout(pubCtx, c.relay.config.PublishTimeout)
	v := laneShipper.Ship(recCtx, ship)
	cancel()

	record := ship.Record
	switch v.Kind {
	case shipDelivered:
		c.res.apply(lane, c.relay.recordPublished(ctx, c.log, c.db, record))
	case shipRetry:
		// Connectivity: the broker either could not be reached or could not take
		// responsibility for the message. We advance retry_count and retry
		// (at-least-once); only poison ever dead-letters.
		c.fail(ctx, record, lane, ship.Key, v.Err)
	case shipPoison:
		// Below the ceiling the row stays pending, so its key keeps its order — the same
		// rule the poison the relay decided before the ship follows.
		c.deadLetter(ctx, record, lane, ship.Key, v.Err.Error())
	case shipAborted:
		// Shutting down mid-ship — stop without counting this record.
		return true
	case shipBrokerDown:
		// The lane dropped mid-batch. Route the UNATTEMPTED remainder through the same
		// outage path the cycle-start pre-flight applies — advance retry_count without
		// paying each record's own serial readiness wait — and stop the cycle. The
		// remainder counts as failed too (markOutage marked it in the DB), so the cycle's
		// counts still sum to the batch total. The key it holds is moot: the cycle ends
		// here and the hold-back set does not outlive it.
		c.fail(ctx, record, lane, ship.Key, v.Err)
		c.res.outageErr = v.Err
		c.res.stallWait = max(c.res.stallWait, v.Waited)
		c.relay.markOutage(ctx, c.log, c.db, c.lead, remainder, c.res)
		return true
	case shipScopeDown:
		// Evidence that THIS shipment's scope is not carrying messages, not that this row
		// is special: paying the bound for every remaining row of that scope would hold
		// the leader transaction for batchsize x publishtimeout.
		c.fail(ctx, record, lane, ship.Key, v.Err)
		c.down[ship.Scope] = struct{}{}
		c.res.stallWait = max(c.res.stallWait, v.Waited)
	}
	return false
}

// markOutage advances retry_count for every pending record without attempting a ship,
// used when its lane is unreachable/not-ready. Stops early on shutdown/cancel so a
// shutdown does not inflate retry_count for records it never got to.
func (r *Relay) markOutage(ctx context.Context, log logger.Logger, db dbtypes.Interface, lead Leadership, records []Record, res *relayBatchResult) {
	for i := range records {
		if ctx.Err() != nil {
			return
		}
		// A mark is a write, so it needs the same leadership guarantee as a ship. Recorded
		// on res.leadershipErr, the same field runRelayLoop sets, so relayTenant's
		// leadership-first check catches this path too instead of letting the lane-down
		// error it was marking under hide a database-side leadership loss.
		if err := lead.Probe(ctx); err != nil {
			log.Warn().Err(err).Msg("Outbox relay lost leadership while marking an outage; stopping")
			res.leadershipErr = fmt.Errorf("%w: lost the leader row while marking an outage, database unreachable or the transaction was ended: %w", ErrNotLeader, err)
			return
		}
		r.markRecordFailed(ctx, log, db, records[i].ID, "messaging unavailable")
		res.apply(laneOrDefault(records[i].Lane), outcomeFailed)
	}
}

// logCycle emits the per-cycle delivery summary. "unrecorded" counts events delivered to the
// broker whose MarkPublished failed (they re-deliver next cycle) — kept distinct from
// "published" so the success count is not inflated by stuck-but-delivered records.
// stall_wait_ms is what the cycle paid to discover a down lane or scope, zero when none did.
func (r *Relay) logCycle(log logger.Logger, tenantID string, res *relayBatchResult, total int) {
	totals := res.totals()
	event := log.Info().
		Int("published", totals.published).
		Int("unrecorded", res.unrecorded).
		Int("failed", totals.failed).
		Int("deadlettered", totals.deadlettered).
		Int("parked", totals.parked).
		Int("total", total).
		Int64("stall_wait_ms", res.stallWait.Milliseconds())
	for i, lane := range r.laneOrder {
		// A lane with no rows this cycle reads back as the zero value, which is what it
		// did: no entry is allocated for an idle lane.
		counts, fields := res.byLane[lane], &r.laneLogFields[i]
		event = event.
			Int(fields.published, counts.published).
			Int(fields.failed, counts.failed).
			Int(fields.deadlettered, counts.deadlettered).
			Int(fields.parked, counts.parked)
	}
	if tenantID != "" {
		event = event.Str("tenant", tenantID)
	}
	if r.config.Tenancy == config.TenancyShared {
		event = event.Str("tenancy", "shared")
	}
	event.Msg("Outbox relay cycle completed")
}

// brokerUnavailableErr spells the AMQP lane's outage text ONCE, for both the paths that
// report one: the lane's own pre-flight (amqpShipper.Ready, which reports an unready client
// with a nil error here) and the mid-batch drop the cycle surfaces at the job level. An
// operator reading "messaging not available" must not have to know which of the two produced
// it.
func brokerUnavailableErr(msgErr error) error {
	if msgErr != nil {
		return fmt.Errorf("messaging not available: %w", msgErr)
	}
	return errors.New("messaging not ready")
}

// recordPublished marks a delivered record published. Shared by every lane: the delivery
// happened either way, so a failed mark must not bump retry_count on any of them.
func (r *Relay) recordPublished(ctx context.Context, log logger.Logger, db dbtypes.Interface, record *Record) publishOutcome {
	if err := r.store.MarkPublished(ctx, db, record.ID); err != nil {
		log.Error().
			Err(err).
			Str("eventID", record.ID).
			Msg("Failed to mark outbox event as published")
		// The message WAS delivered; do not bump retry_count. It re-delivers next
		// cycle and the consumer dedups via the x-outbox-event-id header.
		return outcomePublishedUnrecorded
	}
	return outcomePublished
}

// deadLetterPoison handles a poison record: it advances retry_count and, once the record has
// reached MaxRetries, parks it to status=failed (the only auto-parking path). Below the ceiling
// it only advances retry_count and leaves the record pending.
//
// Poison is message-intrinsic — the row reads the same way every cycle, so no broker state can
// make it publishable. Six classes park here, each from a call site above:
//   - undecodable headers: the stored JSON does not parse, so no frame can be built.
//   - unknown lane: the row names a lane this build has no shipper for.
//   - unpublishable destination: an exchange, routing key or header key past the AMQP shortstr
//     ceiling, refused before any channel work (messaging.ErrInvalidPublishDestination).
//   - tenant-stamp conflict: the row's own stamp contradicts the publish context
//     (messaging.ErrTenantStampConflict). ONE class, reachable on BOTH lanes.
//   - stream not an outbox target: the streams lane is not wired at all, or the row's stream
//     left outbox.superstreams between deploys.
//   - stream row with no partition key: nothing to hash a partition from.
//
// Connectivity never reaches here: a broker NACK, a missing exchange, a not-connected client or
// a confirmation timeout on the AMQP lane, a publisher not yet started on the stream lane, and
// the publish bound on either, all call markRecordFailed directly and never park, however
// high retry_count climbs.
func (r *Relay) deadLetterPoison(ctx context.Context, log logger.Logger, db dbtypes.Interface, record *Record, errMsg string) publishOutcome {
	if record.RetryCount+1 >= r.config.MaxRetries {
		if err := r.store.MarkDeadLettered(ctx, db, record.ID, ledgererr.Bound(errMsg)); err != nil {
			log.Error().Err(err).Str("eventID", record.ID).Msg("Failed to dead-letter outbox event")
			return outcomeFailed
		}
		log.Warn().
			Str("eventID", record.ID).
			Str("eventType", record.EventType).
			Msg("Outbox event dead-lettered after exhausting retries")
		return outcomeDeadLettered
	}
	r.markRecordFailed(ctx, log, db, record.ID, errMsg)
	return outcomeFailed
}

// markRecordFailed marks an outbox record as failed, logging any secondary errors.
func (r *Relay) markRecordFailed(ctx context.Context, log logger.Logger, db dbtypes.Interface, eventID, errMsg string) {
	if markErr := r.store.MarkFailed(ctx, db, eventID, ledgererr.Bound(errMsg)); markErr != nil {
		log.Error().
			Err(markErr).
			Str("eventID", eventID).
			Msg("Failed to mark outbox event as failed")
	}
}

// decodeHeaders unmarshals JSON-encoded headers.
// Returns (nil, nil) on empty input, or an error on invalid JSON.
func decodeHeaders(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var headers map[string]any
	if err := json.Unmarshal(data, &headers); err != nil {
		return nil, fmt.Errorf("outbox relay: invalid headers JSON: %w", err)
	}

	return headers, nil
}
