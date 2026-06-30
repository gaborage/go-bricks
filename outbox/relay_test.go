package outbox

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
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

// newRelayWithFakes wires a single-tenant Relay with the supplied fake store and AMQP
// client. tenants is [""], so multitenant.SetTenant is a no-op; getDB reads the db from a
// context value (dbFromCtx) stashed by newFakeJobCtx, which survives the per-tenant lease
// scope's context wrapping (ADR-032).
func newRelayWithFakes(store *fakeStore, amqp *fakeAMQP) *Relay {
	return &Relay{
		store: store,
		config: config.OutboxConfig{
			BatchSize:      10,
			MaxRetries:     3,
			PublishTimeout: 5 * time.Second,
		},
		getDB: func(ctx context.Context) (dbtypes.Interface, error) {
			return dbFromCtx(ctx), nil
		},
		getMessaging: func(context.Context) (messaging.AMQPClient, error) { return amqp, nil },
		tenants:      []string{""},
	}
}

func TestRelayExecuteReturnsErrorWhenDBUnavailable(t *testing.T) {
	r := newRelayWithFakes(&fakeStore{}, newFakeAMQP())
	ctx := newFakeJobCtx(nil, nil)

	err := r.Execute(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database not available")
}

// TestRelayAdvancesRetryCountWhenMessagingResolverReturnsNil: a tenant whose broker
// cannot be resolved (nil client) is treated as unreachable — every pending record's
// retry_count advances rather than the whole cycle erroring out and freezing the count.
func TestRelayAdvancesRetryCountWhenMessagingResolverReturnsNil(t *testing.T) {
	db := dbtesting.NewTestDB("postgresql")
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "evt-1", Exchange: "ex", RoutingKey: "rk"},
		{ID: "evt-2", Exchange: "ex", RoutingKey: "rk"},
	}}
	r := &Relay{
		store:        store,
		config:       config.OutboxConfig{BatchSize: 10, MaxRetries: 3, PublishTimeout: 5 * time.Second},
		getDB:        func(context.Context) (dbtypes.Interface, error) { return db, nil },
		getMessaging: func(context.Context) (messaging.AMQPClient, error) { return nil, nil },
		tenants:      []string{""},
	}
	ctx := newFakeJobCtx(db, nil)

	err := r.Execute(ctx)
	require.Error(t, err, "an unusable broker with pending work surfaces as a job error")
	assert.Contains(t, err.Error(), "messaging not ready")
	assert.Equal(t, 2, store.MarkFailedCalls, "retry_count still advances for every record before the cycle reports failure")
	assert.Equal(t, 0, store.MarkPublishedCalls)
	assert.Equal(t, 0, store.MarkDeadLetteredCalls, "an unreachable broker is connectivity — never parked")
}

// TestRelayAdvancesRetryCountWhenBrokerNotReady is the direct regression test for the
// reported bug: when the broker is not ready the relay used to early-return and the
// retry_count stayed frozen. Now every pending record's retry_count advances per cycle.
func TestRelayAdvancesRetryCountWhenBrokerNotReady(t *testing.T) {
	amqp := newFakeAMQP()
	amqp.Ready = false
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "evt-1", Exchange: "ex", RoutingKey: "rk"},
		{ID: "evt-2", Exchange: "ex", RoutingKey: "rk"},
	}}
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	err := r.Execute(ctx)
	require.Error(t, err, "a not-ready broker with pending work surfaces as a job error (preserves the failure signal)")
	assert.Contains(t, err.Error(), "messaging not ready")
	assert.Equal(t, 2, store.MarkFailedCalls, "retry_count still advances for every record while the broker is down (the reported bug)")
	assert.Equal(t, 0, amqp.PublishCalls, "no publish is attempted when the broker is not ready")
	assert.Equal(t, 0, store.MarkDeadLetteredCalls)
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
	out := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec)

	assert.Equal(t, outcomeFailed, out, "corrupt headers are a (poison) failure")
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
	out := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec)
	require.Equal(t, outcomePublished, out)

	require.NotNil(t, amqp.LastPublishHdrs)
	assert.Equal(t, "evt-42", amqp.LastPublishHdrs[HeaderEventID])
	assert.Equal(t, "order.created", amqp.LastPublishHdrs[HeaderEventType])
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
	require.Equal(t, outcomePublished, r.publishRecord(ctx, ctx.Logger(), db, amqp, rec))
	require.NotNil(t, amqp.LastPublishHdrs)
	assert.Equal(t, "evt-7", amqp.LastPublishHdrs[HeaderEventID])
}

func TestPublishRecordReturnsFalseWhenMarkPublishedFails(t *testing.T) {
	store := &fakeStore{MarkPublishedErr: errors.New("db gone")}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	rec := &Record{ID: "evt-mp-fail", Exchange: "ex", RoutingKey: "rk"}
	out := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec)

	assert.Equal(t, outcomePublishedUnrecorded, out, "the message WAS delivered; a MarkPublished failure must not bump retry_count")
	assert.Equal(t, 1, amqp.PublishCalls)
	assert.Equal(t, 1, store.MarkPublishedCalls)
	assert.Equal(t, 0, store.MarkFailedCalls, "MarkFailed not called when only MarkPublished failed")
}

// TestPublishRecordRehydratesTraceContextForPublish asserts that the relay
// reconstructs the originating trace context from the persisted row headers and
// publishes with it. Without this, preparePublishing runs under the relay's
// trace-less background context and stamps the AMQP CorrelationId (which the
// consumer's failure-path logger and consume span surface as correlation_id)
// with a freshly generated UUID, breaking continuity precisely on the error path.
func TestPublishRecordRehydratesTraceContextForPublish(t *testing.T) {
	store := &fakeStore{}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	// A row as persisted by Publish: headers carry the originating trace context.
	rec := &Record{
		ID:         "evt-trace",
		EventType:  "order.created",
		Exchange:   "orders",
		RoutingKey: "created",
		Headers:    []byte(`{"traceparent":"` + inboundTraceparent + `","X-Request-ID":"` + inboundTraceID + `"}`),
	}
	require.Equal(t, outcomePublished, r.publishRecord(ctx, ctx.Logger(), db, amqp, rec))

	require.NotNil(t, amqp.LastPublishCtx, "publish context must be captured")
	tp, ok := gobrickstrace.ParentFromContext(amqp.LastPublishCtx)
	assert.True(t, ok, "publish context must carry the persisted traceparent")
	assert.Equal(t, inboundTraceparent, tp)
	assert.Equal(t, inboundTraceID, gobrickstrace.EnsureTraceID(amqp.LastPublishCtx),
		"publish context trace id must be the originating trace id, not a fresh one")
}

func TestMarkRecordFailedLogsButDoesNotPanicOnStoreError(t *testing.T) {
	// Even if the store fails to record the failure, the relay must continue.
	store := &fakeStore{MarkFailedErr: errors.New("store unreachable")}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	require.NotPanics(t, func() {
		r.markRecordFailed(ctx, ctx.Logger(), db, "evt-id", "publish err")
	})
	assert.Equal(t, 1, store.MarkFailedCalls)
}

// TestRelayExecuteFansOutAcrossStaticTenants verifies the multi-tenant fix: the relay
// resolves the database once per configured tenant (with that tenant injected into the
// context) and relays each tenant's pending events — rather than the prior tenant-less
// resolution that returned ErrNoTenantInContext and relayed nothing.
func TestRelayExecuteFansOutAcrossStaticTenants(t *testing.T) {
	var resolved []string
	store := &fakeStore{FetchPendingResult: []Record{{ID: "e1", Exchange: "ex", RoutingKey: "rk"}}}
	amqp := newFakeAMQP()
	r := &Relay{
		store:  store,
		config: config.OutboxConfig{BatchSize: 10, MaxRetries: 3, PublishTimeout: 5 * time.Second},
		getDB: func(ctx context.Context) (dbtypes.Interface, error) {
			tid, _ := multitenant.GetTenant(ctx)
			resolved = append(resolved, tid)
			return dbtesting.NewTestDB("postgresql"), nil
		},
		getMessaging: func(context.Context) (messaging.AMQPClient, error) { return amqp, nil },
		tenants:      []string{"tenant-a", "tenant-b"},
	}
	ctx := newFakeJobCtx(nil, amqp)

	require.NoError(t, r.Execute(ctx))
	assert.Equal(t, []string{"tenant-a", "tenant-b"}, resolved, "relay must resolve the DB once per configured tenant, in order")
	assert.Equal(t, 2, store.FetchPendingCalls, "FetchPending runs once per tenant")
	assert.Equal(t, 2, amqp.PublishCalls, "each tenant's pending record is published")
}

// TestRelayExecuteIsolatesPerTenantFailures verifies one unhealthy tenant does not block
// the others: its error is collected (naming the tenant) while healthy tenants still run.
func TestRelayExecuteIsolatesPerTenantFailures(t *testing.T) {
	store := &fakeStore{}
	amqp := newFakeAMQP()
	r := &Relay{
		store:  store,
		config: config.OutboxConfig{BatchSize: 10, MaxRetries: 3, PublishTimeout: 5 * time.Second},
		getDB: func(ctx context.Context) (dbtypes.Interface, error) {
			if tid, _ := multitenant.GetTenant(ctx); tid == "bad" {
				return nil, errors.New("tenant db down")
			}
			return dbtesting.NewTestDB("postgresql"), nil
		},
		getMessaging: func(context.Context) (messaging.AMQPClient, error) { return amqp, nil },
		tenants:      []string{"good", "bad"},
	}
	ctx := newFakeJobCtx(nil, amqp)

	err := r.Execute(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `tenant "bad"`)
	assert.Contains(t, err.Error(), "tenant db down")
	assert.Equal(t, 1, store.FetchPendingCalls, "the healthy tenant is still relayed despite the other failing")
}

// --- Status-driven parking: poison (corrupt) vs connectivity (everything else) ---

// TestRelayDeadLettersPoisonAtMaxRetries: the ONLY genuine poison is an undecodable
// (broker-independent) message — corrupt headers. At MaxRetries it is dead-lettered to
// status=failed rather than retried forever.
func TestRelayDeadLettersPoisonAtMaxRetries(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "poison", Headers: []byte(`{not valid json}`), RetryCount: 2}, // MaxRetries-1
	}}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	require.NoError(t, r.Execute(ctx))
	assert.Equal(t, 0, amqp.PublishCalls, "undecodable headers never reach the broker")
	assert.Equal(t, 1, store.MarkDeadLetteredCalls, "corrupt-header poison at MaxRetries is parked as failed")
	assert.Equal(t, "poison", store.MarkDeadLetteredLastID)
	assert.Equal(t, 0, store.MarkFailedCalls)
}

// TestRelayMarksNackAsConnectivityNeverParks: a broker NACK is a transient broker condition
// (disk alarm, mirror resync, failover) and a missing exchange surfaces as a synthesized NACK
// — both are CONNECTIVITY, so they advance retry_count and are NEVER dead-lettered, even past
// MaxRetries. This is the at-least-once guarantee for recoverable broker faults.
func TestRelayMarksNackAsConnectivityNeverParks(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "nacked", Exchange: "ex", RoutingKey: "rk", RetryCount: 99}, // well past MaxRetries
	}}
	amqp := newFakeAMQP()
	amqp.PublishErrFor = map[string]error{
		"ex:rk": fmt.Errorf("%w after 5 attempts: %w", messaging.ErrPublishRetriesExhausted, messaging.ErrPublishNacked),
	}
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	require.NoError(t, r.Execute(ctx))
	assert.Equal(t, 1, store.MarkFailedCalls, "a NACK advances retry_count (connectivity)")
	assert.Equal(t, 0, store.MarkDeadLetteredCalls, "a NACK never parks, no matter the count")
}

// TestRelayNeverDeadLettersConnectivityEvenPastMaxRetries guards the locked decision:
// a prolonged outage must never park a healthy event, even once its (outage-inflated)
// retry_count is well past MaxRetries.
func TestRelayNeverDeadLettersConnectivityEvenPastMaxRetries(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "evt", Exchange: "ex", RoutingKey: "rk", RetryCount: 99},
	}}
	amqp := newFakeAMQP()
	amqp.PublishErrFor = map[string]error{"ex:rk": messaging.ErrPublishConfirmTimeout}
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	require.NoError(t, r.Execute(ctx))
	assert.Equal(t, 1, store.MarkFailedCalls, "connectivity advances retry_count")
	assert.Equal(t, 0, store.MarkDeadLetteredCalls, "connectivity never parks, no matter the count")
}

// TestRelayShutdownDuringPublishDoesNotInflateRetryCount guards finding S4: a publish
// interrupted by shutdown (ErrShutdown / context.Canceled) must NOT advance retry_count,
// and stops the batch cleanly.
func TestRelayShutdownDuringPublishDoesNotInflateRetryCount(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "evt-1", Exchange: "ex", RoutingKey: "rk"},
		{ID: "evt-2", Exchange: "ex", RoutingKey: "rk"},
	}}
	amqp := newFakeAMQP()
	amqp.PublishErr = messaging.ErrShutdown
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	require.NoError(t, r.Execute(ctx))
	assert.Equal(t, 0, store.MarkFailedCalls, "shutdown must not inflate retry_count")
	assert.Equal(t, 0, store.MarkDeadLetteredCalls)
	assert.Equal(t, 1, amqp.PublishCalls, "the batch stops at the first shutdown-aborted record")
}

// TestRelayPerRecordPublishTimeoutDoesNotStarveBatch guards finding S1: one stuck record
// is bounded by PublishTimeout (DeadlineExceeded -> connectivity -> MarkFailed) and does
// NOT prevent the rest of the batch from publishing.
func TestRelayPerRecordPublishTimeoutDoesNotStarveBatch(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "stuck", Exchange: "ex", RoutingKey: "slow"},
		{ID: "healthy", Exchange: "ex", RoutingKey: "fast"},
	}}
	amqp := newFakeAMQP()
	amqp.PublishBlock = map[string]bool{"ex:slow": true}
	r := newRelayWithFakes(store, amqp)
	r.config.PublishTimeout = 30 * time.Millisecond
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	require.NoError(t, r.Execute(ctx))
	assert.Equal(t, 2, amqp.PublishCalls, "the healthy record is still attempted after the stuck one times out")
	assert.Equal(t, 1, store.MarkPublishedCalls, "the healthy record publishes")
	assert.Equal(t, "healthy", store.MarkPublishedLastID)
	assert.Equal(t, 1, store.MarkFailedCalls, "the stuck record times out and advances retry_count (connectivity)")
	assert.Equal(t, "stuck", store.MarkFailedLastID)
}
