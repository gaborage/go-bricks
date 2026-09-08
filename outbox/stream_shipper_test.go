package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/publishdoor"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/messaging/streams"
)

// TestStreamShipperIsAlwaysReady pins that the stream lane has no lane-wide pre-flight:
// handles are per super stream, so readiness is per target and is judged in Ship.
func TestStreamShipperIsAlwaysReady(t *testing.T) {
	assert.NoError(t, streamShipperWith(nil).Ready(context.Background()),
		"a lane whose targets all vanished is still not a LANE outage")
	assert.NoError(t, streamShipperWith(&fakeStreamPublisher{}).Ready(context.Background()))
}

func TestStreamShipperPlansConfigDriftAsPoison(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*Record)
		wantReason string
	}{
		{
			name:       "stream_left_the_configured_targets",
			mutate:     func(r *Record) { r.Stream = "payments" },
			wantReason: `stream "payments" is not an outbox target`,
		},
		{
			name:       "row_has_no_partition_key",
			mutate:     func(r *Record) { r.PartitionKey = "" },
			wantReason: "stream row has no partition key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := streamShipperWith(&fakeStreamPublisher{})
			rec := streamRow()
			if tt.mutate != nil {
				tt.mutate(&rec)
			}

			ship := s.Plan(&rec, map[string]any{})

			assert.Equal(t, tt.wantReason, ship.Poison)
			assert.NotEmpty(t, ship.Key, "a refused row still holds its stream's order")
		})
	}
}

func TestStreamShipperPlansTheStreamAsItsDownScope(t *testing.T) {
	s := streamShipperWith(&fakeStreamPublisher{})
	rec := streamRow()
	headers := map[string]any{messaging.TenantStampHeader: "acme"}

	ship := s.Plan(&rec, headers)

	require.Empty(t, ship.Poison)
	assert.Equal(t, "stream:customers:acme", ship.Key,
		"the stream AND the partition key: rows aimed at different super streams do not park each other")
	assert.Equal(t, "customers", ship.Scope, "one stalled super stream holds back only its own rows")
	assert.Equal(t, "acme", ship.Stamp, "a stream row keeps its tenant in the partition key, never in its headers")
	assert.NotContains(t, ship.Headers, messaging.TenantStampHeader)
}

// TestStreamShipperStripsTheContentTypeStamp pins that the framework's own bookkeeping
// (ADR-105) leaves the headers on this lane too, or a stream row would carry it as a
// message property.
func TestStreamShipperStripsTheContentTypeStamp(t *testing.T) {
	pub := &fakeStreamPublisher{}
	rec := streamRow()
	headers := map[string]any{headerContentTypeStamp: publishdoor.ContentTypeJSON, "x-correlation-id": "abc"}

	ship := streamShipperWith(pub).Plan(&rec, headers)

	require.Empty(t, ship.Poison)
	assert.NotContains(t, ship.Headers, headerContentTypeStamp)
	assert.Equal(t, "abc", ship.Headers["x-correlation-id"], "a caller header is untouched")
}

// TestStreamShipperPlansWithNilHeaders pins the nil-safety the relay leans on when a row's
// headers would not decode but its key is still needed to park behind.
func TestStreamShipperPlansWithNilHeaders(t *testing.T) {
	rec := streamRow()

	ship := streamShipperWith(&fakeStreamPublisher{}).Plan(&rec, nil)

	require.Empty(t, ship.Poison)
	assert.Equal(t, "stream:customers:acme", ship.Key)
	assert.Nil(t, ship.Headers)
}

func TestStreamShipperShipsWithThePartitionKeyAsRoutingKey(t *testing.T) {
	pub := &fakeStreamPublisher{}
	s := streamShipperWith(pub)
	rec := streamRow()
	ship := shipment{Record: &rec, Headers: map[string]any{HeaderEventID: "S1"}, Stamp: "acme"}

	v := s.Ship(context.Background(), &ship)

	assert.Equal(t, shipDelivered, v.Kind)
	require.Equal(t, 1, pub.Calls)
	assert.Equal(t, "acme", pub.LastMsg.RoutingKey, "the partition key selects the partition")
	assert.Equal(t, []byte("p"), pub.LastMsg.Data)
	assert.Equal(t, "S1", pub.LastMsg.Properties[HeaderEventID])
	assert.NotContains(t, pub.LastMsg.Properties, messaging.TenantStampHeader,
		"the stamp rides the context so the publisher sets it; the relay must not supply one")
}

// TestStreamShipperHoldsAnUnreadyProducerWithoutWaiting is what Publisher.Ready() buys:
// a producer that is reconnecting, closed or not yet bound is known to be unusable
// without paying the publish bound to find out.
func TestStreamShipperHoldsAnUnreadyProducerWithoutWaiting(t *testing.T) {
	pub := &fakeStreamPublisher{NotReady: true}
	s := streamShipperWith(pub)
	rec := streamRow()

	v := s.Ship(context.Background(), &shipment{Record: &rec, Headers: map[string]any{}, Stamp: "acme", Scope: "customers"})

	assert.Equal(t, shipScopeDown, v.Kind)
	assert.Equal(t, time.Duration(0), v.Waited, "an unready producer is known without waiting for it")
	assert.Zero(t, pub.Calls, "no publish is attempted through a producer that is not carrying messages")
	require.Error(t, v.Err, "the relay writes this into the ledger, so it must say something")
	assert.ErrorIs(t, v.Err, streams.ErrPublisherNotStarted)
}

// TestStreamShipperAbortsOnAClosedPublisherWithoutWriting pins the shutdown window:
// stopSlots closes the stream publishers BEFORE the scheduler cancels the relay job's
// context, so between the two a row meets a closed producer while its context is still
// live. Reading that as an unready target would charge the row a retry_count bump for the
// process going down; it is a shutdown, and shipAborted writes nothing (the relay-level
// guarantee is TestRelayAbortedShipDoesNotInflateRetryCount).
func TestStreamShipperAbortsOnAClosedPublisherWithoutWriting(t *testing.T) {
	pub := &fakeStreamPublisher{IsClosed: true, NotReady: true}
	s := streamShipperWith(pub)
	rec := streamRow()

	v := s.Ship(context.Background(), &shipment{Record: &rec, Headers: map[string]any{}, Stamp: "acme", Scope: "customers"})

	assert.Equal(t, shipAborted, v.Kind, "a closed producer is a shutdown, not this row failing")
	require.ErrorIs(t, v.Err, streams.ErrPublisherClosed)
	assert.Zero(t, pub.Calls, "nothing is published through a closed producer")
}

func TestStreamShipperClassifiesItsPublishFailures(t *testing.T) {
	confirmErr := errors.New(`publish to stream "customers" was not confirmed by the broker`)
	tests := []struct {
		name       string
		publishErr error
		wantKind   verdictKind
		wantReason string
	}{
		{name: "delivered", wantKind: shipDelivered},
		{name: "canceled_is_not_a_delivery_failure", publishErr: context.Canceled, wantKind: shipAborted},
		{name: "closed_publisher_is_not_a_delivery_failure", publishErr: streams.ErrPublisherClosed, wantKind: shipAborted},
		{
			name: "stamp_conflict_is_poison", publishErr: messaging.ErrTenantStampConflict,
			wantKind: shipPoison, wantReason: messaging.ErrTenantStampConflict.Error(),
		},
		{name: "publisher_not_started_stalls_the_stream", publishErr: streams.ErrPublisherNotStarted, wantKind: shipScopeDown},
		{name: "deadline_stalls_the_stream", publishErr: context.DeadlineExceeded, wantKind: shipScopeDown},
		{name: "unconfirmed_publish_is_an_ordinary_failure", publishErr: confirmErr, wantKind: shipRetry},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := &fakeStreamPublisher{Err: tt.publishErr}
			s := streamShipperWith(pub)
			rec := streamRow()

			v := s.Ship(context.Background(), &shipment{Record: &rec, Headers: map[string]any{}, Stamp: "acme", Scope: "customers"})

			assert.Equal(t, tt.wantKind, v.Kind)
			if tt.wantReason != "" {
				assert.Equal(t, tt.wantReason, v.Err.Error(), "the poison text is what the ledger records")
			}
			if tt.wantKind == shipRetry || tt.wantKind == shipScopeDown {
				assert.ErrorIs(t, v.Err, tt.publishErr)
			}
		})
	}
}

// TestStreamShipperShipsPoisonAsATotalityGuardForAnUnknownTarget keeps Ship total: the
// target map is written once before any job runs, so this is not a plan-to-ship race but
// the guard that a shipment reaching Ship without Plan's verdict still gets one.
func TestStreamShipperShipsPoisonAsATotalityGuardForAnUnknownTarget(t *testing.T) {
	s := streamShipperWith(nil)
	rec := streamRow()

	v := s.Ship(context.Background(), &shipment{Record: &rec, Headers: map[string]any{}, Stamp: "acme"})

	assert.Equal(t, shipPoison, v.Kind)
	require.Error(t, v.Err)
	assert.Equal(t, `stream "customers" is not an outbox target`, v.Err.Error())
}
