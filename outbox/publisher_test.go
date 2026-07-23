package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
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

func (s *mockStore) FetchPending(_ context.Context, _ dbtypes.Interface, _ int) ([]Record, error) {
	return nil, nil
}
func (s *mockStore) MarkPublished(_ context.Context, _ dbtypes.Interface, _ string) error {
	return nil
}
func (s *mockStore) MarkFailed(_ context.Context, _ dbtypes.Interface, _, _ string) error {
	return nil
}
func (s *mockStore) MarkDeadLettered(_ context.Context, _ dbtypes.Interface, _, _ string) error {
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

func TestPublisherPublishNilTransaction(t *testing.T) {
	store := &mockStore{}
	pub := newPublisher(store, "")

	event := &app.OutboxEvent{
		EventType:   eventTypeTest,
		AggregateID: aggregateTest,
		Payload:     []byte("{}"),
	}

	_, err := pub.Publish(context.Background(), nil, event)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "transaction must not be nil")
}

// The tests reuse the production mapHeaderAccessor (same package) to drive the
// centralized trace inject/extract helpers exactly as the AMQP layer does. An
// amqp.Delivery's Headers are an amqp.Table (also a map[string]any), so this is
// faithful to the real relay/consumer path.

// inboundTraceparent / inboundTraceID model an inbound W3C trace context: the
// trace-id is the 32-hex middle segment that the HTTP handler logs (site 1).
const (
	inboundTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	inboundTraceID     = "4bf92f3577b34da6a3ce929d0e0e4736"
)

// TestPublisherPublishPersistsTraceContext asserts that when the publish context
// carries an inbound W3C traceparent (as placed by server.TraceContext), Publish
// persists it into the outbox row's headers — the only place the originating
// request context is still live. Without this, the detached relay job has no
// trace to replay and the consumer sees a freshly generated id.
func TestPublisherPublishPersistsTraceContext(t *testing.T) {
	store := &mockStore{}
	pub := newPublisher(store, "")

	ctx := gobrickstrace.WithTraceParent(context.Background(), inboundTraceparent)

	event := &app.OutboxEvent{
		EventType:   eventTypeOrderCreated,
		AggregateID: aggregateOrderID,
		Payload:     []byte("{}"),
	}
	_, err := pub.Publish(ctx, &mockTx{}, event)
	require.NoError(t, err)

	require.Len(t, store.insertedRecords, 1)
	record := store.insertedRecords[0]
	require.NotEmpty(t, record.Headers, "trace context must be persisted in the outbox row")

	var headers map[string]any
	require.NoError(t, json.Unmarshal(record.Headers, &headers))
	assert.Equal(t, inboundTraceparent, headers[gobrickstrace.HeaderTraceParent],
		"persisted traceparent must match the inbound request traceparent")
	assert.Equal(t, inboundTraceID, headers[gobrickstrace.HeaderXRequestID],
		"persisted X-Request-ID must be force-aligned to the traceparent trace-id")
}

// TestPublisherPublishPreservesCallerHeadersWithTrace verifies trace capture does
// not clobber application-supplied headers and does not mutate the caller's map.
func TestPublisherPublishPreservesCallerHeadersWithTrace(t *testing.T) {
	store := &mockStore{}
	pub := newPublisher(store, "")

	callerHeaders := map[string]any{"x-priority": "high"}
	event := &app.OutboxEvent{
		EventType:   eventTypeTest,
		AggregateID: aggregateTest,
		Payload:     []byte("{}"),
		Headers:     callerHeaders,
	}
	ctx := gobrickstrace.WithTraceParent(context.Background(), inboundTraceparent)
	_, err := pub.Publish(ctx, &mockTx{}, event)
	require.NoError(t, err)

	var headers map[string]any
	require.NoError(t, json.Unmarshal(store.insertedRecords[0].Headers, &headers))
	assert.Equal(t, "high", headers["x-priority"], "caller headers must survive trace capture")
	assert.Equal(t, inboundTraceparent, headers[gobrickstrace.HeaderTraceParent])

	// The caller's map must not gain framework keys as a side effect.
	_, mutated := callerHeaders[gobrickstrace.HeaderTraceParent]
	assert.False(t, mutated, "Publish must not mutate the caller-supplied headers map")
}

// TestPublisherPublishPersistsTracestate verifies the optional W3C tracestate
// rides along with the traceparent into the persisted row when the inbound
// request carries it — matching the behavior documented in wiki/outbox.md.
func TestPublisherPublishPersistsTracestate(t *testing.T) {
	store := &mockStore{}
	pub := newPublisher(store, "")

	const tracestate = "vendor1=opaqueValue1,vendor2=opaqueValue2"
	ctx := gobrickstrace.WithTraceState(
		gobrickstrace.WithTraceParent(context.Background(), inboundTraceparent),
		tracestate,
	)

	event := &app.OutboxEvent{
		EventType:   eventTypeOrderCreated,
		AggregateID: aggregateOrderID,
		Payload:     []byte("{}"),
	}
	_, err := pub.Publish(ctx, &mockTx{}, event)
	require.NoError(t, err)

	var headers map[string]any
	require.NoError(t, json.Unmarshal(store.insertedRecords[0].Headers, &headers))
	assert.Equal(t, inboundTraceparent, headers[gobrickstrace.HeaderTraceParent])
	assert.Equal(t, tracestate, headers[gobrickstrace.HeaderTraceState],
		"optional tracestate must be persisted alongside traceparent")
}

// TestPublisherPublishNoTraceLeavesHeadersUnchanged guards the untraced path:
// a background publish with no trace context must not gain generated trace
// headers, preserving existing behavior (nil headers for header-less events).
func TestPublisherPublishNoTraceLeavesHeadersUnchanged(t *testing.T) {
	store := &mockStore{}
	pub := newPublisher(store, "")

	event := &app.OutboxEvent{
		EventType:   eventTypeTest,
		AggregateID: aggregateTest,
		Payload:     []byte("{}"),
	}
	_, err := pub.Publish(context.Background(), &mockTx{}, event)
	require.NoError(t, err)

	assert.Nil(t, store.insertedRecords[0].Headers,
		"untraced publish must not persist generated trace headers")
}

// TestOutboxTracePropagationContinuityAcrossThreeSites is the end-to-end
// continuity regression: the same trace id must appear at all three sites —
// (1) the HTTP handler's context, (2) the persisted outbox row, and (3) the
// consumer's message-scoped logger — even though the relay republishes from a
// detached background context with no ambient trace. This reproduces the bug:
// before the fix the consumer's correlation_id is a freshly generated random id.
func TestOutboxTracePropagationContinuityAcrossThreeSites(t *testing.T) {
	// Site 1 — HTTP handler: server.TraceContext places the inbound W3C
	// traceparent into the request context; server logs its trace-id.
	ctx := gobrickstrace.WithTraceParent(context.Background(), inboundTraceparent)

	// Site 2 — outbox publish persists the trace into the row.
	store := &mockStore{}
	pub := newPublisher(store, "")
	_, err := pub.Publish(ctx, &mockTx{}, &app.OutboxEvent{
		EventType:   eventTypeOrderCreated,
		AggregateID: aggregateOrderID,
		Payload:     []byte("{}"),
	})
	require.NoError(t, err)
	require.Len(t, store.insertedRecords, 1)
	record := store.insertedRecords[0]

	// Relay replay: decode the persisted headers, add outbox metadata, then
	// publish from a DETACHED background context. This mirrors
	// outbox.Relay.publishRecord -> messaging.preparePublishing ->
	// trace.InjectIntoHeaders, where the relay job carries no ambient trace.
	stored := map[string]any{}
	if len(record.Headers) > 0 {
		require.NoError(t, json.Unmarshal(record.Headers, &stored))
	}
	wire := map[string]any{}
	maps.Copy(wire, stored)
	wire[HeaderEventID] = record.ID
	gobrickstrace.InjectIntoHeaders(context.Background(), &mapHeaderAccessor{headers: wire})

	// Site 3 — consumer: messaging.Registry.processMessage derives the
	// per-message logger's correlation_id via ExtractFromHeaders + EnsureTraceID.
	consumerCtx := gobrickstrace.ExtractFromHeaders(context.Background(), &mapHeaderAccessor{headers: wire})
	consumerCorrelationID := gobrickstrace.EnsureTraceID(consumerCtx)

	assert.Equal(t, inboundTraceID, consumerCorrelationID,
		"consumer correlation_id must equal the inbound traceparent trace-id, not a fresh random id")
}

// TestMapHeaderAccessorNilMap exercises the defensive nil-map branches of the
// accessor's Get/Set (unreachable via marshalHeaders, which always passes a
// non-nil map, but part of the type's contract and mirrored from the AMQP
// accessor): Get returns nil, and Set lazily initializes the backing map.
func TestMapHeaderAccessorNilMap(t *testing.T) {
	a := &mapHeaderAccessor{} // nil backing map

	assert.Nil(t, a.Get("missing"), "Get on a nil map must return nil, not panic")

	a.Set("k", "v") // must lazily allocate the map
	assert.Equal(t, "v", a.Get("k"), "Set must initialize the map and store the value")
}

// TestSharedModePublishRejectsForeignTx guards the RunInSharedTx marker
// enforcement: a shared-tenancy module must reject Publish calls on any
// transaction that did not originate from RunInSharedTx, since dbtypes.Tx is
// opaque and the framework cannot otherwise verify the tx targets the
// control-plane database the relay polls.
func TestSharedModePublishRejectsForeignTx(t *testing.T) {
	m := &Module{
		cfg: config.OutboxConfig{Enabled: true, Tenancy: config.TenancyShared, TableName: "gobricks_outbox"},
	}
	pub := &lazyPublisher{module: m}

	event := &app.OutboxEvent{
		EventType:   eventTypeTest,
		AggregateID: aggregateTest,
		Payload:     []byte("{}"),
	}

	_, err := pub.Publish(context.Background(), &mockTx{}, event)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RunInSharedTx")
}

// TestRunInSharedTxPublishSucceeds verifies the happy path: RunInSharedTx
// begins a transaction on the shared DB, wraps it in the sharedTx marker, and
// Publish accepts it (the marker check passes) and inserts the outbox row.
func TestRunInSharedTxPublishSucceeds(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().
		ExpectExec(`INSERT INTO gobricks_outbox`).
		WillReturnRowsAffected(1)

	m := &Module{
		cfg:   config.OutboxConfig{Enabled: true, Tenancy: config.TenancyShared, TableName: "gobricks_outbox", DefaultExchange: "ex"},
		getDB: func(context.Context) (dbtypes.Interface, error) { return db, nil },
	}
	pub := &lazyPublisher{module: m}

	event := &app.OutboxEvent{
		EventType:   eventTypeTest,
		AggregateID: aggregateTest,
		Payload:     []byte("{}"),
		Exchange:    "ex",
	}

	var eventID string
	err := pub.RunInSharedTx(context.Background(), func(ctx context.Context, tx dbtypes.Tx) error {
		id, pubErr := pub.Publish(ctx, tx, event)
		eventID = id
		return pubErr
	})
	require.NoError(t, err)
	assert.NotEmpty(t, eventID)
}

// TestRunInSharedTxErrorsInPerTenantMode guards the mode gate: RunInSharedTx
// is only meaningful under tenancy=shared, so the default per-tenant tenancy
// must reject it rather than silently begin a transaction nobody expects.
func TestRunInSharedTxErrorsInPerTenantMode(t *testing.T) {
	m := &Module{
		cfg: config.OutboxConfig{Enabled: true, Tenancy: config.TenancyPerTenant},
	}
	pub := &lazyPublisher{module: m}

	err := pub.RunInSharedTx(context.Background(), func(context.Context, dbtypes.Tx) error {
		return nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires outbox.tenancy=shared")
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
