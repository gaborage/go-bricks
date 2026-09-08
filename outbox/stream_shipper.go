package outbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/messaging/streams"
)

// streamShipper is the native streams lane's adapter (ADR-088). Its handles are per
// super stream, so it has no lane-wide pre-flight: readiness is asked of the target
// producer inside Ship, where an unready one is held back without paying the publish
// bound to discover it.
type streamShipper struct {
	lookup func(name string) (streamPublisher, bool)
}

// newStreamShipper builds the lane's production adapter over a target resolver. The
// resolver is always supplied: a build with no stream targets still hands over one that
// finds nothing, which is what makes a row aimed at a vanished target poison.
func newStreamShipper(lookup func(name string) (streamPublisher, bool)) *streamShipper {
	return &streamShipper{lookup: lookup}
}

// target resolves one super stream's producer. The error it returns is the poison text a
// row naming a target this build has no handle for carries — spelled once, because Plan
// judges it and Ship asks again as a totality guard: the map is written once at
// registration, before any relay job runs, so the second read cannot disagree with the
// first, and Ship stays total for a hand-built shipment that skipped Plan.
func (s *streamShipper) target(name string) (streamPublisher, error) {
	pub, ok := s.lookup(name)
	if !ok {
		return nil, fmt.Errorf("stream %q is not an outbox target", name)
	}
	return pub, nil
}

// Ready reports the lane usable. A stream that is not carrying messages is a per-target
// condition and shows up as shipScopeDown, not as a lane outage.
func (s *streamShipper) Ready(context.Context) error { return nil }

// Plan resolves the row's target and its partition key. Both failures are config drift
// on a persisted row — a target removed from outbox.superstreams between deploys, or a
// row written without a key — and read the same way every cycle, so they are poison. The
// key is set either way, so a refused row still holds its stream's order.
func (s *streamShipper) Plan(rec *Record, headers map[string]any) shipment {
	// A stream row records its tenant as the partition key and never in its headers, and
	// the content type is bookkeeping rather than a property this lane sets — but either
	// header left behind reaches the wire, so both go the same way as on the AMQP lane.
	takeFrameworkStamps(headers)

	sh := shipment{
		Record:  rec,
		Headers: headers,
		Key:     LaneStream + ":" + rec.Stream + ":" + rec.PartitionKey,
		Scope:   rec.Stream,
		Stamp:   rec.PartitionKey,
	}
	switch _, err := s.target(rec.Stream); {
	case err != nil:
		sh.Poison = err.Error()
	case rec.PartitionKey == "":
		sh.Poison = "stream row has no partition key"
	}
	return sh
}

// Ship publishes through the target's producer. The stamp is NOT put in Properties: the
// relay moved it onto the context and the publisher stamps it from there, so the relay
// never supplies a caller-set one (ADR-087).
func (s *streamShipper) Ship(ctx context.Context, sh *shipment) verdict {
	pub, err := s.target(sh.Record.Stream)
	if err != nil {
		return verdict{Kind: shipPoison, Err: err}
	}
	if pub.Closed() {
		// The shutdown window: stopSlots closes the stream publishers BEFORE the scheduler
		// cancels this job's context, so between the two a row meets a closed producer on a
		// context that is still live. That is the process going down, not this row failing
		// — abort, so nothing about it is written.
		return verdict{Kind: shipAborted, Err: streams.ErrPublisherClosed}
	}
	if !pub.Ready() {
		// Ready() reports the HA layer's connection status, so this catches a producer
		// that is reconnecting or not yet bound — and catches it for free, so none of that
		// stream's rows pays the publish bound to discover it. A closed one never reaches
		// here: the check above already read it as a shutdown.
		//
		// Residual, deliberately: an OPEN producer that has stopped confirming still
		// looks ready, so the FIRST such row of a cycle still pays one bound, because
		// that stall is only observable by waiting for it. Bounded at one per stream per
		// cycle rather than one per row.
		return verdict{Kind: shipScopeDown, Err: fmt.Errorf("stream %q is not carrying messages: %w", sh.Record.Stream, streams.ErrPublisherNotStarted)}
	}

	start := time.Now()
	err = pub.Publish(ctx, &streams.PublishMessage{
		Data:       sh.Record.Payload,
		Properties: sh.Headers,
		RoutingKey: sh.Stamp,
	})

	switch {
	case err == nil:
		return verdict{Kind: shipDelivered}
	case errors.Is(err, context.Canceled), errors.Is(err, streams.ErrPublisherClosed):
		return verdict{Kind: shipAborted}
	case errors.Is(err, messaging.ErrTenantStampConflict):
		// Message-intrinsic, as on the AMQP lane: the row will conflict identically
		// every cycle, so it parks instead of retrying forever.
		return verdict{Kind: shipPoison, Err: err}
	case errors.Is(err, streams.ErrPublisherNotStarted), errors.Is(err, context.DeadlineExceeded):
		// Evidence this stream's producer is not carrying messages right now, not that
		// this row is special. Stop spending the bound on that stream's other rows.
		return verdict{Kind: shipScopeDown, Err: err, Waited: time.Since(start)}
	default:
		return verdict{Kind: shipRetry, Err: err}
	}
}
