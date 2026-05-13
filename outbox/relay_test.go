package outbox

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/scheduler"
)

func TestDecodeHeadersEmpty(t *testing.T) {
	headers, err := decodeHeaders(nil)
	assert.NoError(t, err)
	assert.Nil(t, headers)
}

func TestDecodeHeadersValid(t *testing.T) {
	data := []byte(`{"x-priority":"high","x-source":"test"}`)
	headers, err := decodeHeaders(data)
	require.NoError(t, err)
	assert.Equal(t, "high", headers["x-priority"])
	assert.Equal(t, "test", headers["x-source"])
}

func TestDecodeHeadersInvalidJSON(t *testing.T) {
	data := []byte(`{invalid json}`)
	headers, err := decodeHeaders(data)
	assert.Error(t, err)
	assert.Nil(t, headers)
	assert.Contains(t, err.Error(), "invalid headers JSON")
}

// newRelayWithFakes wires a Relay with the supplied fake store and AMQP
// client. The getMessaging closure always returns the fake AMQP regardless
// of JobContext content.
func newRelayWithFakes(store *fakeStore, amqp *fakeAMQP) *Relay {
	return &Relay{
		store: store,
		config: config.OutboxConfig{
			BatchSize:  10,
			MaxRetries: 3,
		},
		getMessaging: func(_ scheduler.JobContext) messaging.AMQPClient { return amqp },
	}
}

func TestRelayExecuteReturnsErrorWhenDBUnavailable(t *testing.T) {
	r := newRelayWithFakes(&fakeStore{}, newFakeAMQP())
	ctx := newFakeJobCtx(nil, nil)

	err := r.Execute(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database not available")
}

func TestRelayExecuteReturnsErrorWhenMessagingUnavailable(t *testing.T) {
	r := &Relay{
		store:  &fakeStore{},
		config: config.OutboxConfig{BatchSize: 10, MaxRetries: 3},
		// getMessaging deliberately returns nil to simulate misconfigured tenant.
		getMessaging: func(_ scheduler.JobContext) messaging.AMQPClient { return nil },
	}
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, nil)

	err := r.Execute(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messaging not available")
}

func TestRelayExecuteReturnsErrorWhenMessagingNotReady(t *testing.T) {
	amqp := newFakeAMQP()
	amqp.Ready = false
	r := newRelayWithFakes(&fakeStore{}, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	err := r.Execute(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messaging client not ready")
}

func TestRelayExecuteWrapsFetchPendingError(t *testing.T) {
	store := &fakeStore{FetchPendingErr: errors.New("network drop")}
	r := newRelayWithFakes(store, newFakeAMQP())
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, nil)

	err := r.Execute(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch failed")
	assert.Contains(t, err.Error(), "network drop")
}

func TestRelayExecuteIsNoOpWhenNoPendingRecords(t *testing.T) {
	store := &fakeStore{FetchPendingResult: nil}
	r := newRelayWithFakes(store, newFakeAMQP())
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, nil)

	require.NoError(t, r.Execute(ctx))
	assert.Equal(t, 1, store.FetchPendingCalls)
	assert.Equal(t, 0, store.MarkPublishedCalls)
	assert.Equal(t, 0, store.MarkFailedCalls)
}

func TestRelayExecutePublishesPendingRecords(t *testing.T) {
	store := &fakeStore{
		FetchPendingResult: []Record{
			{ID: "evt-1", EventType: "order.created", Exchange: "orders", RoutingKey: "created", Payload: []byte(`{"id":1}`)},
			{ID: "evt-2", EventType: "order.shipped", Exchange: "orders", RoutingKey: "shipped", Payload: []byte(`{"id":2}`)},
		},
	}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	require.NoError(t, r.Execute(ctx))
	assert.Equal(t, 2, amqp.PublishCalls)
	assert.Equal(t, 2, store.MarkPublishedCalls)
	assert.Equal(t, 0, store.MarkFailedCalls)
}

func TestRelayExecuteCountsFailuresAndContinues(t *testing.T) {
	// Two records: first one fails to publish, second succeeds.
	store := &fakeStore{
		FetchPendingResult: []Record{
			{ID: "evt-1", Exchange: "orders", RoutingKey: "created"},
			{ID: "evt-2", Exchange: "orders", RoutingKey: "shipped"},
		},
	}
	amqp := newFakeAMQP()
	amqp.PublishErrFor = map[string]error{
		"orders:created": errors.New("broker rejected"),
	}
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	require.NoError(t, r.Execute(ctx), "Execute returns nil even when some publishes fail (per-record status is in the store)")
	assert.Equal(t, 2, amqp.PublishCalls)
	assert.Equal(t, 1, store.MarkPublishedCalls)
	assert.Equal(t, "evt-2", store.MarkPublishedLastID)
	assert.Equal(t, 1, store.MarkFailedCalls)
	assert.Equal(t, "evt-1", store.MarkFailedLastID)
	assert.Contains(t, store.MarkFailedLastErr, "broker rejected")
}

func TestPublishRecordMarksFailedOnInvalidHeaders(t *testing.T) {
	store := &fakeStore{}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	rec := &Record{ID: "evt-bad-hdr", Headers: []byte(`{not valid json}`)}
	ok := r.publishRecord(ctx, db, amqp, rec)

	assert.False(t, ok, "publishRecord returns false when headers can't be decoded")
	assert.Equal(t, 0, amqp.PublishCalls, "publish never attempted with bad headers")
	assert.Equal(t, 1, store.MarkFailedCalls)
	assert.Equal(t, "evt-bad-hdr", store.MarkFailedLastID)
	assert.Contains(t, store.MarkFailedLastErr, "invalid headers JSON")
}

func TestPublishRecordInjectsOutboxMetadataHeaders(t *testing.T) {
	store := &fakeStore{}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	rec := &Record{
		ID:         "evt-42",
		EventType:  "order.created",
		Exchange:   "orders",
		RoutingKey: "created",
		Headers:    []byte(`{"x-correlation-id":"abc"}`),
	}
	ok := r.publishRecord(ctx, db, amqp, rec)
	require.True(t, ok)

	require.NotNil(t, amqp.LastPublishHdrs)
	assert.Equal(t, "evt-42", amqp.LastPublishHdrs["x-outbox-event-id"])
	assert.Equal(t, "order.created", amqp.LastPublishHdrs["x-outbox-event-type"])
	assert.Equal(t, "abc", amqp.LastPublishHdrs["x-correlation-id"], "preserves caller-supplied headers")
}

func TestPublishRecordInjectsHeadersWhenRecordHasNone(t *testing.T) {
	// Empty/nil Headers should still result in a map containing the two
	// outbox metadata keys.
	store := &fakeStore{}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	rec := &Record{ID: "evt-7", EventType: "x.y", Exchange: "ex", RoutingKey: "rk"}
	require.True(t, r.publishRecord(ctx, db, amqp, rec))
	require.NotNil(t, amqp.LastPublishHdrs)
	assert.Equal(t, "evt-7", amqp.LastPublishHdrs["x-outbox-event-id"])
}

func TestPublishRecordReturnsFalseWhenMarkPublishedFails(t *testing.T) {
	store := &fakeStore{MarkPublishedErr: errors.New("db gone")}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	rec := &Record{ID: "evt-mp-fail", Exchange: "ex", RoutingKey: "rk"}
	ok := r.publishRecord(ctx, db, amqp, rec)

	assert.False(t, ok, "MarkPublished failure makes the cycle treat the record as failed")
	assert.Equal(t, 1, amqp.PublishCalls)
	assert.Equal(t, 1, store.MarkPublishedCalls)
	assert.Equal(t, 0, store.MarkFailedCalls, "MarkFailed not called when only MarkPublished failed")
}

func TestMarkRecordFailedLogsButDoesNotPanicOnStoreError(t *testing.T) {
	// Even if the store fails to record the failure, the relay must continue.
	store := &fakeStore{MarkFailedErr: errors.New("store unreachable")}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	require.NotPanics(t, func() {
		r.markRecordFailed(ctx, db, "evt-id", "publish err")
	})
	assert.Equal(t, 1, store.MarkFailedCalls)
}
