package messaging

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gobrickslogger "github.com/gaborage/go-bricks/logger"
)

const (
	msgClientClosed    = "AMQP client closed"
	msgConnected       = "Connected to AMQP broker"
	msgInitReady       = "AMQP client initialized and ready"
	msgInitFailed      = "Failed to initialize AMQP channel, retrying..."
	msgConnectFailed   = "Failed to connect to AMQP broker, retrying with exponential backoff"
	closeReturnTimeout = 2 * time.Second
)

// gatedConn is a fake connection whose Channel() signals entry, then blocks
// until release is closed and fails with amqp.ErrClosed.
type gatedConn struct {
	entered      chan struct{}
	enterOnce    sync.Once
	release      chan struct{}
	channelCalls atomic.Int32
	closeCalls   atomic.Int32
}

func newGatedConn() *gatedConn {
	return &gatedConn{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedConn) Channel() (*amqp.Channel, error) {
	g.channelCalls.Add(1)
	g.enterOnce.Do(func() { close(g.entered) })
	<-g.release
	return nil, amqp.ErrClosed
}

func (g *gatedConn) NotifyClose(c chan *amqp.Error) chan *amqp.Error { return c }

func (g *gatedConn) Close() error {
	g.closeCalls.Add(1)
	return nil
}

// gatedDial returns a dialer that signals entry, blocks until release is
// closed, then answers with result.
func gatedDial(result func() (amqpConnection, error)) (dial func(string) (amqpConnection, error), entered, release chan struct{}) {
	entered, release = make(chan struct{}), make(chan struct{})
	var once sync.Once
	dial = func(string) (amqpConnection, error) {
		once.Do(func() { close(entered) })
		<-release
		return result()
	}
	return dial, entered, release
}

func swapDialFunc(t *testing.T, dial func(string) (amqpConnection, error)) {
	t.Helper()
	old := getAmqpDialFunc()
	setAmqpDialFunc(dial)
	t.Cleanup(func() { setAmqpDialFunc(old) })
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(closeReturnTimeout):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// closeNonBlocking calls Close and fails the test if it does not return while
// the reconnect goroutine is still parked in a dial or an init.
func closeNonBlocking(t *testing.T, c *AMQPClientImpl) {
	t.Helper()
	returned := make(chan struct{})
	go func() {
		_ = c.Close()
		close(returned)
	}()
	waitSignal(t, returned, "Close to return while the reconnect goroutine is blocked")
}

// linesAfterClose returns the lines recorded after the "AMQP client closed" line.
func linesAfterClose(t *testing.T, log *recordingLogger) []recordedLine {
	t.Helper()
	lines := log.Lines()
	for i, ln := range lines {
		if ln.Msg == msgClientClosed {
			return lines[i+1:]
		}
	}
	t.Fatalf("no %q line recorded", msgClientClosed)
	return nil
}

func requireNoLoudLines(t *testing.T, lines []recordedLine) {
	t.Helper()
	for _, ln := range lines {
		assert.NotContains(t, []string{gobrickslogger.LevelError, gobrickslogger.LevelWarn}, ln.Level,
			"unexpected %s line after Close: %q", ln.Level, ln.Msg)
		assert.NotContains(t, []string{msgConnected, msgInitReady}, ln.Msg, "success line logged after Close")
	}
}

// TestAMQPClientInitFailingAfterCloseIsQuiet pins that a channel init which
// fails because Close landed while it was in flight is shutdown noise: nothing
// at WARN or ERROR, and the reconnect goroutine exits.
func TestAMQPClientInitFailingAfterCloseIsQuiet(t *testing.T) {
	conn := newGatedConn()
	swapDialFunc(t, func(string) (amqpConnection, error) { return conn, nil })
	log := newRecordingLogger()

	c := NewAMQPClient(amqpHost, log, WithReinitDelay(time.Millisecond))
	waitSignal(t, conn.entered, "channel init to start")
	closeNonBlocking(t, c)
	close(conn.release)
	waitSignal(t, c.reconnectDone, "reconnect goroutine to exit")

	requireNoLoudLines(t, linesAfterClose(t, log))
	assert.Equal(t, int32(1), conn.channelCalls.Load(), "no further init once Close has begun")
}

// TestAMQPClientInitFailingWhileOpenLogsError is the control: the same init
// failure on an open client is a real failure and stays at ERROR.
func TestAMQPClientInitFailingWhileOpenLogsError(t *testing.T) {
	conn := newGatedConn()
	close(conn.release)
	swapDialFunc(t, func(string) (amqpConnection, error) { return conn, nil })
	log := newRecordingLogger()

	c := NewAMQPClient(amqpHost, log, WithReinitDelay(time.Millisecond))
	t.Cleanup(func() { closeAndWaitForReconnect(c) })

	require.Eventually(t, func() bool {
		for _, ln := range log.Lines() {
			if ln.Msg == msgInitFailed && ln.Level == gobrickslogger.LevelError {
				return true
			}
		}
		return false
	}, closeReturnTimeout, time.Millisecond, "an init failure on an open client must log at ERROR")
}

// TestAMQPClientDialFailingAfterCloseIsQuiet pins that a dial which fails after
// Close does not log the connect-failure ERROR and ends the goroutine.
func TestAMQPClientDialFailingAfterCloseIsQuiet(t *testing.T) {
	dial, entered, release := gatedDial(func() (amqpConnection, error) { return nil, errors.New(dialFailMsg) })
	swapDialFunc(t, dial)
	log := newRecordingLogger()

	c := NewAMQPClient(amqpHost, log)
	waitSignal(t, entered, "dial to start")
	closeNonBlocking(t, c)
	close(release)
	waitSignal(t, c.reconnectDone, "reconnect goroutine to exit")

	requireNoLoudLines(t, linesAfterClose(t, log))
}

// TestAMQPClientDialSucceedingAfterCloseIsClosedNotInstalled pins that a
// connection whose dial completes after Close is closed exactly once, never
// installed, and never asked for a channel.
func TestAMQPClientDialSucceedingAfterCloseIsClosedNotInstalled(t *testing.T) {
	conn := newGatedConn()
	close(conn.release)
	dial, entered, release := gatedDial(func() (amqpConnection, error) { return conn, nil })
	swapDialFunc(t, dial)
	log := newRecordingLogger()

	c := NewAMQPClient(amqpHost, log)
	waitSignal(t, entered, "dial to start")
	closeNonBlocking(t, c)
	close(release)
	waitSignal(t, c.reconnectDone, "reconnect goroutine to exit")

	assert.Equal(t, int32(1), conn.closeCalls.Load(), "a post-Close connection must be closed exactly once")
	assert.Zero(t, conn.channelCalls.Load(), "no channel may be opened on a post-Close connection")
	c.m.RLock()
	installed := c.connection
	c.m.RUnlock()
	assert.Nil(t, installed, "a post-Close connection must not be installed")
	requireNoLoudLines(t, linesAfterClose(t, log))
}

// TestHandleReInitStartsNoInitAfterClose pins the loop-top guard: once Close
// has begun, handleReInit exits without asking the connection for a channel,
// even when a close notification rather than done woke it.
func TestHandleReInitStartsNoInitAfterClose(t *testing.T) {
	conn := newGatedConn()
	close(conn.release)
	c := &AMQPClientImpl{m: &sync.RWMutex{}, log: newRecordingLogger(), done: make(chan bool), connection: conn}
	close(c.done)

	assert.True(t, c.handleReInit(nil))
	assert.Zero(t, conn.channelCalls.Load())
}

// TestChangeChannelInstallsNothingAfterClose pins that a channel opened after
// Close is refused: no generation rotation, no install, no listeners.
func TestChangeChannelInstallsNothingAfterClose(t *testing.T) {
	c := newClientWithFakeChannel(t, &fakeChannel{})
	require.NoError(t, c.Close())
	gen, _ := c.channelGeneration()
	c.m.RLock()
	before := c.channel
	c.m.RUnlock()

	late := &fakeChannel{}
	assert.False(t, c.changeChannel(late))

	after, _ := c.channelGeneration()
	assert.Equal(t, gen, after)
	c.m.RLock()
	assert.Same(t, before, c.channel)
	c.m.RUnlock()
	assert.Nil(t, late.notifyCloseCh)
}
