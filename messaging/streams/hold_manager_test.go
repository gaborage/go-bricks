package streams

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

// holdDecls is one holding consumer on one plain stream.
func holdDecls() *Declarations {
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareConsumer(&ConsumerOptions{
		Stream: testStream, Name: testConsumerName, Handler: noopHandler, Hold: true,
	})
	return decls
}

// TestStartRefusesAHoldWithoutALedger pins the startup error: a consumer that
// declares Hold with nothing to park into would silently behave like a consumer
// that skips, which is the failure the hold exists to prevent.
func TestStartRefusesAHoldWithoutALedger(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()
	dialFake(m, fake)

	err := m.Start(context.Background(), holdDecls())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "declares Hold but no hold ledger is configured")
	assert.Contains(t, err.Error(), testConsumerName)
}

// TestStartLoadsTheHeldSetBeforeConsuming pins that a starting partition knows
// which tenants are held BEFORE it takes a message: consuming with an empty set
// would deliver a held tenant's later message ahead of the one it is held behind.
func TestStartLoadsTheHeldSetBeforeConsuming(t *testing.T) {
	ledger := &fakeHoldLedger{held: map[string][]string{testConsumerName: {"tenant-a"}}}
	m := NewManager(ManagerOptions{
		URI:    "rabbitmq-stream://localhost:5552/%2f",
		Logger: logger.New("error", false),
		Hold:   ledger,
	})
	fake := newFakeEnvironment()
	dialFake(m, fake)

	require.NoError(t, m.Start(context.Background(), holdDecls()))

	assert.Equal(t, 1, ledger.loads(), "the held set is read once at startup")
	require.Len(t, m.consumers, 1)
	assert.True(t, m.consumers[0].runner.held.has("tenant-a"))
}

// TestStartFailsWhenTheHeldSetCannotBeLoaded pins the fail-closed rule: a
// partition that cannot learn what is held does not start.
func TestStartFailsWhenTheHeldSetCannotBeLoaded(t *testing.T) {
	ledger := &fakeHoldLedger{heldErr: errors.New("ledger unavailable")}
	m := NewManager(ManagerOptions{
		URI:    "rabbitmq-stream://localhost:5552/%2f",
		Logger: logger.New("error", false),
		Hold:   ledger,
	})
	fake := newFakeEnvironment()
	dialFake(m, fake)

	err := m.Start(context.Background(), holdDecls())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load held tenants")
}

// TestAConsumerWithoutAHoldNeedsNoLedger pins that the requirement is per
// declaration: a manager with no ledger still starts every consumer that does not
// hold, which is every consumer that exists today.
func TestAConsumerWithoutAHoldNeedsNoLedger(t *testing.T) {
	m := testManager(t)
	fake := newFakeEnvironment()

	startOnFake(t, m, fake, oneConsumerDecls())

	require.Len(t, m.consumers, 1)
	assert.Nil(t, m.consumers[0].runner.hold)
}

// TestPromotionClosesTheGateAndReturns pins where the fail-closed rule LIVES: the
// promotion callback runs on the connection's frame reader, which every consumer
// on that connection shares, so waiting there for a ledger that is down would
// stop delivering for all of them. The callback closes this partition's gate and
// returns; the retry runs on the runner's own goroutine.
func TestPromotionClosesTheGateAndReturns(t *testing.T) {
	ledger := &fakeHoldLedger{heldErr: errors.New("ledger unavailable")}
	m := NewManager(ManagerOptions{
		URI:    "rabbitmq-stream://localhost:5552/%2f",
		Logger: logger.New("error", false),
		Hold:   ledger,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runner := m.newRunner(ctx, &consumerDeclaration{
		Stream: testStream, Name: testConsumerName, Handler: noopHandler, Hold: true,
	})
	runner.holdBackoff = time.Millisecond

	returned := make(chan struct{})
	go func() {
		m.reloadHeldOnPromotion(runner)
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(30 * time.Second):
		t.Fatal("the promotion callback must not wait on the ledger")
	}
	assert.True(t, runner.held.gateClosed(), "and it leaves the partition gated")

	// The retry is running behind it, and only stopping the consumer ends it.
	require.Eventually(t, func() bool { return ledger.loads() > 1 }, 30*time.Second, time.Millisecond,
		"the reload keeps asking the ledger off the callback")
	cancel()
}

// TestAGatedPartitionDeliversNothing is the other half of the rule: while the
// gate is closed nothing runs and nothing commits, so the broker redelivers once
// the reload lands.
func TestAGatedPartitionDeliversNothing(t *testing.T) {
	handled := 0
	runner := newHoldRunner(t, func(context.Context, *Message) error {
		handled++
		return nil
	}, &fakeHoldLedger{}, newOffsetTracker(1, time.Hour, newFakeClock().Now))
	runner.held.closeGate()
	storer := &fakeStorer{}

	runner.deliver(testStream, 41, stampedMessage("tenant-a"), storer)

	assert.Zero(t, handled, "a gated partition runs no handler")
	assert.Empty(t, storer.offsets(), "and commits nothing, so the message comes back")
}

// TestPromotionOpensTheGateOnceTheLedgerAnswers pins the ordinary path end to
// end: the reload lands on its own goroutine, the set is what the ledger holds,
// and the partition delivers again.
func TestPromotionOpensTheGateOnceTheLedgerAnswers(t *testing.T) {
	ledger := &fakeHoldLedger{held: map[string][]string{testConsumerName: {"tenant-a"}}}
	m := NewManager(ManagerOptions{
		URI:    "rabbitmq-stream://localhost:5552/%2f",
		Logger: logger.New("error", false),
		Hold:   ledger,
	})
	runner := m.newRunner(context.Background(), &consumerDeclaration{
		Stream: testStream, Name: testConsumerName, Handler: noopHandler, Hold: true,
	})

	m.reloadHeldOnPromotion(runner)

	require.Eventually(t, func() bool { return !runner.held.gateClosed() }, 30*time.Second, time.Millisecond,
		"the gate opens when the ledger answers")
	assert.True(t, runner.held.has("tenant-a"))
}
