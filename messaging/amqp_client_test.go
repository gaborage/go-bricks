package messaging

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	gobrickstrace "github.com/gaborage/go-bricks/trace"
	amqp "github.com/rabbitmq/amqp091-go"
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
}

func (f *fakeChannel) Confirm(_ bool) error       { return f.confirmErr }
func (f *fakeChannel) Qos(_, _ int, _ bool) error { return f.qosErr }

//nolint:gocritic // test fake implements interface; signature must match
func (f *fakeChannel) PublishWithContext(_ context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error {
	f.lastPublishing = msg
	f.lastPublishArgs = struct {
		exchange, key        string
		mandatory, immediate bool
	}{exchange, key, mandatory, immediate}
	return f.publishErr
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
func (f *fakeChannel) NotifyPublish(confirm chan amqp.Confirmation) chan amqp.Confirmation {
	f.notifyConfirmCh = confirm
	return confirm
}
func (f *fakeChannel) Close() error { return f.closeErr }

// Helper to build a client with fake channel
func newClientWithFakeChannel(ch amqpChannel) *AMQPClientImpl {
	return &AMQPClientImpl{
		m:                 &sync.RWMutex{},
		log:               &stubLogger{},
		connectionTimeout: 15 * time.Millisecond,
		resendDelay:       5 * time.Millisecond,
		reInitDelay:       5 * time.Millisecond,
		reconnectDelay:    5 * time.Millisecond,
		channel:           ch,
		notifyConfirm:     make(chan amqp.Confirmation, 2),
		done:              make(chan bool),
		isReady:           true,
	}
}

// stubConn implements amqpConnection for connect() tests
type stubConn struct{}

func (stubConn) Channel() (*amqp.Channel, error)                 { return nil, errors.New("no channel") }
func (stubConn) NotifyClose(c chan *amqp.Error) chan *amqp.Error { return c }
func (stubConn) Close() error                                    { return nil }

// ===== Tests exercising amqp_client.go via seam =====

func TestAMQPClient_IsReady_Toggle(t *testing.T) {
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

func TestPublish_NotReady_ReturnsNil(t *testing.T) {
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}}
	if err := c.Publish(context.Background(), "q", []byte("x")); err != nil {
		t.Fatalf("expected nil when not ready, got %v", err)
	}
}

func TestUnsafePublish_Success_InjectionAndIDs(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(ch)
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

func TestPublishToExchange_AckSuccess(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(ch)

	// Send ack after publish
	go func() {
		time.Sleep(1 * time.Millisecond)
		c.notifyConfirm <- amqp.Confirmation{Ack: true, DeliveryTag: 1}
	}()

	if err := c.PublishToExchange(context.Background(), PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg")); err != nil {
		t.Fatalf("publish ack success expected, got %v", err)
	}
}

func TestPublishToExchange_NackThenCancel(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(ch)
	ctx, cancel := context.WithCancel(context.Background())

	// First confirmation is nack, then cancel context to exit loop
	go func() {
		time.Sleep(1 * time.Millisecond)
		c.notifyConfirm <- amqp.Confirmation{Ack: false, DeliveryTag: 2}
		time.Sleep(2 * time.Millisecond)
		cancel()
	}()

	if err := c.PublishToExchange(ctx, PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg")); err == nil {
		t.Fatalf("expected context error after cancel")
	}
}

func TestPublishToExchange_ConfirmTimeoutThenCancel(t *testing.T) {
	t.Helper()
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(ch)
	// No confirmation sent -> timeout branch executed; then cancel
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_ = c.PublishToExchange(ctx, PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
}

func TestConsumeFromQueue_Success(t *testing.T) {
	deliveries := make(chan amqp.Delivery)
	close(deliveries)
	ch := &fakeChannel{consumeCh: deliveries}
	c := newClientWithFakeChannel(ch)
	out, err := c.ConsumeFromQueue(context.Background(), ConsumeOptions{Queue: "q"})
	if err != nil || out == nil {
		t.Fatalf("unexpected consume err=%v ch=%v", err, out)
	}
}

func TestConsume_NotReady(t *testing.T) {
	c := &AMQPClientImpl{m: &sync.RWMutex{}}
	ch, err := c.Consume(context.Background(), "q")
	if err == nil || ch != nil {
		t.Fatalf("expected errNotConnected, got ch=%v err=%v", ch, err)
	}
}

func TestDeclareExchangeQueueBind_Success(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(ch)
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

func TestClose_ChannelAndConnectionErrors(t *testing.T) {
	ch := &fakeChannel{closeErr: errors.New("channel close failed")}
	c := newClientWithFakeChannel(ch)
	c.m.Lock()
	c.isReady = true
	c.m.Unlock()
	c.connection = &fakeConnAdapter{closeErr: errors.New("connection close failed")}
	if err := c.Close(); err == nil {
		t.Fatalf("expected close error from channel close")
	}
}

func TestChangeConnectionAndChannel_SetupNotifications(t *testing.T) {
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

func TestInit_Success_And_FailurePaths(t *testing.T) {
	// success path
	ch := &fakeChannel{}
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}}
	// Use connection adapter that returns no real channel, but we'll set channel directly via changeChannel
	// emulate by calling init with a connection that cannot create channel -> expect error
	if err := c.init(&fakeConnAdapter{}); err == nil {
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

func TestHandleReconnect_ExitsOnDone(t *testing.T) {
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

func TestConnect_StubSuccess(t *testing.T) {
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: &stubLogger{}}
	oldDial := getAmqpDialFunc()
	defer func() { setAmqpDialFunc(oldDial) }()
	setAmqpDialFunc(func(_ string) (amqpConnection, error) { return stubConn{}, nil })
	_, err := c.connect()
	if err != nil {
		t.Fatalf("connect expected nil err, got %v", err)
	}
}

func TestHandleReInit_ExitOnDoneAfterInitError(t *testing.T) {
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

func TestHandleReInit_InitErrorNotifyConnClose(t *testing.T) {
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

func TestNewAMQPClient_ConstructsAndStarts(t *testing.T) {
	t.Helper()
	// Ensure dialer does not hit network
	oldDial := getAmqpDialFunc()
	setAmqpDialFunc(func(_ string) (amqpConnection, error) { return nil, errors.New("dial fail") })
	defer func() { setAmqpDialFunc(oldDial) }()
	c := NewAMQPClient("amqp://example", &stubLogger{})
	if c == nil {
		t.Fatalf("expected client instance")
	}
	// Stop background goroutine to avoid races before restoring dialer
	close(c.done)
}
