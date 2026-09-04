package inbox

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	kstest "github.com/gaborage/go-bricks/keystore/testing"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/outbox"
)

// newTestInbox builds an Inbox whose module resolves to the given test DB.
// AutoCreateTable is false; the logger is what the dedup-hit line writes to.
func newTestInbox(db dbtypes.Interface) *Inbox {
	m := &Module{
		cfg:    config.InboxConfig{Enabled: true, TableName: "gobricks_inbox"},
		getDB:  func(context.Context) (dbtypes.Interface, error) { return db, nil },
		logger: logger.New("info", false),
	}
	return &Inbox{module: m}
}

// TestProcessOnceValidatesTheIDBeforeTheLedger VARIES the id across the grammar
// ^[A-Za-z0-9_-]{1,128}$. Conforming ids — a 128-byte one included — reach the
// INSERT; every other shape is refused with messaging.ErrInvalidEventID before
// a transaction is opened, so nothing is written. The TestDB carries no
// expectations on the rejecting cases: a Begin would fail the test on its own.
func TestProcessOnceValidatesTheIDBeforeTheLedger(t *testing.T) {
	cases := []struct {
		name string
		id   string
		ok   bool
	}{
		{"uuid", "9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f", true},
		{"max_length_128", strings.Repeat("k", 128), true},
		{"colon", "order:1", false},
		{"newline", "evt-1\n", false},
		{"empty", "", false},
		{"length_129", strings.Repeat("k", 129), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
			if tc.ok {
				db.ExpectTransaction().ExpectExec(`INSERT INTO gobricks_inbox`).WillReturnRowsAffected(1)
			}
			in := newTestInbox(db)

			ran := false
			err := in.ProcessOnce(t.Context(), tc.id, func(context.Context, dbtypes.Tx) error {
				ran = true
				return nil
			})
			if tc.ok {
				require.NoError(t, err)
				assert.True(t, ran)
				return
			}
			require.ErrorIs(t, err, messaging.ErrInvalidEventID)
			assert.False(t, ran, "fn never runs for a refused id")
			assert.Empty(t, db.ExecLog(), "no INSERT reaches the ledger for a refused id")
		})
	}
}

// TestProcessOnceRefusesTheSealedKeyShapeFromAHeader is the negative vector the
// grammar exists for: a publisher writes a literal `family:jti` — the sealed
// dedup key spelling — into x-outbox-event-id on an unsealed consumer. It must
// not enter the ledger, or the legitimate sealed delivery would skip+ACK.
func TestProcessOnceRefusesTheSealedKeyShapeFromAHeader(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL) // no expectations: any Begin fails
	in := newTestInbox(db)

	calls := 0
	handler := messaging.NewTypedHandlerWithMeta("evt", func(ctx context.Context, _ testEvent, meta messaging.Metadata) error {
		id, ok := outbox.EventIDFromHeaders(meta.Headers())
		require.True(t, ok, "extraction is permissive; the ledger door is the gate")
		return in.ProcessOnce(ctx, id, func(context.Context, dbtypes.Tx) error {
			calls++
			return nil
		})
	})

	err := handler.Handle(t.Context(), &amqp.Delivery{
		Body:    []byte(`{"reference":"abc"}`),
		Headers: amqp.Table{outbox.HeaderEventID: "rsa:9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f"},
	})
	require.ErrorIs(t, err, messaging.ErrInvalidEventID)
	assert.Equal(t, 0, calls)
	assert.Empty(t, db.ExecLog(), "the sealed-shaped key never reaches the store")
	assert.NotContains(t, err.Error(), "9f0c2b1e", "the error carries the length, never the id")
}

// TestProcessOnceAdmitsTheSealedKeyOnlyFromTheSealedDoor pins the second door:
// a sealed dedup key passes only under a delivery the sealed typed door opened
// (messaging.IsSealedDelivery); the same spelling from any other context — a
// header, a hand-built key — is refused before the ledger.
func TestProcessOnceAdmitsTheSealedKeyOnlyFromTheSealedDoor(t *testing.T) {
	const key = "svc-payments-sign:9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f"

	t.Run("sealed_delivery", func(t *testing.T) {
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
		db.ExpectTransaction().ExpectExec(`INSERT INTO gobricks_inbox`).WillReturnRowsAffected(1)
		in := newTestInbox(db)
		ran := false
		err := runSealed(t, func(ctx context.Context) error {
			return in.ProcessOnce(ctx, key, func(context.Context, dbtypes.Tx) error {
				ran = true
				return nil
			})
		})
		require.NoError(t, err)
		assert.True(t, ran)
	})

	t.Run("plain_context", func(t *testing.T) {
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL) // no expectations: any Begin fails
		in := newTestInbox(db)
		err := in.ProcessOnce(t.Context(), key, func(context.Context, dbtypes.Tx) error { return nil })
		require.ErrorIs(t, err, messaging.ErrInvalidEventID)
		assert.Empty(t, db.ExecLog())
	})
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

// The ledger's second door — a sealed dedup key — is admitted only under a delivery
// the sealed typed door opened. These helpers reach that context the way a
// consumer does: through DeclareTypedConsumerWithMeta on a seal-tagged type, with a
// stub codec standing in for messaging/sealed (which this package must not link).

type sealedEvent struct {
	_   struct{} `seal:"sign=svc-payments-sign,encrypt=acme-core-enc"`
	Ref string   `json:"ref" seal:"subject"`
}

type stubSpec struct{}

func (stubSpec) SignLogical() string    { return "svc-payments-sign" }
func (stubSpec) EncryptLogical() string { return "acme-core-enc" }

type stubOpener struct{}

func (stubOpener) Open(_ context.Context, _ []byte, _ messaging.SealTenantRule, out any) (messaging.SealEnvelope, error) {
	*out.(*sealedEvent) = sealedEvent{Ref: "abc"}
	return messaging.SealEnvelope{JTI: "9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f", SignFamily: "svc-payments-sign"}, nil
}

type stubCodec struct{}

func (stubCodec) ScanType(t reflect.Type) (messaging.SealSpec, error) {
	if t == reflect.TypeOf(sealedEvent{}) {
		return stubSpec{}, nil
	}
	return nil, nil
}

func (stubCodec) NewSealer(messaging.SealSpec, string, *messaging.SealRuntime) (messaging.Sealer, error) {
	return nil, errors.New("producer side not under test")
}

func (stubCodec) NewOpener(messaging.SealSpec, string, *messaging.SealRuntime) (messaging.SealOpener, error) {
	return stubOpener{}, nil
}

var registerStubCodec sync.Once

// runSealed runs body inside a handler the sealed typed door installed, so its
// context carries the framework's sealed-delivery marker.
func runSealed(t *testing.T, body func(ctx context.Context) error) error {
	t.Helper()
	registerStubCodec.Do(func() { messaging.RegisterSealCodec(stubCodec{}) })
	messaging.ConfigureSealing(&messaging.SealRuntime{KeyStore: kstest.NewMockKeyStore()})

	decls := messaging.NewDeclarations()
	decls.DeclareQueue("q")
	opts := &messaging.ConsumerOptions{Queue: "q", Consumer: "c", EventType: "evt"}
	var result error
	messaging.DeclareTypedConsumerWithMeta(decls, opts, func(ctx context.Context, _ sealedEvent, meta messaging.Metadata) error {
		key, err := meta.DedupKey()
		require.NoError(t, err)
		require.True(t, messaging.IsSealedDedupKey(key))
		result = body(ctx)
		return result
	})
	require.NoError(t, decls.Validate())
	err := opts.Handler.Handle(t.Context(), &amqp.Delivery{Body: []byte("a.b.c")})
	require.Equal(t, result, err)
	return result
}
