package streams

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/ha"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

// secondConsumerName is the second member of a two-declaration fan-out.
const secondConsumerName = testConsumerName + "-2"

// secondTestStream is the second target of a two-declaration publisher fan-out.
const secondTestStream = "shipments"

// errStartAttempted marks a consumer start that should never have been reached.
var errStartAttempted = errors.New("consumer start attempted")

func failingStart(*consumerDeclaration) error { return errStartAttempted }

// errBindAttempted marks a publisher bind that should never have been reached.
var errBindAttempted = errors.New("publisher bind attempted")

func failingBind(*publisherDeclaration) error { return errBindAttempted }

// errProducerConstruction is the client constructor's failure, faked.
var errProducerConstruction = errors.New("producer construction failed")

// fakeHandle stands in for *ha.ReliableConsumer so shutdown bookkeeping is
// testable without a broker.
type fakeHandle struct {
	mu       sync.Mutex
	events   []string
	status   int
	closeErr error
	storeErr error
	// onClose records the close in an order shared with other fakes, so the
	// shutdown SEQUENCE is assertable and not only its outcome.
	onClose func()
}

func (f *fakeHandle) StoreCustomOffset(offset int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.storeErr != nil {
		return f.storeErr
	}
	f.events = append(f.events, fmt.Sprintf("store:%d", offset))
	return nil
}

func (f *fakeHandle) Close() error {
	f.mu.Lock()
	f.events = append(f.events, "close")
	closeErr, onClose := f.closeErr, f.onClose
	f.mu.Unlock()

	if onClose != nil {
		onClose()
	}
	return closeErr
}

func (f *fakeHandle) GetStatus() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeHandle) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

// unreachableTestURI points at a port that refuses connections, so a dial fails fast.
const unreachableTestURI = "rabbitmq-stream://guest:guest@127.0.0.1:1/%2f"

// The shutdown and unwind warnings, verbatim. Tests assert on them so a guard
// that stops distinguishing success from failure fails here.
const (
	msgFlushFailed          = "Failed to flush stream offset on shutdown"
	msgCloseConsumerFailed  = "Failed to close stream consumer"
	msgCloseEnvFailed       = "Failed to close stream environment after a failed start"
	msgOffsetStoreFailed    = "Failed to store stream offset"
	msgOffsetQueryFailed    = "Could not query the stored stream offset; attaching at a position that replays rather than skips"
	msgFlushSkipped         = "Shutdown offset flush budget spent; offset not committed - handled messages will replay"
	msgClosePublisherFailed = "Failed to close stream publisher"
)

// recordingLogger captures each event's level, attached error and terminal
// message, so a test asserts what a shutdown actually told the operator instead
// of swapping the process-global os.Stdout.
type recordingLogger struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	l     *recordingLogger
	level string
	err   string
	msg   string
	// fields captures the string fields attached to the event, so a test can
	// assert WHICH stream a shutdown line named rather than only that it spoke.
	fields map[string]string
}

func (l *recordingLogger) event(level string) logger.LogEvent {
	return &recordedEvent{l: l, level: level}
}
func (l *recordingLogger) Info() logger.LogEvent                     { return l.event("info") }
func (l *recordingLogger) Error() logger.LogEvent                    { return l.event("error") }
func (l *recordingLogger) Debug() logger.LogEvent                    { return l.event("debug") }
func (l *recordingLogger) Warn() logger.LogEvent                     { return l.event("warn") }
func (l *recordingLogger) Fatal() logger.LogEvent                    { return l.event("fatal") }
func (l *recordingLogger) WithContext(_ any) logger.Logger           { return l }
func (l *recordingLogger) WithFields(_ map[string]any) logger.Logger { return l }

func (l *recordingLogger) messagesAt(level string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []string{}
	for _, e := range l.events {
		if e.level == level {
			out = append(out, e.msg)
		}
	}
	return out
}

func (l *recordingLogger) warnMessages() []string { return l.messagesAt("warn") }

// warnStreams reports the stream named by every WARN carrying msg, in order, so a
// test asserts which streams a shutdown accounted for rather than only how many
// lines it emitted.
func (l *recordingLogger) warnStreams(msg string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []string{}
	for _, e := range l.events {
		if e.level == "warn" && e.msg == msg {
			out = append(out, e.fields[logFieldStream])
		}
	}
	return out
}

// warnError reports the error text attached to the first WARN carrying msg. An
// empty string with ok=true means the line was emitted with no error at all,
// which is what a guard reading the wrong way round produces.
func (l *recordingLogger) warnError(msg string) (text string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.events {
		if e.level == "warn" && e.msg == msg {
			return e.err, true
		}
	}
	return "", false
}

func (e *recordedEvent) Msg(msg string) {
	e.msg = msg
	e.l.mu.Lock()
	e.l.events = append(e.l.events, *e)
	e.l.mu.Unlock()
}
func (e *recordedEvent) Msgf(format string, args ...any) { e.Msg(fmt.Sprintf(format, args...)) }
func (e *recordedEvent) Err(err error) logger.LogEvent {
	if err != nil {
		e.err = err.Error()
	}
	return e
}

func (e *recordedEvent) Str(key, value string) logger.LogEvent {
	if e.fields == nil {
		e.fields = map[string]string{}
	}
	e.fields[key] = value
	return e
}
func (e *recordedEvent) Int(_ string, _ int) logger.LogEvent           { return e }
func (e *recordedEvent) Int64(_ string, _ int64) logger.LogEvent       { return e }
func (e *recordedEvent) Uint64(_ string, _ uint64) logger.LogEvent     { return e }
func (e *recordedEvent) Dur(_ string, _ time.Duration) logger.LogEvent { return e }
func (e *recordedEvent) Interface(_ string, _ any) logger.LogEvent     { return e }
func (e *recordedEvent) Bytes(_ string, _ []byte) logger.LogEvent      { return e }
func (e *recordedEvent) Bool(_ string, _ bool) logger.LogEvent         { return e }
func (e *recordedEvent) Enabled() bool                                 { return true }

// recordingManager builds a manager with one attached fake consumer whose tracker
// already has a pending offset, so both shutdown guards are reachable.
func recordingManager(t *testing.T, handle *fakeHandle) (*Manager, *recordingLogger) {
	t.Helper()
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: log})
	tracker := newOffsetTracker(1000, time.Hour, nil)
	require.NoError(t, tracker.record(4, nil, &fakeStorer{}))
	attach(m, handle, tracker)
	return m, log
}

func testManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(ManagerOptions{
		URI:    "rabbitmq-stream://localhost:5552/%2f",
		Logger: logger.New("error", false),
	})
}

// attach wires a fake consumer into a manager as if Start had created it: one
// stream, whose flush target is the handle itself — the plain lane's shape.
func attach(m *Manager, handle *fakeHandle, tracker *offsetTracker) {
	book := bookOf(tracker)
	book.trackerFor(testStream)
	m.consumers = append(m.consumers, &runningConsumer{
		stream:    testStream,
		name:      testConsumerName,
		handle:    handle,
		offsets:   book,
		storerFor: func(string) offsetStorer { return handle },
	})
	m.started = true
}

// attachPartitioned wires one consumer that tracks two streams, the shape a super
// stream produces: one book, one flush target per partition.
// countBeforeStorage decides whether the two recorded offsets are still pending
// (a high threshold) or already committed (1).
func attachPartitioned(t *testing.T, m *Manager, countBeforeStorage int) (handle *fakeHandle, storer0, storer1 *fakeStorer) {
	t.Helper()
	handle = &fakeHandle{status: ha.StatusOpen}
	storer0, storer1 = &fakeStorer{}, &fakeStorer{}
	book := newOffsetBook(func() *offsetTracker { return newOffsetTracker(countBeforeStorage, time.Hour, nil) })
	require.NoError(t, book.trackerFor(testPartition0).record(11, nil, storer0))
	require.NoError(t, book.trackerFor(testPartition1).record(501, nil, storer1))

	m.consumers = append(m.consumers, &runningConsumer{
		stream:  testSuperStream,
		name:    testConsumerName,
		handle:  handle,
		offsets: book,
		storerFor: storerByStream(map[string]offsetStorer{
			testPartition0: storer0,
			testPartition1: storer1,
		}),
	})
	m.started = true
	return handle, storer0, storer1
}

// TestManagerStopConsumersFlushesEveryTrackedStream extends the shutdown flush to
// a consumer that tracks more than one stream: every partition's pending offset is
// committed through the storer that reaches it, and only then is the consumer
// closed.
func TestManagerStopConsumersFlushesEveryTrackedStream(t *testing.T) {
	m := testManager(t)
	handle, storer0, storer1 := attachPartitioned(t, m, 1000)
	require.Empty(t, storer0.offsets(), "the premise: both offsets are still pending")

	m.StopConsumers()

	assert.Equal(t, []int64{11}, storer0.offsets())
	assert.Equal(t, []int64{501}, storer1.offsets())
	assert.Equal(t, []string{"close"}, handle.recorded(), "the handle itself stores nothing")
	assert.Empty(t, m.consumers)
}

// blockingStorer stands in for a commit against a broker that is down. The
// client's locator reconnect loop has no attempt cap and no deadline, so such a
// call never returns on its own; release is how the test ends it once the
// assertions are made.
type blockingStorer struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingStorer() *blockingStorer {
	return &blockingStorer{entered: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingStorer) StoreCustomOffset(int64) error {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return nil
}

// TestManagerStopConsumersAbandonsFlushWhenBudgetSpent pins the shutdown flush
// budget. A super stream commits through the environment's locator, and every
// locator call starts with a reconnect loop the client gives no deadline, so a
// broker that is down makes the commit hang forever. Without a budget that hang
// happens while stopLocked holds m.mu, which takes Ready() and Stats() — and with
// them /ready — down for the whole of a pod drain.
//
// The first consumer's commit never returns. What is asserted is that shutdown
// completes anyway, that the consumer BEHIND it is not put through a budget
// already spent, and that both are named at WARN so the replay is not silent.
func TestManagerStopConsumersAbandonsFlushWhenBudgetSpent(t *testing.T) {
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: log})
	m.flushBudget = 50 * time.Millisecond

	blocked := newBlockingStorer()
	defer close(blocked.release)

	stuck := &fakeHandle{status: ha.StatusOpen}
	stuckBook := newOffsetBook(func() *offsetTracker { return newOffsetTracker(1000, time.Hour, nil) })
	require.NoError(t, stuckBook.trackerFor(testPartition0).record(11, nil, blocked))

	behind := &fakeHandle{status: ha.StatusOpen}
	behindStorer := &fakeStorer{}
	behindBook := newOffsetBook(func() *offsetTracker { return newOffsetTracker(1000, time.Hour, nil) })
	require.NoError(t, behindBook.trackerFor(testPartition1).record(501, nil, behindStorer))
	require.Empty(t, behindStorer.offsets(), "the premise: the second consumer's offset is still pending")

	m.consumers = append(m.consumers,
		&runningConsumer{
			stream: testSuperStream, name: testConsumerName, handle: stuck, offsets: stuckBook,
			storerFor: func(string) offsetStorer { return blocked },
		},
		&runningConsumer{
			stream: testStream, name: secondConsumerName, handle: behind, offsets: behindBook,
			storerFor: func(string) offsetStorer { return behindStorer },
		},
	)
	m.started = true

	returned := make(chan struct{})
	go func() {
		m.StopConsumers()
		close(returned)
	}()

	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the blocked commit was never attempted, so this test is not exercising the hang it claims to")
	}

	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("StopConsumers never returned: the flush budget did not bound a commit that cannot finish")
	}

	assert.Empty(t, behindStorer.offsets(),
		"a budget already spent must skip the remaining flush outright, not attempt one more")
	assert.Equal(t, []string{testSuperStream, testStream}, log.warnStreams(msgFlushSkipped),
		"every skipped flush is reported at WARN, naming its stream")
	assert.Equal(t, []string{"close"}, stuck.recorded(),
		"an abandoned flush must still let its consumer be closed")
	assert.Equal(t, []string{"close"}, behind.recorded())
	assert.Empty(t, m.consumers)
	assert.False(t, m.started)
}

// TestManagerStopConsumersFlushesWithinBudget is the other half of the budget:
// with the broker answering, every offset is still committed and nothing is
// skipped. Without it, a budget wired to skip unconditionally would pass the
// abandonment test above.
func TestManagerStopConsumersFlushesWithinBudget(t *testing.T) {
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: log})
	handle, storer0, storer1 := attachPartitioned(t, m, 1000)

	m.StopConsumers()

	assert.Equal(t, []int64{11}, storer0.offsets())
	assert.Equal(t, []int64{501}, storer1.offsets())
	assert.Empty(t, log.warnStreams(msgFlushSkipped), "a flush that lands well inside the budget skips nothing")
	assert.Equal(t, []string{"close"}, handle.recorded())
}

// TestManagerStatsKeysOffsetsByTrackedStream pins the /ready body's key: a position
// is reported under the stream it belongs to, which for a super stream is the
// partition rather than the declared name.
func TestManagerStatsKeysOffsetsByTrackedStream(t *testing.T) {
	m := testManager(t)
	attachPartitioned(t, m, 1)

	stats := m.Stats()

	assert.Equal(t, map[string]int64{
		testPartition0 + "/" + testConsumerName: 11,
		testPartition1 + "/" + testConsumerName: 501,
	}, stats["stored_offsets"])
	assert.Equal(t, 1, stats["consumers"], "the two partitions are one consumer")
}

func TestNewManagerAppliesOffsetStoreDefaults(t *testing.T) {
	m := NewManager(ManagerOptions{URI: "rabbitmq-stream://localhost:5552/", Logger: logger.New("error", false)})

	assert.Equal(t, defaultOffsetStoreCount, m.opts.OffsetStoreCount)
	assert.Equal(t, defaultOffsetStoreInterval, m.opts.OffsetStoreInterval)
}

func TestNewManagerKeepsExplicitOffsetStoreTuning(t *testing.T) {
	m := NewManager(ManagerOptions{
		OffsetStoreCount:    7,
		OffsetStoreInterval: 250 * time.Millisecond,
		Logger:              logger.New("error", false),
	})

	assert.Equal(t, 7, m.opts.OffsetStoreCount)
	assert.Equal(t, 250*time.Millisecond, m.opts.OffsetStoreInterval)
}

// TestNewManagerRequiresLogger pins the constructor guard: ManagerOptions.Logger
// is documented as required and every log call dereferences it unguarded, so an
// omitted logger must fail here rather than on the first consumer event.
func TestNewManagerRequiresLogger(t *testing.T) {
	assert.PanicsWithValue(t,
		"streams: NewManager requires a non-nil Logger (pass deps.Logger)",
		func() { NewManager(ManagerOptions{URI: unreachableTestURI}) })
}

func TestManagerStartWithoutDeclarationsDoesNotDial(t *testing.T) {
	// Port 1 refuses connections, so any dial attempt would surface as an error.
	m := NewManager(ManagerOptions{URI: "rabbitmq-stream://guest:guest@127.0.0.1:1/%2f", Logger: logger.New("error", false)})

	require.NoError(t, m.Start(context.Background(), nil))
	require.NoError(t, m.Start(context.Background(), NewDeclarations()))

	assert.Nil(t, m.env)
	assert.False(t, m.started)
}

// The environment is installed alongside the consumer because Start sets it
// before it sets started; attach alone would leave a state Start never produces.
// The URI is unreachable so a guard that stopped holding surfaces as a dial
// failure the message assertion rejects, rather than as a lucky local broker.
func TestManagerStartRejectsSecondStart(t *testing.T) {
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: logger.New("error", false)})
	attach(m, &fakeHandle{status: ha.StatusOpen}, newOffsetTracker(1, time.Hour, nil))
	m.env = &stream.Environment{}

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)

	err := m.Start(context.Background(), decls)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

// TestManagerStartupAbortsOnACanceledContext pins that a canceled startup stops
// every one of Start's fan-outs before it reaches the broker, and names the phase
// it stopped in. The environment is nil, so any call that got as far as the client
// would panic rather than return.
func TestManagerStartupAbortsOnACanceledContext(t *testing.T) {
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareSuperStream(testSuperStream, 2, nil)
	decls.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: testConsumerName, Handler: noopHandler})
	decls.DeclarePublisher(&PublisherOptions{Stream: testStream})

	tests := []struct {
		name      string
		phase     func(m *Manager, ctx context.Context) error
		wantPhase string
	}{
		{
			name:      "plain_streams",
			phase:     func(m *Manager, ctx context.Context) error { return m.declareStreams(ctx, nil, decls) },
			wantPhase: `declaring stream "` + testStream + `"`,
		},
		{
			name:      "super_streams",
			phase:     func(m *Manager, ctx context.Context) error { return m.declareSuperStreams(ctx, nil, decls) },
			wantPhase: `declaring super stream "` + testSuperStream + `"`,
		},
		{
			// Same shape as the consumer case below: the binder fails loudly, so a
			// guard that let the loop reach it would surface errBindAttempted here
			// instead of the cancellation.
			name: "publishers",
			phase: func(_ *Manager, ctx context.Context) error {
				return bindPublishers(ctx, decls.publishers, failingBind)
			},
			wantPhase: `binding the publisher on stream "` + testStream + `"`,
		},
		{
			// The starter fails loudly, so a guard that let the loop reach it would
			// surface errStartAttempted here instead of the cancellation.
			name: "consumers",
			phase: func(_ *Manager, ctx context.Context) error {
				return startConsumers(ctx, decls.consumers, failingStart)
			},
			wantPhase: `starting consumer "` + testConsumerName + `"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testManager(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			var err error
			require.NotPanics(t, func() { err = tt.phase(m, ctx) })

			assert.ErrorIs(t, err, context.Canceled)
			assert.Contains(t, err.Error(), tt.wantPhase,
				"the caller must be told which startup phase the cancellation stopped")
		})
	}
}

// A live context must not short-circuit the fan-out: the nil environment proves
// the broker call is attempted by panicking, which the guard would have prevented.
func TestManagerDeclareProceedsOnALiveContext(t *testing.T) {
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	m := testManager(t)

	assert.Panics(t, func() { _ = m.declareStreams(context.Background(), nil, decls) })
}

// twoConsumers declares a pair so the fan-out has a second iteration to reach —
// with one declaration, stopping early and running to completion look identical.
func twoConsumers(t *testing.T) *Declarations {
	t.Helper()
	decls := NewDeclarations()
	decls.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: testConsumerName, Handler: noopHandler})
	decls.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: secondConsumerName, Handler: noopHandler})
	return decls
}

// TestStartConsumersStartsEveryDeclaration pins that a live context does not
// short-circuit the fan-out: every declaration is started, in order.
func TestStartConsumersStartsEveryDeclaration(t *testing.T) {
	decls := twoConsumers(t)

	var started []string
	err := startConsumers(context.Background(), decls.consumers, func(decl *consumerDeclaration) error {
		started = append(started, decl.Name)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, []string{testConsumerName, secondConsumerName}, started,
		"a live context starts every consumer, not just the first")
}

// TestStartConsumersStopsAtTheFirstFailure pins the other half: a start that fails
// reaches the caller unchanged — Start turns it into an aborted startup — and the
// declarations behind it are never attempted.
func TestStartConsumersStopsAtTheFirstFailure(t *testing.T) {
	decls := twoConsumers(t)

	var attempted []string
	err := startConsumers(context.Background(), decls.consumers, func(decl *consumerDeclaration) error {
		attempted = append(attempted, decl.Name)
		return errStartAttempted
	})

	require.ErrorIs(t, err, errStartAttempted)
	assert.Equal(t, []string{testConsumerName}, attempted, "the fan-out stops at the first failure")
}

func TestManagerStopConsumersFlushesBeforeClosing(t *testing.T) {
	m := testManager(t)
	handle := &fakeHandle{status: ha.StatusOpen}
	tracker := newOffsetTracker(1000, time.Hour, nil)
	require.NoError(t, tracker.record(88, nil, &fakeStorer{}))
	attach(m, handle, tracker)

	m.StopConsumers()

	assert.Equal(t, []string{"store:88", "close"}, handle.recorded(),
		"a clean shutdown commits the last handled offset before the consumer goes away")
	assert.False(t, m.started)
	assert.Empty(t, m.consumers)
}

func TestManagerStopConsumersIsIdempotent(t *testing.T) {
	m := testManager(t)
	handle := &fakeHandle{status: ha.StatusOpen}
	attach(m, handle, newOffsetTracker(1000, time.Hour, nil))

	m.StopConsumers()
	m.StopConsumers()

	assert.Equal(t, []string{"close"}, handle.recorded(), "nothing pending, and no second close")
}

// TestManagerStopConsumersToleratesFlushAndCloseErrors pins both shutdown
// guards from the failing side: a flush that never reached the broker and a
// consumer that would not close each surface to the operator with the underlying
// error attached, and neither aborts the rest of the teardown.
func TestManagerStopConsumersToleratesFlushAndCloseErrors(t *testing.T) {
	m, log := recordingManager(t, &fakeHandle{
		status:   ha.StatusOpen,
		closeErr: errors.New("close failed"),
		storeErr: errors.New("store failed"),
	})

	assert.NotPanics(t, m.StopConsumers)
	assert.False(t, m.started)

	assert.ElementsMatch(t, []string{msgFlushFailed, msgCloseConsumerFailed}, log.warnMessages())
	flushErr, ok := log.warnError(msgFlushFailed)
	require.True(t, ok)
	assert.Equal(t, "store failed", flushErr)
	closeErr, ok := log.warnError(msgCloseConsumerFailed)
	require.True(t, ok)
	assert.Equal(t, "close failed", closeErr)
}

// TestManagerStopConsumersIsSilentOnCleanShutdown is the other side of the same
// two guards: a teardown whose flush and close both succeed reports no failure.
func TestManagerStopConsumersIsSilentOnCleanShutdown(t *testing.T) {
	handle := &fakeHandle{status: ha.StatusOpen}
	m, log := recordingManager(t, handle)

	m.StopConsumers()

	assert.Equal(t, []string{"store:4", "close"}, handle.recorded())
	assert.Empty(t, log.warnMessages(), "a clean shutdown reports nothing to the operator")
}

func TestManagerStopConsumersCancelsConsumeContext(t *testing.T) {
	m := testManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	attach(m, &fakeHandle{status: ha.StatusOpen}, newOffsetTracker(1, time.Hour, nil))

	m.StopConsumers()

	require.Error(t, ctx.Err())
	assert.Nil(t, m.cancel)
}

// TestConsumeContextInheritsValuesWithoutCancellation is the first half of the
// consume-context contract: the caller's context contributes its values and none
// of its cancellation, so a startup context that is canceled once Start returned
// cannot silently stop consumption.
func TestConsumeContextInheritsValuesWithoutCancellation(t *testing.T) {
	type tenantKey struct{}
	parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), tenantKey{}, "tenant-7"))
	consumeCtx, cancel := consumeContext(parent)
	defer cancel()

	cancelParent()

	require.NoError(t, consumeCtx.Err(), "the caller's cancellation must not reach the consumers")
	assert.Equal(t, "tenant-7", consumeCtx.Value(tenantKey{}),
		"the caller's values must reach the handlers' logs and spans")
}

// TestManagerStopConsumersStopsDetachedConsumeContext is the second half:
// severing the caller's cancellation must not cost StopConsumers its own.
func TestManagerStopConsumersStopsDetachedConsumeContext(t *testing.T) {
	m := testManager(t)
	parent, cancelParent := context.WithCancel(context.Background())
	consumeCtx, cancel := consumeContext(parent)
	m.cancel = cancel
	attach(m, &fakeHandle{status: ha.StatusOpen}, newOffsetTracker(1, time.Hour, nil))

	cancelParent()
	require.NoError(t, consumeCtx.Err(), "the premise: the caller's context is canceled first")

	m.StopConsumers()

	require.ErrorIs(t, consumeCtx.Err(), context.Canceled)
	assert.Nil(t, m.cancel)
	assert.False(t, m.started)
}

func TestManagerCloseWithoutEnvironmentIsIdempotent(t *testing.T) {
	m := testManager(t)

	require.NoError(t, m.Close())
	require.NoError(t, m.Close())
}

func TestManagerStats(t *testing.T) {
	m := NewManager(ManagerOptions{
		URI:                 "rabbitmq-stream://localhost:5552/",
		OffsetStoreCount:    9,
		OffsetStoreInterval: 2 * time.Second,
		Logger:              logger.New("error", false),
	})
	tracker := newOffsetTracker(1, time.Hour, nil)
	require.NoError(t, tracker.record(31, nil, &fakeStorer{}))
	attach(m, &fakeHandle{status: ha.StatusOpen}, tracker)

	stats := m.Stats()

	assert.Equal(t, true, stats["started"])
	assert.Equal(t, 1, stats["consumers"])
	assert.Equal(t, true, stats["ready"])
	assert.Equal(t, map[string]int64{testStream + "/" + testConsumerName: 31}, stats["stored_offsets"])
	assert.Equal(t, 9, stats["offset_store_count"])
	assert.Equal(t, "2s", stats["offset_flush_interval"])
}

func TestManagerStatsOmitsUncommittedOffsets(t *testing.T) {
	m := testManager(t)
	attach(m, &fakeHandle{status: ha.StatusOpen}, newOffsetTracker(1000, time.Hour, nil))

	stats := m.Stats()

	assert.Empty(t, stats["stored_offsets"])
}

func TestManagerReady(t *testing.T) {
	tests := []struct {
		name    string
		started bool
		status  int
		want    bool
	}{
		{name: "open_consumer_is_ready", started: true, status: ha.StatusOpen, want: true},
		{name: "reconnecting_consumer_is_not_ready", started: true, status: ha.StatusReconnecting, want: false},
		{name: "closed_consumer_is_not_ready", started: true, status: ha.StatusClosed, want: false},
		{name: "never_started_is_not_ready", started: false, status: ha.StatusOpen, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testManager(t)
			attach(m, &fakeHandle{status: tt.status}, newOffsetTracker(1, time.Hour, nil))
			m.started = tt.started

			assert.Equal(t, tt.want, m.Ready())
			assert.Equal(t, tt.want, m.Stats()["ready"])
		})
	}
}

// TestOffsetSpecFor covers the four ways a position is chosen. Only a MISSING
// offset may fall back to the declared start: any other query failure answered
// with a start position would skip — fatally so for the zero-value OffsetNext,
// which attaches past everything written since the last commit, and streams have
// no redelivery to get it back.
func TestOffsetSpecFor(t *testing.T) {
	tests := []struct {
		name        string
		stored      int64
		queryErr    error
		start       OffsetStart
		localOffset int64
		hasLocal    bool
		want        stream.OffsetSpecification
	}{
		{
			name:   "stored_offset_resumes_one_past_it",
			stored: 17,
			start:  OffsetFirst(),
			want:   stream.OffsetSpecification{}.Offset(18),
		},
		{
			name:     "no_stored_offset_uses_declared_start",
			queryErr: stream.OffsetNotFoundError,
			start:    OffsetFirst(),
			want:     stream.OffsetSpecification{}.First(),
		},
		{
			name:        "query_failure_resumes_from_the_local_commit",
			queryErr:    errors.New("boom"),
			start:       OffsetNext(),
			localOffset: 41,
			hasLocal:    true,
			want:        stream.OffsetSpecification{}.Offset(42),
		},
		{
			name:     "query_failure_without_a_local_commit_replays_from_first",
			queryErr: errors.New("boom"),
			start:    OffsetNext(),
			want:     stream.OffsetSpecification{}.First(),
		},
		{
			name:     "query_failure_never_answers_with_the_declared_start",
			queryErr: errors.New("boom"),
			start:    OffsetLast(),
			want:     stream.OffsetSpecification{}.First(),
		},
		{
			name:        "a_stored_offset_still_wins_over_the_local_commit",
			stored:      90,
			localOffset: 5,
			hasLocal:    true,
			start:       OffsetFirst(),
			want:        stream.OffsetSpecification{}.Offset(91),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, offsetSpecFor(tt.stored, tt.queryErr, tt.start, tt.localOffset, tt.hasLocal))
		})
	}
}

// TestManagerReportOffsetQuery pins the level and the silence either side of it: a
// failed query changes where a consumer attaches, so it is an ERROR, while the
// routine missing-offset case must not add noise to every first run.
func TestManagerReportOffsetQuery(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantError []string
	}{
		{name: "successful_query_is_silent", err: nil},
		{name: "missing_offset_is_silent", err: stream.OffsetNotFoundError},
		{name: "query_failure_is_reported_at_error", err: errors.New("boom"), wantError: []string{msgOffsetQueryFailed}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &recordingLogger{}
			m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: log})

			m.reportOffsetQuery(tt.err, testConsumerName, testPartition0, false)

			assert.ElementsMatch(t, tt.wantError, log.messagesAt("error"))
			assert.Empty(t, log.messagesAt("warn"), "a lost position is not a warning")
		})
	}
}

func TestStreamOptionsFrom(t *testing.T) {
	assert.Equal(t, stream.NewStreamOptions(), streamOptionsFrom(nil))
	assert.Equal(t, stream.NewStreamOptions(), streamOptionsFrom(&StreamSpec{}),
		"a zero spec leaves every retention knob to the broker")

	opts := streamOptionsFrom(&StreamSpec{
		MaxAge:              45 * time.Minute,
		MaxLengthBytes:      2048,
		MaxSegmentSizeBytes: 1024,
	})

	require.NotNil(t, opts)
	assert.Equal(t, 45*time.Minute, opts.MaxAge)
	assert.Equal(t, stream.ByteCapacity{}.B(2048), opts.MaxLengthBytes)
	assert.Equal(t, stream.ByteCapacity{}.B(1024), opts.MaxSegmentSizeBytes)
}

// TestStreamOptionsFromClampsMaxAge pins the retention rendering against
// messaging.StreamQueueSpec: truncate toward whole seconds, floor a non-zero
// value at 1s, and leave zero alone so the broker default still applies. The
// client formats MaxAge with %.0f (round to nearest), so 1500ms would reach the
// broker as 2s here and 1s in the AMQP lane without the clamp.
func TestStreamOptionsFromClampsMaxAge(t *testing.T) {
	tests := []struct {
		name   string
		maxAge time.Duration
		want   time.Duration
	}{
		{name: "sub_second_floors_to_one_second", maxAge: 500 * time.Millisecond, want: time.Second},
		{name: "nanosecond_floors_to_one_second", maxAge: time.Nanosecond, want: time.Second},
		{name: "fractional_second_truncates_down", maxAge: 1500 * time.Millisecond, want: time.Second},
		{name: "whole_seconds_pass_through", maxAge: 90 * time.Second, want: 90 * time.Second},
		{name: "zero_emits_no_max_age", maxAge: 0, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := streamOptionsFrom(&StreamSpec{MaxAge: tt.maxAge, MaxLengthBytes: 2048})

			assert.Equal(t, tt.want, opts.MaxAge)
			assert.Equal(t, stream.ByteCapacity{}.B(2048), opts.MaxLengthBytes,
				"the MaxAge clamp must not disturb the other retention knobs")
		})
	}
}

func TestPartitionOptionsFrom(t *testing.T) {
	assert.Equal(t, stream.NewPartitionsOptions(testPartitions), partitionOptionsFrom(testPartitions, nil))
	assert.Equal(t, stream.NewPartitionsOptions(testPartitions), partitionOptionsFrom(testPartitions, &StreamSpec{}),
		"a zero spec leaves every retention knob to the broker")

	opts := partitionOptionsFrom(5, &StreamSpec{
		MaxAge:              45 * time.Minute,
		MaxLengthBytes:      2048,
		MaxSegmentSizeBytes: 1024,
	})

	require.NotNil(t, opts)
	assert.Equal(t, 5, opts.Partitions)
	assert.Equal(t, 45*time.Minute, opts.MaxAge)
	assert.Equal(t, stream.ByteCapacity{}.B(2048), opts.MaxLengthBytes)
	assert.Equal(t, stream.ByteCapacity{}.B(1024), opts.MaxSegmentSizeBytes)
}

// TestPartitionOptionsFromClampsMaxAge repeats the plain lane's clamp on the
// super-stream renderer, which needs it more: it formats MaxAge with
// int(MaxAge.Seconds()), so an unclamped sub-second value would reach the broker as
// "0s" and silently disable the retention the caller declared.
func TestPartitionOptionsFromClampsMaxAge(t *testing.T) {
	tests := []struct {
		name   string
		maxAge time.Duration
		want   time.Duration
	}{
		{name: "sub_second_floors_to_one_second", maxAge: 500 * time.Millisecond, want: time.Second},
		{name: "nanosecond_floors_to_one_second", maxAge: time.Nanosecond, want: time.Second},
		{name: "fractional_second_truncates_down", maxAge: 1500 * time.Millisecond, want: time.Second},
		{name: "whole_seconds_pass_through", maxAge: 90 * time.Second, want: 90 * time.Second},
		{name: "zero_emits_no_max_age", maxAge: 0, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := partitionOptionsFrom(testPartitions, &StreamSpec{MaxAge: tt.maxAge, MaxLengthBytes: 2048})

			assert.Equal(t, tt.want, opts.MaxAge)
			assert.Equal(t, testPartitions, opts.Partitions, "the MaxAge clamp must not disturb the partition count")
			assert.Equal(t, stream.ByteCapacity{}.B(2048), opts.MaxLengthBytes,
				"the MaxAge clamp must not disturb the other retention knobs")
		})
	}
}

// TestClampedMaxAgeKeepsBothRenderersInAgreement is the property the shared clamp
// exists for: a stream and a super stream declared with the same retention must
// send the broker the same age, despite the client rounding one and truncating the
// other.
func TestClampedMaxAgeKeepsBothRenderersInAgreement(t *testing.T) {
	for _, maxAge := range []time.Duration{time.Nanosecond, 500 * time.Millisecond, 1500 * time.Millisecond, 90 * time.Second, time.Hour} {
		spec := &StreamSpec{MaxAge: maxAge}

		assert.Equal(t, streamOptionsFrom(spec).MaxAge, partitionOptionsFrom(testPartitions, spec).MaxAge,
			"both lanes must render %s identically", maxAge)
	}
}

func TestManagerEnvironmentOptions(t *testing.T) {
	t.Run("without_address_resolver", func(t *testing.T) {
		m := NewManager(ManagerOptions{URI: "rabbitmq-stream://localhost:5552/%2f", Logger: logger.New("error", false)})

		opts := m.environmentOptions()

		assert.Nil(t, opts.AddressResolver)
		require.Len(t, opts.ConnectionParameters, 1)
		assert.Equal(t, "rabbitmq-stream://localhost:5552/%2f", opts.ConnectionParameters[0].Uri)
	})

	t.Run("with_address_resolver", func(t *testing.T) {
		m := NewManager(ManagerOptions{
			URI:                 "rabbitmq-stream://localhost:5552/%2f",
			AddressResolverHost: "lb.example.com",
			AddressResolverPort: 5553,
			Logger:              logger.New("error", false),
		})

		opts := m.environmentOptions()

		require.NotNil(t, opts.AddressResolver)
		assert.Equal(t, "lb.example.com", opts.AddressResolver.Host)
		assert.Equal(t, 5553, opts.AddressResolver.Port)
	})
}

func TestRedactStreamURI(t *testing.T) {
	// Fixture value only — no real credential appears in this repository.
	const fixturePassword = "fixture-pw"

	tests := []struct {
		name string
		uri  string
		want string
	}{
		{
			name: "masks_password_keeps_username",
			uri:  "rabbitmq-stream://svc:" + fixturePassword + "@broker:5552/%2f",
			want: "rabbitmq-stream://svc:****@broker:5552/%2f",
		},
		{
			name: "masks_both_when_no_userinfo",
			uri:  "rabbitmq-stream://broker:5552/vhost",
			want: "rabbitmq-stream://****:****@broker:5552/vhost",
		},
		{
			name: "masks_query_string",
			uri:  "rabbitmq-stream://svc:" + fixturePassword + "@broker:5552/?token=abc",
			want: "rabbitmq-stream://svc:****@broker:5552/?<redacted>",
		},
		{
			name: "unparseable_uri_degrades_to_placeholder",
			uri:  "rabbitmq-stream://svc:" + fixturePassword + "@broker:55 52/%2f",
			want: redactedStreamURI,
		},
		{
			name: "empty_uri_degrades_to_placeholder",
			uri:  "",
			want: redactedStreamURI,
		},
		{
			name: "tls_scheme_is_preserved",
			uri:  "rabbitmq-stream+tls://svc:" + fixturePassword + "@broker:5551/%2f",
			want: "rabbitmq-stream+tls://svc:****@broker:5551/%2f",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactStreamURI(tt.uri)

			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, fixturePassword, "the password must never survive redaction")
		})
	}
}

// TestManagerStartDoesNotLeakURIOnParseFailure covers the path reachable when
// config.Validate never ran (app.NewWithConfig): the client returns a *url.Error
// whose Error() renders the raw URI, credentials included.
func TestManagerStartDoesNotLeakURIOnParseFailure(t *testing.T) {
	const fixturePassword = "fixture-pw"
	m := NewManager(ManagerOptions{
		// A space makes url.Parse fail inside the client's environment constructor.
		URI:    "rabbitmq-stream://svc:" + fixturePassword + "@broker:55 52/%2f",
		Logger: logger.New("error", false),
	})
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)

	err := m.Start(context.Background(), decls)

	require.Error(t, err)
	assert.NotContains(t, err.Error(), fixturePassword, "the credential must not survive into the error")
	assert.Contains(t, err.Error(), "invalid stream URI")
	assert.Contains(t, err.Error(), redactedStreamURI,
		"an unparseable endpoint degrades to the fixed placeholder rather than echoing the input")
	assert.False(t, m.started)
}

func TestSafeEnvError(t *testing.T) {
	// Not const: a constant expression here lets staticcheck evaluate the
	// deliberately-invalid URL and report SA1007 on the fixture itself.
	fixturePassword := "fixture-pw"
	raw := "rabbitmq-stream://svc:" + fixturePassword + "@broker:55 52/"
	_, parseErr := url.Parse(raw)
	require.Error(t, parseErr)
	require.Contains(t, parseErr.Error(), fixturePassword, "the premise: the vendor's error carries the credential")

	safe := safeEnvError(parseErr)

	assert.NotContains(t, safe.Error(), fixturePassword)
	assert.Contains(t, safe.Error(), "invalid stream URI")

	other := errors.New("connection refused")
	assert.Equal(t, other, safeEnvError(other), "non-URL errors pass through untouched")
}

// TestManagerStartFailedDialLeavesNothingToDispose is the pre-dial half: the
// environment was never stored, so Close is a no-op. The post-dial half — where
// the environment exists and must be disposed — needs a broker and lives in
// TestStreamsManagerDisposesEnvironmentOnDeclareFailureIntegration.
func TestManagerStartFailedDialLeavesNothingToDispose(t *testing.T) {
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: logger.New("error", false)})
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)

	require.Error(t, m.Start(context.Background(), decls))
	assert.Nil(t, m.env)
	require.NoError(t, m.Close(), "Close after a failed Start is a no-op")
}

// TestManagerAbortStartLockedDisposesEnvironment drives the post-dial unwind
// directly: stopLocked alone left m.env non-nil, so a caller that never called
// Close leaked it and a second Start orphaned it beyond any later Close.
func TestManagerAbortStartLockedDisposesEnvironment(t *testing.T) {
	m := testManager(t)
	handle := &fakeHandle{status: ha.StatusOpen}
	attach(m, handle, newOffsetTracker(1000, time.Hour, nil))

	m.abortStartLocked()

	assert.Nil(t, m.env, "the environment is disposed, not just the consumers")
	assert.Empty(t, m.consumers)
	assert.False(t, m.started)
	assert.Equal(t, []string{"close"}, handle.recorded())

	require.NoError(t, m.Close(), "the follow-up Close short-circuits instead of closing twice")
}

// TestManagerAbortStartLockedReportsOnlyRealDisposalFailures pins the unwind's
// own guard: nothing was dialed, so closeEnvLocked has nothing to close and
// returns nil. A guard reading that the wrong way round would tell the operator
// the environment failed to close on every successful unwind.
func TestManagerAbortStartLockedReportsOnlyRealDisposalFailures(t *testing.T) {
	m, log := recordingManager(t, &fakeHandle{status: ha.StatusOpen})

	m.abortStartLocked()

	require.Nil(t, m.env, "the premise: there was no environment to dispose")
	assert.NotContains(t, log.warnMessages(), msgCloseEnvFailed,
		"a disposal that succeeded is not reported as a failure")
	assert.Empty(t, log.warnMessages(), "the whole unwind is silent when every step succeeds")
}

// TestManagerStartRefusesRestartAfterStopConsumers is the restart variant of the
// environment-lifecycle class: stopLocked clears started but leaves m.env for
// Close, so a guard reading only started let a second Start dial over the live
// environment and orphan it beyond any later Close.
//
// The URI is unreachable on purpose: if the guard stopped holding, Start would
// reach the dial and report a connection failure instead, which is what the
// message assertion below separates from a genuine refusal.
func TestManagerStartRefusesRestartAfterStopConsumers(t *testing.T) {
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: logger.New("error", false)})
	handle := &fakeHandle{status: ha.StatusOpen}
	attach(m, handle, newOffsetTracker(1000, time.Hour, nil))
	env := &stream.Environment{}
	m.env = env

	m.StopConsumers()
	require.False(t, m.started, "the premise: StopConsumers clears started")
	require.Same(t, env, m.env, "the premise: StopConsumers leaves the environment for Close")

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	err := m.Start(context.Background(), decls)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started",
		"the restart is refused by the guard, not by a failed dial")
	assert.Same(t, env, m.env, "the first environment is still the one Close disposes, not an orphan")
	assert.False(t, m.started)
}

func TestManagerCloseEnvLockedIsIdempotent(t *testing.T) {
	m := testManager(t)

	require.NoError(t, m.closeEnvLocked())
	require.NoError(t, m.closeEnvLocked())
	assert.Nil(t, m.env)
}

// attachPublisher wires a fake producer into a manager as if Start had bound it.
func attachPublisher(m *Manager, handle *fakeProducer) *Publisher {
	return rebindPublisher(m, newPublisher(testStream), handle)
}

// rebindPublisher binds an EXISTING publisher to a new producer, which is what a
// second Manager.Start does to the handle a module has held since declaration.
func rebindPublisher(m *Manager, p *Publisher, handle *fakeProducer) *Publisher {
	p.bind(handle)
	m.publishers = append(m.publishers, p)
	m.started = true
	return p
}

// twoPublishers declares a pair so the fan-out has a second iteration to reach —
// with one declaration, stopping early and running to completion look identical.
func twoPublishers(t *testing.T) *Declarations {
	t.Helper()
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareStream(secondTestStream, nil)
	decls.DeclarePublisher(&PublisherOptions{Stream: testStream})
	decls.DeclarePublisher(&PublisherOptions{Stream: secondTestStream})
	return decls
}

// TestBindPublishersBindsEveryDeclaration pins that a live context does not
// short-circuit the fan-out: every declaration is bound, in order.
func TestBindPublishersBindsEveryDeclaration(t *testing.T) {
	decls := twoPublishers(t)

	var bound []string
	err := bindPublishers(context.Background(), decls.publishers, func(decl *publisherDeclaration) error {
		bound = append(bound, decl.Stream)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, []string{testStream, secondTestStream}, bound,
		"a live context binds every publisher, not just the first")
}

// TestBindPublishersStopsAtTheFirstFailure pins the other half: a bind that fails
// reaches the caller unchanged — Start turns it into an aborted startup — and the
// declarations behind it are never attempted.
func TestBindPublishersStopsAtTheFirstFailure(t *testing.T) {
	decls := twoPublishers(t)

	var attempted []string
	err := bindPublishers(context.Background(), decls.publishers, func(decl *publisherDeclaration) error {
		attempted = append(attempted, decl.Stream)
		return errBindAttempted
	})

	require.ErrorIs(t, err, errBindAttempted)
	assert.Equal(t, []string{testStream}, attempted, "the fan-out stops at the first failure")
}

// TestManagerBindPublisherWrapsAConstructionFailure exercises the vendor
// constructor's failure path through the producerFactory seam: it dials, so this
// is the only way a broker-free test can reach it.
func TestManagerBindPublisherWrapsAConstructionFailure(t *testing.T) {
	m := testManager(t)
	m.newProducer = func(*stream.Environment, string, ha.ConfirmMessageHandler) (producerHandle, error) {
		return nil, errProducerConstruction
	}
	decl := onePublisherDeclaration(t)

	err := m.bindPublisher(nil, decl)

	require.ErrorIs(t, err, errProducerConstruction, "the client's own cause reaches the caller")
	assert.Contains(t, err.Error(), `failed to start the publisher on stream "`+testStream+`"`)
	assert.Empty(t, m.publishers, "a publisher that could not be constructed is not tracked")
	assert.ErrorIs(t, decl.Publisher.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)}),
		ErrPublisherNotStarted, "and its handle stays unbound")
}

// TestManagerBindPublisherTracksAConstructedProducer is the success half: the
// producer is built for the declared stream, wired to that publisher's own
// confirmation handler, bound, and tracked.
func TestManagerBindPublisherTracksAConstructedProducer(t *testing.T) {
	m := testManager(t)
	handle := openProducer()
	var gotStream string
	var gotConfirmed ha.ConfirmMessageHandler
	m.newProducer = func(_ *stream.Environment, streamName string, confirmed ha.ConfirmMessageHandler) (producerHandle, error) {
		gotStream, gotConfirmed = streamName, confirmed
		return handle, nil
	}
	decl := onePublisherDeclaration(t)

	require.NoError(t, m.bindPublisher(nil, decl))

	assert.Equal(t, testStream, gotStream, "the producer is built for the declared stream")
	require.NotNil(t, gotConfirmed, "the client is given a confirmation handler")
	assert.Equal(t, []*Publisher{decl.Publisher}, m.publishers)
	assert.Equal(t, ha.StatusOpen, decl.Publisher.status(), "the handle is bound to the new producer")
}

// onePublisherDeclaration builds a single declared publisher for the bind tests.
func onePublisherDeclaration(t *testing.T) *publisherDeclaration {
	t.Helper()
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclarePublisher(&PublisherOptions{Stream: testStream})
	require.Len(t, decls.publishers, 1)
	return decls.publishers[0]
}

// TestManagerStopClosesPublishersAfterConsumers pins the shutdown order. A
// consumer handler may publish on its way out, so the producers have to outlive
// the consumers rather than the other way round.
func TestManagerStopClosesPublishersAfterConsumers(t *testing.T) {
	m := testManager(t)

	var order []string
	consumer := &fakeHandle{status: ha.StatusOpen, onClose: func() { order = append(order, "consumer") }}
	attach(m, consumer, newOffsetTracker(1000, time.Hour, nil))
	producer := openProducer()
	producer.onClose = func() { order = append(order, "publisher") }
	attachPublisher(m, producer)

	m.StopConsumers()

	assert.Equal(t, []string{"consumer", "publisher"}, order)
	assert.Empty(t, m.publishers)
	assert.False(t, m.started)
}

// TestManagerStopFailsAnInFlightPublish is why the close sweep is mandatory: this
// publish has no deadline of its own, and its send never returned, so without the
// sweep the caller would hang for the whole shutdown.
func TestManagerStopFailsAnInFlightPublish(t *testing.T) {
	m := testManager(t)
	producer := blockingProducer(t)
	p := attachPublisher(m, producer)

	done := publishAsync(context.Background(), p, &PublishMessage{Data: []byte(testBody)})
	waitForSend(t, producer)

	m.StopConsumers()

	require.ErrorIs(t, <-done, ErrPublisherClosed)
	assert.ErrorIs(t, p.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)}), ErrPublisherClosed,
		"the publisher stays closed for late callers")
}

func TestManagerStopReportsAPublisherCloseFailure(t *testing.T) {
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: log})
	producer := openProducer()
	producer.closeErr = errors.New("close failed")
	attachPublisher(m, producer)

	assert.NotPanics(t, m.StopConsumers)

	assert.Equal(t, []string{msgClosePublisherFailed}, log.warnMessages())
	closeErr, ok := log.warnError(msgClosePublisherFailed)
	require.True(t, ok)
	assert.Equal(t, "close failed", closeErr)
}

func TestManagerStopIsSilentOnACleanPublisherClose(t *testing.T) {
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: log})
	attachPublisher(m, openProducer())

	m.StopConsumers()

	assert.Empty(t, log.warnMessages(), "a clean publisher close reports nothing to the operator")
}

// TestManagerReadyRequiresEveryPublisher extends readiness to the publish side: a
// producer that is reconnecting cannot publish, so the probe must say so.
func TestManagerReadyRequiresEveryPublisher(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   bool
	}{
		{name: "open_publisher_is_ready", status: ha.StatusOpen, want: true},
		{name: "reconnecting_publisher_is_not_ready", status: ha.StatusReconnecting, want: false},
		{name: "closed_publisher_is_not_ready", status: ha.StatusClosed, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testManager(t)
			attachPublisher(m, &fakeProducer{status: tt.status})

			assert.Equal(t, tt.want, m.Ready())
		})
	}
}

func TestManagerStatsCountsPublishers(t *testing.T) {
	m := testManager(t)
	attachPublisher(m, openProducer())

	stats := m.Stats()

	assert.Equal(t, 1, stats["publishers"])
	assert.Equal(t, 0, stats["consumers"])
	assert.Equal(t, true, stats["ready"])
}

// TestManagerRebindRevivesAPublisherAfterAStopCycle covers the Start → Close →
// Start cycle Manager.Start allows: its guard is the environment, which Close
// nils, and consumers survive it because each Start rebuilds them. A publisher
// cannot be rebuilt — the module holds the same handle from declaration onwards —
// so the rebind has to reopen it or the second Start comes up publishing nothing.
func TestManagerRebindRevivesAPublisherAfterAStopCycle(t *testing.T) {
	m := testManager(t)
	first := openProducer()
	p := attachPublisher(m, first)

	m.StopConsumers()

	require.ErrorIs(t, p.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)}), ErrPublisherClosed)
	assert.False(t, m.Ready(), "a stopped manager is not ready")

	second := openProducer()
	rebindPublisher(m, p, second)

	assert.True(t, m.Ready(), "the second start comes up ready")
	publishConfirmed(t, p, second, &PublishMessage{Data: []byte(testBody)})
	assert.Zero(t, first.sentCount(), "the revived publisher sends through the new producer, not the disposed one")
}
