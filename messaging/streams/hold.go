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
	"github.com/gaborage/go-bricks/messaging/internal/delivery"
)

// HeldMessage is one parked delivery as the ledger sees it. It carries the bytes
// rather than a reference, because the delivery that produced it is settled and
// gone by the time anything replays it.
type HeldMessage struct {
	Consumer   string
	Stream     string
	Offset     int64
	TenantID   string
	Data       []byte
	Properties map[string]any
	HeldAt     time.Time
}

// HoldLedger is the port a runner parks through. It is implemented outside this
// package — the ledger is two tables on the control-plane database, which this
// package cannot reach without an import cycle.
//
// Park is idempotent on (Consumer, Stream, Offset) and marks the tenant held in
// the same write: a row whose tenant is not held would be replayed by nothing.
type HoldLedger interface {
	Park(ctx context.Context, msg *HeldMessage) error
	HeldTenants(ctx context.Context, consumer string) ([]string, error)
}

// HoldReplayer is what the drain drives to put a held message back through the
// lane, and how it tells a runner which tenants the ledger still holds.
type HoldReplayer interface {
	HoldConsumers() []string
	Replay(ctx context.Context, consumer string, msg *HeldMessage) error
	ReloadHeld(consumer string, tenants []string)
}

// heldSet is one consumer's view of which tenants are held. Partitions read it on
// every delivery from their own goroutines while the drain replaces it from its
// own, so it is guarded rather than merely published.
type heldSet struct {
	mu      sync.RWMutex
	tenants map[string]struct{}
}

func newHeldSet() *heldSet {
	return &heldSet{tenants: map[string]struct{}{}}
}

// has reports whether this tenant is held. It is the gate every delivery passes.
func (s *heldSet) has(tenant string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, held := s.tenants[tenant]
	return held
}

// add holds one tenant, which is what a park does to the partition that owns it.
func (s *heldSet) add(tenant string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants[tenant] = struct{}{}
}

// replace swaps the whole set for what the ledger reports. A tenant the listing
// omits is released here: only the ledger knows a replay finally succeeded.
func (s *heldSet) replace(tenants []string) {
	next := make(map[string]struct{}, len(tenants))
	for _, tenant := range tenants {
		next[tenant] = struct{}{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants = next
}

// holdBackoffDefault and holdBackoffMax bound the wait between failed ledger
// writes while a partition is stalled.
const (
	holdBackoffDefault = 200 * time.Millisecond
	holdBackoffMax     = 5 * time.Second
)

const (
	holdParkedMsg = "Tenant held: delivery parked"
	// attrHoldGated marks a span whose delivery was parked WITHOUT running, so a
	// success on that span is not mistaken for a handled message.
	attrHoldGated = "messaging.hold.gated"
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
	return r.hold != nil && tenant != "" && res.Err != nil &&
		(res.Outcome == delivery.HandlerError || res.Outcome == delivery.Panicked)
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

	wait := r.holdBackoff
	if wait <= 0 {
		wait = holdBackoffDefault
	}

	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := r.hold.Park(ctx, msg)
		if err == nil {
			r.held.add(msg.TenantID)
			return nil
		}

		// The context-bound logger, not r.log: during a ledger outage this is the
		// only repeating signal, and it correlates with the delivery that stalled
		// exactly as every other per-delivery line in this lane does.
		r.log.WithContext(ctx).Error().Err(err).
			Str(logFieldStream, msg.Stream).
			Str(logFieldConsumer, r.name).
			Int64(logFieldOffset, msg.Offset).
			Int("attempt", attempt).
			Msg("Hold ledger write failed; partition stalled until it succeeds")

		if !delivery.Wait(ctx, wait) {
			return ctx.Err()
		}
		wait *= 2
		if wait > holdBackoffMax {
			wait = holdBackoffMax
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
func (m *Manager) loadHeld(ctx context.Context, runner *consumerRunner) error {
	if runner.hold == nil {
		return nil
	}

	tenants, err := runner.hold.HeldTenants(ctx, runner.name)
	if err != nil {
		return fmt.Errorf("failed to load held tenants for consumer %q: %w", runner.name, err)
	}
	runner.held.replace(tenants)
	return nil
}

// reloadHeldOnPromotion refreshes the set when this member is promoted to a
// partition, and does not let the partition consume until it succeeds.
//
// The client offers no way to refuse a promotion, so "does not consume" is
// spelled as blocking this callback: it returns only once the ledger answered,
// or once the consumer is stopping. Consuming from a STALE set would deliver a
// held tenant's later message ahead of the one it is held behind — the ordering
// the hold exists to keep — and a set is at its stalest right here, on a member
// that has been standing by while another partition owner parked tenants.
func (m *Manager) reloadHeldOnPromotion(runner *consumerRunner) {
	if runner.hold == nil {
		return
	}

	wait := runner.holdBackoff
	if wait <= 0 {
		wait = holdBackoffDefault
	}

	for attempt := 1; ; attempt++ {
		if err := m.loadHeld(runner.baseCtx, runner); err == nil {
			return
		} else if attempt == 1 {
			// Once: a ledger outage during a promotion storm should not write a line
			// per retry per partition.
			m.log.Error().Err(err).
				Str(logFieldConsumer, runner.name).
				Msg("Could not reload held tenants on promotion; not consuming until it succeeds")
		}

		if !delivery.Wait(runner.baseCtx, wait) {
			// The consumer is stopping. Nothing has been consumed on this partition,
			// so a restart reloads the set before it takes a message.
			return
		}
		wait *= 2
		if wait > holdBackoffMax {
			wait = holdBackoffMax
		}
	}
}
