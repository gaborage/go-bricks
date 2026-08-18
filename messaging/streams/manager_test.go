package streams

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
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
	msgRoutingKeyMissing    = "No routing key registered for a super-stream message; it will be routed as if unkeyed"
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

// fieldAt reports the string field key attached to the first event at level
// carrying msg, so a test asserts WHICH subject a line named and not only that it
// spoke.
func (l *recordingLogger) fieldAt(level, msg, key string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.events {
		if e.level == level && e.msg == msg {
			return e.fields[key]
		}
	}
	return ""
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

// recordingManager starts a manager on fake with one plain consumer that has a
// pending, uncommitted offset, so both shutdown guards are reachable. The count
// threshold is deliberately high: the delivery must leave work for the shutdown
// flush to do rather than commit it inline.
func recordingManager(t *testing.T, fake *fakeEnvironment) (*Manager, *recordingLogger) {
	t.Helper()
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: log})
	startOnFake(t, m, fake, oneConsumerDecls())
	consumer := fake.consumer(testStream)
	require.NotNil(t, consumer)
	consumer.deliver(testStream, 4, amqpMessage("payload"))
	return m, log
}

// twoPartitionSuperConsumer starts m on a fresh fake with the super-stream
// consumer declared by superConsumerDecls, then delivers offset 11 on partition0
// and 501 on partition1, leaving both pending — the fixture shared by every test
// that needs a two-partition super consumer in that state.
func twoPartitionSuperConsumer(t *testing.T, m *Manager) (*fakeEnvironment, *fakeConsumer) {
	t.Helper()
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, superConsumerDecls())

	consumer := fake.consumer(testSuperStream)
	require.NotNil(t, consumer)
	consumer.deliver(testPartition0, 11, amqpMessage("a"))
	consumer.deliver(testPartition1, 501, amqpMessage("b"))
	return fake, consumer
}

func testManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(ManagerOptions{
		URI:    "rabbitmq-stream://localhost:5552/%2f",
		Logger: logger.New("error", false),
	})
}

// countOf reports how many times want appears in entries. assert.Contains alone
// cannot see a duplicate: a partition flushed twice still contains its entry
// once, so the shutdown-flush tests below count occurrences instead.
func countOf(entries []string, want string) int {
	n := 0
	for _, e := range entries {
		if e == want {
			n++
		}
	}
	return n
}

// TestManagerStopConsumersFlushesEveryTrackedStream extends the shutdown flush to
// a consumer that tracks more than one stream: every partition's pending offset is
// committed through the storer that reaches it, and only then is the consumer
// closed.
func TestManagerStopConsumersFlushesEveryTrackedStream(t *testing.T) {
	m := NewManager(ManagerOptions{
		URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: logger.New("error", false),
	})
	fake, consumer := twoPartitionSuperConsumer(t, m)
	require.NotContains(t, fake.recorded(), callStoreOffset+":"+testConsumerName+"/"+testPartition0+"=11",
		"the premise: both offsets are still pending")

	m.StopConsumers()

	recorded := fake.recorded()
	assert.Equal(t, 1, countOf(recorded, callStoreOffset+":"+testConsumerName+"/"+testPartition0+"=11"),
		"partition 0 flushed exactly once")
	assert.Equal(t, 1, countOf(recorded, callStoreOffset+":"+testConsumerName+"/"+testPartition1+"=501"),
		"partition 1 flushed exactly once")
	assert.Equal(t, []string{"close"}, consumer.events.recorded(), "the handle itself stores nothing")
	assert.Empty(t, m.consumers)
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
	m := NewManager(ManagerOptions{URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: log})
	m.flushBudget = 50 * time.Millisecond

	fake := newFakeEnvironment()
	entered, release := fake.blockStoreOn(testConsumerName + "/" + testPartition0)
	defer close(release)

	decls := NewDeclarations()
	decls.DeclareSuperStream(testSuperStream, testPartitions, nil)
	decls.DeclareStream(testStream, nil)
	decls.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
		SuperStream: testSuperStream, Name: testConsumerName, Handler: noopHandler,
	})
	decls.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: secondConsumerName, Handler: noopHandler})
	startOnFake(t, m, fake, decls)

	stuck := fake.consumer(testSuperStream)
	require.NotNil(t, stuck)
	stuck.deliver(testPartition0, 11, amqpMessage("a"))
	behind := fake.consumer(testStream)
	require.NotNil(t, behind)
	behind.deliver(testStream, 501, amqpMessage("b"))
	require.Empty(t, behind.events.recorded(), "the premise: the second consumer's offset is still pending")

	returned := make(chan struct{})
	go func() {
		m.StopConsumers()
		close(returned)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the blocked commit was never attempted, so this test is not exercising the hang it claims to")
	}

	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("StopConsumers never returned: the flush budget did not bound a commit that cannot finish")
	}

	assert.Equal(t, []string{"close"}, behind.events.recorded(),
		"a budget already spent must skip the remaining flush outright, not attempt one more")
	assert.Equal(t, []string{testSuperStream, testStream}, log.warnStreams(msgFlushSkipped),
		"every skipped flush is reported at WARN, naming its stream")
	assert.Equal(t, []string{"close"}, stuck.events.recorded(),
		"an abandoned flush must still let its consumer be closed")
	assert.Empty(t, m.consumers)
	assert.False(t, m.started)
}

// TestManagerStopConsumersFlushesWithinBudget is the other half of the budget:
// with the broker answering, every offset is still committed and nothing is
// skipped. Without it, a budget wired to skip unconditionally would pass the
// abandonment test above.
func TestManagerStopConsumersFlushesWithinBudget(t *testing.T) {
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: log})
	fake, consumer := twoPartitionSuperConsumer(t, m)

	m.StopConsumers()

	recorded := fake.recorded()
	assert.Equal(t, 1, countOf(recorded, callStoreOffset+":"+testConsumerName+"/"+testPartition0+"=11"),
		"partition 0 flushed exactly once")
	assert.Equal(t, 1, countOf(recorded, callStoreOffset+":"+testConsumerName+"/"+testPartition1+"=501"),
		"partition 1 flushed exactly once")
	assert.Empty(t, log.warnStreams(msgFlushSkipped), "a flush that lands well inside the budget skips nothing")
	assert.Equal(t, []string{"close"}, consumer.events.recorded())
}

// TestManagerStatsKeysOffsetsByTrackedStream pins the /ready body's key: a position
// is reported under the stream it belongs to, which for a super stream is the
// partition rather than the declared name.
func TestManagerStatsKeysOffsetsByTrackedStream(t *testing.T) {
	m := NewManager(ManagerOptions{
		URI: unreachableTestURI, OffsetStoreCount: 1, Logger: logger.New("error", false),
	})
	twoPartitionSuperConsumer(t, m)

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

// TestManagerStartDeclaresThroughTheEnvironmentPort is the whole point of the
// port: Start's declare phase reaches the broker seam in a unit test. Before the
// port there was no way in — the phase was only ever reached with a nil
// environment, by a test that asserted through a panic.
func TestManagerStartDeclaresThroughTheEnvironmentPort(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	dialFake(m, fake)

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)

	require.NoError(t, m.Start(context.Background(), decls))

	assert.Equal(t, []string{callDeclareStream + ":" + testStream}, fake.recorded())
	assert.True(t, m.started)
}

// The URI is unreachable so a guard that stopped holding surfaces as a dial
// failure the message assertion rejects, rather than as a lucky local broker.
func TestManagerStartRejectsSecondStart(t *testing.T) {
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: logger.New("error", false)})
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())

	err := m.Start(context.Background(), oneConsumerDecls())

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

// A live context must not short-circuit the fan-out. Before the Environment port
// this was asserted by panicking on a nil environment, which proved only that
// SOMETHING was dereferenced; now it names the call that reached the broker.
func TestManagerDeclareProceedsOnALiveContext(t *testing.T) {
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	m := testManager(t)
	fake := newFakeEnvironment()

	require.NoError(t, m.declareStreams(context.Background(), fake, decls))

	assert.Equal(t, []string{callDeclareStream + ":" + testStream}, fake.recorded())
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
	m := NewManager(ManagerOptions{
		URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: logger.New("error", false),
	})
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())
	consumer := fake.consumer(testStream)
	require.NotNil(t, consumer)
	consumer.deliver(testStream, 88, amqpMessage("payload"))

	m.StopConsumers()

	assert.Equal(t, []string{"store:88", "close"}, consumer.events.recorded(),
		"a clean shutdown commits the last handled offset before the consumer goes away")
	assert.False(t, m.started)
	assert.Empty(t, m.consumers)
}

func TestManagerStopConsumersIsIdempotent(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())
	consumer := fake.consumer(testStream)
	require.NotNil(t, consumer)

	m.StopConsumers()
	m.StopConsumers()

	assert.Equal(t, []string{"close"}, consumer.events.recorded(),
		"nothing pending, and no second close")
}

// TestManagerStopConsumersToleratesFlushAndCloseErrors pins both shutdown
// guards from the failing side: a flush that never reached the broker and a
// consumer that would not close each surface to the operator with the underlying
// error attached, and neither aborts the rest of the teardown.
func TestManagerStopConsumersToleratesFlushAndCloseErrors(t *testing.T) {
	fake := newFakeEnvironment()
	m, log := recordingManager(t, fake)
	consumer := fake.consumer(testStream)
	require.NotNil(t, consumer)
	handle := consumer.events
	handle.closeErr = errors.New("close failed")
	handle.storeErr = errors.New("store failed")

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
	fake := newFakeEnvironment()
	m, log := recordingManager(t, fake)
	consumer := fake.consumer(testStream)
	require.NotNil(t, consumer)

	m.StopConsumers()

	assert.Equal(t, []string{"store:4", "close"}, consumer.events.recorded())
	assert.Empty(t, log.warnMessages(), "a clean shutdown reports nothing to the operator")
}

// startWithCapturedHandlerCtx runs a real Start on parent with one consumer
// whose handler records the context it receives, delivers one message, and
// returns that context: the one Start actually built and handed down, not one a
// test wires up and assigns to m.cancel itself.
func startWithCapturedHandlerCtx(parent context.Context, t *testing.T, m *Manager, fake *fakeEnvironment) context.Context {
	t.Helper()
	var capturedCtx context.Context
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareConsumer(&ConsumerOptions{
		Stream: testStream, Name: testConsumerName,
		Handler: func(ctx context.Context, _ *Message) error {
			capturedCtx = ctx
			return nil
		},
	})
	dialFake(m, fake)
	require.NoError(t, m.Start(parent, decls))

	consumer := fake.consumer(testStream)
	require.NotNil(t, consumer)
	consumer.deliver(testStream, 4, amqpMessage("payload"))
	require.NotNil(t, capturedCtx, "the premise: the handler ran and captured the context it received")
	return capturedCtx
}

// TestManagerStopConsumersCancelsConsumeContext pins the context StopConsumers
// actually cancels: the one Start built and handed down to the handler.
func TestManagerStopConsumersCancelsConsumeContext(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	capturedCtx := startWithCapturedHandlerCtx(context.Background(), t, m, fake)

	m.StopConsumers()

	require.ErrorIs(t, capturedCtx.Err(), context.Canceled)
	assert.Nil(t, m.cancel)
}

// TestManagerStartRefusesACanceledContext pins the pre-dial gate: the dial is a
// blocking broker round trip the client gives no context for, so a caller that
// gave up before Start must not pay for it.
func TestManagerStartRefusesACanceledContext(t *testing.T) {
	m := testManager(t)
	dialed := false
	m.dialEnvironment = func(*stream.EnvironmentOptions) (environment, error) {
		dialed = true
		return newFakeEnvironment(), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := m.Start(ctx, oneConsumerDecls())

	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, dialed, "a canceled caller must not pay for the dial")
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
// severing the caller's cancellation must not cost StopConsumers its own. The
// consume context comes out of a real Start on a cancelable parent, so the
// cancel path under test is the one Start owns.
func TestManagerStopConsumersStopsDetachedConsumeContext(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	parent, cancelParent := context.WithCancel(context.Background())
	capturedCtx := startWithCapturedHandlerCtx(parent, t, m, fake)

	cancelParent()
	require.NoError(t, capturedCtx.Err(), "the premise: the caller's cancellation must not reach the consume context")

	m.StopConsumers()

	require.ErrorIs(t, capturedCtx.Err(), context.Canceled)
	assert.Nil(t, m.cancel)
	assert.False(t, m.started)
}

func TestManagerCloseWithoutEnvironmentIsIdempotent(t *testing.T) {
	m := testManager(t)

	require.NoError(t, m.Close())
	require.NoError(t, m.Close())
}

// TestManagerStats keeps offset_store_count at 9, distinct from the single
// declared consumer: a threshold of 1 would equal stats["consumers"] and hide a
// guard that swapped the two keys. Nine deliveries hit that threshold so the
// last one still commits inline, leaving stored_offsets non-empty below.
func TestManagerStats(t *testing.T) {
	m := NewManager(ManagerOptions{
		URI:                 unreachableTestURI,
		OffsetStoreCount:    9,
		OffsetStoreInterval: 2 * time.Second,
		Logger:              logger.New("error", false),
	})
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())
	consumer := fake.consumer(testStream)
	require.NotNil(t, consumer)
	for offset := int64(23); offset <= 31; offset++ {
		consumer.deliver(testStream, offset, amqpMessage("payload"))
	}

	stats := m.Stats()

	assert.Equal(t, true, stats["started"])
	assert.Equal(t, 1, stats["consumers"])
	assert.Equal(t, true, stats["ready"])
	assert.Equal(t, map[string]int64{testStream + "/" + testConsumerName: 31}, stats["stored_offsets"])
	assert.Equal(t, 9, stats["offset_store_count"])
	assert.Equal(t, "2s", stats["offset_flush_interval"])
}

func TestManagerStatsOmitsUncommittedOffsets(t *testing.T) {
	m := NewManager(ManagerOptions{
		URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: logger.New("error", false),
	})
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())
	consumer := fake.consumer(testStream)
	require.NotNil(t, consumer)
	consumer.deliver(testStream, 31, amqpMessage("payload"))

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
			fake := newFakeEnvironment()
			startOnFake(t, m, fake, oneConsumerDecls())
			consumer := fake.consumer(testStream)
			require.NotNil(t, consumer)
			consumer.events.status = tt.status
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
// the environment exists and must be disposed — is
// TestManagerStartUnwindsAFailedDeclaration, in process against the Environment
// port.
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
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())
	consumer := fake.consumer(testStream)
	require.NotNil(t, consumer)

	m.abortStartLocked()

	assert.Nil(t, m.env, "the environment is disposed, not just the consumers")
	assert.Empty(t, m.consumers)
	assert.False(t, m.started)
	assert.Equal(t, []string{"close"}, consumer.events.recorded())
	assert.Contains(t, fake.recorded(), callClose)

	require.NoError(t, m.Close(), "the follow-up Close short-circuits instead of closing twice")
}

// TestManagerAbortStartLockedReportsOnlyRealDisposalFailures pins the unwind's
// own guard from both sides: a disposal that succeeded must not be reported as a
// failure on every successful unwind, and one that failed must reach the operator
// with its cause attached.
func TestManagerAbortStartLockedReportsOnlyRealDisposalFailures(t *testing.T) {
	t.Run("successful_disposal_is_silent", func(t *testing.T) {
		fake := newFakeEnvironment()
		m, log := recordingManager(t, fake)

		m.abortStartLocked()

		require.Contains(t, fake.recorded(), callClose, "the premise: there was an environment to dispose")
		assert.Empty(t, log.warnMessages(), "the whole unwind is silent when every step succeeds")
	})

	t.Run("failed_disposal_is_reported", func(t *testing.T) {
		fake := newFakeEnvironment()
		m, log := recordingManager(t, fake)
		fake.failOn(callClose, errors.New("close failed"))

		m.abortStartLocked()

		assert.Contains(t, log.warnMessages(), msgCloseEnvFailed)
		closeErr, ok := log.warnError(msgCloseEnvFailed)
		require.True(t, ok)
		assert.Equal(t, "close failed", closeErr)
	})
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
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())

	m.StopConsumers()
	require.False(t, m.started, "the premise: StopConsumers clears started")
	require.Same(t, fake, m.env, "the premise: StopConsumers leaves the environment for Close")

	err := m.Start(context.Background(), oneConsumerDecls())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started",
		"the restart is refused by the guard, not by a failed dial")
	assert.Same(t, fake, m.env, "the first environment is still the one Close disposes, not an orphan")
	assert.False(t, m.started)
	assert.NotContains(t, fake.recorded(), callClose, "a refused restart disposes nothing")
}

func TestManagerCloseEnvLockedIsIdempotent(t *testing.T) {
	m := testManager(t)

	require.NoError(t, m.closeEnvLocked())
	require.NoError(t, m.closeEnvLocked())
	assert.Nil(t, m.env)
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

// TestBindPublishersAbortsWhenCanceledDuringABind pins the gap a check only at
// the top of the loop leaves: a bind is a blocking dial, so the cancellation can
// land while it is in flight and the bind still succeeds. For a publisher-only
// service nothing downstream would notice — startConsumers checks ctx inside a
// loop body that never runs with no consumers declared — so Start would come up
// green for a caller that had already given up.
func TestBindPublishersAbortsWhenCanceledDuringABind(t *testing.T) {
	tests := []struct {
		name       string
		publishers []*publisherDeclaration
	}{
		{
			// The regression proper. With nothing queued behind it, a cancellation
			// during the LAST bind has no later iteration to be caught by: the loop
			// would return nil, and a publisher-only Start would then set started and
			// come up green for a caller that had already given up.
			name:       "canceled_during_the_only_bind",
			publishers: onePublisher(t).publishers,
		},
		{
			// With a declaration still to come the fan-out stops too, and the error
			// names the bind that completed rather than the one never attempted.
			name:       "canceled_during_an_earlier_bind",
			publishers: twoPublishers(t).publishers,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var bound []string
			err := bindPublishers(ctx, tt.publishers, func(decl *publisherDeclaration) error {
				bound = append(bound, decl.Stream)
				cancel() // the caller gives up while this bind is in flight
				return nil
			})

			require.ErrorIs(t, err, context.Canceled)
			assert.Contains(t, err.Error(), `after binding the publisher on stream "`+testStream+`"`,
				"the cancellation is reported against the bind that completed")
			assert.Equal(t, []string{testStream}, bound, "nothing is bound after the caller gave up")
		})
	}
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

// TestManagerBindPublisherWrapsAConstructionFailure exercises the construction
// failure through the Environment port: against a real broker it dials, so this
// is the only way a broker-free test can reach it.
func TestManagerBindPublisherWrapsAConstructionFailure(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	fake.failOn(callNewProducer, errProducerConstruction)
	decl := onePublisherDeclaration(t)

	err := m.bindPublisher(fake, decl)

	require.ErrorIs(t, err, errProducerConstruction, "the client's own cause reaches the caller")
	assert.Contains(t, err.Error(), `failed to start the publisher on stream "`+testStream+`"`)
	assert.Empty(t, m.publishers, "a publisher that could not be constructed is not tracked")
	assert.ErrorIs(t, decl.Publisher.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)}),
		ErrPublisherNotStarted, "and its handle stays unbound")
}

// TestManagerBindPublisherTracksAConstructedProducer is the success half: the
// producer is built for the declared stream, bound, and tracked.
func TestManagerBindPublisherTracksAConstructedProducer(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	decl := onePublisherDeclaration(t)

	require.NoError(t, m.bindPublisher(fake, decl))

	assert.Equal(t, []string{callNewProducer + ":" + testStream}, fake.recorded(),
		"the producer is built for the declared stream, through the plain constructor")
	assert.Equal(t, []*Publisher{decl.Publisher}, m.publishers)
	assert.Equal(t, ha.StatusOpen, decl.Publisher.status(), "the handle is bound to the new producer")

	call := fake.producerCall(testStream)
	require.NotNil(t, call, "the port must have captured the plain producer's construction call")
	assert.NotNil(t, call.opts, "stream.NewProducerOptions() must reach the constructor")
	assert.NotNil(t, call.confirmed,
		"the confirmation handler must reach the constructor, or the publisher's sends are never correlated")
}

// onePublisher declares a single publisher — the shape where a cancellation
// during its bind has no later iteration to be caught by.
func onePublisher(t *testing.T) *Declarations {
	t.Helper()
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclarePublisher(&PublisherOptions{Stream: testStream})
	return decls
}

// onePublisherDeclaration builds a single declared publisher for the bind tests.
func onePublisherDeclaration(t *testing.T) *publisherDeclaration {
	t.Helper()
	decls := onePublisher(t)
	require.Len(t, decls.publishers, 1)
	return decls.publishers[0]
}

// oneSuperPublisherDeclaration is the same for a super-stream target, which binds
// through the client's other producer constructor.
func oneSuperPublisherDeclaration(t *testing.T) *publisherDeclaration {
	t.Helper()
	decls := NewDeclarations()
	decls.DeclareSuperStream(testSuperStream, testPartitions, nil)
	decls.DeclareSuperStreamPublisher(&SuperStreamPublisherOptions{SuperStream: testSuperStream})
	require.Len(t, decls.publishers, 1)
	return decls.publishers[0]
}

// TestManagerBindPublisherWrapsASuperConstructionFailure is the super-stream half
// of the construction-failure path, and pins that the failure names the kind of
// target the declaration asked for rather than calling every target a stream.
func TestManagerBindPublisherWrapsASuperConstructionFailure(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	fake.failOn(callNewSuperProducer, errProducerConstruction)
	decl := oneSuperPublisherDeclaration(t)

	err := m.bindPublisher(fake, decl)

	require.ErrorIs(t, err, errProducerConstruction)
	assert.Contains(t, err.Error(), `failed to start the publisher on super stream "`+testSuperStream+`"`)
	assert.Empty(t, m.publishers)
}

// TestManagerBindPublisherBuildsASuperProducerForASuperTarget pins the dispatch: a
// super declaration must reach the port's super-stream constructor, with the hash
// routing strategy that constructor requires and an extractor that answers the key
// the caller registered. Binding it through the plain constructor would compile and
// then fail at the broker, because a super stream is not a stream.
func TestManagerBindPublisherBuildsASuperProducerForASuperTarget(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	// A plain bind here is the defect under test: it must never be reached.
	fake.failOn(callNewProducer, errProducerConstruction)
	decl := oneSuperPublisherDeclaration(t)

	require.NoError(t, m.bindPublisher(fake, decl))

	assert.Equal(t, []string{callNewSuperProducer + ":" + testSuperStream}, fake.recorded())
	opts := fake.superProducerOptions(testSuperStream)
	require.NotNil(t, opts)
	strategy, ok := opts.RoutingStrategy.(*stream.HashRoutingStrategy)
	require.True(t, ok, "hash routing is the only strategy offered")
	assert.NotNil(t, fake.superProducerConfirmed(testSuperStream),
		"the partition confirmation handler must reach the constructor, or the publisher's sends are never correlated")

	registered := amqp.NewMessage([]byte(testBody))
	decl.Publisher.pending.add(registered, testRoutingKey)
	assert.Equal(t, testRoutingKey, strategy.RoutingKeyExtractor(registered),
		"the extractor answers the key the caller registered with that exact message")

	assert.Equal(t, []*Publisher{decl.Publisher}, m.publishers)
	assert.Equal(t, ha.StatusOpen, decl.Publisher.status())
}

// TestManagerBindPublisherBuildsAPlainProducerForAPlainTarget is the other half of
// that dispatch, and would fail if the two constructors were ever swapped.
func TestManagerBindPublisherBuildsAPlainProducerForAPlainTarget(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	// A super bind here is the defect under test: it must never be reached.
	fake.failOn(callNewSuperProducer, errProducerConstruction)

	require.NoError(t, m.bindPublisher(fake, onePublisherDeclaration(t)))

	assert.Equal(t, []string{callNewProducer + ":" + testStream}, fake.recorded())
	require.Len(t, m.publishers, 1)
	assert.Equal(t, ha.StatusOpen, m.publishers[0].status())
}

// TestManagerRoutingKeyExtractorAnswersTheRegisteredKey pins what the client asks
// this for: the key the caller registered with that exact message, which is what
// makes the partition assignment the caller's choice rather than a hash of "".
func TestManagerRoutingKeyExtractorAnswersTheRegisteredKey(t *testing.T) {
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: log})
	p := newPublisher(testSuperStream, true)
	registered := amqp.NewMessage([]byte(testBody))
	p.pending.add(registered, testRoutingKey)

	key := m.routingKeyExtractor(p)(registered)

	assert.Equal(t, testRoutingKey, key)
	assert.Empty(t, log.messagesAt("error"), "a routed send is routine, not an incident")
}

// TestManagerRoutingKeyExtractorReportsAnUnregisteredMessage covers the miss the
// tombstone lifecycle is supposed to make impossible: the extractor must not
// panic the client's sending goroutine, and the operator has to hear about it,
// because a miss means the correlation assumption broke.
func TestManagerRoutingKeyExtractorReportsAnUnregisteredMessage(t *testing.T) {
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: log})
	p := newPublisher(testSuperStream, true)

	key := m.routingKeyExtractor(p)(amqp.NewMessage([]byte(testBody)))

	assert.Empty(t, key)
	assert.Equal(t, []string{msgRoutingKeyMissing}, log.messagesAt("error"))
	assert.Equal(t, testSuperStream, log.fieldAt("error", msgRoutingKeyMissing, logFieldStream),
		"the report names the publisher whose correlation broke")
}

// TestManagerStopClosesPublishersAfterConsumers pins the shutdown order. A
// consumer handler may publish on its way out, so the producers have to outlive
// the consumers rather than the other way round.
func TestManagerStopClosesPublishersAfterConsumers(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()

	var order []string
	producer := openProducer()
	producer.onClose = func() { order = append(order, "publisher") }
	fake.useProducer(producer)

	decls := oneConsumerDecls()
	decls.DeclarePublisher(&PublisherOptions{Stream: testStream})
	startOnFake(t, m, fake, decls)
	consumer := fake.consumer(testStream)
	require.NotNil(t, consumer)
	consumer.events.onClose = func() { order = append(order, "consumer") }

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
	fake := newFakeEnvironment()
	producer := blockingProducer(t)
	fake.useProducer(producer)

	decls := onePublisher(t)
	startOnFake(t, m, fake, decls)
	p := decls.publishers[0].Publisher

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
	fake := newFakeEnvironment()
	producer := openProducer()
	producer.closeErr = errors.New("close failed")
	fake.useProducer(producer)
	startOnFake(t, m, fake, onePublisher(t))

	assert.NotPanics(t, m.StopConsumers)

	assert.Equal(t, []string{msgClosePublisherFailed}, log.warnMessages())
	closeErr, ok := log.warnError(msgClosePublisherFailed)
	require.True(t, ok)
	assert.Equal(t, "close failed", closeErr)
}

func TestManagerStopIsSilentOnACleanPublisherClose(t *testing.T) {
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: log})
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, onePublisher(t))

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
			fake := newFakeEnvironment()
			fake.useProducer(&fakeProducer{status: tt.status})
			startOnFake(t, m, fake, onePublisher(t))

			assert.Equal(t, tt.want, m.Ready())
		})
	}
}

func TestManagerStatsCountsPublishers(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, onePublisher(t))

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
	decls := onePublisher(t)
	p := decls.publishers[0].Publisher

	firstEnv := newFakeEnvironment()
	first := openProducer()
	firstEnv.useProducer(first)
	startOnFake(t, m, firstEnv, decls)

	m.StopConsumers()
	require.NoError(t, m.Close())

	require.ErrorIs(t, p.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)}), ErrPublisherClosed)
	assert.False(t, m.Ready(), "a stopped manager is not ready")

	secondEnv := newFakeEnvironment()
	second := openProducer()
	secondEnv.useProducer(second)
	startOnFake(t, m, secondEnv, decls)

	assert.True(t, m.Ready(), "the second start comes up ready")
	publishConfirmed(t, p, second, &PublishMessage{Data: []byte(testBody)})
	assert.Zero(t, first.sentCount(), "the revived publisher sends through the new producer, not the disposed one")
}

// TestManagerStartDeclaresBindsThenStarts pins Start's phase order against the
// port. Publishers bind BEFORE consumers start because a handler may publish from
// its very first delivery, and an unbound publisher would reject it with
// ErrPublisherNotStarted; both declare phases come first because neither a
// producer nor a consumer can attach to a stream that does not exist.
func TestManagerStartDeclaresBindsThenStarts(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareSuperStream(testSuperStream, testPartitions, nil)
	decls.DeclarePublisher(&PublisherOptions{Stream: testStream})
	decls.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: testConsumerName, Handler: noopHandler})

	startOnFake(t, m, fake, decls)

	assert.Equal(t, []string{
		callDeclareStream + ":" + testStream,
		callDeclareSuperStream + ":" + testSuperStream,
		callNewProducer + ":" + testStream,
		callQueryOffset + ":" + testConsumerName + "/" + testStream,
		callNewConsumer + ":" + testStream,
	}, fake.recorded())
	assert.True(t, m.started)
	assert.Len(t, m.consumers, 1)
	assert.Len(t, m.publishers, 1)
}

// TestManagerStartUnwindsAFailedDeclaration is abortStartLocked in process: the
// environment was already dialed, so a caller that treats the error as fatal
// without calling Close must leak nothing, and a retried Start must not orphan
// the previous connection pool.
func TestManagerStartUnwindsAFailedDeclaration(t *testing.T) {
	m := testManager(t)
	failing := newFakeEnvironment()
	failing.failOn(callDeclareStream+":"+testStream, errDeclareFailed)
	dialFake(m, failing)

	err := m.Start(context.Background(), oneConsumerDecls())

	require.ErrorIs(t, err, errDeclareFailed)
	assert.Contains(t, err.Error(), `failed to declare stream "`+testStream+`"`)
	assert.Contains(t, failing.recorded(), callClose, "the dialed environment is disposed by the failed Start itself")
	assert.Nil(t, m.env)
	assert.Empty(t, m.consumers)
	assert.Empty(t, m.publishers)
	assert.False(t, m.started)

	// The unwind has to leave the manager startable, or a retry would be refused
	// by the already-started guard for the rest of the process's life.
	healthy := newFakeEnvironment()
	startOnFake(t, m, healthy, oneConsumerDecls())
	assert.True(t, m.started)
}

// TestManagerStartUnwindsAFailedConsumerStart is the unwind's other entry point,
// and the one with something to undo: a publisher was already bound, so the
// unwind has to close it rather than leave a producer attached to an environment
// it is about to dispose.
func TestManagerStartUnwindsAFailedConsumerStart(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	fake.failOn(callNewConsumer+":"+testStream, errConsumerStart)
	dialFake(m, fake)

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	publisher := decls.DeclarePublisher(&PublisherOptions{Stream: testStream})
	decls.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: testConsumerName, Handler: noopHandler})

	err := m.Start(context.Background(), decls)

	require.ErrorIs(t, err, errConsumerStart)
	assert.Contains(t, err.Error(), `failed to start consumer "`+testConsumerName+`" on stream "`+testStream+`"`)
	producer := fake.producer(testStream)
	require.NotNil(t, producer)
	assert.True(t, producer.isClosed(), "the publisher bound before the failure is closed")
	assert.ErrorIs(t, publisher.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)}),
		ErrPublisherClosed)
	assert.Contains(t, fake.recorded(), callClose)
	assert.Nil(t, m.env)
	assert.False(t, m.started)
}

// TestManagerStartResolvesTheAttachPosition drives resolveOffset through Start.
// Only a MISSING offset may fall back to the declared start: any other query
// failure answered with a start position would SKIP, fatally so for the
// zero-value OffsetNext, and streams have no redelivery to get it back.
func TestManagerStartResolvesTheAttachPosition(t *testing.T) {
	tests := []struct {
		name      string
		stored    int64
		hasStored bool
		queryErr  error
		start     OffsetStart
		want      stream.OffsetSpecification
	}{
		{
			name:      "stored_offset_resumes_one_past_it",
			stored:    17,
			hasStored: true,
			start:     OffsetFirst(),
			want:      stream.OffsetSpecification{}.Offset(18),
		},
		{
			name:  "no_stored_offset_uses_the_declared_start",
			start: OffsetFirst(),
			want:  stream.OffsetSpecification{}.First(),
		},
		{
			name:     "query_failure_without_a_local_commit_replays_from_first",
			queryErr: errors.New("boom"),
			start:    OffsetNext(),
			want:     stream.OffsetSpecification{}.First(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testManager(t)
			fake := newFakeEnvironment()
			if tt.hasStored {
				fake.setOffset(testConsumerName, testStream, tt.stored)
			}
			if tt.queryErr != nil {
				fake.failOn(callQueryOffset, tt.queryErr)
			}

			decls := NewDeclarations()
			decls.DeclareStream(testStream, nil)
			decls.DeclareConsumer(&ConsumerOptions{
				Stream: testStream, Name: testConsumerName, Start: tt.start, Handler: noopHandler,
			})
			startOnFake(t, m, fake, decls)

			opts := fake.consumerOptions(testStream)
			require.NotNil(t, opts)
			assert.Equal(t, tt.want, opts.Offset)
		})
	}
}

// TestManagerStartPromotionResolvesTheOffsetAgain exercises the single active
// consumer promotion callback the client fires from its own goroutine: another
// group member may have advanced the offset while this one was passive, so the
// position is resolved again rather than reused from attach time.
func TestManagerStartPromotionResolvesTheOffsetAgain(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareConsumer(&ConsumerOptions{
		Stream: testStream, Name: testConsumerName, SAC: true, Start: OffsetFirst(), Handler: noopHandler,
	})
	startOnFake(t, m, fake, decls)

	opts := fake.consumerOptions(testStream)
	require.NotNil(t, opts)
	require.NotNil(t, opts.SingleActiveConsumer, "the premise: the SAC block reached the port")
	require.Equal(t, stream.OffsetSpecification{}.First(), opts.Offset,
		"the premise: nothing was stored when the consumer attached")

	// Another member committed while this one was passive.
	fake.setOffset(testConsumerName, testStream, 41)

	consumer := fake.consumer(testStream)
	require.NotNil(t, consumer)
	assert.Equal(t, stream.OffsetSpecification{}.Offset(42), consumer.promote(testStream))
}

// TestManagerStartPromotionFallsBackToTheLocalCommit is resolveOffset's third
// branch, which only a promotion can reach: the broker cannot be asked, but this
// process committed a position for the stream itself, so that is the closest
// truth available and is no older than the broker's.
func TestManagerStartPromotionFallsBackToTheLocalCommit(t *testing.T) {
	m := NewManager(ManagerOptions{
		URI: unreachableTestURI, OffsetStoreCount: 1, Logger: logger.New("error", false),
	})
	fake := newFakeEnvironment()

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareConsumer(&ConsumerOptions{
		Stream: testStream, Name: testConsumerName, SAC: true, Start: OffsetNext(), Handler: noopHandler,
	})
	startOnFake(t, m, fake, decls)

	opts := fake.consumerOptions(testStream)
	require.NotNil(t, opts)
	require.NotNil(t, opts.SingleActiveConsumer, "the premise: the SAC block reached the port")

	consumer := fake.consumer(testStream)
	require.NotNil(t, consumer)
	consumer.deliver(testStream, 41, amqpMessage("payload"))
	require.Equal(t, map[string]int64{testStream: 41}, m.consumers[0].offsets.stored(),
		"the premise: this process committed 41")

	fake.failOn(callQueryOffset, errors.New("boom"))

	assert.Equal(t, stream.OffsetSpecification{}.Offset(42), consumer.promote(testStream),
		"a broker that cannot be asked resumes from the local commit, never from the declared start")
}

// TestManagerStartCommitsInFlightThroughTheDeliveringConsumer pins the commit
// path the Environment port must NOT have moved: an in-flight commit goes through
// the consumer that delivered the message, on its connection — neither through
// the reliable handle above it nor through the port's locator. Both kinds behave
// identically here; the asymmetry is the shutdown flush's alone.
func TestManagerStartCommitsInFlightThroughTheDeliveringConsumer(t *testing.T) {
	tests := []struct {
		name        string
		decls       func() *Declarations
		trackedName string
		deliverOn   string
	}{
		{name: "plain_stream", decls: oneConsumerDecls, trackedName: testStream, deliverOn: testStream},
		{name: "super_stream_partition", decls: superConsumerDecls, trackedName: testSuperStream, deliverOn: testPartition0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManager(ManagerOptions{
				URI: unreachableTestURI, OffsetStoreCount: 1, Logger: logger.New("error", false),
			})
			fake := newFakeEnvironment()
			startOnFake(t, m, fake, tt.decls())

			consumer := fake.consumer(tt.trackedName)
			require.NotNil(t, consumer)
			consumer.deliver(tt.deliverOn, 7, amqpMessage("payload"))

			key := testConsumerName + "/" + tt.deliverOn
			assert.Contains(t, fake.recorded(), callConsumerStore+":"+key+"=7")
			assert.NotContains(t, fake.recorded(), callStoreOffset+":"+key+"=7",
				"an in-flight commit never goes through the Environment port")
			assert.Empty(t, consumer.events.recorded(),
				"nor through the reliable handle the manager tracks")

			offset, ok := fake.storedOffset(testConsumerName, tt.deliverOn)
			require.True(t, ok, "the commit reaches the broker's offset store")
			assert.Equal(t, int64(7), offset)
			assert.Equal(t, map[string]int64{tt.deliverOn: 7}, m.consumers[0].offsets.stored())
		})
	}
}

// TestManagerStartFlushesPlainThroughTheHandleAndSuperThroughThePort is the
// offset-storer asymmetry, which lives entirely in the SHUTDOWN flush: a plain
// consumer's handle is an offsetStorer, while *ha.ReliableSuperStreamConsumer has
// no StoreCustomOffset at all, so its partitions commit through the port.
func TestManagerStartFlushesPlainThroughTheHandleAndSuperThroughThePort(t *testing.T) {
	tests := []struct {
		name        string
		decls       func() *Declarations
		trackedName string
		deliverOn   string
		wantHandle  []string
		wantPort    bool
	}{
		{
			name:  "plain_stream_flushes_through_its_own_handle",
			decls: oneConsumerDecls, trackedName: testStream, deliverOn: testStream,
			wantHandle: []string{"store:7", "close"}, wantPort: false,
		},
		{
			name:  "super_stream_flushes_through_the_port",
			decls: superConsumerDecls, trackedName: testSuperStream, deliverOn: testPartition0,
			wantHandle: []string{"close"}, wantPort: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A high threshold leaves the delivery pending, so the flush has work.
			m := NewManager(ManagerOptions{
				URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: logger.New("error", false),
			})
			fake := newFakeEnvironment()
			startOnFake(t, m, fake, tt.decls())

			consumer := fake.consumer(tt.trackedName)
			require.NotNil(t, consumer)
			consumer.deliver(tt.deliverOn, 7, amqpMessage("payload"))
			require.Empty(t, consumer.events.recorded(), "the premise: the offset is still pending")

			m.StopConsumers()

			assert.Equal(t, tt.wantHandle, consumer.events.recorded())
			portEntry := callStoreOffset + ":" + testConsumerName + "/" + tt.deliverOn + "=7"
			recorded := fake.recorded()
			if tt.wantPort {
				assert.Equal(t, 1, countOf(recorded, portEntry), "flushed exactly once through the port")
			} else {
				assert.NotContains(t, recorded, portEntry)
			}
		})
	}
}

// TestManagerStartRunsTheHandlerForADeliveredMessage is the delivery path end to
// end through Start: the port hands the runner a message, the module's handler
// sees the framework's view of it, and the offset is committed after success.
func TestManagerStartRunsTheHandlerForADeliveredMessage(t *testing.T) {
	var got *Message
	m := NewManager(ManagerOptions{
		URI: unreachableTestURI, OffsetStoreCount: 1, Logger: logger.New("error", false),
	})
	fake := newFakeEnvironment()

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareConsumer(&ConsumerOptions{
		Stream: testStream, Name: testConsumerName,
		Handler: func(_ context.Context, msg *Message) error {
			got = msg
			return nil
		},
	})
	startOnFake(t, m, fake, decls)

	consumer := fake.consumer(testStream)
	require.NotNil(t, consumer)
	consumer.deliver(testStream, 55, amqpMessage("payload"))

	require.NotNil(t, got)
	assert.Equal(t, []byte("payload"), got.Data)
	assert.Equal(t, int64(55), got.Offset)
	assert.Equal(t, testStream, got.Stream)
	assert.Equal(t, map[string]any{"kind": "test"}, got.Properties)
	assert.Contains(t, fake.recorded(), callConsumerStore+":"+testConsumerName+"/"+testStream+"=55")
}
