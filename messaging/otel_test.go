package messaging

import (
	"context"
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

	cleanup = func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Logf("Failed to shutdown test tracer provider: %v", err)
		}
		otel.SetTracerProvider(originalTP)
		otel.SetTextMapPropagator(originalPropagator)
	}

	return exporter, cleanup
}

func TestPublishCreatesSpan(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	client, fakeConn, fakeCh := setupReadyClient(t)
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	destination := testQueueOtel
	data := []byte(testMessage)

	// Trigger publish success
	go func() {
		time.Sleep(10 * time.Millisecond)
		fakeCh.notifyConfirmCh <- amqp.Confirmation{DeliveryTag: 1, Ack: true}
	}()

	err := client.Publish(ctx, destination, data)
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

func TestPublishToExchangeCreatesSpanWithExchangeAttributes(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	client, fakeConn, fakeCh := setupReadyClient(t)
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	options := PublishOptions{
		Exchange:   testExchange,
		RoutingKey: testRoutingKey,
	}
	data := []byte("exchange message")

	// Trigger publish success
	go func() {
		time.Sleep(10 * time.Millisecond)
		fakeCh.notifyConfirmCh <- amqp.Confirmation{DeliveryTag: 1, Ack: true}
	}()

	err := client.PublishToExchange(ctx, options, data)
	require.NoError(t, err)

	// Verify span attributes
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, testRoutingKey+" publish", span.Name)

	attrs := span.Attributes
	assertAttribute(t, attrs, "messaging.rabbitmq.exchange", testExchange)
	assertAttribute(t, attrs, "messaging.rabbitmq.routing_key", testRoutingKey)

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

	err := client.Publish(ctx, destination, data)
	require.Error(t, err)
	assert.Equal(t, context.Canceled, err)

	// Verify span recorded the error
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, codes.Error, span.Status.Code)
	assert.Contains(t, span.Status.Description, "context canceled")

	// Verify error event recorded
	require.Len(t, span.Events, 1)
	event := span.Events[0]
	assert.Equal(t, "exception", event.Name)

	// Cleanup channels
	close(fakeConn.notifyCloseCh)
	close(fakeCh.notifyCloseCh)
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

	err := client.Publish(ctx, destination, data)
	require.NoError(t, err) // Returns nil to avoid failing business operation

	// Verify span recorded the error status
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, codes.Error, span.Status.Code)
	assert.Contains(t, span.Status.Description, "not ready")

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
	err := client.Publish(ctx, destination, data)
	// Should return context deadline exceeded
	require.Error(t, err)
	assert.Equal(t, context.DeadlineExceeded, err)

	// Verify span was created and recorded error
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, codes.Error, span.Status.Code)
	assert.Contains(t, span.Status.Description, "deadline exceeded")

	// Cleanup channels
	close(fakeConn.notifyCloseCh)
	close(fakeCh.notifyCloseCh)
}

func TestStartConsumeSpanCreatesSpanWithAttributes(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	ctx := context.Background()
	queueName := testQueueOtel
	delivery := &amqp.Delivery{
		Exchange:      testExchange,
		RoutingKey:    testRoutingKey,
		MessageId:     "msg-123",
		CorrelationId: "corr-456",
		Body:          []byte("message body"),
		Headers:       amqp.Table{},
	}

	spanCtx, span := StartConsumeSpan(ctx, delivery, queueName)
	require.NotNil(t, spanCtx)
	require.NotNil(t, span)
	span.End()

	// Verify span was created
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	consumeSpan := spans[0]
	assert.Equal(t, testQueueOtel+" receive", consumeSpan.Name)
	assert.Equal(t, trace.SpanKindConsumer, consumeSpan.SpanKind)

	// Verify attributes
	attrs := consumeSpan.Attributes
	assertAttribute(t, attrs, string(semconv.MessagingSystemKey), "rabbitmq")
	assertAttribute(t, attrs, string(semconv.MessagingOperationNameKey), "receive")
	assertAttribute(t, attrs, string(semconv.MessagingDestinationNameKey), testQueueOtel)
	assertAttribute(t, attrs, string(semconv.MessagingMessageBodySizeKey), int64(len(delivery.Body)))
	assertAttribute(t, attrs, "messaging.rabbitmq.exchange", testExchange)
	assertAttribute(t, attrs, "messaging.rabbitmq.routing_key", testRoutingKey)
	assertAttribute(t, attrs, string(semconv.MessagingMessageIDKey), "msg-123")
	assertAttribute(t, attrs, string(semconv.MessagingMessageConversationIDKey), "corr-456")
}

func TestStartConsumeSpanExtractsTraceContext(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	ctx := context.Background()

	// Create delivery with traceparent header (simulating message from publisher)
	headers := amqp.Table{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}
	delivery := &amqp.Delivery{
		Body:    []byte("message"),
		Headers: headers,
	}

	// Start consume span - should extract trace context from headers
	consumeCtx, consumeSpan := StartConsumeSpan(ctx, delivery, testQueueOtel)
	require.NotNil(t, consumeCtx)
	consumeSpan.End()

	// Verify span was created
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	span := spans[0]
	assert.Equal(t, testQueueOtel+" receive", span.Name)

	// Verify span has the trace ID from the traceparent header
	assert.NotEqual(t, trace.TraceID{}, span.SpanContext.TraceID())
}

func TestConsumeSpanWithMinimalDelivery(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	ctx := context.Background()
	delivery := &amqp.Delivery{
		Body:    []byte("minimal"),
		Headers: amqp.Table{},
	}

	spanCtx, span := StartConsumeSpan(ctx, delivery, "minimal-queue")
	require.NotNil(t, spanCtx)
	span.End()

	// Verify span created with minimal attributes
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)

	consumeSpan := spans[0]
	assert.Equal(t, "minimal-queue receive", consumeSpan.Name)

	// Verify only required attributes are present
	attrs := consumeSpan.Attributes
	assertAttribute(t, attrs, string(semconv.MessagingSystemKey), "rabbitmq")
	assertAttribute(t, attrs, string(semconv.MessagingDestinationNameKey), "minimal-queue")
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
	originalDial := amqpDialFunc
	amqpDialFunc = func(_ string) (amqpConnection, error) {
		return fakeConn, nil
	}
	t.Cleanup(func() {
		amqpDialFunc = originalDial
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

	// Manually set up connection and channel
	client.connection = fakeConn
	client.channel = fakeCh
	client.notifyConnClose = fakeConn.notifyCloseCh
	client.notifyChanClose = fakeCh.notifyCloseCh
	client.notifyConfirm = fakeCh.notifyConfirmCh
	client.isReady = true

	return client, fakeConn, fakeCh
}

func assertAttribute(t *testing.T, attrs []attribute.KeyValue, key string, expectedValue interface{}) {
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
func (l *testLogger) WithContext(_ interface{}) logger.Logger {
	return l
}
func (l *testLogger) WithFields(_ map[string]interface{}) logger.Logger {
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
func (e *testLogEvent) Msg(_ string)                                  {} // No-op for test logger
func (e *testLogEvent) Msgf(_ string, _ ...any)                       {} // No-op for test logger
