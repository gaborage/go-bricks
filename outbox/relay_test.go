package outbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	out, outErr := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec)

	assert.Equal(t, outcomeFailed, out, "corrupt headers are a (poison) failure")
	assert.NoError(t, outErr)
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
	out, outErr := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec)
	require.Equal(t, outcomePublished, out)
	require.NoError(t, outErr)

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
	out, outErr := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec)
	require.Equal(t, outcomePublished, out)
	require.NoError(t, outErr)
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
	out, outErr := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec)

	assert.Equal(t, outcomePublishedUnrecorded, out, "the message WAS delivered; a MarkPublished failure must not bump retry_count")
	assert.NoError(t, outErr)
	assert.Equal(t, 1, amqp.PublishCalls)
	assert.Equal(t, 1, store.MarkPublishedCalls)
	assert.Equal(t, 0, store.MarkFailedCalls, "MarkFailed not called when only MarkPublished failed")
}

// TestPublishRecordRehydratesTraceContextForPublish asserts that the relay
// reconstructs the originating trace context from the persisted row headers and
// publishes with it. Without this, preparePublishing runs under the relay's
// trace-less background context and stamps the AMQP CorrelationId (which the
// consumer's failure-path logger surfaces as amqp_correlation_id and the
// consume span as messaging.message.conversation_id)
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
	out, outErr := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec)
	require.Equal(t, outcomePublished, out)
	require.NoError(t, outErr)

	require.NotNil(t, amqp.LastPublishCtx, "publish context must be captured")
	tp, ok := gobrickstrace.ParentFromContext(amqp.LastPublishCtx)
	assert.True(t, ok, "publish context must carry the persisted traceparent")
	assert.Equal(t, inboundTraceparent, tp)
	assert.Equal(t, inboundTraceID, gobrickstrace.EnsureTraceID(amqp.LastPublishCtx),
		"publish context trace id must be the originating trace id, not a fresh one")
}

// TestPublishRecordDoesNotReEmitAPersistedMalformedTraceParent is #1121's second
// reacher: a row written before the ingress seam existed carries whatever
// traceparent the caller planted, and the relay publishes that persisted map
// verbatim — ExtractFromHeaders only sanitizes the CONTEXT it derives from it.
//
// The neutralization therefore happens one layer down, where the AMQP client
// injects over the map it was handed, so this test runs that same injection over
// the captured publish arguments rather than asserting on the raw map. Before
// #1121 the poisoned value survived that injection and went on the wire.
func TestPublishRecordDoesNotReEmitAPersistedMalformedTraceParent(t *testing.T) {
	const persisted = "00-!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!-00f067aa0ba902b7-01"
	store := &fakeStore{}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	rec := &Record{
		ID:         "evt-poisoned",
		EventType:  "order.created",
		Exchange:   "orders",
		RoutingKey: "created",
		Headers:    []byte(`{"traceparent":"` + persisted + `"}`),
	}
	out, outErr := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec)
	require.Equal(t, outcomePublished, out)
	require.NoError(t, outErr)

	require.NotNil(t, amqp.LastPublishHdrs)
	require.NotNil(t, amqp.LastPublishCtx)
	gobrickstrace.InjectIntoHeaders(amqp.LastPublishCtx, &mapHeaderAccessor{headers: amqp.LastPublishHdrs})

	emitted, ok := amqp.LastPublishHdrs[gobrickstrace.HeaderTraceParent].(string)
	require.True(t, ok)
	assert.NotEqual(t, persisted, emitted, "the persisted value must not go back on the wire")
	assert.Equal(t, emitted, gobrickstrace.ValidateTraceParent(emitted), "the emitted traceparent is well-formed")
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

// TestRelayDeadLettersInvalidPublishDestinationAtMaxRetries: a publish refused with
// messaging.ErrInvalidPublishDestination is message-intrinsic — the frame is unwritable
// whatever the broker's state — so it is the second poison class and parks at MaxRetries
// rather than being re-attempted for the life of the table.
func TestRelayDeadLettersInvalidPublishDestinationAtMaxRetries(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "unpublishable", Exchange: "ex", RoutingKey: "rk", RetryCount: 99}, // past MaxRetries
	}}
	amqp := newFakeAMQP()
	amqp.PublishErrFor = map[string]error{
		"ex:rk": fmt.Errorf("%w: routing key is 256 bytes, limit is 255", messaging.ErrInvalidPublishDestination),
	}
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	require.NoError(t, r.Execute(ctx))
	assert.Equal(t, 1, amqp.PublishCalls, "the record is attempted once this cycle")
	assert.Equal(t, 1, store.MarkDeadLetteredCalls, "an unpublishable destination parks at MaxRetries")
	assert.Equal(t, "unpublishable", store.MarkDeadLetteredLastID)
	assert.Equal(t, 0, store.MarkFailedCalls)
}

// TestRelayKeepsInvalidPublishDestinationPendingBelowMaxRetries: below the ceiling the
// record only advances retry_count and stays pending, the same shape the decode-poison
// path already has.
func TestRelayKeepsInvalidPublishDestinationPendingBelowMaxRetries(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "unpublishable", Exchange: "ex", RoutingKey: "rk"}, // RetryCount 0, MaxRetries 3
	}}
	amqp := newFakeAMQP()
	amqp.PublishErrFor = map[string]error{
		"ex:rk": fmt.Errorf("%w: exchange is 256 bytes, limit is 255", messaging.ErrInvalidPublishDestination),
	}
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	require.NoError(t, r.Execute(ctx))
	assert.Equal(t, 1, store.MarkFailedCalls, "below the ceiling retry_count advances and the record stays pending")
	assert.Equal(t, 0, store.MarkDeadLetteredCalls)
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

// TestRelayStopsBatchWhenBrokerDropsMidBatch guards the fix for a mid-batch broker drop:
// before this fix, once the broker dropped after the cycle-start IsReady() gate had
// already passed, every REMAINING record in the batch paid its own serial readiness
// pre-flight wait inside PublishToExchange (BatchSize x readyTimeout stall). Now the
// relay detects the drop on the record whose publish fails with ErrNotConnected AND
// IsReady() still false, and routes the unattempted remainder through the same
// no-publish outage path the cycle-start gate uses — stopping the loop immediately.
func TestRelayStopsBatchWhenBrokerDropsMidBatch(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "evt-1", Exchange: "ex", RoutingKey: "rk1"},
		{ID: "evt-2", Exchange: "ex", RoutingKey: "rk2"},
		{ID: "evt-3", Exchange: "ex", RoutingKey: "rk3"},
	}}
	amqp := newFakeAMQP()
	amqp.PublishErrFor = map[string]error{
		"ex:rk2": messaging.ErrNotConnected,
	}
	amqp.PublishHook = func(f *fakeAMQP) {
		if f.PublishCalls == 2 {
			// Simulate the broker dropping connectivity exactly as record 2's
			// publish is about to fail with ErrNotConnected.
			f.Ready = false
		}
	}
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	err := r.Execute(ctx)
	require.Error(t, err, "the mid-batch outage surfaces as a job-level error, like the cycle-start path")
	assert.Contains(t, err.Error(), "messaging not available")

	assert.Equal(t, 2, amqp.PublishCalls, "the loop stops after record 2's connectivity failure — record 3 is never attempted")
	assert.Equal(t, 1, store.MarkPublishedCalls, "record 1 published normally before the drop")
	assert.Equal(t, "evt-1", store.MarkPublishedLastID)
	assert.Equal(t, 2, store.MarkFailedCalls, "record 2 (the failed attempt) and record 3 (the outage remainder) both advance retry_count")
	assert.Equal(t, 0, store.MarkDeadLetteredCalls)
}

// TestRelayMidBatchDropAccountingSumsToTotal guards the cycle-accounting invariant for
// the mid-batch broker-drop path: the outage remainder routed through markOutage is
// marked failed in the DB, so it must be reflected in the batch result too — otherwise
// logCycle reports published+unrecorded+failed+deadlettered < total whenever the drop
// isn't on the last record. Tests runRelayLoop directly since relayBatchResult is the
// seam that feeds logCycle's arguments (Execute discards it and the test logger is
// disabled, so there is no log-capture seam in this file).
func TestRelayMidBatchDropAccountingSumsToTotal(t *testing.T) {
	records := []Record{
		{ID: "evt-1", Exchange: "ex", RoutingKey: "rk1"},
		{ID: "evt-2", Exchange: "ex", RoutingKey: "rk2"},
		{ID: "evt-3", Exchange: "ex", RoutingKey: "rk3"},
	}
	store := &fakeStore{}
	amqp := newFakeAMQP()
	amqp.PublishErrFor = map[string]error{
		"ex:rk2": messaging.ErrNotConnected,
	}
	amqp.PublishHook = func(f *fakeAMQP) {
		if f.PublishCalls == 2 {
			f.Ready = false
		}
	}
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	res := r.runRelayLoop(ctx, ctx.Logger(), db, amqp, records)

	assert.Equal(t, 1, res.published, "record 1 published before the drop")
	assert.Equal(t, 0, res.unrecorded)
	assert.Equal(t, 0, res.deadlettered)
	assert.Equal(t, 2, res.failed, "record 2 (failed attempt) AND record 3 (outage remainder) both count as failed")
	assert.ErrorIs(t, res.outageErr, messaging.ErrNotConnected)
	sum := res.published + res.unrecorded + res.failed + res.deadlettered
	assert.Equal(t, len(records), sum, "cycle accounting must sum to the batch total")
	assert.Equal(t, res.failed, store.MarkFailedCalls, "result count matches what was actually marked failed in the DB")
}

// TestRelayContinuesBatchWhenNotConnectedButStillReady locks in the "AND IsReady()"
// half of the mid-batch-drop detection: an ErrNotConnected on its own (e.g. a stray
// error classification, or a flap that already recovered) must NOT stop the batch
// when the client reports ready again by the time the check runs — that is an
// ordinary per-record failure, not a broker-down condition.
func TestRelayContinuesBatchWhenNotConnectedButStillReady(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "evt-1", Exchange: "ex", RoutingKey: "rk1"},
		{ID: "evt-2", Exchange: "ex", RoutingKey: "rk2"},
	}}
	amqp := newFakeAMQP()
	amqp.PublishErrFor = map[string]error{
		"ex:rk1": messaging.ErrNotConnected,
	}
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	require.NoError(t, r.Execute(ctx), "IsReady() stayed true, so this is an ordinary failure — no job-level outage error")
	assert.Equal(t, 2, amqp.PublishCalls, "record 2 is still attempted despite record 1's ErrNotConnected")
	assert.Equal(t, 1, store.MarkPublishedCalls)
	assert.Equal(t, "evt-2", store.MarkPublishedLastID)
	assert.Equal(t, 1, store.MarkFailedCalls)
	assert.Equal(t, "evt-1", store.MarkFailedLastID)
}

func TestBoundPersistedErrorMakesArbitraryTextSafeToStore(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantExact string
		truncated bool
	}{
		{name: "short_text_passes_through", in: "broker rejected", wantExact: "broker rejected"},
		{name: "empty_stays_empty", in: "", wantExact: ""},
		// The column is read back into logs and dashboards, so a broker-supplied
		// newline must not be able to forge a line there.
		{
			name:      "control_bytes_become_spaces",
			in:        "publish failed\r\nlevel=error msg=\"forged\"\x00",
			wantExact: "publish failed  level=error msg=\"forged\" ",
		},
		// PostgreSQL rejects invalid UTF-8 outright: the UPDATE would fail and
		// retry_count would never advance, retrying forever over text nobody reads.
		{name: "invalid_utf8_is_dropped", in: "err: " + string([]byte{0xff, 0xfe}) + " tail", wantExact: "err:  tail"},
		// The pair that matters: invalid BYTES are dropped by ToValidUTF8 above,
		// so by the time the mapping runs, a U+FFFD is a character the sender
		// actually wrote. Substituting it would silently discard real content,
		// and the two cases together pin the seam between the two behaviors.
		{
			name:      "a_genuine_replacement_character_survives",
			in:        "broker said \ufffd here",
			wantExact: "broker said \ufffd here",
		},
		{name: "at_the_cap_is_untouched", in: strings.Repeat("a", maxPersistedErrorBytes), wantExact: strings.Repeat("a", maxPersistedErrorBytes)},
		{name: "one_over_the_cap_truncates", in: strings.Repeat("a", maxPersistedErrorBytes+1), truncated: true},
		{name: "pathological_10kb_truncates", in: strings.Repeat("broker unreachable; ", 512), truncated: true},
		{name: "multibyte_truncates_on_a_rune_boundary", in: strings.Repeat("日", 5000), truncated: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := boundPersistedError(tt.in)

			assert.LessOrEqual(t, len(got), maxPersistedErrorBytes, "the ledger never receives more than the cap")
			assert.True(t, utf8.ValidString(got), "invalid UTF-8 would be rejected by PostgreSQL and fail the UPDATE")
			assert.NotContains(t, got, "\x00")
			assert.NotContains(t, got, "\n")
			assert.NotContains(t, got, "\r")

			if tt.truncated {
				assert.True(t, strings.HasSuffix(got, truncationMarker),
					"a shortened error says so, or a reader cannot tell it from a short one")
			} else {
				assert.Equal(t, tt.wantExact, got)
			}
		})
	}
}

// The helper being correct is not the property that matters: what matters is that
// the value REACHING the ledger is bounded. This drives the real failure path and
// asserts on what the store was handed, so removing the call from the relay fails
// here even though the helper still passes its own tests.
func TestPublishRecordBoundsTheErrorItPersists(t *testing.T) {
	oversized := strings.Repeat("broker unreachable; ", 512) // ~10 KiB

	store := &fakeStore{
		FetchPendingResult: []Record{{ID: "evt-1", Exchange: "orders", RoutingKey: "created"}},
	}
	amqp := newFakeAMQP()
	amqp.PublishErrFor = map[string]error{"orders:created": errors.New(oversized)}
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	require.NoError(t, r.Execute(ctx))

	require.Equal(t, 1, store.MarkFailedCalls)
	assert.Greater(t, len(oversized), maxPersistedErrorBytes, "the fixture is actually oversized")
	assert.LessOrEqual(t, len(store.MarkFailedLastErr), maxPersistedErrorBytes,
		"the ledger receives the bounded error, not the broker's whole message")
	assert.True(t, strings.HasSuffix(store.MarkFailedLastErr, truncationMarker))
	assert.Contains(t, store.MarkFailedLastErr, "broker unreachable",
		"and it is still diagnostic — truncated, not discarded")
}

// The dead-letter path writes to the same unbounded column, so bounding only the
// failure path would leave the invariant untrue on the other half.
func TestDeadLetterPoisonBoundsTheErrorItPersists(t *testing.T) {
	oversized := strings.Repeat("x", 9000)

	store := &fakeStore{}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	rec := &Record{ID: "evt-poison", RetryCount: r.config.MaxRetries}
	r.deadLetterPoison(ctx, ctx.Logger(), db, rec, oversized)

	require.Equal(t, 1, store.MarkDeadLetteredCalls)
	assert.LessOrEqual(t, len(store.MarkDeadLetteredLastErr), maxPersistedErrorBytes)
	assert.True(t, strings.HasSuffix(store.MarkDeadLetteredLastErr, truncationMarker))
}

// When the ledger write itself fails, the relay's only remaining job is to say
// so. Nothing is returned and nothing else is stored, so the emitted line is the
// whole observable — and its absence is how "we could not record why this record
// failed" becomes silent.
func TestMarkRecordFailedReportsAFailedLedgerWrite(t *testing.T) {
	tests := []struct {
		name      string
		markErr   error
		wantLines []string
	}{
		{name: "ledger_write_fails", markErr: errors.New("connection reset"), wantLines: []string{"Failed to mark outbox event as failed"}},
		// The negative half: a successful write says nothing. Without this, a
		// condition inverted to log on success would still look correct.
		{name: "ledger_write_succeeds", wantLines: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{MarkFailedErr: tt.markErr}
			r := newRelayWithFakes(store, newFakeAMQP())
			log := newRecordingLogger()
			db := dbtesting.NewTestDB("postgresql")

			r.markRecordFailed(context.Background(), log, db, "evt-1", "boom")

			assert.Equal(t, 1, store.MarkFailedCalls)
			assert.Equal(t, tt.wantLines, log.messages())
		})
	}
}

// Same shape on the dead-letter path, which additionally reports the failure
// through its return value: a record that could not be parked is NOT reported as
// parked, or the relay would claim it had stopped retrying something it had not.
func TestDeadLetterPoisonReportsAFailedLedgerWrite(t *testing.T) {
	tests := []struct {
		name      string
		markErr   error
		want      publishOutcome
		wantLines []string
	}{
		{
			name: "parking_fails", markErr: errors.New("connection reset"),
			want: outcomeFailed, wantLines: []string{"Failed to dead-letter outbox event"},
		},
		{
			name: "parking_succeeds",
			want: outcomeDeadLettered, wantLines: []string{"Outbox event dead-lettered after exhausting retries"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{MarkDeadLetteredErr: tt.markErr}
			r := newRelayWithFakes(store, newFakeAMQP())
			log := newRecordingLogger()
			db := dbtesting.NewTestDB("postgresql")
			rec := &Record{ID: "evt-poison", RetryCount: r.config.MaxRetries}

			got := r.deadLetterPoison(context.Background(), log, db, rec, "bad headers")

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantLines, log.messages())
		})
	}
}
