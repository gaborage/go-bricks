package streams

import (
	"context"
	"fmt"

	"github.com/gaborage/go-bricks/messaging/internal/delivery"
	"github.com/gaborage/go-bricks/messaging/internal/payloaderr"
)

// typedConsumer holds one typed consumer's decode pipeline. Both the handler and
// the poison screen read T through the SAME decoder, so a message the screen
// rejects is exactly one the handler would have rejected.
//
// Every field is set once at declaration and read-only afterwards: one instance
// serves every partition of a super stream, which the client delivers to
// concurrently, so any mutable state here would be a data race.
type typedConsumer[T any] struct {
	name    string
	decoder *payloaderr.Decoder[T]
	fn      func(context.Context, T, *Message) error
}

// newTypedConsumer is the single construction point, so no entry point can end
// up with a handler whose screen decodes a different way.
func newTypedConsumer[T any](name string, fn func(context.Context, T, *Message) error) *typedConsumer[T] {
	return &typedConsumer[T]{
		name:    name,
		decoder: payloaderr.NewDecoder[T](payloaderr.JSONCodec{}),
		fn:      fn,
	}
}

// handle is the Handler the lane runs: decode (JSON) → validate (go-playground
// struct tags) → fn.
//
// A payload failure is returned Permanent, so the declared RetryOptions do not
// re-run a decode that cannot come out differently. fn's own error is returned
// unwrapped, so a consumer's errors.Is against its business sentinels still
// works — and so does streams.Permanent, if fn applied it itself.
func (c *typedConsumer[T]) handle(ctx context.Context, msg *Message) error {
	// A fresh payload per delivery: partitions share the consumer, not the value.
	var payload T
	if body := c.decoder.Decode(msg.Data, &payload); body != nil {
		return delivery.Permanent(newPayloadError(c.name, body))
	}

	return c.fn(ctx, payload, msg)
}

// screen reports a message the handler could not have accepted, WITHOUT running
// fn. The runner calls it on the one path that would otherwise put an undecodable
// body into the hold: a delivery whose tenant is already held is parked without
// running, and a body that will never decode would then park that tenant behind
// a message no replay can drain (ADR-092).
//
// It runs only on that gated path, so a normal delivery still decodes exactly
// once.
func (c *typedConsumer[T]) screen(msg *Message) error {
	var payload T
	if body := c.decoder.Decode(msg.Data, &payload); body != nil {
		return delivery.Permanent(newPayloadError(c.name, body))
	}

	return nil
}

// checkTypedConsumerArgs holds the declaration-time wiring guards shared by
// every typed declare helper, past the two that need the concrete option type.
// entry names the caller in every panic message, so a panic always points at the
// entry point actually used.
func checkTypedConsumerArgs(handler Handler, fnIsNil bool, entry, optsType string) {
	if handler != nil {
		panic(fmt.Sprintf(
			"streams: %s builds the handler itself, so %s.Handler must be nil\n"+
				"  Use DeclareConsumer or DeclareSuperStreamConsumer for a hand-written Handler",
			entry, optsType,
		))
	}
	if fnIsNil {
		panic("streams: " + entry + " requires a non-nil handler function")
	}
}

// DeclareTypedConsumer registers a stream consumer whose handler decodes the
// message body into T and validates it against the struct's `validate` tags
// before calling fn. It is the streams-lane mirror of
// messaging.DeclareTypedConsumer, and T is inferred from fn.
//
// There is deliberately no exported NewTypedHandler counterpart on this lane.
// The declaration carries a poison screen alongside the handler, and a handler
// handed to DeclareConsumer as a plain Handler could not carry one — a typed
// consumer assembled that way would park undecodable bodies in the hold, which
// is exactly what ADR-092 forbids.
//
// Failure semantics: a body that does not decode, or decodes but fails
// validation, is deterministic poison. It is not retried in place whatever
// Retry says, it is never parked when Hold is set, and its offset is not
// committed — the lane skips it, exactly as ADR-059 settles any failure on a
// consumer that does not hold. Match the two modes with errors.Is against
// ErrPayloadUndecodable and ErrPayloadInvalid.
//
// It panics on a nil decls, a nil opts, or an opts that already carries a
// Handler — all three are declaration-time wiring mistakes, which this lane
// already fails startup on for duplicate registrations.
func DeclareTypedConsumer[T any](decls *Declarations, opts *ConsumerOptions, fn func(context.Context, T) error) {
	DeclareTypedConsumerWithMeta(decls, opts, func(ctx context.Context, payload T, _ *Message) error {
		return fn(ctx, payload)
	})
}

// DeclareTypedConsumerWithMeta is DeclareTypedConsumer for an fn that also needs
// the delivery itself — msg.Offset, msg.Stream and msg.Properties. Same panics,
// same poison semantics; msg.Data is the body fn's payload was decoded from.
func DeclareTypedConsumerWithMeta[T any](decls *Declarations, opts *ConsumerOptions, fn func(context.Context, T, *Message) error) {
	const entry = "DeclareTypedConsumerWithMeta"
	if decls == nil {
		panic("streams: " + entry + " requires a non-nil *Declarations")
	}
	if opts == nil {
		panic(nilDeclarationPanic("consumer", entry, "ConsumerOptions"))
	}
	checkTypedConsumerArgs(opts.Handler, fn == nil, entry, "ConsumerOptions")

	typed := newTypedConsumer(opts.Name, fn)
	opts.Handler = typed.handle
	decls.declareConsumer(opts, typed.screen)
}

// DeclareTypedSuperStreamConsumer is DeclareTypedConsumer over every partition of
// a super stream. fn is called CONCURRENTLY across partitions — see Handler — so
// it must be safe for concurrent use.
func DeclareTypedSuperStreamConsumer[T any](decls *Declarations, opts *SuperStreamConsumerOptions, fn func(context.Context, T) error) {
	DeclareTypedSuperStreamConsumerWithMeta(decls, opts, func(ctx context.Context, payload T, _ *Message) error {
		return fn(ctx, payload)
	})
}

// DeclareTypedSuperStreamConsumerWithMeta is DeclareTypedSuperStreamConsumer for
// an fn that also needs the delivery. msg.Stream names the PARTITION the message
// arrived on, not the super stream.
func DeclareTypedSuperStreamConsumerWithMeta[T any](decls *Declarations, opts *SuperStreamConsumerOptions, fn func(context.Context, T, *Message) error) {
	const entry = "DeclareTypedSuperStreamConsumerWithMeta"
	if decls == nil {
		panic("streams: " + entry + " requires a non-nil *Declarations")
	}
	if opts == nil {
		panic(nilDeclarationPanic("consumer", entry, "SuperStreamConsumerOptions"))
	}
	checkTypedConsumerArgs(opts.Handler, fn == nil, entry, "SuperStreamConsumerOptions")

	typed := newTypedConsumer(opts.Name, fn)
	opts.Handler = typed.handle
	decls.declareSuperStreamConsumer(opts, typed.screen)
}
