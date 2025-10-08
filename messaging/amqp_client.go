package messaging

import (
	"context"
	"errors"
	"maps"
	"strconv"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/logger"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
	"github.com/google/uuid"
)

// Interfaces and dialer are defined in amqp_adapters.go

// AMQPClientImpl provides an AMQP implementation of the messaging client interface.
// It includes automatic reconnection, retry logic, and AMQP-specific features.
type AMQPClientImpl struct {
	m               *sync.RWMutex
	brokerURL       string
	log             logger.Logger
	connection      amqpConnection
	channel         amqpChannel
	done            chan bool
	notifyConnClose chan *amqp.Error
	notifyChanClose chan *amqp.Error
	notifyConfirm   chan amqp.Confirmation
	isReady         bool

	// Configuration
	reconnectDelay    time.Duration
	reInitDelay       time.Duration
	resendDelay       time.Duration
	connectionTimeout time.Duration
}

// Reconnection delays
const (
	defaultReconnectDelay    = 5 * time.Second
	defaultReInitDelay       = 2 * time.Second
	defaultResendDelay       = 5 * time.Second
	defaultConnectionTimeout = 30 * time.Second
)

// OpenTelemetry constants
const (
	messagingTracerName     = "go-bricks/messaging"
	messagingSystemRabbitMQ = "rabbitmq"
	operationPublish        = "publish"
	operationReceive        = "receive"
)

var (
	errNotConnected  = errors.New("not connected to AMQP broker")
	errAlreadyClosed = errors.New("AMQP client already closed")
	errShutdown      = errors.New("AMQP client is shutting down")
)

// NewAMQPClient creates a new AMQP client instance.
// It automatically attempts to connect to the broker and handles reconnections.
func NewAMQPClient(brokerURL string, log logger.Logger) *AMQPClientImpl {
	client := &AMQPClientImpl{
		m:                 &sync.RWMutex{},
		brokerURL:         brokerURL,
		log:               log,
		done:              make(chan bool),
		reconnectDelay:    defaultReconnectDelay,
		reInitDelay:       defaultReInitDelay,
		resendDelay:       defaultResendDelay,
		connectionTimeout: defaultConnectionTimeout,
	}

	// Start connection management in background
	go client.handleReconnect()
	return client
}

// IsReady returns true if the client is connected and ready to send/receive messages.
func (c *AMQPClientImpl) IsReady() bool {
	c.m.RLock()
	defer c.m.RUnlock()
	return c.isReady
}

// Publish sends a message to the specified destination (queue name).
// Uses default exchange ("") and destination as routing key.
func (c *AMQPClientImpl) Publish(ctx context.Context, destination string, data []byte) error {
	return c.PublishToExchange(ctx, PublishOptions{
		Exchange:   "",
		RoutingKey: destination,
	}, data)
}

// createPublishSpan creates and configures an OpenTelemetry span for AMQP publish operations.
func createPublishSpan(ctx context.Context, options PublishOptions, dataLen int, startTime time.Time) (context.Context, trace.Span) {
	tracer := otel.Tracer(messagingTracerName)
	// Prefer Exchange over RoutingKey for destination since exchange is the primary AMQP entity
	destination := options.Exchange
	if destination == "" {
		destination = options.RoutingKey
	}
	spanName := destination + " " + operationPublish

	ctx, span := tracer.Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithTimestamp(startTime),
	)

	// Set messaging semantic attributes
	attrs := []attribute.KeyValue{
		attribute.String(string(semconv.MessagingSystemKey), messagingSystemRabbitMQ),
		semconv.MessagingOperationName(operationPublish),
		semconv.MessagingDestinationName(destination),
		semconv.MessagingMessageBodySize(dataLen),
	}
	if options.Exchange != "" {
		attrs = append(attrs, attribute.String("messaging.rabbitmq.exchange", options.Exchange))
	}
	if options.RoutingKey != "" {
		attrs = append(attrs, attribute.String("messaging.rabbitmq.routing_key", options.RoutingKey))
	}
	span.SetAttributes(attrs...)

	return ctx, span
}

// PublishToExchange publishes a message to a specific exchange with routing key.
func (c *AMQPClientImpl) PublishToExchange(ctx context.Context, options PublishOptions, data []byte) error {
	startTime := time.Now()

	// Create OpenTelemetry span for publish operation
	ctx, span := createPublishSpan(ctx, options, len(data), startTime)
	defer span.End()

	c.m.RLock()
	if !c.isReady {
		c.m.RUnlock()
		c.log.Warn().
			Str("exchange", options.Exchange).
			Str("routing_key", options.RoutingKey).
			Msg("AMQP client not ready, message not published")
		span.SetStatus(codes.Error, "AMQP client not ready")
		return nil // Return nil to avoid failing the business operation
	}
	c.m.RUnlock()

	for {
		select {
		case <-ctx.Done():
			span.RecordError(ctx.Err())
			span.SetStatus(codes.Error, ctx.Err().Error())
			return ctx.Err()
		case <-c.done:
			span.RecordError(errShutdown)
			span.SetStatus(codes.Error, errShutdown.Error())
			return errShutdown
		default:
		}

		err := c.unsafePublish(ctx, options, data)
		if err != nil {
			c.log.Warn().Err(err).Msg("Publish failed, retrying...")
			select {
			case <-ctx.Done():
				span.RecordError(ctx.Err())
				span.SetStatus(codes.Error, ctx.Err().Error())
				return ctx.Err()
			case <-c.done:
				span.RecordError(errShutdown)
				span.SetStatus(codes.Error, errShutdown.Error())
				return errShutdown
			case <-time.After(c.resendDelay):
			}
			continue
		}

		// Wait for confirmation
		select {
		case <-ctx.Done():
			span.RecordError(ctx.Err())
			span.SetStatus(codes.Error, ctx.Err().Error())
			return ctx.Err()
		case <-c.done:
			span.RecordError(errShutdown)
			span.SetStatus(codes.Error, errShutdown.Error())
			return errShutdown
		case confirm := <-c.notifyConfirm:
			if confirm.Ack {
				// Track elapsed time and increment AMQP counter in context for request tracking
				elapsed := time.Since(startTime)
				logger.IncrementAMQPCounter(ctx)
				logger.AddAMQPElapsed(ctx, elapsed.Nanoseconds())
				c.log.Debug().
					Str("exchange", options.Exchange).
					Str("routing_key", options.RoutingKey).
					Uint64("delivery_tag", confirm.DeliveryTag).
					Msg("Message published successfully")
				span.SetStatus(codes.Ok, "")
				return nil
			}
			// NACK received - retry the publish
			c.log.Warn().
				Uint64("delivery_tag", confirm.DeliveryTag).
				Msg("Message publish not acknowledged, retrying...")
			span.AddEvent("amqp.publish.retry", trace.WithAttributes(
				attribute.String("reason", "message not acknowledged"),
				attribute.String("delivery_tag", strconv.FormatUint(confirm.DeliveryTag, 10)),
			))
			// Continue to retry the publish operation
			continue
		case <-time.After(c.connectionTimeout):
			// Confirmation timeout - retry the publish
			c.log.Warn().Msg("Publish confirmation timeout, retrying...")
			span.AddEvent("amqp.publish.retry", trace.WithAttributes(
				attribute.String("reason", "confirmation timeout"),
			))
			// Continue to retry the publish operation
			continue
		}
	}
}

// Consume starts consuming messages from the specified destination (queue name).
func (c *AMQPClientImpl) Consume(ctx context.Context, destination string) (<-chan amqp.Delivery, error) {
	return c.ConsumeFromQueue(ctx, ConsumeOptions{
		Queue:     destination,
		Consumer:  "",
		AutoAck:   false,
		Exclusive: false,
		NoLocal:   false,
		NoWait:    false,
	})
}

// ConsumeFromQueue consumes messages from a queue with specific options.
func (c *AMQPClientImpl) ConsumeFromQueue(_ context.Context, options ConsumeOptions) (<-chan amqp.Delivery, error) {
	c.m.RLock()
	if !c.isReady {
		c.m.RUnlock()
		return nil, errNotConnected
	}
	channel := c.channel
	c.m.RUnlock()

	// Set QoS for fair dispatch
	if err := channel.Qos(1, 0, false); err != nil {
		return nil, err
	}

	return channel.Consume(
		options.Queue,
		options.Consumer,
		options.AutoAck,
		options.Exclusive,
		options.NoLocal,
		options.NoWait,
		nil, // args
	)
}

// DeclareQueue declares a queue with the given parameters.
func (c *AMQPClientImpl) DeclareQueue(name string, durable, autoDelete, exclusive, noWait bool) error {
	c.m.RLock()
	if !c.isReady {
		c.m.RUnlock()
		return errNotConnected
	}
	channel := c.channel
	c.m.RUnlock()

	_, err := channel.QueueDeclare(name, durable, autoDelete, exclusive, noWait, nil)
	return err
}

// DeclareExchange declares an exchange with the given parameters.
func (c *AMQPClientImpl) DeclareExchange(name, kind string, durable, autoDelete, internal, noWait bool) error {
	c.m.RLock()
	if !c.isReady {
		c.m.RUnlock()
		return errNotConnected
	}
	channel := c.channel
	c.m.RUnlock()

	return channel.ExchangeDeclare(name, kind, durable, autoDelete, internal, noWait, nil)
}

// BindQueue binds a queue to an exchange with a routing key.
func (c *AMQPClientImpl) BindQueue(queue, exchange, routingKey string, noWait bool) error {
	c.m.RLock()
	if !c.isReady {
		c.m.RUnlock()
		return errNotConnected
	}
	channel := c.channel
	c.m.RUnlock()

	return channel.QueueBind(queue, routingKey, exchange, noWait, nil)
}

// Close gracefully shuts down the AMQP client.
func (c *AMQPClientImpl) Close() error {
	c.m.Lock()
	defer c.m.Unlock()

	if !c.isReady {
		return errAlreadyClosed
	}

	close(c.done)
	c.isReady = false

	var err error
	if c.channel != nil {
		if closeErr := c.channel.Close(); closeErr != nil {
			err = closeErr
		}
	}
	if c.connection != nil {
		if closeErr := c.connection.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}

	c.log.Info().Msg("AMQP client closed")
	return err
}

// handleReconnect manages connection lifecycle and reconnection logic.
func (c *AMQPClientImpl) handleReconnect() {
	for {
		c.m.Lock()
		c.isReady = false
		c.m.Unlock()

		c.log.Info().Str("broker_url", c.brokerURL).Msg("Attempting to connect to AMQP broker")

		conn, err := c.connect()
		if err != nil {
			c.log.Error().Err(err).Msg("Failed to connect to AMQP broker, retrying...")

			select {
			case <-c.done:
				return
			case <-time.After(c.reconnectDelay):
			}
			continue
		}

		if done := c.handleReInit(conn); done {
			break
		}
	}
}

// connect creates a new AMQP connection.
func (c *AMQPClientImpl) connect() (*amqp.Connection, error) {
	// Use pluggable dialer
	dial := getAmqpDialFunc()
	ac, err := dial(c.brokerURL)
	if err != nil {
		return nil, err
	}
	// Store as interface and also return underlying real connection when available
	c.changeConnection(ac)
	c.log.Info().Msg("Connected to AMQP broker")
	// If the underlying type is realConnection, return its concrete pointer; otherwise nil
	if rc, ok := ac.(realConnection); ok {
		return rc.c, nil
	}
	return nil, nil
}

// handleReInit manages channel initialization and reinitialization.
func (c *AMQPClientImpl) handleReInit(conn *amqp.Connection) bool {
	for {
		c.m.Lock()
		c.isReady = false
		c.m.Unlock()

		// Wrap real connection into adapter if needed
		var ac amqpConnection
		if conn != nil {
			ac = realConnection{c: conn}
		} else {
			ac = c.connection
		}
		err := c.init(ac)
		if err != nil {
			c.log.Error().Err(err).Msg("Failed to initialize AMQP channel, retrying...")

			select {
			case <-c.done:
				return true
			case <-c.notifyConnClose:
				c.log.Info().Msg("AMQP connection closed, reconnecting...")
				return false
			case <-time.After(c.reInitDelay):
			}
			continue
		}

		select {
		case <-c.done:
			return true
		case <-c.notifyConnClose:
			c.log.Info().Msg("AMQP connection closed, reconnecting...")
			return false
		case <-c.notifyChanClose:
			c.log.Info().Msg("AMQP channel closed, reinitializing...")
		}
	}
}

// init initializes the AMQP channel and sets up confirmation mode.
func (c *AMQPClientImpl) init(conn amqpConnection) error {
	ch, err := conn.Channel()
	if err != nil {
		return err
	}

	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return err
	}

	c.changeChannel(ch)
	c.m.Lock()
	c.isReady = true
	c.m.Unlock()

	c.log.Info().Msg("AMQP client initialized and ready")
	return nil
}

// changeConnection updates the connection and sets up close notifications.
func (c *AMQPClientImpl) changeConnection(connection amqpConnection) {
	c.connection = connection
	c.notifyConnClose = make(chan *amqp.Error, 1)
	c.connection.NotifyClose(c.notifyConnClose)
}

// changeChannel updates the channel and sets up notifications.
func (c *AMQPClientImpl) changeChannel(channel amqpChannel) {
	c.channel = channel
	c.notifyChanClose = make(chan *amqp.Error, 1)
	c.notifyConfirm = make(chan amqp.Confirmation, 1)
	c.channel.NotifyClose(c.notifyChanClose)
	c.channel.NotifyPublish(c.notifyConfirm)
}

// amqpHeaderAccessor implements trace.HeaderAccessor for AMQP headers
type amqpHeaderAccessor struct {
	headers amqp.Table
}

func (a *amqpHeaderAccessor) Get(key string) any {
	if a.headers == nil {
		return nil
	}
	return a.headers[key]
}

func (a *amqpHeaderAccessor) Set(key string, value any) {
	if a.headers == nil {
		a.headers = amqp.Table{}
	}
	a.headers[key] = value
}

// StartConsumeSpan creates an OpenTelemetry span for message consumption.
// It extracts the trace context from the delivery headers and creates a child span.
// This should be called by consumers when processing messages.
// The returned context should be used for downstream operations, and the span must be ended when done.
func StartConsumeSpan(ctx context.Context, delivery *amqp.Delivery, queueName string) (context.Context, trace.Span) {
	// Extract trace context from message headers
	accessor := &amqpHeaderAccessor{headers: delivery.Headers}
	ctx = gobrickstrace.ExtractFromHeaders(ctx, accessor)

	// Create span for consume operation
	tracer := otel.Tracer(messagingTracerName)
	spanName := queueName + " " + operationReceive

	ctx, span := tracer.Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindConsumer),
	)

	// Set messaging semantic attributes
	attrs := []attribute.KeyValue{
		attribute.String(string(semconv.MessagingSystemKey), messagingSystemRabbitMQ),
		semconv.MessagingOperationName(operationReceive),
		semconv.MessagingDestinationName(queueName),
		semconv.MessagingMessageBodySize(len(delivery.Body)),
	}
	if delivery.Exchange != "" {
		attrs = append(attrs, attribute.String("messaging.rabbitmq.exchange", delivery.Exchange))
	}
	if delivery.RoutingKey != "" {
		attrs = append(attrs, attribute.String("messaging.rabbitmq.routing_key", delivery.RoutingKey))
	}
	if delivery.MessageId != "" {
		attrs = append(attrs, semconv.MessagingMessageID(delivery.MessageId))
	}
	if delivery.CorrelationId != "" {
		attrs = append(attrs, semconv.MessagingMessageConversationID(delivery.CorrelationId))
	}
	span.SetAttributes(attrs...)

	return ctx, span
}

// unsafePublish publishes a message without confirmation handling.
func (c *AMQPClientImpl) unsafePublish(ctx context.Context, options PublishOptions, data []byte) error {
	c.m.RLock()
	if !c.isReady {
		c.m.RUnlock()
		return errNotConnected
	}
	channel := c.channel
	c.m.RUnlock()

	publishing := amqp.Publishing{
		ContentType: "application/octet-stream",
		Body:        data,
		Headers:     amqp.Table{},
	}

	if options.Headers != nil {
		maps.Copy(publishing.Headers, options.Headers)
	}

	// Inject trace headers using centralized trace package
	accessor := &amqpHeaderAccessor{headers: publishing.Headers}
	gobrickstrace.InjectIntoHeaders(ctx, accessor)

	// AMQP-specific: populate CorrelationId and MessageId
	// CorrelationId should correlate across a trace; MessageId should be unique per message
	traceID := gobrickstrace.EnsureTraceID(ctx)
	if publishing.CorrelationId == "" {
		publishing.CorrelationId = traceID
	}
	if publishing.MessageId == "" {
		// Prefer a unique ID per message for dedup/observability
		publishing.MessageId = uuid.New().String()
	}

	return channel.PublishWithContext(
		ctx,
		options.Exchange,
		options.RoutingKey,
		options.Mandatory,
		options.Immediate,
		publishing,
	)
}
