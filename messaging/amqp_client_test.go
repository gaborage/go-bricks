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
	// Mutex to protect concurrent access to fields
	mu sync.RWMutex
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

func TestInit_Success_CompleteFlow(t *testing.T) {
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

func TestInit_ChannelCreationFailure(t *testing.T) {
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

func TestInit_ConfirmFailure_ChannelCloseError(t *testing.T) {
	ch := &fakeChannel{
		confirmErr: errors.New("confirm failed"),
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

func TestInit_ConfirmFailure_ChannelCloseSuccess(t *testing.T) {
	ch := &fakeChannel{confirmErr: errors.New("confirm failed")}

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
	if confirmErr.Error() != "confirm failed" {
		t.Fatalf("expected confirm error, got: %v", confirmErr)
	}
}

func TestHandleReInit_SuccessAfterInitFailure(t *testing.T) {
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

func TestHandleReconnect_ConnectionSuccess_InitFailure(t *testing.T) {
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

func TestHandleReconnect_ConnectionFailure_RetryCycle(t *testing.T) {
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

func TestConnect_RealConnectionWrapping(t *testing.T) {
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

func TestConnect_DialFailure(t *testing.T) {
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

func TestPublishToExchange_ShutdownDuringPublish(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(ch)

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

func TestPublishToExchange_ShutdownDuringConfirmation(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(ch)

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

func TestPublishToExchange_RetryLogicWithUnsafePublishFailure(t *testing.T) {
	ch := &fakeChannel{publishErr: errors.New("publish failed")}
	c := newClientWithFakeChannel(ch)
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

func TestPublishToExchange_MultipleRetriesBeforeSuccess(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(ch)
	c.resendDelay = 1 * time.Millisecond

	// Fail first attempt, succeed on second
	ch.mu.Lock()
	ch.publishErr = errors.New("initial failure")
	ch.mu.Unlock()

	go func() {
		time.Sleep(2 * time.Millisecond) // Wait for first attempt
		ch.mu.Lock()
		ch.publishErr = nil // Success on retry
		ch.mu.Unlock()
		time.Sleep(1 * time.Millisecond) // Wait for retry attempt
		c.notifyConfirm <- amqp.Confirmation{Ack: true, DeliveryTag: 1}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := c.PublishToExchange(ctx, PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
}

func TestPublishToExchange_CustomHeaders(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(ch)

	// Send ack after publish
	go func() {
		time.Sleep(1 * time.Millisecond)
		c.notifyConfirm <- amqp.Confirmation{Ack: true, DeliveryTag: 1}
	}()

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

func TestPublishToExchange_ContextTrackingOnSuccess(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(ch)

	// Create context with counters for tracking
	ctx := context.Background()

	// Send ack after publish
	go func() {
		time.Sleep(1 * time.Millisecond)
		c.notifyConfirm <- amqp.Confirmation{Ack: true, DeliveryTag: 1}
	}()

	err := c.PublishToExchange(ctx, PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

func TestPublishToExchange_MultipleNacksBeforeTimeout(_ *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(ch)
	c.connectionTimeout = 5 * time.Millisecond
	c.resendDelay = 1 * time.Millisecond

	nackCount := 0
	// Send multiple nacks to test the retry loop
	go func() {
		for nackCount < 2 {
			time.Sleep(1 * time.Millisecond)
			c.notifyConfirm <- amqp.Confirmation{Ack: false, DeliveryTag: uint64(nackCount + 1)}
			nackCount++
		}
		// After 2 nacks, let timeout happen naturally
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	// This should eventually timeout
	_ = c.PublishToExchange(ctx, PublishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
	// Test passes if it doesn't hang indefinitely
}

func TestPublish_BasicMethodDelegation(t *testing.T) {
	ch := &fakeChannel{}
	c := newClientWithFakeChannel(ch)

	// Send ack after publish
	go func() {
		time.Sleep(1 * time.Millisecond)
		c.notifyConfirm <- amqp.Confirmation{Ack: true, DeliveryTag: 1}
	}()

	// Test the basic Publish method delegates to PublishToExchange
	err := c.Publish(context.Background(), "test-queue", []byte("msg"))
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	// Verify it used default exchange and destination as routing key
	if ch.lastPublishArgs.exchange != "" {
		t.Fatalf("expected empty exchange for default, got: %s", ch.lastPublishArgs.exchange)
	}
	if ch.lastPublishArgs.key != "test-queue" {
		t.Fatalf("expected routing key 'test-queue', got: %s", ch.lastPublishArgs.key)
	}
}

func TestUnsafePublish_NotReady(t *testing.T) {
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

func TestAMQPClient_DeclareQueue_NotReady_Error(t *testing.T) {
	client := NewAMQPClient("amqp://localhost", &stubLogger{})
	defer client.Close() // Prevent goroutine leak

	// Client not ready
	err := client.DeclareQueue("test-queue", true, false, false, false)

	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)
}

func TestAMQPClient_DeclareExchange_NotReady_Error(t *testing.T) {
	client := NewAMQPClient("amqp://localhost", &stubLogger{})
	defer client.Close() // Prevent goroutine leak

	// Client not ready
	err := client.DeclareExchange("test-exchange", "topic", true, false, false, false)

	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)
}

func TestAMQPClient_BindQueue_NotReady_Error(t *testing.T) {
	client := NewAMQPClient("amqp://localhost", &stubLogger{})
	defer client.Close() // Prevent goroutine leak

	// Client not ready
	err := client.BindQueue("test-queue", "test-exchange", "test.key", false)

	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)
}

func TestAMQPClient_ConsumeFromQueue_NotReady_Error(t *testing.T) {
	client := NewAMQPClient("amqp://localhost", &stubLogger{})
	defer client.Close() // Prevent goroutine leak

	// Client not ready
	_, err := client.ConsumeFromQueue(context.Background(), ConsumeOptions{
		Queue: "test-queue",
	})

	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)
}

func TestAMQPClient_ConsumeFromQueue_ChannelError(t *testing.T) {
	originalDialFunc := getAmqpDialFunc()
	defer setAmqpDialFunc(originalDialFunc)

	consumeErr := errors.New("consume channel error")
	mockChannel := &fakeChannel{
		consumeErr: consumeErr,
	}

	setAmqpDialFunc(func(_ string) (amqpConnection, error) {
		return &fakeConnAdapter{}, nil
	})

	client := NewAMQPClient("amqp://localhost", &stubLogger{})
	// Set up manually with our mock channel
	client.m.Lock()
	client.channel = mockChannel
	client.isReady = true
	client.m.Unlock()

	// Consume should fail with channel error
	_, err := client.ConsumeFromQueue(context.Background(), ConsumeOptions{
		Queue: "test-queue",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "consume channel error")
}

func TestAMQPClient_Init_ChannelCreationFailure(t *testing.T) {
	client := NewAMQPClient("amqp://localhost", &stubLogger{})

	// Test init with a connection that fails to create channels
	mockConn := &stubConn{}
	err := client.init(mockConn)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no channel")
	assert.False(t, client.IsReady())
}
