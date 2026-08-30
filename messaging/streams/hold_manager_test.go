package streams

import (
	"context"
	"errors"
	"testing"

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
