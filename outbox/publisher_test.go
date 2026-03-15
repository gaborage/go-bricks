package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/app"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	eventTypeOrderCreated = "order.created"
	aggregateOrderID      = "order-123"
	eventTypeTest         = "test.event"
	aggregateTest         = "agg-1"
)

// mockStore is a test double for Store that records Insert calls.
type mockStore struct {
	insertedRecords []*Record
	insertErr       error
}

func (s *mockStore) Insert(_ context.Context, _ dbtypes.Tx, record *Record) error {
	if s.insertErr != nil {
		return s.insertErr
	}
	s.insertedRecords = append(s.insertedRecords, record)
	return nil
}

func (s *mockStore) FetchPending(_ context.Context, _ dbtypes.Interface, _, _ int) ([]Record, error) {
	return nil, nil
}
func (s *mockStore) MarkPublished(_ context.Context, _ dbtypes.Interface, _ string) error {
	return nil
}
func (s *mockStore) MarkFailed(_ context.Context, _ dbtypes.Interface, _, _ string) error {
	return nil
}
func (s *mockStore) DeletePublished(_ context.Context, _ dbtypes.Interface, _ time.Time) (int64, error) {
	return 0, nil
}
func (s *mockStore) CreateTable(_ context.Context, _ dbtypes.Interface) error { return nil }

// mockTx implements dbtypes.Tx for testing (minimal no-op).
type mockTx struct{}

func (t *mockTx) Query(_ context.Context, _ string, _ ...any) (*sql.Rows, error) {
	return nil, nil
}
func (t *mockTx) QueryRow(_ context.Context, _ string, _ ...any) dbtypes.Row { return nil }
func (t *mockTx) Exec(_ context.Context, _ string, _ ...any) (sql.Result, error) {
	return nil, nil
}
func (t *mockTx) Commit(_ context.Context) error   { return nil }
func (t *mockTx) Rollback(_ context.Context) error { return nil }
func (t *mockTx) Prepare(_ context.Context, _ string) (dbtypes.Statement, error) {
	return nil, nil
}

func TestPublisherPublishBasicEvent(t *testing.T) {
	store := &mockStore{}
	pub := newPublisher(store, "default.exchange")

	event := &app.OutboxEvent{
		EventType:   eventTypeOrderCreated,
		AggregateID: aggregateOrderID,
		Payload:     map[string]string{"id": "123"},
		Exchange:    "order.events",
		RoutingKey:  eventTypeOrderCreated,
	}

	eventID, err := pub.Publish(context.Background(), &mockTx{}, event)

	require.NoError(t, err)
	assert.NotEmpty(t, eventID)
	require.Len(t, store.insertedRecords, 1)

	record := store.insertedRecords[0]
	assert.Equal(t, eventTypeOrderCreated, record.EventType)
	assert.Equal(t, aggregateOrderID, record.AggregateID)
	assert.Equal(t, "order.events", record.Exchange)
	assert.Equal(t, eventTypeOrderCreated, record.RoutingKey)
	assert.Equal(t, StatusPending, record.Status)
	assert.NotEmpty(t, record.ID)

	// Payload should be JSON
	var payload map[string]string
	require.NoError(t, json.Unmarshal(record.Payload, &payload))
	assert.Equal(t, "123", payload["id"])
}

func TestPublisherPublishWithDefaultExchange(t *testing.T) {
	store := &mockStore{}
	pub := newPublisher(store, "fallback.exchange")

	event := &app.OutboxEvent{
		EventType:   eventTypeOrderCreated,
		AggregateID: aggregateOrderID,
		Payload:     []byte(`{"id":"123"}`),
		// Exchange intentionally empty — should use default
	}

	eventID, err := pub.Publish(context.Background(), &mockTx{}, event)

	require.NoError(t, err)
	assert.NotEmpty(t, eventID)

	record := store.insertedRecords[0]
	assert.Equal(t, "fallback.exchange", record.Exchange)
	// RoutingKey should default to EventType
	assert.Equal(t, eventTypeOrderCreated, record.RoutingKey)
}

func TestPublisherPublishBytePayload(t *testing.T) {
	store := &mockStore{}
	pub := newPublisher(store, "")

	rawPayload := []byte(`{"raw":"data"}`)
	event := &app.OutboxEvent{
		EventType:   eventTypeTest,
		AggregateID: aggregateTest,
		Payload:     rawPayload,
	}

	_, err := pub.Publish(context.Background(), &mockTx{}, event)

	require.NoError(t, err)
	// []byte payload should be stored as-is
	assert.Equal(t, rawPayload, store.insertedRecords[0].Payload)
}

func TestPublisherPublishWithHeaders(t *testing.T) {
	store := &mockStore{}
	pub := newPublisher(store, "")

	event := &app.OutboxEvent{
		EventType:   eventTypeTest,
		AggregateID: aggregateTest,
		Payload:     []byte("{}"),
		Headers: map[string]any{
			"x-priority": "high",
			"x-source":   "test",
		},
	}

	_, err := pub.Publish(context.Background(), &mockTx{}, event)

	require.NoError(t, err)
	record := store.insertedRecords[0]
	assert.NotNil(t, record.Headers)

	var headers map[string]any
	require.NoError(t, json.Unmarshal(record.Headers, &headers))
	assert.Equal(t, "high", headers["x-priority"])
	assert.Equal(t, "test", headers["x-source"])
}

func TestPublisherPublishNilEvent(t *testing.T) {
	store := &mockStore{}
	pub := newPublisher(store, "")

	_, err := pub.Publish(context.Background(), &mockTx{}, nil)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "event must not be nil")
}

func TestPublisherPublishEmptyEventType(t *testing.T) {
	store := &mockStore{}
	pub := newPublisher(store, "")

	event := &app.OutboxEvent{
		AggregateID: aggregateTest,
		Payload:     []byte("{}"),
	}

	_, err := pub.Publish(context.Background(), &mockTx{}, event)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "event type must not be empty")
}

func TestPublisherPublishEmptyAggregateID(t *testing.T) {
	store := &mockStore{}
	pub := newPublisher(store, "")

	event := &app.OutboxEvent{
		EventType: eventTypeTest,
		Payload:   []byte("{}"),
	}

	_, err := pub.Publish(context.Background(), &mockTx{}, event)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "aggregate ID must not be empty")
}

func TestPublisherPublishStoreError(t *testing.T) {
	store := &mockStore{insertErr: assert.AnError}
	pub := newPublisher(store, "")

	event := &app.OutboxEvent{
		EventType:   eventTypeTest,
		AggregateID: aggregateTest,
		Payload:     []byte("{}"),
	}

	_, err := pub.Publish(context.Background(), &mockTx{}, event)

	assert.Error(t, err)
}

func TestPublisherPublishNilPayload(t *testing.T) {
	store := &mockStore{}
	pub := newPublisher(store, "")

	event := &app.OutboxEvent{
		EventType:   eventTypeTest,
		AggregateID: aggregateTest,
		Payload:     nil,
	}

	_, err := pub.Publish(context.Background(), &mockTx{}, event)

	require.NoError(t, err)
	assert.Equal(t, []byte("null"), store.insertedRecords[0].Payload)
}

func TestMarshalPayloadStruct(t *testing.T) {
	type TestPayload struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	data, err := marshalPayload(TestPayload{Name: "Alice", Age: 30})

	require.NoError(t, err)
	var result TestPayload
	require.NoError(t, json.Unmarshal(data, &result))
	assert.Equal(t, "Alice", result.Name)
	assert.Equal(t, 30, result.Age)
}
