package testing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gaborage/go-bricks/app"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	inboxtest "github.com/gaborage/go-bricks/inbox/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time guard: MockInbox satisfies the production interface.
var _ app.InboxProcessor = (*inboxtest.MockInbox)(nil)

func noopFn(context.Context, dbtypes.Tx) error { return nil }

func TestMockInboxRunsFnOncePerID(t *testing.T) {
	m := inboxtest.NewMockInbox()

	ran := 0
	fn := func(context.Context, dbtypes.Tx) error { ran++; return nil }

	require.NoError(t, m.ProcessOnce(context.Background(), "evt-1", fn))
	require.NoError(t, m.ProcessOnce(context.Background(), "evt-1", fn)) // duplicate

	assert.Equal(t, 1, ran, "fn runs once across duplicate event ids")
	inboxtest.AssertProcessCount(t, m, 2)
	inboxtest.AssertProcessed(t, m, "evt-1")
	inboxtest.AssertHandlerRan(t, m, "evt-1")
	inboxtest.AssertNotProcessed(t, m, "evt-2")
}

func TestMockInboxWithError(t *testing.T) {
	wantErr := errors.New("inbox down")
	m := inboxtest.NewMockInbox().WithError(wantErr)

	err := m.ProcessOnce(context.Background(), "evt-1", noopFn)
	assert.ErrorIs(t, err, wantErr)
	inboxtest.AssertProcessCount(t, m, 0) // errored calls are not recorded as processed
}

func TestMockInboxPropagatesHandlerError(t *testing.T) {
	m := inboxtest.NewMockInbox()
	wantErr := errors.New("handler boom")

	err := m.ProcessOnce(context.Background(), "evt-1", func(context.Context, dbtypes.Tx) error {
		return wantErr
	})
	assert.ErrorIs(t, err, wantErr)
	inboxtest.AssertProcessed(t, m, "evt-1") // the call is recorded
	inboxtest.AssertProcessCount(t, m, 1)
}

func TestMockInboxMarkAlreadyProcessed(t *testing.T) {
	m := inboxtest.NewMockInbox().MarkAlreadyProcessed("evt-1")

	ran := false
	require.NoError(t, m.ProcessOnce(context.Background(), "evt-1", func(context.Context, dbtypes.Tx) error {
		ran = true
		return nil
	}))
	assert.False(t, ran, "pre-marked id skips fn")
}

func TestMockInboxReset(t *testing.T) {
	m := inboxtest.NewMockInbox()
	require.NoError(t, m.ProcessOnce(context.Background(), "evt-1", noopFn))
	m.Reset()
	inboxtest.AssertProcessCount(t, m, 0)
}
