package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-playground/validator/v10"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errBusiness stands in for a consumer's own sentinel: the adapter must return
// fn's error untouched, or errors.Is against it stops working.
var errBusiness = errors.New("orders: already processed")

// mccPayload carries a framework-custom tag. validator PANICS on an unregistered
// tag, so a passing run proves the shared validator came from validation.New and
// not a bare validator.New().
type mccPayload struct {
	MCC string `json:"mcc" validate:"mcc_code"`
}

func validBody(t *testing.T) []byte {
	t.Helper()

	body, err := json.Marshal(orderPayload{Reference: "abc", Amount: 7})
	require.NoError(t, err)

	return body
}

func TestNewTypedHandlerDecodesAndInvokesFn(t *testing.T) {
	var got orderPayload
	var calls int
	h := NewTypedHandler(orderEventType, func(_ context.Context, p orderPayload) error {
		got = p
		calls++
		return nil
	})

	require.NoError(t, h.Handle(t.Context(), &amqp.Delivery{Body: validBody(t)}))
	assert.Equal(t, 1, calls)
	assert.Equal(t, orderPayload{Reference: "abc", Amount: 7}, got)
}

// The worker loop hands Handle a context carrying the extracted W3C trace and
// the per-message lease scope that deps.DB/deps.Cache borrow tenant handles
// from. Substituting context.Background() would orphan every span and resolve
// handles outside the scope, with nothing else in the suite noticing.
func TestNewTypedHandlerPassesCallerContextToFn(t *testing.T) {
	type ctxKey struct{}
	var seen any
	h := NewTypedHandler(orderEventType, func(ctx context.Context, _ orderPayload) error {
		seen = ctx.Value(ctxKey{})
		return nil
	})

	ctx := context.WithValue(t.Context(), ctxKey{}, "lease-scope")
	require.NoError(t, h.Handle(ctx, &amqp.Delivery{Body: validBody(t)}))
	assert.Equal(t, "lease-scope", seen)
}

// Each delivery must decode into a zero-valued T. A shared payload — a struct
// field, or a sync.Pool added for the allocation win — would leave delivery N+1
// inheriting the fields N set but N+1 omits, and a mutex would hide that from
// the race detector.
func TestNewTypedHandlerAllocatesFreshPayloadPerDelivery(t *testing.T) {
	var seen []orderPayload
	h := NewTypedHandler(orderEventType, func(_ context.Context, p orderPayload) error {
		seen = append(seen, p)
		return nil
	})

	require.NoError(t, h.Handle(t.Context(), &amqp.Delivery{Body: []byte(`{"reference":"abc","amount":7}`)}))
	require.NoError(t, h.Handle(t.Context(), &amqp.Delivery{Body: []byte(`{"amount":9}`)}))
	require.Len(t, seen, 2)
	assert.Equal(t, orderPayload{Reference: "", Amount: 9}, seen[1], "Reference must not survive from the previous delivery")

	// A stale Amount would also satisfy `required` and skip the validation error.
	assert.ErrorIs(t, h.Handle(t.Context(), &amqp.Delivery{Body: []byte(`{"reference":"xy"}`)}), ErrPayloadInvalid)
}

// The shared validator is a performance contract: go-playground caches struct
// metadata per instance, so constructing one per delivery re-registers the
// custom rules and rebuilds the cache every message. Measured, that is ~7-8
// allocs/op against ~222 — the ceiling has wide margin on both sides and is the
// only thing that catches Handle bypassing the package-level typedValidator.
func TestNewTypedHandlerReusesOneValidator(t *testing.T) {
	h := NewTypedHandler(orderEventType, func(_ context.Context, _ orderPayload) error { return nil })
	ctx := t.Context()
	body := validBody(t)

	avg := testing.AllocsPerRun(200, func() {
		_ = h.Handle(ctx, &amqp.Delivery{Body: body})
	})
	assert.Less(t, avg, 40.0, "per-delivery validator construction would push this past 200")
}

func TestNewTypedHandlerReturnsFnResult(t *testing.T) {
	h := NewTypedHandler(orderEventType, func(_ context.Context, _ orderPayload) error {
		return errBusiness
	})

	err := h.Handle(t.Context(), &amqp.Delivery{Body: validBody(t)})

	require.ErrorIs(t, err, errBusiness)
	require.Same(t, errBusiness, err, "fn's error must pass through unwrapped")
	assert.NotErrorIs(t, err, ErrPayloadUndecodable)
	assert.NotErrorIs(t, err, ErrPayloadInvalid)
	assert.NotErrorAs(t, err, new(*PayloadError))
}

// The adapter is the seam where a naive implementation would echo the body, so
// each subtest plants a marker the raw cause (or the body) carries and asserts a
// positive premise before the negative one.
func TestNewTypedHandlerDecodeFailure(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		marker     string
		wantRender string
		causeLeaks bool
	}{
		{
			// UnmarshalTypeError.Value becomes "number 1234.56".
			name:       "numeric_literal_into_int",
			body:       `{"amount":` + numericMarker + `}`,
			marker:     numericMarker,
			wantRender: "json: type mismatch at field",
			causeLeaks: true,
		},
		{
			name:       "garbage_bytes",
			body:       "not json " + payloadMarker,
			marker:     payloadMarker,
			wantRender: "json: syntax error at offset",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewTypedHandler(orderEventType, func(_ context.Context, _ orderPayload) error {
				t.Error("fn must not run after a decode failure")
				return nil
			})

			err := h.Handle(t.Context(), &amqp.Delivery{Body: []byte(tc.body)})

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrPayloadUndecodable)
			assert.NotErrorIs(t, err, ErrPayloadInvalid)

			var perr *PayloadError
			require.ErrorAs(t, err, &perr)
			assert.Equal(t, PayloadStageDecode, perr.Stage)
			assert.Equal(t, orderEventType, perr.EventType)
			assert.Empty(t, perr.Fields())

			require.Contains(t, err.Error(), tc.wantRender, "premise: a decode summary was rendered")
			if tc.causeLeaks {
				require.Contains(t, perr.Unwrap().Error(), tc.marker, "premise: the raw cause leaks the value")
			}
			assert.NotContains(t, err.Error(), tc.marker)
		})
	}
}

func TestNewTypedHandlerValidationFailure(t *testing.T) {
	h := NewTypedHandler(orderEventType, func(_ context.Context, _ orderPayload) error {
		t.Error("fn must not run after a validation failure")
		return nil
	})
	body := fmt.Appendf(nil, `{"reference":%q,"amount":1}`, payloadMarker)

	err := h.Handle(t.Context(), &amqp.Delivery{Body: body})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPayloadInvalid)
	assert.NotErrorIs(t, err, ErrPayloadUndecodable)

	var perr *PayloadError
	require.ErrorAs(t, err, &perr)
	assert.Equal(t, PayloadStageValidate, perr.Stage)
	assert.Equal(t, []string{fieldReference}, perr.Fields())
	require.Contains(t, err.Error(), fieldReference, "premise: the field list was rendered")
	assert.NotContains(t, err.Error(), payloadMarker)
}

// End-to-end proof that the adapter routes through the sanitizer: a dived map
// interpolates its key into the validator namespace verbatim, so a PAN-shaped
// key reaches Fields() unredacted unless PayloadError is doing its job.
func TestNewTypedHandlerRedactsMapKeyNamespace(t *testing.T) {
	h := NewTypedHandler(orderEventType, func(_ context.Context, _ mapKeyPayload) error {
		t.Error("fn must not run after a validation failure")
		return nil
	})
	body := fmt.Appendf(nil, `{"limits":{%q:99}}`, pan)

	err := h.Handle(t.Context(), &amqp.Delivery{Body: body})

	require.Error(t, err)
	require.ErrorIs(t, err, ErrPayloadInvalid)

	var perr *PayloadError
	require.ErrorAs(t, err, &perr)
	require.Contains(t, perr.Unwrap().Error(), pan, "premise: the raw cause carries the map key")
	require.Equal(t, []string{"mapKeyPayload.Limits[*]"}, perr.Fields(), "premise: a namespace was derived and redacted")

	assert.NotContains(t, err.Error(), pan)
	for _, field := range perr.Fields() {
		assert.NotContains(t, field, pan)
	}
}

// A non-struct T decodes fine and then trips validator's own type guard. It must
// fail closed on the FIRST delivery rather than skipping validation forever.
func TestNewTypedHandlerNonStructPayloadFailsClosed(t *testing.T) {
	h := NewTypedHandler("Ints", func(_ context.Context, _ []int) error {
		t.Error("fn must not run for a non-struct payload")
		return nil
	})

	var err error
	require.NotPanics(t, func() {
		err = h.Handle(t.Context(), &amqp.Delivery{Body: []byte(`[1,2,3]`)})
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPayloadInvalid, "the failure is validate-stage, not decode-stage")
	assert.NotErrorIs(t, err, ErrPayloadUndecodable)
	require.ErrorAs(t, err, new(*validator.InvalidValidationError))

	var perr *PayloadError
	require.ErrorAs(t, err, &perr)
	assert.Empty(t, perr.Fields(), "a non-validator cause yields no field list")
	assert.Equal(t, `messaging: validate failed for event "Ints"`, err.Error())
}

func TestNewTypedHandlerNilDeliveryAndEmptyBody(t *testing.T) {
	tests := []struct {
		name     string
		delivery *amqp.Delivery
		want     string
	}{
		{
			name:     "nil_delivery",
			delivery: nil,
			want:     `messaging: decode failed for event "OrderCreated": nil delivery`,
		},
		{
			name:     "nil_body",
			delivery: &amqp.Delivery{},
			want:     `messaging: decode failed for event "OrderCreated": json: syntax error at offset 0`,
		},
		{
			name:     "empty_body",
			delivery: &amqp.Delivery{Body: []byte{}},
			want:     `messaging: decode failed for event "OrderCreated": json: syntax error at offset 0`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewTypedHandler(orderEventType, func(_ context.Context, _ orderPayload) error {
				t.Error("fn must not run without a body")
				return nil
			})

			var err error
			require.NotPanics(t, func() { err = h.Handle(t.Context(), tc.delivery) })

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrPayloadUndecodable)
			assert.Equal(t, tc.want, err.Error())
		})
	}
}

func TestNewTypedHandlerEventType(t *testing.T) {
	fn := func(_ context.Context, _ orderPayload) error { return nil }

	assert.Equal(t, orderEventType, NewTypedHandler(orderEventType, fn).EventType())
	assert.Equal(t, shipmentEventType, NewTypedHandler(shipmentEventType, fn).EventType())
}

// A nil fn would otherwise nil-panic on every delivery — recovered and nacked by
// the worker loop, so the wiring bug would present as silent message loss.
func TestNewTypedHandlerNilFuncPanics(t *testing.T) {
	assert.PanicsWithValue(t,
		"messaging: NewTypedHandler requires a non-nil handler function (event_type=OrderCreated)",
		func() { NewTypedHandler[orderPayload](orderEventType, nil) })
}

func TestTypedHandlerUsesFrameworkValidator(t *testing.T) {
	// OnceValue's caching is directly observable through pointer identity: two
	// calls returning the same instance is the whole contract. What CANNOT be
	// pinned here is a construction COUNT — validation.New is a plain function
	// with no seam to count through, and the package-level var is already
	// resolved by the time any test runs.
	require.Same(t, typedValidator(), typedValidator())

	// The instance is the framework's, not a bare validator.New(): an
	// unregistered mcc_code tag makes validator panic instead of returning.
	var calls atomic.Int64
	h := NewTypedHandler(orderEventType, func(_ context.Context, _ mccPayload) error {
		calls.Add(1)
		return nil
	})

	var invalid error
	require.NotPanics(t, func() {
		invalid = h.Handle(t.Context(), &amqp.Delivery{Body: []byte(`{"mcc":"12"}`)})
	})
	assert.ErrorIs(t, invalid, ErrPayloadInvalid)

	require.NoError(t, h.Handle(t.Context(), &amqp.Delivery{Body: []byte(`{"mcc":"5411"}`)}))
	assert.Equal(t, int64(1), calls.Load())

	// A second handler shares the same instance, which is what makes one adapter
	// per consumer safe across tenants.
	other := NewTypedHandler(shipmentEventType, func(_ context.Context, _ orderPayload) error { return nil })
	require.NoError(t, other.Handle(t.Context(), &amqp.Delivery{Body: validBody(t)}))

	// Sharing is not observable from Handle's own allocations — a per-HANDLER
	// validator costs nothing per delivery, so the sibling per-delivery ceiling
	// is blind to it. Construction is where it shows: validation.New registers
	// the custom rules and builds a fresh metadata cache, ~200 allocs against a
	// handler struct's handful.
	avg := testing.AllocsPerRun(50, func() {
		_ = NewTypedHandler(orderEventType, func(_ context.Context, _ orderPayload) error { return nil })
	})
	assert.Less(t, avg, 20.0, "a per-handler validator would push construction past 200")
}

// One adapter instance serves every worker of a consumer AND every tenant, so
// the -race run here is the actual production shape. Assertions off the test
// goroutine use t.Errorf: require.* calls runtime.Goexit, which is undefined
// outside the test goroutine.
// assertDelivery drives one delivery and reports mismatches with t.Errorf, never
// require.*: FailNow calls runtime.Goexit, which is undefined off the test
// goroutine. A want of nil expects success.
func assertDelivery(ctx context.Context, t *testing.T, h MessageHandler, kind string, body []byte, want error) {
	t.Helper()

	err := h.Handle(ctx, &amqp.Delivery{Body: body})
	switch {
	case want == nil && err != nil:
		t.Errorf("%s delivery failed: %v", kind, err)
	case want != nil && !errors.Is(err, want):
		t.Errorf("%s delivery: want %v, got %v", kind, want, err)
	case err != nil && strings.Contains(err.Error(), payloadMarker):
		t.Errorf("%s error leaked the payload marker: %v", kind, err)
	}
}

func TestNewTypedHandlerConcurrentDeliveries(t *testing.T) {
	const goroutines = 24

	var oks atomic.Int64
	h := NewTypedHandler(orderEventType, func(_ context.Context, p orderPayload) error {
		if p.Reference != "abc" || p.Amount != 7 {
			t.Errorf("fn saw a torn payload: %+v", p)
		}
		oks.Add(1)
		return nil
	})

	kinds := []struct {
		name string
		body []byte
		want error
	}{
		{name: "valid", body: validBody(t)},
		{name: "garbage", body: []byte("not json " + payloadMarker), want: ErrPayloadUndecodable},
		{name: "invalid", body: fmt.Appendf(nil, `{"reference":%q,"amount":1}`, payloadMarker), want: ErrPayloadInvalid},
	}
	var handled [3]atomic.Int64

	ctx := t.Context()
	var wg sync.WaitGroup
	for i := range goroutines {
		slot := i % len(kinds)
		wg.Add(1)
		go func() {
			defer wg.Done()
			assertDelivery(ctx, t, h, kinds[slot].name, kinds[slot].body, kinds[slot].want)
			handled[slot].Add(1)
		}()
	}
	wg.Wait()

	for i, k := range kinds {
		assert.Equal(t, int64(goroutines/len(kinds)), handled[i].Load(), "%s deliveries handled", k.name)
	}
	// Counted inside fn, so it also proves fn ran for every successful delivery
	// rather than the adapter short-circuiting.
	assert.Equal(t, int64(goroutines/len(kinds)), oks.Load())
}

func TestDeclareTypedConsumerRegistersAdapter(t *testing.T) {
	decls := NewDeclarations()
	var got orderPayload
	opts := &ConsumerOptions{Queue: testQueue, Consumer: testConsumer, EventType: orderEventType}

	decl := DeclareTypedConsumer(decls, opts, func(_ context.Context, p orderPayload) error {
		got = p
		return nil
	})

	require.NotNil(t, decl)
	assert.Same(t, opts.Handler, decl.Handler, "the declaration carries the adapter the options were given")

	// Drive the REGISTERED declaration, not the returned one: RegisterConsumer
	// copies the handler by reference, and that copy is what the worker calls.
	registered := decls.Consumers()
	require.Len(t, registered, 1)
	require.NotNil(t, registered[0].Handler)
	assert.Equal(t, orderEventType, registered[0].Handler.EventType())

	require.NoError(t, registered[0].Handler.Handle(t.Context(), &amqp.Delivery{Body: validBody(t)}))
	assert.Equal(t, orderPayload{Reference: "abc", Amount: 7}, got)

	err := registered[0].Handler.Handle(t.Context(), &amqp.Delivery{Body: []byte("not json")})
	assert.ErrorIs(t, err, ErrPayloadUndecodable)
}

func TestDeclareTypedConsumerAppliesConcurrencyDefaults(t *testing.T) {
	fn := func(_ context.Context, _ orderPayload) error { return nil }

	t.Run("auto_scaled", func(t *testing.T) {
		decls := NewDeclarations()
		opts := &ConsumerOptions{Queue: testQueue, Consumer: testConsumer, EventType: orderEventType}

		decl := DeclareTypedConsumer(decls, opts, fn)

		wantWorkers := min(runtime.NumCPU()*4, maxWorkersPerConsumer)
		assert.Equal(t, wantWorkers, decl.Workers)
		assert.Equal(t, min(wantWorkers*defaultPrefetchMultiplier, maxDefaultPrefetch), decl.PrefetchCount)
		assert.Equal(t, decl.Workers, opts.Workers, "DeclareConsumer defaults opts in place")
	})

	t.Run("explicit_sequential", func(t *testing.T) {
		decls := NewDeclarations()
		opts := &ConsumerOptions{
			Queue: testQueue, Consumer: testConsumer, EventType: orderEventType,
			Workers: 1, PrefetchCount: 3,
		}

		decl := DeclareTypedConsumer(decls, opts, fn)

		assert.Equal(t, 1, decl.Workers)
		assert.Equal(t, 3, decl.PrefetchCount)
	})
}

// The queue is the caller's to declare. Passing nil is the same pass-through an
// untyped DeclareConsumer has, and a missing queue surfaces at Validate().
func TestDeclareTypedConsumerLeavesQueueToTheCaller(t *testing.T) {
	fn := func(_ context.Context, _ orderPayload) error { return nil }

	decls := NewDeclarations()
	DeclareTypedConsumer(decls, &ConsumerOptions{
		Queue: missingQueue, Consumer: testConsumer, EventType: orderEventType,
	}, fn)
	assert.Empty(t, decls.Queues, "the helper declares no queue of its own")

	err := decls.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "consumer references non-existent queue: "+missingQueue)

	decls.DeclareQueue(missingQueue)
	assert.NoError(t, decls.Validate())
}

func TestDeclareTypedConsumerPanicsOnWiringMistakes(t *testing.T) {
	fn := func(_ context.Context, _ orderPayload) error { return nil }
	opts := func() *ConsumerOptions {
		return &ConsumerOptions{Queue: testQueue, Consumer: testConsumer, EventType: orderEventType}
	}

	assert.PanicsWithValue(t, "messaging: DeclareTypedConsumer requires a non-nil *Declarations",
		func() { DeclareTypedConsumer(nil, opts(), fn) })

	assert.PanicsWithValue(t, "messaging: DeclareTypedConsumer requires non-nil *ConsumerOptions",
		func() { DeclareTypedConsumer[orderPayload](NewDeclarations(), nil, fn) })

	// A hand-written handler already in opts means the caller wanted
	// DeclareConsumer; silently replacing it would drop their handler.
	taken := opts()
	mine := &MockMessageHandler{eventType: orderEventType}
	taken.Handler = mine
	decls := NewDeclarations()

	recovered := func() (msg any) {
		defer func() { msg = recover() }()
		DeclareTypedConsumer(decls, taken, fn)
		return nil
	}()

	require.NotNil(t, recovered, "a pre-set Handler must panic")
	assert.Contains(t, recovered, "ConsumerOptions.Handler must be nil")
	assert.Contains(t, recovered, "queue="+testQueue)
	assert.Same(t, mine, taken.Handler, "the caller's handler is not overwritten")
	assert.Empty(t, decls.Consumers(), "nothing is registered when the guard fires")
}

func TestMetadataAccessors(t *testing.T) {
	tests := []struct {
		name        string
		meta        Metadata
		wantHeaders amqp.Table
		wantType    string
		wantRedeliv bool
	}{
		{
			name: "populated_delivery",
			meta: Metadata{delivery: &amqp.Delivery{
				Headers:     amqp.Table{"x-outbox-event-id": "evt-1"},
				Type:        orderEventType,
				Redelivered: true,
			}},
			wantHeaders: amqp.Table{"x-outbox-event-id": "evt-1"},
			wantType:    orderEventType,
			wantRedeliv: true,
		},
		{
			name:        "zero_value_is_inert",
			meta:        Metadata{},
			wantHeaders: nil,
			wantType:    "",
			wantRedeliv: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantHeaders, tc.meta.Headers())
			assert.Equal(t, tc.wantType, tc.meta.EventType())
			assert.Equal(t, tc.wantRedeliv, tc.meta.Redelivered())
		})
	}
}

func TestNewTypedHandlerWithMeta(t *testing.T) {
	var got orderPayload
	var gotMeta Metadata
	var calls int
	h := NewTypedHandlerWithMeta(orderEventType, func(_ context.Context, p orderPayload, meta Metadata) error {
		got = p
		gotMeta = meta
		calls++
		return nil
	})

	delivery := &amqp.Delivery{
		Body:    validBody(t),
		Headers: amqp.Table{"x-outbox-event-id": "evt-1"},
	}
	require.NoError(t, h.Handle(t.Context(), delivery))
	assert.Equal(t, 1, calls)
	assert.Equal(t, orderPayload{Reference: "abc", Amount: 7}, got)
	assert.Equal(t, "evt-1", gotMeta.Headers()["x-outbox-event-id"])

	// The pre-fn pipeline is typedHandler.Handle's, asserted in depth against
	// NewTypedHandler above; the meta-carrying constructor only has to reach the
	// same short-circuits without ever calling fn.
	t.Run("failures_short_circuit_before_fn", func(t *testing.T) {
		tests := []struct {
			name     string
			delivery *amqp.Delivery
			wantErr  error
		}{
			{name: "nil_delivery", delivery: nil, wantErr: ErrPayloadUndecodable},
			{name: "undecodable_body", delivery: &amqp.Delivery{Body: []byte("not json")}, wantErr: ErrPayloadUndecodable},
			{
				name:     "invalid_payload",
				delivery: &amqp.Delivery{Body: []byte(`{"reference":"over-max-5","amount":1}`)},
				wantErr:  ErrPayloadInvalid,
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				h := NewTypedHandlerWithMeta(orderEventType, func(context.Context, orderPayload, Metadata) error {
					t.Error("fn must not run after a pre-dispatch failure")
					return nil
				})

				err := h.Handle(t.Context(), tc.delivery)

				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			})
		}
	})

	t.Run("nil_fn_panics", func(t *testing.T) {
		assert.PanicsWithValue(t,
			"messaging: NewTypedHandlerWithMeta requires a non-nil handler function (event_type=OrderCreated)",
			func() { NewTypedHandlerWithMeta[orderPayload](orderEventType, nil) })
	})
}

func TestDeclareTypedConsumerWithMeta(t *testing.T) {
	decls := NewDeclarations()
	var got orderPayload
	opts := &ConsumerOptions{Queue: testQueue, Consumer: testConsumer, EventType: orderEventType}

	decl := DeclareTypedConsumerWithMeta(decls, opts, func(_ context.Context, p orderPayload, _ Metadata) error {
		got = p
		return nil
	})

	require.NotNil(t, decl)
	assert.Same(t, opts.Handler, decl.Handler)

	registered := decls.Consumers()
	require.Len(t, registered, 1)
	require.NotNil(t, registered[0].Handler)
	assert.Equal(t, orderEventType, registered[0].Handler.EventType())

	require.NoError(t, registered[0].Handler.Handle(t.Context(), &amqp.Delivery{Body: validBody(t)}))
	assert.Equal(t, orderPayload{Reference: "abc", Amount: 7}, got)

	t.Run("panics_on_wiring_mistakes", func(t *testing.T) {
		fn := func(context.Context, orderPayload, Metadata) error { return nil }
		opts := func() *ConsumerOptions {
			return &ConsumerOptions{Queue: testQueue, Consumer: testConsumer, EventType: orderEventType}
		}

		assert.PanicsWithValue(t, "messaging: DeclareTypedConsumerWithMeta requires a non-nil *Declarations",
			func() { DeclareTypedConsumerWithMeta(nil, opts(), fn) })

		assert.PanicsWithValue(t, "messaging: DeclareTypedConsumerWithMeta requires non-nil *ConsumerOptions",
			func() { DeclareTypedConsumerWithMeta[orderPayload](NewDeclarations(), nil, fn) })

		taken := opts()
		mine := &MockMessageHandler{eventType: orderEventType}
		taken.Handler = mine
		decls := NewDeclarations()

		recovered := func() (msg any) {
			defer func() { msg = recover() }()
			DeclareTypedConsumerWithMeta(decls, taken, fn)
			return nil
		}()

		require.NotNil(t, recovered, "a pre-set Handler must panic")
		assert.Contains(t, recovered, "ConsumerOptions.Handler must be nil")
		assert.Contains(t, recovered, "queue="+testQueue)
		assert.Same(t, mine, taken.Handler, "the caller's handler is not overwritten")
		assert.Empty(t, decls.Consumers(), "nothing is registered when the guard fires")
	})
}

// TestNewTypedHandlerWithMetaKeepsMetadataPairedUnderConcurrency pins that
// Metadata stays paired with its own delivery under concurrent Handle calls on
// one shared typedHandler instance. Reference is capped at 5 chars
// (validate:"max=5"), so the per-goroutine marker must stay short or every
// delivery fails validation before fn runs and the pairing assertion never
// executes.
func TestNewTypedHandlerWithMetaKeepsMetadataPairedUnderConcurrency(t *testing.T) {
	const goroutines = 24

	var mismatches atomic.Int64
	var calls atomic.Int64
	h := NewTypedHandlerWithMeta(orderEventType, func(_ context.Context, p orderPayload, meta Metadata) error {
		calls.Add(1)
		if meta.Headers()["x-check"] != p.Reference {
			t.Errorf("metadata mismatch: got header %v for payload %+v", meta.Headers()["x-check"], p)
			mismatches.Add(1)
		}
		return nil
	})

	ctx := t.Context()
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			marker := fmt.Sprintf("r%02d", i)
			body, err := json.Marshal(orderPayload{Reference: marker, Amount: 1})
			if err != nil {
				t.Errorf("marshal payload: %v", err)
				return
			}
			delivery := &amqp.Delivery{
				Body:    body,
				Headers: amqp.Table{"x-check": marker},
			}
			if err := h.Handle(ctx, delivery); err != nil {
				t.Errorf("delivery %s failed: %v", marker, err)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(0), mismatches.Load(), "Metadata must stay paired with its own delivery")
	assert.Equal(t, int64(goroutines), calls.Load(), "fn must run for every delivery, or the pairing assertion passes vacuously")
}

// TestNewTypedHandlerWithMetaNilHeaders pins that a delivery with nil Headers
// survives Handle as a nil amqp.Table via Metadata.Headers() — the shape
// outbox.EventIDFromHeaders must tolerate.
func TestNewTypedHandlerWithMetaNilHeaders(t *testing.T) {
	var calls int
	var gotMeta Metadata
	h := NewTypedHandlerWithMeta(orderEventType, func(_ context.Context, _ orderPayload, meta Metadata) error {
		calls++
		gotMeta = meta
		return nil
	})

	require.NoError(t, h.Handle(t.Context(), &amqp.Delivery{Body: validBody(t)}))
	assert.Equal(t, 1, calls)
	assert.Nil(t, gotMeta.Headers())
}

func TestJSONCodecSummarize(t *testing.T) {
	typeErr := &json.UnmarshalTypeError{
		Value:  "number " + numericMarker,
		Type:   reflect.TypeOf(int64(0)),
		Offset: 18,
		Struct: "orderPayload",
		Field:  "amount",
	}
	syntaxOffset := int64(1)

	tests := []struct {
		name        string
		err         error
		want        string
		notContains string
	}{
		{
			name:        "unmarshal_type_error",
			err:         typeErr,
			want:        `json: type mismatch at field "amount" (want int64, offset 18)`,
			notContains: numericMarker,
		},
		{
			name:        "wrapped_unmarshal_type_error",
			err:         fmt.Errorf("decode: %w", typeErr),
			want:        `json: type mismatch at field "amount" (want int64, offset 18)`,
			notContains: numericMarker,
		},
		{
			name: "unmarshal_type_error_without_field",
			err:  fieldlessTypeError(t),
			want: "json: type mismatch (want messaging.orderPayload, offset 3)",
		},
		{
			name: "unmarshal_type_error_without_type",
			err:  &json.UnmarshalTypeError{Value: "object", Offset: 2},
			want: "json: type mismatch (want unknown, offset 2)",
		},
		{
			name: "syntax_error",
			err:  syntaxError(t, syntaxOffset),
			want: "json: syntax error at offset 1",
		},
		{
			name: "wrapped_syntax_error",
			err:  fmt.Errorf("decode: %w", syntaxError(t, syntaxOffset)),
			want: "json: syntax error at offset 1",
		},
		{
			name:        "unrelated_error_falls_back",
			err:         errors.New("json: unknown field " + payloadMarker),
			want:        "",
			notContains: payloadMarker,
		},
		{
			name: "nil_error_falls_back",
			err:  nil,
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := jsonCodec{}.summarize(tc.err)
			assert.Equal(t, tc.want, got)
			if tc.notContains != "" {
				assert.NotContains(t, got, tc.notContains)
			}
		})
	}
}

// fieldlessTypeError produces a real *json.UnmarshalTypeError with an empty
// Field: a bare scalar body never enters a struct field, so encoding/json has no
// FieldStack to draw one from. Built from the decoder rather than by literal so
// the Field == "" branch proves its own reachability.
func fieldlessTypeError(t *testing.T) *json.UnmarshalTypeError {
	t.Helper()

	var payload orderPayload
	err := json.Unmarshal([]byte("123"), &payload)

	var typeErr *json.UnmarshalTypeError
	require.ErrorAs(t, err, &typeErr)
	require.Empty(t, typeErr.Field)

	return typeErr
}

// syntaxError produces a real *json.SyntaxError; its msg field is unexported,
// so it cannot be built by literal.
func syntaxError(t *testing.T, wantOffset int64) *json.SyntaxError {
	t.Helper()

	var payload orderPayload
	err := json.Unmarshal([]byte(`}`), &payload)

	var syntaxErr *json.SyntaxError
	require.ErrorAs(t, err, &syntaxErr)
	require.Equal(t, wantOffset, syntaxErr.Offset)

	return syntaxErr
}
