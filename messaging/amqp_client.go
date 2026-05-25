package messaging

import (
	"context"
	"errors"
	"maps"
	"math/rand/v2"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
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

	// pendingPublishes correlates broker confirmations to the in-flight publish
	// that issued them. Keyed by (channel generation, DeliveryTag) →
	// chan amqp.Confirmation (buffered, capacity 1). The generation rotates on
	// every changeChannel() so that late confirmations from a torn-down
	// channel cannot hit a publish registered against the NEW channel — both
	// channels start their DeliveryTag sequence at 1, so without generation
	// scoping a stale ACK would silently mark the wrong publish as confirmed.
	// On channel reinit, pending entries from the previous generation are
	// drained with a synthetic NACK so their publishers retry instead of
	// hanging on a tag the new generation will never emit.
	pendingPublishes sync.Map

	// generation rotates on every changeChannel() to scope pendingPublishes
	// entries to a single channel incarnation. Read under publishSerial
	// (which also guards channel reinit) so each publisher captures a
	// channel+generation pair that is mutually consistent.
	generation uint64

	// publishSerial serializes the full (channel snapshot → generation snapshot
	// → GetNextPublishSeqNo → pendingPublishes.Store → PublishWithContext)
	// sequence inside PublishToExchange and also guards changeChannel() —
	// changeChannel rotates the generation and starts a new dispatcher, so it
	// must not interleave with an in-flight publish that has already captured
	// the old channel reference. amqp091 takes its own lock around
	// GetNextPublishSeqNo and PublishWithContext separately, so without this
	// serialization two concurrent publishes can both read the same "next"
	// seq number, both register under that tag (second overwrites first), and
	// end up receiving each other's (or no) confirmations. The per-publish
	// confirm wait stays outside the lock and remains concurrent.
	publishSerial sync.Mutex

	// Configuration
	reconnectDelay    time.Duration
	reconnectMaxDelay time.Duration // upper bound for exponential backoff
	reInitDelay       time.Duration
	resendDelay       time.Duration
	connectionTimeout time.Duration
}

// Reconnection delays
const (
	defaultReconnectDelay    = 5 * time.Second
	defaultReconnectMaxDelay = 60 * time.Second
	defaultReInitDelay       = 2 * time.Second
	defaultResendDelay       = 5 * time.Second
	defaultConnectionTimeout = 30 * time.Second
	// defaultConfirmBufferSize sizes the channel that receives broker publish
	// confirmations. Big enough to absorb a reasonable concurrent-publish burst
	// without backpressuring producers, small enough that a stalled dispatcher
	// surfaces quickly rather than hiding behind a massive in-flight queue.
	defaultConfirmBufferSize = 256
)

// OpenTelemetry constants
const (
	messagingTracerName     = "go-bricks/messaging"
	messagingSystemRabbitMQ = "rabbitmq"
	operationPublish        = "publish"
	operationReceive        = "receive"
	contentTypeOctetStream  = "application/octet-stream"
	eventPublishRetry       = "amqp.publish.retry"
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
		reconnectMaxDelay: defaultReconnectMaxDelay,
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

	// Set messaging semantic attributes using semconv v1.32.0 helpers where available
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
		// Use official semconv helper for RabbitMQ routing key
		attrs = append(attrs, semconv.MessagingRabbitMQDestinationRoutingKey(options.RoutingKey))
	}
	span.SetAttributes(attrs...)

	return ctx, span
}

// preparePublishing creates an AMQP publishing message with headers, trace context, and message IDs.
func preparePublishing(ctx context.Context, options PublishOptions, data []byte) amqp.Publishing {
	publishing := amqp.Publishing{
		ContentType: contentTypeOctetStream,
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
	traceID := gobrickstrace.EnsureTraceID(ctx)
	if publishing.CorrelationId == "" {
		publishing.CorrelationId = traceID
	}
	if publishing.MessageId == "" {
		publishing.MessageId = uuid.New().String()
	}

	return publishing
}

// PublishToExchange publishes a message to a specific exchange with routing key.
func (c *AMQPClientImpl) PublishToExchange(ctx context.Context, options PublishOptions, data []byte) error {
	startTime := time.Now()

	// Create OpenTelemetry span for publish operation
	ctx, span := createPublishSpan(ctx, options, len(data), startTime)
	defer span.End()

	retryCount := 0
	for {
		select {
		case <-ctx.Done():
			// Record failed publish metrics before returning
			elapsed := time.Since(startTime)
			tracking.RecordAMQPPublishMetrics(ctx, options.Exchange, options.RoutingKey, elapsed, ctx.Err())
			span.RecordError(ctx.Err())
			span.SetStatus(codes.Error, ctx.Err().Error())
			return ctx.Err()
		case <-c.done:
			// Record failed publish metrics before returning
			elapsed := time.Since(startTime)
			tracking.RecordAMQPPublishMetrics(ctx, options.Exchange, options.RoutingKey, elapsed, errShutdown)
			span.RecordError(errShutdown)
			span.SetStatus(codes.Error, errShutdown.Error())
			return errShutdown
		default:
		}

		// Prepare message with headers, trace context, and message IDs
		publishing := preparePublishing(ctx, options, data)
		messageID := publishing.MessageId
		correlationID := publishing.CorrelationId

		// publishSerial guards the full critical section: readiness check,
		// channel snapshot, generation snapshot, GetNextPublishSeqNo,
		// pendingPublishes.Store, and PublishWithContext. Holding it across
		// all of these means:
		//   1. The channel and generation are mutually consistent — a
		//      changeChannel() rotation cannot interleave between our
		//      channel capture and our generation capture.
		//   2. amqp091's separately-locked GetNextPublishSeqNo and
		//      PublishWithContext become atomic from the publisher's POV,
		//      so concurrent publishers cannot collide on the same tag.
		// The per-publish confirm wait (below the Unlock) stays concurrent.
		confirmCh := make(chan amqp.Confirmation, 1)
		c.publishSerial.Lock()
		c.m.RLock()
		isReady := c.isReady
		channel := c.channel
		gen := c.generation
		c.m.RUnlock()
		if !isReady {
			c.publishSerial.Unlock()
			c.log.Warn().
				Str("exchange", options.Exchange).
				Str("routing_key", options.RoutingKey).
				Msg("AMQP client not ready, message not published")
			span.RecordError(errNotConnected)
			span.SetStatus(codes.Error, errNotConnected.Error())
			// BREAKING CHANGE: previously returned nil, silently dropping the message.
			// Returning the error gives callers a chance to retry, log, or escalate
			// instead of believing publish succeeded.
			return errNotConnected
		}
		expectedTag := channel.GetNextPublishSeqNo()
		key := confirmKey{generation: gen, tag: expectedTag}
		c.pendingPublishes.Store(key, confirmCh)
		err := channel.PublishWithContext(
			ctx,
			options.Exchange,
			options.RoutingKey,
			options.Mandatory,
			options.Immediate,
			publishing,
		)
		c.publishSerial.Unlock()

		if err != nil {
			// Publish never made it to the broker — drop our pending registration.
			// (A stray broker confirmation for this tag, if it somehow arrives later,
			// will be silently dropped by the dispatcher's unmatched-tag handling.)
			c.pendingPublishes.Delete(key)
			retryCount++
			c.log.Warn().Err(err).Int("retry_count", retryCount).Msg("Publish failed, retrying...")

			// Record retry metric
			tracking.RecordPublishRetry(ctx, options.Exchange, options.RoutingKey, "publish_error")

			span.AddEvent(eventPublishRetry, trace.WithAttributes(
				attribute.String("reason", "publish error"),
				attribute.String("error", err.Error()),
				attribute.Int("retry_count", retryCount),
			))
			select {
			case <-ctx.Done():
				// Record failed publish metrics before returning
				elapsed := time.Since(startTime)
				tracking.RecordAMQPPublishMetrics(ctx, options.Exchange, options.RoutingKey, elapsed, ctx.Err())
				span.RecordError(ctx.Err())
				span.SetStatus(codes.Error, ctx.Err().Error())
				return ctx.Err()
			case <-c.done:
				// Record failed publish metrics before returning
				elapsed := time.Since(startTime)
				tracking.RecordAMQPPublishMetrics(ctx, options.Exchange, options.RoutingKey, elapsed, errShutdown)
				span.RecordError(errShutdown)
				span.SetStatus(codes.Error, errShutdown.Error())
				return errShutdown
			case <-time.After(c.resendDelay):
			}
			continue
		}

		// Wait for confirmation on OUR per-publish channel — the dispatcher
		// goroutine routes only the confirmation whose (generation, DeliveryTag)
		// matches our key here.
		select {
		case <-ctx.Done():
			// Cleanup so the dispatcher doesn't hold a stale chan reference.
			c.pendingPublishes.Delete(key)
			elapsed := time.Since(startTime)
			tracking.RecordAMQPPublishMetrics(ctx, options.Exchange, options.RoutingKey, elapsed, ctx.Err())
			span.RecordError(ctx.Err())
			span.SetStatus(codes.Error, ctx.Err().Error())
			return ctx.Err()
		case <-c.done:
			c.pendingPublishes.Delete(key)
			elapsed := time.Since(startTime)
			tracking.RecordAMQPPublishMetrics(ctx, options.Exchange, options.RoutingKey, elapsed, errShutdown)
			span.RecordError(errShutdown)
			span.SetStatus(codes.Error, errShutdown.Error())
			return errShutdown
		case confirm := <-confirmCh:
			if confirm.Ack {
				// Track elapsed time and increment AMQP counter in context for request tracking
				elapsed := time.Since(startTime)
				logger.IncrementAMQPCounter(ctx)
				logger.AddAMQPElapsed(ctx, elapsed.Nanoseconds())

				// Record AMQP publish metrics
				tracking.RecordAMQPPublishMetrics(ctx, options.Exchange, options.RoutingKey, elapsed, nil)

				c.log.Debug().
					Str("exchange", options.Exchange).
					Str("routing_key", options.RoutingKey).
					Uint64("delivery_tag", confirm.DeliveryTag).
					Msg("Message published successfully")

				// Add message IDs to span for cross-system correlation
				span.SetAttributes(
					semconv.MessagingMessageID(messageID),
					semconv.MessagingMessageConversationID(correlationID),
				)
				span.SetStatus(codes.Ok, "")
				return nil
			}
			// NACK received - retry the publish
			retryCount++
			c.log.Warn().
				Uint64("delivery_tag", confirm.DeliveryTag).
				Int("retry_count", retryCount).
				Msg("Message publish not acknowledged, retrying...")

			// Record retry metric
			tracking.RecordPublishRetry(ctx, options.Exchange, options.RoutingKey, "nack")

			span.AddEvent(eventPublishRetry, trace.WithAttributes(
				attribute.String("reason", "message not acknowledged"),
				// #nosec G115 -- delivery tags are sequential and never overflow int in practice
				semconv.MessagingRabbitMQMessageDeliveryTag(int(confirm.DeliveryTag)),
				attribute.Int("retry_count", retryCount),
			))
			// Continue to retry the publish operation
			continue
		case <-time.After(c.connectionTimeout):
			// Confirmation timeout - retry the publish
			retryCount++
			c.log.Warn().Int("retry_count", retryCount).Msg("Publish confirmation timeout, retrying...")

			// Record retry metric
			tracking.RecordPublishRetry(ctx, options.Exchange, options.RoutingKey, "timeout")

			span.AddEvent(eventPublishRetry, trace.WithAttributes(
				attribute.String("reason", "confirmation timeout"),
				attribute.Int("retry_count", retryCount),
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

	// Set QoS for fair dispatch with configurable prefetch (v0.17+)
	prefetchCount := options.PrefetchCount
	if prefetchCount <= 0 {
		prefetchCount = 1 // Backward compatible default
	}
	if err := channel.Qos(prefetchCount, 0, false); err != nil {
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
		// Record channel close event
		tracking.RecordChannelEvent("close", nil)
	}
	if c.connection != nil {
		if closeErr := c.connection.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		// Record connection close event
		tracking.RecordConnectionEvent("close", nil)
	}

	c.log.Info().Msg("AMQP client closed")
	return err
}

// handleReconnect manages connection lifecycle and reconnection logic.
func (c *AMQPClientImpl) handleReconnect() {
	attempt := 0
	for {
		c.m.Lock()
		c.isReady = false
		c.m.Unlock()

		c.log.Info().Str("broker_url", redactAMQPURL(c.brokerURL)).Msg("Attempting to connect to AMQP broker")

		conn, err := c.connect()
		if err != nil {
			attempt++
			delay := computeBackoff(c.reconnectDelay, c.reconnectMaxDelay, attempt)
			c.log.Error().Err(err).
				Dur("backoff", delay).
				Int("attempt", attempt).
				Msg("Failed to connect to AMQP broker, retrying with exponential backoff")

			select {
			case <-c.done:
				return
			case <-time.After(delay):
			}
			continue
		}

		// Reset attempt counter on successful connect — next outage starts fresh.
		attempt = 0
		if c.handleReInit(conn) {
			break
		}
	}
}

// computeBackoff returns a duration in [0, min(base*2^attempt, cap)] for use as
// a reconnection/retry delay. Implements "full jitter" exponential backoff
// (https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter/) —
// the upper bound grows exponentially with the attempt count but every actual
// wait is a uniform random sample below it, which spreads herd recovery
// after a broker outage instead of all clients reconnecting at exactly t=cap.
func computeBackoff(base, maxDelay time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = defaultReconnectDelay
	}
	if maxDelay <= 0 {
		maxDelay = defaultReconnectMaxDelay
	}
	if maxDelay < base {
		maxDelay = base
	}
	backoff := base
	for i := 0; i < attempt && backoff < maxDelay; i++ {
		backoff *= 2
	}
	if backoff > maxDelay {
		backoff = maxDelay
	}
	if backoff <= 0 {
		return base
	}
	// Full jitter is a load-distribution mechanism, not a security primitive —
	// math/rand/v2 is the right tool here. crypto/rand would add system-call
	// overhead per reconnect attempt without changing the herd-spreading behavior.
	return time.Duration(rand.Int64N(int64(backoff))) //nolint:gosec // G404: jitter randomness, not cryptographic
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

	// Record connection create event
	tracking.RecordConnectionEvent("create", nil)

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
				// Record connection close event
				tracking.RecordConnectionEvent("close", nil)
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
			// Record connection close event
			tracking.RecordConnectionEvent("close", nil)
			return false
		case <-c.notifyChanClose:
			c.log.Info().Msg("AMQP channel closed, reinitializing...")
			// Record channel close event
			tracking.RecordChannelEvent("close", nil)
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

	// Record channel create event
	tracking.RecordChannelEvent("create", nil)

	c.log.Info().Msg("AMQP client initialized and ready")
	return nil
}

// changeConnection updates the connection and sets up close notifications.
func (c *AMQPClientImpl) changeConnection(connection amqpConnection) {
	c.connection = connection
	c.notifyConnClose = make(chan *amqp.Error, 1)
	c.connection.NotifyClose(c.notifyConnClose)
}

// confirmKey scopes a pending-publish entry to a single channel incarnation.
// The dispatcher matches on the full (generation, tag) pair so a late
// confirmation from a torn-down channel cannot hit a publish registered
// against the new channel (both channels start at DeliveryTag 1).
type confirmKey struct {
	generation uint64
	tag        uint64
}

// changeChannel updates the channel, rotates the generation, drains any
// pending publishes from the previous incarnation (synthetic NACK so their
// waiters retry on the new channel instead of hanging on a tag the new
// generation will never emit), and starts a fresh dispatcher pinned to the
// new generation. Acquires publishSerial so it is mutually exclusive with
// in-flight publish-handshake critical sections.
func (c *AMQPClientImpl) changeChannel(channel amqpChannel) {
	c.publishSerial.Lock()
	defer c.publishSerial.Unlock()

	oldGen := c.generation
	c.drainPendingPublishesWithNack(oldGen)

	c.generation++
	newGen := c.generation

	c.channel = channel
	c.notifyChanClose = make(chan *amqp.Error, 1)
	// Buffer sized for a reasonable concurrent-publish burst. The dispatcher
	// goroutine drains it; the broker will block PublishWithContext if this
	// fills up, providing natural backpressure.
	c.notifyConfirm = make(chan amqp.Confirmation, defaultConfirmBufferSize)
	c.channel.NotifyClose(c.notifyChanClose)
	c.channel.NotifyPublish(c.notifyConfirm)

	// Start the dispatcher for this channel incarnation, pinned to newGen
	// so late confirms from a previous channel (read by the previous
	// dispatcher, which is still running until its source channel closes)
	// route only to entries of THAT generation — never to ours.
	go c.dispatchConfirms(c.notifyConfirm, newGen)
}

// drainPendingPublishesWithNack signals every pending publish from the given
// generation that its confirmation will never arrive. Synthesizing a NACK
// (rather than closing the channel) lets the publisher's existing retry
// loop kick in cleanly — it captures a fresh DeliveryTag from the new
// channel/generation and tries again.
func (c *AMQPClientImpl) drainPendingPublishesWithNack(gen uint64) {
	c.pendingPublishes.Range(func(key, value any) bool {
		k, ok := key.(confirmKey)
		if !ok || k.generation != gen {
			return true
		}
		c.pendingPublishes.Delete(key)
		ch, ok := value.(chan amqp.Confirmation)
		if !ok {
			return true
		}
		// Non-blocking send: if the publisher already gave up via ctx.Done,
		// nobody reads, and we'd otherwise block forever.
		select {
		case ch <- amqp.Confirmation{DeliveryTag: k.tag, Ack: false}:
		default:
		}
		return true
	})
}

// dispatchConfirms reads confirmations from the broker's notify channel and
// routes each to the in-flight publish that registered for its (generation,
// DeliveryTag). The generation parameter pins this dispatcher to one channel
// incarnation — late confirms from a previous channel are read by THAT
// channel's dispatcher (still running until amqp091 closes the old notify
// channel), looked up under the previous generation, and never touch entries
// from the new generation even though the tags collide.
//
// Exits when src is closed (channel teardown) OR when c.done is closed (full
// client shutdown). Unmatched confirmations are silently dropped — they happen
// when a publish errored out before registering or when the publisher already
// drained via NACK after channel reinit.
func (c *AMQPClientImpl) dispatchConfirms(src <-chan amqp.Confirmation, gen uint64) {
	for {
		select {
		case <-c.done:
			return
		case confirm, ok := <-src:
			if !ok {
				return // channel closed by broker / channel teardown
			}
			v, ok := c.pendingPublishes.LoadAndDelete(confirmKey{generation: gen, tag: confirm.DeliveryTag})
			if !ok {
				continue
			}
			ch, ok := v.(chan amqp.Confirmation)
			if !ok {
				continue
			}
			// Non-blocking send: publisher may have abandoned via ctx.Done.
			select {
			case ch <- confirm:
			default:
			}
		}
	}
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
	tracer := otel.Tracer(messagingTracerName)
	if delivery == nil {
		// No delivery, return a no-op span
		return tracer.Start(ctx, queueName+" "+operationReceive, trace.WithSpanKind(trace.SpanKindConsumer))
	}
	// Extract trace context from message headers
	accessor := &amqpHeaderAccessor{headers: delivery.Headers}
	ctx = gobrickstrace.ExtractFromHeaders(ctx, accessor)

	// Create span for consume operation
	// Uses "receive" operation as this span covers receiving from broker;
	// application code can create child "process" spans for message handling if needed
	spanName := queueName + " " + operationReceive

	ctx, span := tracer.Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindConsumer),
	)

	// Set messaging semantic attributes using semconv v1.32.0 helpers where available
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
		// Use official semconv helper for RabbitMQ routing key
		attrs = append(attrs, semconv.MessagingRabbitMQDestinationRoutingKey(delivery.RoutingKey))
	}
	if delivery.MessageId != "" {
		attrs = append(attrs, semconv.MessagingMessageID(delivery.MessageId))
	}
	if delivery.CorrelationId != "" {
		attrs = append(attrs, semconv.MessagingMessageConversationID(delivery.CorrelationId))
	}
	span.SetAttributes(attrs...)

	// Automatically record consume metrics
	// Duration is 0 at initial receive time - application can track processing duration separately
	tracking.RecordAMQPConsumeMetrics(ctx, delivery, queueName, 0, nil)

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

	publishing := preparePublishing(ctx, options, data)

	return channel.PublishWithContext(
		ctx,
		options.Exchange,
		options.RoutingKey,
		options.Mandatory,
		options.Immediate,
		publishing,
	)
}
