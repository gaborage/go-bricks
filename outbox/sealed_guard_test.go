package outbox

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/app"
)

// sealedOutboxPayload and plainOutboxPayload differ ONLY in the seal sentinel,
// so the pair proves the refusal keys on the tag, not on the struct's shape.
type sealedOutboxPayload struct {
	_    struct{} `seal:"sign=svc-sign,encrypt=aud-enc"`
	ID   string   `json:"id"`
	Card string   `json:"card" seal:"subject"`
}

type plainOutboxPayload struct {
	ID   string `json:"id"`
	Card string `json:"card"`
}

// TestPublisherPublishRefusesSealTaggedStructPayload: a seal-tagged struct or
// pointer reaches Publish as plaintext and is refused with
// ErrSealedPayloadNeedsBytes before anything is stored; the untagged twin and a
// []byte body pass unchanged.
func TestPublisherPublishRefusesSealTaggedStructPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload any
		refused bool
	}{
		{name: "sealed_value", payload: sealedOutboxPayload{ID: "1", Card: "4111"}, refused: true},
		{name: "sealed_pointer", payload: &sealedOutboxPayload{ID: "1", Card: "4111"}, refused: true},
		{name: "plain_value", payload: plainOutboxPayload{ID: "1", Card: "4111"}},
		{name: "plain_pointer", payload: &plainOutboxPayload{ID: "1", Card: "4111"}},
		{name: "bytes_stored_as_is", payload: []byte(`{"sealed":"eyJ..."}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockStore{}
			pub := newPublisher(store, "", nil)
			event := &app.OutboxEvent{EventType: eventTypeTest, AggregateID: aggregateTest, Payload: tt.payload}

			_, err := pub.Publish(context.Background(), &mockTx{}, event)

			if tt.refused {
				require.ErrorIs(t, err, ErrSealedPayloadNeedsBytes)
				assert.Contains(t, err.Error(), "Publisher[T].Seal")
				assert.Contains(t, err.Error(), "sealedOutboxPayload")
				assert.Empty(t, store.insertedRecords, "a refused payload is never persisted")
				return
			}
			require.NoError(t, err)
			require.Len(t, store.insertedRecords, 1)
			if b, ok := tt.payload.([]byte); ok {
				assert.Equal(t, b, store.insertedRecords[0].Payload)
			}
		})
	}
}

// TestErrSealedPayloadNeedsBytesIsDistinct: the new sentinel is its own identity.
func TestErrSealedPayloadNeedsBytesIsDistinct(t *testing.T) {
	assert.NotErrorIs(t, ErrSealedPayloadNeedsBytes, ErrConflictingTargets)
}
