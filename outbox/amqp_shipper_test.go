package outbox

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/app"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	"github.com/gaborage/go-bricks/internal/publishdoor"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
)

func TestAMQPShipperReadyReportsWhetherTheLaneIsUsable(t *testing.T) {
	resolverErr := errors.New("no broker for tenant")
	tests := []struct {
		name    string
		client  func(context.Context) (messaging.AMQPClient, error)
		wantErr string
	}{
		{
			name:    "resolver_fails",
			client:  func(context.Context) (messaging.AMQPClient, error) { return nil, resolverErr },
			wantErr: "messaging not available: no broker for tenant",
		},
		{
			name:    "resolver_returns_no_client",
			client:  func(context.Context) (messaging.AMQPClient, error) { return nil, nil },
			wantErr: "messaging not ready",
		},
		{
			name: "client_is_not_ready",
			client: func(context.Context) (messaging.AMQPClient, error) {
				f := newFakeAMQP()
				f.Ready = false
				return f, nil
			},
			wantErr: "messaging not ready",
		},
		{
			name:   "client_is_ready",
			client: func(context.Context) (messaging.AMQPClient, error) { return newFakeAMQP(), nil },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &amqpShipper{client: tt.client}
			err := s.Ready(context.Background())
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.wantErr, err.Error())
		})
	}
}

func TestAMQPShipperPlansTheOrderingKeyAndStamp(t *testing.T) {
	tests := []struct {
		name      string
		headers   map[string]any
		wantKey   string
		wantStamp string
	}{
		{
			name:    "unstamped_row_orders_by_destination",
			headers: map[string]any{},
			wantKey: "amqp:ex:rk",
		},
		{
			// The relay asks for a key this way when a row's own headers would not
			// decode, so the key must still come out — and it must be the destination,
			// since nothing could have been read to say otherwise.
			name:    "nil_headers_still_yield_the_destination_key",
			wantKey: "amqp:ex:rk",
		},
		{
			name:      "stamped_row_orders_by_tenant",
			headers:   map[string]any{messaging.TenantStampHeader: "acme"},
			wantKey:   "amqp:tenant:acme",
			wantStamp: "acme",
		},
		{
			name:    "empty_valued_stamp_orders_by_destination",
			headers: map[string]any{messaging.TenantStampHeader: ""},
			wantKey: "amqp:ex:rk",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &amqpShipper{}
			rec := &Record{ID: "evt-1", Exchange: "ex", RoutingKey: "rk"}

			ship := s.Plan(rec, tt.headers)

			assert.Empty(t, ship.Poison, "the AMQP lane can refuse nothing without a client")
			assert.Equal(t, tt.wantKey, ship.Key)
			assert.Equal(t, tt.wantStamp, ship.Stamp)
			assert.Empty(t, ship.Scope, "the AMQP lane has no sub-scope; an outage takes the whole lane")
			assert.NotContains(t, ship.Headers, messaging.TenantStampHeader,
				"the stamp is removed on PRESENCE: the conflict check keys on the header existing at all")
		})
	}
}

func TestAMQPShipperShipsTheRecordToItsDestination(t *testing.T) {
	f := newFakeAMQP()
	s := newAMQPShipperWithFake(f)
	rec := &Record{ID: "evt-1", Exchange: "orders", RoutingKey: "created", Payload: []byte(`{"id":1}`)}
	ship := shipment{Record: rec, Headers: map[string]any{"x-correlation-id": "abc"}}

	v := s.Ship(context.Background(), &ship)

	assert.Equal(t, shipDelivered, v.Kind)
	require.NoError(t, v.Err)
	require.Equal(t, 1, f.PublishCalls)
	assert.Equal(t, publishdoor.Options{
		Exchange: "orders", RoutingKey: "created", Headers: ship.Headers,
		Props: &publishdoor.MessageProps{MessageID: "evt-1"},
	}, f.LastPublishOpts)
	assert.Equal(t, []byte(`{"id":1}`), f.LastPublishData)
}

func TestAMQPShipperClassifiesItsPublishFailures(t *testing.T) {
	destinationErr := fmt.Errorf("%w: routing key is 256 bytes, limit is 255", messaging.ErrInvalidPublishDestination)
	nackErr := fmt.Errorf("%w after 5 attempts: %w", messaging.ErrPublishRetriesExhausted, messaging.ErrPublishNacked)
	// The resolver Ready already reports as "the lane is unusable" — a nil client with no
	// error. Ship must read that condition identically, so the expected text is asked of
	// Ready itself rather than hand-spelled, and the two can never drift apart.
	nilClientResolver := func(context.Context) (messaging.AMQPClient, error) { return nil, nil }

	tests := []struct {
		name       string
		publishErr error
		dropsReady bool
		client     func(context.Context) (messaging.AMQPClient, error)
		wantKind   verdictKind
		wantReason string
	}{
		{name: "delivered", wantKind: shipDelivered},
		{name: "canceled_is_not_a_delivery_failure", publishErr: context.Canceled, wantKind: shipAborted},
		{name: "shutdown_is_not_a_delivery_failure", publishErr: messaging.ErrShutdown, wantKind: shipAborted},
		{
			name: "unwritable_destination_is_poison", publishErr: destinationErr,
			wantKind: shipPoison, wantReason: destinationErr.Error(),
		},
		{
			name: "stamp_conflict_is_poison", publishErr: messaging.ErrTenantStampConflict,
			wantKind: shipPoison, wantReason: messaging.ErrTenantStampConflict.Error(),
		},
		{
			name: "not_connected_and_still_unready_is_a_broker_drop", publishErr: messaging.ErrNotConnected,
			dropsReady: true, wantKind: shipBrokerDown,
		},
		{
			name: "not_connected_but_ready_again_is_an_ordinary_failure", publishErr: messaging.ErrNotConnected,
			wantKind: shipRetry,
		},
		{name: "a_nack_is_an_ordinary_failure", publishErr: nackErr, wantKind: shipRetry},
		{name: "confirmation_timeout_is_an_ordinary_failure", publishErr: messaging.ErrPublishConfirmTimeout, wantKind: shipRetry},
		{
			name: "nil_client_is_broker_down", client: nilClientResolver,
			wantKind: shipBrokerDown, wantReason: "messaging not ready",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAMQP()
			f.PublishErr = tt.publishErr
			if tt.dropsReady {
				f.PublishHook = func(f *fakeAMQP) { f.Ready = false }
			}
			s := newAMQPShipperWithFake(f)
			if tt.client != nil {
				s = &amqpShipper{client: tt.client}
			}
			rec := &Record{ID: "evt-1", Exchange: "ex", RoutingKey: "rk"}

			v := s.Ship(context.Background(), &shipment{Record: rec, Headers: map[string]any{}})

			assert.Equal(t, tt.wantKind, v.Kind)
			if tt.wantReason != "" {
				assert.Equal(t, tt.wantReason, v.Err.Error(), "the poison text is what the ledger records")
			}
			if tt.client == nil && (tt.wantKind == shipRetry || tt.wantKind == shipBrokerDown) {
				assert.ErrorIs(t, v.Err, tt.publishErr, "the relay writes this error into the ledger")
			}
		})
	}
}

// TestAMQPShipperReportsABrokerDropWhenItsClientCannotBeResolved pins that a client that
// disappears mid-cycle is an outage, not a per-record fault: the relay stops the cycle
// rather than resolving a dead broker once per remaining row.
func TestAMQPShipperReportsABrokerDropWhenItsClientCannotBeResolved(t *testing.T) {
	resolverErr := errors.New("tenant broker gone")
	s := &amqpShipper{client: func(context.Context) (messaging.AMQPClient, error) { return nil, resolverErr }}

	v := s.Ship(context.Background(), &shipment{Record: &Record{ID: "evt-1"}, Headers: map[string]any{}})

	assert.Equal(t, shipBrokerDown, v.Kind)
	assert.ErrorIs(t, v.Err, resolverErr)
}

// TestAMQPShipperReportsWaitedWhenClientResolutionStalls pins that a resolver failure still
// counts toward stall_wait_ms: the production resolver (messaging.Manager.Publisher via
// m.getMsg) can block until the bounded publish context expires before returning an error, so
// Ship must time the resolver call rather than reporting zero wait for a stall that happened.
func TestAMQPShipperReportsWaitedWhenClientResolutionStalls(t *testing.T) {
	resolverErr := errors.New("tenant broker gone")
	const minWait = 5 * time.Millisecond
	s := &amqpShipper{client: func(context.Context) (messaging.AMQPClient, error) {
		time.Sleep(minWait)
		return nil, resolverErr
	}}

	v := s.Ship(context.Background(), &shipment{Record: &Record{ID: "evt-1"}, Headers: map[string]any{}})

	assert.Equal(t, shipBrokerDown, v.Kind)
	assert.GreaterOrEqual(t, v.Waited, minWait)
}

// compile-time proof the production adapters satisfy the port the relay holds.
var (
	_ shipper = (*amqpShipper)(nil)
	_ shipper = (*streamShipper)(nil)
)

// publishedRows runs Publish for each event under ctx and returns the rows exactly as the
// ledger holds them, so the key test exercises the stamp the WRITER produced rather than a
// hand-crafted one.
func publishedRows(ctx context.Context, t *testing.T, events ...*app.OutboxEvent) []Record {
	t.Helper()
	store := &mockStore{}
	pub := newPublisher(store, "", nil)
	rows := make([]Record, 0, len(events))
	for _, event := range events {
		_, err := pub.Publish(ctx, &mockTx{}, event)
		require.NoError(t, err)
		rows = append(rows, *store.insertedRecords[len(store.insertedRecords)-1])
	}
	return rows
}

// TestAMQPShipperKeysTheRowsPublishWroteByTenant is the write side of the ordering key:
// rows Publish persisted for two tenants key by the stamp IT wrote, spanning routing keys
// within a tenant and never across tenants. A hand-written header would prove nothing
// about what the writer actually stores.
func TestAMQPShipperKeysTheRowsPublishWroteByTenant(t *testing.T) {
	acme := multitenant.SetTenant(context.Background(), "acme")
	beta := multitenant.SetTenant(context.Background(), "beta")
	rows := publishedRows(acme, t,
		&app.OutboxEvent{EventType: "e", AggregateID: "A1", Exchange: "ex", RoutingKey: "a"},
		&app.OutboxEvent{EventType: "e", AggregateID: "A2", Exchange: "ex", RoutingKey: "b"},
	)
	rows = append(rows, publishedRows(beta, t,
		&app.OutboxEvent{EventType: "e", AggregateID: "B1", Exchange: "ex", RoutingKey: "a"},
	)...)

	s := &amqpShipper{}
	keys := make([]string, 0, len(rows))
	for i := range rows {
		headers, err := decodeHeaders(rows[i].Headers)
		require.NoError(t, err)
		keys = append(keys, s.Plan(&rows[i], headers).Key)
	}

	assert.Equal(t, keys[0], keys[1],
		"one tenant's rows share a key across routing keys, which is the ordering a tenant needs")
	assert.NotEqual(t, keys[0], keys[2],
		"B1's routing key equals A1's — Publish stamped it for another tenant, so it is another key")
}

// TestLaneKeysNeverCollideAcrossLanes pins that the lane prefix is load-bearing, not
// decoration. Parking is head-of-line blocking, so two rows that share a key but not a
// destination would block each other for nothing: a stream literally named "amqp" whose
// partition key begins "tenant:" would otherwise produce a tenant-stamped AMQP row's key.
func TestLaneKeysNeverCollideAcrossLanes(t *testing.T) {
	amqpLane, streamLane := &amqpShipper{}, streamShipperWith(&fakeStreamPublisher{})
	plan := func(s shipper, rec Record, headers map[string]any) string {
		return s.Plan(&rec, headers).Key
	}
	stamped := func() map[string]any { return map[string]any{messaging.TenantStampHeader: "acme"} }

	amqpStamped := plan(amqpLane, Record{Exchange: "ex", RoutingKey: "created"}, stamped())
	amqpPlain := plan(amqpLane, Record{Exchange: "ex", RoutingKey: "created"}, map[string]any{})
	streamAcme := plan(streamLane, Record{Lane: LaneStream, Stream: "customers", PartitionKey: "acme"}, map[string]any{})

	assert.NotEqual(t, amqpStamped, amqpPlain, "a stamped row orders by tenant, not by destination")
	assert.NotEqual(t, amqpStamped, streamAcme, "a partition key and a tenant stamp are different scopes")
	assert.NotEqual(t, amqpPlain,
		plan(amqpLane, Record{Exchange: "billing", RoutingKey: "created"}, map[string]any{}),
		"the destination includes the exchange, not the routing key alone")
	assert.Equal(t, amqpStamped,
		plan(amqpLane, Record{Exchange: "other", RoutingKey: "shipped"}, stamped()),
		"same scope still collapses to one key, which is what parking depends on")
}

// relayShipped drains one relay cycle over rows the ledger holds through the production
// AMQP adapter, and returns what reached the door. The whole path is exercised — the
// relay's own header injection included — because the properties must MIRROR those
// headers, which a hand-built shipment could not show.
func relayShipped(t *testing.T, rows ...Record) *fakeAMQP {
	t.Helper()
	f := newFakeAMQP()
	store := &fakeStore{FetchPendingResult: rows}
	r := newRelayWithShippers(store, map[string]shipper{LaneAMQP: newAMQPShipperWithFake(f)})

	require.NoError(t, r.Execute(newFakeJobCtx(dbtesting.NewTestDB("postgresql"))))
	require.Equal(t, len(rows), f.PublishCalls)
	return f
}

// TestOutboxShipperCarriesTheEventProperties pins ADR-105 on the AMQP lane: the row's id
// and event type reach the door as message properties AND stay in the headers, since the
// id header is the ledger key consumers dedupe on (ADR-097).
func TestOutboxShipperCarriesTheEventProperties(t *testing.T) {
	rows := publishedRows(context.Background(), t, &app.OutboxEvent{
		EventType:   "order.created",
		AggregateID: "A1",
		Exchange:    "orders",
		RoutingKey:  "created",
		Payload:     map[string]any{"id": 1},
	})

	f := relayShipped(t, rows...)

	require.NotNil(t, f.LastPublishOpts.Props)
	assert.Equal(t, rows[0].ID, f.LastPublishOpts.Props.MessageID)
	assert.Equal(t, "order.created", f.LastPublishOpts.Props.EventType)
	assert.Equal(t, rows[0].ID, f.LastPublishHdrs[HeaderEventID],
		"the properties mirror the headers, they do not replace them")
	assert.Equal(t, "order.created", f.LastPublishHdrs[HeaderEventType])
}

// TestOutboxShipperCarriesThePayloadContentType pins that the encoding resolved at enqueue
// travels in the persisted headers and leaves them again as a property: caller bytes are
// opaque and are never sniffed, so they name no encoding at all.
func TestOutboxShipperCarriesThePayloadContentType(t *testing.T) {
	tests := []struct {
		name    string
		payload any
		want    string
	}{
		{name: "marshaled_struct_is_json", payload: map[string]any{"id": 1}, want: "application/json"},
		{name: "nil_payload_is_the_json_literal_null", payload: nil, want: "application/json"},
		{name: "caller_supplied_bytes_are_opaque", payload: []byte(`{"id":1}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := publishedRows(context.Background(), t, &app.OutboxEvent{
				EventType:   "order.created",
				AggregateID: "A1",
				Exchange:    "orders",
				RoutingKey:  "created",
				Payload:     tt.payload,
			})
			stored, err := decodeHeaders(rows[0].Headers)
			require.NoError(t, err)
			if tt.want == "" {
				assert.Nil(t, rows[0].Headers, "opaque bytes leave nothing to persist")
			} else {
				assert.Equal(t, tt.want, stored[headerContentTypeStamp], "the enqueue side records it")
			}

			f := relayShipped(t, rows...)

			require.NotNil(t, f.LastPublishOpts.Props)
			assert.Equal(t, tt.want, f.LastPublishOpts.Props.ContentType)
			assert.NotContains(t, f.LastPublishHdrs, headerContentTypeStamp,
				"the stamp is the framework's bookkeeping and must not reach the wire")
		})
	}
}

// TestOutboxShipperStripsAPreUpgradeCallerStamp covers the rows the enqueue refusal
// cannot reach: one persisted before Publish began refusing the reserved prefix still
// carries a caller-spelled stamp, and the relay's strip is all that keeps it off the wire.
// The casing arms matter because the refusal is case-folded while the ledger is not: a row
// written before it could spell the prefix any way at all.
func TestOutboxShipperStripsAPreUpgradeCallerStamp(t *testing.T) {
	tests := map[string]struct {
		headers string
		stamped string
	}{
		"canonical_spelling":  {headers: `{"x-gobricks-content-type":"application/json","keep":"me"}`, stamped: headerContentTypeStamp},
		"mixed_case_spelling": {headers: `{"X-GoBricks-Content-Type":"application/json","keep":"me"}`, stamped: "X-GoBricks-Content-Type"},
		"upper_case_prefix":   {headers: `{"X-GOBRICKS-ANYTHING":"whatever","keep":"me"}`, stamped: "X-GOBRICKS-ANYTHING"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			row := Record{
				ID:          "11111111-2222-4333-8444-555555555555",
				EventType:   "order.created",
				AggregateID: "A1",
				Payload:     []byte(`{"id":1}`),
				Headers:     []byte(tt.headers),
				Exchange:    "orders",
				RoutingKey:  "created",
				Lane:        LaneAMQP,
				Status:      StatusPending,
			}

			f := relayShipped(t, row)

			assert.NotContains(t, f.LastPublishHdrs, tt.stamped,
				"a header in the framework's namespace must not reach the wire whatever its casing")
			assert.NotContains(t, f.LastPublishHdrs, headerContentTypeStamp)
			assert.Equal(t, "me", f.LastPublishHdrs["keep"], "its other headers still travel")
		})
	}
}
