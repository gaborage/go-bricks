package outbox

import (
	"context"
	"errors"
	"time"

	"github.com/gaborage/go-bricks/internal/publishdoor"
	"github.com/gaborage/go-bricks/messaging"
)

// amqpShipper is the classic AMQP 0.9.1 lane's adapter (ADR-088). It reaches the
// client's unexported byte door through a func field defaulting to publishdoor.Publish
// (ADR-096) — the door's rule and init-registered mechanism are untouched; the field
// exists so an outbox test substitutes a fake instead of swapping the process global.
type amqpShipper struct {
	client  func(context.Context) (messaging.AMQPClient, error)
	publish func(context.Context, messaging.AMQPClient, publishdoor.Options, []byte) error
}

// newAMQPShipper builds the lane's production adapter over a tenant-aware client resolver.
func newAMQPShipper(client func(context.Context) (messaging.AMQPClient, error)) *amqpShipper {
	return &amqpShipper{client: client, publish: registeredDoor}
}

// registeredDoor is the production publish func: the ADR-096 door, adapted to the typed
// signature the field carries. Spelled once, so the constructor and the nil-field fallback
// in Ship cannot drift into publishing through different mechanisms.
func registeredDoor(ctx context.Context, c messaging.AMQPClient, opts publishdoor.Options, data []byte) error {
	return publishdoor.Publish(ctx, c, opts, data)
}

// Ready resolves the tenant's client and asks it, reporting the outage through the same
// helper the mid-batch path uses so an operator reads one text for one condition.
func (s *amqpShipper) Ready(ctx context.Context) error {
	client, err := s.client(ctx)
	if err != nil {
		return brokerUnavailableErr(err)
	}
	if client == nil || !client.IsReady() {
		return brokerUnavailableErr(nil)
	}
	return nil
}

// Plan keys the row by its tenant stamp when it carries one — deliberately spanning that
// tenant's exchanges, which is the ordering an event stream for one tenant needs — and
// otherwise by the destination it is actually published to. The stamp is moved out of the
// headers on PRESENCE, not on a non-empty value: the conflict check keys on the header
// existing at all, so an empty-valued one left behind fails every publish. Nothing here can
// refuse a row, so the shipment is never poison.
func (s *amqpShipper) Plan(rec *Record, headers map[string]any) shipment {
	stamp, _ := headers[messaging.TenantStampHeader].(string)
	delete(headers, messaging.TenantStampHeader)

	key := LaneAMQP + ":" + rec.Exchange + ":" + rec.RoutingKey
	if stamp != "" {
		key = LaneAMQP + ":tenant:" + stamp
	}
	return shipment{Record: rec, Headers: headers, Key: key, Stamp: stamp}
}

// Ship publishes the row's bytes once and reads the failure. Every broker-side failure
// that is not a shutdown and not an unwritable frame is connectivity — a NACK included,
// since a NACK is a transient broker condition rather than a bad message.
func (s *amqpShipper) Ship(ctx context.Context, sh *shipment) verdict {
	start := time.Now()
	client, err := s.client(ctx)
	if err != nil {
		// The client resolved at pre-flight and is gone now: an outage mid-batch, not a
		// fault of this row, so the remainder must not each pay for discovering it. The
		// resolver itself can block (messaging.Manager.Publisher via m.getMsg waits out the
		// bounded publish context before failing), so the elapsed time counts toward
		// stall_wait_ms the same as any other broker-down verdict.
		return verdict{Kind: shipBrokerDown, Err: err, Waited: time.Since(start)}
	}
	if client == nil {
		// Ready already treats a resolver returning (nil, nil) as the lane being
		// unusable; Ship must read the same condition identically instead of falling
		// into the publish path, where the door's own nil-client error would classify
		// as an ordinary retry and advance only this row instead of stopping the cycle.
		return verdict{Kind: shipBrokerDown, Err: brokerUnavailableErr(nil), Waited: time.Since(start)}
	}

	opts := publishdoor.Options{
		Exchange:   sh.Record.Exchange,
		RoutingKey: sh.Record.RoutingKey,
		Headers:    sh.Headers,
	}
	// A zero-value shipper carries no publish func: fall back to the registered door, so
	// an &amqpShipper{} built without the constructor reaches the real ADR-096 dispatcher
	// instead of nil-panicking. The field exists only so a test substitutes a fake.
	publish := s.publish
	if publish == nil {
		publish = registeredDoor
	}
	err = publish(ctx, client, opts, sh.Record.Payload)

	switch {
	case err == nil:
		return verdict{Kind: shipDelivered}
	case errors.Is(err, context.Canceled), errors.Is(err, messaging.ErrShutdown):
		// Shutdown/cancel is not a delivery failure — do not advance retry_count.
		return verdict{Kind: shipAborted}
	case errors.Is(err, messaging.ErrInvalidPublishDestination), errors.Is(err, messaging.ErrTenantStampConflict):
		// A frame the wire can never carry, or a stamp the row contradicts: both read
		// the same way every cycle, so they park rather than retrying forever.
		return verdict{Kind: shipPoison, Err: err}
	case errors.Is(err, messaging.ErrNotConnected) && !client.IsReady():
		// Checked fresh, not inferred from the error alone: a flap that already
		// recovered is an ordinary failure and the batch keeps going.
		return verdict{Kind: shipBrokerDown, Err: err, Waited: time.Since(start)}
	default:
		return verdict{Kind: shipRetry, Err: err}
	}
}
