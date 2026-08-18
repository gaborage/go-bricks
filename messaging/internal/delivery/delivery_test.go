package delivery

import (
	"context"
	"errors"
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
		require.NoError(t, ttp.Shutdown(context.Background()))
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
		otel.SetMeterProvider(prevMP)
		tracking.ResetMeterForTesting()
		ResetTracerForTesting()
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
	assert.Nil(t, res.Panic)
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
	assert.Nil(t, res.Panic)
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
	assert.Equal(t, "panic in message handler: handler exploded", res.Err.Error())
	assert.Equal(t, "handler exploded", res.Panic)
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
			wantMsg: "panic in message handler: nope",
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
