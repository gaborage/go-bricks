// Package delivery runs the delivery pipeline both messaging lanes share:
// everything that happens to one consumed message between "bytes arrived" and
// "outcome recorded" — trace extraction from the lane's carrier, the consumer
// span, the per-message lease scope, handler invocation, panic-to-error, one
// consumed record at completion, and the lane's own outcome line.
//
// Settlement is not here. Turning an outcome into a broker action — ack or
// nack-without-requeue on the classic lane, commit-offset or skip on the streams
// lane — is the lane's, and so is the policy behind it.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/internal/leasescope"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/tenantstamp"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/observability"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

const (
	// tracerName is the one instrumentation scope both lanes report under.
	tracerName = "go-bricks/messaging"

	spanOperationReceive = "receive"
	messagingSystem      = "rabbitmq"

	panicMessage       = "panic in message handler (type: %T)"
	settlePanicMessage = "Panic recovered while settling a delivery; not retried"

	spanAttrAttempts  = "messaging.delivery.attempts"
	spanAttrPermanent = "messaging.delivery.permanent"

	// spanAttrCap is the four common attributes plus the AMQP lane's four extras.
	spanAttrCap = 8
)

// consumerSpanOpts is built once and shared read-only by every delivery: a fresh
// inline variadic would heap-allocate both the slice and the option per message.
var consumerSpanOpts = []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindConsumer)}

// sharedTracer holds the resolved tracer; nil until the first delivery. An
// atomic pointer keeps the hot path lock-free and lets the test hook reset it
// safely against a concurrent Run.
var sharedTracer atomic.Pointer[trace.Tracer]

// tracer returns the one tracer both lanes' deliveries report under. otel.Tracer
// takes the global provider's lock and re-resolves the scope on every call, so
// the hot path resolves it once — the streams lane caches its tracer per runner
// today, and routing it through this seam must not regress that.
func tracer() trace.Tracer {
	if t := sharedTracer.Load(); t != nil {
		return *t
	}
	t := otel.Tracer(tracerName)
	if sharedTracer.CompareAndSwap(nil, &t) {
		return t
	}
	// A concurrent resolver won; use its tracer — unless a concurrent reset already
	// cleared it again, in which case this delivery keeps the one it resolved.
	if winner := sharedTracer.Load(); winner != nil {
		return *winner
	}
	return t
}

// Outcome names how one delivery ended.
type Outcome int

// The three outcomes of one delivery. Succeeded is the zero value, so a result
// is built as a success and only overwritten when the handler says otherwise.
const (
	Succeeded Outcome = iota
	HandlerError
	Panicked
)

// Handler invokes the module's handler for one message. The pipeline owns the
// per-message context, so it hands over the two things derived from it that a
// lane needs before its own handler runs: the context-bound logger and the
// trace ID. ctx carries the same id (trace.IDFromContext); the parameter is
// kept so a lane's own hot path does not pay a context lookup for it.
type Handler func(ctx context.Context, log logger.Logger, traceID string) error

// Request is what one lane hands the pipeline for one message. Handle and
// LogOutcome are required.
type Request struct {
	// Carrier is where the trace context traveled: AMQP 0.9.1 headers on the
	// classic lane, AMQP 1.0 application properties on the streams lane.
	Carrier gobrickstrace.HeaderAccessor

	// Destination is the queue or stream the message arrived on — the span
	// name's prefix and messaging.destination.name.
	Destination string

	// BodySize is the payload length for messaging.message.body.size.
	BodySize int

	// SpanExtras are the lane's span attributes, set after the four both lanes
	// share.
	SpanExtras []attribute.KeyValue

	// Metrics identifies this message on the receive instruments.
	Metrics tracking.ConsumeAttributes

	// Log is the consumer's logger. The pipeline binds it to the per-message
	// context and hands the bound one back on Result.
	Log logger.Logger

	Handle Handler

	// Retry bounds in-place re-invocation of Handle after a HandlerError. Nil
	// means exactly one attempt, which is what every classic-lane consumer and
	// every stream consumer that does not ask for a policy gets.
	Retry *Retry

	// TenantStamps makes the pipeline read the carrier's tenant stamp and seed the
	// handler context with it. True only under multitenant.enabled together with
	// messaging.tenancy: shared — under per-tenant tenancy the replay key already
	// seeded the tenant, and single-tenant deployments carry no stamp to read.
	//
	// It lives here rather than in either lane because the rule is one rule: both
	// lanes must refuse the same deliveries with the same text, and a lane-side copy
	// is a copy that can drift.
	TenantStamps bool

	// TenantOptional lets this consumer run a delivery that carries no stamp — a
	// control-plane consumer whose events belong to no tenant. It never admits a
	// stamp that is present but unusable, whoever the consumer is.
	TenantOptional bool

	// LogOutcome writes the lane's own line for the finished delivery. It runs
	// while the span is open and the lease scope still holds, so a handle the
	// handler borrowed outlives the line.
	LogOutcome func(*Result)

	// Settle turns the outcome into the lane's broker action: ack or
	// nack-without-requeue on the classic lane, commit-offset or skip on the
	// streams lane. The pipeline calls it at most once per delivery, after the
	// span has closed and the lease scope has drained, so a handle the handler
	// borrowed is released before the message is acknowledged.
	//
	// There is no fallback variant: a panic in the body is recovered and Settle
	// still runs with a Panicked result, which is the same action a lane's own
	// fallback performed. Only a panic INSIDE Settle needs different handling,
	// and its correct response is to log and stop rather than retry.
	Settle func(*Result)
}

// Result is what the lane settles on. PanicType and Stack are set only when
// Outcome is Panicked; Err is nil only when Outcome is Succeeded. A pointer, not a
// value, so the lane's LogOutcome does not copy it per message.
type Result struct {
	Outcome  Outcome
	Err      error
	Duration time.Duration
	TraceID  string
	Log      logger.Logger
	// Attempts is how often Handle ran for this delivery: 1 unless a Retry
	// policy re-invoked it.
	Attempts int
	// PanicType is the Go type of the value a panicking handler produced, never
	// the value (ADR-081). The raw value is deliberately NOT retained: this struct
	// crosses both messaging lanes, and a field holding it would let any lane —
	// or a future AppendOutcome variant — log a consumer-chosen value. Storing the
	// rendering instead of the source makes that unrepresentable rather than
	// merely discouraged.
	PanicType string
	Stack     []byte
}

// AppendOutcome stamps the outcome fields both lanes share. The caller creates
// the event, so the level, the lane's own fields and the message text stay with
// the lane.
func AppendOutcome(e logger.LogEvent, res *Result) logger.LogEvent {
	e = e.Str("correlation_id", res.TraceID).
		Dur("processing_time", res.Duration)
	if res.Outcome == Panicked {
		// SECURITY: the panic value's TYPE only (ADR-081) — and Result carries only
		// the type, so there is no value here to leak. The field would be `panic`,
		// which is no needle, so the sensitive-data filter could not mask it: a
		// handler panicking with a bare string put that string in the log verbatim,
		// and a map key the needle list does not name went the same way.
		// The stack stays: debug.Stack() renders the panic frame as `panic({0x...})`
		// and carries no value.
		e = e.Str("panic_type", res.PanicType).Bytes("stack", res.Stack)
	}
	return e
}

// Run puts one message through the delivery pipeline and returns the outcome for
// the lane to settle. It never returns nil, and a handler panic never escapes: it
// becomes a Panicked result carrying the recovered value's TYPE, its stack, and an
// error. A panic in the lane's own LogOutcome or Log is the lane's bug, but it does
// NOT escape either: the guard installed below covers everything after it, so such
// a panic becomes a Panicked result and the message is still settled. The span
// still ends and the lease scope still drains, both deferred.
func Run(ctx context.Context, req *Request) (res *Result) {
	start := time.Now()

	msgCtx := gobrickstrace.ExtractFromHeaders(ctx, req.Carrier)

	msgCtx, span := tracer().Start(msgCtx, req.Destination+" "+spanOperationReceive, consumerSpanOpts...)
	span.SetAttributes(spanAttributes(req)...)

	// Install the per-message lease scope (ADR-032): per-tenant handles borrowed
	// via deps.DB/Cache/Messaging while this message is handled (including inbox
	// ProcessOnce, which runs inside the handler and inherits msgCtx) are
	// released when the message is done, so a handle evicted mid-handling is not
	// closed under it. Deferred before span.End so the span closes first and
	// ReleaseAll runs last.
	msgCtx, scope := leasescope.Install(msgCtx)

	// Deferred FIRST so it runs LAST: the order is span end -> lease drain ->
	// settle. Everything below this line is inside the guard, so a panic in the
	// lane's LogOutcome, in the span marking or in the consume record is
	// recovered and the delivery is still settled — a message is never left
	// unsettled on the broker because the tail of its own delivery crashed.
	defer func() {
		if recovered := recover(); recovered != nil {
			res = panickedResult(req, res, recovered, start)
		}
		settleOnce(req, res)
	}()
	defer scope.ReleaseAll()
	defer span.End()

	// EnsureTraceID mints without writing back, so an id it minted must be planted
	// here or trace.IDFromContext stays empty inside the handler and every later
	// call mints a different one. ExtractFromHeaders already planted a carried id.
	traceID, carried := gobrickstrace.IDFromContext(msgCtx)
	if !carried {
		traceID = gobrickstrace.EnsureTraceID(msgCtx)
		msgCtx = gobrickstrace.WithTraceID(msgCtx, traceID)
	}

	log := req.Log.WithContext(msgCtx)

	// Above the retry loop on purpose: a delivery whose tenant cannot be
	// established is never made more valid by being tried again, so a stamp
	// failure must be refused before anything that can re-invoke the handler.
	// runAttempts is exactly such a thing.
	if stampCtx, err := seedTenant(msgCtx, req); err != nil {
		res = handlerErrorResult(err)
	} else {
		msgCtx = stampCtx
		res = runAttempts(msgCtx, log, traceID, req)
	}
	res.Duration = time.Since(start)
	res.TraceID = traceID
	res.Log = log

	req.LogOutcome(res)

	span.SetAttributes(outcomeAttributes(res)...)

	// SECURITY: a consumer handler's error may embed the payload — type only on
	// both span sinks; LogOutcome above keeps the message (ADR-083).
	observability.RecordErrorByType(span, res.Err)

	tracking.RecordConsume(msgCtx, req.Metrics, res.Duration, res.Err)

	return res
}

// outcomeAttributes renders what only the finished delivery knows, in one call
// and one slice: two SetAttributes each take the span's lock and grow its
// attribute slice, and the permanent check is skipped unless the handler
// actually failed — errors.As on a nil error still escapes its target.
func outcomeAttributes(res *Result) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 1, 2)
	attrs[0] = attribute.Int64(spanAttrAttempts, int64(res.Attempts))
	if res.Outcome == HandlerError && IsPermanent(res.Err) {
		attrs = append(attrs, attribute.Bool(spanAttrPermanent, true))
	}
	return attrs
}

// seedTenant reads the carrier's tenant stamp into the handler context.
//
// A stamp that cannot be used fails the delivery closed: the handler is never
// called, and the error travels the outcome path each lane already has — nack
// without requeue on the classic lane, an uncommitted offset on the streams lane.
//
// SECURITY: the stamp is producer-written and arrives unauthenticated, so the
// error names the reason and the byte length only — never the value.
func seedTenant(ctx context.Context, req *Request) (context.Context, error) {
	if !req.TenantStamps {
		return ctx, nil
	}

	id, err := tenantstamp.Read(req.Carrier.Get)
	if err != nil {
		var readErr *tenantstamp.ReadError
		if req.TenantOptional && errors.As(err, &readErr) && readErr.Reason == tenantstamp.ReasonMissing {
			return ctx, nil
		}
		return ctx, err
	}

	return multitenant.SetTenant(ctx, id), nil
}

// handlerErrorResult renders a refusal raised before the handler ran as the same
// outcome a handler error produces, so every lane settles it the way it already
// settles a failure.
func handlerErrorResult(err error) *Result {
	return &Result{Outcome: HandlerError, Err: err}
}

// spanAttributes renders the four attributes both lanes set, then the lane's own.
func spanAttributes(req *Request) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, spanAttrCap)
	attrs = append(attrs,
		attribute.String(string(semconv.MessagingSystemKey), messagingSystem),
		semconv.MessagingOperationName(spanOperationReceive),
		semconv.MessagingDestinationName(req.Destination),
		semconv.MessagingMessageBodySize(req.BodySize),
	)
	return append(attrs, req.SpanExtras...)
}

// runAttempts invokes the handler under the request's policy and returns the
// result the lane settles on — the LAST one, so the outcome line, the span and
// the consume record see one delivery however often it was tried. Only a
// HandlerError is retried: a panic repeats (ADR-081's rule aside, a handler that
// panics on a message panics on it again), and a Permanent error says so itself.
func runAttempts(ctx context.Context, log logger.Logger, traceID string, req *Request) *Result {
	for attempt := 1; ; attempt++ {
		res := invoke(ctx, log, traceID, req.Handle)
		res.Attempts = attempt

		if res.Outcome != HandlerError || req.Retry == nil ||
			attempt >= req.Retry.MaxAttempts || IsPermanent(res.Err) {
			return res
		}

		if !wait(ctx, backoffFor(req.Retry, attempt+1)) {
			return res
		}
	}
}

// wait sleeps for d unless the context ends first, reporting whether the caller
// may try again. A consumer shutting down must not sleep out its backoff. d comes
// from backoffFor, which never returns a negative duration.
func wait(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if d == 0 {
		return true
	}

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// invoke runs the lane's handler, turning a panic into an error so one tail
// logs, marks and records every outcome.
func invoke(ctx context.Context, log logger.Logger, traceID string, handle Handler) (res *Result) {
	res = &Result{}

	defer func() {
		if recovered := recover(); recovered != nil {
			res.Outcome = Panicked
			res.PanicType = fmt.Sprintf("%T", recovered)
			res.Stack = debug.Stack()
			res.Err = fmt.Errorf(panicMessage, recovered)
		}
	}()

	if err := handle(ctx, log, traceID); err != nil {
		res.Outcome = HandlerError
		res.Err = err
	}
	return res
}

// panickedResult turns a panic in the delivery tail into the result the lane
// settles on. A delivery whose handler succeeded but whose outcome line panicked
// is still Panicked here, so it nacks rather than acks: the lane never saw a
// complete delivery, and acknowledging one it could not finish reporting would
// lose the message silently.
func panickedResult(req *Request, res *Result, recovered any, start time.Time) *Result {
	if res == nil {
		res = &Result{}
	}
	if res.Log == nil {
		// The panic may have come from req.Log.WithContext itself, before the
		// bound logger was ever assigned. Without this the lane is handed a
		// Result with a nil logger: settleOnce would skip its recovery report,
		// and a settle path that logs — the classic lane's ack/nack failure
		// lines — would nil-deref, leaving the delivery UNSETTLED and silent.
		// The unbound logger is worse than the bound one and far better than none.
		res.Log = req.Log
	}
	res.Outcome = Panicked
	res.PanicType = fmt.Sprintf("%T", recovered)
	res.Stack = debug.Stack()
	res.Err = fmt.Errorf(panicMessage, recovered)
	if res.Duration == 0 {
		res.Duration = time.Since(start)
	}
	return res
}

// settleOnce hands the result to the lane's Settle exactly once, guarded. A
// panic inside Settle is the lane's own bug on its own broker call: retrying it
// would panic again, so it is logged and stopped rather than escalated into the
// consume loop.
func settleOnce(req *Request, res *Result) {
	if req.Settle == nil || res == nil {
		return
	}

	defer func() {
		recovered := recover()
		if recovered == nil || res.Log == nil {
			return
		}
		// The logger is the lane's; if logging the panic panics too, there is
		// nothing left to report it with.
		//
		// SECURITY: the panic value's TYPE only (ADR-081) — same rule and same
		// reason as AppendOutcome above.
		defer func() { _ = recover() }()
		res.Log.Error().Str("panic_type", fmt.Sprintf("%T", recovered)).Msg(settlePanicMessage)
	}()

	req.Settle(res)
}
