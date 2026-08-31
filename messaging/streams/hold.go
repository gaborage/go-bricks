package streams

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/internal/ledgererr"
	"github.com/gaborage/go-bricks/internal/streamruntime"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/delivery"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
	"github.com/gaborage/go-bricks/multitenant"
)

// The hold port lives on the streamruntime seam so inbox can implement it
// without importing this package (ADR-091). The aliases keep the streams
// spelling for lane callers.
type (
	HeldMessage  = streamruntime.HeldMessage
	HoldLedger   = streamruntime.HoldLedger
	HoldReplayer = streamruntime.HoldReplayer
)

// heldSet is one consumer's view of which tenants are held, plus whether the
// gate is CLOSED — the state a partition is in between a promotion and the
// reload that tells it what the ledger holds.
//
// Every write goes through the same lock, and every replace carries the
// generation it read at: a park landing mid-read must not be erased by a listing
// taken before it, so a stale replace is refused rather than applied.
type heldSet struct {
	mu      sync.RWMutex
	tenants map[string]struct{}
	// generation advances on every add, so a replace can tell whether the listing
	// it carries still describes the set it was read from.
	generation uint64
	// closed means the ledger has not been read yet for this partition. Nothing is
	// delivered while it is up: an empty set would let a held tenant's later
	// message run ahead of the one it is held behind.
	closed bool
	// epoch advances on every promotion. A reload carries the epoch it was started
	// for, so a slow one from an EARLIER promotion cannot open a gate a LATER
	// promotion closed — the set it read describes a partition this member may no
	// longer own the same way.
	epoch uint64
	// opened broadcasts the gate opening to deliveries waiting on it.
	opened chan struct{}
}

func newHeldSet() *heldSet {
	return &heldSet{tenants: map[string]struct{}{}, opened: make(chan struct{})}
}

// has reports whether this tenant is held. It is the gate every delivery passes.
func (s *heldSet) has(tenant string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, held := s.tenants[tenant]
	return held
}

// gateClosed reports whether the partition may deliver at all.
func (s *heldSet) gateClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

// closeGate stops deliveries until a reload lands, and returns the epoch that
// reload must carry back. Called on promotion, where the set this member
// inherited may be missing tenants another owner parked while it stood by.
func (s *heldSet) closeGate() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.closed {
		s.closed = true
		s.opened = make(chan struct{})
	}
	s.epoch++
	return s.epoch
}

// awaitOpen blocks until the gate opens or the consumer stops, reporting whether
// the partition may now deliver. It returns immediately on an open gate.
func (s *heldSet) awaitOpen(ctx context.Context) bool {
	s.mu.RLock()
	closed, opened := s.closed, s.opened
	s.mu.RUnlock()

	if !closed {
		return true
	}

	select {
	case <-opened:
		return true
	case <-ctx.Done():
		return false
	}
}

// add holds one tenant, which is what a park does to the partition that owns it,
// and advances the generation so a listing read before it cannot erase it.
func (s *heldSet) add(tenant string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants[tenant] = struct{}{}
	s.generation++
}

// generationAt is the generation a caller must pass back to replace what it read.
func (s *heldSet) generationAt() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation
}

// replace swaps the whole set for what the ledger reported, and opens the gate.
// It REFUSES a listing read before a park landed — reporting false so the caller
// reads again — because applying it would release a tenant the ledger has not
// been asked about yet, and one delivery would run ahead of its replay.
func (s *heldSet) replace(generation, epoch uint64, tenants []string) bool {
	next := make(map[string]struct{}, len(tenants))
	for _, tenant := range tenants {
		next[tenant] = struct{}{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation || s.epoch != epoch {
		return false
	}

	s.tenants = next
	if s.closed {
		s.closed = false
		close(s.opened)
	}
	return true
}

// epochAt is the promotion epoch a caller must pass back to open the gate it
// closed.
func (s *heldSet) epochAt() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.epoch
}

// holdBackoffDefault and holdBackoffMax bound the wait between failed ledger
// writes while a partition is stalled.
const (
	holdBackoffDefault = 200 * time.Millisecond
	holdBackoffMax     = 5 * time.Second
)

// backoffSeries is the wait before each retry of a hold's ledger call: the
// configured first wait, doubling, capped. Both stalls — the park and the
// promotion reload — take their waits from one of these rather than keeping the
// same arithmetic twice.
type backoffSeries struct {
	next time.Duration
	max  time.Duration
}

func newBackoffSeries(first, maxWait time.Duration) *backoffSeries {
	if first <= 0 {
		first = holdBackoffDefault
	}
	return &backoffSeries{next: min(first, maxWait), max: maxWait}
}

// take returns the next wait and advances the series.
func (b *backoffSeries) take() time.Duration {
	wait := b.next
	b.next = min(b.next*2, b.max)
	return wait
}

const (
	holdParkedMsg = "Tenant held: delivery parked"
	// attrHoldGated marks a span whose delivery was parked WITHOUT running, so a
	// success on that span is not mistaken for a handled message.
	attrHoldGated = "messaging.hold.gated"
	// attrHoldReplay marks a delivery the drain put back through the lane, so a
	// replayed failure is not read as a fresh one.
	attrHoldReplay = "messaging.hold.replay"
)

// heldMessageOf renders one delivery as the ledger sees it.
func heldMessageOf(consumer, streamName string, offset int64, tenant string, msg *Message, raw *amqp.Message) *HeldMessage {
	return &HeldMessage{
		Consumer:   consumer,
		Stream:     streamName,
		Offset:     offset,
		TenantID:   tenant,
		Data:       msg.Data,
		Properties: raw.ApplicationProperties,
		HeldAt:     time.Now(),
	}
}

// gates reports whether this delivery must be parked without running: its tenant
// is already held, so running it would deliver a message ahead of the one it is
// held behind.
func (r *consumerRunner) gates(tenant string) bool {
	return r.hold != nil && tenant != "" && r.held.has(tenant)
}

// parks reports whether a finished delivery's failure is this runner's to park.
// A delivery with no tenant is skipped as it always was: a hold is keyed by the
// tenant, and there is nothing to key this one on.
func (r *consumerRunner) parks(res *delivery.Result, tenant string) bool {
	// res.Err is the whole test: it is non-nil for exactly the two failing
	// outcomes, so naming them as well would add a clause no delivery can
	// disagree with.
	return r.hold != nil && tenant != "" && res.Err != nil
}

// park writes one delivery to the ledger, retrying until it lands or the consumer
// stops. The write happens inside the partition's own delivery callback, so a
// ledger that is down STALLS this partition rather than dropping the message —
// nothing is committed while stalled, and a restart redelivers from the last
// committed offset.
func (r *consumerRunner) park(ctx context.Context, msg *HeldMessage, gated bool) error {
	if gated {
		trace.SpanFromContext(ctx).SetAttributes(attribute.Bool(attrHoldGated, true))
	}

	backoff := newBackoffSeries(r.holdBackoff, holdBackoffMax)
	logged := false

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := r.hold.Park(ctx, msg)
		if err == nil {
			r.held.add(msg.TenantID)
			return nil
		}

		if !logged {
			// Once: a ledger outage would otherwise write a line per retry per
			// partition, and the first one already says the partition is stalled.
			logged = true
			// The context-bound logger, not r.log: during a ledger outage this is the
			// only signal, and it correlates with the delivery that stalled exactly as
			// every other per-delivery line in this lane does.
			r.log.WithContext(ctx).Error().Err(err).
				Str(logFieldStream, msg.Stream).
				Str(logFieldConsumer, r.name).
				Int64(logFieldOffset, msg.Offset).
				Msg("Hold ledger write failed; partition stalled until it succeeds")
		}

		if !delivery.Wait(ctx, backoff.take()) {
			return ctx.Err()
		}
	}
}

// parkFailed settles a failed delivery into the hold. The offset commits once the
// ledger has the message: it is durable there, and leaving it uncommitted would
// redeliver a message the ledger already owns. A park that could not be written
// commits nothing.
func (r *consumerRunner) parkFailed(res *delivery.Result, streamName string, offset int64,
	tenant string, msg *Message, raw *amqp.Message, store offsetStorer,
) {
	held := heldMessageOf(r.name, streamName, offset, tenant, msg, raw)
	if err := r.park(r.baseCtx, held, false); err != nil {
		return
	}

	event := res.Log.Warn().
		Str(logFieldStream, streamName).
		Str(logFieldConsumer, r.name).
		Int64(logFieldOffset, offset).
		Str("tenant", tenant).
		Int("attempts", res.Attempts).
		Str("error_type", fmt.Sprintf("%T", res.Err))
	if res.Outcome == delivery.HandlerError {
		// The handler's own text, bounded the way the ledger column bounds it: the
		// message is consumer-written and reaches this line as well as the row.
		event = event.Str("error", ledgererr.Bound(res.Err.Error()))
	}
	event.Msg(holdParkedMsg)

	// Recorded as a success: the delivery is settled, just not by the handler.
	r.recordSettled(res.Log, streamName, offset, nil, store)
}

// requireHoldLedger refuses a declaration set that asks for a hold this manager
// cannot provide. Checked before the dial: the answer does not depend on the
// broker, and failing after it would leak a connection.
func (m *Manager) requireHoldLedger(decls *Declarations) error {
	if m.opts.Hold != nil {
		return nil
	}
	for _, decl := range decls.consumers {
		if decl.Hold {
			return fmt.Errorf(
				"streams: consumer %q on %s %q declares Hold but no hold ledger is configured; "+
					"set inbox.enabled, inbox.tenancy: shared and inbox.hold.enabled",
				decl.Name, streamKindLabel(decl.Super), decl.Stream)
		}
	}
	return nil
}

// loadHeld fills a holding consumer's set before it consumes. A failure fails
// startup: a partition that does not know which tenants are held would deliver a
// held tenant's later message ahead of the one it is held behind.
//
// The listing is applied only if no park landed while it was being read. A park
// during the read means the ledger answered before that tenant was in it, so the
// listing would release a tenant that is in fact held; the read simply runs again.
func (m *Manager) loadHeld(ctx context.Context, runner *consumerRunner) error {
	return m.loadHeldForEpoch(ctx, runner, runner.held.epochAt())
}

// loadHeldForEpoch is loadHeld for one promotion. A reload started by an earlier
// promotion carries that promotion's epoch, so it cannot open a gate a later one
// closed: the listing it read describes a partition this member may no longer own
// the same way, and the later promotion's own reload is the one that speaks.
func (m *Manager) loadHeldForEpoch(ctx context.Context, runner *consumerRunner, epoch uint64) error {
	if runner.hold == nil {
		return nil
	}

	for {
		generation := runner.held.generationAt()

		tenants, err := runner.hold.HeldTenants(ctx, runner.name)
		if err != nil {
			return fmt.Errorf("failed to load held tenants for consumer %q: %w", runner.name, err)
		}

		if runner.held.replace(generation, epoch, tenants) {
			return nil
		}
		if runner.held.epochAt() != epoch {
			// A later promotion owns the gate now; its reload will open it.
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// reloadHeldOnPromotion closes this partition's gate and RETURNS, leaving the
// reload to the runner's own goroutine.
//
// The client calls the promotion callback synchronously from the connection's
// frame reader, which every consumer on that connection shares — waiting here for
// a ledger that is down would stop delivering for all of them, not just this
// partition. So the fail-closed rule is kept by the GATE rather than by blocking:
// nothing is delivered until the reload lands.
func (m *Manager) reloadHeldOnPromotion(runner *consumerRunner) {
	if runner.hold == nil {
		return
	}

	epoch := runner.held.closeGate()
	go m.reloadUntilLoaded(runner, epoch)
}

// reloadUntilLoaded retries the ledger read until it lands or the consumer stops.
// It runs off the frame reader, so a slow ledger costs this partition's throughput
// and nothing else.
func (m *Manager) reloadUntilLoaded(runner *consumerRunner, epoch uint64) {
	backoff := newBackoffSeries(runner.holdBackoff, holdBackoffMax)
	logged := false

	for {
		if err := m.loadHeldForEpoch(runner.baseCtx, runner, epoch); err == nil {
			return
		} else if !logged {
			// Once: a promotion storm during a ledger outage should not write a line
			// per retry per partition.
			logged = true
			m.log.Error().Err(err).
				Str(logFieldConsumer, runner.name).
				Msg("Could not reload held tenants on promotion; not delivering until it succeeds")
		}

		if !delivery.Wait(runner.baseCtx, backoff.take()) {
			// The consumer is stopping. The gate stays closed, and nothing was
			// delivered — a restart reloads before it takes a message.
			return
		}
	}
}

// Manager is the replayer the hold's drain drives: it owns the running consumers,
// which is what a held message has to go back through.
var _ HoldReplayer = (*Manager)(nil)

// HoldConsumers names the running consumers that hold. A consumer that does not
// hold has nothing parked, so the drain never asks about it.
func (m *Manager) HoldConsumers() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var names []string
	for _, consumer := range m.consumers {
		if consumer.runner != nil && consumer.runner.hold != nil {
			names = append(names, consumer.name)
		}
	}
	return names
}

// ReloadHeld refreshes one consumer's held set from the ledger. A consumer this
// replica does not run is a no-op: the ledger is shared and deployments differ in
// which consumers they start.
//
// The read happens here rather than in the caller because the generation that
// makes the replace safe must be taken BEFORE it. A caller that read the ledger
// first could only hand back a token taken after its own read, which compares
// equal to itself and erases any park that landed in between.
func (m *Manager) ReloadHeld(ctx context.Context, consumer string) error {
	runner := m.runnerFor(consumer)
	if runner == nil {
		return nil
	}
	return m.loadHeldForEpoch(ctx, runner, runner.held.epochAt())
}

// Replay puts a held message back through the lane. It returns the handler's own
// error untouched: the drain decides what a failed replay means — defer the
// tenant — and this call settles nothing, because the row's fate is the drain's
// to write.
func (m *Manager) Replay(ctx context.Context, consumer string, msg *HeldMessage) error {
	runner := m.runnerFor(consumer)
	if runner == nil {
		return fmt.Errorf("streams: no running consumer %q to replay through", consumer)
	}
	return runner.replay(ctx, msg)
}

// runnerFor snapshots one consumer's runner under the lock, so a replay does not
// hold it across a handler.
func (m *Manager) runnerFor(consumer string) *consumerRunner {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, running := range m.consumers {
		if running.name == consumer && running.runner != nil {
			return running.runner
		}
	}
	return nil
}

// replay runs one held message through the pipeline again. The tenant comes from
// the ROW, not from a stamp: the carrier is whatever the producer sent, and the
// hold already decided whose message this is. No Retry — the drain's own
// per-tenant backoff is the retry — and no Settle, because there is no offset to
// commit: this delivery's record is the ledger row the drain deletes or keeps.
func (r *consumerRunner) replay(ctx context.Context, msg *HeldMessage) error {
	res := delivery.Run(ctx, &delivery.Request{
		Carrier:     propertyAccessor(msg.Properties),
		Destination: msg.Stream,
		BodySize:    len(msg.Data),
		SpanExtras: []attribute.KeyValue{
			attribute.String(AttrConsumerName, r.name),
			attribute.Int64(attrStreamOffset, msg.Offset),
			attribute.Bool(attrHoldReplay, true),
		},
		Metrics: tracking.StreamConsumeAttributes(msg.Stream),
		Log:     r.log,
		Handle: func(msgCtx context.Context, _ logger.Logger, _ string) error {
			return r.handler(multitenant.SetTenant(msgCtx, msg.TenantID), &Message{
				Data:       msg.Data,
				Offset:     msg.Offset,
				Stream:     msg.Stream,
				Properties: msg.Properties,
			})
		},
		LogOutcome: func(res *delivery.Result) {
			r.logReplayOutcome(res, msg)
		},
	})
	return res.Err
}

// logReplayOutcome writes the lane's line for a replayed delivery, marked as one
// so an operator reading the failures can tell a retry of a held message from a
// message failing for the first time.
func (r *consumerRunner) logReplayOutcome(res *delivery.Result, msg *HeldMessage) {
	if res.Outcome == delivery.Succeeded {
		return
	}

	event := delivery.AppendOutcome(res.Log.Error(), res).
		Str(logFieldStream, msg.Stream).
		Str(logFieldConsumer, r.name).
		Int64(logFieldOffset, msg.Offset).
		Str("tenant", msg.TenantID).
		Bool("hold_replay", true)
	if res.Outcome == delivery.HandlerError {
		event = event.Str("error", ledgererr.Bound(res.Err.Error()))
	}
	event.Msg("Hold replay failed")
}
