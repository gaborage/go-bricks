package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
)

// Publisher is the module-facing handle DeclareTypedPublisher returns: a
// publisher bound at declaration time to ONE destination — the declared
// exchange, routing key, default headers and delivery flags — so a call site
// never re-spells them. It is the publish mirror of the typed consumer built
// by DeclareTypedConsumer.
//
// Every field is set once at construction and read-only afterwards: one handle
// is shared by every goroutine of a module AND, under multi-tenant messaging,
// by every tenant's client, so any mutable state here would be a data race.
// Publish therefore hands the client a fresh headers map on every call and
// never writes back into the handle.
type Publisher[T any] struct {
	eventType  string
	exchange   string
	routingKey string
	headers    map[string]any
	mandatory  bool
	immediate  bool
}

// newTypedPublisher is the single construction point for the handle, so every
// exported entry builds it the same way. The copy of decl.Headers is the ONLY
// thing severing the handle from the caller's PublisherOptions.Headers map:
// NewPublisher aliases a non-nil Headers rather than copying it, and
// DeclarePublisher returns that pre-registration value, not the deep copy
// RegisterPublisher stores. Removing this copy would let a later write through
// the caller's map reach every publish.
func newTypedPublisher[T any](decl *PublisherDeclaration) *Publisher[T] {
	headers := make(map[string]any, len(decl.Headers))
	maps.Copy(headers, decl.Headers)

	return &Publisher[T]{
		eventType:  decl.EventType,
		exchange:   decl.Exchange,
		routingKey: decl.RoutingKey,
		headers:    headers,
		mandatory:  decl.Mandatory,
		immediate:  decl.Immediate,
	}
}

// DeclareTypedPublisher registers a publisher exactly as DeclarePublisher does —
// same registry entry, same replay, validation and hash path — and returns a
// Publisher[T] handle bound to the declared destination. Keep the handle on the
// module (there is no deps accessor to look one up again) and publish through
// it from handlers and services.
//
// The exchange is not declared here: pass it to DeclareTopicExchange
// separately, exactly as a DeclarePublisher with a nil exchange does.
//
// It panics on a nil decls or a nil opts — both are declaration-time wiring
// mistakes, and the package already fails startup that way for the typed
// consumer entries.
func DeclareTypedPublisher[T any](decls *Declarations, opts *PublisherOptions) *Publisher[T] {
	if decls == nil {
		panic("messaging: DeclareTypedPublisher requires a non-nil *Declarations")
	}
	if opts == nil {
		panic("messaging: DeclareTypedPublisher requires non-nil *PublisherOptions")
	}

	return newTypedPublisher[T](decls.DeclarePublisher(opts, nil))
}

// Publish JSON-marshals evt and publishes it to the DECLARED exchange and
// routing key with the declared default headers, through client — the
// tenant-aware client a handler already holds (the getMessaging(ctx) idiom),
// so the framework's tenant stamping and trace injection apply unchanged.
//
// A marshal failure is returned wrapped and publishes nothing. Every other
// error is the client's own (ErrInvalidPublishDestination,
// ErrPublishRetriesExhausted, ...) and is returned unwrapped.
//
// Safe for concurrent use: the handle is never written after construction and
// the client receives a fresh copy of the declared headers on every call.
func (h *Publisher[T]) Publish(ctx context.Context, client AMQPClient, evt T) error {
	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("messaging: marshal %s event: %w", h.eventType, err)
	}

	return h.publishBytes(ctx, client, data)
}

// publishBytes is the handle's ONE bytes door. It is the only place the handle
// names the client's raw publish method, so retargeting it — to an internal
// bytes interface reached by type assertion, say — never touches Publish's
// exported signature or its callers.
func (h *Publisher[T]) publishBytes(ctx context.Context, client AMQPClient, data []byte) error {
	headers := make(map[string]any, len(h.headers))
	maps.Copy(headers, h.headers)

	return client.PublishToExchange(ctx, PublishOptions{
		Exchange:   h.exchange,
		RoutingKey: h.routingKey,
		Headers:    headers,
		Mandatory:  h.mandatory,
		Immediate:  h.immediate,
	}, data)
}
