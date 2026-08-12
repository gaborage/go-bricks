package streams

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/gaborage/go-bricks/logger"
)

var errHandlerFailed = errors.New("handler failed")

// fakeStorer records every offset handed to the broker, so tests assert exact
// stored values rather than "was called".
type fakeStorer struct {
	mu      sync.Mutex
	stored  []int64
	failNow bool
}

func (f *fakeStorer) StoreCustomOffset(offset int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNow {
		return errors.New("store failed")
	}
	f.stored = append(f.stored, offset)
	return nil
}

func (f *fakeStorer) offsets() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.stored...)
}

// fakeClock advances only when a test says so, so interval-triggered commits
// never depend on sleeping.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestRunner(t *testing.T, handler Handler, tracker *offsetTracker) *consumerRunner {
	t.Helper()
	return &consumerRunner{
		stream:  testStream,
		name:    testConsumerName,
		handler: handler,
		tracker: tracker,
		log:     logger.New("error", false),
		tracer:  otel.Tracer(tracerName),
		baseCtx: context.Background(),
	}
}

func amqpMessage(body string) *amqp.Message {
	return &amqp.Message{
		Data:                  [][]byte{[]byte(body)},
		ApplicationProperties: map[string]any{"kind": "test"},
	}
}

func TestOffsetTrackerStoresAfterCountThreshold(t *testing.T) {
	clock := newFakeClock()
	tracker := newOffsetTracker(3, time.Hour, clock.Now)
	storer := &fakeStorer{}

	for offset := int64(10); offset <= 12; offset++ {
		require.NoError(t, tracker.record(offset, nil, storer))
	}

	assert.Equal(t, []int64{12}, storer.offsets(), "exactly the third offset is committed")
}

func TestOffsetTrackerDoesNotStoreBeforeThreshold(t *testing.T) {
	clock := newFakeClock()
	tracker := newOffsetTracker(3, time.Hour, clock.Now)
	storer := &fakeStorer{}

	require.NoError(t, tracker.record(10, nil, storer))
	require.NoError(t, tracker.record(11, nil, storer))

	assert.Empty(t, storer.offsets())
}

func TestOffsetTrackerStoresAfterInterval(t *testing.T) {
	clock := newFakeClock()
	tracker := newOffsetTracker(1000, 5*time.Second, clock.Now)
	storer := &fakeStorer{}

	require.NoError(t, tracker.record(7, nil, storer))
	assert.Empty(t, storer.offsets(), "interval has not elapsed yet")

	clock.advance(5 * time.Second)
	require.NoError(t, tracker.record(8, nil, storer))

	assert.Equal(t, []int64{8}, storer.offsets())
}

func TestOffsetTrackerNeverStoresAFailedOffset(t *testing.T) {
	clock := newFakeClock()
	tracker := newOffsetTracker(1, time.Hour, clock.Now)
	storer := &fakeStorer{}

	require.NoError(t, tracker.record(41, errHandlerFailed, storer))

	assert.Empty(t, storer.offsets(), "a failed message must not advance the stored offset")
}

func TestOffsetTrackerLaterSuccessStoresHigherOffset(t *testing.T) {
	clock := newFakeClock()
	tracker := newOffsetTracker(1, time.Hour, clock.Now)
	storer := &fakeStorer{}

	require.NoError(t, tracker.record(5, nil, storer))
	require.NoError(t, tracker.record(6, errHandlerFailed, storer))
	require.NoError(t, tracker.record(7, nil, storer))

	assert.Equal(t, []int64{5, 7}, storer.offsets(), "the failed offset is skipped, not replayed")
}

func TestOffsetTrackerFlushCommitsPending(t *testing.T) {
	clock := newFakeClock()
	tracker := newOffsetTracker(1000, time.Hour, clock.Now)
	storer := &fakeStorer{}

	require.NoError(t, tracker.record(100, nil, storer))
	require.NoError(t, tracker.record(101, nil, storer))
	require.Empty(t, storer.offsets())

	require.NoError(t, tracker.flush(storer))

	assert.Equal(t, []int64{101}, storer.offsets(), "a clean stop commits the last handled offset")
}

func TestOffsetTrackerFlushWithNothingPendingIsNoop(t *testing.T) {
	clock := newFakeClock()
	tracker := newOffsetTracker(1, time.Hour, clock.Now)
	storer := &fakeStorer{}

	require.NoError(t, tracker.record(3, nil, storer))
	require.Equal(t, []int64{3}, storer.offsets())

	require.NoError(t, tracker.flush(storer))

	assert.Equal(t, []int64{3}, storer.offsets(), "flush must not re-commit an already stored offset")
}

func TestOffsetTrackerFlushAfterOnlyFailuresStoresNothing(t *testing.T) {
	clock := newFakeClock()
	tracker := newOffsetTracker(1000, time.Hour, clock.Now)
	storer := &fakeStorer{}

	require.NoError(t, tracker.record(9, errHandlerFailed, storer))
	require.NoError(t, tracker.flush(storer))

	assert.Empty(t, storer.offsets())
}

func TestOffsetTrackerRetriesAfterStoreFailure(t *testing.T) {
	clock := newFakeClock()
	tracker := newOffsetTracker(1, time.Hour, clock.Now)
	storer := &fakeStorer{failNow: true}

	require.Error(t, tracker.record(20, nil, storer))
	assert.Empty(t, storer.offsets())

	storer.failNow = false
	require.NoError(t, tracker.record(21, nil, storer))

	assert.Equal(t, []int64{21}, storer.offsets(), "the next message retries the commit")
}

func TestOffsetTrackerLastStored(t *testing.T) {
	clock := newFakeClock()
	tracker := newOffsetTracker(1, time.Hour, clock.Now)
	storer := &fakeStorer{}

	offset, ok := tracker.lastStored()
	assert.False(t, ok)
	assert.Equal(t, int64(0), offset)

	require.NoError(t, tracker.record(77, nil, storer))

	offset, ok = tracker.lastStored()
	assert.True(t, ok)
	assert.Equal(t, int64(77), offset)
}

func TestOffsetTrackerDefaultsToWallClock(t *testing.T) {
	tracker := newOffsetTracker(1, time.Hour, nil)

	require.NotNil(t, tracker.now)
	assert.WithinDuration(t, time.Now(), tracker.lastStoreAt, time.Minute)
}

func TestRunnerDeliverPassesMessageToHandler(t *testing.T) {
	clock := newFakeClock()
	var got *Message
	runner := newTestRunner(t, func(_ context.Context, msg *Message) error {
		got = msg
		return nil
	}, newOffsetTracker(1, time.Hour, clock.Now))
	storer := &fakeStorer{}

	runner.deliver(testStream, 55, amqpMessage("payload"), storer)

	require.NotNil(t, got)
	assert.Equal(t, []byte("payload"), got.Data)
	assert.Equal(t, int64(55), got.Offset)
	assert.Equal(t, testStream, got.Stream)
	assert.Equal(t, map[string]any{"kind": "test"}, got.Properties)
	assert.Equal(t, []int64{55}, storer.offsets())
}

func TestRunnerDeliverHandlerErrorSkipsOffsetCommit(t *testing.T) {
	clock := newFakeClock()
	runner := newTestRunner(t, func(context.Context, *Message) error {
		return errHandlerFailed
	}, newOffsetTracker(1, time.Hour, clock.Now))
	storer := &fakeStorer{}

	runner.deliver(testStream, 61, amqpMessage("payload"), storer)

	assert.Empty(t, storer.offsets())
}

func TestRunnerDeliverRecoversPanicAndContinues(t *testing.T) {
	clock := newFakeClock()
	handled := 0
	runner := newTestRunner(t, func(_ context.Context, msg *Message) error {
		handled++
		if msg.Offset == 1 {
			panic("boom")
		}
		return nil
	}, newOffsetTracker(1, time.Hour, clock.Now))
	storer := &fakeStorer{}

	assert.NotPanics(t, func() {
		runner.deliver(testStream, 1, amqpMessage("first"), storer)
		runner.deliver(testStream, 2, amqpMessage("second"), storer)
	})

	assert.Equal(t, 2, handled, "the stream continues after a panicking handler")
	assert.Equal(t, []int64{2}, storer.offsets(), "the panicking message's offset is never committed")
}

func TestRunnerInvokeWrapsPanicAsError(t *testing.T) {
	runner := newTestRunner(t, func(context.Context, *Message) error {
		panic("kaboom")
	}, newOffsetTracker(1, time.Hour, nil))

	err := runner.invoke(context.Background(), &Message{Stream: testStream, Offset: 3})

	require.Error(t, err)
	assert.Equal(t, "panic in stream handler: kaboom", err.Error())
}

func TestRunnerDeliverSurvivesStoreFailure(t *testing.T) {
	clock := newFakeClock()
	runner := newTestRunner(t, func(context.Context, *Message) error { return nil },
		newOffsetTracker(1, time.Hour, clock.Now))
	storer := &fakeStorer{failNow: true}

	assert.NotPanics(t, func() {
		runner.deliver(testStream, 12, amqpMessage("payload"), storer)
	})

	assert.Empty(t, storer.offsets())
}

func TestRunnerDeliverHandlesEmptyBody(t *testing.T) {
	clock := newFakeClock()
	var got *Message
	runner := newTestRunner(t, func(_ context.Context, msg *Message) error {
		got = msg
		return nil
	}, newOffsetTracker(1, time.Hour, clock.Now))

	runner.deliver(testStream, 1, &amqp.Message{}, &fakeStorer{})

	require.NotNil(t, got)
	assert.Nil(t, got.Data)
	assert.Nil(t, got.Properties)
}
