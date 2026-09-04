package messaging

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/logger"
	pipeline "github.com/gaborage/go-bricks/messaging/internal/delivery"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

const testQueueOtel = "test-queue"

// setupTestTracing sets up an in-memory exporter for testing and returns a cleanup function.
func setupTestTracing(t *testing.T) (exporter *tracetest.InMemoryExporter, cleanup func()) {
	t.Helper()

	// Save original global state
	originalTP := otel.GetTracerProvider()
	originalPropagator := otel.GetTextMapPropagator()

	exporter = tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	pipeline.ResetTracerForTesting() // the delivery pipeline caches its tracer; bind it to tp

	cleanup = func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Logf("Failed to shutdown test tracer provider: %v", err)
		}
		otel.SetTracerProvider(originalTP)
		otel.SetTextMapPropagator(originalPropagator)
		pipeline.ResetTracerForTesting()
	}

	return exporter, cleanup
}

func TestPublishCreatesSpan(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	client, fakeConn, fakeCh := setupReadyClient(t)
	defer func() { _ = client.Close() }()

	// setupReadyClient leaves maxPublishAttempts at 0 (unbounded) and its 100ms
	// confirmation timeout can be outrun by a loaded runner: the publish then
	// abandons the wait and retries under a NEW tag, so a hardcoded tag-1
	// confirmation matches nothing and a deadline-free publish waits forever.
	// Ack whichever tag the publish actually used, under a deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	destination := testQueueOtel
	data := []byte(testMessage)

	ackNextSuccessfulPublish(ctx, t, client, fakeCh)

	err := client.publishBytes(ctx, publishOptions{RoutingKey: destination}, data)
	require.NoError(t, err)

	// Verify span was created
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, testQueueOtel+" publish", span.Name)
	assert.Equal(t, trace.SpanKindProducer, span.SpanKind)
	assert.Equal(t, codes.Ok, span.Status.Code)

	// Verify required attributes
	attrs := span.Attributes
	assertAttribute(t, attrs, string(semconv.MessagingSystemKey), "rabbitmq")
	assertAttribute(t, attrs, string(semconv.MessagingOperationNameKey), "publish")
	assertAttribute(t, attrs, string(semconv.MessagingDestinationNameKey), testQueueOtel)
	assertAttribute(t, attrs, string(semconv.MessagingMessageBodySizeKey), int64(len(data)))

	// Cleanup channels
	close(fakeConn.notifyCloseCh)
	close(fakeCh.notifyCloseCh)
}

func TestPublishBytesCreatesSpanWithExchangeAttributes(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	client, fakeConn, fakeCh := setupReadyClient(t)
	defer func() { _ = client.Close() }()

	// Same hardcoded-tag hazard as TestPublishCreatesSpan — see the note there.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	options := publishOptions{
		Exchange:   testExchange,
		RoutingKey: testRoutingKey,
	}
	data := []byte("exchange message")

	ackNextSuccessfulPublish(ctx, t, client, fakeCh)

	err := client.publishBytes(ctx, options, data)
	require.NoError(t, err)

	// Verify span attributes
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	// Span name should use exchange since it's the primary AMQP entity
	assert.Equal(t, testExchange+" publish", span.Name)

	attrs := span.Attributes
	assertAttribute(t, attrs, "messaging.rabbitmq.exchange", testExchange)
	assertAttribute(t, attrs, "messaging.rabbitmq.destination.routing_key", testRoutingKey)

	// Cleanup channels
	close(fakeConn.notifyCloseCh)
	close(fakeCh.notifyCloseCh)
}

func TestPublishErrorRecordsSpanError(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	client, fakeConn, fakeCh := setupReadyClient(t)
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	destination := testQueueOtel
	data := []byte(testMessage)

	// Cancel context immediately to trigger error
	cancel()

	err := client.publishBytes(ctx, publishOptions{RoutingKey: destination}, data)
	require.Error(t, err)
	assert.Equal(t, context.Canceled, err)

	// Verify span recorded the error
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, codes.Error, span.Status.Code)
	// ADR-083: the span sinks carry the error's Go type, never its message.
	assert.Equal(t, "*errors.errorString", span.Status.Description)

	// Verify error event recorded
	require.Len(t, span.Events, 1)
	event := span.Events[0]
	assert.Equal(t, "exception", event.Name)

	// Cleanup channels
	close(fakeConn.notifyCloseCh)
	close(fakeCh.notifyCloseCh)
}

// TestRecordPublishFailureKeepsTheErrorMessageOffTheSpan pins ADR-083 on the
// AMQP publish path: whatever the broker or a wrapped cause put in the message,
// the span reports the error's Go type only.
func TestRecordPublishFailureKeepsTheErrorMessageOffTheSpan(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	ctx, span := otel.Tracer("publish-failure-test").Start(context.Background(), "publish")
	client := &AMQPClientImpl{}
	publishErr := errors.New("NACK from broker: " + obtest.LeakCanary)

	got := client.recordPublishFailure(ctx, publishOptions{Exchange: "events", RoutingKey: "orders"}, time.Now(), span, publishErr)
	span.End()

	require.ErrorIs(t, got, publishErr)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	recorded := spans[0]

	assert.Equal(t, codes.Error, recorded.Status.Code)
	assert.Equal(t, "*errors.errorString", recorded.Status.Description)
	obtest.AssertExceptionTypeOnly(t, &recorded, "*errors.errorString")
	obtest.AssertNoSpanMarkers(t, &recorded, obtest.LeakCanary)
}

// TestPublishRetryEventCarriesNoErrorMessage covers the publish.retry span
// event, which is an off-platform sink like the terminal status: a broker error's
// Reason is server-authored, so the event names the error's Go TYPE (ADR-083).
// The terminal-status test above drives recordPublishFailure and never reaches
// this event, so without this the retry attribute would be unasserted.
func TestPublishRetryEventCarriesNoErrorMessage(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	ch := &fakeChannel{publishErr: errors.New("NOT_FOUND - no exchange: " + obtest.LeakCanary)}
	c := newClientWithFakeChannel(t, ch)
	c.resendDelay = time.Millisecond
	c.maxPublishAttempts = 2

	err := c.publishBytes(context.Background(), publishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
	require.ErrorIs(t, err, ErrPublishRetriesExhausted)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0]

	var retries int
	for i := range span.Events {
		if span.Events[i].Name != eventPublishRetry {
			continue
		}
		retries++
		assertAttributeValue(t, span.Events[i].Attributes, "error.type", "*errors.errorString")
		assertNoAttributeKey(t, span.Events[i].Attributes, "error")
	}
	require.Positive(t, retries, "the publish must have retried, or this test asserts nothing")

	obtest.AssertNoSpanMarkers(t, &span, obtest.LeakCanary)
}

// assertAttributeValue requires key to be present with want.
func assertAttributeValue(t *testing.T, attrs []attribute.KeyValue, key, want string) {
	t.Helper()
	for _, kv := range attrs {
		if string(kv.Key) == key {
			assert.Equal(t, want, kv.Value.AsString(), "attribute %s", key)
			return
		}
	}
	t.Errorf("attribute %s not found", key)
}

// assertNoAttributeKey requires key to be absent.
func assertNoAttributeKey(t *testing.T, attrs []attribute.KeyValue, key string) {
	t.Helper()
	for _, kv := range attrs {
		assert.NotEqual(t, key, string(kv.Key), "attribute %s should be absent", key)
	}
}

func TestPublishNotReadyRecordsError(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	client, fakeConn, fakeCh := setupReadyClient(t)

	// Mark client as not ready
	client.m.Lock()
	client.isReady = false
	client.m.Unlock()

	ctx := context.Background()
	destination := testQueueOtel
	data := []byte(testMessage)

	err := client.publishBytes(ctx, publishOptions{RoutingKey: destination}, data)
	// W3-D breaking change: pre-fix returned nil to avoid failing the business
	// operation; post-fix returns errNotConnected so callers can retry/escalate.
	require.ErrorIs(t, err, errNotConnected)

	// Verify span recorded the error status
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, codes.Error, span.Status.Code)
	assert.Equal(t, "*errors.errorString", span.Status.Description)

	// Cleanup channels
	close(fakeConn.notifyCloseCh)
	close(fakeCh.notifyCloseCh)
}

func TestPublishConfirmationTimeoutRecordsError(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	client, fakeConn, fakeCh := setupReadyClient(t)
	defer func() { _ = client.Close() }()

	// Set very short timeout for testing
	client.connectionTimeout = 10 * time.Millisecond

	// Use context with timeout to eventually exit the retry loop
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	destination := testQueueOtel
	data := []byte(testMessage)

	// Don't send confirmation - let it timeout and then context expires
	err := client.publishBytes(ctx, publishOptions{RoutingKey: destination}, data)
	// Should return context deadline exceeded. The error now wraps the last retry
	// cause (ErrPublishConfirmTimeout), so match by errors.Is rather than identity.
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// Verify span was created and recorded error
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, codes.Error, span.Status.Code)
	assert.Equal(t, "*fmt.wrapErrors", span.Status.Description)

	// Verify retry events were recorded
	require.NotEmpty(t, span.Events, "Expected retry events to be recorded")
	hasRetryEvent := false
	for _, event := range span.Events {
		if event.Name != "amqp.publish.retry" {
			continue
		}
		hasRetryEvent = true
		// Verify retry attributes
		attrs := event.Attributes
		foundReason := false
		foundRetryCount := false
		for _, attr := range attrs {
			if attr.Key == "reason" && attr.Value.AsString() == "confirmation timeout" {
				foundReason = true
			}
			if attr.Key == "retry_count" {
				foundRetryCount = true
				assert.Greater(t, int(attr.Value.AsInt64()), 0, "retry_count should be greater than 0")
			}
		}
		assert.True(t, foundReason, "Expected 'reason' attribute with value 'confirmation timeout'")
		assert.True(t, foundRetryCount, "Expected 'retry_count' attribute")
	}
	assert.True(t, hasRetryEvent, "Expected at least one 'amqp.publish.retry' event to be recorded")

	// Cleanup channels
	close(fakeConn.notifyCloseCh)
	close(fakeCh.notifyCloseCh)
}

// Helper functions

func setupReadyClient(t *testing.T) (*AMQPClientImpl, *fakeConnAdapter, *fakeChannel) {
	t.Helper()

	fakeConn := &fakeConnAdapter{
		notifyCloseCh: make(chan *amqp.Error, 1),
	}
	fakeCh := &fakeChannel{
		notifyCloseCh:   make(chan *amqp.Error, 1),
		notifyConfirmCh: make(chan amqp.Confirmation, 1),
	}

	// Override dial function to return fake connection
	originalDial := getAmqpDialFunc()
	setAmqpDialFunc(func(_ string) (amqpConnection, error) {
		return fakeConn, nil
	})
	t.Cleanup(func() {
		setAmqpDialFunc(originalDial)
	})

	client := &AMQPClientImpl{
		m:                 &sync.RWMutex{},
		log:               &testLogger{t: t},
		done:              make(chan bool),
		reconnectDelay:    1 * time.Millisecond,
		reInitDelay:       1 * time.Millisecond,
		resendDelay:       1 * time.Millisecond,
		connectionTimeout: 100 * time.Millisecond,
	}

	// Manually set up connection
	client.connection = fakeConn
	client.notifyConnClose = fakeConn.notifyCloseCh

	// Use changeChannel for channel/confirm wiring so the W3-D dispatcher
	// goroutine routes confirmations to per-publish channels. After this call,
	// fakeCh.notifyConfirmCh == client.notifyConfirm (the fake's NotifyPublish
	// captures the channel argument), so tests that send to fakeCh.notifyConfirmCh
	// still feed the dispatcher correctly.
	client.changeChannel(fakeCh)
	client.isReady = true

	return client, fakeConn, fakeCh
}

func assertAttribute(t *testing.T, attrs []attribute.KeyValue, key string, expectedValue any) {
	t.Helper()

	for _, attr := range attrs {
		if string(attr.Key) == key {
			switch v := expectedValue.(type) {
			case string:
				assert.Equal(t, v, attr.Value.AsString())
			case int64:
				assert.Equal(t, v, attr.Value.AsInt64())
			case int:
				assert.Equal(t, int64(v), attr.Value.AsInt64())
			default:
				t.Fatalf("Unsupported attribute type: %T", expectedValue)
			}
			return
		}
	}

	t.Errorf("Attribute %s not found in span attributes", key)
}

// testLogger is a minimal logger implementation for testing
type testLogger struct {
	t *testing.T
}

func (l *testLogger) Info() logger.LogEvent  { return &testLogEvent{l.t, "INFO"} }
func (l *testLogger) Error() logger.LogEvent { return &testLogEvent{l.t, "ERROR"} }
func (l *testLogger) Debug() logger.LogEvent { return &testLogEvent{l.t, "DEBUG"} }
func (l *testLogger) Warn() logger.LogEvent  { return &testLogEvent{l.t, "WARN"} }
func (l *testLogger) Fatal() logger.LogEvent { return &testLogEvent{l.t, "FATAL"} }
func (l *testLogger) WithContext(_ any) logger.Logger {
	return l
}

func (l *testLogger) WithFields(_ map[string]any) logger.Logger {
	return l
}

type testLogEvent struct {
	t     *testing.T
	level string
}

func (e *testLogEvent) Str(_, _ string) logger.LogEvent               { return e }
func (e *testLogEvent) Err(_ error) logger.LogEvent                   { return e }
func (e *testLogEvent) Uint64(_ string, _ uint64) logger.LogEvent     { return e }
func (e *testLogEvent) Int(_ string, _ int) logger.LogEvent           { return e }
func (e *testLogEvent) Int64(_ string, _ int64) logger.LogEvent       { return e }
func (e *testLogEvent) Dur(_ string, _ time.Duration) logger.LogEvent { return e }
func (e *testLogEvent) Interface(_ string, _ any) logger.LogEvent     { return e }
func (e *testLogEvent) Bytes(_ string, _ []byte) logger.LogEvent      { return e }
func (e *testLogEvent) Bool(_ string, _ bool) logger.LogEvent         { return e }
func (e *testLogEvent) Enabled() bool                                 { return true }
func (e *testLogEvent) Msg(_ string)                                  {} // No-op for test logger
func (e *testLogEvent) Msgf(_ string, _ ...any)                       {} // No-op for test logger
