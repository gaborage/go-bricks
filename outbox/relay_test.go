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
	"github.com/gaborage/go-bricks/messaging/streams"
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
func newRelayWithFakes(store *fakeStore, amqp *fakeAMQP, streamPubs map[string]streamPublisher) *Relay {
	return &Relay{
		store: store,
		streamPublisher: func(name string) (streamPublisher, bool) {
			p, ok := streamPubs[name]
			return p, ok
		},
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
	r := newRelayWithFakes(&fakeStore{}, newFakeAMQP(), nil)
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
	r := newRelayWithFakes(store, amqp, nil)
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
	r := newRelayWithFakes(store, newFakeAMQP(), nil)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, nil)

	err := r.Execute(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch failed")
	assert.Contains(t, err.Error(), "network drop")
}

func TestRelayExecuteIsNoOpWhenNoPendingRecords(t *testing.T) {
	store := &fakeStore{FetchPendingResult: nil}
	r := newRelayWithFakes(store, newFakeAMQP(), nil)
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
	r := newRelayWithFakes(store, amqp, nil)
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
	r := newRelayWithFakes(store, amqp, nil)
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
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	rec := &Record{ID: "evt-bad-hdr", Headers: []byte(`{not valid json}`)}
	hdrs, decodeErr := decodeHeaders(rec.Headers)
	out, outErr := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec, hdrs, decodeErr)

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
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	rec := &Record{
		ID:         "evt-42",
		EventType:  "order.created",
		Exchange:   "orders",
		RoutingKey: "created",
		Headers:    []byte(`{"x-correlation-id":"abc"}`),
	}
	hdrs, decodeErr := decodeHeaders(rec.Headers)
	out, outErr := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec, hdrs, decodeErr)
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
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	rec := &Record{ID: "evt-7", EventType: "x.y", Exchange: "ex", RoutingKey: "rk"}
	hdrs, decodeErr := decodeHeaders(rec.Headers)
	out, outErr := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec, hdrs, decodeErr)
	require.Equal(t, outcomePublished, out)
	require.NoError(t, outErr)
	require.NotNil(t, amqp.LastPublishHdrs)
	assert.Equal(t, "evt-7", amqp.LastPublishHdrs[HeaderEventID])
}

func TestPublishRecordReturnsFalseWhenMarkPublishedFails(t *testing.T) {
	store := &fakeStore{MarkPublishedErr: errors.New("db gone")}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	rec := &Record{ID: "evt-mp-fail", Exchange: "ex", RoutingKey: "rk"}
	hdrs, decodeErr := decodeHeaders(rec.Headers)
	out, outErr := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec, hdrs, decodeErr)

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
	r := newRelayWithFakes(store, amqp, nil)
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
	hdrs, decodeErr := decodeHeaders(rec.Headers)
	out, outErr := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec, hdrs, decodeErr)
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
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	rec := &Record{
		ID:         "evt-poisoned",
		EventType:  "order.created",
		Exchange:   "orders",
		RoutingKey: "created",
		Headers:    []byte(`{"traceparent":"` + persisted + `"}`),
	}
	hdrs, decodeErr := decodeHeaders(rec.Headers)
	out, outErr := r.publishRecord(ctx, ctx.Logger(), db, amqp, rec, hdrs, decodeErr)
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
	r := newRelayWithFakes(store, amqp, nil)
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
	r := newRelayWithFakes(store, amqp, nil)
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
	r := newRelayWithFakes(store, amqp, nil)
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
	r := newRelayWithFakes(store, amqp, nil)
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
	r := newRelayWithFakes(store, amqp, nil)
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
	r := newRelayWithFakes(store, amqp, nil)
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
	r := newRelayWithFakes(store, amqp, nil)
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
	r := newRelayWithFakes(store, amqp, nil)
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
	r := newRelayWithFakes(store, amqp, nil)
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
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	lead, err := store.Lead(ctx, db)
	require.NoError(t, err)
	res := r.runRelayLoop(ctx, ctx.Logger(), db, amqp, lead, records)

	assert.Equal(t, 1, res.published, "record 1 published before the drop")
	assert.Equal(t, 0, res.unrecorded)
	assert.Equal(t, 0, res.deadlettered)
	assert.Equal(t, 2, res.failed, "record 2 (failed attempt) AND record 3 (outage remainder) both count as failed")
	assert.ErrorIs(t, res.outageErr, messaging.ErrNotConnected)
	sum := res.published + res.unrecorded + res.failed + res.deadlettered + res.parked
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
	r := newRelayWithFakes(store, amqp, nil)
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
	r := newRelayWithFakes(store, amqp, nil)
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
	r := newRelayWithFakes(store, amqp, nil)
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
			r := newRelayWithFakes(store, newFakeAMQP(), nil)
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
			r := newRelayWithFakes(store, newFakeAMQP(), nil)
			log := newRecordingLogger()
			db := dbtesting.NewTestDB("postgresql")
			rec := &Record{ID: "evt-poison", RetryCount: r.config.MaxRetries}

			got := r.deadLetterPoison(context.Background(), log, db, rec, "bad headers")

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantLines, log.messages())
		})
	}
}

// --- leadership --------------------------------------------------------------

func TestRelayNotLeaderSkipsCycle(t *testing.T) {
	store := &fakeStore{
		LeadErr:            ErrNotLeader,
		FetchPendingResult: []Record{{ID: "evt-1", Exchange: "orders", RoutingKey: "created"}},
	}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")

	require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)), "another instance leading is not a cycle failure")
	assert.Zero(t, store.FetchPendingCalls, "a non-leader must not even fetch")
	assert.Zero(t, amqp.PublishCalls)
	assert.Zero(t, store.ReleaseCalls, "nothing was acquired, so nothing is released")
}

func TestRelayLeaderErrorFailsCycle(t *testing.T) {
	store := &fakeStore{
		LeadErr:            errors.New("leader row missing in gobricks_outbox_leader"),
		FetchPendingResult: []Record{{ID: "evt-1", Exchange: "orders", RoutingKey: "created"}},
	}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")

	err := r.Execute(newFakeJobCtx(db, amqp))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leader")
	assert.Zero(t, amqp.PublishCalls)
}

func TestRelayLeaderReleasedAfterCycle(t *testing.T) {
	store := &fakeStore{
		FetchPendingResult: []Record{
			{ID: "evt-1", Exchange: "orders", RoutingKey: "a"},
			{ID: "evt-2", Exchange: "orders", RoutingKey: "b"},
		},
	}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")

	require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))
	assert.Equal(t, 1, store.LeadCalls)
	assert.Equal(t, 1, store.ReleaseCalls)
	assert.Equal(t, 2, store.ProbeCalls, "leadership is probed once per record")
}

func TestRelayLostLeadershipStopsBatch(t *testing.T) {
	store := &fakeStore{
		ProbeErrAfter: 2,
		ProbeErr:      errors.New("gone"),
		FetchPendingResult: []Record{
			{ID: "evt-1", Exchange: "orders", RoutingKey: "a"},
			{ID: "evt-2", Exchange: "orders", RoutingKey: "b"},
			{ID: "evt-3", Exchange: "orders", RoutingKey: "c"},
		},
	}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")

	err := r.Execute(newFakeJobCtx(db, amqp))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotLeader, "a lost leader row is reported as such")
	assert.NotContains(t, err.Error(), "messaging not available",
		"the cause is the database, so it must not be reported as a broker outage")
	assert.Equal(t, 1, amqp.PublishCalls, "a deposed leader publishes nothing further")
	assert.Zero(t, store.MarkFailedCalls, "the unattempted remainder is left pending, not marked")
	assert.Equal(t, 1, store.ReleaseCalls)
}

// --- key-ordered draining ----------------------------------------------------

func TestRelayFailedKeyParksLaterRowsOfThatKey(t *testing.T) {
	store := &fakeStore{
		FetchPendingResult: []Record{
			{ID: "K1", Exchange: "ex", RoutingKey: "k"},
			{ID: "K2", Exchange: "ex", RoutingKey: "k"},
			{ID: "J1", Exchange: "ex", RoutingKey: "j"},
		},
	}
	amqp := newFakeAMQP()
	amqp.PublishErrFor = map[string]error{"ex:k": errors.New("broker rejected")}
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")

	require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))
	assert.Equal(t, 2, amqp.PublishCalls, "K2 is parked behind K1; K1 and J1 are attempted")
	assert.Equal(t, 1, store.MarkFailedCalls)
	assert.Equal(t, "K1", store.MarkFailedLastID)
	assert.Equal(t, "J1", store.MarkPublishedLastID, "an unrelated key drains past a parked one")
}

func TestRelayNextCycleReattemptsParkedKeyInOrder(t *testing.T) {
	records := []Record{
		{ID: "K1", Exchange: "ex", RoutingKey: "k"},
		{ID: "K2", Exchange: "ex", RoutingKey: "k"},
	}
	store := &fakeStore{FetchPendingResult: records}
	amqp := newFakeAMQP()
	amqp.PublishErrFor = map[string]error{"ex:k": errors.New("broker rejected")}
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")

	require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))
	require.Equal(t, 1, amqp.PublishCalls)

	amqp.PublishErrFor = nil
	require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))
	assert.Equal(t, []string{"k", "k", "k"}, amqp.PublishOrder,
		"the parked row follows its predecessor, in sequence order, on the next cycle")
}

func TestRelayStampedAMQPRowsKeyByTenantStamp(t *testing.T) {
	store := &fakeStore{
		FetchPendingResult: []Record{
			{ID: "A1", Exchange: "ex", RoutingKey: "a", Headers: []byte(`{"x-tenant-id":"acme"}`)},
			{ID: "A2", Exchange: "ex", RoutingKey: "b", Headers: []byte(`{"x-tenant-id":"acme"}`)},
			{ID: "B1", Exchange: "ex", RoutingKey: "a", Headers: []byte(`{"x-tenant-id":"beta"}`)},
		},
	}
	amqp := newFakeAMQP()
	amqp.PublishErrOnce = map[string]error{"ex:a": errors.New("broker rejected")}
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")

	require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))
	assert.Equal(t, 2, amqp.PublishCalls,
		"A2 parks behind A1 because they share a tenant stamp, not a routing key")
	assert.Equal(t, []string{"a", "a"}, amqp.PublishOrder,
		"B1 publishes although its routing key equals A1's — a different stamp is a different key")
	assert.Equal(t, "B1", store.MarkPublishedLastID)
}

// TestRelayStreamRowsKeyByPartitionKey pins that a stream-lane row orders under its
// PARTITION KEY, not its routing key (which it has none of). Two rows sharing a key park
// together; a third on a different key drains past them.
func TestRelayStreamRowsKeyByPartitionKey(t *testing.T) {
	store := &fakeStore{
		FetchPendingResult: []Record{
			{ID: "S1", Lane: LaneStream, Stream: "customers", PartitionKey: "acme"},
			{ID: "S2", Lane: LaneStream, Stream: "customers", PartitionKey: "acme"},
			{ID: "T1", Lane: LaneStream, Stream: "customers", PartitionKey: "beta"},
		},
	}
	amqp := newFakeAMQP()
	// Every stream publish fails, so the first row of each key parks the rest of that key.
	pub := &fakeStreamPublisher{Err: errors.New("broker rejected")}
	r := newRelayWithFakes(store, amqp, map[string]streamPublisher{"customers": pub})
	db := dbtesting.NewTestDB("postgresql")

	require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))
	assert.Equal(t, 2, pub.Calls,
		"S2 parks behind S1 on their shared partition key; T1's differs, so it is attempted")
	assert.Equal(t, 2, store.MarkFailedCalls, "only the two attempted rows advance retry_count")
	assert.Zero(t, amqp.PublishCalls, "a stream row never reaches the AMQP lane")
}

func TestRelayDeadLetteredRowDoesNotPark(t *testing.T) {
	store := &fakeStore{
		FetchPendingResult: []Record{
			{ID: "K1", Exchange: "ex", RoutingKey: "k", Headers: []byte(`{not json}`), RetryCount: 2},
			{ID: "K2", Exchange: "ex", RoutingKey: "k"},
		},
	}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")

	require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))
	assert.Equal(t, 1, store.MarkDeadLetteredCalls)
	assert.Equal(t, "K2", store.MarkPublishedLastID, "K2 is attempted, not parked behind a terminal row")
}

func TestRelayOutagePathMarksUnderLeadership(t *testing.T) {
	store := &fakeStore{
		FetchPendingResult: []Record{
			{ID: "evt-1", Exchange: "ex", RoutingKey: "a"},
			{ID: "evt-2", Exchange: "ex", RoutingKey: "b"},
		},
	}
	amqp := newFakeAMQP()
	amqp.Ready = false
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")

	err := r.Execute(newFakeJobCtx(db, amqp))
	require.Error(t, err)
	assert.Equal(t, 1, store.LeadCalls, "marks are writes, so the outage path runs under leadership too")
	assert.Equal(t, 2, store.MarkFailedCalls)
	assert.Equal(t, 1, store.ReleaseCalls)
}

// TestRelayKeyNamespacesDistinctScopes pins that parking never couples rows which merely
// share a string. Before the lane prefix, a stream row partitioned by "acme" and an AMQP row
// stamped "acme" produced the same key, as did two stream rows on DIFFERENT streams sharing a
// partition key — so a failure on one would park the other for nothing.
func TestRelayKeyNamespacesDistinctScopes(t *testing.T) {
	stamped := map[string]any{messaging.TenantStampHeader: "acme"}

	streamOrders := relayKey(&Record{Lane: LaneStream, Stream: "orders", PartitionKey: "acme"}, nil)
	streamCustomers := relayKey(&Record{Lane: LaneStream, Stream: "customers", PartitionKey: "acme"}, nil)
	amqpStamped := relayKey(&Record{Exchange: "ex", RoutingKey: "created"}, stamped)
	amqpPlain := relayKey(&Record{Exchange: "ex", RoutingKey: "created"}, nil)

	// Two exchanges sharing a routing-key convention must not park each other.
	assert.NotEqual(t,
		relayKey(&Record{Exchange: "orders", RoutingKey: "created"}, nil),
		relayKey(&Record{Exchange: "billing", RoutingKey: "created"}, nil),
		"the destination includes the exchange, not the routing key alone")

	// The lane prefix is load-bearing, not decoration: a stream literally named "amqp"
	// whose partition key begins "tenant:" would otherwise produce the same key as a
	// tenant-stamped AMQP row.
	assert.NotEqual(t,
		relayKey(&Record{Lane: LaneStream, Stream: LaneAMQP, PartitionKey: "tenant:acme"}, nil),
		relayKey(&Record{Exchange: "ex", RoutingKey: "created"}, stamped),
		"the lane prefix keeps a stream named like the other lane out of its key space")

	assert.NotEqual(t, streamOrders, streamCustomers, "different streams are different scopes")
	assert.NotEqual(t, streamOrders, amqpStamped, "a partition key and a tenant stamp are different scopes")
	assert.NotEqual(t, amqpStamped, amqpPlain, "a stamped row orders by tenant, not by destination")

	// Same scope still collapses to one key, which is what parking depends on.
	assert.Equal(t, streamOrders,
		relayKey(&Record{Lane: LaneStream, Stream: "orders", PartitionKey: "acme"}, nil))
	assert.Equal(t, amqpStamped,
		relayKey(&Record{Exchange: "other", RoutingKey: "shipped"}, stamped),
		"one tenant's rows share a key across exchanges, which is the ordering a tenant needs")
}

// TestRelayLoopCountsParkedRows asserts the parked COUNT, not just the parking behavior.
// The count is what logCycle reports and what makes a cycle's numbers sum to its batch, so a
// test that only observes which rows were published leaves it unpinned.
func TestRelayLoopCountsParkedRows(t *testing.T) {
	// TWO rows park behind K1, so the count proves the counter ACCUMULATES: a dropped
	// increment reads 0 and a decrement reads -2, neither of which a single parked row
	// would distinguish from an off-by-one.
	records := []Record{
		{ID: "K1", Exchange: "ex", RoutingKey: "k"},
		{ID: "K2", Exchange: "ex", RoutingKey: "k"},
		{ID: "K3", Exchange: "ex", RoutingKey: "k"},
		{ID: "J1", Exchange: "ex", RoutingKey: "j"},
	}
	store := &fakeStore{FetchPendingResult: records}
	amqp := newFakeAMQP()
	amqp.PublishErrFor = map[string]error{"ex:k": errors.New("broker rejected")}
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, amqp)

	lead, err := store.Lead(ctx, db)
	require.NoError(t, err)
	res := r.runRelayLoop(ctx, ctx.Logger(), db, amqp, lead, records)

	assert.Equal(t, 2, res.parked, "K2 and K3 both park behind K1")
	assert.Equal(t, 1, res.failed, "K1 failed")
	assert.Equal(t, 1, res.published, "J1 published")
	assert.Equal(t, len(records),
		res.published+res.unrecorded+res.failed+res.deadlettered+res.parked,
		"every fetched row is accounted for exactly once")
}

// --- the stream lane ---------------------------------------------------------

// streamRow mirrors what applyStreamTarget actually persists: the tenant lives in
// partition_key and NOT in the headers. An earlier version of this fixture hand-wrote an
// x-tenant-id header the writer never produces, which hid that a real stream row reached the
// publisher unstamped under shared tenancy.
func streamRow() Record {
	return Record{
		ID: "S1", Lane: LaneStream, Stream: "customers", PartitionKey: "acme",
		EventType: "customer.created", Payload: []byte("p"),
		Headers: []byte(`{"traceparent":"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}`),
	}
}

func TestRelayStreamRowPublishesWithPartitionKey(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{streamRow()}}
	amqp := newFakeAMQP()
	pub := &fakeStreamPublisher{}
	r := newRelayWithFakes(store, amqp, map[string]streamPublisher{"customers": pub})
	db := dbtesting.NewTestDB("postgresql")

	require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))

	require.Equal(t, 1, pub.Calls)
	assert.Equal(t, "acme", pub.LastMsg.RoutingKey, "the partition key selects the partition")
	assert.Equal(t, []byte("p"), pub.LastMsg.Data)
	assert.Equal(t, "S1", pub.LastMsg.Properties[HeaderEventID])
	assert.NotContains(t, pub.LastMsg.Properties, messaging.TenantStampHeader,
		"the stamp rides the context so the publisher sets it; the relay must not supply one")
	tenant, ok := multitenant.GetTenant(pub.LastCtx)
	assert.True(t, ok)
	assert.Equal(t, "acme", tenant, "the row's stamp is rehydrated onto the publish context")
	assert.Equal(t, 1, store.MarkPublishedCalls)
	assert.Zero(t, amqp.PublishCalls, "a stream row never touches the AMQP lane")
}

func TestRelayStreamRowFailuresAreConnectivity(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "confirmation_failure", err: errors.New(`publish to stream "customers" was not confirmed by the broker`)},
		{name: "publisher_not_started", err: streams.ErrPublisherNotStarted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := streamRow()
			row.RetryCount = 99
			store := &fakeStore{FetchPendingResult: []Record{row}}
			amqp := newFakeAMQP()
			pub := &fakeStreamPublisher{Err: tt.err}
			r := newRelayWithFakes(store, amqp, map[string]streamPublisher{"customers": pub})
			db := dbtesting.NewTestDB("postgresql")

			require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))
			assert.Equal(t, 1, store.MarkFailedCalls)
			assert.Zero(t, store.MarkDeadLetteredCalls,
				"connectivity never parks, however far past MaxRetries the row is")
		})
	}
}

func TestRelayStreamRowClosedPublisherAborts(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		streamRow(),
		{ID: "A1", Exchange: "ex", RoutingKey: "a"},
	}}
	amqp := newFakeAMQP()
	pub := &fakeStreamPublisher{Err: streams.ErrPublisherClosed}
	r := newRelayWithFakes(store, amqp, map[string]streamPublisher{"customers": pub})
	db := dbtesting.NewTestDB("postgresql")

	require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))
	assert.Zero(t, store.MarkFailedCalls, "shutdown is not a delivery failure")
	assert.Zero(t, store.MarkDeadLetteredCalls)
	assert.Zero(t, amqp.PublishCalls, "the batch stops; the following AMQP row is not attempted")
}

func TestRelayStreamRowPoisonCases(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "unknown_stream", mutate: func(r *Record) { r.Stream = "payments" }},
		{name: "empty_partition_key", mutate: func(r *Record) { r.PartitionKey = "" }},
		{name: "unknown_lane", mutate: func(r *Record) { r.Lane = "carrier-pigeon" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := streamRow()
			row.RetryCount = 2
			tt.mutate(&row)
			store := &fakeStore{FetchPendingResult: []Record{row}}
			amqp := newFakeAMQP()
			pub := &fakeStreamPublisher{}
			r := newRelayWithFakes(store, amqp, map[string]streamPublisher{"customers": pub})
			db := dbtesting.NewTestDB("postgresql")

			require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))
			assert.Equal(t, 1, store.MarkDeadLetteredCalls, "config drift on a persisted row is poison")
			assert.Zero(t, pub.Calls)
			assert.Zero(t, amqp.PublishCalls)
		})
	}
}

func TestRelayStreamRowHonorsPublishTimeout(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{streamRow()}}
	amqp := newFakeAMQP()
	pub := &fakeStreamPublisher{Block: true}
	r := newRelayWithFakes(store, amqp, map[string]streamPublisher{"customers": pub})
	r.config.PublishTimeout = 50 * time.Millisecond
	db := dbtesting.NewTestDB("postgresql")

	done := make(chan struct{})
	go func() {
		defer close(done)
		assert.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a stuck stream publish was not bounded by PublishTimeout")
	}
	assert.Equal(t, 1, store.MarkFailedCalls, "a deadline is connectivity, so it retries")
}

// TestRelayStreamRowStampsFromPartitionKeyWithoutContextTenant is the shared-tenancy case:
// the cycle's own context carries no tenant (SetTenant with "" is a no-op) and a stream row
// keeps its tenant ONLY in partition_key, so reading the header alone would publish unstamped
// — and a shared-tenancy consumer fails closed on a missing stamp.
func TestRelayStreamRowStampsFromPartitionKeyWithoutContextTenant(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{streamRow()}}
	amqp := newFakeAMQP()
	pub := &fakeStreamPublisher{}
	r := newRelayWithFakes(store, amqp, map[string]streamPublisher{"customers": pub})
	r.tenants = []string{""} // shared ledger: no tenant on the cycle's context
	db := dbtesting.NewTestDB("postgresql")

	require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))

	require.Equal(t, 1, pub.Calls)
	tenant, ok := multitenant.GetTenant(pub.LastCtx)
	require.True(t, ok, "the publisher must learn the tenant, or it ships an unstamped message")
	assert.Equal(t, "acme", tenant, "a stream row's tenant is its partition key")
	assert.NotContains(t, pub.LastMsg.Properties, messaging.TenantStampHeader,
		"the framework stamps from context; the relay supplies no header")
}

// TestRelayStripsAnEmptyValuedStamp pins the presence-not-value rule: the conflict check keys
// on the header EXISTING, so an empty-valued one left in place would fail every publish.
func TestRelayStripsAnEmptyValuedStamp(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "A1", Exchange: "ex", RoutingKey: "a", Headers: []byte(`{"x-tenant-id":""}`)},
	}}
	amqp := newFakeAMQP()
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")

	require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))
	require.Equal(t, 1, amqp.PublishCalls)
	assert.NotContains(t, amqp.LastPublishHdrs, messaging.TenantStampHeader,
		"an empty-valued stamp is still a present header, so it must be removed")
}

// TestRelayStampConflictIsPoison pins that a stamp conflict parks instead of retrying: it is
// deterministic in the row, so retrying it forever would never succeed.
func TestRelayStampConflictIsPoison(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "A1", Exchange: "ex", RoutingKey: "a", RetryCount: 2},
	}}
	amqp := newFakeAMQP()
	amqp.PublishErr = messaging.ErrTenantStampConflict
	r := newRelayWithFakes(store, amqp, nil)
	db := dbtesting.NewTestDB("postgresql")

	require.NoError(t, r.Execute(newFakeJobCtx(db, amqp)))
	assert.Equal(t, 1, store.MarkDeadLetteredCalls, "a conflict is message-intrinsic, so it parks")
	assert.Zero(t, store.MarkFailedCalls)
}
