package streams

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
)

const (
	// tracerName matches the AMQP lane's tracer so both messaging lanes land under
	// one instrumentation scope.
	tracerName = "go-bricks/messaging"

	spanOperationReceive = "receive"
	messagingSystem      = "rabbitmq"

	logFieldStream   = "stream"
	logFieldConsumer = "consumer"
	logFieldOffset   = "offset"
)

// offsetStorer is the seam the stream client's consumers satisfy
// (*stream.Consumer and *ha.ReliableConsumer both implement StoreCustomOffset).
// The commit policy is written against it so it can be exercised without a broker.
type offsetStorer interface {
	StoreCustomOffset(offset int64) error
}

// offsetTracker owns the commit-after-success policy for one consumer.
//
// An offset reaches the broker only once its message has been handled without
// error, and only when either countBeforeStorage successes have accumulated
// since the last commit or flushInterval has elapsed since it. There is no
// background goroutine: a stalled stream commits nothing, which is correct
// because nothing new was handled.
type offsetTracker struct {
	countBeforeStorage int
	flushInterval      time.Duration
	now                func() time.Time

	mu            sync.Mutex
	pending       int
	lastHandledOK int64
	lastStoreAt   time.Time
	storedOffset  int64
	hasStored     bool
}

func newOffsetTracker(countBeforeStorage int, flushInterval time.Duration, now func() time.Time) *offsetTracker {
	if now == nil {
		now = time.Now
	}
	return &offsetTracker{
		countBeforeStorage: countBeforeStorage,
		flushInterval:      flushInterval,
		now:                now,
		lastStoreAt:        now(),
	}
}

// record applies the policy to one handled message and reports a store failure.
// A handler error contributes nothing: the offset of a failed message is never
// committed, and a later success therefore commits a HIGHER offset — the failed
// message is skipped, not redelivered.
func (t *offsetTracker) record(offset int64, handleErr error, store offsetStorer) error {
	if handleErr != nil {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.lastHandledOK = offset
	t.pending++

	if t.pending < t.countBeforeStorage && t.now().Sub(t.lastStoreAt) < t.flushInterval {
		return nil
	}
	return t.storeLocked(store)
}

// flush commits whatever is pending. It runs on stop so a clean shutdown does
// not replay work that was already handled successfully.
func (t *offsetTracker) flush(store offsetStorer) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.pending == 0 {
		return nil
	}
	return t.storeLocked(store)
}

// storeLocked commits the last successfully handled offset. On failure the
// pending counter is deliberately left intact so the next message retries.
func (t *offsetTracker) storeLocked(store offsetStorer) error {
	if err := store.StoreCustomOffset(t.lastHandledOK); err != nil {
		return err
	}
	t.storedOffset = t.lastHandledOK
	t.hasStored = true
	t.pending = 0
	t.lastStoreAt = t.now()
	return nil
}

// lastStored reports the last offset committed to the broker.
func (t *offsetTracker) lastStored() (offset int64, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.storedOffset, t.hasStored
}

// consumerRunner adapts one declared consumer to the stream client's callback.
type consumerRunner struct {
	name    string
	handler Handler
	tracker *offsetTracker
	log     logger.Logger
	tracer  trace.Tracer

	// baseCtx is held rather than passed because the client's MessagesHandler
	// carries no context of its own. The manager cancels it in StopConsumers.
	baseCtx context.Context
}

// messagesHandler is the callback handed to the stream client. The client
// invokes it sequentially per consumer, and the framework keeps it that way:
// handlers run inline with no worker pool, because a stream is an ordered log
// and any parallelism would both break that order and make a committed offset
// claim messages behind it were handled.
func (r *consumerRunner) messagesHandler(consumerContext stream.ConsumerContext, message *amqp.Message) {
	consumer := consumerContext.Consumer
	r.deliver(consumer.GetStreamName(), consumer.GetOffset(), message, consumer)
}

// deliver runs the handler for one message, then applies the commit policy.
func (r *consumerRunner) deliver(streamName string, offset int64, message *amqp.Message, store offsetStorer) {
	ctx, span := r.tracer.Start(r.baseCtx, streamName+" "+spanOperationReceive,
		trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()

	msg := &Message{
		Data:       message.GetData(),
		Offset:     offset,
		Stream:     streamName,
		Properties: message.ApplicationProperties,
	}

	span.SetAttributes(
		attribute.String(string(semconv.MessagingSystemKey), messagingSystem),
		semconv.MessagingOperationName(spanOperationReceive),
		semconv.MessagingDestinationName(streamName),
		semconv.MessagingMessageBodySize(len(msg.Data)),
	)

	start := time.Now()
	err := r.invoke(ctx, msg)
	tracking.RecordStreamConsume(ctx, streamName, time.Since(start), err)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		r.log.Error().Err(err).
			Str(logFieldStream, streamName).
			Str(logFieldConsumer, r.name).
			Int64(logFieldOffset, offset).
			Msg("Stream message handling failed - offset not committed")
	}

	if storeErr := r.tracker.record(offset, err, store); storeErr != nil {
		r.log.Warn().Err(storeErr).
			Str(logFieldStream, streamName).
			Str(logFieldConsumer, r.name).
			Int64(logFieldOffset, offset).
			Msg("Failed to store stream offset")
	}
}

// invoke calls the module handler, converting a panic into an error so the
// offset is not committed and consumption continues with the next message.
func (r *consumerRunner) invoke(ctx context.Context, msg *Message) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic in stream handler: %v", recovered)
			r.log.Error().
				Str(logFieldStream, msg.Stream).
				Str(logFieldConsumer, r.name).
				Int64(logFieldOffset, msg.Offset).
				Interface("panic", recovered).
				Bytes("stack", debug.Stack()).
				Msg("Panic recovered in stream handler - offset not committed")
		}
	}()

	return r.handler(ctx, msg)
}
