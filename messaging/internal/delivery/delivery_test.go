package delivery

import (
	"context"
	"errors"
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

// nopEvent satisfies logger.LogEvent without recording: the pipeline itself
// writes no line, so there is nothing on an event to assert.
type nopEvent struct{}

func (nopEvent) Msg(string)                                  {}
func (nopEvent) Msgf(string, ...any)                         {}
func (e nopEvent) Err(error) logger.LogEvent                 { return e }
func (e nopEvent) Str(_, _ string) logger.LogEvent           { return e }
func (e nopEvent) Int(string, int) logger.LogEvent           { return e }
func (e nopEvent) Int64(string, int64) logger.LogEvent       { return e }
func (e nopEvent) Uint64(string, uint64) logger.LogEvent     { return e }
func (e nopEvent) Dur(string, time.Duration) logger.LogEvent { return e }
func (e nopEvent) Interface(string, any) logger.LogEvent     { return e }
func (e nopEvent) Bytes(string, []byte) logger.LogEvent      { return e }
func (e nopEvent) Bool(string, bool) logger.LogEvent         { return e }
func (nopEvent) Enabled() bool                               { return true }

// bindingLogger records the context it was bound to and the logger that binding
// produced, so a test can assert WHICH logger the lane gets back.
type bindingLogger struct {
	boundTo context.Context
	bound   *bindingLogger
}

func (l *bindingLogger) WithContext(ctx any) logger.Logger {
	msgCtx, _ := ctx.(context.Context)
	l.bound = &bindingLogger{boundTo: msgCtx}
	return l.bound
}
func (l *bindingLogger) WithFields(map[string]any) logger.Logger { return l }
func (l *bindingLogger) Info() logger.LogEvent                   { return nopEvent{} }
func (l *bindingLogger) Error() logger.LogEvent                  { return nopEvent{} }
func (l *bindingLogger) Debug() logger.LogEvent                  { return nopEvent{} }
func (l *bindingLogger) Warn() logger.LogEvent                   { return nopEvent{} }
func (l *bindingLogger) Fatal() logger.LogEvent                  { return nopEvent{} }

var _ logger.Logger = (*bindingLogger)(nil)

// setupTelemetry installs an in-memory span exporter and a test meter provider,
// both restored on cleanup. The tracking instruments are package singletons, so
// the reset brackets the test on both sides.
func setupTelemetry(t *testing.T) (*tracetest.InMemoryExporter, *obtest.TestMeterProvider) {
	t.Helper()

	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	prevMP := otel.GetMeterProvider()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	mp := obtest.NewTestMeterProvider()
	otel.SetMeterProvider(mp)
	tracking.ResetMeterForTesting()

	t.Cleanup(func() {
		require.NoError(t, tp.Shutdown(context.Background()))
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
		otel.SetMeterProvider(prevMP)
		tracking.ResetMeterForTesting()
		require.NoError(t, mp.Shutdown(context.Background()))
	})

	return exporter, mp
}

// outcomes records every Result the lane's LogOutcome was handed.
type outcomes struct {
	seen []*Result
}

func (o *outcomes) log(res *Result) { o.seen = append(o.seen, res) }

// newRequest builds a Request with the classic lane's shape and a handler the
// test supplies. Fields a test cares about are overwritten by the caller.
func newRequest(log logger.Logger, rec *outcomes, handle Handler) *Request {
	return &Request{
		Carrier:     mapCarrier{},
		Destination: testQueue,
		BodySize:    12,
		Metrics:     tracking.AMQPConsumeAttributes("events", "orders.created", testQueue),
		Log:         log,
		Handle:      handle,
		LogOutcome:  rec.log,
	}
}

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
	setupTelemetry(t)
	rec := &outcomes{}

	res := Run(context.Background(), newRequest(&bindingLogger{}, rec, func(context.Context, logger.Logger, string) error {
		time.Sleep(time.Millisecond)
		return nil
	}))

	require.NotNil(t, res)
	assert.Equal(t, Succeeded, res.Outcome)
	assert.NoError(t, res.Err)
	assert.Nil(t, res.Panic)
	assert.Nil(t, res.Stack)
	assert.GreaterOrEqual(t, res.Duration, time.Millisecond)
}

func TestRunReportsHandlerErrorAndCarriesTheError(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}
	handlerErr := errors.New("boom")

	res := Run(context.Background(), newRequest(&bindingLogger{}, rec, func(context.Context, logger.Logger, string) error {
		return handlerErr
	}))

	assert.Equal(t, HandlerError, res.Outcome)
	assert.Same(t, handlerErr, res.Err)
	assert.Nil(t, res.Panic)
}

func TestRunConvertsAPanicIntoAnError(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}

	var res *Result
	require.NotPanics(t, func() {
		res = Run(context.Background(), newRequest(&bindingLogger{}, rec, func(context.Context, logger.Logger, string) error {
			panic("handler exploded")
		}))
	})

	assert.Equal(t, Panicked, res.Outcome)
	require.Error(t, res.Err)
	// The classic lane's wording, now both lanes'.
	assert.Equal(t, "panic in message handler: handler exploded", res.Err.Error())
	assert.Equal(t, "handler exploded", res.Panic)
	assert.NotEmpty(t, res.Stack)
}

func TestRunBindsTheLoggerToThePerMessageContext(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}
	base := &bindingLogger{}

	var handleLog logger.Logger
	var handleCtx context.Context
	res := Run(context.Background(), newRequest(base, rec, func(ctx context.Context, log logger.Logger, _ string) error {
		handleCtx, handleLog = ctx, log
		return nil
	}))

	require.NotNil(t, base.bound)
	assert.Same(t, base.bound, res.Log, "the lane settles through the context-bound logger")
	assert.Same(t, base.bound, handleLog, "the handler adapter gets the same one")
	assert.Same(t, base.bound.boundTo, handleCtx, "and it is bound to the context the handler ran under")
}

func TestRunCarriesTheCarrierTraceIDIntoTheContextAndTheResult(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}

	req := newRequest(&bindingLogger{}, rec, func(ctx context.Context, _ logger.Logger, traceID string) error {
		assert.Equal(t, testTraceID, traceID)
		got, ok := gobrickstrace.IDFromContext(ctx)
		assert.True(t, ok)
		assert.Equal(t, testTraceID, got)
		return nil
	})
	req.Carrier = mapCarrier{gobrickstrace.HeaderXRequestID: testTraceID}

	res := Run(context.Background(), req)

	assert.Equal(t, testTraceID, res.TraceID)
}

func TestRunGeneratesATraceIDWhenNoneTraveled(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}

	first := Run(context.Background(), newRequest(&bindingLogger{}, rec, succeedingHandler))
	second := Run(context.Background(), newRequest(&bindingLogger{}, rec, succeedingHandler))

	assert.NotEmpty(t, first.TraceID)
	assert.NotEqual(t, first.TraceID, second.TraceID)
}

func TestRunAcceptsANilCarrier(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}

	req := newRequest(&bindingLogger{}, rec, succeedingHandler)
	req.Carrier = nil

	var res *Result
	require.NotPanics(t, func() { res = Run(context.Background(), req) })
	assert.Equal(t, Succeeded, res.Outcome)
	assert.NotEmpty(t, res.TraceID)
}

func TestRunStartsARootSpanWhenOnlyAW3CHeaderTraveled(t *testing.T) {
	exporter, _ := setupTelemetry(t)
	rec := &outcomes{}

	req := newRequest(&bindingLogger{}, rec, succeedingHandler)
	req.Carrier = mapCarrier{gobrickstrace.HeaderTraceParent: testTraceParent}

	Run(context.Background(), req)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	// go-bricks extraction populates context VALUES, not the OTel span context,
	// so a consume span has always been a root span. Changing that would
	// re-parent every existing consumer span; ADR-068 keeps it out of scope.
	assert.False(t, spans[0].Parent.IsValid())
	tp, ok := gobrickstrace.ParentFromContext(rec.seen[0].Log.(*bindingLogger).boundTo)
	require.True(t, ok)
	assert.Equal(t, testTraceParent, tp)
}

func TestRunStartsOneConsumerSpanPerMessage(t *testing.T) {
	exporter, _ := setupTelemetry(t)
	rec := &outcomes{}

	req := newRequest(&bindingLogger{}, rec, succeedingHandler)
	req.SpanExtras = []attribute.KeyValue{attribute.String("messaging.rabbitmq.exchange", "events")}

	Run(context.Background(), req)

	spans := exporter.GetSpans()
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
			exporter, _ := setupTelemetry(t)
			rec := &outcomes{}

			Run(context.Background(), newRequest(&bindingLogger{}, rec, tt.handle))

			spans := exporter.GetSpans()
			require.Len(t, spans, 1)
			assert.Equal(t, codes.Error, spans[0].Status.Code)
			assert.Equal(t, tt.wantMsg, spans[0].Status.Description)
			require.Len(t, spans[0].Events, 1)
			assert.Equal(t, "exception", spans[0].Events[0].Name)
		})
	}
}

func TestRunRecordsOneConsumeAtCompletion(t *testing.T) {
	_, mp := setupTelemetry(t)
	rec := &outcomes{}

	Run(context.Background(), newRequest(&bindingLogger{}, rec, func(context.Context, logger.Logger, string) error {
		time.Sleep(time.Millisecond)
		return nil
	}))

	rm := mp.Collect(t)
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
	_, mp := setupTelemetry(t)
	rec := &outcomes{}

	Run(context.Background(), newRequest(&bindingLogger{}, rec, func(context.Context, logger.Logger, string) error {
		time.Sleep(time.Millisecond)
		return errors.New("nope")
	}))

	rm := mp.Collect(t)

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
					return nil
				}
			},
		},
		{
			name: "panicked",
			handle: func(released *bool) Handler {
				return func(ctx context.Context, _ logger.Logger, _ string) error {
					leasescope.Register(ctx, func() { *released = true })
					panic("after borrowing")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTelemetry(t)
			rec := &outcomes{}
			released := false

			req := newRequest(&bindingLogger{}, rec, tt.handle(&released))
			req.LogOutcome = func(res *Result) {
				// The lane logs and settles while the lease is still held: a
				// handle borrowed by the handler must outlive the outcome line.
				assert.False(t, released, "the scope drained before the lane saw the outcome")
				// The lane's line reads these, so they must already be filled in
				// by the time it runs — not after the outcome line.
				assert.NotEmpty(t, res.TraceID)
				assert.Positive(t, res.Duration)
				assert.NotNil(t, res.Log)
				rec.log(res)
			}

			Run(context.Background(), req)

			assert.True(t, released, "the scope must drain once the message is done")
		})
	}
}

func TestRunEndsTheSpanBeforeItDrainsTheLeaseScope(t *testing.T) {
	setupTelemetry(t)
	rec := &outcomes{}

	var order []string
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(onEndRecorder{onEnd: func() {
		order = append(order, "span-end")
	}}))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { require.NoError(t, tp.Shutdown(context.Background())) })

	Run(context.Background(), newRequest(&bindingLogger{}, rec, func(ctx context.Context, _ logger.Logger, _ string) error {
		leasescope.Register(ctx, func() { order = append(order, "release") })
		return nil
	}))

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
			setupTelemetry(t)
			rec := &outcomes{}

			res := Run(context.Background(), newRequest(&bindingLogger{}, rec, tt.handle))

			require.Len(t, rec.seen, 1)
			assert.Same(t, res, rec.seen[0], "the lane logs and settles the same Result")
			assert.Equal(t, tt.want, res.Outcome)
			assert.NotEmpty(t, res.TraceID)
			assert.NotNil(t, res.Log)
		})
	}
}
