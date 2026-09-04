package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/gaborage/go-bricks/app"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/messaging"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// outboxPublisher implements app.OutboxPublisher by writing events to the outbox table
// within the caller's database transaction.
type outboxPublisher struct {
	store           Store
	defaultExchange string
	streamTargets   []string
}

func newPublisher(store Store, defaultExchange string, superStreams []string) app.OutboxPublisher {
	return &outboxPublisher{
		store:           store,
		defaultExchange: defaultExchange,
		streamTargets:   superStreams,
	}
}

func (p *outboxPublisher) Publish(ctx context.Context, tx dbtypes.Tx, event *app.OutboxEvent) (string, error) {
	if tx == nil {
		return "", errors.New("outbox: transaction must not be nil")
	}

	if event == nil {
		return "", errors.New("outbox: event must not be nil")
	}

	if event.EventType == "" {
		return "", errors.New("outbox: event type must not be empty")
	}

	if event.AggregateID == "" {
		return "", errors.New("outbox: aggregate ID must not be empty")
	}

	// The tenant is resolved once, for both lanes, before anything else is judged: the
	// relay runs detached, and under outbox.tenancy=shared its cycle carries no tenant,
	// so Publish is the only point that still knows it — the same reason it snapshots
	// the trace keys (below). The rule is the publisher's own (ADR-087 §3): a
	// caller-supplied x-tenant-id is refused whatever the lane, then the context tenant
	// is the stamp. Where it is persisted differs by lane: an AMQP row carries it in its
	// headers, a stream row as its partition key (applyStreamTarget).
	stamp, err := messaging.ResolveTenantStamp(ctx, event.Headers)
	if err != nil {
		return "", fmt.Errorf("outbox: %w", err)
	}

	var exchange, routingKey string
	if event.Stream == "" {
		if exchange, routingKey, err = p.resolveAMQPDestination(event); err != nil {
			return "", err
		}
	}

	payload, err := marshalPayload(event.Payload)
	if err != nil {
		return "", fmt.Errorf("outbox: failed to marshal payload: %w", err)
	}

	// Capture trace context (traceparent / X-Request-ID / tracestate) from the
	// publish context into the persisted headers. The relay runs later as a
	// detached scheduled job whose context carries no trace, so Publish — the
	// only point where the originating request context is still live — must
	// snapshot it. Persisting it here lets the relay replay the SAME trace to
	// the broker (messaging.preparePublishing honors an existing traceparent
	// header), so the consumer's message-scoped logger reports the originating
	// trace id instead of a freshly generated one. Mirrors the injection the
	// direct AMQP fast-path performs, keeping outbox + direct publishes
	// trace-equivalent. Untraced publishes (no trace in context) are left as-is
	// so background events don't accrue synthetic trace headers.
	headers, err := marshalHeaders(ctx, event.Headers, stamp)
	if err != nil {
		return "", fmt.Errorf("outbox: failed to marshal headers: %w", err)
	}

	record := &Record{
		ID:          uuid.New().String(),
		EventType:   event.EventType,
		AggregateID: event.AggregateID,
		Payload:     payload,
		Headers:     headers,
		Status:      StatusPending,
		CreatedAt:   time.Now(),
	}

	if event.Stream != "" {
		if err := p.applyStreamTarget(stamp, event, record); err != nil {
			return "", err
		}
	} else {
		record.Lane = LaneAMQP
		record.Exchange = exchange
		record.RoutingKey = routingKey
	}

	if err := p.store.Insert(ctx, tx, record); err != nil {
		return "", err
	}

	return record.ID, nil
}

// resolveAMQPDestination applies the exchange and routing-key fallbacks and judges the
// result, for the AMQP lane only. A stream-lane row carries no exchange or routing key, so
// applying the fallbacks to it would invent a destination it will never be published to —
// and then refuse the event when that invented destination happens to be too long for a
// frame it never enters.
//
// The values returned are the ones the relay later puts on the wire, so the publish rule
// runs on the post-fallback destination BEFORE the INSERT: a row the AMQP frame can never
// carry is refused at its source rather than parked by the relay after MaxRetries. Only
// the caller's header keys are judged — the trace keys the framework adds are literals. It
// runs before the payload and header marshaling because it needs none of it.
func (p *outboxPublisher) resolveAMQPDestination(event *app.OutboxEvent) (exchange, routingKey string, err error) {
	exchange, routingKey = event.Exchange, event.RoutingKey
	if exchange == "" {
		exchange = p.defaultExchange
	}
	if routingKey == "" {
		routingKey = event.EventType
	}
	if err = messaging.ValidatePublishDestination(messaging.PublishOptions{
		Exchange:   exchange,
		RoutingKey: routingKey,
		Headers:    event.Headers,
	}); err != nil {
		return "", "", fmt.Errorf("outbox: %w", err)
	}
	return exchange, routingKey, nil
}

// applyStreamTarget validates a stream-targeted event and fills the record's stream
// lane fields. The partition key is the tenant stamp Publish resolved, refused here at
// Publish where the developer sees it, rather than as poison cycles later in the relay.
func (p *outboxPublisher) applyStreamTarget(stamp string, event *app.OutboxEvent, record *Record) error {
	if event.Exchange != "" || event.RoutingKey != "" {
		return ErrConflictingTargets
	}
	if !slices.Contains(p.streamTargets, event.Stream) {
		return fmt.Errorf("%w: %q", ErrStreamNotAnOutboxTarget, event.Stream)
	}
	if stamp == "" {
		return ErrStreamTargetRequiresTenant
	}
	record.Lane = LaneStream
	record.Stream = event.Stream
	record.PartitionKey = stamp
	return nil
}

// marshalHeaders JSON-encodes the AMQP headers, first capturing the trace
// context from ctx so it survives to the relay and consumer, then the tenant
// stamp the caller resolved. The caller's map is never mutated — the framework's
// keys are written to a fresh copy. Returns nil (a SQL NULL) when there are
// neither caller headers, nor a trace context, nor a stamp to persist.
//
// An empty stamp writes NOTHING, not an empty-valued header: the relay strips the
// stamp on presence and the conflict check keys on presence, so an empty value
// persisted here would be a present, malformed stamp.
func marshalHeaders(ctx context.Context, eventHeaders map[string]any, stamp string) ([]byte, error) {
	traced := hasTraceContext(ctx)

	// Common path: an untraced, tenant-less publish with no caller headers (every
	// background/non-HTTP event). Store SQL NULL without allocating a map.
	if len(eventHeaders) == 0 && !traced && stamp == "" {
		return nil, nil
	}

	// Sized to the caller's headers only: the map grows itself for the trace keys
	// and the stamp, and a +N in the hint is unobservable — the same reason the
	// stamping publisher sizes its map this way, and the mutation gate's proof of it
	// (an operator swap in the hint changes nothing any test can see).
	headers := make(map[string]any, len(eventHeaders))
	maps.Copy(headers, eventHeaders)
	if traced {
		gobrickstrace.InjectIntoHeaders(ctx, &mapHeaderAccessor{headers: headers})
	}
	if stamp != "" {
		headers[messaging.TenantStampHeader] = stamp
	}
	return json.Marshal(headers)
}

// hasTraceContext reports whether ctx carries an inbound trace identity worth
// persisting (a W3C traceparent or an X-Request-ID derived trace id).
func hasTraceContext(ctx context.Context) bool {
	if _, ok := gobrickstrace.ParentFromContext(ctx); ok {
		return true
	}
	_, ok := gobrickstrace.IDFromContext(ctx)
	return ok
}

// mapHeaderAccessor adapts a map[string]any to trace.HeaderAccessor, letting the
// publisher reuse the same centralized trace injection the AMQP client uses.
type mapHeaderAccessor struct {
	headers map[string]any
}

func (m *mapHeaderAccessor) Get(key string) any {
	if m.headers == nil {
		return nil
	}
	return m.headers[key]
}

func (m *mapHeaderAccessor) Set(key string, value any) {
	if m.headers == nil {
		m.headers = make(map[string]any)
	}
	m.headers[key] = value
}

func marshalPayload(payload any) ([]byte, error) {
	if payload == nil {
		return []byte("null"), nil
	}

	if b, ok := payload.([]byte); ok {
		return b, nil
	}
	if t := reflect.TypeOf(payload); messaging.IsSealTagged(t) {
		return nil, fmt.Errorf("%w (payload type %v)", ErrSealedPayloadNeedsBytes, t)
	}

	return json.Marshal(payload)
}
