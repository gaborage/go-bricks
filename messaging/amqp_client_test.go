package messaging

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gobrickstrace "github.com/gaborage/go-bricks/trace"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
)

const (
	confirmFailedMsg = "confirm failed"
	amqpHost         = "amqp://localhost"
	dialFailMsg      = "dial fail"
)

// adapter that satisfies amqpConnection with injectable amqpChannel
type fakeConnAdapter struct {
	notifyCloseCh chan *amqp.Error
	closeErr      error
}

func (f *fakeConnAdapter) Channel() (*amqp.Channel, error) {
	return nil, errors.New("adapter does not return *amqp.Channel")
}
func (f *fakeConnAdapter) NotifyClose(c chan *amqp.Error) chan *amqp.Error {
	f.notifyCloseCh = c
	return c
}
func (f *fakeConnAdapter) Close() error { return f.closeErr }

type fakeChannel struct {
	confirmErr      error
	qosErr          error
	publishErr      error
	consumeCh       chan amqp.Delivery
	consumeErr      error
	qDeclareErr     error
	exDeclareErr    error
	bindErr         error
	closeErr        error
	notifyCloseCh   chan *amqp.Error
	notifyConfirmCh chan amqp.Confirmation
	lastPublishing  amqp.Publishing
	lastPublishArgs struct {
		exchange, key        string
		mandatory, immediate bool
	}
	declaredQueue    string
	declaredExchange string
	boundQueue       struct{ q, ex, rk string }
	// Signal channel for test coordination
	publishAttemptSignal chan struct{}
	// Mutex to protect concurrent access to fields
	mu sync.RWMutex
	// nextDeliveryTag is incremented by GetNextPublishSeqNo to mimic the
	// broker's monotonic per-channel sequence counter.
	nextDeliveryTag uint64
}

func (f *fakeChannel) Confirm(_ bool) error       { return f.confirmErr }
func (f *fakeChannel) Qos(_, _ int, _ bool) error { return f.qosErr }

//nolint:gocritic // test fake implements interface; signature must match
func (f *fakeChannel) PublishWithContext(_ context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error {
	f.mu.Lock()
	f.lastPublishing = msg
	f.lastPublishArgs = struct {
		exchange, key        string
		mandatory, immediate bool
	}{exchange, key, mandatory, immediate}
	err := f.publishErr
	// Advance the sequence counter only when actually publishing — matches
	// amqp091 broker semantic. Combined with the racy GetNextPublishSeqNo above,
	// this makes our fake reproduce the production race that publishSerial
	// must prevent.
	atomic.AddUint64(&f.nextDeliveryTag, 1)

	// Signal that a publish attempt occurred (non-blocking)
	if f.publishAttemptSignal != nil {
		select {
		case f.publishAttemptSignal <- struct{}{}:
		default:
		}
	}
	f.mu.Unlock()
	return err
}
func (f *fakeChannel) Consume(_, _ string, _, _, _, _ bool, _ amqp.Table) (<-chan amqp.Delivery, error) {
	return f.consumeCh, f.consumeErr
}
func (f *fakeChannel) QueueDeclare(name string, _, _, _, _ bool, _ amqp.Table) (amqp.Queue, error) {
	f.declaredQueue = name
	return amqp.Queue{Name: name}, f.qDeclareErr
}
func (f *fakeChannel) ExchangeDeclare(name, _ string, _, _, _, _ bool, _ amqp.Table) error {
	f.declaredExchange = name
	return f.exDeclareErr
}
func (f *fakeChannel) QueueBind(name, key, exchange string, _ bool, _ amqp.Table) error {
	f.boundQueue = struct{ q, ex, rk string }{name, exchange, key}
	return f.bindErr
}
func (f *fakeChannel) NotifyClose(c chan *amqp.Error) chan *amqp.Error { f.notifyCloseCh = c; return c }

// GetNextPublishSeqNo mimics amqp091.Channel.GetNextPublishSeqNo: returns
// (currentCount + 1) WITHOUT advancing. The counter is advanced inside
// PublishWithContext (matching the real broker semantic). This is the exact
// race window that AMQPClientImpl.publishSerial closes — two concurrent
// callers see the same "next" value here unless serialized externally.
func (f *fakeChannel) GetNextPublishSeqNo() uint64 {
	return atomic.LoadUint64(&f.nextDeliveryTag) + 1
}

func (f *fakeChannel) NotifyPublish(confirm chan amqp.Confirmation) chan amqp.Confirmation {
	f.notifyConfirmCh = confirm
	return confirm
}
func (f *fakeChannel) Close() error { return f.closeErr }

// sendConfirmsAfterEachAttempt arranges for each given Confirmation to be sent
// to c.notifyConfirm AFTER its corresponding PublishWithContext call fires on ch.
// This is the W3-D-era replacement for "send confirm BEFORE publish runs" — the
// dispatcher now drops unmatched DeliveryTag confirmations, so the publish must
// have called channel.GetNextPublishSeqNo() and registered its pending entry
// before the confirm arrives. The fake's publishAttemptSignal fires after each
// PublishWithContext, providing the synchronization point.
//
// Caller must NOT pre-set ch.publishAttemptSignal — this helper owns it. The
// helper consumes len(confs) signals; tests must trigger that many publish
// attempts or risk goroutine leak.
func sendConfirmsAfterEachAttempt(t *testing.T, c *AMQPClientImpl, ch *fakeChannel, confs ...amqp.Confirmation) {
	t.Helper()
	ch.mu.Lock()
	if ch.publishAttemptSignal != nil {
		ch.mu.Unlock()
		t.Fatalf("ch.publishAttemptSignal already set; use manual coordination instead")
	}
	sig := make(chan struct{}, len(confs)+1)
	ch.publishAttemptSignal = sig
	ch.mu.Unlock()
	go func() {
		for _, conf := range confs {
			<-sig
			c.notifyConfirm <- conf
		}
	}()
}

// Helper to build a client with fake channel.
// Uses changeChannel() so the production notify-wiring and confirm-dispatcher
// goroutine are active — tests that need to drive ACKs send to ch.notifyConfirmCh
// (captured by fakeChannel.NotifyPublish during changeChannel). Registers a
// t.Cleanup that calls Close() so the dispatcher goroutine spawned by
// changeChannel doesn't leak across tests in the suite.
func newClientWithFakeChannel(t *testing.T, ch amqpChannel) *AMQPClientImpl {
	t.Helper()
	c := &AMQPClientImpl{
		m:                 &sync.RWMutex{},
		log:               &stubLogger{},
		connectionTimeout: 15 * time.Millisecond,
		resendDelay:       5 * time.Millisecond,
		reInitDelay:       5 * time.Millisecond,
		reconnectDelay:    5 * time.Millisecond,
		done:              make(chan bool),
		isReady:           true,
	}
	c.changeChannel(ch)
	t.Cleanup(func() {
		// Stop the dispatcher goroutine spawned by changeChannel. Some tests
		// close c.done directly without going through Close() — guard against
		// double-close by checking the channel state first.
		select {
		case <-c.done:
			// already closed by the test itself; nothing to do
		default:
			_ = c.Close()
		}
	})
	return c
}

// stubConn implements amqpConnection for connect() tests
type stubConn struct{}

func (stubConn) Channel() (*amqp.Channel, error)                 { return nil, errors.New("no channel") }
func (stubConn) NotifyClose(c chan *amqp.Error) chan *amqp.Error { return c }
func (stubConn) Close() error                                    { return nil }

// ===== Tests exercising amqp_client.go via seam =====

func TestAMQPClientIsReadyToggle(t *testing.T) {
	c := &AMQPClientImpl{m: &sync.RWMutex{}}
	if c.IsReady() {
		t.Fatalf("expected not ready")
	}
	c.m.Lock()
	c.isReady = true
	c.m.Unlock()
	if !c.IsReady() {
		t.Fatalf("expected ready")
	}
}

// TestComputeBackoffBoundsAndExponentialGrowth verifies the full-jitter exponential
// backoff helper: result is always in [0, min(base*2^attempt, cap)) so the upper
// bound grows with attempt count but each actual wait is a uniform sample below it.
// This is the W3-D fix for the previously-linear reconnectDelay that caused
// thundering herd on broker recovery after extended outages.
func TestComputeBackoffBoundsAndExponentialGrowth(t *testing.T) {
	base := 1 * time.Second
	maxDelay := 60 * time.Second

	tests := []struct {
		name        string
		attempt     int
		wantUpperNS int64
	}{
		{"attempt_0_upper_is_base", 0, int64(1 * time.Second)},
		{"attempt_1_upper_is_2x_base", 1, int64(2 * time.Second)},
		{"attempt_3_upper_is_8x_base", 3, int64(8 * time.Second)},
		{"attempt_5_upper_is_32x_base", 5, int64(32 * time.Second)},
		{"attempt_6_capped_at_max", 6, int64(60 * time.Second)},
		{"attempt_100_still_capped_at_max", 100, int64(60 * time.Second)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Sample many times to catch boundary violations
			for range 200 {
				got := computeBackoff(base, maxDelay, tc.attempt)
				if int64(got) < 0 {
					t.Fatalf("backoff must be non-negative, got %v", got)
				}
				if int64(got) >= tc.wantUpperNS {
					t.Fatalf("backoff %v exceeded upper bound %v at attempt=%d",
						got, time.Duration(tc.wantUpperNS), tc.attempt)
				}
			}
		})
	}
}

// TestComputeBackoffHandlesDegenerateInputs covers the defensive defaults:
// zero/negative base or cap fall back to package defaults; cap < base clamps cap.
func TestComputeBackoffHandlesDegenerateInputs(t *testing.T) {
	// Zero base falls back to defaultReconnectDelay (5s); attempt=0 → upper = 5s.
	got := computeBackoff(0, 60*time.Second, 0)
	if got >= 5*time.Second {
		t.Errorf("zero base should default to 5s upper, got %v", got)
	}

	// Zero cap falls back to defaultReconnectMaxDelay (60s); large attempt should still cap.
	for range 50 {
		got = computeBackoff(1*time.Second, 0, 100)
		if got >= 60*time.Second {
			t.Errorf("zero cap should default to 60s, got %v", got)
		}
	}

	// cap < base: cap clamps up to base; attempt=0 upper = base.
	for range 50 {
		got = computeBackoff(5*time.Second, 1*time.Second, 0)
		if got >= 5*time.Second {
			t.Errorf("cap<base should clamp cap=base, expected upper=5s, got %v", got)
		}
	}
}

// TestPublishConfirmsRoutedByDeliveryTag is the headline regression test for
// Fix #3 in the W3-D bundle. Pre-fix, all publishes shared a single buffered
// notifyConfirm channel — whichever publish read first claimed the next ACK
// regardless of which DeliveryTag it carried. Two concurrent publishes could
// see each other's confirmations.
//
// Post-fix, each publish registers its expected DeliveryTag in pendingPublishes
// and a dispatcher goroutine routes confirms to the matching pending entry.
// The test fires two publishes serially (so DeliveryTags 1 and 2 are deterministic),
// then sends ACKs in REVERSE order (tag 2 before tag 1) — both publishes must
// still resolve correctly, proving the routing is keyed by tag not by arrival.
func TestPublishConfirmsRoutedByDeliveryTag(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch)

	// Coordinate via publishAttemptSignal so we know both publishes have
	// registered their tags before we send confirmations.
	ch.mu.Lock()
	sig := make(chan struct{}, 4)
	ch.publishAttemptSignal = sig
	ch.mu.Unlock()

	resultA := make(chan error, 1)
	resultB := make(chan error, 1)

	// Publish A — will get DeliveryTag=1
	go func() {
		resultA <- c.PublishToExchange(context.Background(),
			PublishOptions{Exchange: "ex", RoutingKey: "a"}, []byte("A"))
	}()
	<-sig // A's PublishWithContext fired → tag 1 registered

	// Publish B — will get DeliveryTag=2
	go func() {
		resultB <- c.PublishToExchange(context.Background(),
			PublishOptions{Exchange: "ex", RoutingKey: "b"}, []byte("B"))
	}()
	<-sig // B's PublishWithContext fired → tag 2 registered

	// Send ACKs OUT OF ORDER. Pre-fix this would have made A claim B's ACK
	// (and vice versa); post-fix the dispatcher routes by DeliveryTag.
	c.notifyConfirm <- amqp.Confirmation{Ack: true, DeliveryTag: 2}
	c.notifyConfirm <- amqp.Confirmation{Ack: true, DeliveryTag: 1}

	// Both should succeed.
	select {
	case errA := <-resultA:
		if errA != nil {
			t.Fatalf("publish A expected success, got %v", errA)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("publish A timed out waiting for its DeliveryTag=1 ACK")
	}
	select {
	case errB := <-resultB:
		if errB != nil {
			t.Fatalf("publish B expected success, got %v", errB)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("publish B timed out waiting for its DeliveryTag=2 ACK")
	}
}

// TestPublishConcurrentNoTagCollision guards a race between
// channel.GetNextPublishSeqNo and pendingPublishes.Store across concurrent
// publishers. amqp091 takes its own lock around GetNextPublishSeqNo and
// PublishWithContext separately — without our publishSerial mutex, two
// goroutines can both see the same "next" sequence number, both register
// pendingPublishes[N], and end up consuming each other's (or no) confirmations.
//
// With the publishSerial fix in place, N concurrent publishes must each
// register a distinct tag (1..N) and receive their own confirmation in any
// arrival order. Run under -race; without the fix this test deadlocks (one
// publisher's chan is overwritten, never receives) or returns a false success
// (the wrong publisher gets the ACK).
func TestPublishConcurrentNoTagCollision(t *testing.T) {
	const N = 16

	ch := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch)
	// The default helper sets connectionTimeout=15ms — too aggressive when we
	// want to send N ACKs after a small settle delay. Give the publishers room
	// to actually receive their per-tag confirmation before retry-on-timeout.
	c.connectionTimeout = 5 * time.Second

	// Deterministic synchronization: wait until each publisher has actually
	// reached PublishWithContext (and therefore registered its tag) before
	// feeding ACKs. Without this, a time.Sleep-based settle delay races
	// with goroutine scheduling — the publishers might not all have entered
	// publishSerial yet when ACKs start flowing, and unmatched ACKs are
	// dropped by the dispatcher.
	ch.mu.Lock()
	sig := make(chan struct{}, N)
	ch.publishAttemptSignal = sig
	ch.mu.Unlock()

	results := make(chan error, N)
	startBarrier := make(chan struct{})

	// Launch N publishers; they all unblock simultaneously on `startBarrier` close.
	for i := 0; i < N; i++ {
		go func() {
			<-startBarrier
			results <- c.PublishToExchange(context.Background(),
				PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
		}()
	}
	close(startBarrier)

	// Feed N ACKs into the dispatcher, but only after all N publishers have
	// fired their PublishWithContext (and thus registered their tag under
	// publishSerial). With the fix each one is registered under a unique
	// tag in [1..N] before the next claims a tag.
	go func() {
		for i := 0; i < N; i++ {
			<-sig
		}
		for i := uint64(1); i <= N; i++ {
			c.notifyConfirm <- amqp.Confirmation{Ack: true, DeliveryTag: i}
		}
	}()

	for i := 0; i < N; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("publisher %d failed: %v", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("publisher %d timed out — likely tag-collision race", i)
		}
	}
}

// TestPublishStaleConfirmFromOldChannelDoesNotRouteToNewPublisher guards the
// most consequential W3-D bug, caught by CodeRabbit on PR #459 review:
//
// Per amqp091 + RabbitMQ semantics, each new channel restarts its DeliveryTag
// sequence at 1. The previous channel's dispatcher (still running until its
// notify channel closes during channel teardown) can deliver late
// confirmations whose tags collide with tags already registered against the
// NEW channel. If pendingPublishes is keyed only by DeliveryTag, that late
// confirm hits a publish from the new channel and routes the wrong ACK —
// which propagates to outbox/relay.go marking events as published when they
// weren't. The fix scopes pendingPublishes by (generation, DeliveryTag).
//
// This test simulates the worst case: a publisher registers tag 1 against
// channel 1, then channel 1 is replaced (rotating generation), then a NEW
// publisher registers tag 1 against channel 2. A "late" confirm for tag 1
// from the OLD channel's dispatcher must NOT resolve the NEW publisher.
func TestPublishStaleConfirmFromOldChannelDoesNotRouteToNewPublisher(t *testing.T) {
	ch1 := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch1)

	// Capture the channel-1 dispatcher's source channel BEFORE rotation
	// (so we can manually feed it a late confirm after changeChannel).
	c.m.RLock()
	gen1NotifyConfirm := c.notifyConfirm
	c.m.RUnlock()

	// Rotate to channel 2.
	ch2 := &fakeChannel{}
	c.changeChannel(ch2)

	// New publisher registers against channel 2; its expected tag is 1
	// (channel 2's broker session starts fresh).
	confirmCh2 := make(chan amqp.Confirmation, 1)
	c.publishSerial.Lock()
	c.m.RLock()
	channel2 := c.channel
	gen2 := c.generation
	c.m.RUnlock()
	tag2 := channel2.GetNextPublishSeqNo()
	if tag2 != 1 {
		t.Fatalf("expected channel 2 to start at tag 1, got %d", tag2)
	}
	key2 := confirmKey{generation: gen2, tag: tag2}
	c.pendingPublishes.Store(key2, confirmCh2)
	c.publishSerial.Unlock()

	// Inject a "late" confirm for tag 1 into the OLD generation's notify
	// channel. The OLD dispatcher (pinned to gen1) reads it and looks up
	// (gen1, 1) — which does not exist (drained on rotation). The NEW
	// publisher's entry (gen2, 1) MUST remain untouched.
	gen1NotifyConfirm <- amqp.Confirmation{DeliveryTag: 1, Ack: true}

	// Verify the new publisher's per-publish channel got NO confirm.
	// Use a short timeout because if the bug exists, the confirm arrives
	// promptly; the absence of a confirm in that window is the success
	// condition.
	select {
	case got := <-confirmCh2:
		t.Fatalf("stale confirm from old generation routed to new publisher: got %+v", got)
	case <-time.After(50 * time.Millisecond):
		// Expected: nothing arrived; the new publisher's entry is still pending.
	}

	// Sanity: the new entry is still registered.
	if _, ok := c.pendingPublishes.Load(key2); !ok {
		t.Fatal("new publisher's pendingPublishes entry was unexpectedly removed")
	}
}

// TestPublishNotReadyReturnsErrNotConnected guards the W3-D breaking change:
// pre-fix Publish silently returned nil when the client wasn't ready, dropping
// the message without any error to the caller. Post-fix it returns errNotConnected
// so callers can retry, log, or escalate — same contract as Subscribe/Consume.
func TestPublishNotReadyReturnsErrNotConnected(t *testing.T) {
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}}
	err := c.Publish(context.Background(), "q", []byte("x"))
	if !errors.Is(err, errNotConnected) {
		t.Fatalf("expected errNotConnected when not ready, got %v", err)
	}
}

func TestUnsafePublishSuccessInjectionAndIDs(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch)
	ctx := context.Background()
	err := c.unsafePublish(ctx, PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("payload"))
	if err != nil {
		t.Fatalf("unsafePublish err: %v", err)
	}
	// Check headers injected
	if ch.lastPublishing.Headers == nil {
		t.Fatalf("expected headers injected")
	}
	if _, ok := ch.lastPublishing.Headers[gobrickstrace.HeaderTraceParent]; !ok {
		t.Fatalf("expected traceparent header")
	}
	if ch.lastPublishing.CorrelationId == "" {
		t.Fatalf("expected correlation id set")
	}
	if ch.lastPublishing.MessageId == "" {
		t.Fatalf("expected message id set")
	}
}

func TestPublishToExchangeAckSuccess(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch)

	sendConfirmsAfterEachAttempt(t, c, ch,
		amqp.Confirmation{Ack: true, DeliveryTag: 1},
	)

	if err := c.PublishToExchange(context.Background(), PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg")); err != nil {
		t.Fatalf("publish ack success expected, got %v", err)
	}
}

func TestPublishToExchangeNackThenCancel(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch)
	ctx, cancel := context.WithCancel(context.Background())

	// First publish gets DeliveryTag=1 (fakeChannel.GetNextPublishSeqNo). Send a
	// NACK for that tag so the publish retries; then cancel from another goroutine.
	// The 2nd publish attempt (tag=2) never receives a confirm — context cancel
	// exits the wait.
	ch.mu.Lock()
	sig := make(chan struct{}, 4)
	ch.publishAttemptSignal = sig
	ch.mu.Unlock()

	go func() {
		<-sig // first PublishWithContext fired
		c.notifyConfirm <- amqp.Confirmation{Ack: false, DeliveryTag: 1}
		<-sig // retry PublishWithContext fired
		cancel()
	}()

	if c.PublishToExchange(ctx, PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg")) == nil {
		t.Fatalf("expected context error after cancel")
	}
}

func TestPublishToExchangeConfirmTimeoutThenCancel(t *testing.T) {
	t.Helper()
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch)
	// No confirmation sent -> timeout branch executed
	// Use timeout context instead of sleep-based cancellation
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_ = c.PublishToExchange(ctx, PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
}

func TestConsumeFromQueueSuccess(t *testing.T) {
	deliveries := make(chan amqp.Delivery)
	close(deliveries)
	ch := &fakeChannel{consumeCh: deliveries}
	c := newClientWithFakeChannel(t, ch)
	out, err := c.ConsumeFromQueue(context.Background(), ConsumeOptions{Queue: "q"})
	if err != nil || out == nil {
		t.Fatalf("unexpected consume err=%v ch=%v", err, out)
	}
}

func TestConsumeNotReady(t *testing.T) {
	c := &AMQPClientImpl{m: &sync.RWMutex{}}
	ch, err := c.Consume(context.Background(), "q")
	if err == nil || ch != nil {
		t.Fatalf("expected errNotConnected, got ch=%v err=%v", ch, err)
	}
}

func TestDeclareExchangeQueueBindSuccess(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch)
	if err := c.DeclareQueue("q", true, false, false, false); err != nil {
		t.Fatalf("DeclareQueue err=%v", err)
	}
	if err := c.DeclareExchange("ex", "topic", true, false, false, false); err != nil {
		t.Fatalf("DeclareExchange err=%v", err)
	}
	if err := c.BindQueue("q", "ex", "rk", false); err != nil {
		t.Fatalf("BindQueue err=%v", err)
	}
}

func TestCloseChannelAndConnectionErrors(t *testing.T) {
	ch := &fakeChannel{closeErr: errors.New("channel close failed")}
	c := newClientWithFakeChannel(t, ch)
	c.m.Lock()
	c.isReady = true
	c.m.Unlock()
	c.connection = &fakeConnAdapter{closeErr: errors.New("connection close failed")}
	if c.Close() == nil {
		t.Fatalf("expected close error from channel close")
	}
}

func TestChangeConnectionAndChannelSetupNotifications(t *testing.T) {
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}}
	fc := &fakeConnAdapter{}
	c.changeConnection(fc)
	if c.notifyConnClose == nil {
		t.Fatalf("expected notifyConnClose initialized")
	}
	fch := &fakeChannel{}
	c.changeChannel(fch)
	if c.notifyChanClose == nil || c.notifyConfirm == nil {
		t.Fatalf("expected notification channels initialized")
	}
}

func TestInitSuccessAndFailurePaths(t *testing.T) {
	// success path
	ch := &fakeChannel{}
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}}
	// Use connection adapter that returns no real channel, but we'll set channel directly via changeChannel
	// emulate by calling init with a connection that cannot create channel -> expect error
	if c.init(&fakeConnAdapter{}) == nil {
		// Channel() in adapter returns error; expect error
		t.Fatalf("expected error from init when channel creation fails")
	}
	// Manually create a connection that returns a real *amqp.Channel is not feasible; instead emulate init steps:
	// call changeChannel and toggle ready
	c.changeChannel(ch)
	c.m.Lock()
	c.isReady = true
	c.m.Unlock()
}

func TestHandleReconnectExitsOnDone(t *testing.T) {
	t.Log("spawn reconnect and close done")
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}, brokerURL: "amqp://example", reconnectDelay: 5 * time.Millisecond}
	// Force dialer to always error
	oldDial := getAmqpDialFunc()
	setAmqpDialFunc(func(_ string) (amqpConnection, error) { return nil, errors.New("dial failed") })
	defer func() { setAmqpDialFunc(oldDial) }()
	done := make(chan bool)
	c.done = done
	go c.handleReconnect()
	time.Sleep(2 * time.Millisecond)
	close(done)
	// function should return soon after; give it a moment
	time.Sleep(5 * time.Millisecond)
}

func TestConnectStubSuccess(t *testing.T) {
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}}
	oldDial := getAmqpDialFunc()
	defer func() { setAmqpDialFunc(oldDial) }()
	setAmqpDialFunc(func(_ string) (amqpConnection, error) { return stubConn{}, nil })
	_, err := c.connect()
	if err != nil {
		t.Fatalf("connect expected nil err, got %v", err)
	}
}

func TestHandleReInitExitOnDoneAfterInitError(t *testing.T) {
	t.Helper()
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}, reInitDelay: 5 * time.Millisecond}
	c.done = make(chan bool)
	// connection stubbed -> init will error (Channel() returns error)
	c.connection = stubConn{}
	// close done to force exit
	done := c.done
	go func() {
		time.Sleep(2 * time.Millisecond)
		close(done)
	}()
	// nil conn means it uses c.connection (nil) and init will fail immediately; should exit on done
	_ = c.handleReInit(nil)
}

func TestHandleReInitInitErrorNotifyConnClose(t *testing.T) {
	t.Helper()
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}, reInitDelay: 5 * time.Millisecond}
	c.done = make(chan bool)
	c.connection = stubConn{}
	c.notifyConnClose = make(chan *amqp.Error, 1)
	c.notifyConnClose <- &amqp.Error{} // trigger path
	// Should return false on notifyConnClose when init fails
	if c.handleReInit(nil) != false {
		t.Fatalf("expected handleReInit to return false when connection closed")
	}
}

func TestNewAMQPClientConstructsAndStarts(t *testing.T) {
	t.Helper()
	// Ensure dialer does not hit network
	oldDial := getAmqpDialFunc()
	setAmqpDialFunc(func(_ string) (amqpConnection, error) { return nil, errors.New(dialFailMsg) })
	defer func() { setAmqpDialFunc(oldDial) }()
	c := NewAMQPClient("amqp://example", &stubLogger{})
	if c == nil {
		t.Fatalf("expected client instance")
	}
	// Ensure background goroutines are stopped before the test ends (and before
	// the deferred setAmqpDialFunc restore), so the reconnect goroutine can't
	// race a later test through the shared dial func.
	t.Cleanup(func() { closeAndWaitForReconnect(c) })
}

func TestNewAMQPClientWithConnectionTimeout(t *testing.T) {
	// Stub the dialer so the reconnect goroutine never touches the network.
	oldDial := getAmqpDialFunc()
	setAmqpDialFunc(func(_ string) (amqpConnection, error) { return nil, errors.New(dialFailMsg) })
	defer func() { setAmqpDialFunc(oldDial) }()

	tests := []struct {
		name string
		opts []ClientOption
		want time.Duration
	}{
		{name: "default_when_no_option", opts: nil, want: defaultConnectionTimeout},
		{name: "override_applied", opts: []ClientOption{WithConnectionTimeout(7 * time.Second)}, want: 7 * time.Second},
		{name: "non_positive_ignored", opts: []ClientOption{WithConnectionTimeout(0)}, want: defaultConnectionTimeout},
		{name: "negative_ignored", opts: []ClientOption{WithConnectionTimeout(-1 * time.Second)}, want: defaultConnectionTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewAMQPClient("amqp://example", &stubLogger{}, tc.opts...)
			t.Cleanup(func() { closeAndWaitForReconnect(c) })
			if c.connectionTimeout != tc.want {
				t.Errorf("connectionTimeout = %v, want %v", c.connectionTimeout, tc.want)
			}
		})
	}
}

// ===== Enhanced Connection Management Tests =====

// mockChannelConnection implements amqpConnection for testing
type mockChannelConnection struct {
	channelErr  error
	notifyClose chan *amqp.Error
	closeErr    error
}

func (m *mockChannelConnection) Channel() (*amqp.Channel, error) {
	// For testing we can't return a real *amqp.Channel
	return nil, m.channelErr
}

func (m *mockChannelConnection) NotifyClose(c chan *amqp.Error) chan *amqp.Error {
	m.notifyClose = c
	return c
}

func (m *mockChannelConnection) Close() error {
	return m.closeErr
}

func TestInitSuccessCompleteFlow(t *testing.T) {
	ch := &fakeChannel{}
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}}

	// Test the success path by manually simulating init steps
	// since we can't easily mock *amqp.Channel creation
	if err := ch.Confirm(false); err != nil {
		t.Fatalf("confirm should succeed: %v", err)
	}

	c.changeChannel(ch)
	c.m.Lock()
	c.isReady = true
	c.m.Unlock()

	if !c.IsReady() {
		t.Fatalf("expected client to be ready after successful init")
	}
}

func TestInitChannelCreationFailure(t *testing.T) {
	mockConn := &mockChannelConnection{channelErr: errors.New("channel creation failed")}
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}}

	err := c.init(mockConn)
	if err == nil {
		t.Fatalf("expected error when channel creation fails")
	}
	if err.Error() != "channel creation failed" {
		t.Fatalf("expected channel creation error, got: %v", err)
	}
}

func TestInitConfirmFailureChannelCloseError(t *testing.T) {
	ch := &fakeChannel{
		confirmErr: errors.New(confirmFailedMsg),
		closeErr:   errors.New("close failed"),
	}

	// Test the error handling logic by calling init steps manually
	confirmErr := ch.Confirm(false)
	if confirmErr == nil {
		t.Fatalf("expected confirm to fail")
	}

	closeErr := ch.Close()
	if closeErr == nil {
		t.Fatalf("expected close to fail")
	}

	// The init function would return the close error in this case
	if closeErr.Error() != "close failed" {
		t.Fatalf("expected close error, got: %v", closeErr)
	}
}

func TestInitConfirmFailureChannelCloseSuccess(t *testing.T) {
	ch := &fakeChannel{confirmErr: errors.New(confirmFailedMsg)}

	// Test confirm failure with successful channel close
	confirmErr := ch.Confirm(false)
	if confirmErr == nil {
		t.Fatalf("expected confirm to fail")
	}

	closeErr := ch.Close()
	if closeErr != nil {
		t.Fatalf("expected close to succeed, got: %v", closeErr)
	}

	// The init function would return the original confirm error
	if confirmErr.Error() != confirmFailedMsg {
		t.Fatalf("expected confirm error, got: %v", confirmErr)
	}
}

func TestHandleReInitSuccessAfterInitFailure(t *testing.T) {
	c := &AMQPClientImpl{
		m:           &sync.RWMutex{},
		log:         &stubLogger{},
		reInitDelay: 1 * time.Millisecond,
		done:        make(chan bool),
	}

	// Mock connection that will fail init
	mockConn := &mockChannelConnection{
		channelErr: errors.New("init will fail"),
	}
	c.connection = mockConn
	c.notifyConnClose = make(chan *amqp.Error, 1)
	c.notifyChanClose = make(chan *amqp.Error, 1)

	// Trigger connection close after a brief delay to exit handleReInit
	go func() {
		time.Sleep(2 * time.Millisecond)
		c.notifyConnClose <- &amqp.Error{Code: 320, Reason: "connection forced"}
	}()

	// This should return false due to connection close notification
	result := c.handleReInit(nil)
	if result != false {
		t.Fatalf("expected handleReInit to return false on connection close")
	}
}

func TestHandleReconnectConnectionSuccessInitFailure(t *testing.T) {
	c := &AMQPClientImpl{
		m:              &sync.RWMutex{},
		log:            &stubLogger{},
		brokerURL:      "amqp://test",
		reconnectDelay: 1 * time.Millisecond,
		reInitDelay:    1 * time.Millisecond,
		done:           make(chan bool),
	}

	// Mock successful connection but failing init
	oldDial := getAmqpDialFunc()
	defer setAmqpDialFunc(oldDial)

	mockConn := &mockChannelConnection{channelErr: errors.New("channel failed")}
	setAmqpDialFunc(func(_ string) (amqpConnection, error) {
		return mockConn, nil
	})

	// Start handleReconnect in background
	go c.handleReconnect()

	// Let it try to connect and fail init
	time.Sleep(5 * time.Millisecond)

	// Signal done to stop the reconnection loop
	close(c.done)

	// Give it time to process the done signal
	time.Sleep(2 * time.Millisecond)

	// Verify client is not ready due to init failure
	if c.IsReady() {
		t.Fatalf("expected client to not be ready after init failure")
	}
}

func TestHandleReconnectConnectionFailureRetryCycle(t *testing.T) {
	c := &AMQPClientImpl{
		m:              &sync.RWMutex{},
		log:            &stubLogger{},
		brokerURL:      "amqp://test",
		reconnectDelay: 1 * time.Millisecond,
		done:           make(chan bool),
	}

	// Mock connection failures
	oldDial := getAmqpDialFunc()
	defer setAmqpDialFunc(oldDial)

	// Use atomic counter to avoid race conditions
	var attempts int64
	setAmqpDialFunc(func(_ string) (amqpConnection, error) {
		atomic.AddInt64(&attempts, 1)
		return nil, errors.New("connection failed")
	})

	// Start handleReconnect in background
	go c.handleReconnect()

	// Let it try multiple times
	time.Sleep(5 * time.Millisecond)

	// Signal done to stop
	close(c.done)
	time.Sleep(2 * time.Millisecond)

	// Verify multiple connection attempts were made
	currentAttempts := atomic.LoadInt64(&attempts)
	if currentAttempts < 2 {
		t.Fatalf("expected at least 2 connection attempts, got %d", currentAttempts)
	}

	// Verify client is not ready
	if c.IsReady() {
		t.Fatalf("expected client to not be ready after connection failures")
	}
}

func TestConnectRealConnectionWrapping(t *testing.T) {
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}}

	// Test with stubConnection (not realConnection)
	oldDial := getAmqpDialFunc()
	defer setAmqpDialFunc(oldDial)

	setAmqpDialFunc(func(_ string) (amqpConnection, error) {
		return stubConn{}, nil
	})

	conn, err := c.connect()
	if err != nil {
		t.Fatalf("expected successful connection, got: %v", err)
	}

	// Should return nil since stubConn is not realConnection
	if conn != nil {
		t.Fatalf("expected nil connection for non-realConnection, got: %v", conn)
	}

	// Verify connection was stored
	if c.connection == nil {
		t.Fatalf("expected connection to be stored")
	}
}

func TestConnectDialFailure(t *testing.T) {
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}}

	oldDial := getAmqpDialFunc()
	defer setAmqpDialFunc(oldDial)

	expectedErr := errors.New("dial failed")
	setAmqpDialFunc(func(_ string) (amqpConnection, error) {
		return nil, expectedErr
	})

	conn, err := c.connect()
	if err == nil {
		t.Fatalf("expected connection error")
	}
	if err != expectedErr {
		t.Fatalf("expected dial error, got: %v", err)
	}
	if conn != nil {
		t.Fatalf("expected nil connection on error")
	}
}

// ===== Enhanced Publishing & Confirmation Tests =====

func TestPublishToExchangeShutdownDuringPublish(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch)

	// Close done channel immediately to simulate shutdown
	close(c.done)

	err := c.PublishToExchange(context.Background(), PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
	if err == nil {
		t.Fatalf("expected shutdown error")
	}
	if err != errShutdown {
		t.Fatalf("expected errShutdown, got: %v", err)
	}
}

func TestPublishToExchangeShutdownDuringConfirmation(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch)

	// Close done channel after publish but before confirmation
	go func() {
		time.Sleep(2 * time.Millisecond)
		close(c.done)
	}()

	err := c.PublishToExchange(context.Background(), PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
	if err == nil {
		t.Fatalf("expected shutdown error")
	}
	if err != errShutdown {
		t.Fatalf("expected errShutdown, got: %v", err)
	}
}

func TestPublishToExchangeRetryLogicWithUnsafePublishFailure(t *testing.T) {
	ch := &fakeChannel{publishErr: errors.New("publish failed")}
	c := newClientWithFakeChannel(t, ch)
	c.resendDelay = 1 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	err := c.PublishToExchange(ctx, PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
	if err == nil {
		t.Fatalf("expected context timeout error")
	}
	if err != context.DeadlineExceeded {
		t.Fatalf("expected context deadline exceeded, got: %v", err)
	}
}

func TestPublishToExchangeMultipleRetriesBeforeSuccess(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch)
	c.resendDelay = 1 * time.Millisecond

	// Fail first attempt, succeed on second
	// Use signaling to coordinate retry behavior
	publishAttempts := make(chan struct{}, 2)

	// Set up the fakeChannel to fail initially
	ch.mu.Lock()
	ch.publishErr = errors.New("initial failure")
	ch.publishAttemptSignal = publishAttempts // Signal on each publish attempt
	ch.mu.Unlock()

	go func() {
		<-publishAttempts // Wait for first attempt (gets DeliveryTag=1, fails)
		ch.mu.Lock()
		ch.publishErr = nil // Success on retry
		ch.mu.Unlock()
		<-publishAttempts // Wait for retry attempt (gets DeliveryTag=2, succeeds)
		// Send ack for tag 2 — the retry's tag, not the failed first attempt's.
		c.notifyConfirm <- amqp.Confirmation{Ack: true, DeliveryTag: 2}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := c.PublishToExchange(ctx, PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
}

func TestPublishToExchangeCustomHeaders(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch)

	sendConfirmsAfterEachAttempt(t, c, ch,
		amqp.Confirmation{Ack: true, DeliveryTag: 1},
	)

	customHeaders := map[string]any{
		"custom-header": "test-value",
		"priority":      5,
	}

	options := PublishOptions{
		Exchange:   "custom-ex",
		RoutingKey: "custom-rk",
		Headers:    customHeaders,
		Mandatory:  true,
		Immediate:  false,
	}

	err := c.PublishToExchange(context.Background(), options, []byte("custom-msg"))
	if err != nil {
		t.Fatalf("expected success with custom headers, got: %v", err)
	}

	// Verify headers were applied
	if ch.lastPublishing.Headers["custom-header"] != "test-value" {
		t.Fatalf("expected custom header to be preserved")
	}
	if ch.lastPublishing.Headers["priority"] != 5 {
		t.Fatalf("expected priority header to be preserved")
	}

	// Verify trace headers were injected
	if _, ok := ch.lastPublishing.Headers["traceparent"]; !ok {
		t.Fatalf("expected traceparent header to be injected")
	}

	// Verify publish arguments
	if ch.lastPublishArgs.exchange != "custom-ex" {
		t.Fatalf("expected exchange 'custom-ex', got: %s", ch.lastPublishArgs.exchange)
	}
	if ch.lastPublishArgs.key != "custom-rk" {
		t.Fatalf("expected routing key 'custom-rk', got: %s", ch.lastPublishArgs.key)
	}
	if !ch.lastPublishArgs.mandatory {
		t.Fatalf("expected mandatory to be true")
	}
	if ch.lastPublishArgs.immediate {
		t.Fatalf("expected immediate to be false")
	}
}

func TestPublishToExchangeContextTrackingOnSuccess(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch)

	// Create context with counters for tracking
	ctx := context.Background()

	sendConfirmsAfterEachAttempt(t, c, ch,
		amqp.Confirmation{Ack: true, DeliveryTag: 1},
	)

	err := c.PublishToExchange(ctx, PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

func TestPublishToExchangeMultipleNacksBeforeTimeout(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch)
	c.connectionTimeout = 5 * time.Millisecond
	c.resendDelay = 1 * time.Millisecond

	// Each retry advances the DeliveryTag counter. Send a NACK for whichever tag
	// is currently in flight. The context times out long before we exhaust this
	// supply; the test just verifies the publish doesn't hang on a stale confirm
	// queue after my dispatcher rewrite.
	sendConfirmsAfterEachAttempt(t, c, ch,
		amqp.Confirmation{Ack: false, DeliveryTag: 1},
		amqp.Confirmation{Ack: false, DeliveryTag: 2},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	// This should eventually timeout
	_ = c.PublishToExchange(ctx, PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
	// Test passes if it doesn't hang indefinitely
}

func TestPublishBasicMethodDelegation(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(t, ch)

	sendConfirmsAfterEachAttempt(t, c, ch,
		amqp.Confirmation{Ack: true, DeliveryTag: 1},
	)

	// Test the basic Publish method delegates to PublishToExchange
	err := c.Publish(context.Background(), testQueue, []byte("msg"))
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	// Verify it used default exchange and destination as routing key
	if ch.lastPublishArgs.exchange != "" {
		t.Fatalf("expected empty exchange for default, got: %s", ch.lastPublishArgs.exchange)
	}
	if ch.lastPublishArgs.key != testQueue {
		t.Fatalf("expected routing key 'test-queue', got: %s", ch.lastPublishArgs.key)
	}
}

func TestUnsafePublishNotReady(t *testing.T) {
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}}
	c.isReady = false

	err := c.unsafePublish(context.Background(), PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
	if err == nil {
		t.Fatalf("expected errNotConnected when not ready")
	}
	if err != errNotConnected {
		t.Fatalf("expected errNotConnected, got: %v", err)
	}
}

// =============================================================================
// Error Scenarios and Edge Case Tests
// =============================================================================

func TestAMQPClientDeclareQueueNotReadyError(t *testing.T) {
	// Stub dialer to prevent real network calls
	originalDialFunc := getAmqpDialFunc()
	defer setAmqpDialFunc(originalDialFunc)
	setAmqpDialFunc(func(_ string) (amqpConnection, error) { return nil, errors.New(dialFailMsg) })

	client := NewAMQPClient(amqpHost, &stubLogger{})
	defer closeAndWaitForReconnect(client) // Prevent goroutine leak / cross-test race

	// Client not ready
	err := client.DeclareQueue(testQueue, true, false, false, false)

	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)
}

func TestAMQPClientDeclareExchangeNotReadyError(t *testing.T) {
	// Stub dialer to prevent real network calls
	originalDialFunc := getAmqpDialFunc()
	defer setAmqpDialFunc(originalDialFunc)
	setAmqpDialFunc(func(_ string) (amqpConnection, error) { return nil, errors.New(dialFailMsg) })

	client := NewAMQPClient(amqpHost, &stubLogger{})
	defer closeAndWaitForReconnect(client) // Prevent goroutine leak / cross-test race

	// Client not ready
	err := client.DeclareExchange("test-exchange", "topic", true, false, false, false)

	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)
}

func TestAMQPClientBindQueueNotReadyError(t *testing.T) {
	// Stub dialer to prevent real network calls
	originalDialFunc := getAmqpDialFunc()
	defer setAmqpDialFunc(originalDialFunc)
	setAmqpDialFunc(func(_ string) (amqpConnection, error) { return nil, errors.New(dialFailMsg) })

	client := NewAMQPClient(amqpHost, &stubLogger{})
	defer closeAndWaitForReconnect(client) // Prevent goroutine leak / cross-test race

	// Client not ready
	err := client.BindQueue(testQueue, "test-exchange", "test.key", false)

	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)
}

func TestAMQPClientConsumeFromQueueNotReadyError(t *testing.T) {
	// Stub dialer to prevent real network calls
	originalDialFunc := getAmqpDialFunc()
	defer setAmqpDialFunc(originalDialFunc)
	setAmqpDialFunc(func(_ string) (amqpConnection, error) { return nil, errors.New(dialFailMsg) })

	client := NewAMQPClient(amqpHost, &stubLogger{})
	defer closeAndWaitForReconnect(client) // Prevent goroutine leak / cross-test race

	// Client not ready
	_, err := client.ConsumeFromQueue(context.Background(), ConsumeOptions{
		Queue: testQueue,
	})

	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)
}

func TestAMQPClientConsumeFromQueueChannelError(t *testing.T) {
	consumeErr := errors.New("consume channel error")
	mockChannel := &fakeChannel{
		consumeErr: consumeErr,
	}

	client := newClientWithFakeChannel(t, mockChannel)
	// Ensure resources are cleaned up like the production constructor would
	t.Cleanup(func() { _ = client.Close() })

	// Consume should fail with channel error
	_, err := client.ConsumeFromQueue(context.Background(), ConsumeOptions{
		Queue: testQueue,
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "consume channel error")
}

func TestAMQPClientInitChannelCreationFailure(t *testing.T) {
	// Stub dialer to prevent real network calls
	originalDialFunc := getAmqpDialFunc()
	defer setAmqpDialFunc(originalDialFunc)
	setAmqpDialFunc(func(_ string) (amqpConnection, error) { return nil, errors.New(dialFailMsg) })

	client := NewAMQPClient(amqpHost, &stubLogger{})
	defer closeAndWaitForReconnect(client) // Prevent goroutine leak / cross-test race

	// Test init with a connection that fails to create channels
	mockConn := &stubConn{}
	err := client.init(mockConn)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no channel")
	assert.False(t, client.IsReady())
}

// closeAndWaitForReconnect closes the client and waits for its background
// reconnection goroutine to fully exit. Without this wait a goroutine leaked
// from one test can, via the package-global dial func, dial a later test's fake
// connection and race it. Close itself stays non-blocking in production; this
// deterministic wait lives only in tests. The timeout is a safety net — with
// the in-test stub dialers the goroutine exits near-instantly once done closes.
func closeAndWaitForReconnect(c *AMQPClientImpl) {
	_ = c.Close()
	if c.reconnectDone == nil {
		return
	}
	select {
	case <-c.reconnectDone:
	case <-time.After(2 * time.Second):
	}
}
