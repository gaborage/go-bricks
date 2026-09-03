package messaging

import (
	"context"
	"errors"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/gaborage/go-bricks/messaging/internal/payloaderr"
)

// errNilDelivery is the cause a nil *amqp.Delivery is reported with. The
// framework's worker loop never produces one, so this only guards a hand-driven
// Handle call; it stays unexported because no consumer can branch on it usefully.
var errNilDelivery = errors.New("messaging: nil delivery")

// nilDeliverySummary renders errNilDelivery. It is a constant, so the no-payload
// -bytes guarantee holds trivially.
const nilDeliverySummary = "nil delivery"

// Metadata exposes read-only delivery facts to a typed consumer without
// widening fn's payload contract. It is a per-delivery value constructed by
// the adapter AFTER the nil-delivery guard; the zero value is inert (every
// accessor returns its zero). Headers returns the live amqp.Table — treat it
// as read-only, it is shared with the worker loop.
type Metadata struct {
	delivery *amqp.Delivery
}

// Headers returns the AMQP delivery headers. Nil when no headers were
// published. For the outbox dedup key prefer DedupKey, which validates as it
// extracts. The table is
// the delivery's own, not a copy — read it, do not mutate it. Values are
// publisher-controlled, so treat them as untrusted input on a queue fed from
// outside this service.
func (m Metadata) Headers() amqp.Table {
	if m.delivery == nil {
		return nil
	}
	return m.delivery.Headers
}

// EventType returns the wire-level message type stamped by the publisher.
func (m Metadata) EventType() string {
	if m.delivery == nil {
		return ""
	}
	return m.delivery.Type
}

// Redelivered reports the broker's redelivery flag.
func (m Metadata) Redelivered() bool {
	if m.delivery == nil {
		return false
	}
	return m.delivery.Redelivered
}

// typedHandler adapts a typed function to MessageHandler. Every field is set
// once at construction and read-only afterwards: one instance is shared by every
// worker goroutine of a consumer AND by every tenant replaying the same
// declarations, so any mutable state here would be a data race. fn always
// carries the metadata-aware shape; NewTypedHandler wraps a metadata-less fn
// so Handle has a single dispatch path. Metadata itself is a per-delivery
// value, not adapter state, so this does not reopen the race the comment warns
// about.
type typedHandler[T any] struct {
	eventType string
	decoder   *payloaderr.Decoder[T]
	fn        func(context.Context, T, Metadata) error
}

// newTypedHandler is the single construction point, so the decoder cannot be
// forgotten on one of the two exported entry points.
func newTypedHandler[T any](eventType string, fn func(context.Context, T, Metadata) error) *typedHandler[T] {
	return &typedHandler[T]{
		eventType: eventType,
		decoder:   payloaderr.NewDecoder[T](payloaderr.JSONCodec{}),
		fn:        fn,
	}
}

// NewTypedHandler adapts a typed function to the MessageHandler contract:
// decode (JSON) → validate (go-playground struct tags) → fn. It is the consumer
// mirror of the typed HTTP handlers registered with server.POST.
//
// Failures short-circuit with a *PayloadError, which the worker loop nacks
// WITHOUT requeue like any other handler error — decode and validation failures
// are not retryable, so pair the queue with DeclareQueueWithDLQ to park them.
// Match them with errors.Is against ErrPayloadUndecodable or ErrPayloadInvalid.
// fn's own error is returned unwrapped, so a consumer's errors.Is against its
// business sentinels still works.
//
// The returned handler holds no mutable state and is safe to share across
// workers and tenants. fn must therefore be safe for concurrent use too.
func NewTypedHandler[T any](eventType string, fn func(context.Context, T) error) MessageHandler {
	if fn == nil {
		panic("messaging: NewTypedHandler requires a non-nil handler function (event_type=" + eventType + ")")
	}

	return newTypedHandler(eventType, func(ctx context.Context, payload T, _ Metadata) error { return fn(ctx, payload) })
}

func (h *typedHandler[T]) Handle(ctx context.Context, delivery *amqp.Delivery) error {
	if delivery == nil {
		return newPayloadError(h.eventType, payloaderr.NewDecode(errNilDelivery, nilDeliverySummary))
	}

	// A fresh payload per delivery: workers share the handler, not the value.
	var payload T
	if body := h.decoder.Decode(delivery.Body, &payload); body != nil {
		return newPayloadError(h.eventType, body)
	}

	return h.fn(ctx, payload, Metadata{delivery: delivery})
}

func (h *typedHandler[T]) EventType() string {
	return h.eventType
}

// DeclareTypedConsumer registers a consumer whose handler is built by
// NewTypedHandler from fn, so T is inferred from fn and never spelled out.
//
// The queue is not declared here: pass it to DeclareQueue or
// DeclareQueueWithDLQ separately, exactly as an untyped DeclareConsumer with a
// nil queue does. A consumer naming a queue nobody declared surfaces at
// Declarations.Validate() as "consumer references non-existent queue", not here.
//
// It panics on a nil decls, a nil opts, or an opts that already carries a
// Handler — all three are declaration-time wiring mistakes, and the package
// already fails startup that way for duplicate consumer registrations.
//
// Failure and concurrency semantics are NewTypedHandler's: decode and validation
// failures return a *PayloadError that nacks WITHOUT requeue, so pair the queue
// with DeclareQueueWithDLQ, and fn must be safe for concurrent use.
func DeclareTypedConsumer[T any](decls *Declarations, opts *ConsumerOptions, fn func(context.Context, T) error) *ConsumerDeclaration {
	checkTypedConsumerArgs(decls, opts, "DeclareTypedConsumer")

	opts.Handler = NewTypedHandler[T](opts.EventType, fn)

	return decls.DeclareConsumer(opts, nil)
}

// checkTypedConsumerArgs holds the three declaration-time wiring guards shared
// by DeclareTypedConsumer and DeclareTypedConsumerWithMeta. entry names the
// caller in every panic message, so a panic always points at the entry point
// actually used.
func checkTypedConsumerArgs(decls *Declarations, opts *ConsumerOptions, entry string) {
	if decls == nil {
		panic("messaging: " + entry + " requires a non-nil *Declarations")
	}
	if opts == nil {
		panic("messaging: " + entry + " requires non-nil *ConsumerOptions")
	}
	if opts.Handler != nil {
		panic(fmt.Sprintf(
			"messaging: %s builds the handler itself, so ConsumerOptions.Handler must be nil\n"+
				"  queue=%s consumer=%s event_type=%s\n"+
				"  Use DeclareConsumer for a hand-written MessageHandler, or build a fresh\n"+
				"  ConsumerOptions if this one was already passed to %s",
			entry, opts.Queue, opts.Consumer, opts.EventType, entry,
		))
	}
}

// NewTypedHandlerWithMeta is NewTypedHandler for consumers that also need
// delivery metadata — the outbox-dedup shape: take meta.DedupKey() (the
// grammar-validated x-outbox-event-id, or an error to return) and wrap the
// business logic in inbox.ProcessOnce. Failure and concurrency semantics are identical
// to NewTypedHandler; fn must be safe for concurrent use.
func NewTypedHandlerWithMeta[T any](eventType string, fn func(context.Context, T, Metadata) error) MessageHandler {
	if fn == nil {
		panic("messaging: NewTypedHandlerWithMeta requires a non-nil handler function (event_type=" + eventType + ")")
	}
	return newTypedHandler(eventType, fn)
}

// DeclareTypedConsumerWithMeta is DeclareTypedConsumer for a metadata-aware
// fn. Same panics, same queue rules, same PayloadError semantics.
func DeclareTypedConsumerWithMeta[T any](decls *Declarations, opts *ConsumerOptions, fn func(context.Context, T, Metadata) error) *ConsumerDeclaration {
	checkTypedConsumerArgs(decls, opts, "DeclareTypedConsumerWithMeta")
	opts.Handler = NewTypedHandlerWithMeta[T](opts.EventType, fn)
	return decls.DeclareConsumer(opts, nil)
}
