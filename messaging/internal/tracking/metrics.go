package tracking

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	// Meter name for AMQP messaging metrics instrumentation
	amqpMeterName = "go-bricks/messaging"

	// Standard OTel messaging metric names (semconv v1.37.0)
	metricOperationDuration = "messaging.client.operation.duration"
	metricMessagesSent      = "messaging.client.sent.messages"
	metricMessagesConsumed  = "messaging.client.consumed.messages"

	// Framework-specific operational metrics
	metricPublishRetries   = "messaging.client.publish.retries"
	metricConnectionCreate = "messaging.connection.create"
	metricConnectionClose  = "messaging.connection.close"
	metricChannelCreate    = "messaging.channel.create"
	metricChannelClose     = "messaging.channel.close"

	// Attribute keys (following OTel semconv)
	attrMessagingSystem             = "messaging.system"
	attrMessagingOperation          = "messaging.operation.name"
	attrMessagingDestination        = "messaging.destination.name"
	attrErrorType                   = "error.type"
	attrMessagingRabbitMQExchange   = "messaging.rabbitmq.exchange"
	attrMessagingRabbitMQRoutingKey = "messaging.rabbitmq.routing_key"
	attrMessagingRabbitMQQueue      = "messaging.rabbitmq.queue"
	attrRetryReason                 = "retry.reason"

	// Operation types
	operationPublish = "publish"
	operationReceive = "receive"

	// Messaging system identifier
	messagingSystemRabbitMQ = "rabbitmq"
)

var (
	// Singleton meter initialization
	amqpMeter   metric.Meter
	meterOnce   sync.Once
	meterInitMu sync.Mutex

	// Metric instruments (standard OTel metrics)
	amqpOperationDuration metric.Float64Histogram
	amqpMessagesSent      metric.Int64Counter
	amqpMessagesConsumed  metric.Int64Counter

	// Additional operational metrics
	amqpPublishRetries   metric.Int64Counter
	amqpConnectionCreate metric.Int64Counter
	amqpConnectionClose  metric.Int64Counter
	amqpChannelCreate    metric.Int64Counter
	amqpChannelClose     metric.Int64Counter
)

// logMetricError logs a metric initialization or registration error to stderr.
// This is a best-effort operation - metrics failures should not break the application.
func logMetricError(metricName string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: Failed to initialize metric %s: %v\n", metricName, err)
	}
}

// initAMQPMeter initializes the OpenTelemetry meter and metric instruments.
// This function is called lazily and only once using sync.Once to ensure
// thread-safe initialization.
func initAMQPMeter() {
	meterInitMu.Lock()
	defer meterInitMu.Unlock()

	// Prevent re-initialization if already set
	if amqpMeter != nil {
		return
	}

	// Get meter from global meter provider
	amqpMeter = otel.Meter(amqpMeterName)

	var err error

	// Initialize operation duration histogram with explicit bucket boundaries per OTel semconv
	// Buckets: [0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10]
	amqpOperationDuration, err = amqpMeter.Float64Histogram(
		metricOperationDuration,
		metric.WithDescription("Duration of messaging operation initiated by a producer or consumer client"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10),
	)
	logMetricError(metricOperationDuration, err)

	// Initialize sent messages counter
	amqpMessagesSent, err = amqpMeter.Int64Counter(
		metricMessagesSent,
		metric.WithDescription("Number of messages producer attempted to send to the broker"),
		metric.WithUnit("{message}"),
	)
	logMetricError(metricMessagesSent, err)

	// Initialize consumed messages counter
	amqpMessagesConsumed, err = amqpMeter.Int64Counter(
		metricMessagesConsumed,
		metric.WithDescription("Number of messages that were delivered to the application"),
		metric.WithUnit("{message}"),
	)
	logMetricError(metricMessagesConsumed, err)

	// Initialize publish retries counter
	amqpPublishRetries, err = amqpMeter.Int64Counter(
		metricPublishRetries,
		metric.WithDescription("Number of message publish retry attempts due to NACK or timeout"),
		metric.WithUnit("{retry}"),
	)
	logMetricError(metricPublishRetries, err)

	// Initialize connection create counter
	amqpConnectionCreate, err = amqpMeter.Int64Counter(
		metricConnectionCreate,
		metric.WithDescription("Number of AMQP connection creation events"),
		metric.WithUnit("{connection}"),
	)
	logMetricError(metricConnectionCreate, err)

	// Initialize connection close counter
	amqpConnectionClose, err = amqpMeter.Int64Counter(
		metricConnectionClose,
		metric.WithDescription("Number of AMQP connection close events"),
		metric.WithUnit("{connection}"),
	)
	logMetricError(metricConnectionClose, err)

	// Initialize channel create counter
	amqpChannelCreate, err = amqpMeter.Int64Counter(
		metricChannelCreate,
		metric.WithDescription("Number of AMQP channel creation events"),
		metric.WithUnit("{channel}"),
	)
	logMetricError(metricChannelCreate, err)

	// Initialize channel close counter
	amqpChannelClose, err = amqpMeter.Int64Counter(
		metricChannelClose,
		metric.WithDescription("Number of AMQP channel close events"),
		metric.WithUnit("{channel}"),
	)
	logMetricError(metricChannelClose, err)
}

// getAMQPMeter returns the initialized AMQP meter, initializing it if necessary.
func getAMQPMeter() metric.Meter {
	meterOnce.Do(initAMQPMeter)
	return amqpMeter
}

// RecordAMQPPublishMetrics records OpenTelemetry metrics for an AMQP publish operation.
// This function is called after a publish attempt to emit metrics about the operation.
//
// Parameters:
//   - ctx: Context for metrics recording
//   - exchange: The AMQP exchange name (empty string for default exchange)
//   - routingKey: The routing key used for message delivery
//   - duration: Time taken for the publish operation
//   - err: Error if the operation failed, nil if successful
//
// Metrics recorded:
// - messaging.client.operation.duration: Histogram of operation durations in seconds
// - messaging.client.sent.messages: Counter of messages sent (incremented on success)
//
// The function is non-blocking and handles errors gracefully - metric recording failures
// will not impact messaging operation execution.
func RecordAMQPPublishMetrics(ctx context.Context, exchange, routingKey string, duration time.Duration, err error) {
	// Ensure meter is initialized
	meter := getAMQPMeter()
	if meter == nil {
		return
	}

	// Format destination name per OTel RabbitMQ convention (producer format)
	destination := formatDestinationName(exchange, routingKey, "")
	errorType := extractErrorType(err)

	// Common attributes for metrics
	commonAttrs := []attribute.KeyValue{
		attribute.String(attrMessagingSystem, messagingSystemRabbitMQ),
		attribute.String(attrMessagingOperation, operationPublish),
		attribute.String(attrMessagingDestination, destination),
	}

	// Add granular attributes for filtering
	if exchange != "" {
		commonAttrs = append(commonAttrs, attribute.String(attrMessagingRabbitMQExchange, exchange))
	}
	if routingKey != "" {
		commonAttrs = append(commonAttrs, attribute.String(attrMessagingRabbitMQRoutingKey, routingKey))
	}

	// Add error type if present
	if errorType != "" {
		commonAttrs = append(commonAttrs, attribute.String(attrErrorType, errorType))
	}

	// Record duration histogram (in seconds)
	if amqpOperationDuration != nil {
		durationSeconds := durationToSeconds(duration)
		amqpOperationDuration.Record(ctx, durationSeconds, metric.WithAttributes(commonAttrs...))
	}

	// Record sent messages counter (only on success)
	if amqpMessagesSent != nil && err == nil {
		amqpMessagesSent.Add(ctx, 1, metric.WithAttributes(commonAttrs...))
	}
}

// RecordAMQPConsumeMetrics records OpenTelemetry metrics for an AMQP consume operation.
// This function is called automatically when a message is consumed to emit metrics.
//
// Metrics recorded:
// - messaging.client.operation.duration: Histogram of operation durations in seconds
// - messaging.client.consumed.messages: Counter of messages consumed
//
// The function is non-blocking and handles errors gracefully.
func RecordAMQPConsumeMetrics(ctx context.Context, delivery *amqp.Delivery, queueName string, duration time.Duration, err error) {
	// Ensure meter is initialized
	meter := getAMQPMeter()
	if meter == nil {
		return
	}

	// Extract delivery information
	var exchange, routingKey string
	if delivery != nil {
		exchange = delivery.Exchange
		routingKey = delivery.RoutingKey
	}

	// Format destination name per OTel RabbitMQ convention (consumer format)
	destination := formatDestinationName(exchange, routingKey, queueName)
	errorType := extractErrorType(err)

	// Common attributes for metrics
	commonAttrs := []attribute.KeyValue{
		attribute.String(attrMessagingSystem, messagingSystemRabbitMQ),
		attribute.String(attrMessagingOperation, operationReceive),
		attribute.String(attrMessagingDestination, destination),
	}

	// Add granular attributes for filtering
	if exchange != "" {
		commonAttrs = append(commonAttrs, attribute.String(attrMessagingRabbitMQExchange, exchange))
	}
	if routingKey != "" {
		commonAttrs = append(commonAttrs, attribute.String(attrMessagingRabbitMQRoutingKey, routingKey))
	}
	if queueName != "" {
		commonAttrs = append(commonAttrs, attribute.String(attrMessagingRabbitMQQueue, queueName))
	}

	// Add error type if present
	if errorType != "" {
		commonAttrs = append(commonAttrs, attribute.String(attrErrorType, errorType))
	}

	// Record duration histogram (in seconds) - only if duration is > 0
	if amqpOperationDuration != nil && duration > 0 {
		durationSeconds := durationToSeconds(duration)
		amqpOperationDuration.Record(ctx, durationSeconds, metric.WithAttributes(commonAttrs...))
	}

	// Record consumed messages counter (only on success)
	if amqpMessagesConsumed != nil && err == nil {
		amqpMessagesConsumed.Add(ctx, 1, metric.WithAttributes(commonAttrs...))
	}
}

// RecordPublishRetry records a publish retry attempt in the retry counter.
// This is called each time a publish operation is retried due to NACK, timeout, or error.
//
// Parameters:
//   - ctx: Context for metrics recording
//   - exchange: The AMQP exchange name (empty string for default exchange)
//   - routingKey: The routing key used for message delivery
//   - reason: The reason for the retry (e.g., "nack", "timeout", "publish_error")
func RecordPublishRetry(ctx context.Context, exchange, routingKey, reason string) {
	// Ensure meter is initialized
	meter := getAMQPMeter()
	if meter == nil {
		return
	}

	// Format destination name per OTel RabbitMQ convention (producer format)
	destination := formatDestinationName(exchange, routingKey, "")

	// Attributes for retry metric
	attrs := []attribute.KeyValue{
		attribute.String(attrMessagingSystem, messagingSystemRabbitMQ),
		attribute.String(attrMessagingDestination, destination),
		attribute.String(attrRetryReason, reason),
	}

	// Add granular attributes for filtering
	if exchange != "" {
		attrs = append(attrs, attribute.String(attrMessagingRabbitMQExchange, exchange))
	}
	if routingKey != "" {
		attrs = append(attrs, attribute.String(attrMessagingRabbitMQRoutingKey, routingKey))
	}

	// Record retry counter
	if amqpPublishRetries != nil {
		amqpPublishRetries.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// recordLifecycleEvent is a helper function to record connection or channel lifecycle events.
// It reduces code duplication between RecordConnectionEvent and RecordChannelEvent.
func recordLifecycleEvent(eventType string, err error, createCounter, closeCounter metric.Int64Counter) {
	// Ensure meter is initialized
	meter := getAMQPMeter()
	if meter == nil {
		return
	}

	errorType := extractErrorType(err)

	// Attributes for lifecycle event
	attrs := []attribute.KeyValue{
		attribute.String(attrMessagingSystem, messagingSystemRabbitMQ),
	}

	// Add error type if present (for close events)
	if errorType != "" {
		attrs = append(attrs, attribute.String(attrErrorType, errorType))
	}

	// Record appropriate counter
	ctx := context.Background()
	if eventType == "create" && createCounter != nil {
		createCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	} else if eventType == "close" && closeCounter != nil {
		closeCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
}

// RecordConnectionEvent records AMQP connection lifecycle events.
// eventType should be "create" or "close".
func RecordConnectionEvent(eventType string, err error) {
	recordLifecycleEvent(eventType, err, amqpConnectionCreate, amqpConnectionClose)
}

// RecordChannelEvent records AMQP channel lifecycle events.
// eventType should be "create" or "close".
func RecordChannelEvent(eventType string, err error) {
	recordLifecycleEvent(eventType, err, amqpChannelCreate, amqpChannelClose)
}
