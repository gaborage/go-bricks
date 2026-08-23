package delivery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/internal/leasescope"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
	obtest "github.com/gaborage/go-bricks/observability/testing"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

const (
	testQueue       = "orders"
	testTraceID     = "req-2026"
	testTraceParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
)

// mapCarrier is a Carrier over a plain map: the pipeline only ever reads through
// trace.HeaderAccessor, so a test does not need either lane's header type.
type mapCarrier map[string]any

func (c mapCarrier) Get(key string) any        { return c[key] }
func (c mapCarrier) Set(key string, value any) { c[key] = value }

// bindingLogger records the logger a binding produced and the context it was bound
// to, so a test can assert WHICH logger the lane gets back. Everything but
// WithContext comes from the embedded real logger.
type bindingLogger struct {
	logger.Logger
	boundTo context.Context
	bound   *bindingLogger
}

func (l *bindingLogger) WithContext(ctx any) logger.Logger {
	msgCtx, _ := ctx.(context.Context)
	l.bound = &bindingLogger{Logger: l.Logger, boundTo: msgCtx}
	return l.bound
}

var _ logger.Logger = (*bindingLogger)(nil)

// setupTelemetry installs a test tracer provider and a test meter provider, both
// restored on cleanup, plus any span processor the test needs on that same
// provider. The tracking meter and the pipeline's tracer are package singletons,
// so both resets bracket the test on both sides.
func setupTelemetry(t *testing.T, processors ...sdktrace.SpanProcessor) (*tracetest.InMemoryExporter, *obtest.TestMeterProvider) {
	t.Helper()

	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	prevMP := otel.GetMeterProvider()

	ttp := obtest.NewTestTraceProvider()
	for _, processor := range processors {
		ttp.RegisterSpanProcessor(processor)
	}
	otel.SetTracerProvider(ttp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	mp := obtest.NewTestMeterProvider()
	otel.SetMeterProvider(mp)
	tracking.ResetMeterForTesting()
	ResetTracerForTesting()

	t.Cleanup(func() {
		// Restore and reset BEFORE asserting: require.NoError runs Goexit on
		// failure, which would skip everything after it and leave a shut-down
		// provider installed process-wide, with sharedTracer still pinned to it —
		// so every later test in the binary would silently record nothing.
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
		otel.SetMeterProvider(prevMP)
		tracking.ResetMeterForTesting()
		ResetTracerForTesting()
		require.NoError(t, ttp.Shutdown(context.Background()))
		require.NoError(t, mp.Shutdown(context.Background()))
	})

	return ttp.Exporter, mp
}

// outcomes records every Result the lane's LogOutcome was handed.
type outcomes struct {
	seen []*Result
}

func (o *outcomes) log(res *Result) { o.seen = append(o.seen, res) }

// harness owns one test's telemetry, outcome recorder and logger, so each test
// states only what it is pinning.
type harness struct {
	exporter *tracetest.InMemoryExporter
	mp       *obtest.TestMeterProvider
	rec      *outcomes
	log      *bindingLogger
}

func newHarness(t *testing.T, processors ...sdktrace.SpanProcessor) *harness {
	t.Helper()
	exporter, mp := setupTelemetry(t, processors...)
	return &harness{
		exporter: exporter,
		mp:       mp,
		rec:      &outcomes{},
		log:      &bindingLogger{Logger: logger.New("error", false)},
	}
}

// request builds a Request with the classic lane's shape; a test overwrites the
// fields it cares about before handing it to runRequest.
func (h *harness) request(handle Handler) *Request {
	return &Request{
		Carrier:     mapCarrier{},
		Destination: testQueue,
		BodySize:    12,
		Metrics:     tracking.AMQPConsumeAttributes("events", "orders.created", testQueue),
		Log:         h.log,
		Handle:      handle,
		LogOutcome:  h.rec.log,
	}
}

func (h *harness) run(handle Handler) *Result { return h.runRequest(h.request(handle)) }

func (h *harness) runRequest(req *Request) *Result { return Run(context.Background(), req) }

func succeedingHandler(context.Context, logger.Logger, string) error { return nil }

func assertAttribute(t *testing.T, attrs []attribute.KeyValue, key string, want any) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			assert.Equal(t, want, attr.Value.AsInterface(), "attribute %s", key)
			return
		}
	}
	t.Errorf("attribute %s not found", key)
}

func assertNoAttribute(t *testing.T, attrs []attribute.KeyValue, key string) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			t.Errorf("attribute %s should be absent, got %v", key, attr.Value.AsInterface())
		}
	}
}

func TestRunReportsSucceededForAHandlerThatReturnsNil(t *testing.T) {
	h := newHarness(t)

	res := h.run(func(context.Context, logger.Logger, string) error {
		time.Sleep(time.Millisecond)
		return nil
	})

	require.NotNil(t, res)
	assert.Equal(t, Succeeded, res.Outcome)
	assert.NoError(t, res.Err)
	assert.Empty(t, res.PanicType)
	assert.Nil(t, res.Stack)
	assert.GreaterOrEqual(t, res.Duration, time.Millisecond)
}

func TestRunReportsHandlerErrorAndCarriesTheError(t *testing.T) {
	h := newHarness(t)
	handlerErr := errors.New("boom")

	res := h.run(func(context.Context, logger.Logger, string) error {
		return handlerErr
	})

	assert.Equal(t, HandlerError, res.Outcome)
	assert.Same(t, handlerErr, res.Err)
	assert.Empty(t, res.PanicType)
}

func TestRunConvertsAPanicIntoAnError(t *testing.T) {
	h := newHarness(t)

	var res *Result
	require.NotPanics(t, func() {
		res = h.run(func(context.Context, logger.Logger, string) error {
			panic("handler exploded")
		})
	})

	assert.Equal(t, Panicked, res.Outcome)
	require.Error(t, res.Err)
	// The classic lane's wording, now both lanes'.
	assert.Equal(t, "panic in message handler (type: string)", res.Err.Error())
	assert.Equal(t, "string", res.PanicType)
	assert.NotEmpty(t, res.Stack)
}

func TestRunBindsTheLoggerToThePerMessageContext(t *testing.T) {
	h := newHarness(t)

	var handleLog logger.Logger
	var handleCtx context.Context
	res := h.run(func(ctx context.Context, log logger.Logger, _ string) error {
		handleCtx, handleLog = ctx, log
		return nil
	})

	require.NotNil(t, h.log.bound)
	assert.Same(t, h.log.bound, res.Log, "the lane settles through the context-bound logger")
	assert.Same(t, h.log.bound, handleLog, "the handler adapter gets the same one")
	assert.Same(t, h.log.bound.boundTo, handleCtx, "and it is bound to the context the handler ran under")
}

func TestRunCarriesTheCarrierTraceIDIntoTheContextAndTheResult(t *testing.T) {
	h := newHarness(t)

	req := h.request(func(ctx context.Context, _ logger.Logger, traceID string) error {
		assert.Equal(t, testTraceID, traceID)
		got, ok := gobrickstrace.IDFromContext(ctx)
		assert.True(t, ok)
		assert.Equal(t, testTraceID, got)
		return nil
	})
	req.Carrier = mapCarrier{gobrickstrace.HeaderXRequestID: testTraceID}

	res := h.runRequest(req)

	assert.Equal(t, testTraceID, res.TraceID)
}

func TestRunGeneratesATraceIDWhenNoneTraveled(t *testing.T) {
	h := newHarness(t)

	first := h.run(succeedingHandler)
	second := h.run(succeedingHandler)

	assert.NotEmpty(t, first.TraceID)
	assert.NotEqual(t, first.TraceID, second.TraceID)
}

// The generated-trace-ID path is the one the write-back exists for: without it
// EnsureTraceID mints a fresh id on every call, so the id the pipeline reports is
// neither readable from the handler's context nor stable within one delivery.
func TestRunRebindsAGeneratedTraceIDIntoTheContext(t *testing.T) {
	h := newHarness(t)

	var fromContext, reEnsured string
	var found bool
	req := h.request(func(ctx context.Context, _ logger.Logger, _ string) error {
		fromContext, found = gobrickstrace.IDFromContext(ctx)
		reEnsured = gobrickstrace.EnsureTraceID(ctx)
		return nil
	})

	res := h.runRequest(req)

	require.True(t, found, "a generated trace ID must be readable from the per-message context")
	assert.Equal(t, res.TraceID, fromContext)
	assert.Equal(t, res.TraceID, reEnsured, "EnsureTraceID re-mints when the id was never written back")
}

func TestRunAcceptsANilCarrier(t *testing.T) {
	h := newHarness(t)

	req := h.request(succeedingHandler)
	req.Carrier = nil

	var res *Result
	require.NotPanics(t, func() { res = h.runRequest(req) })
	assert.Equal(t, Succeeded, res.Outcome)
	assert.NotEmpty(t, res.TraceID)
}

func TestRunStartsARootSpanWhenOnlyAW3CHeaderTraveled(t *testing.T) {
	h := newHarness(t)

	req := h.request(succeedingHandler)
	req.Carrier = mapCarrier{gobrickstrace.HeaderTraceParent: testTraceParent}

	h.runRequest(req)

	spans := h.exporter.GetSpans()
	require.Len(t, spans, 1)
	// go-bricks extraction populates context VALUES, not the OTel span context,
	// so a consume span has always been a root span. Changing that would
	// re-parent every existing consumer span; ADR-068 keeps it out of scope.
	assert.False(t, spans[0].Parent.IsValid())
	tp, ok := gobrickstrace.ParentFromContext(h.rec.seen[0].Log.(*bindingLogger).boundTo)
	require.True(t, ok)
	assert.Equal(t, testTraceParent, tp)
}

func TestRunStartsOneConsumerSpanPerMessage(t *testing.T) {
	h := newHarness(t)

	req := h.request(succeedingHandler)
	req.SpanExtras = []attribute.KeyValue{attribute.String("messaging.rabbitmq.exchange", "events")}

	h.runRequest(req)

	spans := h.exporter.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, testQueue+" receive", span.Name)
	assert.Equal(t, trace.SpanKindConsumer, span.SpanKind)
	assertAttribute(t, span.Attributes, string(semconv.MessagingSystemKey), "rabbitmq")
	assertAttribute(t, span.Attributes, string(semconv.MessagingOperationNameKey), "receive")
	assertAttribute(t, span.Attributes, string(semconv.MessagingDestinationNameKey), testQueue)
	assertAttribute(t, span.Attributes, string(semconv.MessagingMessageBodySizeKey), int64(12))
	assertAttribute(t, span.Attributes, "messaging.rabbitmq.exchange", "events")
	assert.Equal(t, codes.Unset, span.Status.Code)
}

func TestRunMarksTheSpanFailedForEveryFailingOutcome(t *testing.T) {
	tests := []struct {
		name    string
		handle  Handler
		wantMsg string
	}{
		{
			name:    "handler_error",
			handle:  func(context.Context, logger.Logger, string) error { return errors.New("nope") },
			wantMsg: "nope",
		},
		{
			name:    "panicked",
			handle:  func(context.Context, logger.Logger, string) error { panic("nope") },
			wantMsg: "panic in message handler (type: string)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)

			h.run(tt.handle)

			spans := h.exporter.GetSpans()
			require.Len(t, spans, 1)
			assert.Equal(t, codes.Error, spans[0].Status.Code)
			assert.Equal(t, tt.wantMsg, spans[0].Status.Description)
			require.Len(t, spans[0].Events, 1)
			assert.Equal(t, "exception", spans[0].Events[0].Name)
		})
	}
}

func TestRunRecordsOneConsumeAtCompletion(t *testing.T) {
	h := newHarness(t)

	h.run(func(context.Context, logger.Logger, string) error {
		time.Sleep(time.Millisecond)
		return nil
	})

	rm := h.mp.Collect(t)
	obtest.AssertMetricValue(t, rm, "messaging.client.consumed.messages", int64(1))

	durationMetric := obtest.FindMetric(rm, "messaging.client.operation.duration")
	require.NotNil(t, durationMetric)
	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, histData.DataPoints, 1)

	attrs := histData.DataPoints[0].Attributes.ToSlice()
	assertAttribute(t, attrs, "messaging.destination.name", "events:orders.created:orders")
	assertNoAttribute(t, attrs, "error.type")
}

func TestRunRecordsAFailedDeliveryWithItsErrorType(t *testing.T) {
	h := newHarness(t)

	h.run(func(context.Context, logger.Logger, string) error {
		time.Sleep(time.Millisecond)
		return errors.New("nope")
	})

	rm := h.mp.Collect(t)

	consumed := obtest.FindMetric(rm, "messaging.client.consumed.messages")
	require.NotNil(t, consumed)
	sumData, ok := consumed.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sumData.DataPoints, 1)
	assert.Equal(t, int64(1), sumData.DataPoints[0].Value)
	assertAttribute(t, sumData.DataPoints[0].Attributes.ToSlice(), "error.type", "*errors.errorString")
}

func TestRunInstallsALeaseScopeAndDrainsItAfterTheHandler(t *testing.T) {
	tests := []struct {
		name   string
		handle func(released *bool) Handler
	}{
		{
			name: "succeeded",
			handle: func(released *bool) Handler {
				return func(ctx context.Context, _ logger.Logger, _ string) error {
					leasescope.Register(ctx, func() { *released = true })
					time.Sleep(2 * time.Millisecond) // outlast Windows' coarse clock so Duration reads > 0
					return nil
				}
			},
		},
		{
			name: "panicked",
			handle: func(released *bool) Handler {
				return func(ctx context.Context, _ logger.Logger, _ string) error {
					leasescope.Register(ctx, func() { *released = true })
					time.Sleep(2 * time.Millisecond)
					panic("after borrowing")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			released := false

			req := h.request(tt.handle(&released))
			req.LogOutcome = func(res *Result) {
				// The lane logs and settles while the lease is still held: a
				// handle borrowed by the handler must outlive the outcome line.
				assert.False(t, released, "the scope drained before the lane saw the outcome")
				// The lane's line reads these, so they must already be filled in
				// by the time it runs — not after the outcome line.
				assert.NotEmpty(t, res.TraceID)
				assert.Positive(t, res.Duration)
				assert.NotNil(t, res.Log)
				h.rec.log(res)
			}

			h.runRequest(req)

			assert.True(t, released, "the scope must drain once the message is done")
		})
	}
}

func TestRunEndsTheSpanBeforeItDrainsTheLeaseScope(t *testing.T) {
	var order []string
	h := newHarness(t, onEndRecorder{onEnd: func() { order = append(order, "span-end") }})

	h.run(func(ctx context.Context, _ logger.Logger, _ string) error {
		leasescope.Register(ctx, func() { order = append(order, "release") })
		return nil
	})

	assert.Equal(t, []string{"span-end", "release"}, order)
}

// onEndRecorder is a span processor that only reports that a span ended, so the
// pipeline's defer order can be asserted.
type onEndRecorder struct {
	onEnd func()
}

func (onEndRecorder) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (p onEndRecorder) OnEnd(sdktrace.ReadOnlySpan)                   { p.onEnd() }
func (onEndRecorder) Shutdown(context.Context) error                  { return nil }
func (onEndRecorder) ForceFlush(context.Context) error                { return nil }

func TestRunHandsTheLaneOneOutcomePerMessage(t *testing.T) {
	tests := []struct {
		name   string
		handle Handler
		want   Outcome
	}{
		{name: "succeeded", handle: succeedingHandler, want: Succeeded},
		{
			name:   "handler_error",
			handle: func(context.Context, logger.Logger, string) error { return errors.New("nope") },
			want:   HandlerError,
		},
		{
			name:   "panicked",
			handle: func(context.Context, logger.Logger, string) error { panic("nope") },
			want:   Panicked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)

			res := h.run(tt.handle)

			require.Len(t, h.rec.seen, 1)
			assert.Same(t, res, h.rec.seen[0], "the lane logs and settles the same Result")
			assert.Equal(t, tt.want, res.Outcome)
			assert.NotEmpty(t, res.TraceID)
			assert.NotNil(t, res.Log)
		})
	}
}

// TestTracerCacheSurvivesAConcurrentReset pins the reset hook's contract: a
// delivery that already resolved the tracer keeps it, one resolving alongside a
// reset never sees nil, and -race stays quiet across the atomic pointer.
func TestTracerCacheSurvivesAConcurrentReset(t *testing.T) {
	setupTelemetry(t)

	// One gate releases workers and resetter together, and the resets run in a
	// joined goroutine: without both, the scheduler can drain every reset before
	// the first tracer() call and the test proves nothing about the race.
	start := make(chan struct{})
	var wg sync.WaitGroup
	var nilTracers atomic.Int64
	for range 8 {
		wg.Go(func() {
			<-start
			for range 200 {
				if tracer() == nil { // no require in the worker: a Goexit there would hide the failure
					nilTracers.Add(1)
				}
			}
		})
	}
	wg.Go(func() {
		<-start
		for range 200 {
			ResetTracerForTesting()
		}
	})
	close(start)
	wg.Wait()
	assert.Zero(t, nilTracers.Load(), "a delivery racing a reset must never see a nil tracer")
}

// ===== AppendOutcome Tests =====

// outcomeEvent records every field write in order, so a test can pin the exact
// shape AppendOutcome stamps — including that it stamps nothing else.
type outcomeEvent struct {
	pairs [][2]any
	msg   string
}

func (e *outcomeEvent) add(key string, value any) logger.LogEvent {
	e.pairs = append(e.pairs, [2]any{key, value})
	return e
}

func (e *outcomeEvent) Msg(msg string)                          { e.msg = msg }
func (e *outcomeEvent) Msgf(format string, args ...any)         { e.msg = fmt.Sprintf(format, args...) }
func (e *outcomeEvent) Err(err error) logger.LogEvent           { return e.add("error", err) }
func (e *outcomeEvent) Str(k, v string) logger.LogEvent         { return e.add(k, v) }
func (e *outcomeEvent) Int(k string, v int) logger.LogEvent     { return e.add(k, v) }
func (e *outcomeEvent) Int64(k string, v int64) logger.LogEvent { return e.add(k, v) }
func (e *outcomeEvent) Uint64(k string, v uint64) logger.LogEvent {
	return e.add(k, v)
}
func (e *outcomeEvent) Dur(k string, v time.Duration) logger.LogEvent { return e.add(k, v) }
func (e *outcomeEvent) Interface(k string, v any) logger.LogEvent     { return e.add(k, v) }
func (e *outcomeEvent) Bytes(k string, v []byte) logger.LogEvent      { return e.add(k, v) }
func (e *outcomeEvent) Bool(k string, v bool) logger.LogEvent         { return e.add(k, v) }
func (e *outcomeEvent) Enabled() bool                                 { return true }

var _ logger.LogEvent = (*outcomeEvent)(nil)

func TestAppendOutcomeStampsTheSpine(t *testing.T) {
	tests := []struct {
		name    string
		outcome Outcome
	}{
		{name: "succeeded", outcome: Succeeded},
		{name: "handler_error", outcome: HandlerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := &outcomeEvent{}
			res := &Result{Outcome: tt.outcome, TraceID: testTraceID, Duration: 7 * time.Millisecond}

			got := AppendOutcome(event, res)

			assert.Same(t, event, got, "the lane keeps appending to the same event")
			assert.Equal(t, [][2]any{
				{"correlation_id", testTraceID},
				{"processing_time", 7 * time.Millisecond},
			}, event.pairs, "panic and stack belong to the Panicked outcome only")
			assert.Empty(t, event.msg, "the message text is the lane's")
		})
	}
}

func TestAppendOutcomeAddsThePanicTypeAndItsStack(t *testing.T) {
	event := &outcomeEvent{}
	stack := []byte("goroutine 1 [running]:")
	res := &Result{
		Outcome:   Panicked,
		TraceID:   testTraceID,
		Duration:  3 * time.Millisecond,
		PanicType: "string",
		Stack:     stack,
	}

	AppendOutcome(event, res)

	assert.Equal(t, [][2]any{
		{"correlation_id", testTraceID},
		{"processing_time", 3 * time.Millisecond},
		{"panic_type", "string"},
		{"stack", stack},
	}, event.pairs)
}

// ===== Settle Tests (ADR-069) =====

var errBoom = errors.New("boom")

// settleRecord captures every Settle call. Counting rather than flagging is what
// makes "exactly once" provable: a boolean cannot tell one call from two, and
// double-settling is the defect this guarantee exists to prevent.
type settleRecord struct{ outcomes []Outcome }

func (s *settleRecord) settle(res *Result) { s.outcomes = append(s.outcomes, res.Outcome) }

func TestRunSettlesEveryOutcomeExactlyOnce(t *testing.T) {
	tests := []struct {
		name    string
		handle  Handler
		outcome Outcome
	}{
		{name: "succeeded", handle: succeedingHandler, outcome: Succeeded},
		{
			name:    "handler_error",
			handle:  func(context.Context, logger.Logger, string) error { return errBoom },
			outcome: HandlerError,
		},
		{
			name:    "handler_panic",
			handle:  func(context.Context, logger.Logger, string) error { panic("boom") },
			outcome: Panicked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			settles := &settleRecord{}

			req := h.request(tt.handle)
			req.Settle = settles.settle
			h.runRequest(req)

			assert.Equal(t, []Outcome{tt.outcome}, settles.outcomes,
				"the delivery settles exactly once, on its own outcome")
		})
	}
}

// A panic in the lane's own outcome line must not leave the delivery unsettled:
// the broker would hold it until the consumer's channel closed, and the message
// would be redelivered with no record of the first attempt.
func TestRunSettlesEvenWhenTheDeliveryTailPanics(t *testing.T) {
	h := newHarness(t)
	settles := &settleRecord{}

	req := h.request(succeedingHandler)
	req.LogOutcome = func(*Result) { panic("the lane's own line blew up") }
	req.Settle = settles.settle

	var settleLog logger.Logger
	req.Settle = func(res *Result) {
		settles.settle(res)
		settleLog = res.Log
	}

	require.NotPanics(t, func() { h.runRequest(req) }, "a tail panic never escapes into the consume loop")

	// The discriminator: a mutant that drops the recover makes the test panic and
	// records NOTHING, so asserting the recorded outcome — not merely that the
	// run returned — is what separates "recovered and settled" from "crashed".
	assert.Equal(t, []Outcome{Panicked}, settles.outcomes,
		"a delivery whose tail panicked settles as Panicked, so the lane nacks rather than acks it")

	// The fallback in panickedResult is a fallback, not a replacement: a delivery
	// that DID get a bound logger keeps it, so the lane's ack/nack failure line
	// still carries trace_id and span_id. Only a delivery that never got one is
	// handed the unbound logger instead of nil.
	require.NotNil(t, h.log.bound)
	assert.Same(t, h.log.bound, settleLog,
		"a tail panic settles through the context-bound logger, not the unbound one")
}

// A panic inside Settle is the lane's own bug on its own broker call. Retrying
// would panic again, so the pipeline logs it and stops — and above all does not
// let it escape into the consume loop.
func TestRunDoesNotRetryASettleThatPanics(t *testing.T) {
	h := newHarness(t)
	calls := 0

	req := h.request(succeedingHandler)
	req.Settle = func(*Result) {
		calls++
		panic("settle blew up")
	}

	require.NotPanics(t, func() { h.runRequest(req) })

	assert.Equal(t, 1, calls, "a panicking Settle is called once and not retried")
}

// Settle is optional: a lane that has not adopted it yet must still deliver.
func TestRunAcceptsALaneWithNoSettle(t *testing.T) {
	h := newHarness(t)

	req := h.request(succeedingHandler)
	req.Settle = nil

	var res *Result
	require.NotPanics(t, func() { res = h.runRequest(req) })
	assert.Equal(t, Succeeded, res.Outcome)
}

// Ordering is the whole point of deferring the guard first: the lane must not
// settle a message while a handle its handler borrowed is still open.
func TestRunSettlesAfterTheSpanClosedAndTheLeaseDrained(t *testing.T) {
	var order []string
	h := newHarness(t, onEndRecorder{onEnd: func() { order = append(order, "span-end") }})

	req := h.request(func(ctx context.Context, _ logger.Logger, _ string) error {
		leasescope.Register(ctx, func() { order = append(order, "lease-drain") })
		return nil
	})
	req.Settle = func(*Result) { order = append(order, "settle") }

	h.runRequest(req)

	assert.Equal(t, []string{"span-end", "lease-drain", "settle"}, order,
		"span end -> lease drain -> settle, so a borrowed handle outlives the acknowledgement")
}

// panicOnBindLogger panics from WithContext, which Run calls BEFORE invoke. It
// is the only way to reach the tail guard with no Result yet built, which is the
// path that decides whether a delivery can be settled at all when the pipeline
// itself fails early.
type panicOnBindLogger struct{ logger.Logger }

func (l *panicOnBindLogger) WithContext(any) logger.Logger {
	// Outlast Windows' coarse clock before panicking, matching the idiom at the
	// lease test above: this path finishes inside a single ~15.6ms timer tick
	// there, so time.Since reads exactly 0 and the duration assertion below
	// would be unsound rather than wrong. Sleeping keeps the claim true on every
	// platform instead of weakening it to >= 0, which would assert nothing — and
	// would let the `res.Duration == 0` guard's mutant survive.
	time.Sleep(2 * time.Millisecond)
	panic("binding blew up")
}

func TestRunSettlesAPanicRaisedBeforeTheHandlerRan(t *testing.T) {
	h := newHarness(t)
	settles := &settleRecord{}
	var settled *Result

	req := h.request(succeedingHandler)
	req.Log = &panicOnBindLogger{Logger: logger.New("error", false)}
	req.Settle = func(res *Result) {
		settles.settle(res)
		settled = res
	}

	require.NotPanics(t, func() { h.runRequest(req) })

	assert.Equal(t, []Outcome{Panicked}, settles.outcomes,
		"a panic before the handler ran still settles, so the message is not left in flight")
	require.NotNil(t, settled, "the lane is handed a result even when none had been built yet")
	assert.Positive(t, settled.Duration,
		"a result synthesized by the guard still carries how long the delivery took")
	assert.NotEmpty(t, settled.PanicType)
	assert.NotEmpty(t, settled.Stack)
}

// lineRecorder captures whether the pipeline logged anything, so the settle-panic
// report is observable. delivery cannot import the lane-contract recorder: that
// package imports this one.
type lineRecorder struct {
	logger.Logger
	msgs *[]string
}

func (l *lineRecorder) WithContext(ctx any) logger.Logger {
	// Delegate, so a wrapped logger whose binding panics still panics through here.
	l.Logger.WithContext(ctx)
	return l
}
func (l *lineRecorder) Error() logger.LogEvent { return &recordingLine{msgs: l.msgs} }

type recordingLine struct {
	logger.LogEvent
	msgs *[]string
}

func (e *recordingLine) Interface(string, any) logger.LogEvent { return e }
func (e *recordingLine) Str(string, string) logger.LogEvent    { return e }
func (e *recordingLine) Msg(msg string)                        { *e.msgs = append(*e.msgs, msg) }

// A panic inside Settle must be REPORTED, not swallowed: it is the lane's own
// broker call failing, and an operator with no log line sees a message that
// simply never settled.
func TestRunLogsAPanicRaisedInsideSettle(t *testing.T) {
	h := newHarness(t)
	var msgs []string

	req := h.request(succeedingHandler)
	req.Log = &lineRecorder{Logger: logger.New("error", false), msgs: &msgs}
	req.Settle = func(*Result) { panic("settle blew up") }

	require.NotPanics(t, func() { h.runRequest(req) })

	assert.Equal(t, []string{settlePanicMessage}, msgs,
		"the settle panic is reported exactly once, under its own message")
}

// A Settle that returns normally must log nothing: a report on the happy path
// would make the settle-panic line meaningless as an alert.
func TestRunLogsNothingWhenSettleSucceeds(t *testing.T) {
	h := newHarness(t)
	var msgs []string

	req := h.request(succeedingHandler)
	req.Log = &lineRecorder{Logger: logger.New("error", false), msgs: &msgs}
	req.Settle = func(*Result) {}

	h.runRequest(req)

	assert.Empty(t, msgs, "a settlement that worked reports nothing")
}

// The compound failure the logger fallback exists for: the binding panics, so no
// bound logger was ever assigned, AND the lane's settle path then fails and tries
// to report it. Without the fallback the settle path nil-derefs, the recovery
// report is skipped, and the delivery is left unsettled and silent — the exact
// outcome the recovered tail exists to prevent.
func TestRunStillSettlesWhenTheBindingPanickedAndSettleAlsoFails(t *testing.T) {
	h := newHarness(t)
	var msgs []string
	settles := 0

	req := h.request(succeedingHandler)
	req.Log = &lineRecorder{Logger: &panicOnBindLogger{Logger: logger.New("error", false)}, msgs: &msgs}
	req.Settle = func(res *Result) {
		settles++
		// What the classic lane does when its broker call fails: log through the
		// result's logger. A nil one panics here.
		res.Log.Error().Interface("panic", "ack failed").Msg("Failed to ack message")
	}

	require.NotPanics(t, func() { h.runRequest(req) })

	// The discriminators: the settle RAN, and it logged. A mutant that drops the
	// fallback makes both of those false while still "panicking somewhere".
	assert.Equal(t, 1, settles, "the delivery is settled exactly once despite the binding panic")
	assert.Equal(t, []string{"Failed to ack message"}, msgs,
		"the lane's settle path can log, so a broker failure is reported rather than swallowed")
}

// capturingLogger hands out one recording event so a test can read what a
// reporting call actually stamped. Embeds logger.Logger so only the methods the
// path under test uses need defining.
type capturingLogger struct {
	logger.Logger
	event *outcomeEvent
}

func (l *capturingLogger) Error() logger.LogEvent        { return l.event }
func (l *capturingLogger) WithContext(any) logger.Logger { return l }

const deliveryPanicSecret = "not-a-real-secret-0002"

// TestAppendOutcomeNeverDisclosesThePanicValue pins that the lane's outcome line
// carries the panic's TYPE, not the value. The field is `panic`, which is not a
// needle, so the sensitive-data filter cannot mask it — a handler panicking with a
// bare string put that string in the log verbatim. The STACK stays: measured,
// debug.Stack() renders the panic frame as `panic({0x...})` and does not contain
// the value.
func TestAppendOutcomeNeverDisclosesThePanicValue(t *testing.T) {
	// Drive the REAL conversion rather than hand-building a Result: since Result
	// carries only PanicType, a hand-built fixture cannot hold the value and the
	// test would pass by construction.
	res := invoke(context.Background(), logger.New("disabled", true), testTraceID,
		func(context.Context, logger.Logger, string) error { panic(deliveryPanicSecret) })
	require.Equal(t, Panicked, res.Outcome)

	event := &outcomeEvent{}
	AppendOutcome(event, res)

	for _, pair := range event.pairs {
		assert.NotEqual(t, any(deliveryPanicSecret), pair[1],
			"the panic value must not be stamped onto the outcome line")
	}
	assert.Contains(t, event.pairs, [2]any{"panic_type", "string"},
		"the panic's type must be reported instead")

	// Structural, not incidental: no field of the Result a real panic produces may
	// contain the value. This fails if anyone re-adds a field that retains it.
	//
	// Rendering is deliberately shape-independent. A guard that only checked
	// `%v` would be conditional on the value's shape — the same weakness this
	// ADR condemns in the sensitive-data filter: `Stack []byte` renders as
	// `[110 111 …]` and a `*string` renders as an address, so both would hide a
	// secret in plain sight. renderDeep DEREFERENCES rather than formatting:
	// `%#v` on a *string prints the address, not the pointee, so it would hide
	// exactly what it is meant to find — established by probe, not assumed.
	// CanInterface keeps an unexported field from panicking the loop with a
	// message naming neither the field nor the leak.
	rv := reflect.ValueOf(*res)
	for i := range rv.NumField() {
		name := rv.Type().Field(i).Name
		f := rv.Field(i)
		require.True(t, f.CanInterface(),
			"Result field %q is unexported, so this guard cannot inspect it — "+
				"make it exported or assert it here explicitly", name)
		assert.NotContains(t, renderDeep(f), deliveryPanicSecret,
			"Result field %q retains the panic value", name)
	}
}

// TestSettlePanicNeverDisclosesThePanicValue pins the same rule on the settle
// recovery path, which reports through a different call.
func TestSettlePanicNeverDisclosesThePanicValue(t *testing.T) {
	h := newHarness(t)
	rec := &outcomeEvent{}

	req := h.request(succeedingHandler)
	req.Log = &capturingLogger{event: rec}
	req.Settle = func(*Result) { panic(deliveryPanicSecret) }

	require.NotPanics(t, func() { h.runRequest(req) })

	for _, pair := range rec.pairs {
		assert.NotEqual(t, any(deliveryPanicSecret), pair[1],
			"the settle panic value must not reach the log")
	}
}

// panickingReportLogger's event panics when the settle-panic report is written,
// keyed on `panic_type` — the field only that report uses. Aimed at that call
// specifically: a double that panicked on every call would take out the lane's
// ordinary logging instead, a surface this guard has nothing to do with.
type panickingReportLogger struct{ logger.Logger }

func (l *panickingReportLogger) Error() logger.LogEvent        { return &panickingReportEvent{} }
func (l *panickingReportLogger) WithContext(any) logger.Logger { return l }

type panickingReportEvent struct {
	logger.LogEvent
	reporting bool
}

func (e *panickingReportEvent) Str(key, _ string) logger.LogEvent {
	if key == "panic_type" {
		e.reporting = true
	}
	return e
}
func (e *panickingReportEvent) Interface(string, any) logger.LogEvent { return e }

func (e *panickingReportEvent) Msg(string) {
	if e.reporting {
		panic("logger write failed")
	}
}

// TestSettlePanicReportSurvivesAPanickingLogger pins settleOnce's terminal
// swallow. The report runs inside a defer that has already spent its recover(),
// so a panic in the logger — which is the lane's, i.e. consumer-supplied —
// escapes Run entirely unless the swallow catches it. Deleting that one line left
// this package green before this test existed; no mutation operator removes a
// defer, so neither the suite nor `make mutate` covered it.
func TestSettlePanicReportSurvivesAPanickingLogger(t *testing.T) {
	h := newHarness(t)

	req := h.request(succeedingHandler)
	req.Log = &panickingReportLogger{}
	req.Settle = func(*Result) { panic("settle blew up") }

	require.NotPanics(t, func() { h.runRequest(req) },
		"a panic while reporting a settle panic must not escape Run")
}

// TestRunKeepsAPanicValueOutOfTheSpan pins the property ADR-081 states: a value
// a consumer's handler chose to panic with must not reach the tracing backend.
// The span is the sink that leaves the platform, and both lanes deliberately
// withhold res.Err from the log on the panicked branch, so this is the only
// sink left to guard — and the one no log-field assertion covers.
func TestRunKeepsAPanicValueOutOfTheSpan(t *testing.T) {
	h := newHarness(t)
	h.run(func(context.Context, logger.Logger, string) error {
		panic(deliveryPanicSecret)
	})

	spans := h.exporter.GetSpans()
	require.Len(t, spans, 1)

	assert.NotContains(t, spans[0].Status.Description, deliveryPanicSecret,
		"span status description discloses the panic value")
	assert.Contains(t, spans[0].Status.Description, "string",
		"span status description should name the panic value's TYPE")

	require.Len(t, spans[0].Events, 1)
	for _, attr := range spans[0].Events[0].Attributes {
		assert.NotContains(t, attr.Value.String(), deliveryPanicSecret,
			"span exception attribute %q discloses the panic value", attr.Key)
	}
}

// nilProbeError is a typed nil carrier: `(*nilProbeError)(nil)` asserted to `error`
// compares NON-nil, because the interface header holds a type.
type nilProbeError struct{ text string }

func (e *nilProbeError) Error() string { return e.text }

type nilStringerProbe struct{ text string }

func (s *nilStringerProbe) String() string { return s.text }

// TestRenderDeepSurvivesATypedNil pins the case the interface `!= nil` guard could
// not see. Before the reflect-level nil test, renderSelfDescribing called Error()
// on a nil pointer here and the guard panicked instead of rendering — the guard
// exists to render arbitrary panic values, and a nil is among the likeliest to
// reach it. Reverting renderSelfDescribing to `if x != nil` turns this test RED
// with a nil pointer dereference.
func TestRenderDeepSurvivesATypedNil(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  any
	}{
		{name: "typed_nil_error", val: (*nilProbeError)(nil)},
		{name: "typed_nil_stringer", val: (*nilStringerProbe)(nil)},
		{name: "untyped_nil", val: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				_ = renderDeep(reflect.ValueOf(tc.val))
			}, "the guard must render a typed nil, not panic on it")
		})
	}
}

// renderDeep renders v so a secret cannot hide behind a shape. A guard
// conditional on the value's shape is the weakness ADR-081 condemns in the
// field-name filter, so this one must not be. Each helper below closes one
// blind spot that a probe caught in an earlier version of this guard — the
// names are the documentation of what was missed and why.
func renderDeep(v reflect.Value) string {
	// An untyped nil reaches here as the ZERO reflect.Value, whose Kind is Invalid
	// and on which CanInterface panics. Guard once at the entry rather than in each
	// helper below.
	if !v.IsValid() {
		return "<nil>"
	}
	if text, ok := renderSelfDescribing(v); ok {
		return text
	}
	v, addressable := derefToValue(v)
	if !addressable {
		return "<nil>"
	}
	if !v.CanInterface() {
		return ""
	}
	if text, ok := renderByteSequence(v); ok {
		return text
	}
	return renderComposite(v)
}

// renderSelfDescribing asks the value to render ITSELF, before any structural
// walk. An error or Stringer carries its text in unexported fields —
// `*errors.errorString` and `*fmt.wrapError` both do — so a struct walk sees
// nothing it can read and returns empty. Result.Err is the field actually
// derived from the panic value, so that blind spot sat over the one field that
// matters most. Runs before dereferencing, because the method set belongs to
// the pointer.
func renderSelfDescribing(v reflect.Value) (string, bool) {
	if !v.CanInterface() {
		return "", false
	}
	// A TYPED NIL passes `x != nil`: the interface header holds a type, so the
	// comparison is false only for a nil interface, and `(*E)(nil)` asserted to
	// `error` compares non-nil while `x.Error()` dereferences and panics. Test it
	// at the reflect level instead, the way derefToValue below already does — a
	// guard whose whole job is rendering arbitrary panic values must not panic on
	// the nil case, which is among the likeliest values to reach it.
	for v.Kind() == reflect.Interface {
		if v.IsNil() {
			return "", false
		}
		v = v.Elem()
	}
	if !v.CanInterface() {
		return "", false
	}
	if (v.Kind() == reflect.Pointer || v.Kind() == reflect.Map ||
		v.Kind() == reflect.Slice || v.Kind() == reflect.Func ||
		v.Kind() == reflect.Chan) && v.IsNil() {
		return "", false
	}
	switch x := v.Interface().(type) {
	case error:
		return x.Error(), true
	case fmt.Stringer:
		return x.String(), true
	}
	return "", false
}

// derefToValue follows pointers and interfaces to the value they hold. Without
// it `%v`/`%#v` on a *string prints an address, which hides exactly what this
// guard exists to find.
func derefToValue(v reflect.Value) (out reflect.Value, ok bool) {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return v, false
		}
		v = v.Elem()
	}
	return v, true
}

// renderByteSequence reads any byte sequence as TEXT, array included.
// reflect.Value.Bytes cannot serve the array case — it panics with
// "unaddressable byte array" on a [N]byte reached through reflect.ValueOf(*res)
// — but letting arrays fall through to the element walk renders `0x6e 0x6f …`
// and finds nothing, which is a blind guard rather than a panicking one.
// Neither is acceptable, so the array is copied into a slice and read as text.
func renderByteSequence(v reflect.Value) (string, bool) {
	kind := v.Kind()
	if kind != reflect.Slice && kind != reflect.Array {
		return "", false
	}
	if v.Type().Elem().Kind() != reflect.Uint8 {
		return "", false
	}
	if kind == reflect.Slice {
		return string(v.Bytes()), true
	}
	b := make([]byte, v.Len())
	for i := range b {
		b[i] = byte(v.Index(i).Uint())
	}
	return string(b), true
}

// renderComposite walks the members of a non-byte composite, and falls back to
// %#v for a scalar.
func renderComposite(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Slice, reflect.Array:
		return renderMembers(v.Len(), v.Index)
	case reflect.Struct:
		return renderMembers(v.NumField(), v.Field)
	default:
		return fmt.Sprintf("%#v", v.Interface())
	}
}

// renderMembers renders n members obtained by at, skipping any the reflect
// package will not let us read rather than panicking on them.
func renderMembers(n int, at func(int) reflect.Value) string {
	var sb strings.Builder
	for i := range n {
		member := at(i)
		if !member.CanInterface() {
			continue
		}
		sb.WriteString(renderDeep(member))
		sb.WriteByte(' ')
	}
	return sb.String()
}
