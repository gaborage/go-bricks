package streams

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/ha"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/message"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"

	obtest "github.com/gaborage/go-bricks/observability/testing"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

const (
	testRoutingKey = "customer-7"
	testBody       = "order-created"

	// sendWaitTimeout bounds the wait for a send to reach the fake producer. The
	// send runs on its own goroutine, so a test that never sees it must fail
	// rather than block the suite.
	sendWaitTimeout = 2 * time.Second
	sendPollEvery   = time.Millisecond
)

// fakeProducer stands in for *ha.ReliableProducer so the publish/confirm
// correlation is testable without a broker.
type fakeProducer struct {
	mu       sync.Mutex
	sent     []message.StreamMessage
	sendErr  error
	closeErr error
	closed   bool
	status   int
	// block, when non-nil, holds Send until the test closes it — the shape of a
	// client parked in a reconnect, which is the case ctx and the close sweep
	// both have to be able to walk away from.
	block chan struct{}
	// onClose records the close in an order shared with other fakes.
	onClose func()
}

func (f *fakeProducer) Send(msg message.StreamMessage) error {
	f.mu.Lock()
	f.sent = append(f.sent, msg)
	block, sendErr := f.block, f.sendErr
	f.mu.Unlock()

	if block != nil {
		<-block
	}
	return sendErr
}

func (f *fakeProducer) Close() error {
	f.mu.Lock()
	f.closed = true
	closeErr, onClose := f.closeErr, f.onClose
	f.mu.Unlock()

	if onClose != nil {
		onClose()
	}
	return closeErr
}

func (f *fakeProducer) GetStatus() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeProducer) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeProducer) allSent() []message.StreamMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]message.StreamMessage(nil), f.sent...)
}

func (f *fakeProducer) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// openProducer is a fake the manager would consider ready.
func openProducer() *fakeProducer { return &fakeProducer{status: ha.StatusOpen} }

// blockingProducer is a fake whose Send never returns until the test releases it.
func blockingProducer(t *testing.T) *fakeProducer {
	t.Helper()
	p := &fakeProducer{status: ha.StatusOpen, block: make(chan struct{})}
	t.Cleanup(func() { close(p.block) })
	return p
}

// boundPublisher builds the publisher a started manager would hand a module.
func boundPublisher(handle producerHandle) *Publisher {
	p := newPublisher(testStream, false)
	p.bind(handle)
	return p
}

// boundSuperPublisher is the same for a super-stream target, whose routing rules
// are the inverse.
func boundSuperPublisher(handle producerHandle) *Publisher {
	p := newPublisher(testSuperStream, true)
	p.bind(handle)
	return p
}

// pendingCount reports how many sends the publisher is still correlating.
func pendingCount(p *Publisher) int {
	p.pending.mu.Lock()
	defer p.pending.mu.Unlock()
	return len(p.pending.m)
}

// publishAsync starts a publish on its own goroutine and returns the channel its
// result arrives on. Publish blocks until a confirmation, so every test that
// wants to confirm one has to get there first.
func publishAsync(ctx context.Context, p *Publisher, msg *PublishMessage) <-chan error {
	done := make(chan error, 1)
	go func() { done <- p.Publish(ctx, msg) }()
	return done
}

// waitForSend blocks until exactly one message reached the fake, and returns it.
func waitForSend(t *testing.T, handle *fakeProducer) message.StreamMessage {
	t.Helper()
	require.Eventually(t, func() bool { return handle.sentCount() == 1 }, sendWaitTimeout, sendPollEvery,
		"the publish never reached the client")
	return handle.allSent()[0]
}

// publishConfirmed runs one publish through to a successful confirmation.
func publishConfirmed(t *testing.T, p *Publisher, handle *fakeProducer, msg *PublishMessage) {
	t.Helper()
	done := publishAsync(context.Background(), p, msg)
	p.resolveConfirmation(waitForSend(t, handle), true, nil)
	require.NoError(t, <-done)
}

// resolvedError reads a waiter's outcome, failing the test instead of blocking on
// it: a waiter nobody resolves is exactly the defect these tests guard against,
// and it must surface as an attributed failure rather than a suite-wide hang.
func resolvedError(t *testing.T, w *waiter) error {
	t.Helper()
	select {
	case err := <-w.ch:
		return err
	case <-time.After(sendWaitTimeout):
		t.Fatal("the waiter was never resolved")
		return nil
	}
}

// propertiesOf reads the application properties a message carries to the broker.
func propertiesOf(t *testing.T, msg message.StreamMessage) map[string]any {
	t.Helper()
	require.NotNil(t, msg)
	return msg.GetApplicationProperties()
}

func TestPublisherPublishResolvesOnConfirmation(t *testing.T) {
	handle := openProducer()
	p := boundPublisher(handle)

	done := publishAsync(context.Background(), p, &PublishMessage{Data: []byte(testBody)})
	sent := waitForSend(t, handle)
	p.resolveConfirmation(sent, true, nil)

	require.NoError(t, <-done)
	assert.Zero(t, pendingCount(p), "a confirmed send leaves no entry behind")
}

func TestPublisherPublishFailsOnARejectedConfirmation(t *testing.T) {
	handle := openProducer()
	p := boundPublisher(handle)
	cause := errors.New("stream does not exist")

	done := publishAsync(context.Background(), p, &PublishMessage{Data: []byte(testBody)})
	p.resolveConfirmation(waitForSend(t, handle), false, cause)

	err := <-done
	require.ErrorIs(t, err, cause, "the broker's own cause must survive to the caller")
	assert.Contains(t, err.Error(), testStream)
	assert.Zero(t, pendingCount(p))
}

// TestPublisherPublishNamesAnUnexplainedRejection covers the other half of the
// rejection path: the client maps unknown response codes to a nil cause, and a
// failure with nothing to wrap still has to say it was not confirmed.
func TestPublisherPublishNamesAnUnexplainedRejection(t *testing.T) {
	handle := openProducer()
	p := boundPublisher(handle)

	done := publishAsync(context.Background(), p, &PublishMessage{Data: []byte(testBody)})
	p.resolveConfirmation(waitForSend(t, handle), false, nil)

	err := <-done
	require.Error(t, err)
	assert.Contains(t, err.Error(), "was not confirmed by the broker")
	assert.Contains(t, err.Error(), testStream)
}

// TestPublisherPublishFailsOnASynchronousSendError pins the one path that does
// NOT wait for a confirmation: a send the client rejected outright never reaches
// its queue, so no callback will ever arrive for it.
func TestPublisherPublishFailsOnASynchronousSendError(t *testing.T) {
	sendErr := errors.New("frame too large")
	handle := &fakeProducer{status: ha.StatusOpen, sendErr: sendErr}
	p := boundPublisher(handle)

	err := p.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)})

	require.ErrorIs(t, err, sendErr)
	assert.Contains(t, err.Error(), testStream)
	assert.Zero(t, pendingCount(p), "a send that never enqueued removes its own entry")
}

// TestPublisherPublishTombstonesTheWaiterOnContextExpiry is the load-bearing
// lifecycle test: a caller that gave up stops waiting, but the entry stays so a
// send still parked in the client can find its routing key. The confirmation that
// eventually arrives is a no-op that removes it.
// TestPublisherPublishKeepsTheSendErrorMessageOffTheSpan pins ADR-083 on the
// stream publish path: the span reports the error's Go type, and the vendor
// message — which a broker can shape freely — reaches no span sink.
func TestPublisherPublishKeepsTheSendErrorMessageOffTheSpan(t *testing.T) {
	tp := obtest.NewTestTraceProvider()
	original := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(original)
		require.NoError(t, tp.Shutdown(context.Background()))
	})

	handle := &fakeProducer{status: ha.StatusOpen, sendErr: errors.New("frame rejected: " + obtest.LeakCanary)}
	p := boundPublisher(handle)

	err := p.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)})
	require.Error(t, err)

	span := obtest.NewSpanCollector(t, tp.Exporter).First()
	assert.Equal(t, codes.Error, span.Status.Code)
	assert.Equal(t, "*fmt.wrapError", span.Status.Description)
	obtest.AssertExceptionTypeOnly(t, &span, "*fmt.wrapError")
	obtest.AssertNoSpanMarkers(t, &span, obtest.LeakCanary)
}

func TestPublisherPublishTombstonesTheWaiterOnContextExpiry(t *testing.T) {
	handle := blockingProducer(t)
	p := boundPublisher(handle)

	ctx, cancel := context.WithCancel(context.Background())
	done := publishAsync(ctx, p, &PublishMessage{Data: []byte(testBody)})
	sent := waitForSend(t, handle)
	cancel()

	err := <-done
	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), testStream)
	assert.Equal(t, 1, pendingCount(p), "ctx expiry tombstones the entry, it never removes it")

	assert.NotPanics(t, func() { p.resolveConfirmation(sent, true, nil) },
		"a late confirmation must not send on the abandoned waiter's channel")
	assert.Zero(t, pendingCount(p), "the late confirmation is what finally removes the entry")
}

// TestPublisherCloseResolvesAnInFlightPublish pins the close sweep. The publish
// below has no deadline of its own, so without the sweep it would hang across the
// whole shutdown: a send parked in a reconnect never reached the client's queue
// and therefore never gets an entityClosed confirmation.
func TestPublisherCloseResolvesAnInFlightPublish(t *testing.T) {
	handle := blockingProducer(t)
	p := boundPublisher(handle)

	done := publishAsync(context.Background(), p, &PublishMessage{Data: []byte(testBody)})
	sent := waitForSend(t, handle)

	require.NoError(t, p.closeBound())

	require.ErrorIs(t, <-done, ErrPublisherClosed)
	assert.True(t, handle.isClosed(), "the client producer is closed too")
	assert.Zero(t, pendingCount(p))
	assert.NotPanics(t, func() { p.resolveConfirmation(sent, false, errors.New("entity closed")) },
		"a late entityClosed confirmation for a swept send is a no-op")
}

func TestPublisherCloseReportsTheClientCloseError(t *testing.T) {
	closeErr := errors.New("close failed")
	handle := &fakeProducer{status: ha.StatusOpen, closeErr: closeErr}
	p := boundPublisher(handle)

	assert.ErrorIs(t, p.closeBound(), closeErr)
}

// TestPublisherCloseWithoutABindingIsANoop covers a manager that aborts its start
// before every publisher was bound.
func TestPublisherCloseWithoutABindingIsANoop(t *testing.T) {
	p := newPublisher(testStream, false)

	assert.NoError(t, p.closeBound())
	assert.ErrorIs(t, p.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)}), ErrPublisherClosed)
}

func TestPublisherPublishBeforeBindIsNotStarted(t *testing.T) {
	p := newPublisher(testStream, false)

	err := p.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)})

	assert.ErrorIs(t, err, ErrPublisherNotStarted)
}

func TestPublisherPublishAfterCloseIsClosed(t *testing.T) {
	handle := openProducer()
	p := boundPublisher(handle)
	require.NoError(t, p.closeBound())

	err := p.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)})

	assert.ErrorIs(t, err, ErrPublisherClosed)
	assert.Zero(t, handle.sentCount(), "a closed publisher never reaches the client")
}

func TestPublisherPublishRejectsANilMessage(t *testing.T) {
	handle := openProducer()
	p := boundPublisher(handle)

	err := p.Publish(context.Background(), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil message")
	assert.Zero(t, handle.sentCount())
}

// TestPublisherPublishRejectsAMismatchedRoutingKey pins both directions of the
// routing contract. Both are rejected before the client is touched: a stray key
// on a plain stream is a wiring error, and an empty one on a super stream would
// hash every message onto a single partition.
func TestPublisherPublishRejectsAMismatchedRoutingKey(t *testing.T) {
	tests := []struct {
		name       string
		super      bool
		routingKey string
		want       string
	}{
		{
			name:       "plain_publisher_rejects_a_routing_key",
			routingKey: testRoutingKey,
			want:       "routing keys select super-stream partitions",
		},
		{
			name:  "super_publisher_requires_a_routing_key",
			super: true,
			want:  "a RoutingKey is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handle := openProducer()
			p := boundPublisher(handle)
			if tt.super {
				p = boundSuperPublisher(handle)
			}

			err := p.Publish(context.Background(), &PublishMessage{Data: []byte(testBody), RoutingKey: tt.routingKey})

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Zero(t, handle.sentCount(), "a rejected message never reaches the client")
			assert.Zero(t, pendingCount(p))
		})
	}
}

// TestPublisherSuperStreamRoutingKeySurvivesContextExpiry is why ctx expiry
// tombstones instead of removing: the client routes on the sending goroutine,
// which can be parked in a reconnect long after the caller gave up. A removed
// entry would answer "" here and pile the message onto one partition.
func TestPublisherSuperStreamRoutingKeySurvivesContextExpiry(t *testing.T) {
	handle := blockingProducer(t)
	p := boundSuperPublisher(handle)

	ctx, cancel := context.WithCancel(context.Background())
	done := publishAsync(ctx, p, &PublishMessage{Data: []byte(testBody), RoutingKey: testRoutingKey})
	sent := waitForSend(t, handle)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	key, ok := p.pending.routingKeyFor(sent)

	require.True(t, ok, "the tombstoned entry must still be there for the late route")
	assert.Equal(t, testRoutingKey, key)
}

// TestPublisherRoutingKeyIsGoneOnceTheSendResolved is the bound on that
// retention: a resolved send releases its entry, so the map does not grow without
// limit.
func TestPublisherRoutingKeyIsGoneOnceTheSendResolved(t *testing.T) {
	handle := openProducer()
	p := boundSuperPublisher(handle)

	done := publishAsync(context.Background(), p, &PublishMessage{Data: []byte(testBody), RoutingKey: testRoutingKey})
	sent := waitForSend(t, handle)
	p.resolveConfirmation(sent, true, nil)
	require.NoError(t, <-done)

	_, ok := p.pending.routingKeyFor(sent)

	assert.False(t, ok)
}

// TestPublisherResolvesConcurrentPublishesIndependently proves the correlation is
// per message and not per publisher: half the batch is confirmed and half
// rejected, and every caller gets its own outcome.
func TestPublisherResolvesConcurrentPublishesIndependently(t *testing.T) {
	const publishes = 8
	handle := openProducer()
	p := boundPublisher(handle)

	results := make(chan error, publishes)
	for i := range publishes {
		go func() {
			results <- p.Publish(context.Background(), &PublishMessage{Data: fmt.Appendf(nil, "%s-%d", testBody, i)})
		}()
	}
	require.Eventually(t, func() bool { return handle.sentCount() == publishes }, sendWaitTimeout, sendPollEvery)

	rejected := errors.New("rejected")
	for i, sent := range handle.allSent() {
		p.resolveConfirmation(sent, i%2 == 0, rejected)
	}

	var confirmed, failed int
	for range publishes {
		if err := <-results; err != nil {
			failed++
		} else {
			confirmed++
		}
	}

	assert.Equal(t, publishes/2, confirmed)
	assert.Equal(t, publishes/2, failed)
	assert.Zero(t, pendingCount(p))
}

// TestPublisherInjectsTraceHeaders pins the message the broker actually receives:
// the caller's properties ride along, the framework's trace headers are added,
// and the caller's own map is never written to.
func TestPublisherInjectsTraceHeaders(t *testing.T) {
	handle := openProducer()
	p := boundPublisher(handle)
	callerProperties := map[string]any{"tenant": "acme"}

	publishConfirmed(t, p, handle, &PublishMessage{Data: []byte(testBody), Properties: callerProperties})

	properties := propertiesOf(t, handle.allSent()[0])
	assert.Equal(t, "acme", properties["tenant"])
	assert.NotEmpty(t, properties[gobrickstrace.HeaderTraceParent])
	assert.NotEmpty(t, properties[gobrickstrace.HeaderXRequestID])
	assert.Equal(t, map[string]any{"tenant": "acme"}, callerProperties,
		"the caller's properties map is copied, never written to")
}

// TestPublisherPreservesACallerSuppliedTraceParent pins the injection contract
// this package inherits: an existing traceparent wins, and X-Request-ID is forced
// into alignment with it so logs and metrics correlate.
func TestPublisherPreservesACallerSuppliedTraceParent(t *testing.T) {
	const traceparent = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	handle := openProducer()
	p := boundPublisher(handle)

	publishConfirmed(t, p, handle, &PublishMessage{
		Data:       []byte(testBody),
		Properties: map[string]any{gobrickstrace.HeaderTraceParent: traceparent},
	})

	properties := propertiesOf(t, handle.allSent()[0])
	assert.Equal(t, traceparent, properties[gobrickstrace.HeaderTraceParent])
	assert.Equal(t, "0123456789abcdef0123456789abcdef", properties[gobrickstrace.HeaderXRequestID])
}

func TestPublisherPublishesNilPropertiesWithTraceHeadersOnly(t *testing.T) {
	handle := openProducer()
	p := boundPublisher(handle)

	publishConfirmed(t, p, handle, &PublishMessage{Data: []byte(testBody)})

	properties := propertiesOf(t, handle.allSent()[0])
	assert.NotEmpty(t, properties[gobrickstrace.HeaderTraceParent])
	assert.NotEmpty(t, properties[gobrickstrace.HeaderXRequestID])
}

// TestPublisherConfirmedSkipsNilStatuses covers the adapter over the client's
// confirmation type. Its fields are unexported, so a unit test can only hand it
// the degenerate values a defensive guard exists for; the correlation itself is
// proven against a real broker in the integration suite.
func TestPublisherConfirmedSkipsNilStatuses(t *testing.T) {
	p := boundPublisher(openProducer())

	assert.NotPanics(t, func() { p.confirmed([]*stream.ConfirmationStatus{nil, {}}) })
	assert.Zero(t, pendingCount(p))
}

// TestPartitionStatusesFlattensEveryPartition pins the super-stream unwrapping: a
// super producer confirms per partition, and every status inside every partition
// has to reach the correlation — one dropped batch hangs its caller until close.
func TestPartitionStatusesFlattensEveryPartition(t *testing.T) {
	first, second, third := &stream.ConfirmationStatus{}, &stream.ConfirmationStatus{}, &stream.ConfirmationStatus{}

	statuses := partitionStatuses([]*stream.PartitionPublishConfirm{
		nil,
		{Partition: testPartition0, ConfirmationStatus: []*stream.ConfirmationStatus{first}},
		{Partition: testPartition1, ConfirmationStatus: []*stream.ConfirmationStatus{second, third}},
	})

	assert.Equal(t, []*stream.ConfirmationStatus{first, second, third}, statuses,
		"every partition's statuses carry through, in order, and a nil batch is skipped")
}

// TestPublisherPartitionsConfirmedResolvesThroughTheSamePath proves a super
// stream's confirmations land in the same correlation the plain lane uses.
//
// The client's ConfirmationStatus keeps its message in an unexported field, so a
// broker-free test can only drive the nil one a zero value renders; the waiter is
// registered under that same key, which is what makes the resolution observable.
// The pointer correlation itself is proven against a broker in the integration suite.
func TestPublisherPartitionsConfirmedResolvesThroughTheSamePath(t *testing.T) {
	p := boundSuperPublisher(openProducer())
	w := p.pending.add(nil, testRoutingKey)

	p.partitionsConfirmed([]*stream.PartitionPublishConfirm{
		{Partition: testPartition0, ConfirmationStatus: []*stream.ConfirmationStatus{{}}},
	})

	err := resolvedError(t, w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), testSuperStream, "the failure names the publisher's own target")
	assert.Zero(t, pendingCount(p), "a resolved send leaves no entry behind")
}

// TestPublisherRegisterSendResolvesASendThatRacedTheCloseSweep pins the window
// the early closed check cannot cover: a close that completes — sweep included —
// between a send's own closed check and its registration. The entry lands after
// the sweep already passed, so nothing else would ever remove it, and because the
// client swallows write errors the send against the closed producer may return
// nothing at all. Driven directly rather than by racing goroutines so the
// interleaving is the test's, not the scheduler's.
func TestPublisherRegisterSendResolvesASendThatRacedTheCloseSweep(t *testing.T) {
	handle := openProducer()
	p := boundPublisher(handle)
	require.NoError(t, p.closeBound())

	clientMsg := amqp.NewMessage([]byte(testBody))
	w, live := p.registerSend(clientMsg, "")

	assert.False(t, live, "a publisher that closed mid-registration must not reach the client")
	assert.ErrorIs(t, resolvedError(t, w), ErrPublisherClosed, "the caller is failed, never left waiting")
	assert.Zero(t, pendingCount(p), "and the entry does not outlive the sweep")
	assert.Zero(t, handle.sentCount())
}

// TestPublisherRegisterSendDispatchesWhileOpen is the other side of that guard:
// an open publisher registers the send and hands it on, entry retained until a
// confirmation resolves it.
func TestPublisherRegisterSendDispatchesWhileOpen(t *testing.T) {
	p := boundPublisher(openProducer())

	clientMsg := amqp.NewMessage([]byte(testBody))
	w, live := p.registerSend(clientMsg, testRoutingKey)

	assert.True(t, live)
	assert.Equal(t, 1, pendingCount(p), "an open publisher keeps the entry for its confirmation")
	assert.Empty(t, w.ch, "and resolves nothing yet")

	key, ok := p.pending.routingKeyFor(clientMsg)
	require.True(t, ok)
	assert.Equal(t, testRoutingKey, key)
}

// TestPublisherConcurrentPublishesSurviveAClose is the end-to-end probe of the
// same invariant, and the shape that would have caught the defect in the wild:
// none of these publishes carries a deadline, so a waiter the close failed to
// resolve hangs its caller forever rather than failing it.
func TestPublisherConcurrentPublishesSurviveAClose(t *testing.T) {
	const publishes = 32
	handle := openProducer()
	p := boundPublisher(handle)

	var wg sync.WaitGroup
	results := make(chan error, publishes)
	for range publishes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- p.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)})
		}()
	}

	require.NoError(t, p.closeBound())

	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(sendWaitTimeout):
		t.Fatal("a publish outlived the close: its waiter was never resolved")
	}

	close(results)
	for err := range results {
		assert.Error(t, err, "a publish racing a close fails, it never succeeds by accident")
	}
	assert.Zero(t, pendingCount(p), "the close leaves no correlation entry behind")
}

// TestPublisherBindReopensAClosedPublisher pins the Close-then-Start cycle at the
// handle level: the module keeps the same *Publisher across it, so a rebind has
// to make it live again or every later publish fails for good.
func TestPublisherBindReopensAClosedPublisher(t *testing.T) {
	first := openProducer()
	p := boundPublisher(first)
	require.NoError(t, p.closeBound())
	require.ErrorIs(t, p.Publish(context.Background(), &PublishMessage{Data: []byte(testBody)}), ErrPublisherClosed)

	second := openProducer()
	p.bind(second)

	publishConfirmed(t, p, second, &PublishMessage{Data: []byte(testBody)})
	assert.Zero(t, first.sentCount(), "the revived publisher sends through the new producer")
}

// TestPublisherBindingNamesWhyThereIsNoProducer pins the sentinel a missing
// binding reports. A close drops the binding, so without the flag check a normal
// shutdown would tell the caller its startup wiring was broken. The closed case
// is not reachable through Publish — send's own closed check short-circuits an
// already-closed publisher — so it is asserted here, at the seam.
func TestPublisherBindingNamesWhyThereIsNoProducer(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(p *Publisher)
		wantErr error
	}{
		{
			name:    "never_bound_was_never_started",
			arrange: func(*Publisher) {},
			wantErr: ErrPublisherNotStarted,
		},
		{
			name:    "binding_dropped_by_a_close_reports_closed",
			arrange: func(p *Publisher) { p.closed.Store(true) },
			wantErr: ErrPublisherClosed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPublisher(testStream, false)
			tt.arrange(p)

			bound, err := p.binding()

			assert.Nil(t, bound)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestPublisherBindingReturnsTheInstalledProducer(t *testing.T) {
	handle := openProducer()
	p := boundPublisher(handle)

	bound, err := p.binding()

	require.NoError(t, err)
	require.NotNil(t, bound)
	assert.Same(t, handle, bound.handle)
}
