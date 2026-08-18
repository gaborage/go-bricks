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

	panicMessage = "panic in message handler: %v"

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
// trace ID.
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

// Run puts one message through the delivery pipeline and returns the outcome for
// the lane to settle. It never returns nil, and a handler panic never escapes: it
// becomes a Panicked result carrying the recovered value, its stack, and an
// error. A panic in the lane's own LogOutcome or Log is the lane's bug and does
// propagate — the span still ends and the lease scope still drains, both deferred.
func Run(ctx context.Context, req *Request) *Result {
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
	defer scope.ReleaseAll()
	defer span.End()

	log := req.Log.WithContext(msgCtx)
	traceID := gobrickstrace.EnsureTraceID(msgCtx)

	res := invoke(msgCtx, log, traceID, req.Handle)
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
