package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gaborage/go-bricks/config"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/leasescope"
	"github.com/gaborage/go-bricks/internal/ledgererr"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/scheduler"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// Relay is a scheduler.Executor that polls for pending outbox events
// and publishes them to the message broker via existing AMQP infrastructure.
//
// The relay runs as a scheduled job (registered via scheduler.FixedRate),
// getting overlapping prevention, panic recovery, and OTel metrics for free.
//
// Resources are resolved through the tenant-aware getDB/getMessaging resolvers
// (the module's deps.DB/deps.Messaging) rather than the scheduler JobContext,
// because the scheduler builds the JobContext from a tenant-less context — so
// in multi-tenant mode the relay must inject each tenant into the context
// itself before resolving that tenant's database and broker.
type Relay struct {
	store        Store
	config       config.OutboxConfig
	getDB        func(context.Context) (dbtypes.Interface, error)
	getMessaging func(context.Context) (messaging.AMQPClient, error)
	// tenants lists the tenant keys to relay each cycle. Always non-empty: a single
	// "" entry for single-tenant mode (multitenant.SetTenant with "" is a no-op) and
	// for shared (control-plane) tenancy, or the configured static multitenant tenant
	// IDs. In per-tenant tenancy, dynamic multi-tenant sources are rejected at module
	// Init (their tenant set is not enumerable at registration time).
	tenants []string
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
	outcomeAborted                                   // interrupted by shutdown/cancel; do NOT count, stop the batch
	// outcomeBrokerDown signals that THIS record's publish failed with a connectivity
	// error (messaging.ErrNotConnected) AND the client is still not ready right now —
	// i.e. the broker dropped mid-batch rather than being down at cycle start. This
	// record keeps normal failed accounting (markRecordFailed already ran); the
	// caller stops the loop and routes the UNATTEMPTED remainder through the same
	// outage semantics markOutage applies at cycle start, instead of letting every
	// remaining record pay its own serial readiness pre-flight wait.
	outcomeBrokerDown
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

	// Resolve the broker, but TOLERATE an unresolved/not-ready broker rather than
	// skipping the batch. The old early-return froze retry_count while the broker was
	// down (the reported bug); now we still fetch and advance every pending record, so
	// operators see retry_count climb during an outage.
	msgClient, msgErr := r.getMessaging(ctx)
	brokerUsable := msgErr == nil && msgClient != nil && msgClient.IsReady()

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
		// No undelivered work — an idle relay is not a failure even if the broker is down.
		return nil
	}

	// Outage path: the broker is unreachable/not-ready but there ARE pending events.
	// Advance every record's retry_count (the operator's "still retrying" signal) without
	// a publish call, then return a job error so the failure stays visible at the scheduler
	// level (and, in multi-tenant mode, names the affected tenant) instead of silently
	// reporting success forever under a permanent misconfiguration.
	if !brokerUsable {
		r.markOutage(ctx, log, db, lead, records)
		return brokerUnavailableErr(msgErr)
	}

	res := r.runRelayLoop(ctx, log, db, msgClient, lead, records)

	r.logCycle(log, tenantID, res, len(records))
	// Leadership loss first: it is a database-side failure, and wrapping it as a broker
	// outage would send an operator to the wrong system. The next tick re-leads.
	if res.leadershipErr != nil {
		return res.leadershipErr
	}
	if res.outageErr != nil {
		return brokerUnavailableErr(res.outageErr)
	}
	return nil
}

// relayBatchResult holds the per-record bookkeeping counts from one relay cycle's
// publish loop, plus (if the batch stopped early on a mid-batch broker drop) the
// outage error the caller surfaces at the job level.
type relayBatchResult struct {
	published, unrecorded, failed, deadlettered, parked int
	outageErr                                           error
	// leadershipErr is set when the claim on the leader row was lost mid-cycle. It is kept
	// apart from outageErr because its cause is the DATABASE, not the broker: reporting it
	// as a broker outage would point an operator at the wrong system.
	leadershipErr error
}

// runRelayLoop publishes each pending record in order, stopping early on
// shutdown/cancel, a shutdown-aborted publish, or a mid-batch broker drop
// (outcomeBrokerDown — see publishRecord). Split out of relayTenant to keep
// that function's cyclomatic complexity within budget (gocyclo).
func (r *Relay) runRelayLoop(ctx context.Context, log logger.Logger, db dbtypes.Interface, msgClient messaging.AMQPClient, lead Leadership, records []Record) relayBatchResult {
	var res relayBatchResult
	// Keys whose head failed this cycle. A later row of a parked key is left untouched —
	// not even its retry_count moves — so the key keeps its order across cycles.
	parked := make(map[string]struct{})
	for i := range records {
		// Stop cleanly on shutdown/cancel: leave the rest pending for the next startup
		// rather than bumping their retry_count on the way down.
		if ctx.Err() != nil {
			break
		}
		// A deposed leader must not publish another row: another instance may already be
		// draining the same ledger.
		if err := lead.Probe(ctx); err != nil {
			log.Warn().Err(err).Msg("Outbox relay lost its leader row mid-cycle; stopping")
			res.leadershipErr = fmt.Errorf("%w: lost the leader row mid-cycle, database unreachable or the transaction was ended: %w", ErrNotLeader, err)
			return res
		}
		// Decode once per record: the key derivation and the publish need the same blob.
		// A decode failure yields nil headers, so the key falls back to the routing key
		// while publishRecord dead-letters the row as poison.
		headers, decodeErr := decodeHeaders(records[i].Headers)
		key := relayKey(&records[i], headers)
		if _, isParked := parked[key]; isParked {
			res.parked++
			continue
		}
		outcome, pubErr := r.publishRecord(ctx, log, db, msgClient, &records[i], headers, decodeErr)
		switch outcome {
		case outcomePublished:
			res.published++
		case outcomePublishedUnrecorded:
			res.unrecorded++
		case outcomeFailed:
			res.failed++
			// Only a failure parks: a dead-lettered row is terminal and an unrecorded one
			// was delivered, so neither blocks its key.
			parked[key] = struct{}{}
		case outcomeDeadLettered:
			res.deadlettered++
		case outcomeBrokerDown:
			// The broker dropped mid-batch: this record's own failed accounting
			// already ran inside publishRecord. Route the UNATTEMPTED remainder
			// through the same outage path markOutage applies at cycle start —
			// advance retry_count without paying each record's own serial
			// readiness pre-flight wait — and stop the loop. The remainder counts
			// as failed too (markOutage marked it in the DB), so logCycle's counts
			// still sum to the batch total.
			res.failed++
			res.outageErr = pubErr
			res.failed += r.markOutage(ctx, log, db, lead, records[i+1:])
			return res
		case outcomeAborted:
			// Shutting down mid-publish — stop without counting this record.
			return res
		}
	}
	return res
}

// tenantStampHeader is the header the messaging layer stamps a tenant into. Mirrored here
// because messaging keeps its constant unexported; it becomes an import once that is exported.
const tenantStampHeader = "x-tenant-id"

// relayKey is the key a row is ordered under within a cycle. Every key is namespaced by the
// scope it orders within, because parking is head-of-line blocking: two rows that share a key
// but not a destination block each other for nothing.
//
// Stream lane: the stream AND the partition key, so rows aimed at different super streams do
// not park each other merely for carrying the same tenant's key. AMQP lane: the tenant stamp
// when the row carries one — deliberately spanning that tenant's exchanges, which is the
// ordering an event stream for one tenant needs — and otherwise the destination the row is
// actually published to, exchange and routing key together.
//
// The lane prefix keeps the two spaces apart, so a stream row partitioned by "acme" and an
// AMQP row stamped "acme" are not treated as one key. A row whose headers will not decode
// arrives with nil headers and is keyed by its destination; it is poison and publishRecord
// dead-letters it, so it holds its siblings only until it does.
func relayKey(record *Record, headers map[string]any) string {
	if record.Lane == LaneStream {
		return LaneStream + ":" + record.Stream + ":" + record.PartitionKey
	}
	if stamp, ok := headers[tenantStampHeader].(string); ok && stamp != "" {
		return LaneAMQP + ":tenant:" + stamp
	}
	return LaneAMQP + ":" + record.Exchange + ":" + record.RoutingKey
}

// markOutage advances retry_count for every pending record without attempting a publish,
// used when the broker is unreachable/not-ready. Stops early on shutdown/cancel so a
// shutdown does not inflate retry_count for records it never got to. Returns how many
// records it actually marked (fewer than len(records) on early stop) so callers can
// fold the outage remainder into their cycle accounting.
func (r *Relay) markOutage(ctx context.Context, log logger.Logger, db dbtypes.Interface, lead Leadership, records []Record) int {
	for i := range records {
		if ctx.Err() != nil {
			return i
		}
		// A mark is a write, so it needs the same leadership guarantee as a publish.
		if err := lead.Probe(ctx); err != nil {
			log.Warn().Err(err).Msg("Outbox relay lost leadership while marking an outage; stopping")
			return i
		}
		r.markRecordFailed(ctx, log, db, records[i].ID, "messaging unavailable")
	}
	return len(records)
}

// logCycle emits the per-cycle delivery summary. "unrecorded" counts events delivered to the
// broker whose MarkPublished failed (they re-deliver next cycle) — kept distinct from
// "published" so the success count is not inflated by stuck-but-delivered records.
func (r *Relay) logCycle(log logger.Logger, tenantID string, res relayBatchResult, total int) {
	event := log.Info().
		Int("published", res.published).
		Int("unrecorded", res.unrecorded).
		Int("failed", res.failed).
		Int("deadlettered", res.deadlettered).
		Int("parked", res.parked).
		Int("total", total)
	if tenantID != "" {
		event = event.Str("tenant", tenantID)
	}
	if r.config.Tenancy == config.TenancyShared {
		event = event.Str("tenancy", "shared")
	}
	event.Msg("Outbox relay cycle completed")
}

// brokerUnavailableErr builds the job-level error returned when a relay cycle had pending
// work but the broker was unreachable or not ready, so the delivery failure stays visible at
// the scheduler level rather than silently succeeding.
func brokerUnavailableErr(msgErr error) error {
	if msgErr != nil {
		return fmt.Errorf("messaging not available: %w", msgErr)
	}
	return errors.New("messaging not ready")
}

// publishRecord attempts to publish a single outbox record and returns the bookkeeping
// outcome. All Mark* calls run on the parent ctx; the per-record publish deadline applies
// only to the publish itself, so an expired deadline never fails the bookkeeping UPDATE.
// The second return is non-nil only for outcomeBrokerDown, carrying the connectivity
// error the caller surfaces as the job-level outage error (see relayTenant).
func (r *Relay) publishRecord(ctx context.Context, log logger.Logger, db dbtypes.Interface, msgClient messaging.AMQPClient, record *Record, headers map[string]any, decodeErr error) (publishOutcome, error) {
	// Corrupt headers are deterministic, broker-independent corruption (poison), so they
	// dead-letter at MaxRetries rather than retrying forever. Poison has one other class: a
	// destination the frame can never carry (below). Every remaining broker-side publish
	// failure is connectivity. The decode itself happens once per record in the caller, which
	// also needs it to derive the ordering key.
	if decodeErr != nil {
		return r.deadLetterPoison(ctx, log, db, record, decodeErr.Error()), nil
	}

	// Inject outbox metadata headers for consumer idempotency
	if headers == nil {
		headers = make(map[string]any)
	}
	headers[HeaderEventID] = record.ID
	headers[HeaderEventType] = record.EventType

	// Rehydrate the originating trace context (persisted by Publish) into the
	// publish context. The relay job runs detached with no ambient trace, so
	// without this the downstream preparePublishing would stamp a freshly
	// generated CorrelationId — which the consumer's failure-path logger and
	// consume span surface — breaking trace continuity on the error path.
	pubCtx := gobrickstrace.ExtractFromHeaders(ctx, &mapHeaderAccessor{headers: headers})

	opts := messaging.PublishOptions{
		Exchange:   record.Exchange,
		RoutingKey: record.RoutingKey,
		Headers:    headers,
	}

	// Bound this single publish so one stuck record cannot block the whole cycle and
	// starve the rest of the batch. cancel() is called immediately; Mark* below uses
	// the parent ctx, never the (possibly expired) recCtx.
	recCtx, cancel := context.WithTimeout(pubCtx, r.config.PublishTimeout)
	err := msgClient.PublishToExchange(recCtx, opts, record.Payload)
	cancel()
	if err != nil {
		// Shutdown/cancel is not a delivery failure — do not advance retry_count.
		if errors.Is(err, context.Canceled) || errors.Is(err, messaging.ErrShutdown) {
			return outcomeAborted, nil
		}
		// A destination the frame can never carry (over-long exchange, routing key or
		// header key) is refused before any channel work and is refused identically
		// every cycle: message-intrinsic, so it is poison on the same path as an
		// undecodable header rather than connectivity.
		if errors.Is(err, messaging.ErrInvalidPublishDestination) {
			return r.deadLetterPoison(ctx, log, db, record, err.Error()), nil
		}
		// Every other broker-side publish failure — NOT-connected, confirmation timeout, deadline,
		// and even a broker NACK — is CONNECTIVITY: the broker either could not be reached or
		// could not take responsibility for the message. A RabbitMQ NACK is a transient broker
		// condition (disk alarm, mirror resync, failover), and a missing exchange surfaces as a
		// synthesized NACK too — neither means the message is bad, so neither parks. We advance
		// retry_count and retry (at-least-once); only the two poison classes above — undecodable
		// headers and an unpublishable destination — ever park.
		r.markRecordFailed(ctx, log, db, record.ID, err.Error())
		// A not-connected error AND a still-not-ready client (checked fresh, not
		// inferred from the error alone) means the broker dropped mid-batch — the
		// caller stops attempting the rest rather than letting each remaining
		// record pay its own serial readiness pre-flight wait. If IsReady() has
		// already flipped back to true (a brief flap), fall through to the normal
		// per-record failed outcome and keep the batch going.
		if errors.Is(err, messaging.ErrNotConnected) && !msgClient.IsReady() {
			return outcomeBrokerDown, err
		}
		return outcomeFailed, nil
	}

	if err := r.store.MarkPublished(ctx, db, record.ID); err != nil {
		log.Error().
			Err(err).
			Str("eventID", record.ID).
			Msg("Failed to mark outbox event as published")
		// The message WAS delivered; do not bump retry_count. It re-delivers next
		// cycle and the consumer dedups via the x-outbox-event-id header.
		return outcomePublishedUnrecorded, nil
	}

	return outcomePublished, nil
}

// deadLetterPoison handles a poison record — undecodable headers, or a destination past the
// AMQP shortstr ceiling that no broker state can accept: it advances retry_count and,
// once the record has reached MaxRetries, parks it to status=failed (the only auto-parking path).
// Below the ceiling it just advances retry_count and leaves the record pending. Connectivity
// failures never reach here — they call markRecordFailed directly and are never parked.
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
