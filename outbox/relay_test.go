package outbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/internal/ledgererr"
	"github.com/gaborage/go-bricks/multitenant"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

func TestDecodeHeadersEmpty(t *testing.T) {
	headers, err := decodeHeaders(nil)
	assert.Nil(t, headers)
	assert.NoError(t, err)
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
	require.Error(t, err)
	assert.Nil(t, headers)
	assert.Contains(t, err.Error(), "invalid headers JSON")
}

// newRelayWithShippers wires a single-tenant Relay with the supplied fake store and per-lane
// adapters. tenants is [""], so multitenant.SetTenant is a no-op; getDB reads the db from a
// context value (dbFromCtx) stashed by newFakeJobCtx, which survives the per-tenant lease
// scope's context wrapping (ADR-032). readyTimeout defaults to a real-world value; a test that
// cares about the bound (e.g. TestRelayBoundsEachPreflightReadinessCheck) overrides
// r.readyTimeout directly after construction.
func newRelayWithShippers(store Store, shippers map[string]shipper) *Relay {
	return newRelay(
		store,
		&config.OutboxConfig{BatchSize: 10, MaxRetries: 3, PublishTimeout: 5 * time.Second},
		func(ctx context.Context) (dbtypes.Interface, error) { return dbFromCtx(ctx), nil },
		5*time.Second,
		[]string{""},
		shippers,
	)
}

// newRelayWithLanes is the common shape: both production lanes, each a fake.
func newRelayWithLanes(store *fakeStore) (r *Relay, amqpLane, streamLane *fakeShipper) {
	amqpLane, streamLane = &fakeShipper{}, &fakeShipper{}
	return newRelayWithShippers(store, map[string]shipper{
		LaneAMQP:   amqpLane,
		LaneStream: streamLane,
	}), amqpLane, streamLane
}

func TestRelayExecuteReturnsErrorWhenDBUnavailable(t *testing.T) {
	r, _, _ := newRelayWithLanes(&fakeStore{})

	err := r.Execute(newFakeJobCtx(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database not available")
}

func TestRelayExecuteWrapsFetchPendingError(t *testing.T) {
	store := &fakeStore{FetchPendingErr: errors.New("network drop")}
	r, _, _ := newRelayWithLanes(store)

	err := r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch failed")
	assert.Contains(t, err.Error(), "network drop")
}

func TestRelayExecuteIsNoOpWhenNoPendingRecords(t *testing.T) {
	store := &fakeStore{FetchPendingResult: nil}
	r, amqpLane, _ := newRelayWithLanes(store)
	amqpLane.ReadyErr = errors.New("messaging not ready")

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))),
		"an idle relay is not a failure even if a lane is down")
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
	r, amqpLane, _ := newRelayWithLanes(store)

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))
	assert.Equal(t, []string{"evt-1", "evt-2"}, amqpLane.shippedIDs())
	assert.Equal(t, 2, store.MarkPublishedCalls)
	assert.Equal(t, 0, store.MarkFailedCalls)
}

func TestRelayExecuteCountsFailuresAndContinues(t *testing.T) {
	store := &fakeStore{
		FetchPendingResult: []Record{
			{ID: "evt-1", Exchange: "orders", RoutingKey: "created"},
			{ID: "evt-2", Exchange: "orders", RoutingKey: "shipped"},
		},
	}
	r, amqpLane, _ := newRelayWithLanes(store)
	amqpLane.Verdicts = []verdict{{Kind: shipRetry, Err: errors.New("broker rejected")}} // evt-1

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))),
		"Execute returns nil even when some publishes fail (per-record status is in the store)")
	assert.Equal(t, []string{"evt-1", "evt-2"}, amqpLane.shippedIDs())
	assert.Equal(t, 1, store.MarkPublishedCalls)
	assert.Equal(t, "evt-2", store.MarkPublishedLastID)
	assert.Equal(t, 1, store.MarkFailedCalls)
	assert.Equal(t, "evt-1", store.MarkFailedLastID)
	assert.Contains(t, store.MarkFailedLastErr, "broker rejected")
}

// --- what the relay hands a lane ---------------------------------------------

func TestRelayInjectsOutboxMetadataHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers []byte
		want    map[string]any
	}{
		{
			name:    "preserves_caller_supplied_headers",
			headers: []byte(`{"x-correlation-id":"abc"}`),
			want:    map[string]any{"x-correlation-id": "abc", HeaderEventID: "evt-42", HeaderEventType: "order.created"},
		},
		{
			name: "builds_a_map_when_the_row_has_none",
			want: map[string]any{HeaderEventID: "evt-42", HeaderEventType: "order.created"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{FetchPendingResult: []Record{
				{ID: "evt-42", EventType: "order.created", Exchange: "orders", RoutingKey: "created", Headers: tt.headers},
			}}
			r, amqpLane, _ := newRelayWithLanes(store)

			require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))
			require.Len(t, amqpLane.Ships, 1)
			assert.Equal(t, tt.want, amqpLane.Ships[0].Headers)
		})
	}
}

// TestRelayRehydratesTheTraceContextForTheShip asserts that the relay reconstructs the
// originating trace context from the persisted row headers and ships with it. Without this,
// the downstream preparePublishing runs under the relay's trace-less background context and
// stamps the AMQP CorrelationId (which the consumer's failure-path logger surfaces as
// amqp_correlation_id and the consume span as messaging.message.conversation_id) with a
// freshly generated UUID, breaking continuity precisely on the error path.
func TestRelayRehydratesTheTraceContextForTheShip(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{{
		ID: "evt-trace", EventType: "order.created", Exchange: "orders", RoutingKey: "created",
		Headers: []byte(`{"traceparent":"` + inboundTraceparent + `","X-Request-ID":"` + inboundTraceID + `"}`),
	}}}
	r, amqpLane, _ := newRelayWithLanes(store)

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))

	require.Len(t, amqpLane.Ctxs, 1)
	tp, ok := gobrickstrace.ParentFromContext(amqpLane.Ctxs[0])
	assert.True(t, ok, "the ship context must carry the persisted traceparent")
	assert.Equal(t, inboundTraceparent, tp)
	assert.Equal(t, inboundTraceID, gobrickstrace.EnsureTraceID(amqpLane.Ctxs[0]),
		"the ship context's trace id must be the originating trace id, not a fresh one")
}

// TestRelayDoesNotReEmitAPersistedMalformedTraceParent is #1121's second reacher: a row
// written before the ingress seam existed carries whatever traceparent the caller planted,
// and the relay ships that persisted map verbatim — ExtractFromHeaders only sanitizes the
// CONTEXT it derives from it. The neutralization therefore happens one layer down, where the
// AMQP client injects over the map it was handed, so this test runs that same injection over
// the captured shipment rather than asserting on the raw map.
func TestRelayDoesNotReEmitAPersistedMalformedTraceParent(t *testing.T) {
	const persisted = "00-!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!-00f067aa0ba902b7-01"
	store := &fakeStore{FetchPendingResult: []Record{{
		ID: "evt-poisoned", EventType: "order.created", Exchange: "orders", RoutingKey: "created",
		Headers: []byte(`{"traceparent":"` + persisted + `"}`),
	}}}
	r, amqpLane, _ := newRelayWithLanes(store)

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))

	require.Len(t, amqpLane.Ships, 1)
	headers := amqpLane.Ships[0].Headers
	gobrickstrace.InjectIntoHeaders(amqpLane.Ctxs[0], &mapHeaderAccessor{headers: headers})

	emitted, ok := headers[gobrickstrace.HeaderTraceParent].(string)
	require.True(t, ok)
	assert.NotEqual(t, persisted, emitted, "the persisted value must not go back on the wire")
	assert.Equal(t, emitted, gobrickstrace.ValidateTraceParent(emitted), "the emitted traceparent is well-formed")
}

// TestRelayMovesTheShipmentStampOntoTheShipContext is the #1340 property at the relay's own
// seam: the framework is the stamp's only header writer (ADR-087), so a stamp a lane read
// off a persisted row travels by CONTEXT. The cycle dimension varies too — a cycle carrying
// no tenant (shared tenancy, control-plane key) must still stamp from the row.
func TestRelayMovesTheShipmentStampOntoTheShipContext(t *testing.T) {
	tests := []struct {
		name        string
		stamp       string
		cycleTenant string
	}{
		{name: "shared_cycle_tenant_scoped_row", stamp: "acme"},
		{name: "shared_cycle_other_tenant_scoped_row", stamp: "beta"},
		{name: "shared_cycle_tenant_less_row"},
		{name: "per_tenant_cycle", stamp: "acme", cycleTenant: "acme"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{FetchPendingResult: []Record{{ID: "evt-1", Exchange: "ex", RoutingKey: "a"}}}
			r, amqpLane, _ := newRelayWithLanes(store)
			r.tenants = []string{tt.cycleTenant}
			amqpLane.Plans = func(rec *Record, headers map[string]any) shipment {
				return shipment{Record: rec, Headers: headers, Key: rec.ID, Stamp: tt.stamp}
			}

			require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))

			require.Len(t, amqpLane.Ctxs, 1)
			got, ok := multitenant.GetTenant(amqpLane.Ctxs[0])
			assert.Equal(t, tt.stamp != "", ok)
			assert.Equal(t, tt.stamp, got)
			assert.Equal(t, 1, store.MarkPublishedCalls)
		})
	}
}

// TestRelayBoundsEachShipByThePublishTimeout pins that the publish bound is the relay's, not
// a lane's: every Ship runs on a context that already carries the deadline, so one stuck
// record cannot hold the batch and a later row is still attempted.
func TestRelayBoundsEachShipByThePublishTimeout(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "stuck", Exchange: "ex", RoutingKey: "slow"},
		{ID: "healthy", Exchange: "ex", RoutingKey: "fast"},
	}}
	r, amqpLane, _ := newRelayWithLanes(store)
	r.config.PublishTimeout = 30 * time.Millisecond
	amqpLane.Verdicts = []verdict{{Kind: shipRetry, Err: context.DeadlineExceeded}} // "stuck", then "healthy" delivers

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))

	require.Len(t, amqpLane.Ctxs, 2, "the healthy record is still attempted after the stuck one")
	deadline, ok := amqpLane.Ctxs[0].Deadline()
	require.True(t, ok, "a lane must never be handed an unbounded ship context")
	assert.LessOrEqual(t, time.Until(deadline), r.config.PublishTimeout)
	assert.Equal(t, 1, store.MarkPublishedCalls)
	assert.Equal(t, "healthy", store.MarkPublishedLastID)
	assert.Equal(t, 1, store.MarkFailedCalls)
	assert.Equal(t, "stuck", store.MarkFailedLastID)
}

// --- verdict to bookkeeping ---------------------------------------------------

func TestRelayCountsADeliveredButUnrecordedRow(t *testing.T) {
	store := &fakeStore{
		MarkPublishedErr:   errors.New("db gone"),
		FetchPendingResult: []Record{{ID: "evt-mp-fail", Exchange: "ex", RoutingKey: "rk"}},
	}
	r, _, _ := newRelayWithLanes(store)
	log := newRecordingLogger()
	ctx := newFakeJobCtx(dbtesting.NewTestDB("postgresql"))
	ctx.log = log

	require.NoError(t, r.Execute(ctx))

	assert.Equal(t, 1, store.MarkPublishedCalls)
	assert.Equal(t, 0, store.MarkFailedCalls,
		"the message WAS delivered; a MarkPublished failure must not bump retry_count")
	assert.Equal(t, int64(1), log.numbers()["unrecorded"])
	assert.Equal(t, int64(0), log.numbers()["published"])
}

func TestRelayDeadLettersUndecodableHeaders(t *testing.T) {
	tests := []struct {
		name             string
		retryCount       int
		wantDeadLettered int
		wantFailed       int
	}{
		{name: "at_max_retries", retryCount: 2, wantDeadLettered: 1},
		{name: "below_max_retries", wantFailed: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{FetchPendingResult: []Record{
				{ID: "poison", Headers: []byte(`{not valid json}`), RetryCount: tt.retryCount},
			}}
			r, amqpLane, _ := newRelayWithLanes(store)

			require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))

			assert.Empty(t, amqpLane.Ships, "undecodable headers never reach a lane")
			assert.Equal(t, tt.wantDeadLettered, store.MarkDeadLetteredCalls)
			assert.Equal(t, tt.wantFailed, store.MarkFailedCalls)
			if tt.wantFailed > 0 {
				assert.Contains(t, store.MarkFailedLastErr, "invalid headers JSON")
			}
		})
	}
}

// TestRelayDeadLettersAPoisonVerdictAtMaxRetries pins the poison half of the outcome
// vocabulary: a lane that calls its own failure message-intrinsic parks the row at the
// ceiling and only advances retry_count below it.
func TestRelayDeadLettersAPoisonVerdictAtMaxRetries(t *testing.T) {
	tests := []struct {
		name             string
		retryCount       int
		wantDeadLettered int
		wantFailed       int
	}{
		{name: "past_max_retries", retryCount: 99, wantDeadLettered: 1},
		{name: "below_max_retries", wantFailed: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{FetchPendingResult: []Record{
				{ID: "unpublishable", Exchange: "ex", RoutingKey: "rk", RetryCount: tt.retryCount},
			}}
			r, amqpLane, _ := newRelayWithLanes(store)
			amqpLane.Verdicts = []verdict{{Kind: shipPoison, Err: errors.New("routing key is 256 bytes, limit is 255")}}

			require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))

			assert.Len(t, amqpLane.Ships, 1, "the record is attempted once this cycle")
			assert.Equal(t, tt.wantDeadLettered, store.MarkDeadLetteredCalls)
			assert.Equal(t, tt.wantFailed, store.MarkFailedCalls)
			if tt.wantDeadLettered > 0 {
				assert.Equal(t, "routing key is 256 bytes, limit is 255", store.MarkDeadLetteredLastErr,
					"the lane's reason is what the ledger records")
			}
		})
	}
}

// TestRelayNeverDeadLettersConnectivityEvenPastMaxRetries guards the locked decision: a
// prolonged outage must never park a healthy event, even once its (outage-inflated)
// retry_count is well past MaxRetries.
func TestRelayNeverDeadLettersConnectivityEvenPastMaxRetries(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "evt", Exchange: "ex", RoutingKey: "rk", RetryCount: 99},
	}}
	r, amqpLane, _ := newRelayWithLanes(store)
	amqpLane.Verdicts = []verdict{{Kind: shipRetry, Err: errors.New("confirmation timed out")}}

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))
	assert.Equal(t, 1, store.MarkFailedCalls, "connectivity advances retry_count")
	assert.Equal(t, 0, store.MarkDeadLetteredCalls, "connectivity never parks, no matter the count")
}

// TestRelayAbortedShipDoesNotInflateRetryCount guards finding S4: a ship interrupted by
// shutdown must NOT advance retry_count, and stops the batch cleanly.
func TestRelayAbortedShipDoesNotInflateRetryCount(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "evt-1", Exchange: "ex", RoutingKey: "rk1"},
		{ID: "evt-2", Exchange: "ex", RoutingKey: "rk2"},
	}}
	r, amqpLane, _ := newRelayWithLanes(store)
	amqpLane.Verdicts = []verdict{{Kind: shipAborted}}

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))
	assert.Equal(t, 0, store.MarkFailedCalls, "shutdown must not inflate retry_count")
	assert.Equal(t, 0, store.MarkDeadLetteredCalls)
	assert.Equal(t, []string{"evt-1"}, amqpLane.shippedIDs(), "the batch stops at the first aborted record")
}

// TestRelayBrokerDownStopsTheCycleAndRoutesTheRemainder: once a lane reports it dropped
// mid-batch, every REMAINING record would otherwise pay its own serial readiness pre-flight
// (BatchSize x readyTimeout stall). The relay instead routes the unattempted remainder
// through the same no-publish outage path the cycle-start pre-flight uses, and the remainder
// still counts as failed so the cycle's numbers sum to its batch.
func TestRelayBrokerDownStopsTheCycleAndRoutesTheRemainder(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "evt-1", Exchange: "ex", RoutingKey: "rk1"},
		{ID: "evt-2", Exchange: "ex", RoutingKey: "rk2"},
		{ID: "evt-3", Exchange: "ex", RoutingKey: "rk3"},
	}}
	r, amqpLane, _ := newRelayWithLanes(store)
	dropped := errors.New("not connected")
	amqpLane.Verdicts = []verdict{
		{Kind: shipDelivered},
		{Kind: shipBrokerDown, Err: dropped, Waited: 90 * time.Millisecond}, // evt-2
	}
	log := newRecordingLogger()
	ctx := newFakeJobCtx(dbtesting.NewTestDB("postgresql"))
	ctx.log = log

	err := r.Execute(ctx)
	require.Error(t, err, "the mid-batch outage surfaces as a job-level error, like the cycle-start path")
	assert.Contains(t, err.Error(), "messaging not available")
	require.ErrorIs(t, err, dropped)

	assert.Equal(t, []string{"evt-1", "evt-2"}, amqpLane.shippedIDs(), "record 3 is never attempted")
	assert.Equal(t, 1, store.MarkPublishedCalls, "record 1 published normally before the drop")
	assert.Equal(t, "evt-1", store.MarkPublishedLastID)
	assert.Equal(t, 2, store.MarkFailedCalls,
		"record 2 (the failed attempt) and record 3 (the outage remainder) both advance retry_count")
	assert.Equal(t, 0, store.MarkDeadLetteredCalls)

	numbers := log.numbers()
	assert.Equal(t, int64(1), numbers["published"])
	assert.Equal(t, int64(2), numbers["failed"], "the outage remainder is counted, not just marked")
	assert.Equal(t, int64(3), numbers["total"])
	assert.Equal(t, int64(90), numbers["stall_wait_ms"], "the stall the cycle actually paid")
}

// TestRelayBoundsTheErrorItPersists: the helper being correct is not the property that
// matters; what matters is that the value REACHING the ledger is bounded. This drives the
// real failure path and asserts on what the store was handed.
func TestRelayBoundsTheErrorItPersists(t *testing.T) {
	oversized := strings.Repeat("broker unreachable; ", 512) // ~10 KiB

	store := &fakeStore{FetchPendingResult: []Record{{ID: "evt-1", Exchange: "orders", RoutingKey: "created"}}}
	r, amqpLane, _ := newRelayWithLanes(store)
	amqpLane.Verdicts = []verdict{{Kind: shipRetry, Err: errors.New(oversized)}}

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))

	require.Equal(t, 1, store.MarkFailedCalls)
	assert.Greater(t, len(oversized), ledgererr.MaxBytes, "the fixture is actually oversized")
	assert.LessOrEqual(t, len(store.MarkFailedLastErr), ledgererr.MaxBytes,
		"the ledger receives the bounded error, not the broker's whole message")
	assert.True(t, strings.HasSuffix(store.MarkFailedLastErr, ledgererr.TruncationMarker))
	assert.Contains(t, store.MarkFailedLastErr, "broker unreachable",
		"and it is still diagnostic — truncated, not discarded")
}

// The dead-letter path writes to the same unbounded column, so bounding only the failure
// path would leave the invariant untrue on the other half.
func TestDeadLetterPoisonBoundsTheErrorItPersists(t *testing.T) {
	oversized := strings.Repeat("x", 9000)

	store := &fakeStore{}
	r, _, _ := newRelayWithLanes(store)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db)

	rec := &Record{ID: "evt-poison", RetryCount: r.config.MaxRetries}
	r.deadLetterPoison(ctx, ctx.Logger(), db, rec, oversized)

	require.Equal(t, 1, store.MarkDeadLetteredCalls)
	assert.LessOrEqual(t, len(store.MarkDeadLetteredLastErr), ledgererr.MaxBytes)
	assert.True(t, strings.HasSuffix(store.MarkDeadLetteredLastErr, ledgererr.TruncationMarker))
}

func TestMarkRecordFailedLogsButDoesNotPanicOnStoreError(t *testing.T) {
	store := &fakeStore{MarkFailedErr: errors.New("store unreachable")}
	r, _, _ := newRelayWithLanes(store)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db)

	require.NotPanics(t, func() {
		r.markRecordFailed(ctx, ctx.Logger(), db, "evt-id", "publish err")
	})
	assert.Equal(t, 1, store.MarkFailedCalls)
}

// When the ledger write itself fails, the relay's only remaining job is to say so. Nothing
// is returned and nothing else is stored, so the emitted line is the whole observable — and
// its absence is how "we could not record why this record failed" becomes silent.
func TestMarkRecordFailedReportsAFailedLedgerWrite(t *testing.T) {
	tests := []struct {
		name      string
		markErr   error
		wantLines []string
	}{
		{name: "ledger_write_fails", markErr: errors.New("connection reset"), wantLines: []string{"Failed to mark outbox event as failed"}},
		// The negative half: a successful write says nothing. Without this, a condition
		// inverted to log on success would still look correct.
		{name: "ledger_write_succeeds", wantLines: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{MarkFailedErr: tt.markErr}
			r, _, _ := newRelayWithLanes(store)
			log := newRecordingLogger()

			r.markRecordFailed(context.Background(), log, dbtesting.NewTestDB("postgresql"), "evt-1", "boom")

			assert.Equal(t, 1, store.MarkFailedCalls)
			assert.Equal(t, tt.wantLines, log.messages())
		})
	}
}

// Same shape on the dead-letter path, which additionally reports the failure through its
// return value: a record that could not be parked is NOT reported as parked, or the relay
// would claim it had stopped retrying something it had not.
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
			r, _, _ := newRelayWithLanes(store)
			log := newRecordingLogger()
			rec := &Record{ID: "evt-poison", RetryCount: r.config.MaxRetries}

			got := r.deadLetterPoison(context.Background(), log, dbtesting.NewTestDB("postgresql"), rec, "bad headers")

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantLines, log.messages())
		})
	}
}

// --- tenants ------------------------------------------------------------------

// TestRelayExecuteFansOutAcrossStaticTenants verifies the multi-tenant fix: the relay
// resolves the database once per configured tenant (with that tenant injected into the
// context) and relays each tenant's pending events — rather than the prior tenant-less
// resolution that returned ErrNoTenantInContext and relayed nothing.
func TestRelayExecuteFansOutAcrossStaticTenants(t *testing.T) {
	var resolved []string
	store := &fakeStore{FetchPendingResult: []Record{{ID: "e1", Exchange: "ex", RoutingKey: "rk"}}}
	r, amqpLane, _ := newRelayWithLanes(store)
	r.tenants = []string{"tenant-a", "tenant-b"}
	r.getDB = func(ctx context.Context) (dbtypes.Interface, error) {
		tid, _ := multitenant.GetTenant(ctx)
		resolved = append(resolved, tid)
		return dbtesting.NewTestDB("postgresql"), nil
	}

	require.NoError(t, r.Execute(newFakeJobCtx(nil)))
	assert.Equal(t, []string{"tenant-a", "tenant-b"}, resolved, "relay must resolve the DB once per configured tenant, in order")
	assert.Equal(t, 2, store.FetchPendingCalls, "FetchPending runs once per tenant")
	assert.Len(t, amqpLane.Ships, 2, "each tenant's pending record is published")
}

// TestRelayExecuteIsolatesPerTenantFailures verifies one unhealthy tenant does not block the
// others: its error is collected (naming the tenant) while healthy tenants still run.
func TestRelayExecuteIsolatesPerTenantFailures(t *testing.T) {
	store := &fakeStore{}
	r, _, _ := newRelayWithLanes(store)
	r.tenants = []string{"good", "bad"}
	r.getDB = func(ctx context.Context) (dbtypes.Interface, error) {
		if tid, _ := multitenant.GetTenant(ctx); tid == "bad" {
			return nil, errors.New("tenant db down")
		}
		return dbtesting.NewTestDB("postgresql"), nil
	}

	err := r.Execute(newFakeJobCtx(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `tenant "bad"`)
	assert.Contains(t, err.Error(), "tenant db down")
	assert.Equal(t, 1, store.FetchPendingCalls, "the healthy tenant is still relayed despite the other failing")
}

// --- leadership ---------------------------------------------------------------

func TestRelayNotLeaderSkipsCycle(t *testing.T) {
	store := &fakeStore{
		LeadErr:            ErrNotLeader,
		FetchPendingResult: []Record{{ID: "evt-1", Exchange: "orders", RoutingKey: "created"}},
	}
	r, amqpLane, _ := newRelayWithLanes(store)

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))),
		"another instance leading is not a cycle failure")
	assert.Zero(t, store.FetchPendingCalls, "a non-leader must not even fetch")
	assert.Empty(t, amqpLane.Ships)
	assert.Zero(t, amqpLane.ReadyCalls, "a non-leader probes no lane either")
	assert.Zero(t, store.ReleaseCalls, "nothing was acquired, so nothing is released")
}

func TestRelayLeaderErrorFailsCycle(t *testing.T) {
	store := &fakeStore{
		LeadErr:            errors.New("leader row missing in gobricks_outbox_leader"),
		FetchPendingResult: []Record{{ID: "evt-1", Exchange: "orders", RoutingKey: "created"}},
	}
	r, amqpLane, _ := newRelayWithLanes(store)

	err := r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leader")
	assert.Empty(t, amqpLane.Ships)
}

func TestRelayLeaderReleasedAfterCycle(t *testing.T) {
	store := &fakeStore{
		FetchPendingResult: []Record{
			{ID: "evt-1", Exchange: "orders", RoutingKey: "a"},
			{ID: "evt-2", Exchange: "orders", RoutingKey: "b"},
		},
	}
	r, _, _ := newRelayWithLanes(store)

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))
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
	r, amqpLane, _ := newRelayWithLanes(store)

	err := r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql")))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "messaging not available",
		"the cause is the database, so it must not be reported as a broker outage")
	assert.Len(t, amqpLane.Ships, 1, "a deposed leader publishes nothing further")
	assert.Zero(t, store.MarkFailedCalls, "the unattempted remainder is left pending, not marked")
	assert.Equal(t, 1, store.ReleaseCalls)
	require.ErrorIs(t, err, ErrNotLeader, "a lost leader row is reported as such")
}

func TestRelayOutagePathMarksUnderLeadership(t *testing.T) {
	store := &fakeStore{
		FetchPendingResult: []Record{
			{ID: "evt-1", Exchange: "ex", RoutingKey: "a"},
			{ID: "evt-2", Exchange: "ex", RoutingKey: "b"},
		},
	}
	r, amqpLane, _ := newRelayWithLanes(store)
	amqpLane.ReadyErr = errors.New("messaging not ready")

	err := r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql")))
	require.Error(t, err)
	assert.Equal(t, 1, store.LeadCalls, "marks are writes, so the outage path runs under leadership too")
	assert.Equal(t, 2, store.MarkFailedCalls)
	assert.Equal(t, 1, store.ReleaseCalls)
}

// TestRelayAllOutageLeadershipLossOutranksTheLaneError pins that a leadership probe failure
// inside markOutage propagates as the leadershipErr, not the lane-down error markOutage was
// marking under: the mixed-lane path already prioritizes a database-side leadership loss over
// a broker outage (relayTenant checks res.leadershipErr before laneErr), and the all-outage
// path — where every fetched row lands on a down lane and runnable is empty — must read the
// same way instead of hiding the leadership failure behind "messaging not ready".
func TestRelayAllOutageLeadershipLossOutranksTheLaneError(t *testing.T) {
	probeErr := errors.New("leader row gone")
	store := &fakeStore{
		ProbeErrAfter: 1,
		ProbeErr:      probeErr,
		FetchPendingResult: []Record{
			{ID: "evt-1", Exchange: "ex", RoutingKey: "a"},
		},
	}
	r, amqpLane, _ := newRelayWithLanes(store)
	amqpLane.ReadyErr = errors.New("messaging not ready")

	err := r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql")))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNotLeader, "leadership loss must outrank the lane-down error")
	require.ErrorIs(t, err, probeErr, "the scripted probe failure is the cause reported")
	assert.NotContains(t, err.Error(), "messaging not ready",
		"the database-side cause must not be reported as a broker outage")
	assert.Zero(t, store.MarkFailedCalls, "the probe failure stops markOutage before it marks anything")
}

// --- lane pre-flight ----------------------------------------------------------

// TestRelayAdvancesRetryCountWhenALaneIsNotReady is the direct regression test for the
// reported bug: when the broker was not ready the relay used to early-return and the
// retry_count stayed frozen. Now every pending record on that lane advances per cycle.
func TestRelayAdvancesRetryCountWhenALaneIsNotReady(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "evt-1", Exchange: "ex", RoutingKey: "rk"},
		{ID: "evt-2", Exchange: "ex", RoutingKey: "rk"},
	}}
	r, amqpLane, _ := newRelayWithLanes(store)
	amqpLane.ReadyErr = errors.New("messaging not ready")

	err := r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql")))
	require.Error(t, err, "a not-ready lane with pending work surfaces as a job error")
	assert.Contains(t, err.Error(), "messaging not ready")
	assert.Equal(t, 1, amqpLane.ReadyCalls, "the lane is probed once per cycle, not once per record")
	assert.Equal(t, 2, store.MarkFailedCalls,
		"retry_count still advances for every record while the lane is down (the reported bug)")
	assert.Empty(t, amqpLane.Ships, "no publish is attempted on a lane that is not ready")
	assert.Equal(t, 0, store.MarkDeadLetteredCalls, "an unreachable broker is connectivity — never parked")
}

// TestRelayOneLaneOutageDoesNotStallTheOther pins that the two lanes fail independently: the
// AMQP client being unready says nothing about the stream protocol, which is a separate
// connection — so a stream row must still publish, and must not have its retry_count
// advanced for an outage on a transport it never uses.
func TestRelayOneLaneOutageDoesNotStallTheOther(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "A1", Exchange: "ex", RoutingKey: "a"},
		streamRow(),
	}}
	r, amqpLane, streamLane := newRelayWithLanes(store)
	amqpLane.ReadyErr = errors.New("messaging not ready")

	err := r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql")))
	require.Error(t, err, "the AMQP half still failed, so the cycle still reports it")

	assert.Equal(t, []string{"S1"}, streamLane.shippedIDs(), "the stream row published despite the AMQP outage")
	assert.Equal(t, 1, store.MarkPublishedCalls, "and was marked published")
	assert.Equal(t, 1, store.MarkFailedCalls, "only the AMQP row took the outage path")
	assert.Empty(t, amqpLane.Ships)
}

// TestRelayBoundsEachPreflightReadinessCheck is the regression test for #1538: preflight
// used to call Ready with the bare job context, which carries no deadline (the scheduler
// builds it from a tenant-less, undeadlined context). A shipper whose Ready hangs — the
// AMQP lane's Ready resolves the tenant client through messaging.Manager.Publisher, which
// can wait — must not stall the whole cycle. The relay must bound the check itself with
// messaging.reconnect.readytimeout, so the shipper observes a context with its own deadline
// and the cycle still returns promptly, reporting the lane down.
func TestRelayBoundsEachPreflightReadinessCheck(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "evt-1", Exchange: "ex", RoutingKey: "rk"},
	}}
	r, amqpLane, _ := newRelayWithLanes(store)
	r.readyTimeout = 20 * time.Millisecond

	var hasDeadline bool
	amqpLane.ReadyFn = func(ctx context.Context) error {
		_, hasDeadline = ctx.Deadline()
		<-ctx.Done()
		return ctx.Err()
	}

	start := time.Now()
	// The job ctx itself carries no deadline (as scheduler.JobContext builds it) — only the
	// relay's own bound can make the hung Ready check return.
	err := r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql")))
	elapsed := time.Since(start)

	require.Error(t, err, "a preflight check that never returns on its own must still surface as a lane failure")
	assert.Contains(t, err.Error(), context.DeadlineExceeded.Error())
	assert.Less(t, elapsed, time.Second, "the ready check must be bounded by the relay, not the bare job ctx")
	assert.True(t, hasDeadline, "preflight must hand Ready a context with its own deadline")
	assert.Equal(t, 1, store.MarkFailedCalls, "the outaged row still advances retry_count while the lane is down")
}

// TestRelayZeroReadyTimeoutLeavesPreflightUnbounded pins the zero arm of the readyTimeout
// bound at outbox/relay.go:223 (`r.readyTimeout > 0`): a zero timeout means "no extra
// bound" — Ready receives the caller's ctx unchanged — never a zero-deadline ctx that would
// be expired the instant it is created. A `>=` mutant on that comparison turns a zero
// timeout into context.WithTimeout(ctx, 0), an already-expired context.
func TestRelayZeroReadyTimeoutLeavesPreflightUnbounded(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "evt-1", Exchange: "ex", RoutingKey: "rk"},
	}}
	r, amqpLane, _ := newRelayWithLanes(store)
	r.readyTimeout = 0

	var hasDeadline bool
	var readyCtxErr error
	amqpLane.ReadyFn = func(ctx context.Context) error {
		_, hasDeadline = ctx.Deadline()
		readyCtxErr = ctx.Err()
		return nil
	}

	// The job ctx itself carries no deadline (as scheduler.JobContext builds it).
	err := r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql")))

	require.NoError(t, err)
	assert.False(t, hasDeadline, "a zero readyTimeout must not add a deadline to the ready check's ctx")
	assert.NoError(t, readyCtxErr, "ctx must not already be expired when Ready runs")
}

// --- lane resolution ----------------------------------------------------------

// TestRelayResolvesTheEmptyLegacyLaneToTheAMQPShipper pins the one place the empty lane a
// row written before the column existed carries is filled in.
func TestRelayResolvesTheEmptyLegacyLaneToTheAMQPShipper(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{{ID: "legacy", Exchange: "ex", RoutingKey: "rk"}}}
	r, amqpLane, streamLane := newRelayWithLanes(store)

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))
	assert.Equal(t, []string{"legacy"}, amqpLane.shippedIDs())
	assert.Empty(t, streamLane.Ships)
}

// TestRelayDeadLettersAnUnknownLane: a row naming a lane this build has no shipper for is
// message-intrinsic — it reads the same way every cycle, so it parks rather than retrying
// forever, and it needs no broker to be judged.
func TestRelayDeadLettersAnUnknownLane(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "X1", Lane: "carrier-pigeon", Exchange: "ex", RoutingKey: "a", RetryCount: 2},
	}}
	r, amqpLane, _ := newRelayWithLanes(store)

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))
	assert.Equal(t, 1, store.MarkDeadLetteredCalls)
	assert.Equal(t, `unknown lane "carrier-pigeon"`, store.MarkDeadLetteredLastErr)
	assert.Empty(t, amqpLane.Ships, "an unknown lane never reaches a broker")
}

// TestRelayUnknownLaneIsJudgedDuringALaneOutage pins that a row needing no broker must not
// be diverted into the outage path, where its retry_count would climb every cycle until the
// broker returned.
func TestRelayUnknownLaneIsJudgedDuringALaneOutage(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "X1", Lane: "carrier-pigeon", Exchange: "ex", RoutingKey: "a", RetryCount: 2},
	}}
	r, amqpLane, _ := newRelayWithLanes(store)
	amqpLane.ReadyErr = errors.New("messaging not ready")

	err := r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql")))
	require.Error(t, err, "the cycle still reports the AMQP outage")
	assert.Equal(t, 1, store.MarkDeadLetteredCalls, "an unknown lane is poison, outage or not")
	assert.Zero(t, store.MarkFailedCalls, "it must not be marked as an outage casualty")
}

// TestRelayUnknownLanesDoNotParkEachOther: an unknown-lane row is never planned, so it holds
// no key. Were they keyed alike, the first would park all the others in the batch, however
// unrelated their destinations.
func TestRelayUnknownLanesDoNotParkEachOther(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "X1", Lane: "carrier-pigeon", Exchange: "ex", RoutingKey: "a"},
		{ID: "X2", Lane: "semaphore", Exchange: "other", RoutingKey: "b"},
	}}
	r, amqpLane, _ := newRelayWithLanes(store)
	r.config.MaxRetries = 5 // both rows sit below the ceiling, so both take the failed path

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))
	assert.Equal(t, 2, store.MarkFailedCalls, "each unknown-lane row is judged on its own; neither parks the other")
	assert.Empty(t, amqpLane.Ships)
}

// TestRelayUnknownLaneIsNotParkedBehindAnotherRow pins that poison is never parkable. An
// unknown-lane row used to fall into the AMQP key namespace, so a failing AMQP row aimed at
// the same destination parked it before it could be classified.
func TestRelayUnknownLaneIsNotParkedBehindAnotherRow(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "A1", Exchange: "ex", RoutingKey: "k"}, // fails first, parks "ex:k"
		{ID: "X1", Lane: "carrier-pigeon", Exchange: "ex", RoutingKey: "k", RetryCount: 2},
	}}
	r, amqpLane, _ := newRelayWithLanes(store)
	amqpLane.Verdicts = []verdict{{Kind: shipRetry, Err: errors.New("broker rejected")}}

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))
	assert.Equal(t, 1, store.MarkFailedCalls, "the AMQP row failed and parked its own key")
	assert.Equal(t, 1, store.MarkDeadLetteredCalls,
		"the unknown-lane row is classified the same cycle, not parked behind it")
}

// --- key-ordered draining -----------------------------------------------------

func TestRelayFailedKeyParksLaterRowsOfThatKey(t *testing.T) {
	store := &fakeStore{
		FetchPendingResult: []Record{
			{ID: "K1", Exchange: "ex", RoutingKey: "k"},
			{ID: "K2", Exchange: "ex", RoutingKey: "k"},
			{ID: "J1", Exchange: "ex", RoutingKey: "j"},
		},
	}
	r, amqpLane, _ := newRelayWithLanes(store)
	amqpLane.Verdicts = []verdict{{Kind: shipRetry, Err: errors.New("broker rejected")}} // K1

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))
	assert.Equal(t, []string{"K1", "J1"}, amqpLane.shippedIDs(), "K2 is parked behind K1; an unrelated key drains past it")
	assert.Equal(t, 1, store.MarkFailedCalls)
	assert.Equal(t, "K1", store.MarkFailedLastID)
	assert.Equal(t, "J1", store.MarkPublishedLastID)
}

func TestRelayNextCycleReattemptsParkedKeyInOrder(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "K1", Exchange: "ex", RoutingKey: "k"},
		{ID: "K2", Exchange: "ex", RoutingKey: "k"},
	}}
	r, amqpLane, _ := newRelayWithLanes(store)
	amqpLane.Verdicts = []verdict{{Kind: shipRetry, Err: errors.New("broker rejected")}}
	db := dbtesting.NewTestDB("postgresql")

	require.NoError(t, r.Execute(newFakeJobCtx(db)))
	require.Equal(t, []string{"K1"}, amqpLane.shippedIDs())

	require.NoError(t, r.Execute(newFakeJobCtx(db)))
	assert.Equal(t, []string{"K1", "K1", "K2"}, amqpLane.shippedIDs(),
		"the parked row follows its predecessor, in sequence order, on the next cycle")
}

// TestRelayUndecodableHeadersBelowMaxRetriesParkTheKey pins ADR-088's per-key ordering
// promise across the poison the relay decides for itself. Below the ceiling an undecodable
// row only advances retry_count and stays PENDING, so the later rows of its key must wait
// for it — otherwise they ship ahead of a row that is still going to be retried, and the
// key's order is broken by exactly the failure ordering exists for.
func TestRelayUndecodableHeadersBelowMaxRetriesParkTheKey(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "K1", Exchange: "ex", RoutingKey: "k", Headers: []byte(`{not json}`)}, // RetryCount 0, MaxRetries 3
		{ID: "K2", Exchange: "ex", RoutingKey: "k"},
	}}
	r, amqpLane, _ := newRelayWithLanes(store)

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))

	assert.Empty(t, amqpLane.shippedIDs(), "K2 waits behind a row that is still pending")
	assert.Equal(t, 1, store.MarkFailedCalls, "only K1 advances retry_count; a parked row is untouched")
	assert.Equal(t, "K1", store.MarkFailedLastID)
	assert.Zero(t, store.MarkPublishedCalls)
	assert.Zero(t, store.MarkDeadLetteredCalls)
}

// TestRelayPoisonRowBehindParkedHeadIsHeldBack pins that the hold-back is decided BEFORE
// any ledger write, not after. Planning a row is pure; dead-lettering it is not, so a
// poison row sitting behind a still-pending head of its own key — or behind a scope this
// cycle already found stalled — must not have its retry_count moved, or ADR-088's promise
// that a held-back row is untouched is broken by the one row that never reached a broker.
func TestRelayPoisonRowBehindParkedHeadIsHeldBack(t *testing.T) {
	streamPoison := func(rec *Record, headers map[string]any) shipment {
		sh := planByStream(rec, headers)
		if rec.ID == "S2" {
			sh.Poison = "stream row has no partition key"
		}
		return sh
	}
	tests := []struct {
		name        string
		records     []Record
		setup       func(amqpLane, streamLane *fakeShipper)
		wantShipped []string
		wantHead    string
	}{
		{
			name: "undecodable_row_behind_its_own_parked_key",
			records: []Record{
				{ID: "K1", Exchange: "ex", RoutingKey: "k"},
				{ID: "K2", Exchange: "ex", RoutingKey: "k", Headers: []byte(`{not json}`)},
			},
			setup: func(amqpLane, _ *fakeShipper) {
				amqpLane.Verdicts = []verdict{{Kind: shipRetry, Err: errors.New("broker rejected")}}
			},
			wantShipped: []string{"K1"},
			wantHead:    "K1",
		},
		{
			name: "lane_poison_behind_a_down_scope",
			records: []Record{
				streamRow(), // S1, stream customers
				{ID: "S2", Lane: LaneStream, Stream: "customers", PartitionKey: "beta", Payload: []byte("p")},
			},
			setup: func(_, streamLane *fakeShipper) {
				streamLane.Plans = streamPoison
				streamLane.Verdicts = []verdict{{Kind: shipScopeDown, Err: errors.New("producer is not carrying messages")}}
			},
			wantShipped: []string{"S1"},
			wantHead:    "S1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{FetchPendingResult: tt.records}
			r, amqpLane, streamLane := newRelayWithLanes(store)
			tt.setup(amqpLane, streamLane)
			log := newRecordingLogger()
			ctx := newFakeJobCtx(dbtesting.NewTestDB("postgresql"))
			ctx.log = log

			require.NoError(t, r.Execute(ctx))

			shipped := append(amqpLane.shippedIDs(), streamLane.shippedIDs()...)
			assert.Equal(t, tt.wantShipped, shipped, "the held-back row is never handed to a lane")
			assert.Equal(t, 1, store.MarkFailedCalls, "only the head advances retry_count")
			assert.Equal(t, tt.wantHead, store.MarkFailedLastID)
			assert.Zero(t, store.MarkDeadLetteredCalls, "a held-back row is not judged this cycle")
			assert.Equal(t, int64(1), log.numbers()["parked"], "the poison row is counted as held, not as failed")
		})
	}
}

func TestRelayDeadLetteredRowDoesNotPark(t *testing.T) {
	store := &fakeStore{
		FetchPendingResult: []Record{
			{ID: "K1", Exchange: "ex", RoutingKey: "k", Headers: []byte(`{not json}`), RetryCount: 2},
			{ID: "K2", Exchange: "ex", RoutingKey: "k"},
		},
	}
	r, _, _ := newRelayWithLanes(store)

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))
	assert.Equal(t, 1, store.MarkDeadLetteredCalls)
	assert.Equal(t, "K2", store.MarkPublishedLastID, "K2 is attempted, not parked behind a terminal row")
}

// --- down-scopes --------------------------------------------------------------

// TestRelayScopeDownHoldsThatScopeOnly pins the stream lane's counterpart to a lane outage.
// A bound-but-unconfirming producer costs the publish bound PER ROW, so a full batch would
// hold the leader transaction for batchsize x publishtimeout; one such failure is evidence
// enough to leave the rest of that scope's rows for the next cycle. Rows on another lane are
// a separate connection and must still drain.
func TestRelayScopeDownHoldsThatScopeOnly(t *testing.T) {
	s1, s2, s3 := streamRow(), streamRow(), streamRow()
	// Distinct keys, so parking cannot explain either skip; two of them, so the skip tally
	// has to ACCUMULATE rather than merely be set.
	s2.ID, s2.PartitionKey = "S2", "beta"
	s3.ID, s3.PartitionKey = "S3", "gamma"
	store := &fakeStore{FetchPendingResult: []Record{s1, {ID: "A1", Exchange: "ex", RoutingKey: "a"}, s2, s3}}
	r, amqpLane, streamLane := newRelayWithLanes(store)
	streamLane.Plans = planByStream
	streamLane.Verdicts = []verdict{{Kind: shipScopeDown, Err: errors.New("producer is not carrying messages"), Waited: 40 * time.Millisecond}}
	log := newRecordingLogger()
	ctx := newFakeJobCtx(dbtesting.NewTestDB("postgresql"))
	ctx.log = log

	require.NoError(t, r.Execute(ctx))

	assert.Equal(t, []string{"S1"}, streamLane.shippedIDs(),
		"the later stream rows are left for the next cycle rather than paying the deadline again")
	assert.Equal(t, []string{"A1"}, amqpLane.shippedIDs(), "the AMQP row drains; the lanes are separate connections")
	assert.Equal(t, 1, store.MarkFailedCalls, "only the attempted stream row advanced retry_count")

	numbers := log.numbers()
	assert.Equal(t, int64(1), numbers["failed"], "the row that met the stalled producer is charged a failure")
	assert.Equal(t, int64(2), numbers["parked"], "both untried stream rows are held, not just the first")
	assert.Equal(t, int64(1), numbers["published"], "the AMQP row is the only publish this cycle")
	assert.Equal(t, int64(40), numbers["stall_wait_ms"])
	assert.Equal(t, int64(4), numbers["total"])
}

// TestRelayOneDownScopeDoesNotHoldTheOthers pins that a stall is tracked PER SCOPE. Each
// super stream has its own producer, so one unready producer is evidence about that stream
// alone. Holding every stream's rows on the first stall would let one misconfigured or
// restarting stream stop delivery for all of them, cycle after cycle.
func TestRelayOneDownScopeDoesNotHoldTheOthers(t *testing.T) {
	down1, down2 := streamRow(), streamRow()
	down2.ID, down2.PartitionKey = "D2", "beta" // its own key, so parking cannot explain the skip
	healthy := streamRow()
	healthy.ID, healthy.Stream, healthy.PartitionKey = "H1", "orders", "acme"
	store := &fakeStore{FetchPendingResult: []Record{down1, healthy, down2}}
	r, _, streamLane := newRelayWithLanes(store)
	streamLane.Plans = planByStream
	streamLane.Verdicts = []verdict{{Kind: shipScopeDown, Err: errors.New("producer is not carrying messages")}} // S1
	log := newRecordingLogger()
	ctx := newFakeJobCtx(dbtesting.NewTestDB("postgresql"))
	ctx.log = log

	require.NoError(t, r.Execute(ctx))

	assert.Equal(t, []string{"S1", "H1"}, streamLane.shippedIDs(),
		"the healthy stream's row drains in the same cycle; the stalled one is attempted once, then held")
	numbers := log.numbers()
	assert.Equal(t, int64(1), numbers["published"], "the healthy row is the cycle's one publish")
	assert.Equal(t, int64(1), numbers["failed"], "only the row that met the stalled producer is charged")
	assert.Equal(t, int64(1), numbers["parked"], "the stalled stream's second row waits; the healthy one did not")
}

// planByStream mirrors the stream adapter's planning closely enough for the relay's own
// scope bookkeeping: the scope is the super stream, the key is the stream and partition key.
func planByStream(rec *Record, headers map[string]any) shipment {
	return shipment{
		Record: rec, Headers: headers,
		Key:   LaneStream + ":" + rec.Stream + ":" + rec.PartitionKey,
		Scope: rec.Stream, Stamp: rec.PartitionKey,
	}
}

// --- the cycle log ------------------------------------------------------------

// TestRelayLogsPerLaneCounts pins that a cycle's summary says WHICH lane the work happened
// on: an aggregate "failed: 3" cannot distinguish a stalled super stream from a broker
// outage, which are different pages for whoever is holding it.
func TestRelayLogsPerLaneCounts(t *testing.T) {
	s2 := streamRow()
	s2.ID, s2.PartitionKey, s2.RetryCount = "S2", "beta", 99
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "A1", Exchange: "ex", RoutingKey: "a"},
		{ID: "A2", Exchange: "ex", RoutingKey: "b"},
		{ID: "A3", Exchange: "ex", RoutingKey: "b"}, // parks behind A2
		{ID: "X1", Lane: "carrier-pigeon", RetryCount: 99},
		streamRow(),
		s2,
	}}
	r, amqpLane, streamLane := newRelayWithLanes(store)
	streamLane.Plans = planByStream
	amqpLane.Verdicts = []verdict{{Kind: shipDelivered}, {Kind: shipRetry, Err: errors.New("broker rejected")}}                    // A1, then A2
	streamLane.Verdicts = []verdict{{Kind: shipDelivered}, {Kind: shipPoison, Err: errors.New("stream row has no partition key")}} // S1, then S2
	log := newRecordingLogger()
	ctx := newFakeJobCtx(dbtesting.NewTestDB("postgresql"))
	ctx.log = log

	require.NoError(t, r.Execute(ctx))

	numbers := log.numbers()
	assert.Equal(t, int64(1), numbers["published_amqp"])
	assert.Equal(t, int64(1), numbers["failed_amqp"])
	assert.Equal(t, int64(1), numbers["parked_amqp"])
	assert.Equal(t, int64(0), numbers["deadlettered_amqp"], "the unknown-lane row is neither lane's")
	assert.Equal(t, int64(1), numbers["published_stream"])
	assert.Equal(t, int64(0), numbers["failed_stream"])
	assert.Equal(t, int64(0), numbers["parked_stream"])
	assert.Equal(t, int64(1), numbers["deadlettered_stream"], "S2's poison is charged to its own lane")
	assert.Equal(t, int64(0), numbers["stall_wait_ms"], "no lane stalled this cycle")
	assert.Equal(t, int64(6), numbers["total"])
	assert.Equal(t, numbers["total"],
		numbers["published"]+numbers["unrecorded"]+numbers["failed"]+numbers["deadlettered"]+numbers["parked"],
		"every fetched row is accounted for exactly once")
}

// TestRelayAllOutageCycleStillLogsItsCounts pins that an all-outage cycle — every fetched
// record lands on a down lane, so runnable is empty — still emits the cycle summary. The
// mixed-lane path already logs before returning; a cycle where nothing is runnable must not
// skip that log line, or an operator loses the failed_<lane> counts for the exact cycles
// where they matter most.
func TestRelayAllOutageCycleStillLogsItsCounts(t *testing.T) {
	store := &fakeStore{FetchPendingResult: []Record{
		{ID: "evt-1", Exchange: "ex", RoutingKey: "a"},
		{ID: "evt-2", Exchange: "ex", RoutingKey: "b"},
	}}
	r, amqpLane, _ := newRelayWithLanes(store)
	amqpLane.ReadyErr = errors.New("messaging not ready")
	log := newRecordingLogger()
	ctx := newFakeJobCtx(dbtesting.NewTestDB("postgresql"))
	ctx.log = log

	err := r.Execute(ctx)
	require.Error(t, err, "the lane error still surfaces at the job level")
	assert.Contains(t, err.Error(), "messaging not ready")

	numbers := log.numbers()
	assert.Equal(t, int64(2), numbers["failed_amqp"], "both outaged rows are charged to the AMQP lane")
	assert.Equal(t, int64(2), numbers["total"], "the cycle summary still reports every fetched row")
}
