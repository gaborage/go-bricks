package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/gaborage/go-bricks/app"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
	"github.com/google/uuid"
)

// outboxPublisher implements app.OutboxPublisher by writing events to the outbox table
// within the caller's database transaction.
type outboxPublisher struct {
	store           Store
	defaultExchange string
}

// newPublisher creates a new outbox publisher.
func newPublisher(store Store, defaultExchange string) app.OutboxPublisher {
	return &outboxPublisher{
		store:           store,
		defaultExchange: defaultExchange,
	}
}

func (p *outboxPublisher) Publish(ctx context.Context, tx dbtypes.Tx, event *app.OutboxEvent) (string, error) {
	if tx == nil {
		return "", fmt.Errorf("outbox: transaction must not be nil")
	}

	if event == nil {
		return "", fmt.Errorf("outbox: event must not be nil")
	}

	if event.EventType == "" {
		return "", fmt.Errorf("outbox: event type must not be empty")
	}

	if event.AggregateID == "" {
		return "", fmt.Errorf("outbox: aggregate ID must not be empty")
	}

	// Marshal payload
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
	headers, err := marshalHeaders(ctx, event.Headers)
	if err != nil {
		return "", fmt.Errorf("outbox: failed to marshal headers: %w", err)
	}

	// Resolve exchange
	exchange := event.Exchange
	if exchange == "" {
		exchange = p.defaultExchange
	}

	// Resolve routing key
	routingKey := event.RoutingKey
	if routingKey == "" {
		routingKey = event.EventType
	}

	// Generate event ID
	eventID := uuid.New().String()

	record := &Record{
		ID:          eventID,
		EventType:   event.EventType,
		AggregateID: event.AggregateID,
		Payload:     payload,
		Headers:     headers,
		Exchange:    exchange,
		RoutingKey:  routingKey,
		Status:      StatusPending,
		CreatedAt:   time.Now(),
	}

	if err := p.store.Insert(ctx, tx, record); err != nil {
		return "", err
	}

	return eventID, nil
}

// marshalHeaders JSON-encodes the AMQP headers, first capturing the trace
// context from ctx so it survives to the relay and consumer. The caller's map
// is never mutated — trace keys are written to a fresh copy. Returns nil (a SQL
// NULL) when there are neither caller headers nor a trace context to persist.
func marshalHeaders(ctx context.Context, eventHeaders map[string]any) ([]byte, error) {
	traced := hasTraceContext(ctx)

	// Common path: an untraced publish with no caller headers (every
	// background/non-HTTP event). Store SQL NULL without allocating a map.
	if len(eventHeaders) == 0 && !traced {
		return nil, nil
	}

	// +3 leaves room for the trace keys (X-Request-ID, traceparent, tracestate).
	headers := make(map[string]any, len(eventHeaders)+3)
	maps.Copy(headers, eventHeaders)
	if traced {
		gobrickstrace.InjectIntoHeaders(ctx, &mapHeaderAccessor{headers: headers})
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

// marshalPayload converts the payload to []byte.
// If the payload is already []byte, it is returned as-is.
// Otherwise, it is JSON-marshaled.
func marshalPayload(payload any) ([]byte, error) {
	if payload == nil {
		return []byte("null"), nil
	}

	if b, ok := payload.([]byte); ok {
		return b, nil
	}

	return json.Marshal(payload)
}
