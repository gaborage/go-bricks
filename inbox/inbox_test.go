package inbox

import (
	"context"
	"errors"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/outbox"
)

// newTestInbox builds an Inbox whose module resolves to the given test DB.
// AutoCreateTable is false, so no logger is needed.
func newTestInbox(db dbtypes.Interface) *Inbox {
	m := &Module{
		cfg:   config.InboxConfig{Enabled: true, TableName: "gobricks_inbox"},
		getDB: func(context.Context) (dbtypes.Interface, error) { return db, nil },
	}
	return &Inbox{module: m}
}

func TestProcessOnceRunsFnOnFirstEvent(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).
		WillReturnRowsAffected(1)
	in := newTestInbox(db)

	ran := false
	err := in.ProcessOnce(t.Context(), "evt-1", func(context.Context, dbtypes.Tx) error {
		ran = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, ran, "fn runs on first occurrence of the event id")
}

func TestProcessOnceSkipsFnOnDuplicate(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	// ON CONFLICT DO NOTHING -> 0 rows affected -> already processed.
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).
		WillReturnRowsAffected(0)
	in := newTestInbox(db)

	ran := false
	err := in.ProcessOnce(t.Context(), "evt-1", func(context.Context, dbtypes.Tx) error {
		ran = true
		return nil
	})
	require.NoError(t, err)
	assert.False(t, ran, "fn is skipped when the event id was already processed")
}

func TestProcessOncePropagatesFnError(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).
		WillReturnRowsAffected(1)
	in := newTestInbox(db)

	sentinel := errors.New("handler failed")
	err := in.ProcessOnce(t.Context(), "evt-1", func(context.Context, dbtypes.Tx) error {
		return sentinel
	})
	assert.ErrorIs(t, err, sentinel, "a handler error rolls back and propagates")
}

func TestProcessOnceReturnsDBError(t *testing.T) {
	m := &Module{
		cfg:   config.InboxConfig{Enabled: true, TableName: "gobricks_inbox"},
		getDB: func(context.Context) (dbtypes.Interface, error) { return nil, errors.New("db down") },
	}
	in := &Inbox{module: m}

	err := in.ProcessOnce(t.Context(), "evt-1", func(context.Context, dbtypes.Tx) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database unavailable")
}

// testEvent is a minimal outbox-shaped payload for the typed-consumer
// redelivery acceptance test.
type testEvent struct {
	Reference string `json:"reference" validate:"required"`
}

// TestProcessOnceViaTypedConsumerRedelivery proves the issue's acceptance
// criterion end-to-end: a typed consumer reads x-outbox-event-id from
// messaging.Metadata and wraps its business logic in ProcessOnce, so the SAME
// delivery handled twice (an outbox at-least-once redelivery) runs the
// business callback exactly once.
func TestProcessOnceViaTypedConsumerRedelivery(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).WillReturnRowsAffected(1) // 1st delivery: inserted
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).WillReturnRowsAffected(0) // redelivery: ON CONFLICT DO NOTHING
	in := newTestInbox(db)

	calls := 0
	handler := messaging.NewTypedHandlerWithMeta("evt", func(ctx context.Context, _ testEvent, meta messaging.Metadata) error {
		id, ok := outbox.EventIDFromHeaders(meta.Headers())
		require.True(t, ok, "the outbox event id header must be present")
		return in.ProcessOnce(ctx, id, func(context.Context, dbtypes.Tx) error {
			calls++
			return nil
		})
	})

	delivery := &amqp.Delivery{
		Body:    []byte(`{"reference":"abc"}`),
		Headers: amqp.Table{outbox.HeaderEventID: "evt-1"},
	}

	require.NoError(t, handler.Handle(t.Context(), delivery))
	require.NoError(t, handler.Handle(t.Context(), delivery))
	assert.Equal(t, 1, calls, "the business callback runs exactly once across a redelivery")
}
