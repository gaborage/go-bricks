package streams

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// holdManagerOn builds a manager with a ledger and starts the given declarations
// against a fake environment, which is how a holding consumer exists at all.
func holdManagerOn(t *testing.T, ledger HoldLedger, decls *Declarations) *Manager {
	t.Helper()
	m := NewManager(ManagerOptions{
		URI:    "rabbitmq-stream://localhost:5552/%2f",
		Logger: logger.New("error", false),
		Hold:   ledger,
	})
	m.SetTenantStamps(true)
	startOnFake(t, m, newFakeEnvironment(), decls)
	return m
}

// holdDeclsWithHandler is one holding consumer whose handler the test owns.
func holdDeclsWithHandler(handler Handler) *Declarations {
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareConsumer(&ConsumerOptions{
		Stream: testStream, Name: testConsumerName, Handler: handler, Hold: true,
	})
	return decls
}

// TestManagerReplayRunsTheHandlerWithTheHeldTenant pins what a replay is: the
// handler runs again for a parked message, under the tenant the row remembered
// rather than a stamp read off a carrier, and NOTHING is committed — settlement
// belongs to the drain, which deletes the row or defers the tenant.
func TestManagerReplayRunsTheHandlerWithTheHeldTenant(t *testing.T) {
	var sawTenant string
	var sawOffset int64
	m := holdManagerOn(t, &fakeHoldLedger{}, holdDeclsWithHandler(
		func(ctx context.Context, msg *Message) error {
			sawTenant, _ = multitenant.TenantID(ctx)
			sawOffset = msg.Offset
			return nil
		}))

	err := m.Replay(context.Background(), testConsumerName, &HeldMessage{
		Consumer: testConsumerName, Stream: testStream, Offset: 4, TenantID: "acme",
		Data: []byte("payload"), HeldAt: time.Now(),
	})

	require.NoError(t, err)
	assert.Equal(t, "acme", sawTenant, "the replay carries the tenant the row remembered")
	assert.Equal(t, int64(4), sawOffset)
}

// TestManagerReplayReturnsTheHandlerErrorUntouched pins that the drain decides
// what a failed replay means — the lane hands the error back as it is.
func TestManagerReplayReturnsTheHandlerErrorUntouched(t *testing.T) {
	wantErr := errors.New("still failing")
	m := holdManagerOn(t, &fakeHoldLedger{}, holdDeclsWithHandler(
		func(context.Context, *Message) error { return wantErr }))

	err := m.Replay(context.Background(), testConsumerName, &HeldMessage{
		Consumer: testConsumerName, Stream: testStream, Offset: 4, TenantID: "acme",
	})

	assert.ErrorIs(t, err, wantErr)
}

// TestManagerReplayRefusesAnUnknownConsumer pins the case where a ledger row
// outlives the consumer that parked it: the drain must hear about it rather than
// silently drop the row.
func TestManagerReplayRefusesAnUnknownConsumer(t *testing.T) {
	m := holdManagerOn(t, &fakeHoldLedger{}, holdDeclsWithHandler(noopHandler))

	err := m.Replay(context.Background(), "nope", &HeldMessage{TenantID: "acme"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `no running consumer "nope"`)
}

// TestManagerReloadHeldSwapsTheGate pins the drain's other direction: what the
// ledger holds becomes what the partition gates on.
func TestManagerReloadHeldSwapsTheGate(t *testing.T) {
	ledger := &fakeHoldLedger{held: map[string][]string{testConsumerName: {"globex"}}}
	handled := map[string]int{}
	m := holdManagerOn(t, ledger, holdDeclsWithHandler(
		func(ctx context.Context, _ *Message) error {
			tenant, _ := multitenant.TenantID(ctx)
			handled[tenant]++
			return nil
		}))
	runner := m.consumers[0].runner
	runner.held.add("acme")

	require.NoError(t, m.ReloadHeld(t.Context(), testConsumerName))

	storer := &fakeStorer{}
	runner.deliver(testStream, 1, stampedMessage("acme"), storer)
	runner.deliver(testStream, 2, stampedMessage("globex"), storer)

	assert.Equal(t, 1, handled["acme"], "acme was released by the reload")
	assert.Zero(t, handled["globex"], "and globex is held by it")
}

// TestManagerReloadHeldIgnoresAnUnknownConsumer pins that a drain pass naming a
// consumer this replica does not run is a no-op, not a panic: consumers differ
// per deployment and the ledger is shared.
func TestManagerReloadHeldIgnoresAnUnknownConsumer(t *testing.T) {
	m := holdManagerOn(t, &fakeHoldLedger{}, holdDeclsWithHandler(noopHandler))

	assert.NotPanics(t, func() {
		assert.NoError(t, m.ReloadHeld(t.Context(), "nope"))
	})
}

// TestManagerHoldConsumersListsOnlyHoldingOnes pins what the drain iterates: a
// consumer that does not hold has nothing to drain.
func TestManagerHoldConsumersListsOnlyHoldingOnes(t *testing.T) {
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareStream("plain", nil)
	decls.DeclareConsumer(&ConsumerOptions{
		Stream: testStream, Name: testConsumerName, Handler: noopHandler, Hold: true,
	})
	decls.DeclareConsumer(&ConsumerOptions{
		Stream: "plain", Name: "plain-worker", Handler: noopHandler,
	})
	m := holdManagerOn(t, &fakeHoldLedger{}, decls)

	assert.Equal(t, []string{testConsumerName}, m.HoldConsumers())
}

// TestManagerReloadHeldKeepsAParkThatRacesTheRead pins the generation guard on
// the drain's reload: a park landing while the ledger listing is in flight must
// survive it. The listing predates that park, so applying it would un-hold a
// tenant whose message is already parked — and the next message for that tenant
// would be delivered ahead of it, which is the ordering guarantee gone.
func TestManagerReloadHeldKeepsAParkThatRacesTheRead(t *testing.T) {
	ledger := &fakeHoldLedger{held: map[string][]string{testConsumerName: {"globex"}}}
	m := holdManagerOn(t, ledger, holdDeclsWithHandler(noopHandler))
	runner := m.consumers[0].runner

	// Read 1 answers the listing the park is NOT in, and the park lands while that
	// read is in flight. Read 2 — the retry the refused replace forces — is the one
	// that sees it. A reload that applies read 1 loses the park.
	reads := 0
	ledger.duringHeldRead = func() {
		reads++
		switch reads {
		case 1:
			runner.held.add("acme")
		case 2:
			ledger.held[testConsumerName] = []string{"globex", "acme"}
		}
	}

	require.NoError(t, m.ReloadHeld(t.Context(), testConsumerName))

	assert.True(t, runner.held.has("acme"), "the park that raced the read is still held")
	assert.True(t, runner.held.has("globex"), "and what the ledger reported is held too")
}
