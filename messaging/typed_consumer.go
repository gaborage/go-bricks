package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/gaborage/go-bricks/internal/validation"
)

// errNilDelivery is the cause a nil *amqp.Delivery is reported with. The
// framework's worker loop never produces one, so this only guards a hand-driven
// Handle call; it stays unexported because no consumer can branch on it usefully.
var errNilDelivery = errors.New("messaging: nil delivery")

// nilDeliverySummary renders errNilDelivery. It is a constant, so the no-payload
// -bytes guarantee holds trivially.
const nilDeliverySummary = "nil delivery"

// typedValidator is the one validator instance every typed handler shares.
// validator caches struct metadata by reflect.Type, so per-message construction
// would throw that cache away on every delivery; the instance is safe for
// concurrent use, which is what lets one adapter serve every worker.
var typedValidator = sync.OnceValue(validation.New)

// codec decodes a raw payload into T. Unexported by design: schema negotiation
// and non-JSON payloads (issue #346) widen this seam without an API break.
type codec interface {
	Unmarshal(data []byte, v any) error
	// summarize renders a decode failure with NO payload bytes in it. Returning
	// "" means the shape was not audited; the PayloadError constructor
	// substitutes the fail-closed phrase, so a codec never spells it out.
	summarize(err error) string
}

// jsonCodec is the only codec today: AMQP bodies are JSON on every path the
// framework publishes.
type jsonCodec struct{}

func (jsonCodec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// summarize renders a decode failure without any payload bytes.
// json.UnmarshalTypeError.Value carries the raw literal ("number 1234.56") and
// json.SyntaxError's message quotes the offending byte, so neither error's own
// text may be rendered. Field, Type and Offset are destination-schema
// facts: encoding/json builds Field from the matched destination field's json
// tag and never from an input key (map keys never enter its FieldStack).
// Anything else — including json.Decoder.DisallowUnknownFields, which names the
// partner's key — falls through to a phrase that reveals nothing.
func (jsonCodec) summarize(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		wantType := "unknown"
		if typeErr.Type != nil {
			wantType = typeErr.Type.String()
		}
		if typeErr.Field != "" {
			return fmt.Sprintf("json: type mismatch at field %q (want %s, offset %d)", typeErr.Field, wantType, typeErr.Offset)
		}

		return fmt.Sprintf("json: type mismatch (want %s, offset %d)", wantType, typeErr.Offset)
	}

	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Sprintf("json: syntax error at offset %d", syntaxErr.Offset)
	}

	return ""
}

// Metadata exposes read-only delivery facts to a typed consumer without
// widening fn's payload contract. It is a per-delivery value constructed by
// the adapter AFTER the nil-delivery guard; the zero value is inert (every
// accessor returns its zero). Headers returns the live amqp.Table — treat it
// as read-only, it is shared with the worker loop.
type Metadata struct {
	delivery *amqp.Delivery
}

// Headers returns the AMQP delivery headers (e.g. for
// outbox.EventIDFromHeaders). Nil when no headers were published. The table is
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
	codec     codec
	fn        func(context.Context, T, Metadata) error
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

	return &typedHandler[T]{
		eventType: eventType,
		codec:     jsonCodec{},
		fn:        func(ctx context.Context, payload T, _ Metadata) error { return fn(ctx, payload) },
	}
}

func (h *typedHandler[T]) Handle(ctx context.Context, delivery *amqp.Delivery) error {
	if delivery == nil {
		return newPayloadDecodeError(h.eventType, errNilDelivery, nilDeliverySummary)
	}

	// A fresh payload per delivery: workers share the handler, not the value.
	var payload T
	if err := h.codec.Unmarshal(delivery.Body, &payload); err != nil {
		return newPayloadDecodeError(h.eventType, err, h.codec.summarize(err))
	}

	// A non-struct T reaches this with a *validator.InvalidValidationError, which
	// yields no fields and still matches ErrPayloadInvalid — fail closed on the
	// first delivery rather than silently skipping validation forever.
	if err := typedValidator().Struct(payload); err != nil {
		return newPayloadValidateError(h.eventType, err)
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
// delivery metadata — the outbox-dedup shape: read the x-outbox-event-id
// header via outbox.EventIDFromHeaders(meta.Headers()) and wrap the business
// logic in inbox.ProcessOnce. Failure and concurrency semantics are identical
// to NewTypedHandler; fn must be safe for concurrent use.
func NewTypedHandlerWithMeta[T any](eventType string, fn func(context.Context, T, Metadata) error) MessageHandler {
	if fn == nil {
		panic("messaging: NewTypedHandlerWithMeta requires a non-nil handler function (event_type=" + eventType + ")")
	}
	return &typedHandler[T]{eventType: eventType, codec: jsonCodec{}, fn: fn}
}

// DeclareTypedConsumerWithMeta is DeclareTypedConsumer for a metadata-aware
// fn. Same panics, same queue rules, same PayloadError semantics.
func DeclareTypedConsumerWithMeta[T any](decls *Declarations, opts *ConsumerOptions, fn func(context.Context, T, Metadata) error) *ConsumerDeclaration {
	checkTypedConsumerArgs(decls, opts, "DeclareTypedConsumerWithMeta")
	opts.Handler = NewTypedHandlerWithMeta[T](opts.EventType, fn)
	return decls.DeclareConsumer(opts, nil)
}
