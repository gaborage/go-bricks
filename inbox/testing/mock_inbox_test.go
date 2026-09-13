package testing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/app"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	inboxtest "github.com/gaborage/go-bricks/inbox/testing"
	"github.com/gaborage/go-bricks/messaging"
)

// Compile-time guard: MockInbox satisfies the production interface.
var _ app.InboxProcessor = (*inboxtest.MockInbox)(nil)

func noopFn(context.Context, dbtypes.Tx) error { return nil }

// evt1 is the one wire key these tests dedup on. A construction failure would
// leave the zero key, which ProcessOnce refuses — so every test below asserts it.
var evt1, _ = messaging.WireDedupKey("evt-1")

func TestMockInboxRunsFnOncePerID(t *testing.T) {
	m := inboxtest.NewMockInbox()

	ran := 0
	fn := func(context.Context, dbtypes.Tx) error { ran++; return nil }

	require.NoError(t, m.ProcessOnce(context.Background(), evt1, fn))
	require.NoError(t, m.ProcessOnce(context.Background(), evt1, fn)) // duplicate

	assert.Equal(t, 1, ran, "fn runs once across duplicate event ids")
	inboxtest.AssertProcessCount(t, m, 2)
	inboxtest.AssertProcessed(t, m, "evt-1")
	inboxtest.AssertHandlerRan(t, m, "evt-1")
	inboxtest.AssertNotProcessed(t, m, "evt-2")
}

// TestMockInboxRefusesTheZeroKey pins that the double runs the same ledger-door
// validation as the real inbox: a key from no door is refused before anything is
// recorded. (The sealed refusal is unreachable from here — no exported function
// mints a sealed key.)
func TestMockInboxRefusesTheZeroKey(t *testing.T) {
	m := inboxtest.NewMockInbox()

	ran := false
	err := m.ProcessOnce(context.Background(), messaging.DedupKey{}, func(context.Context, dbtypes.Tx) error {
		ran = true
		return nil
	})
	require.ErrorIs(t, err, messaging.ErrInvalidEventID)
	assert.False(t, ran, "fn never runs for a refused key")
	inboxtest.AssertProcessCount(t, m, 0)
}

func TestMockInboxWithError(t *testing.T) {
	wantErr := errors.New("inbox down")
	m := inboxtest.NewMockInbox().WithError(wantErr)

	err := m.ProcessOnce(context.Background(), evt1, noopFn)
	inboxtest.AssertProcessCount(t, m, 0) // errored calls are not recorded as processed
	require.ErrorIs(t, err, wantErr)
}

func TestMockInboxPropagatesHandlerError(t *testing.T) {
	m := inboxtest.NewMockInbox()
	wantErr := errors.New("handler boom")

	err := m.ProcessOnce(context.Background(), evt1, func(context.Context, dbtypes.Tx) error {
		return wantErr
	})
	inboxtest.AssertProcessed(t, m, "evt-1") // the call is recorded
	inboxtest.AssertProcessCount(t, m, 1)
	require.ErrorIs(t, err, wantErr)
}

func TestMockInboxMarkAlreadyProcessed(t *testing.T) {
	m := inboxtest.NewMockInbox().MarkAlreadyProcessed("evt-1")

	ran := false
	require.NoError(t, m.ProcessOnce(context.Background(), evt1, func(context.Context, dbtypes.Tx) error {
		ran = true
		return nil
	}))
	assert.False(t, ran, "pre-marked id skips fn")
}

func TestMockInboxReset(t *testing.T) {
	m := inboxtest.NewMockInbox()
	require.NoError(t, m.ProcessOnce(context.Background(), evt1, noopFn))
	m.Reset()
	inboxtest.AssertProcessCount(t, m, 0)
}
