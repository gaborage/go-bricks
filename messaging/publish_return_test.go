package messaging

import (
	"context"
	"fmt"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/logger"
)

// returnTestBody is disjoint from every other value a returned publish logs, so a
// line carrying it can only have leaked the returned body.
const returnTestBody = "returned-body-9f3c"

var mandatoryOptions = publishOptions{Exchange: "ex", RoutingKey: "rk", Mandatory: true}

// returnTestClient is readyClientForSlotTest, whose confirmation wait is long
// enough that no attempt here ends in a timeout instead of the broker's answer,
// with a NACK backoff short enough to retry at once.
func returnTestClient(t *testing.T, ch *fakeChannel) *AMQPClientImpl {
	t.Helper()
	c := readyClientForSlotTest(t, ch)
	c.nackBackoff = time.Millisecond
	return c
}

// pendingKeys lists every key c still holds in pendingPublishes and in its
// Mandatory index.
func pendingKeys(c *AMQPClientImpl) []any {
	var keys []any
	collect := func(k, _ any) bool {
		keys = append(keys, k)
		return true
	}
	c.pendingPublishes.Range(collect)
	c.pendingMandatory.Range(collect)
	return keys
}

// TestRecordReturnMarksOnlyItsOwnGeneration also requires a return that matches no
// waiting publish to leave one DEBUG line naming the broker's reply, the message id
// and the generation, and never the returned body.
func TestRecordReturnMarksOnlyItsOwnGeneration(t *testing.T) {
	log := newRecordingLogger()
	c := &AMQPClientImpl{log: log}
	live := &pendingPublish{messageID: "m"}
	c.trackPending(confirmKey{generation: 2, tag: 1}, live)
	ret := &amqp.Return{ReplyCode: amqp.NoRoute, ReplyText: "NO_ROUTE", MessageId: "m", Body: []byte(returnTestBody)}

	c.recordReturn(1, ret)
	assert.Nil(t, live.returned, "a generation-1 return must not mark a generation-2 publish")
	dropped := log.Line(t, "Dropped a broker return that matches no waiting publish")
	assert.Equal(t, logger.LevelDebug, dropped.Level)
	assert.Equal(t, []string{"312"}, dropped.Values("amqp_reply_code"))
	assert.Equal(t, []string{"NO_ROUTE"}, dropped.Values("amqp_reply_text"))
	assert.Equal(t, []string{"m"}, dropped.Values("message_id"))
	assert.Equal(t, []string{"1"}, dropped.Values("generation"))
	for _, p := range dropped.Pairs {
		assert.NotContains(t, p[1], returnTestBody, "the dropped return's body leaked under %q", p[0])
	}

	c.recordReturn(2, ret)
	assert.Equal(t, &publishReturn{replyCode: amqp.NoRoute, replyText: "NO_ROUTE", messageID: "m"}, live.returned)
	assert.Len(t, log.Lines(), 1, "a matched return logs nothing")
}

func TestUntrackPendingKeepsALaterPublishOfTheSameMessageID(t *testing.T) {
	c := &AMQPClientImpl{}
	first, second := &pendingPublish{messageID: "m"}, &pendingPublish{messageID: "m"}
	c.trackPending(confirmKey{generation: 1, tag: 1}, first)
	c.trackPending(confirmKey{generation: 1, tag: 2}, second)

	assert.Same(t, first, c.untrackPending(confirmKey{generation: 1, tag: 1}))
	assert.Nil(t, c.untrackPending(confirmKey{generation: 1, tag: 1}), "an entry is untracked once")

	c.recordReturn(1, &amqp.Return{ReplyCode: amqp.NoRoute, MessageId: "m"})
	assert.NotNil(t, second.returned, "untracking the first publish must keep the second one's index entry")
}

// TestDrainPendingPublishesWithNackUntracksOnlyItsGeneration requires the reconnect
// drain to drop the torn-down generation's publish from both maps, and to leave the
// live generation's publish of the same message id alone.
func TestDrainPendingPublishesWithNackUntracksOnlyItsGeneration(t *testing.T) {
	c := &AMQPClientImpl{}
	oldKey, liveKey := confirmKey{generation: 1, tag: 1}, confirmKey{generation: 2, tag: 1}
	old := &pendingPublish{confirm: make(chan publishConfirm, 1), messageID: "m"}
	live := &pendingPublish{confirm: make(chan publishConfirm, 1), messageID: "m"}
	c.trackPending(oldKey, old)
	c.trackPending(liveKey, live)

	c.drainPendingPublishesWithNack(1)

	require.Len(t, old.confirm, 1, "the drained publish must be answered")
	assert.False(t, (<-old.confirm).Ack, "the drain answers with a synthetic NACK")
	assert.ElementsMatch(t, []any{liveKey, mandatoryKey{generation: 2, messageID: "m"}}, pendingKeys(c))
}

// TestPublishAttemptUntracksACancelledOrShutDownMandatoryPublish covers the two
// exits that end an attempt with a terminal error. The stop comes after the publish
// lands, so the exit taken is the confirmation wait's, never the slot's.
func TestPublishAttemptUntracksACancelledOrShutDownMandatoryPublish(t *testing.T) {
	tests := []struct {
		name    string
		stop    func(c *AMQPClientImpl, cancel context.CancelFunc)
		wantErr error
	}{
		{name: "caller_cancels", stop: func(_ *AMQPClientImpl, cancel context.CancelFunc) { cancel() }, wantErr: context.Canceled},
		{name: "client_shuts_down", stop: func(c *AMQPClientImpl, _ context.CancelFunc) { _ = c.Close() }, wantErr: ErrShutdown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := &fakeChannel{publishAttemptSignal: make(chan struct{}, 1)}
			c := returnTestClient(t, ch)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			publishing := c.preparePublishing(ctx, mandatoryOptions, []byte(returnTestBody))

			result := make(chan error, 1)
			go func() {
				arm, termErr := c.publishAttempt(ctx, mandatoryOptions, &publishing, time.Now(), trace.SpanFromContext(ctx), nil)
				assert.Nil(t, arm, "a stopped attempt must not be retried")
				result <- termErr
			}()
			awaitPublishAttempt(t, ch.publishAttemptSignal)
			tt.stop(c, cancel)

			require.ErrorIs(t, awaitResult(t, result), tt.wantErr)
			assert.Empty(t, pendingKeys(c), "an attempt that stops waiting must drop the publish from both maps")
		})
	}
}

// TestPublishSlottedIndexesOnlyAMandatoryPublish requires a Mandatory publish to be
// findable by its message id while it waits, and a non-mandatory one never to be.
func TestPublishSlottedIndexesOnlyAMandatoryPublish(t *testing.T) {
	tests := []struct {
		name      string
		mandatory bool
	}{
		{name: "mandatory_publish", mandatory: true},
		{name: "non_mandatory_publish", mandatory: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := returnTestClient(t, &fakeChannel{})
			options := mandatoryOptions
			options.Mandatory = tt.mandatory
			ctx := t.Context()
			publishing := c.preparePublishing(ctx, options, []byte(returnTestBody))

			_, key, publishErr, termErr := c.publishSlotted(ctx, options, &publishing, time.Now(), trace.SpanFromContext(ctx), nil)
			require.NoError(t, termErr)
			require.NoError(t, publishErr)

			want := []any{key}
			if tt.mandatory {
				want = append(want, mandatoryKey{generation: key.generation, messageID: publishing.MessageId})
			}
			assert.ElementsMatch(t, want, pendingKeys(c))
			c.untrackPending(key)
		})
	}
}

// TestDrainReturnsStopsAtAClosedListener covers the pre-ack drain meeting a
// listener amqp091 already closed: it must record what was buffered, then answer
// nil so the dispatcher stops reading the closed listener, while an open listener
// with nothing buffered is handed back as it is.
func TestDrainReturnsStopsAtAClosedListener(t *testing.T) {
	c := &AMQPClientImpl{}
	p := &pendingPublish{messageID: "m"}
	c.trackPending(confirmKey{generation: 1, tag: 1}, p)

	open := make(chan amqp.Return)
	var openRecv <-chan amqp.Return = open
	assert.Equal(t, openRecv, c.drainReturns(1, open), "an open listener must be kept")

	closed := make(chan amqp.Return, 1)
	closed <- amqp.Return{ReplyCode: amqp.NoRoute, MessageId: "m"}
	close(closed)
	result := make(chan (<-chan amqp.Return), 1)
	go func() { result <- c.drainReturns(1, closed) }()
	select {
	case got := <-result:
		assert.Nil(t, got, "a closed listener must be answered nil")
	case <-time.After(2 * time.Second):
		t.Fatal("drainReturns kept reading a closed listener")
	}
	assert.NotNil(t, p.returned, "the return buffered before the close must be recorded")
}

// tornDownListeners builds the listeners amqp091 leaves at channel teardown: both
// closed, with ret buffered on the return listener behind returns that match no
// publish. Each of them is one more select the dispatcher can end by picking the
// closed confirm listener, so one that exits without draining keeps ret only with
// probability 2^-(tornDownFillers+1).
func tornDownListeners(ret *amqp.Return) (confirms <-chan amqp.Confirmation, returns <-chan amqp.Return) {
	buffered := make(chan amqp.Return, tornDownFillers+1)
	for i := range tornDownFillers {
		buffered <- amqp.Return{ReplyCode: amqp.NoRoute, MessageId: fmt.Sprintf("filler-%d", i)}
	}
	buffered <- *ret
	close(buffered)
	closed := make(chan amqp.Confirmation)
	close(closed)
	return closed, buffered
}

const tornDownFillers = 15

// TestDispatchConfirmsRecordsTheReturnsBufferedAtTeardown requires the dispatcher to
// record every return still buffered when amqp091 closes both listeners, whichever
// closed listener its select picks first.
func TestDispatchConfirmsRecordsTheReturnsBufferedAtTeardown(t *testing.T) {
	c := &AMQPClientImpl{log: newRecordingLogger(), done: make(chan bool)}
	p := &pendingPublish{messageID: "m"}
	c.trackPending(confirmKey{generation: 1, tag: 1}, p)
	confirms, returns := tornDownListeners(&amqp.Return{ReplyCode: amqp.NoRoute, MessageId: "m"})

	exited := make(chan struct{})
	go func() {
		defer close(exited)
		c.dispatchConfirms(confirms, returns, 1)
	}()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("the dispatcher must exit once its confirm listener closes")
	}
	assert.NotNil(t, p.returned, "a return buffered at teardown must be recorded before the dispatcher exits")
}
