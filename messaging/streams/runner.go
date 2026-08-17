package streams

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"runtime/debug"
	"sync"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
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

// errNoOffsetStorer reports a commit that had no storer to reach the broker
// through. The manager resolves a non-nil storer for every stream name, so this
// is not a state production reaches: it is defense in depth at the last frame
// before this package dereferences an interface it does not own, on a path that
// also runs during shutdown, where a panic would cost every other stream the
// commit it was about to make.
var errNoOffsetStorer = errors.New("no offset storer for this stream; offset not committed")

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
	if store == nil {
		return errNoOffsetStorer
	}
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

// flushFailure is one stream's failed offset commit, reported by offsetBook.flush
// so the caller can name the stream that did not land.
type flushFailure struct {
	stream string
	err    error
}

// offsetBook holds the offset trackers of one running consumer, keyed by the
// stream a delivery arrived on: exactly one entry for a plain stream, one per
// partition for a super stream. Partitions must not share a tracker — a commit
// says "everything up to here was handled on THIS stream", which stops being true
// the moment two partitions advance one counter.
//
// Trackers are created on first delivery, so a partition that never delivered has
// nothing to flush and nothing to report.
type offsetBook struct {
	newTracker func() *offsetTracker

	mu       sync.Mutex
	trackers map[string]*offsetTracker
}

func newOffsetBook(newTracker func() *offsetTracker) *offsetBook {
	return &offsetBook{newTracker: newTracker, trackers: make(map[string]*offsetTracker)}
}

// trackerFor returns one stream's tracker, creating it on first use. The client
// calls this from one delivery goroutine per partition, so the map is guarded even
// though each tracker only ever serves a single one of them.
func (b *offsetBook) trackerFor(streamName string) *offsetTracker {
	b.mu.Lock()
	defer b.mu.Unlock()

	tracker, ok := b.trackers[streamName]
	if !ok {
		tracker = b.newTracker()
		b.trackers[streamName] = tracker
	}
	return tracker
}

// flush commits every stream's pending offset through the storer that reaches it,
// and reports the ones that failed rather than stopping at the first: a partition
// whose commit did not land must not cost the others theirs.
func (b *offsetBook) flush(storerFor func(streamName string) offsetStorer) []flushFailure {
	var failures []flushFailure
	for streamName, tracker := range b.snapshot() {
		if err := tracker.flush(storerFor(streamName)); err != nil {
			failures = append(failures, flushFailure{stream: streamName, err: err})
		}
	}
	return failures
}

// stored reports the last committed offset per stream, omitting the streams that
// have not committed one yet.
func (b *offsetBook) stored() map[string]int64 {
	offsets := make(map[string]int64)
	for streamName, tracker := range b.snapshot() {
		if offset, ok := tracker.lastStored(); ok {
			offsets[streamName] = offset
		}
	}
	return offsets
}

func (b *offsetBook) snapshot() map[string]*offsetTracker {
	b.mu.Lock()
	defer b.mu.Unlock()
	return maps.Clone(b.trackers)
}

// consumerRunner adapts one declared consumer to the stream client's callback.
type consumerRunner struct {
	name    string
	handler Handler
	offsets *offsetBook
	log     logger.Logger
	tracer  trace.Tracer

	// baseCtx is held rather than passed because the client's MessagesHandler
	// carries no context of its own. The manager cancels it in StopConsumers.
	baseCtx context.Context // NOSONAR S8242: no parameter to pass it through - the vendor callback signature is fixed
}

// deliver runs the handler for one message, then applies the commit policy.
// store is the consumer that delivered it, which is what the in-flight commit
// goes through.
//
// The client invokes this sequentially per STREAM — which for a super stream
// means per partition, where one runner serves every partition and the client
// calls it from one goroutine each, concurrently. The framework keeps that shape:
// handlers run inline with no worker pool, because a stream is an ordered log and
// parallelism *within* one would break that order and make a committed offset
// claim messages behind it were handled. Anything reachable from here that is not
// per-stream state must therefore be safe for concurrent use — the offset book
// is, precisely because it hands each stream its own tracker.
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

	if storeErr := r.offsets.trackerFor(streamName).record(offset, err, store); storeErr != nil {
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
