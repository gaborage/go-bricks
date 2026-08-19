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
	"fmt"
	"runtime/debug"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/internal/leasescope"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

const (
	// tracerName is the one instrumentation scope both lanes report under.
	tracerName = "go-bricks/messaging"

	spanOperationReceive = "receive"
	messagingSystem      = "rabbitmq"

	panicMessage       = "panic in message handler: %v"
	settlePanicMessage = "Panic recovered while settling a delivery; not retried"

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

// Result is what the lane settles on. Panic and Stack are set only when Outcome
// is Panicked; Err is nil only when Outcome is Succeeded. A pointer, not a
// value, so the lane's LogOutcome does not copy it per message.
type Result struct {
	Outcome  Outcome
	Err      error
	Duration time.Duration
	TraceID  string
	Log      logger.Logger
	Panic    any
	Stack    []byte
}

// AppendOutcome stamps the outcome fields both lanes share. The caller creates
// the event, so the level, the lane's own fields and the message text stay with
// the lane.
func AppendOutcome(e logger.LogEvent, res *Result) logger.LogEvent {
	e = e.Str("correlation_id", res.TraceID).
		Dur("processing_time", res.Duration)
	if res.Outcome == Panicked {
		e = e.Interface("panic", res.Panic).Bytes("stack", res.Stack)
	}
	return e
}

// Run puts one message through the delivery pipeline and returns the outcome for
// the lane to settle. It never returns nil, and a handler panic never escapes: it
// becomes a Panicked result carrying the recovered value, its stack, and an
// error. A panic in the lane's own LogOutcome or Log is the lane's bug and does
// propagate — the span still ends and the lease scope still drains, both deferred.
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

	res = invoke(msgCtx, log, traceID, req.Handle)
	res.Duration = time.Since(start)
	res.TraceID = traceID
	res.Log = log

	req.LogOutcome(res)

	if res.Err != nil {
		span.RecordError(res.Err)
		span.SetStatus(codes.Error, res.Err.Error())
	}

	tracking.RecordConsume(msgCtx, req.Metrics, res.Duration, res.Err)

	return res
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

// invoke runs the lane's handler, turning a panic into an error so one tail
// logs, marks and records every outcome.
func invoke(ctx context.Context, log logger.Logger, traceID string, handle Handler) (res *Result) {
	res = &Result{}

	defer func() {
		if recovered := recover(); recovered != nil {
			res.Outcome = Panicked
			res.Panic = recovered
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
	res.Panic = recovered
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
		defer func() { _ = recover() }()
		res.Log.Error().Interface("panic", recovered).Msg(settlePanicMessage)
	}()

	req.Settle(res)
}
