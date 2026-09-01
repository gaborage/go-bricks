package payloaderr

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/validation"
)

const (
	// payloadMarker stands in for partner PII/PCI: it must never surface in a
	// rendered message, so tests plant it as a payload VALUE and assert its
	// absence.
	payloadMarker = "MARKER-do-not-leak-9e3f"

	// pan is a card-shaped value for the case where the leak vector is the map
	// key itself rather than a field value.
	pan = "4111111111111111"

	lanePrefix  = "testlane"
	laneSubject = `event "OrderCreated"`
)

type orderPayload struct {
	Reference string `json:"reference" validate:"max=5"`
	Amount    int64  `json:"amount"    validate:"required"`
}

// mapKeyPayload exercises the one shape whose validator namespace embeds payload
// content: a dived map, whose keys are interpolated verbatim.
type mapKeyPayload struct {
	Limits map[string]int `json:"limits" validate:"dive,max=5"`
}

func TestNewDecodeSubstitutesFailClosedSummary(t *testing.T) {
	cause := errors.New("json: unknown field " + payloadMarker)

	body := NewDecode(cause, "")

	assert.Equal(t, StageDecode, body.Stage)
	got := body.Message(lanePrefix, string(StageDecode), laneSubject)
	assert.Contains(t, got, UnauditedDecoderSummary)
	assert.NotContains(t, got, payloadMarker, "an unaudited cause is never rendered")
	require.Same(t, cause, body.Unwrap())
}

func TestNewDecodeKeepsAnAuditedSummary(t *testing.T) {
	body := NewDecode(errors.New("raw "+payloadMarker), "json: syntax error at offset 1")

	got := body.Message(lanePrefix, string(StageDecode), laneSubject)
	assert.Equal(t, `testlane: decode failed for event "OrderCreated": json: syntax error at offset 1`, got)
	assert.NotContains(t, got, payloadMarker)
}

func TestNewValidateStoresRawNamespacesAndRedactsOnRead(t *testing.T) {
	cause := validation.New().Struct(mapKeyPayload{Limits: map[string]int{pan: 99}})
	require.Error(t, cause)
	require.Contains(t, cause.Error(), pan, "premise: the raw cause leaks the map key")

	body := NewValidate(cause)

	// The constructor stores the validator's own namespaces verbatim, so the
	// single read-path design cannot drift into a second sanitization point
	// unnoticed.
	require.Len(t, body.fields, 1)
	assert.Contains(t, body.fields[0], pan, "constructor stores the raw namespace")

	assert.Equal(t, []string{"mapKeyPayload.Limits[*]"}, body.Fields())
	assert.NotContains(t, body.Message(lanePrefix, string(StageValidate), laneSubject), pan)
}

func TestFieldsReturnsAFreshSliceEveryCall(t *testing.T) {
	cause := validation.New().Struct(mapKeyPayload{Limits: map[string]int{payloadMarker: 99}})
	require.Error(t, cause)
	body := NewValidate(cause)

	got := body.Fields()
	require.Len(t, got, 1)
	got[0] = "tampered"

	assert.Equal(t, []string{"mapKeyPayload.Limits[*]"}, body.Fields())
	assert.Equal(t, []string{"mapKeyPayload.Limits[*]"}, body.Fields(), "the accessor must not rewrite in place")
}

// A non-validator cause yields no fields rather than a bogus one.
func TestNewValidateWithoutValidatorErrorsYieldsNoFields(t *testing.T) {
	body := NewValidate(errors.New("boom"))

	assert.Empty(t, body.Fields())
	assert.Equal(t, `testlane: validate failed for event "OrderCreated"`,
		body.Message(lanePrefix, string(StageValidate), laneSubject))
}

func TestMessageComposition(t *testing.T) {
	cause := validation.New().Struct(orderPayload{Reference: payloadMarker})
	require.Error(t, cause)

	// The validator's own text is absent by design — the sanitized field list
	// already carries everything that is safe to render.
	assert.Equal(t,
		`testlane: validate failed for event "OrderCreated" (fields: orderPayload.Reference, orderPayload.Amount)`,
		NewValidate(cause).Message(lanePrefix, string(StageValidate), laneSubject))
	assert.NotContains(t, NewValidate(cause).Message(lanePrefix, string(StageValidate), laneSubject), payloadMarker)
}

// The stage comes from the caller, so a lane error assembled without a Body
// still renders its own stage instead of collapsing to a nil rendering — and a
// nil Body contributes neither fields nor a summary.
func TestMessageOnNilBodyRendersTheCallersStage(t *testing.T) {
	var body *Body

	assert.Equal(t, `testlane: validate failed for event "OrderCreated"`,
		body.Message(lanePrefix, string(StageValidate), laneSubject))
	assert.Equal(t, `testlane: decode failed for event "OrderCreated"`,
		body.Message(lanePrefix, string(StageDecode), laneSubject))
	assert.Nil(t, body.Fields())
	assert.NoError(t, body.Unwrap())
}

// A summary belongs to the decode stage alone: rendering it under a validate
// stage would append a decoder's phrasing to a validation failure.
func TestMessageAppendsTheSummaryOnlyForDecode(t *testing.T) {
	body := NewDecode(errors.New("cause"), "json: syntax error at offset 1")

	assert.NotContains(t, body.Message(lanePrefix, string(StageValidate), laneSubject), "syntax error")
}
