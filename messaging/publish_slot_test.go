package messaging

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// negativeWindow is how long a "this must NOT have happened yet" assertion
// waits before declaring the absence real. It is never used as evidence of
// ordering for a POSITIVE claim — those are ordered by construction (the test
// reads the result off a channel before it opens the gate that could produce
// it).
const negativeWindow = 50 * time.Millisecond

// callerDeadline is the publisher's own context deadline in the tests that need
// one to fire: short enough to keep the suite fast, and long enough that the
// publisher is demonstrably parked on the wait under test when it expires.
const callerDeadline = 50 * time.Millisecond

// nackCallerDeadline is the publisher's own context deadline in the NACK
// scenarios, which must outlive one full attempt (publish → NACK → retry arming)
// before it fires, so it is longer than callerDeadline.
const nackCallerDeadline = 150 * time.Millisecond

// deadlineExceededMsg is the exact terminal message of a slot/confirm wait that
// ends on the caller's deadline with no prior attempt cause.
var deadlineExceededMsg = context.DeadlineExceeded.Error()

// holdSlot takes the publish slot from a goroutine that then parks on a gate the
// TEST owns, and returns that gate's release func. It returns only once the slot
// is actually held, so a caller that publishes afterwards is guaranteed to queue
// behind it rather than race it.
func holdSlot(t *testing.T, c *AMQPClientImpl) (release func()) {
	t.Helper()
	held := make(chan struct{})
	gate := make(chan struct{})
	done := make(chan struct{})
	go func() {
		c.publishSerial.acquireUncond()
		close(held)
		<-gate
		c.publishSerial.release()
		close(done)
	}()
	<-held
	var released bool
	return func() {
		if released {
			return
		}
		released = true
		close(gate)
		<-done
	}
}

// publishAsync starts a publish on its own goroutine and returns the channel its
// terminal error arrives on. The result channel IS the ordering primitive: a test
// that reads it before opening any gate has proven the publish returned first.
func publishAsync(ctx context.Context, c *AMQPClientImpl) <-chan error {
	result := make(chan error, 1)
	go func() {
		result <- c.publishBytes(ctx, publishOptions{Exchange: "ex", RoutingKey: "rk"}, []byte("msg"))
	}()
	return result
}

// awaitResult reads a publish result, failing the test rather than hanging the
// package if the publisher never returns.
func awaitResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("publish never returned; the slot wait is not bounded by the caller's deadline")
		return nil
	}
}

// readyClientForSlotTest builds a ready client whose only unbounded wait is the
// publish slot: connectionTimeout is pushed far out so a confirm wait never
// competes with the caller's deadline for the terminal cause.
func readyClientForSlotTest(t *testing.T, ch *fakeChannel) *AMQPClientImpl {
	t.Helper()
	c := newClientWithFakeChannel(t, ch)
	c.connectionTimeout = 5 * time.Second
	return c
}

// TestPublishSlotWaitObservesCallerDeadline pins the whole point of replacing the
// publish mutex with a context-aware one-place semaphore: a publisher queued
// behind a peer that is parked inside the critical section leaves on its OWN
// deadline instead of waiting for that peer to finish. The holder is still
// parked when the caller's error is read — the test reads the result channel
// before it opens the holder's gate — so the return cannot be attributed to the
// slot becoming free.
func TestPublishSlotWaitObservesCallerDeadline(t *testing.T) {
	ch := &fakeChannel{}
	c := readyClientForSlotTest(t, ch)

	release := holdSlot(t, c)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), callerDeadline)
	defer cancel()
	err := awaitResult(t, publishAsync(ctx, c))

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded from the slot wait, got %v", err)
	}
	// The holder never published, so the queued caller cannot have reached the
	// broker: a publish that got the slot would have advanced the fake's tag.
	if attempts := atomic.LoadUint64(&ch.publishAttempts); attempts != 0 {
		t.Fatalf("expected the deadline to fire BEFORE the critical section; got %d publish attempts", attempts)
	}
}

// TestPublishSlotWaitAbortsOnShutdown pins the slot wait's third arm: a client
// shutdown releases a queued publisher with errShutdown, so Close() cannot be
// held hostage by a peer stuck inside the critical section. The publisher is
// proven to be parked on the SLOT (not on the pre-attempt guard, whose select
// has a default arm and never blocks) by the negative window: it had not
// returned while c.done was still open.
func TestPublishSlotWaitAbortsOnShutdown(t *testing.T) {
	ch := &fakeChannel{}
	c := readyClientForSlotTest(t, ch)

	release := holdSlot(t, c)
	defer release()

	result := publishAsync(context.Background(), c)
	select {
	case err := <-result:
		t.Fatalf("publisher returned %v while the slot was held and c.done still open", err)
	case <-time.After(negativeWindow):
		// Expected: parked on the slot, with no deadline of its own.
	}

	close(c.done)
	err := awaitResult(t, result)
	if !errors.Is(err, errShutdown) {
		t.Fatalf("expected errShutdown from the slot wait, got %v", err)
	}
}

// TestPublishSlotExcludesReconnect pins that changeChannel still gets exclusive
// access to the handshake state: while a publisher holds the slot the rotation
// blocks, and it completes once the slot is released. Ordering is asserted, not
// slept for — the completion channel is read only after the release, and its
// non-completion beforehand is the negative half.
func TestPublishSlotExcludesReconnect(t *testing.T) {
	ch1 := &fakeChannel{}
	c := readyClientForSlotTest(t, ch1)

	c.m.RLock()
	genBefore := c.generation
	c.m.RUnlock()

	release := holdSlot(t, c)
	defer release()

	ch2 := &fakeChannel{}
	rotated := make(chan struct{})
	go func() {
		c.changeChannel(ch2)
		close(rotated)
	}()

	select {
	case <-rotated:
		t.Fatal("changeChannel completed while a publisher held the publish slot")
	case <-time.After(negativeWindow):
		// Expected: the rotation is queued on the slot.
	}

	release()

	select {
	case <-rotated:
	case <-time.After(2 * time.Second):
		t.Fatal("changeChannel never completed after the publish slot was released")
	}

	c.m.RLock()
	genAfter := c.generation
	rotatedTo := c.channel
	c.m.RUnlock()
	if delta := genAfter - genBefore; delta != 1 {
		t.Fatalf("expected exactly one generation rotation, got delta %d", delta)
	}
	if rotatedTo != amqpChannel(ch2) {
		t.Fatal("changeChannel did not install the new channel")
	}
}

// abortShapeCase is one early-vs-late pair: the two ways a publish can end on the
// caller's deadline, plus the prior attempt cause both exits must carry.
type abortShapeCase struct {
	name  string
	early func(t *testing.T) error
	late  func(t *testing.T) error
	// wantCause, when non-nil, must appear in the errors.Is chain and the
	// message must carry the "; last attempt:" wrapper.
	wantCause error
}

// assertAbortShape pins one exit's terminal error: the deadline in its errors.Is
// chain, the prior cause when one is expected, and the message prefix. A prior
// cause is rendered as a suffix, so the expected prefix is the bare deadline
// message plus the wrapper when one is expected — never a hardcoded full text of
// the cause.
func assertAbortShape(t *testing.T, label string, err, wantCause error) {
	t.Helper()
	prefix := deadlineExceededMsg
	if wantCause != nil {
		prefix += "; last attempt:"
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%s exit: expected context.DeadlineExceeded in the chain, got %v", label, err)
	}
	if wantCause != nil && !errors.Is(err, wantCause) {
		t.Fatalf("%s exit: expected %v in the chain, got %v", label, wantCause, err)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("%s exit: expected message prefixed %q, got %q", label, prefix, err.Error())
	}
}

// runBothExits drives one case's early and late exits and holds the identity
// claim itself: each exit has the right shape, and the two render the SAME
// message.
func runBothExits(t *testing.T, tc abortShapeCase) {
	t.Helper()
	earlyErr := tc.early(t)
	lateErr := tc.late(t)

	assertAbortShape(t, "early", earlyErr, tc.wantCause)
	assertAbortShape(t, "late", lateErr, tc.wantCause)

	if earlyErr.Error() != lateErr.Error() {
		t.Fatalf("early and late aborts disagree: early %q vs late %q", earlyErr.Error(), lateErr.Error())
	}
}

// TestPublishSlotAbortShapeMatchesConfirmAbort is the load-bearing identity
// check: the NEW early exit (deadline while queued on the slot) must produce
// exactly the terminal error the long-standing late exit (deadline while waiting
// for a confirmation) produces — same wrapping, same prefix, same errors.Is
// chain — for both a virgin publish and one that already carries a NACK cause.
// Asserted as errors.Is plus the message prefix and an early-vs-late equality,
// never as a hardcoded full text of the cause.
func TestPublishSlotAbortShapeMatchesConfirmAbort(t *testing.T) {
	tests := []abortShapeCase{
		{
			name:  "no_prior_cause",
			early: earlySlotDeadline,
			late:  lateConfirmDeadline,
		},
		{
			name:      "prior_nack_cause",
			early:     earlySlotDeadlineAfterNack,
			late:      lateConfirmDeadlineAfterNack,
			wantCause: ErrPublishNacked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runBothExits(t, tt)
		})
	}
}

// earlySlotDeadline returns the terminal error of a publish whose deadline fires
// while it is queued on the publish slot, with no prior attempt.
func earlySlotDeadline(t *testing.T) error {
	t.Helper()
	c := readyClientForSlotTest(t, &fakeChannel{})
	release := holdSlot(t, c)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), callerDeadline)
	defer cancel()
	return awaitResult(t, publishAsync(ctx, c))
}

// lateConfirmDeadline returns the terminal error of a publish whose deadline
// fires while it waits for a confirmation that never arrives, with no prior
// attempt cause. This is the pre-existing exit the early one must match.
func lateConfirmDeadline(t *testing.T) error {
	t.Helper()
	c := readyClientForSlotTest(t, &fakeChannel{})

	ctx, cancel := context.WithTimeout(context.Background(), callerDeadline)
	defer cancel()
	return awaitResult(t, publishAsync(ctx, c))
}

// earlySlotDeadlineAfterNack drives one attempt to a NACK and then makes the
// RETRY queue on a held slot until the caller's deadline fires. The slot is
// taken only after the first attempt has reached the broker (the fake's
// per-attempt signal), so the sequence is ordered, never timed.
func earlySlotDeadlineAfterNack(t *testing.T) error {
	t.Helper()
	ch := &fakeChannel{}
	sig := make(chan struct{}, 1)
	ch.publishAttemptSignal = sig
	c := readyClientForSlotTest(t, ch)

	taken := make(chan struct{})
	gate := make(chan struct{})
	go func() {
		<-sig
		// Queues behind the in-flight publisher and wins the slot the moment it
		// releases — before the NACK below can send it around the retry loop.
		c.publishSerial.acquireUncond()
		close(taken)
		c.notifyConfirm <- amqp.Confirmation{Ack: false, DeliveryTag: 1}
		<-gate
		c.publishSerial.release()
	}()
	defer close(gate)

	ctx, cancel := context.WithTimeout(context.Background(), nackCallerDeadline)
	defer cancel()
	err := awaitResult(t, publishAsync(ctx, c))
	select {
	case <-taken:
	case <-time.After(2 * time.Second):
		t.Fatal("the slot was never taken by the peer goroutine; the retry did not queue behind it")
	}
	return err
}

// lateConfirmDeadlineAfterNack drives one attempt to a NACK and then lets the
// RETRY sit unconfirmed until the caller's deadline fires — the long-standing
// late exit carrying the same lastCause.
func lateConfirmDeadlineAfterNack(t *testing.T) error {
	t.Helper()
	ch := &fakeChannel{}
	c := readyClientForSlotTest(t, ch)
	sendConfirmsAfterEachAttempt(t, c, ch, amqp.Confirmation{Ack: false, DeliveryTag: 1})

	ctx, cancel := context.WithTimeout(context.Background(), nackCallerDeadline)
	defer cancel()
	return awaitResult(t, publishAsync(ctx, c))
}
