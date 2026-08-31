package streams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/messaging/internal/delivery"
	"github.com/gaborage/go-bricks/messaging/internal/tenantstamp"
)

const (
	// streamPayloadMarker stands in for partner PII/PCI: it must never surface in
	// an error string, so tests plant it as a payload VALUE and assert its absence.
	streamPayloadMarker = "MARKER-do-not-leak-9e3f"

	// streamPan is a card-shaped value for the case where the leak vector is the
	// map key itself rather than a field value.
	streamPan = "4111111111111111"
)

type streamOrder struct {
	Reference string `json:"reference" validate:"max=5"`
	Amount    int64  `json:"amount"    validate:"required"`
}

// streamMapOrder exercises the one shape whose validator namespace embeds
// payload content: a dived map, whose keys are interpolated verbatim.
type streamMapOrder struct {
	Limits map[string]int `json:"limits" validate:"dive,max=5"`
}

// jsonMessage is a stream delivery carrying body as its data section.
func jsonMessage(body string) *amqp.Message {
	msg := amqpMessage(body)
	return msg
}

// stampedJSONMessage is jsonMessage with a tenant stamp, the shape a holding
// consumer's delivery arrives in.
func stampedJSONMessage(tenant, body string) *amqp.Message {
	msg := jsonMessage(body)
	msg.ApplicationProperties[tenantstamp.Header] = tenant
	return msg
}

func validOrderBody(t *testing.T) string {
	t.Helper()

	body, err := json.Marshal(streamOrder{Reference: "abc", Amount: 7})
	require.NoError(t, err)

	return string(body)
}

// typedRunner builds a runner wired the way the manager wires one for a typed
// declaration: the typed handler AND the screen that belongs to it.
func typedRunner[T any](t *testing.T, fn func(context.Context, T, *Message) error) *consumerRunner {
	t.Helper()

	typed := newTypedConsumer(testConsumerName, fn)
	runner := newTestRunner(t, typed.handle, newOffsetTracker(1, time.Hour, newFakeClock().Now))
	runner.screen = typed.screen

	return runner
}

func TestTypedConsumerDecodesAndInvokesFn(t *testing.T) {
	var got streamOrder
	var gotMsg *Message
	typed := newTypedConsumer(testConsumerName, func(_ context.Context, p streamOrder, msg *Message) error {
		got = p
		gotMsg = msg
		return nil
	})

	msg := &Message{Data: []byte(validOrderBody(t)), Offset: 41, Stream: testStream}
	require.NoError(t, typed.handle(t.Context(), msg))

	assert.Equal(t, streamOrder{Reference: "abc", Amount: 7}, got)
	assert.Same(t, msg, gotMsg, "the delivery reaches fn unchanged")
}

// fn's error is returned unwrapped, so a consumer's errors.Is against its
// business sentinels — and its own streams.Permanent — still works.
func TestTypedConsumerReturnsFnResultUnwrapped(t *testing.T) {
	typed := newTypedConsumer(testConsumerName, func(context.Context, streamOrder, *Message) error {
		return errHandlerFailed
	})

	err := typed.handle(t.Context(), &Message{Data: []byte(validOrderBody(t))})

	require.ErrorIs(t, err, errHandlerFailed)
	assert.False(t, isPayloadFailure(err), "a business failure is not poison")
	assert.False(t, delivery.IsPermanent(err), "the lane does not mark fn's own error permanent")
}

func TestTypedConsumerDecodeFailureIsPermanentPoison(t *testing.T) {
	var calls int
	typed := newTypedConsumer(testConsumerName, func(context.Context, streamOrder, *Message) error {
		calls++
		return nil
	})

	err := typed.handle(t.Context(), &Message{Data: fmt.Appendf(nil, `{"amount":%q}`, streamPayloadMarker)})

	require.ErrorIs(t, err, ErrPayloadUndecodable)
	assert.True(t, delivery.IsPermanent(err), "a decode that cannot come out differently is never retried")
	assert.True(t, isPayloadFailure(err))
	assert.Zero(t, calls, "fn never sees a body that did not decode")
	assert.NotContains(t, err.Error(), streamPayloadMarker)
}

func TestTypedConsumerValidationFailureIsPermanentPoison(t *testing.T) {
	var calls int
	typed := newTypedConsumer(testConsumerName, func(context.Context, streamOrder, *Message) error {
		calls++
		return nil
	})

	err := typed.handle(t.Context(), &Message{Data: []byte(`{"reference":"waytoolong","amount":0}`)})

	require.ErrorIs(t, err, ErrPayloadInvalid)
	assert.True(t, delivery.IsPermanent(err))
	assert.Zero(t, calls, "fn never sees a payload that failed validation")

	var payloadErr *PayloadError
	require.ErrorAs(t, err, &payloadErr)
	assert.Equal(t, []string{"streamOrder.Reference", "streamOrder.Amount"}, payloadErr.Fields())
}

// The decode and validate branches both leave the partner's bytes out of every
// rendering the framework produces. Each case drives the marker through a REAL
// producer — encoding/json or the framework validator — so it proves the
// rendering rather than a hand-built literal.
func TestTypedConsumerRendersNoPayloadBytes(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		marker  string
		premise string
		handle  func(*testing.T, string) error
	}{
		{
			name:    "decode_type_mismatch_echoes_the_literal",
			body:    `{"amount":"` + streamPayloadMarker + `"}`,
			marker:  streamPayloadMarker,
			premise: "type mismatch",
			handle:  handleAs[streamOrder],
		},
		{
			name:    "decode_syntax_error_quotes_the_byte",
			body:    `{"amount":~}`,
			marker:  "~",
			premise: "syntax error",
			handle:  handleAs[streamOrder],
		},
		{
			name:    "decode_map_key_reaches_the_field_path",
			body:    fmt.Sprintf(`{"limits":{%q:"notanint"}}`, streamPan),
			marker:  streamPan,
			premise: "type mismatch (want",
			handle:  handleAs[streamMapOrder],
		},
		{
			name:    "validate_map_key_reaches_the_namespace",
			body:    fmt.Sprintf(`{"limits":{%q:99}}`, streamPan),
			marker:  streamPan,
			premise: "(fields: ",
			handle:  handleAs[streamMapOrder],
		},
		{
			name:    "validate_oversized_string_echoes_the_value",
			body:    `{"reference":"` + streamPayloadMarker + `","amount":1}`,
			marker:  streamPayloadMarker,
			premise: "(fields: ",
			handle:  handleAs[streamOrder],
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.handle(t, tc.body)

			require.Error(t, err)
			// A positive premise first, so NotContains cannot pass by the error
			// having taken some other branch entirely.
			require.Contains(t, err.Error(), tc.premise)
			assert.NotContains(t, err.Error(), tc.marker)

			// The raw cause is the deliberate escape hatch, so it MAY carry the
			// marker — pinning that is what makes the absence above meaningful.
			var payloadErr *PayloadError
			require.ErrorAs(t, err, &payloadErr)
			require.Error(t, payloadErr.Unwrap())
		})
	}
}

// handleAs runs one body through a typed consumer of T and returns what the lane
// would have settled on.
func handleAs[T any](t *testing.T, body string) error {
	t.Helper()

	typed := newTypedConsumer(testConsumerName, func(context.Context, T, *Message) error { return nil })

	return typed.handle(t.Context(), &Message{Data: []byte(body)})
}

// The screen must agree with the handler exactly: whatever it rejects is what
// the handler would have rejected, and it never runs fn.
func TestTypedConsumerScreenAgreesWithTheHandler(t *testing.T) {
	var calls int
	typed := newTypedConsumer(testConsumerName, func(context.Context, streamOrder, *Message) error {
		calls++
		return errHandlerFailed
	})

	poison := &Message{Data: []byte(`{"amount":"nope"}`)}
	screened := typed.screen(poison)
	require.ErrorIs(t, screened, ErrPayloadUndecodable)
	assert.True(t, delivery.IsPermanent(screened))
	assert.Zero(t, calls, "the screen never runs fn")

	// A body the handler accepts is one the screen passes, even when fn then
	// fails: the screen judges the payload, not the outcome.
	good := &Message{Data: []byte(validOrderBody(t))}
	assert.NoError(t, typed.screen(good))
	assert.ErrorIs(t, typed.handle(t.Context(), good), errHandlerFailed)
	assert.Equal(t, 1, calls)
}

func TestDeclareTypedConsumerBuildsTheHandlerAndTheScreen(t *testing.T) {
	decls := NewDeclarations()
	opts := &ConsumerOptions{Stream: testStream, Name: testConsumerName}

	DeclareTypedConsumer(decls, opts, func(context.Context, streamOrder) error { return nil })

	require.Len(t, decls.consumers, 1)
	decl := decls.consumers[0]
	assert.NotNil(t, decl.Handler, "the declaration carries the typed handler")
	assert.NotNil(t, decl.Screen, "and the poison screen that belongs to it")
	assert.False(t, decl.Super)
	assert.NotNil(t, opts.Handler, "the caller's options are filled in, as DeclareConsumer's are")
}

func TestDeclareTypedSuperStreamConsumerIsAlwaysSAC(t *testing.T) {
	decls := NewDeclarations()

	DeclareTypedSuperStreamConsumer(decls, &SuperStreamConsumerOptions{
		SuperStream: testSuperStream,
		Name:        testConsumerName,
	}, func(context.Context, streamOrder) error { return nil })

	require.Len(t, decls.consumers, 1)
	decl := decls.consumers[0]
	assert.True(t, decl.Super)
	assert.True(t, decl.SAC)
	assert.Equal(t, testSuperStream, decl.Stream)
	assert.NotNil(t, decl.Screen)
}

// A hand-written DeclareConsumer carries no screen, which is what tells the
// gated path there is nothing to ask.
func TestDeclareConsumerCarriesNoScreen(t *testing.T) {
	decls := NewDeclarations()

	decls.DeclareConsumer(&ConsumerOptions{
		Stream:  testStream,
		Name:    testConsumerName,
		Handler: func(context.Context, *Message) error { return nil },
	})
	decls.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
		SuperStream: testSuperStream,
		Name:        testConsumerName,
		Handler:     func(context.Context, *Message) error { return nil },
	})

	require.Len(t, decls.consumers, 2)
	for _, decl := range decls.consumers {
		assert.Nil(t, decl.Screen, "consumer %q", decl.Stream)
	}
}

// fn reaches the delivery on the WithMeta shape, which is the only way a typed
// stream consumer can read its offset and partition.
func TestDeclareTypedConsumerWithMetaPassesTheDelivery(t *testing.T) {
	decls := NewDeclarations()
	opts := &ConsumerOptions{Stream: testStream, Name: testConsumerName}

	var gotOffset int64
	var gotStream string
	DeclareTypedConsumerWithMeta(decls, opts, func(_ context.Context, _ streamOrder, msg *Message) error {
		gotOffset, gotStream = msg.Offset, msg.Stream
		return nil
	})

	require.NoError(t, opts.Handler(t.Context(), &Message{
		Data:   []byte(validOrderBody(t)),
		Offset: 41,
		Stream: testPartition1,
	}))
	assert.Equal(t, int64(41), gotOffset)
	assert.Equal(t, testPartition1, gotStream)
}

func TestDeclareTypedSuperStreamConsumerWithMetaPassesTheDelivery(t *testing.T) {
	decls := NewDeclarations()
	opts := &SuperStreamConsumerOptions{SuperStream: testSuperStream, Name: testConsumerName}

	var gotStream string
	DeclareTypedSuperStreamConsumerWithMeta(decls, opts, func(_ context.Context, _ streamOrder, msg *Message) error {
		gotStream = msg.Stream
		return nil
	})

	require.NoError(t, opts.Handler(t.Context(), &Message{Data: []byte(validOrderBody(t)), Stream: testPartition2}))
	assert.Equal(t, testPartition2, gotStream, "the partition, not the super stream")
}

// Every typed declare helper refuses the same three wiring mistakes, and names
// itself in the panic so a stack points at the entry point actually used.
func TestDeclareTypedConsumerPanicsOnWiringMistakes(t *testing.T) {
	fn := func(context.Context, streamOrder) error { return nil }
	fnMeta := func(context.Context, streamOrder, *Message) error { return nil }
	taken := func(context.Context, *Message) error { return nil }

	tests := []struct {
		name  string
		want  string
		panic func()
	}{
		{
			name:  "nil_declarations",
			want:  "streams: DeclareTypedConsumerWithMeta requires a non-nil *Declarations",
			panic: func() { DeclareTypedConsumer(nil, &ConsumerOptions{}, fn) },
		},
		{
			name:  "nil_options",
			want:  "DeclareTypedConsumerWithMeta requires a non-nil *ConsumerOptions",
			panic: func() { DeclareTypedConsumer(NewDeclarations(), nil, fn) },
		},
		{
			name: "handler_already_set",
			want: "DeclareTypedConsumerWithMeta builds the handler itself",
			panic: func() {
				DeclareTypedConsumer(NewDeclarations(), &ConsumerOptions{Handler: taken}, fn)
			},
		},
		{
			name: "nil_handler_function",
			want: "streams: DeclareTypedConsumerWithMeta requires a non-nil handler function",
			panic: func() {
				DeclareTypedConsumerWithMeta[streamOrder](NewDeclarations(), &ConsumerOptions{}, nil)
			},
		},
		{
			name:  "super_nil_declarations",
			want:  "streams: DeclareTypedSuperStreamConsumerWithMeta requires a non-nil *Declarations",
			panic: func() { DeclareTypedSuperStreamConsumer(nil, &SuperStreamConsumerOptions{}, fn) },
		},
		{
			name:  "super_nil_options",
			want:  "DeclareTypedSuperStreamConsumerWithMeta requires a non-nil *SuperStreamConsumerOptions",
			panic: func() { DeclareTypedSuperStreamConsumer(NewDeclarations(), nil, fn) },
		},
		{
			name: "super_handler_already_set",
			want: "DeclareTypedSuperStreamConsumerWithMeta builds the handler itself",
			panic: func() {
				DeclareTypedSuperStreamConsumer(NewDeclarations(), &SuperStreamConsumerOptions{Handler: taken}, fn)
			},
		},
		{
			name: "super_nil_handler_function",
			want: "streams: DeclareTypedSuperStreamConsumerWithMeta requires a non-nil handler function",
			panic: func() {
				DeclareTypedSuperStreamConsumerWithMeta[streamOrder](NewDeclarations(), &SuperStreamConsumerOptions{}, nil)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Contains(t, panicValue(t, tc.panic), tc.want)
		})
	}

	// The WithMeta entry points name themselves too, rather than the helper they
	// share with the plain shape.
	assert.Contains(t,
		panicValue(t, func() { DeclareTypedConsumerWithMeta(nil, &ConsumerOptions{}, fnMeta) }),
		"streams: DeclareTypedConsumerWithMeta requires a non-nil *Declarations")
}

// panicValue runs fn and returns the string it panicked with, failing the test
// if it did not panic — the panics under test are multi-line, so they are matched
// by substring rather than by exact value.
func panicValue(t *testing.T, fn func()) (value string) {
	t.Helper()

	defer func() {
		r := recover()
		require.NotNil(t, r, "expected a panic")
		got, ok := r.(string)
		require.True(t, ok, "expected a string panic, got %T", r)
		value = got
	}()

	fn()

	return ""
}

// One typed consumer serves every partition of a super stream concurrently, so
// its per-delivery state must be the delivery's and not the adapter's.
func TestTypedConsumerAllocatesAFreshPayloadPerDelivery(t *testing.T) {
	runner := typedRunner(t, func(_ context.Context, p streamOrder, msg *Message) error {
		if p.Amount != msg.Offset {
			return fmt.Errorf("payload %d does not belong to offset %d", p.Amount, msg.Offset)
		}
		return nil
	})

	for offset := int64(1); offset <= 5; offset++ {
		msg := jsonMessage(fmt.Sprintf(`{"reference":"abc","amount":%d}`, offset))
		runner.deliver(testStream, offset, msg, &fakeStorer{})
	}
}

// A body that decodes but leaves T's zero value must not be mistaken for a
// decode failure: it is validation's to reject, under the validate sentinel.
func TestTypedConsumerEmptyBodyFailsAtDecode(t *testing.T) {
	typed := newTypedConsumer(testConsumerName, func(context.Context, streamOrder, *Message) error { return nil })

	err := typed.handle(t.Context(), &Message{Data: nil})

	require.ErrorIs(t, err, ErrPayloadUndecodable)
	assert.NotErrorIs(t, err, ErrPayloadInvalid)
}

// A non-struct T reaches validation with a *validator.InvalidValidationError,
// which still carries the validate stage — failing closed on the first delivery
// rather than silently skipping validation forever.
func TestTypedConsumerNonStructPayloadFailsClosed(t *testing.T) {
	var calls int
	typed := newTypedConsumer(testConsumerName, func(context.Context, int, *Message) error {
		calls++
		return nil
	})

	err := typed.handle(t.Context(), &Message{Data: []byte(`7`)})

	require.ErrorIs(t, err, ErrPayloadInvalid)
	assert.Zero(t, calls)
	assert.True(t, errors.Is(err, ErrPayloadInvalid) && delivery.IsPermanent(err))
}
