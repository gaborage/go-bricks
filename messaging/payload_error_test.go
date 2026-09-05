package messaging

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/saferender"
	"github.com/gaborage/go-bricks/internal/validation"
	"github.com/gaborage/go-bricks/messaging/internal/payloaderr"
	"github.com/gaborage/go-bricks/messaging/internal/sealruntime"
)

const (
	orderEventType = "OrderCreated"
	// shipmentEventType is a second event type so the constructors are pinned to
	// carry their argument through instead of hardcoding one.
	shipmentEventType = "ShipmentDispatched"
	fieldAmount       = "orderPayload.Amount"
	fieldReference    = "orderPayload.Reference"

	// payloadMarker stands in for partner PII/PCI: it must never surface in an
	// error string, so tests plant it as a payload VALUE and assert its absence.
	payloadMarker = "MARKER-do-not-leak-9e3f"

	// numericMarker is the same idea for a numeric literal — a transaction
	// amount. json.UnmarshalTypeError echoes it as `Value: "number 1234.56"`,
	// so the digits must not reach Error().
	numericMarker = "1234.56"

	// pan is a card-shaped value for the cases where the leak vector is the map
	// key itself rather than a field value.
	pan = "4111111111111111"

	// syntaxMarker is a single byte json.SyntaxError would quote back
	// ("invalid character '~' ..."); it appears nowhere else in a rendered
	// message, so asserting its absence is unambiguous.
	syntaxMarker = "~"
)

type orderPayload struct {
	Reference string `json:"reference" validate:"max=5"`
	Amount    int64  `json:"amount"    validate:"required"`
}

// mapKeyPayload exercises the one shape whose validator namespace embeds
// payload content: a dived map, whose keys are interpolated verbatim.
type mapKeyPayload struct {
	Limits map[string]int `json:"limits" validate:"dive,max=5"`
}

// intKeyPayload exercises the decode-side twin: encoding/json parses an
// integer-keyed map's key itself, and folds the rejected key into
// UnmarshalTypeError.Value.
type intKeyPayload struct {
	Limits map[int]int `json:"limits"`
}

func TestPayloadErrorIsMatchesStageSentinel(t *testing.T) {
	tests := []struct {
		name     string
		stage    PayloadStage
		match    error
		notMatch error
	}{
		{name: "decode_stage", stage: PayloadStageDecode, match: ErrPayloadUndecodable, notMatch: ErrPayloadInvalid},
		{name: "validate_stage", stage: PayloadStageValidate, match: ErrPayloadInvalid, notMatch: ErrPayloadUndecodable},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := &PayloadError{EventType: orderEventType, Stage: tc.stage}

			require.ErrorIs(t, err, tc.match)
			require.NotErrorIs(t, err, tc.notMatch)
			assert.NotErrorIs(t, err, ErrNotConnected)
		})
	}
}

// An unrecognized Stage must match neither sentinel rather than defaulting to
// one: a mistyped stage would otherwise misroute a dead-letter decision.
func TestPayloadErrorIsRejectsUnknownStage(t *testing.T) {
	for _, stage := range []PayloadStage{"", "decoding", "DECODE"} {
		err := &PayloadError{EventType: orderEventType, Stage: stage}

		require.NotErrorIs(t, err, ErrPayloadUndecodable, "stage %q", stage)
		assert.NotErrorIs(t, err, ErrPayloadInvalid, "stage %q", stage)
	}
}

func TestPayloadErrorUnwrapReachesCause(t *testing.T) {
	inner := errors.New("inner cause")
	err := newPayloadError(orderEventType, payloaderr.NewDecode(inner, ""))

	require.Same(t, inner, err.Unwrap())
	require.ErrorIs(t, err, inner)
	// The sentinel mapping must survive alongside the unwrap chain.
	assert.ErrorIs(t, err, ErrPayloadUndecodable)
}

func TestPayloadErrorAsRecoversFromWrappedChain(t *testing.T) {
	// Built through the constructor, from a real validator failure: the wrap chain
	// has to survive the shape production actually produces.
	validateErr := validation.New().Struct(orderPayload{Reference: "abc"})
	require.Error(t, validateErr)
	err := newPayloadError(orderEventType, payloaderr.NewValidate(validateErr))
	wrapped := fmt.Errorf("consumer %q: %w", testConsumer, err)

	var target *PayloadError
	require.ErrorAs(t, wrapped, &target)
	assert.Equal(t, orderEventType, target.EventType)
	assert.Equal(t, PayloadStageValidate, target.Stage)
	assert.Equal(t, []string{fieldAmount}, target.Fields())
}

func TestPayloadErrorMessageComposition(t *testing.T) {
	cause := validation.New().Struct(orderPayload{Reference: payloadMarker})
	require.Error(t, cause)
	validateErr := newPayloadError(orderEventType, payloaderr.NewValidate(cause))

	// The validator's own text is absent by design — the sanitized field list
	// already carries everything that is safe to render.
	assert.Equal(t,
		`messaging: validate failed for event "OrderCreated" (fields: orderPayload.Reference, orderPayload.Amount)`,
		validateErr.Error())

	decodeErr := newPayloadError(orderEventType, payloaderr.NewDecode(errors.New("unexpected EOF"), ""))
	assert.Equal(t,
		`messaging: decode failed for event "OrderCreated": cause withheld (unaudited decoder); use errors.Unwrap for the raw error`,
		decodeErr.Error())
	assert.Empty(t, decodeErr.Fields(), "decode failures carry no field list")
}

// renderDecodeError renders a decode failure exactly as the typed handler does:
// the field-path gate comes from the destination type the body was decoded into,
// so a test cannot pass by choosing a friendlier gate than production would.
func renderDecodeError(cause error, dest any) string {
	summary := payloaderr.JSONCodec{}.Summarize(cause, saferender.FieldPathIsSchema(reflect.TypeOf(dest)))

	return newPayloadError(orderEventType, payloaderr.NewDecode(cause, summary)).Error()
}

// Each subtest drives the marker through a REAL producer — encoding/json or the
// framework validator — so it proves the rendering, not a hand-built literal.
// Each also asserts a positive premise, so NotContains cannot pass by the error
// being empty or the failure never happening.
func TestPayloadErrorNeverRendersPayloadValues(t *testing.T) {
	t.Run("decode_number_into_int", func(t *testing.T) {
		// The old fixture used a JSON string into an int, the one shape whose
		// Value is the payload-free literal "string". A numeric literal is the
		// leaking shape: Value becomes "number 1234.56".
		body := fmt.Appendf(nil, `{"amount": %s}`, numericMarker)

		var payload orderPayload
		decodeErr := json.Unmarshal(body, &payload)
		require.Error(t, decodeErr)
		require.Contains(t, decodeErr.Error(), numericMarker, "premise: the raw cause leaks the amount")

		rendered := renderDecodeError(decodeErr, payload)
		assert.Contains(t, rendered, `type mismatch at field "amount"`,
			"a map-free payload type keeps the schema field path")
		assert.NotContains(t, rendered, numericMarker)
	})

	t.Run("decode_hostile_map_key_stays_out_of_field", func(t *testing.T) {
		// UnmarshalTypeError.Field is schema-only for a map-free destination and
		// input-derived for a map one: through Go 1.26 it read "limits", from
		// Go 1.27 it reads "limits.<input key>". The summary must therefore drop
		// the field path for this type rather than judge the string — asserting
		// only the marker's absence would pass on 1.26 no matter what the gate
		// did, so pin that no field path was rendered at all.
		body := fmt.Appendf(nil, `{"limits":{%q:"notanint"}}`, payloadMarker)

		var payload mapKeyPayload
		decodeErr := json.Unmarshal(body, &payload)
		require.Error(t, decodeErr)
		require.ErrorAs(t, decodeErr, new(*json.UnmarshalTypeError), "premise: the body produced an UnmarshalTypeError")

		rendered := renderDecodeError(decodeErr, payload)
		require.Contains(t, rendered, "type mismatch (want", "premise: a decode summary was rendered")
		assert.NotContains(t, rendered, "at field", "a map-bearing payload type renders no field path")
		assert.NotContains(t, rendered, payloadMarker)
	})

	t.Run("decode_integer_keyed_map_key", func(t *testing.T) {
		// The second way UnmarshalTypeError.Value turns payload-bearing: for an
		// integer-keyed map encoding/json parses the KEY, so a rejected key lands
		// in Value — and from Go 1.27 in Field too.
		body := fmt.Appendf(nil, `{"limits":{%q:1}}`, payloadMarker)

		var payload intKeyPayload
		decodeErr := json.Unmarshal(body, &payload)
		require.Error(t, decodeErr)
		require.Contains(t, decodeErr.Error(), payloadMarker, "premise: the raw cause leaks the map key")

		require.ErrorAs(t, decodeErr, new(*json.UnmarshalTypeError), "premise: the body produced an UnmarshalTypeError")

		rendered := renderDecodeError(decodeErr, payload)
		require.Contains(t, rendered, "type mismatch (want", "premise: a decode summary was rendered")
		assert.NotContains(t, rendered, "at field", "a map-bearing payload type renders no field path")
		assert.NotContains(t, rendered, payloadMarker)
	})

	t.Run("decode_syntax_error", func(t *testing.T) {
		body := fmt.Appendf(nil, `%s{"amount":1}`, syntaxMarker)

		var payload orderPayload
		decodeErr := json.Unmarshal(body, &payload)
		require.Error(t, decodeErr)
		require.ErrorAs(t, decodeErr, new(*json.SyntaxError), "premise: the body produced a SyntaxError")
		require.Contains(t, decodeErr.Error(), syntaxMarker, "premise: the raw cause quotes the offending byte")

		rendered := renderDecodeError(decodeErr, payload)
		require.Contains(t, rendered, "syntax error at offset", "premise: a decode summary was rendered")
		assert.NotContains(t, rendered, syntaxMarker)
	})

	t.Run("decode_unknown_field", func(t *testing.T) {
		// Layer 3 may enable strict decoding; the key is partner-controlled.
		body := fmt.Appendf(nil, `{%q:1}`, payloadMarker)
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()

		var payload orderPayload
		decodeErr := dec.Decode(&payload)
		require.Error(t, decodeErr)
		require.Contains(t, decodeErr.Error(), payloadMarker, "premise: the raw cause leaks the key")

		rendered := renderDecodeError(decodeErr, payload)
		require.Contains(t, rendered, "cause withheld", "premise: the generic fallback rendered")
		assert.NotContains(t, rendered, payloadMarker)
	})

	t.Run("validate_map_key_namespace", func(t *testing.T) {
		validateErr := validation.New().Struct(mapKeyPayload{
			Limits: map[string]int{payloadMarker: 99},
		})
		require.Error(t, validateErr)
		require.Contains(t, validateErr.Error(), payloadMarker, "premise: the raw cause leaks the map key")

		// Redacting the string is the only depth available: all four name
		// accessors derive from the same name validator assembled with the key
		// already embedded, so none of them is a clean alternative to sanitizing.
		var verrs validator.ValidationErrors
		require.ErrorAs(t, validateErr, &verrs)
		require.Len(t, verrs, 1)
		for name, got := range map[string]string{
			"Namespace":       verrs[0].Namespace(),
			"StructNamespace": verrs[0].StructNamespace(),
			"Field":           verrs[0].Field(),
			"StructField":     verrs[0].StructField(),
		} {
			assert.Contains(t, got, payloadMarker, "premise: %s interpolates the map key", name)
		}

		err := newPayloadError(orderEventType, payloaderr.NewValidate(validateErr))
		require.NotEmpty(t, err.Fields(), "premise: fields were derived")
		require.Contains(t, err.Error(), "(fields: ", "premise: the field list was rendered")

		assert.NotContains(t, err.Error(), payloadMarker)
		for _, field := range err.Fields() {
			assert.NotContains(t, field, payloadMarker)
		}
		assert.Equal(t, []string{"mapKeyPayload.Limits[*]"}, err.Fields())
	})

	t.Run("validate_map_key_closing_bracket_cannot_escape_redaction", func(t *testing.T) {
		// Map keys are partner-controlled, so a key may carry the very character
		// the redaction scans for. A key that opens with ']' ends a depth-counting
		// scan at its own bracket and lets the rest through verbatim; the digits
		// after it reached both Fields() and Error() before the span-based rule.
		hostileKey := "]" + pan
		validateErr := validation.New().Struct(mapKeyPayload{
			Limits: map[string]int{hostileKey: 99},
		})
		require.Error(t, validateErr)
		require.Contains(t, validateErr.Error(), pan, "premise: the raw cause leaks the map key")

		err := newPayloadError(orderEventType, payloaderr.NewValidate(validateErr))
		require.NotEmpty(t, err.Fields(), "premise: fields were derived")

		assert.Equal(t, []string{"mapKeyPayload.Limits[*]"}, err.Fields())
		assert.NotContains(t, err.Error(), pan)
	})

	t.Run("validate_oversized_string", func(t *testing.T) {
		validateErr := validation.New().Struct(orderPayload{Reference: payloadMarker, Amount: 1})
		require.Error(t, validateErr)
		require.Contains(t, validateErr.Error(), "failed on the 'max' tag", "premise: the max rule fired")

		err := newPayloadError(orderEventType, payloaderr.NewValidate(validateErr))
		require.Contains(t, err.Error(), fieldReference, "premise: the field list was rendered")
		assert.NotContains(t, err.Error(), payloadMarker)
	})
}

func TestPayloadErrorValidateConstructorDerivesFields(t *testing.T) {
	validateErr := validation.New().Struct(orderPayload{Reference: payloadMarker})
	require.Error(t, validateErr)

	err := newPayloadError(orderEventType, payloaderr.NewValidate(validateErr))

	assert.Equal(t, PayloadStageValidate, err.Stage)
	assert.Equal(t, []string{fieldReference, fieldAmount}, err.Fields())
	require.ErrorIs(t, err, ErrPayloadInvalid)
	require.ErrorAs(t, err, new(validator.ValidationErrors))

	// Redaction is Fields()' job, not the constructor's — pin that the stored
	// namespaces are still the validator's own, so the single read-path design
	// cannot drift into a second sanitization point unnoticed.
	mapErr := validation.New().Struct(mapKeyPayload{Limits: map[string]int{pan: 99}})
	require.Error(t, mapErr)
	stored := newPayloadError(orderEventType, payloaderr.NewValidate(mapErr))
	require.Len(t, stored.Fields(), 1)
	require.Contains(t, mapErr.Error(), pan, "premise: the raw cause carries the key")
	assert.NotContains(t, stored.Fields()[0], pan, "the accessor is what redacts")

	// A non-validator cause yields no fields rather than a bogus one.
	plain := newPayloadError(orderEventType, payloaderr.NewValidate(errors.New("boom")))
	assert.Empty(t, plain.Fields())
	assert.Equal(t, `messaging: validate failed for event "OrderCreated"`, plain.Error())
}

// The constructor stores namespaces raw (pinned in payloaderr), so redaction has
// to hold on this lane's read path too — through Fields() and through the
// rendered message.
func TestPayloadErrorFieldsRedactsOnRead(t *testing.T) {
	cause := validation.New().Struct(mapKeyPayload{Limits: map[string]int{payloadMarker: 99}})
	require.Error(t, cause)
	err := newPayloadError(orderEventType, payloaderr.NewValidate(cause))

	assert.Equal(t, []string{"mapKeyPayload.Limits[*]"}, err.Fields())
	assert.Equal(t,
		`messaging: validate failed for event "OrderCreated" (fields: mapKeyPayload.Limits[*])`,
		err.Error())

	// A fresh slice per call: a caller cannot spoil the next read through the one
	// it was handed.
	got := err.Fields()
	got[0] = "tampered"
	assert.Equal(t, []string{"mapKeyPayload.Limits[*]"}, err.Fields())
}

func TestPayloadErrorConstructorsPropagateEventType(t *testing.T) {
	cause := errors.New("boom")

	decodeErr := newPayloadError(shipmentEventType, payloaderr.NewDecode(cause, ""))
	assert.Equal(t, shipmentEventType, decodeErr.EventType)
	assert.Contains(t, decodeErr.Error(), shipmentEventType)

	validateErr := newPayloadError(shipmentEventType, payloaderr.NewValidate(cause))
	assert.Equal(t, shipmentEventType, validateErr.EventType)
	assert.Contains(t, validateErr.Error(), shipmentEventType)
}

func TestSanitizeNamespace(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "no_brackets", input: "Req.Amount", want: "Req.Amount"},
		{name: "slice_index", input: "Req.Items[0]", want: "Req.Items[*]"},
		{name: "map_key", input: "Req.Limits[PAN-4111111111111111]", want: "Req.Limits[*]"},
		{name: "all_digit_map_key", input: "Req.Limits[4111111111111111]", want: "Req.Limits[*]"},
		{name: "nested_brackets", input: "Req.Grid[a[b]].Name", want: "Req.Grid[*].Name"},
		{name: "empty_brackets", input: "Req.Items[]", want: "Req.Items[*]"},
		// Several bracketed segments collapse to the first: a key may contain
		// '.' or ']', so the boundary between the last key and the trailing
		// field path is not recoverable from the flat string.
		{name: "multiple_segments", input: "Req.Items[0].Tags[secret]", want: "Req.Items[*]"},
		{name: "unterminated_bracket", input: "Req.Items[secret", want: "Req.Items[*]"},
		{name: "already_sanitized", input: "Req.Items[*]", want: "Req.Items[*]"},
		// Hostile keys. A depth counter closes on the key's own ']' and copies
		// the remainder verbatim, so each of these leaked a full PAN before the
		// span-based rule replaced it.
		{name: "key_leads_with_closing_bracket", input: "Req.Limits[]4111111111111111]", want: "Req.Limits[*]"},
		{name: "key_embeds_closing_bracket", input: "Req.Limits[pan]=4111111111111111]", want: "Req.Limits[*]"},
		{name: "key_splits_on_closing_bracket", input: "Req.Limits[4111]1111111111]", want: "Req.Limits[*]"},
		{name: "key_contains_dot", input: "Req.Limits[a.b]", want: "Req.Limits[*]"},
		// A namespace that opens on '[' must still redact: the bracket is at
		// index 0, the boundary an off-by-one in the guard would wave through.
		{name: "leading_bracket", input: "[4111111111111111]", want: "[*]"},
		// An empty key puts the closing bracket first in the remainder, so the
		// trailing field path is only kept if the search is relative to it.
		{name: "empty_key_keeps_trailing_path", input: "Req.Limits[].Amount", want: "Req.Limits[*].Amount"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, saferender.RedactNamespace(tc.input))
		})
	}
}

func TestPayloadErrorNilAndZeroValueAreSafe(t *testing.T) {
	var nilErr *PayloadError
	assert.NotPanics(t, func() {
		_ = nilErr.Error()
		_ = nilErr.Unwrap()
		_ = nilErr.Fields()
		_ = nilErr.Is(ErrPayloadInvalid)
	})
	assert.NoError(t, nilErr.Unwrap())
	assert.Nil(t, nilErr.Fields())
	assert.False(t, nilErr.Is(ErrPayloadInvalid))
	require.NotErrorIs(t, error(nilErr), ErrPayloadUndecodable)

	zero := &PayloadError{}
	assert.NoError(t, zero.Unwrap())
	assert.NotPanics(t, func() { _ = zero.Error() })
	assert.Nil(t, zero.Fields())
	assert.NotErrorIs(t, zero, ErrPayloadUndecodable)
	assert.NotErrorIs(t, zero, ErrPayloadInvalid)
}

func TestPayloadErrorOpenStage(t *testing.T) {
	cause := &sealruntime.OpenRefusedError{Code: "SEAL_HEADER_SLOT_INVALID", Details: map[string]string{"slot": "jti", "present": "false"}, Cause: errors.New("rule 6")}
	err := newPayloadError("OrderCreated", payloaderr.NewOpen(cause))

	assert.Equal(t, PayloadStageOpen, err.Stage)
	assert.Equal(t, `messaging: open failed for event "OrderCreated": sealed open refused: SEAL_HEADER_SLOT_INVALID (present=false, slot=jti)`, err.Error())
	assert.Empty(t, err.Fields())
	assert.ErrorIs(t, err, ErrPayloadOpenRefused)
	assert.NotErrorIs(t, err, ErrPayloadUndecodable)
	require.NotErrorIs(t, err, ErrPayloadInvalid)
	var refused *sealruntime.OpenRefusedError
	require.ErrorAs(t, err, &refused)
	assert.Same(t, cause, refused)
	assert.NotContains(t, err.Error(), "rule 6", "the cause is reachable, never rendered")
}
