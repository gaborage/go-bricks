package payloaderr

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecoderReportsDecodeFailures(t *testing.T) {
	decoder := NewDecoder[orderPayload](JSONCodec{})

	var payload orderPayload
	body := decoder.Decode(fmt.Appendf(nil, `{"amount":%q}`, payloadMarker), &payload)

	require.NotNil(t, body)
	assert.Equal(t, StageDecode, body.Stage)
	got := body.Message(lanePrefix, string(body.Stage), laneSubject)
	assert.Contains(t, got, `type mismatch at field "amount"`)
	assert.NotContains(t, got, payloadMarker)

	var typeErr *json.UnmarshalTypeError
	assert.ErrorAs(t, body.Unwrap(), &typeErr, "the raw cause stays reachable through Unwrap")
}

func TestDecoderReportsValidationFailures(t *testing.T) {
	decoder := NewDecoder[orderPayload](JSONCodec{})

	var payload orderPayload
	body := decoder.Decode([]byte(`{"reference":"toolong","amount":0}`), &payload)

	require.NotNil(t, body)
	assert.Equal(t, StageValidate, body.Stage)
	assert.Equal(t, []string{"orderPayload.Reference", "orderPayload.Amount"}, body.Fields())
}

func TestDecoderReportsNothingOnSuccess(t *testing.T) {
	decoder := NewDecoder[orderPayload](JSONCodec{})

	var payload orderPayload
	require.Nil(t, decoder.Decode([]byte(`{"reference":"abc","amount":7}`), &payload))
	assert.Equal(t, orderPayload{Reference: "abc", Amount: 7}, payload)
}

// A non-struct T reaches validation with a *validator.InvalidValidationError,
// which yields no fields and still carries StageValidate — failing closed on the
// first message rather than silently skipping validation forever.
func TestDecoderFailsClosedOnANonStructPayload(t *testing.T) {
	decoder := NewDecoder[int](JSONCodec{})

	var payload int
	body := decoder.Decode([]byte(`7`), &payload)

	require.NotNil(t, body)
	assert.Equal(t, StageValidate, body.Stage)
	assert.Empty(t, body.Fields())
}

// The gate is decided from T alone, so a destination that can put input text
// into the reported field path drops the path rather than echoing it.
func TestDecoderGatesTheFieldPathOnTheDestinationType(t *testing.T) {
	decoder := NewDecoder[mapKeyPayload](JSONCodec{})

	var payload mapKeyPayload
	body := decoder.Decode(fmt.Appendf(nil, `{"limits":{%q:"notanint"}}`, payloadMarker), &payload)

	require.NotNil(t, body)
	got := body.Message(lanePrefix, string(body.Stage), laneSubject)
	assert.Contains(t, got, "type mismatch (want")
	assert.NotContains(t, got, "at field")
	assert.NotContains(t, got, payloadMarker)
}

// unauditedCodec decodes as JSON but audits nothing, which is the shape a future
// codec starts as. Its failures must fall back to the fail-closed phrase rather
// than to the cause.
type unauditedCodec struct{ JSONCodec }

func (unauditedCodec) Summarize(error, bool) string { return "" }

func TestDecoderFallsBackWhenTheCodecAuditsNothing(t *testing.T) {
	decoder := NewDecoder[orderPayload](unauditedCodec{})

	var payload orderPayload
	body := decoder.Decode(fmt.Appendf(nil, `{"amount":%q}`, payloadMarker), &payload)

	require.NotNil(t, body)
	got := body.Message(lanePrefix, string(body.Stage), laneSubject)
	assert.Contains(t, got, UnauditedDecoderSummary)
	assert.NotContains(t, got, payloadMarker)
}

// A nil codec is JSONCodec, so a lane cannot end up with a decoder that decodes
// nothing.
func TestNewDecoderDefaultsToJSON(t *testing.T) {
	decoder := NewDecoder[orderPayload](nil)

	var payload orderPayload
	require.Nil(t, decoder.Decode([]byte(`{"reference":"abc","amount":7}`), &payload))
	assert.Equal(t, int64(7), payload.Amount)
}

// OnceValue's caching is directly observable through pointer identity: two calls
// returning the same instance is the whole contract. The instance is also the
// framework's, not a bare validator.New() — an unregistered mcc_code tag would
// make validator panic instead of returning an error.
func TestValidatorIsOneSharedFrameworkInstance(t *testing.T) {
	// Hoisted so the assertion compares two independent lookups rather than one
	// expression with itself — returning the same instance is the contract.
	first := Validator()
	second := Validator()
	require.Same(t, first, second)

	type mccPayload struct {
		MCC string `json:"mcc" validate:"mcc_code"`
	}
	assert.Error(t, Validator().Struct(mccPayload{MCC: "nope"}))
}
