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

// The broker names a super stream's partitions "<name>-<index>".
const (
	testPartition0 = testSuperStream + "-0"
	testPartition1 = testSuperStream + "-1"
	testPartition2 = testSuperStream + "-2"
)

// fakeStorer records every offset handed to the broker, so tests assert exact
// stored values rather than "was called".
type fakeStorer struct {
	mu      sync.Mutex
	stored  []int64
	failNow bool
	// failErr, when set, is returned instead of the shared "store failed" error, so
	// a test running two failing storers can tell their failures apart.
	failErr error
}

func (f *fakeStorer) StoreCustomOffset(offset int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return f.failErr
	}
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
	return newTestRunnerWithLogger(t, handler, tracker, logger.New("error", false))
}

func newTestRunnerWithLogger(t *testing.T, handler Handler, tracker *offsetTracker, log logger.Logger) *consumerRunner {
	t.Helper()
	return &consumerRunner{
		name:    testConsumerName,
		handler: handler,
		offsets: bookOf(tracker),
		log:     log,
		tracer:  otel.Tracer(tracerName),
		baseCtx: context.Background(),
	}
}

// bookOf wraps one prepared tracker in a book that hands it out for every stream,
// which is how a plain single-stream consumer's bookkeeping behaves.
func bookOf(tracker *offsetTracker) *offsetBook {
	return newOffsetBook(func() *offsetTracker { return tracker })
}

func newTestRunnerWithBook(t *testing.T, handler Handler, book *offsetBook) *consumerRunner {
	t.Helper()
	return &consumerRunner{
		name:    testConsumerName,
		handler: handler,
		offsets: book,
		log:     logger.New("error", false),
		tracer:  otel.Tracer(tracerName),
		baseCtx: context.Background(),
	}
}

// storerByStream resolves a flush target per stream, standing in for the manager's
// runningConsumer.storerFor.
func storerByStream(storers map[string]offsetStorer) func(string) offsetStorer {
	return func(streamName string) offsetStorer { return storers[streamName] }
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

// TestOffsetTrackerReportsAMissingStorer pins the guard on both commit paths: a
// nil storer must surface as an error, never a nil dereference.
func TestOffsetTrackerReportsAMissingStorer(t *testing.T) {
	tests := []struct {
		name   string
		commit func(tracker *offsetTracker) error
	}{
		{
			name:   "record_reaching_the_count_threshold",
			commit: func(tracker *offsetTracker) error { return tracker.record(5, nil, nil) },
		},
		{
			name:   "flush_on_shutdown",
			commit: func(tracker *offsetTracker) error { return tracker.flush(nil) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := newFakeClock()
			tracker := newOffsetTracker(2, time.Hour, clock.Now)
			// Leaves one offset pending, so both paths reach the commit.
			require.NoError(t, tracker.record(4, nil, &fakeStorer{}))

			var err error
			require.NotPanics(t, func() { err = tt.commit(tracker) })

			require.ErrorIs(t, err, errNoOffsetStorer)
			_, ok := tracker.lastStored()
			assert.False(t, ok, "a refused commit stores no offset")
		})
	}
}

// TestOffsetBookFlushReportsAStreamWithNoStorer pins the book-level half of that
// guard, with a resolver no production path builds: one stream left unanswered,
// and the shutdown flush must name it among the failures while every other stream
// still commits, instead of taking the process down with it.
func TestOffsetBookFlushReportsAStreamWithNoStorer(t *testing.T) {
	clock := newFakeClock()
	book := newOffsetBook(func() *offsetTracker { return newOffsetTracker(1000, time.Hour, clock.Now) })
	landing := &fakeStorer{}
	require.NoError(t, book.trackerFor(testPartition0).record(7, nil, landing))
	require.NoError(t, book.trackerFor(testPartition1).record(42, nil, landing))

	failures := book.flush(storerByStream(map[string]offsetStorer{testPartition1: landing}))

	require.Len(t, failures, 1)
	assert.Equal(t, testPartition0, failures[0].stream)
	assert.ErrorIs(t, failures[0].err, errNoOffsetStorer)
	assert.Equal(t, []int64{42}, landing.offsets(), "the resolved partition still commits")
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

func TestOffsetBookKeepsOneTrackerPerStream(t *testing.T) {
	created := 0
	book := newOffsetBook(func() *offsetTracker {
		created++
		return newOffsetTracker(1, time.Hour, nil)
	})

	first := book.trackerFor(testPartition0)
	again := book.trackerFor(testPartition0)
	other := book.trackerFor(testPartition1)

	assert.Same(t, first, again, "a stream keeps its tracker across deliveries")
	assert.NotSame(t, first, other, "a second stream gets its own tracker")
	assert.Equal(t, 2, created, "trackers are created on first delivery, not per message")
}

// TestOffsetBookIsolatesPartitionOffsets is the reason the book exists. Two
// partitions are delivered alternately with a count threshold of 2: each commits
// its OWN second message. Sharing one tracker would instead commit on the second
// and fourth delivery overall, so the offsets would land on the wrong partitions.
func TestOffsetBookIsolatesPartitionOffsets(t *testing.T) {
	clock := newFakeClock()
	runner := newTestRunnerWithBook(t, func(context.Context, *Message) error { return nil },
		newOffsetBook(func() *offsetTracker { return newOffsetTracker(2, time.Hour, clock.Now) }))
	storer0, storer1 := &fakeStorer{}, &fakeStorer{}

	runner.deliver(testPartition0, 10, amqpMessage("a"), storer0)
	runner.deliver(testPartition1, 500, amqpMessage("b"), storer1)
	runner.deliver(testPartition0, 11, amqpMessage("c"), storer0)
	runner.deliver(testPartition1, 501, amqpMessage("d"), storer1)

	assert.Equal(t, []int64{11}, storer0.offsets(), "partition 0 commits its own second offset")
	assert.Equal(t, []int64{501}, storer1.offsets(), "partition 1 commits its own second offset")
	assert.Equal(t, map[string]int64{testPartition0: 11, testPartition1: 501}, runner.offsets.stored())
}

func TestOffsetBookFlushCommitsEveryStream(t *testing.T) {
	clock := newFakeClock()
	book := newOffsetBook(func() *offsetTracker { return newOffsetTracker(1000, time.Hour, clock.Now) })
	storer0, storer1 := &fakeStorer{}, &fakeStorer{}
	require.NoError(t, book.trackerFor(testPartition0).record(7, nil, storer0))
	require.NoError(t, book.trackerFor(testPartition1).record(42, nil, storer1))
	require.Empty(t, storer0.offsets(), "the premise: nothing committed before the flush")

	failures := book.flush(storerByStream(map[string]offsetStorer{
		testPartition0: storer0,
		testPartition1: storer1,
	}))

	assert.Empty(t, failures)
	assert.Equal(t, []int64{7}, storer0.offsets())
	assert.Equal(t, []int64{42}, storer1.offsets())
}

// TestOffsetBookFlushReportsEveryFailure pins that a failed commit does not stop
// the loop: with TWO of three partitions refusing, both are reported — not just
// whichever the flush reached first — each failure carries its own partition's
// error, and the healthy partition still commits. Two failures are what make this
// discriminating; a single one cannot tell "reports every failure" apart from
// "stops at the first". flush ranges over a map, so the report order is deliberately
// not asserted.
func TestOffsetBookFlushReportsEveryFailure(t *testing.T) {
	clock := newFakeClock()
	book := newOffsetBook(func() *offsetTracker { return newOffsetTracker(1000, time.Hour, clock.Now) })
	errPartition0, errPartition2 := errors.New("partition 0 store failed"), errors.New("partition 2 store failed")
	failing0, failing2 := &fakeStorer{failErr: errPartition0}, &fakeStorer{failErr: errPartition2}
	landing := &fakeStorer{}
	// Below the count threshold, so no commit is attempted before the flush.
	require.NoError(t, book.trackerFor(testPartition0).record(7, nil, failing0))
	require.NoError(t, book.trackerFor(testPartition1).record(42, nil, landing))
	require.NoError(t, book.trackerFor(testPartition2).record(99, nil, failing2))

	failures := book.flush(storerByStream(map[string]offsetStorer{
		testPartition0: failing0,
		testPartition1: landing,
		testPartition2: failing2,
	}))

	require.Len(t, failures, 2, "the first failure must not end the loop")
	reported := make([]string, 0, len(failures))
	errByStream := make(map[string]error, len(failures))
	for _, failure := range failures {
		reported = append(reported, failure.stream)
		errByStream[failure.stream] = failure.err
	}
	assert.ElementsMatch(t, []string{testPartition0, testPartition2}, reported,
		"both failing partitions are named, in whatever order the map yielded them")
	assert.ErrorIs(t, errByStream[testPartition0], errPartition0, "each failure carries its own partition's error")
	assert.ErrorIs(t, errByStream[testPartition2], errPartition2)
	assert.Equal(t, []int64{42}, landing.offsets(), "the healthy partition still commits")
}

func TestOffsetBookStoredOmitsStreamsWithoutACommit(t *testing.T) {
	clock := newFakeClock()
	book := newOffsetBook(func() *offsetTracker { return newOffsetTracker(1000, time.Hour, clock.Now) })
	storer := &fakeStorer{}
	book.trackerFor(testPartition0)
	require.NoError(t, book.trackerFor(testPartition1).record(9, nil, storer))
	require.NoError(t, book.trackerFor(testPartition1).flush(storer))

	assert.Equal(t, map[string]int64{testPartition1: 9}, book.stored(),
		"a partition that never committed contributes no position")
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

// TestRunnerDeliverSurvivesStoreFailure pins the commit guard from the failing
// side: consumption continues, nothing reached the broker, and the operator is
// told which offset did not land, with the store error attached.
func TestRunnerDeliverSurvivesStoreFailure(t *testing.T) {
	clock := newFakeClock()
	log := &recordingLogger{}
	runner := newTestRunnerWithLogger(t, func(context.Context, *Message) error { return nil },
		newOffsetTracker(1, time.Hour, clock.Now), log)
	storer := &fakeStorer{failNow: true}

	assert.NotPanics(t, func() {
		runner.deliver(testStream, 12, amqpMessage("payload"), storer)
	})

	assert.Empty(t, storer.offsets())
	assert.Equal(t, []string{msgOffsetStoreFailed}, log.warnMessages())
	storeErr, ok := log.warnError(msgOffsetStoreFailed)
	require.True(t, ok)
	assert.Equal(t, "store failed", storeErr)
}

// TestRunnerDeliverIsSilentWhenTheOffsetLands is the other side: the commit
// succeeded, so the delivery reports no store failure.
func TestRunnerDeliverIsSilentWhenTheOffsetLands(t *testing.T) {
	clock := newFakeClock()
	log := &recordingLogger{}
	runner := newTestRunnerWithLogger(t, func(context.Context, *Message) error { return nil },
		newOffsetTracker(1, time.Hour, clock.Now), log)
	storer := &fakeStorer{}

	runner.deliver(testStream, 12, amqpMessage("payload"), storer)

	assert.Equal(t, []int64{12}, storer.offsets())
	assert.Empty(t, log.warnMessages(), "a committed offset is not reported as a failure")
}

// TestRunnerDeliverHandlerErrorReportsNoStoreFailure covers the third path into
// the commit guard: a failed handler contributes no offset, so record returns nil
// and the delivery must not claim the offset failed to store. The handler failure
// itself is reported at ERROR.
func TestRunnerDeliverHandlerErrorReportsNoStoreFailure(t *testing.T) {
	clock := newFakeClock()
	log := &recordingLogger{}
	runner := newTestRunnerWithLogger(t, func(context.Context, *Message) error { return errHandlerFailed },
		newOffsetTracker(1, time.Hour, clock.Now), log)
	storer := &fakeStorer{}

	runner.deliver(testStream, 61, amqpMessage("payload"), storer)

	assert.Empty(t, storer.offsets())
	assert.Empty(t, log.warnMessages(),
		"the offset was never eligible for a commit, so nothing failed to store")
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
