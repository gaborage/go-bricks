package messaging

import (
	"context"
	"errors"
	"fmt"
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
	m          *sync.RWMutex
	brokerURL  string
	log        logger.Logger
	connection amqpConnection
	channel    amqpChannel
	done       chan bool
	// reconnectDone is closed by handleReconnect when it returns. Close does not
	// block on it (Close stays non-blocking), but it lets tests confirm the
	// reconnection goroutine has fully exited so a leaked goroutine can't race
	// later tests through the shared dial func. Set only when NewAMQPClient
	// starts the goroutine; nil for manually-constructed test clients.
	reconnectDone   chan struct{}
	notifyConnClose chan *amqp.Error
	notifyChanClose chan *amqp.Error
	notifyConfirm   chan amqp.Confirmation
	isReady         bool
	// closed marks that Close has run. It is distinct from isReady: a client
	// that never finished connecting is not ready but is also not closed, and
	// Close must still stop its reconnection goroutine. Guarded by c.m.
	closed bool

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
	// readyTimeout bounds PublishToExchange's pre-flight wait for a not-yet-ready
	// client (cold start or mid-reconnect) to become ready before the publish
	// attempt begins. Zero or negative disables the wait entirely, reproducing the
	// pre-#655 instant fail-fast — this is the Go zero value, so struct-literal
	// test clients that don't set the field keep their old behavior. See
	// waitForReady.
	readyTimeout time.Duration
	// maxPublishAttempts bounds the per-publish retry loop so PublishToExchange
	// always returns to its caller. A value <= 0 means unbounded (the historical
	// behavior) — kept so struct-literal test clients that don't set the field
	// retain their old semantics.
	maxPublishAttempts int
	// nackBackoff is the cancelable delay inserted between NACK retries, replacing
	// the old zero-delay hot-spin. Zero means no delay.
	nackBackoff time.Duration
}

// Reconnection delays
const (
	defaultReconnectDelay    = 5 * time.Second
	defaultReconnectMaxDelay = 60 * time.Second
	defaultReInitDelay       = 2 * time.Second
	defaultResendDelay       = 5 * time.Second
	defaultConnectionTimeout = 30 * time.Second
	// defaultMaxPublishAttempts bounds the publish retry loop. 5 rides out a
	// typical reconnect (reconnectDelay 5s + reInitDelay 2s) across a few resend
	// cycles before giving up and returning a classifiable error to the caller.
	defaultMaxPublishAttempts = 5
	// defaultReadyTimeout bounds PublishToExchange's pre-flight wait for a cold or
	// mid-reconnect client to become ready before the publish attempt begins. 5s
	// covers the common case (broker handshake + channel init) without materially
	// extending a caller's request budget. See issue #655.
	defaultReadyTimeout = 5 * time.Second
	// defaultNackBackoff is a small cancelable pause between NACK retries so a
	// transiently-unroutable publish (e.g. a binding still being created) gets a
	// few spaced attempts without busy-spinning, while staying well under a tight
	// request deadline.
	defaultNackBackoff = 100 * time.Millisecond
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
	// ErrNotConnected is returned by a publish when the client is not connected to the broker.
	ErrNotConnected = errors.New("not connected to AMQP broker")
	// ErrShutdown is returned when a publish is interrupted by client shutdown. The outbox
	// relay treats this (and context.Canceled) as a shutdown abort that must NOT advance an
	// event's retry_count — it is the one publish error the relay branches on.
	ErrShutdown = errors.New("AMQP client is shutting down")
	// ErrPublishRetriesExhausted is returned by PublishToExchange once the bounded retry loop
	// reaches maxPublishAttempts. It wraps the last attempt's cause (one of ErrPublishNacked,
	// ErrPublishConfirmTimeout, or the raw publish error) so a caller can see WHY it gave up.
	ErrPublishRetriesExhausted = errors.New("amqp: publish retries exhausted")
	// ErrPublishNacked is the cause when the broker negatively acknowledged a publish. A
	// basic.nack on a publish-confirm is a transient broker condition (disk alarm, mirror
	// resync, failover), not a statement that the message is bad. These cause sentinels are
	// informational (logging / direct-publisher branching) — the outbox relay does NOT
	// classify on them (see ADR-033); it retries every publish failure.
	ErrPublishNacked = errors.New("amqp: publish nacked by broker")
	// ErrPublishConfirmTimeout is the cause when a confirmed publish never received an
	// ACK/NACK within connectionTimeout.
	ErrPublishConfirmTimeout = errors.New("amqp: publish confirmation timed out")

	// errNotConnected / errShutdown alias the exported sentinels so the many existing
	// internal references keep compiling and assert.Equal-style tests keep matching.
	errNotConnected   = ErrNotConnected
	errShutdown       = ErrShutdown
	errAlreadyClosed  = errors.New("AMQP client already closed")
	errNilDeclaration = errors.New("nil messaging declaration")
)

// ClientOption configures an AMQPClientImpl at construction time.
type ClientOption func(*AMQPClientImpl)

// WithConnectionTimeout overrides the per-publish broker confirmation timeout —
// the wait for an ACK/NACK after a confirmed publish (see PublishToExchange).
// Non-positive values are ignored, leaving the 30s default in place.
func WithConnectionTimeout(d time.Duration) ClientOption {
	return func(c *AMQPClientImpl) {
		if d > 0 {
			c.connectionTimeout = d
		}
	}
}

// WithMaxPublishAttempts bounds the per-publish retry loop: after n failed
// attempts PublishToExchange returns ErrPublishRetriesExhausted instead of
// retrying forever. Non-positive values are ignored, leaving the default (5).
func WithMaxPublishAttempts(n int) ClientOption {
	return func(c *AMQPClientImpl) {
		if n >= 1 {
			c.maxPublishAttempts = n
		}
	}
}

// WithReadyTimeout bounds PublishToExchange's pre-flight wait for a not-yet-ready
// client to become ready before the publish attempt begins (see waitForReady).
// The wait does not consume a maxPublishAttempts slot. Non-positive values are
// ignored, leaving the 5s default in place.
func WithReadyTimeout(d time.Duration) ClientOption {
	return func(c *AMQPClientImpl) {
		if d > 0 {
			c.readyTimeout = d
		}
	}
}

// WithReconnectDelay overrides the base of the full-jitter reconnect backoff:
// each wait is a uniform random sample in [0, min(base*2^attempt, max)), so this
// is the first attempt's upper bound, not a minimum spacing between attempts.
// Non-positive values are ignored, leaving the 5s default in place.
func WithReconnectDelay(d time.Duration) ClientOption {
	return func(c *AMQPClientImpl) {
		if d > 0 {
			c.reconnectDelay = d
		}
	}
}

// WithReconnectMaxDelay overrides the ceiling of the connection-reconnect backoff.
// The consumer re-subscribe loop (registry.go) keeps its own fixed cap.
// Non-positive values are ignored, leaving the 60s default in place.
func WithReconnectMaxDelay(d time.Duration) ClientOption {
	return func(c *AMQPClientImpl) {
		if d > 0 {
			c.reconnectMaxDelay = d
		}
	}
}

// WithReinitDelay overrides the delay before channel reinitialization after failure.
// Non-positive values are ignored, leaving the 2s default in place.
func WithReinitDelay(d time.Duration) ClientOption {
	return func(c *AMQPClientImpl) {
		if d > 0 {
			c.reInitDelay = d
		}
	}
}

// WithResendDelay overrides the wait between retries after a channel-level
// publish error only; broker NACKs retry on the fixed 100ms nackBackoff and
// confirmation timeouts retry immediately.
// Non-positive values are ignored, leaving the 5s default in place.
func WithResendDelay(d time.Duration) ClientOption {
	return func(c *AMQPClientImpl) {
		if d > 0 {
			c.resendDelay = d
		}
	}
}

// NewAMQPClient creates a new AMQP client instance.
// It automatically attempts to connect to the broker and handles reconnections.
// Optional Option values (e.g. WithConnectionTimeout) override the defaults.
func NewAMQPClient(brokerURL string, log logger.Logger, opts ...ClientOption) *AMQPClientImpl {
	client := &AMQPClientImpl{
		m:                  &sync.RWMutex{},
		brokerURL:          brokerURL,
		log:                log,
		done:               make(chan bool),
		reconnectDone:      make(chan struct{}),
		reconnectDelay:     defaultReconnectDelay,
		reconnectMaxDelay:  defaultReconnectMaxDelay,
		reInitDelay:        defaultReInitDelay,
		resendDelay:        defaultResendDelay,
		connectionTimeout:  defaultConnectionTimeout,
		readyTimeout:       defaultReadyTimeout,
		maxPublishAttempts: defaultMaxPublishAttempts,
		nackBackoff:        defaultNackBackoff,
	}

	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(client)
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

	ctx, span := createPublishSpan(ctx, options, len(data), startTime)
	defer span.End()

	// Pre-flight: give a cold or mid-reconnect client a bounded, context-aware
	// window to become ready BEFORE entering the retry loop below. This wait does
	// NOT consume a maxPublishAttempts slot — a cold client failing fast on the
	// very first publish (before handleReconnect's async connect finishes) was
	// the root cause of issue #655. A zero/negative readyTimeout disables the
	// wait entirely, reproducing the pre-#655 behavior byte-for-byte.
	if err := c.publishPreflight(ctx, options, startTime, span); err != nil {
		return err
	}

	// publishStart marks the beginning of the actual broker attempt, AFTER the
	// pre-flight readiness wait resolved. The retry loop below feeds this — not
	// startTime — into publish-latency metrics/logging: startTime→publishStart can
	// be several seconds on a cold-start or mid-reconnect wait (bounded by
	// readyTimeout), and folding that into "broker latency" would make a single
	// successful publish look like a multi-second broker hiccup. The span created
	// above intentionally keeps startTime — its duration is meant to cover the
	// full call, wait included.
	publishStart := time.Now()

	retryCount := 0
	// lastCause records why the most recent attempt failed (the raw publish error,
	// ErrPublishNacked, or ErrPublishConfirmTimeout). It is wrapped into the terminal error on
	// every exit path so a deadline/cancel that fires mid-retry still reports what was going
	// wrong — for the caller's logging and for direct publishers that branch on the cause.
	var lastCause error
	for {
		select {
		case <-ctx.Done():
			return c.publishAbort(ctx, options, publishStart, span, ctx.Err(), lastCause)
		case <-c.done:
			return c.publishAbort(ctx, options, publishStart, span, errShutdown, lastCause)
		default:
		}

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
			lastCause = err
			c.log.Warn().Err(err).Int("retry_count", retryCount).Msg("Publish failed, retrying...")

			tracking.RecordPublishRetry(ctx, options.Exchange, options.RoutingKey, "publish_error")

			span.AddEvent(eventPublishRetry, trace.WithAttributes(
				attribute.String("reason", "publish error"),
				attribute.String("error", err.Error()),
				attribute.Int("retry_count", retryCount),
			))
			if termErr := c.retryBackoff(ctx, options, publishStart, span, retryCount, lastCause, c.resendDelay); termErr != nil {
				return termErr
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
			return c.publishAbort(ctx, options, publishStart, span, ctx.Err(), lastCause)
		case <-c.done:
			c.pendingPublishes.Delete(key)
			return c.publishAbort(ctx, options, publishStart, span, errShutdown, lastCause)
		case confirm := <-confirmCh:
			if confirm.Ack {
				// Track elapsed time and increment AMQP counter in context for request tracking.
				// publishStart (not startTime) excludes the pre-flight readiness wait so a
				// cold-start publish doesn't misreport that wait as broker latency.
				elapsed := time.Since(publishStart)
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
			lastCause = ErrPublishNacked
			c.log.Warn().
				Uint64("delivery_tag", confirm.DeliveryTag).
				Int("retry_count", retryCount).
				Msg("Message publish not acknowledged, retrying...")

			tracking.RecordPublishRetry(ctx, options.Exchange, options.RoutingKey, "nack")

			span.AddEvent(eventPublishRetry, trace.WithAttributes(
				attribute.String("reason", "message not acknowledged"),
				// #nosec G115 -- delivery tags are sequential and never overflow int in practice
				semconv.MessagingRabbitMQMessageDeliveryTag(int(confirm.DeliveryTag)),
				attribute.Int("retry_count", retryCount),
			))
			// retryBackoff applies the attempt ceiling and a cancelable backoff between
			// NACK retries — replacing the old zero-delay hot-spin so a transiently-
			// unroutable publish gets a few spaced attempts without pinning a core.
			if termErr := c.retryBackoff(ctx, options, publishStart, span, retryCount, lastCause, c.nackBackoff); termErr != nil {
				return termErr
			}
			continue
		case <-time.After(c.connectionTimeout):
			// Drop this attempt's waiter from pendingPublishes before retrying.
			// Unlike the NACK path (where the dispatcher already consumed the
			// entry via LoadAndDelete before forwarding the confirmation), the
			// timeout path bails BEFORE any confirmation arrives, so the entry
			// is still registered. Without this delete every timeout leaks one
			// pendingPublishes entry until the channel is torn down, and a
			// silently-stuck broker can grow the map unboundedly.
			c.pendingPublishes.Delete(key)
			// Confirmation timeout - retry the publish
			retryCount++
			lastCause = ErrPublishConfirmTimeout
			c.log.Warn().Int("retry_count", retryCount).Msg("Publish confirmation timeout, retrying...")

			tracking.RecordPublishRetry(ctx, options.Exchange, options.RoutingKey, "timeout")

			span.AddEvent(eventPublishRetry, trace.WithAttributes(
				attribute.String("reason", "confirmation timeout"),
				attribute.Int("retry_count", retryCount),
			))
			// No extra backoff — this path already waited connectionTimeout; just
			// apply the attempt ceiling before retrying.
			if termErr := c.retryBackoff(ctx, options, publishStart, span, retryCount, lastCause, 0); termErr != nil {
				return termErr
			}
			continue
		}
	}
}

// publishPreflight runs waitForReady ahead of PublishToExchange's retry loop and
// translates the outcome into the same terminal-error shape as every other exit
// path: a readyTimeout expiry logs the same WARN production always emitted for a
// not-ready client and returns the raw errNotConnected; a ctx cancel or shutdown
// is routed through publishAbort like every other abort. Split out of
// PublishToExchange to keep its cyclomatic complexity within budget (gocyclo).
func (c *AMQPClientImpl) publishPreflight(ctx context.Context, options PublishOptions, startTime time.Time, span trace.Span) error {
	err := c.waitForReady(ctx)
	if err == nil {
		return nil
	}
	if errors.Is(err, errNotConnected) {
		c.log.Warn().
			Str("exchange", options.Exchange).
			Str("routing_key", options.RoutingKey).
			Dur("ready_timeout", c.readyTimeout).
			Msg("AMQP client still not ready after waiting, message not published")
		span.RecordError(errNotConnected)
		span.SetStatus(codes.Error, errNotConnected.Error())
		return errNotConnected
	}
	return c.publishAbort(ctx, options, startTime, span, err, nil)
}

// readyWaitOutcome reports why pollUntilReady's bounded wait ended. Callers
// translate it into their own error shape — pollUntilReady itself is
// error-shape-agnostic so it can serve both waitForReady (which distinguishes
// timeout/cancel/shutdown into three different errors) and
// Registry.DeclareInfrastructure (which has no shutdown channel at all).
type readyWaitOutcome int

const (
	readyWaitBecameReady readyWaitOutcome = iota
	readyWaitTimedOut
	readyWaitCanceled
	readyWaitDone
)

// pollUntilReady runs the shared timer+ticker+select poll loop behind both
// AMQPClientImpl.waitForReady and Registry.DeclareInfrastructure's readiness
// wait. It does NOT perform an immediate pre-check of isReady() before
// starting the ticker — callers that want to skip the wait entirely when
// already ready must do that check themselves first (waitForReady does;
// DeclareInfrastructure historically does not, so it always waits for at
// least one interval tick before its first readiness check — preserved here
// to keep that call site's observable behavior unchanged).
//
// done is optional: passing nil is safe because a nil channel is never
// selectable (that case simply never fires), which is what
// DeclareInfrastructure needs since it has no shutdown channel to watch.
// onTick, also optional, runs once per failed poll (i.e. every interval tick
// where isReady() is still false) — DeclareInfrastructure uses it to preserve
// its per-tick Debug log; waitForReady passes nil since it never logged per-tick.
func pollUntilReady(ctx context.Context, timeout, interval time.Duration, isReady func() bool, done <-chan bool, onTick func()) readyWaitOutcome {
	t := time.NewTimer(timeout)
	defer t.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return readyWaitCanceled
		case <-done:
			return readyWaitDone
		case <-t.C:
			return readyWaitTimedOut
		case <-ticker.C:
			if isReady() {
				return readyWaitBecameReady
			}
			if onTick != nil {
				onTick()
			}
		}
	}
}

// waitForReady blocks PublishToExchange's pre-flight check until the client
// becomes ready, the bounded readyTimeout elapses, the context is canceled, or
// the client is shut down — whichever comes first. A readyTimeout <= 0 disables
// the wait entirely (returns nil immediately without even checking IsReady),
// reproducing the pre-#655 instant fail-fast for callers that opt out or for
// struct-literal test clients that never set the field.
//
// Reuses the same readinessCheckInterval poll cadence (via pollUntilReady) as
// Registry.DeclareInfrastructure so both "wait for ready" call sites share one
// mental model. Returns errNotConnected (unwrapped) on timeout expiry — the
// caller re-raises it as-is; ctx.Err() or errShutdown on cancellation/shutdown
// — the caller wraps those through publishAbort like every other exit path.
func (c *AMQPClientImpl) waitForReady(ctx context.Context) error {
	if c.readyTimeout <= 0 {
		return nil
	}
	if c.IsReady() {
		return nil
	}

	switch pollUntilReady(ctx, c.readyTimeout, readinessCheckInterval, c.IsReady, c.done, nil) {
	case readyWaitBecameReady:
		return nil
	case readyWaitCanceled:
		return ctx.Err()
	case readyWaitDone:
		return errShutdown
	default: // readyWaitTimedOut
		return errNotConnected
	}
}

// retryBackoff is the shared tail of every failed-attempt branch. It applies the
// attempt ceiling (returning a terminal ErrPublishRetriesExhausted once reached) and,
// if backoff > 0, a cancelable wait that still honors ctx cancel / client shutdown.
// It returns a non-nil error the caller must RETURN from PublishToExchange, or nil to
// CONTINUE the retry loop. (Only the failure branches call it — never the ACK path.)
func (c *AMQPClientImpl) retryBackoff(ctx context.Context, options PublishOptions, startTime time.Time, span trace.Span, retryCount int, lastCause error, backoff time.Duration) error {
	if c.maxPublishAttempts > 0 && retryCount >= c.maxPublishAttempts {
		return c.publishExhausted(ctx, options, startTime, span, retryCount, lastCause)
	}
	if backoff <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return c.publishAbort(ctx, options, startTime, span, ctx.Err(), lastCause)
	case <-c.done:
		return c.publishAbort(ctx, options, startTime, span, errShutdown, lastCause)
	case <-time.After(backoff):
		return nil
	}
}

// recordPublishFailure stamps the shared terminal metrics/span state for a publish that is
// ending in failure and returns err unchanged, so the terminal helpers only differ in how
// they build err.
func (c *AMQPClientImpl) recordPublishFailure(ctx context.Context, options PublishOptions, startTime time.Time, span trace.Span, err error) error {
	tracking.RecordAMQPPublishMetrics(ctx, options.Exchange, options.RoutingKey, time.Since(startTime), err)
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	return err
}

// wrapCause attaches the last retry cause to a terminal error (context cancel, deadline, or
// shutdown). errors.Is on the result matches BOTH the primary error and the cause, so a deadline
// that fires after a NACK still reports ErrPublishNacked — useful for a caller's logging and for
// direct publishers that branch on the cause. (The outbox relay does NOT branch on it: see
// ADR-033 — it treats every publish failure as connectivity.)
func wrapCause(primary, lastCause error) error {
	if lastCause == nil {
		return primary
	}
	return fmt.Errorf("%w; last attempt: %w", primary, lastCause)
}

// publishAbort records terminal state for a publish ending without success (cancel, deadline,
// or shutdown) and returns the primary error wrapped with the last retry cause.
func (c *AMQPClientImpl) publishAbort(ctx context.Context, options PublishOptions, startTime time.Time, span trace.Span, primary, lastCause error) error {
	return c.recordPublishFailure(ctx, options, startTime, span, wrapCause(primary, lastCause))
}

// publishExhausted is the terminal return once the bounded retry loop reaches maxPublishAttempts.
// The returned error wraps ErrPublishRetriesExhausted around the last attempt's cause (publish
// error, ErrPublishNacked, or ErrPublishConfirmTimeout) so the caller can see why it gave up.
func (c *AMQPClientImpl) publishExhausted(ctx context.Context, options PublishOptions, startTime time.Time, span trace.Span, attempts int, cause error) error {
	err := fmt.Errorf("%w after %d attempts: %w", ErrPublishRetriesExhausted, attempts, cause)
	return c.recordPublishFailure(ctx, options, startTime, span, err)
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
	channel, err := c.readyChannel()
	if err != nil {
		return nil, err
	}

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

// toTable converts declaration args to an amqp table; empty means no table.
func toTable(args map[string]any) amqp.Table {
	if len(args) == 0 {
		return nil
	}
	return amqp.Table(args)
}

// readyChannel returns the current channel under RLock, or errNotConnected
// when the client is not ready. Callers must not retain the channel across
// reconnects.
func (c *AMQPClientImpl) readyChannel() (amqpChannel, error) {
	c.m.RLock()
	defer c.m.RUnlock()
	if !c.isReady {
		return nil, errNotConnected
	}
	return c.channel, nil
}

// DeclareQueue declares a queue from the given declaration.
// ctx is honored as a pre-flight check: amqp091 declare/bind operations are not
// context-aware on the wire, so a canceled context fails fast before the call.
func (c *AMQPClientImpl) DeclareQueue(ctx context.Context, queue *QueueDeclaration) error {
	if queue == nil {
		return errNilDeclaration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	channel, err := c.readyChannel()
	if err != nil {
		return err
	}

	_, err = channel.QueueDeclare(queue.Name, queue.Durable, queue.AutoDelete, queue.Exclusive, queue.NoWait, toTable(queue.Args))
	return err
}

// DeclareExchange declares an exchange from the given declaration (ctx: pre-flight check, see DeclareQueue).
func (c *AMQPClientImpl) DeclareExchange(ctx context.Context, exchange *ExchangeDeclaration) error {
	if exchange == nil {
		return errNilDeclaration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	channel, err := c.readyChannel()
	if err != nil {
		return err
	}

	return channel.ExchangeDeclare(exchange.Name, exchange.Type, exchange.Durable, exchange.AutoDelete, exchange.Internal, exchange.NoWait, toTable(exchange.Args))
}

// BindQueue binds a queue to an exchange from the given declaration (ctx: pre-flight check, see DeclareQueue).
func (c *AMQPClientImpl) BindQueue(ctx context.Context, binding *BindingDeclaration) error {
	if binding == nil {
		return errNilDeclaration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	channel, err := c.readyChannel()
	if err != nil {
		return err
	}

	return channel.QueueBind(binding.Queue, binding.RoutingKey, binding.Exchange, binding.NoWait, toTable(binding.Args))
}

// Close gracefully shuts down the AMQP client.
func (c *AMQPClientImpl) Close() error {
	c.m.Lock()

	// Idempotency is keyed on closed, NOT isReady: a client whose initial
	// connect never succeeded is still running a reconnection goroutine that
	// Close must stop, even though it never became ready.
	if c.closed {
		c.m.Unlock()
		return errAlreadyClosed
	}
	c.closed = true

	close(c.done)
	c.isReady = false

	var err error
	if c.channel != nil {
		if closeErr := c.channel.Close(); closeErr != nil {
			err = closeErr
		}
		tracking.RecordChannelEvent("close", nil)
	}
	if c.connection != nil {
		if closeErr := c.connection.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		tracking.RecordConnectionEvent("close", nil)
	}

	c.m.Unlock()

	// Close stays non-blocking: it signals the reconnection goroutine to stop
	// (via close(c.done)) but does not wait for it. Waiting here would let an
	// in-flight, uncancellable dial stall callers — and Manager invokes Close
	// while holding pubMu/consMu (shutdown, LRU eviction, idle cleanup), so a
	// blocking Close could wedge the publish path. The goroutine exits on its
	// own at its next select; reconnectDone lets tests confirm that exit.
	c.log.Info().Msg("AMQP client closed")
	return err
}

// handleReconnect manages connection lifecycle and reconnection logic.
func (c *AMQPClientImpl) handleReconnect() {
	// Signal that this goroutine has fully exited (tests wait on reconnectDone
	// to confirm teardown; Close itself does not block on it). Guarded because
	// manually-constructed test clients may run handleReconnect without a
	// reconnectDone channel.
	if c.reconnectDone != nil {
		defer close(c.reconnectDone)
	}

	attempt := 0
	for {
		// Stop before starting another connect attempt once Close has fired, so
		// no connect/changeConnection runs while/after Close is tearing down.
		select {
		case <-c.done:
			return
		default:
		}

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
	return time.Duration(rand.Int64N(int64(backoff))) //#nosec G404 -- jitter randomness, not cryptographic
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
			tracking.RecordConnectionEvent("close", nil)
			return false
		case <-c.notifyChanClose:
			c.log.Info().Msg("AMQP channel closed, reinitializing...")
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
	// Don't flip back to ready if Close already ran: a concurrent Close sets
	// closed under c.m, and a closed client must stay not-ready.
	if !c.closed {
		c.isReady = true
	}
	c.m.Unlock()

	tracking.RecordChannelEvent("create", nil)

	c.log.Info().Msg("AMQP client initialized and ready")
	return nil
}

// changeConnection updates the connection and sets up close notifications.
// The connection/notify fields are written under c.m so a concurrent Close
// (which reads c.connection under the same lock) cannot race this write.
func (c *AMQPClientImpl) changeConnection(connection amqpConnection) {
	c.m.Lock()
	c.connection = connection
	c.notifyConnClose = make(chan *amqp.Error, 1)
	notify := c.notifyConnClose
	c.m.Unlock()
	connection.NotifyClose(notify)
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

	// Publish the channel pointer under c.m so a concurrent Close (which reads
	// c.channel under c.m to tear it down) and the c.m-guarded readers in
	// DeclareQueue/ConsumeFromQueue cannot race this write. Lock order is
	// publishSerial → c.m, matching PublishToExchange.
	c.m.Lock()
	c.channel = channel
	c.m.Unlock()
	c.notifyChanClose = make(chan *amqp.Error, 1)
	// Buffer sized for a reasonable concurrent-publish burst. The dispatcher
	// goroutine drains it; the broker will block PublishWithContext if this
	// fills up, providing natural backpressure.
	c.notifyConfirm = make(chan amqp.Confirmation, defaultConfirmBufferSize)
	channel.NotifyClose(c.notifyChanClose)
	channel.NotifyPublish(c.notifyConfirm)

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
		// No delivery, return a no-op span. Span-factory pattern: ownership of the
		// span is transferred to the caller, which must end it (see doc comment).
		// spancheck cannot model this cross-function transfer.
		//nolint:spancheck // span ownership intentionally transferred to caller
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
	channel, err := c.readyChannel()
	if err != nil {
		return err
	}

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
