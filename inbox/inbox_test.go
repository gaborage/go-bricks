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

func wireKey(t *testing.T, id string) messaging.DedupKey {
	t.Helper()
	key, err := messaging.WireDedupKey(id)
	require.NoError(t, err)
	return key
}

// TestProcessOnceAdmitsWireKeysAtBothLengthBoundaries pins that a wire key the
// grammar admitted — a 128-byte one included — reaches the INSERT.
func TestProcessOnceAdmitsWireKeysAtBothLengthBoundaries(t *testing.T) {
	for name, id := range map[string]string{
		"single_byte":    "k",
		"max_length_128": strings.Repeat("k", 128),
	} {
		t.Run(name, func(t *testing.T) {
			db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
			db.ExpectTransaction().ExpectExec(`INSERT INTO gobricks_inbox`).WillReturnRowsAffected(1)
			in := newTestInbox(db)

			ran := false
			err := in.ProcessOnce(t.Context(), wireKey(t, id), func(context.Context, dbtypes.Tx) error {
				ran = true
				return nil
			})
			require.NoError(t, err)
			assert.True(t, ran)
		})
	}
}

// TestProcessOnceRefusesTheZeroKeyBeforeTheLedger pins the first refusal: the
// zero DedupKey came from no door. The TestDB carries no expectations, so a
// Begin would fail the test on its own.
func TestProcessOnceRefusesTheZeroKeyBeforeTheLedger(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	in := newTestInbox(db)

	ran := false
	err := in.ProcessOnce(t.Context(), messaging.DedupKey{}, func(context.Context, dbtypes.Tx) error {
		ran = true
		return nil
	})
	require.ErrorIs(t, err, messaging.ErrInvalidEventID)
	assert.False(t, ran, "fn never runs for a refused key")
	assert.Empty(t, db.ExecLog(), "no INSERT reaches the ledger for a refused key")
}

// TestProcessOnceRefusesTheSealedKeyShapeFromAHeader is the negative vector the
// grammar exists for: a publisher writes a literal `family:jti` — the sealed
// dedup key spelling — into x-outbox-event-id on an unsealed consumer. It cannot
// become a DedupKey at all, so it never reaches the ledger.
func TestProcessOnceRefusesTheSealedKeyShapeFromAHeader(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL) // no expectations: any Begin fails
	in := newTestInbox(db)

	calls := 0
	handler := messaging.NewTypedHandlerWithMeta("evt", func(ctx context.Context, _ testEvent, meta messaging.Metadata) error {
		id, ok := outbox.EventIDFromHeaders(meta.Headers())
		require.True(t, ok, "extraction is permissive; key construction is the gate")
		key, err := messaging.WireDedupKey(id)
		if err != nil {
			return err
		}
		return in.ProcessOnce(ctx, key, func(context.Context, dbtypes.Tx) error {
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

// TestProcessOnceAdmitsTheSealedKeyOnlyFromTheSealedDoor pins the provenance
// cross-check: the sealed door's own key passes inside its handler, the same
// key carried out of that context is refused before the ledger, and a wire key
// is admitted under the sealed context (ADR-097 §4: the grammar governs wire
// keys, which never collide with the sealed key space).
func TestProcessOnceAdmitsTheSealedKeyOnlyFromTheSealedDoor(t *testing.T) {
	t.Run("sealed_delivery", func(t *testing.T) {
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
		db.ExpectTransaction().ExpectExec(`INSERT INTO gobricks_inbox`).WillReturnRowsAffected(1)
		in := newTestInbox(db)
		ran := false
		err := runSealed(t, func(ctx context.Context, key messaging.DedupKey) error {
			return in.ProcessOnce(ctx, key, func(context.Context, dbtypes.Tx) error {
				ran = true
				return nil
			})
		})
		require.NoError(t, err)
		assert.True(t, ran)
	})

	t.Run("sealed_key_outside_the_sealed_delivery", func(t *testing.T) {
		var escaped messaging.DedupKey
		require.NoError(t, runSealed(t, func(_ context.Context, key messaging.DedupKey) error {
			escaped = key
			return nil
		}))
		require.True(t, escaped.Sealed())

		db := dbtesting.NewTestDB(dbtypes.PostgreSQL) // no expectations: any Begin fails
		in := newTestInbox(db)
		ran := false
		err := in.ProcessOnce(t.Context(), escaped, func(context.Context, dbtypes.Tx) error {
			ran = true
			return nil
		})
		require.ErrorIs(t, err, messaging.ErrInvalidEventID)
		assert.False(t, ran)
		assert.Empty(t, db.ExecLog())
	})

	t.Run("wire_key_under_the_sealed_delivery", func(t *testing.T) {
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
		db.ExpectTransaction().ExpectExec(`INSERT INTO gobricks_inbox`).WillReturnRowsAffected(1)
		in := newTestInbox(db)
		ran := false
		err := runSealed(t, func(ctx context.Context, _ messaging.DedupKey) error {
			return in.ProcessOnce(ctx, wireKey(t, "business-key-7"), func(context.Context, dbtypes.Tx) error {
				ran = true
				return nil
			})
		})
		require.NoError(t, err)
		assert.True(t, ran)
	})
}

func TestProcessOnceRunsFnOnFirstEvent(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).
		WillReturnRowsAffected(1)
	in := newTestInbox(db)

	ran := false
	err := in.ProcessOnce(t.Context(), wireKey(t, "evt-1"), func(context.Context, dbtypes.Tx) error {
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
	err := in.ProcessOnce(t.Context(), wireKey(t, "evt-1"), func(context.Context, dbtypes.Tx) error {
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
	err := in.ProcessOnce(t.Context(), wireKey(t, "evt-1"), func(context.Context, dbtypes.Tx) error {
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

	err := in.ProcessOnce(t.Context(), wireKey(t, "evt-1"), func(context.Context, dbtypes.Tx) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database unavailable")
}

// testEvent is a minimal outbox-shaped payload for the typed-consumer
// redelivery acceptance test.
type testEvent struct {
	Reference string `json:"reference" validate:"required"`
}

// TestProcessOnceViaTypedConsumerRedelivery proves the issue's acceptance
// criterion end-to-end: a typed consumer takes its key from
// messaging.Metadata.DedupKey and wraps its business logic in ProcessOnce, so
// the SAME delivery handled twice (an outbox at-least-once redelivery) runs the
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
		key, err := meta.DedupKey()
		require.NoError(t, err, "the outbox event id header must be present")
		return in.ProcessOnce(ctx, key, func(context.Context, dbtypes.Tx) error {
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

// TestProcessOnceViaTypedConsumerMessageIDOnly proves #1547's acceptance
// criterion end-to-end: a delivery from a producer that follows the standard
// without being go-bricks carries the message_id property and NO
// x-outbox-event-id stamp, and the same delivery handled twice still runs the
// business callback exactly once. The consumer reads the key through
// messaging.Metadata.DedupKey, which falls back to the property.
func TestProcessOnceViaTypedConsumerMessageIDOnly(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).WillReturnRowsAffected(1) // 1st delivery: inserted
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_inbox`).WillReturnRowsAffected(0) // redelivery: ON CONFLICT DO NOTHING
	in := newTestInbox(db)

	calls := 0
	var seen string
	handler := messaging.NewTypedHandlerWithMeta("evt", func(ctx context.Context, _ testEvent, meta messaging.Metadata) error {
		key, err := meta.DedupKey()
		require.NoError(t, err, "the message_id property must answer when no stamp is present")
		seen = key.String()
		return in.ProcessOnce(ctx, key, func(context.Context, dbtypes.Tx) error {
			calls++
			return nil
		})
	})

	delivery := &amqp.Delivery{
		Body:      []byte(`{"reference":"abc"}`),
		MessageId: "9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f",
	}

	require.NoError(t, handler.Handle(t.Context(), delivery))
	require.NoError(t, handler.Handle(t.Context(), delivery))
	assert.Equal(t, "9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f", seen, "the ledger is keyed on the property")
	assert.Equal(t, 1, calls, "the business callback runs exactly once across a redelivery")
}

// A sealed DedupKey is minted only by the sealed typed door. These helpers reach
// that door the way a consumer does: through DeclareTypedConsumerWithMeta on a
// seal-tagged type, with a stub codec standing in for messaging/sealed (which
// this package must not link).

type sealedEvent struct {
	_   struct{} `seal:"sign=svc-payments-sign,encrypt=acme-core-enc"`
	Ref string   `json:"ref" seal:"subject"`
}

type stubSpec struct{}

func (stubSpec) SignLogical() string    { return "svc-payments-sign" }
func (stubSpec) EncryptLogical() string { return "acme-core-enc" }

const (
	sealedTestFamily = "svc-payments-sign"
	sealedTestJTI    = "9f0c2b1e-3f4a-4c8d-9e1f-0a2b3c4d5e6f"
	// sealedKeySpelling is what Metadata.DedupKey composes from stubOpener's envelope.
	sealedKeySpelling = sealedTestFamily + ":" + sealedTestJTI
)

type stubOpener struct{}

func (stubOpener) Open(_ context.Context, _ []byte, _ messaging.SealTenantRule, out any) (messaging.SealEnvelope, error) {
	*out.(*sealedEvent) = sealedEvent{Ref: "abc"}
	return messaging.SealEnvelope{JTI: sealedTestJTI, SignFamily: sealedTestFamily}, nil
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

// runSealed runs body inside a handler the sealed typed door installed, handing
// it the handler's context (which carries the sealed-delivery marker) and the
// Sealed DedupKey the door composed.
func runSealed(t *testing.T, body func(ctx context.Context, key messaging.DedupKey) error) error {
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
		require.True(t, key.Sealed())
		result = body(ctx, key)
		return result
	})
	require.NoError(t, decls.Validate())
	err := opts.Handler.Handle(t.Context(), &amqp.Delivery{Body: []byte("a.b.c")})
	require.Equal(t, result, err)
	return result
}
