package streams

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/validation"
	"github.com/gaborage/go-bricks/messaging/internal/delivery"
	"github.com/gaborage/go-bricks/messaging/internal/payloaderr"
)

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
			err := &PayloadError{Consumer: testConsumerName, Stage: tc.stage}

			assert.ErrorIs(t, err, tc.match)
			assert.NotErrorIs(t, err, tc.notMatch)
			assert.NotErrorIs(t, err, errHandlerFailed)
		})
	}
}

// An unrecognized Stage must match neither sentinel rather than defaulting to
// one: a mistyped stage would otherwise misroute the poison decision.
func TestPayloadErrorIsRejectsUnknownStage(t *testing.T) {
	for _, stage := range []PayloadStage{"", "decoding", "DECODE"} {
		err := &PayloadError{Consumer: testConsumerName, Stage: stage}

		assert.NotErrorIs(t, err, ErrPayloadUndecodable, "stage %q", stage)
		assert.NotErrorIs(t, err, ErrPayloadInvalid, "stage %q", stage)
	}
}

// The two sentinels are this lane's own, so a consumer of both lanes cannot
// match a stream failure with the AMQP lane's sentinel by accident.
func TestPayloadErrorSentinelsNameThisLane(t *testing.T) {
	assert.Equal(t, "streams: payload could not be decoded", ErrPayloadUndecodable.Error())
	assert.Equal(t, "streams: payload failed validation", ErrPayloadInvalid.Error())
}

func TestPayloadErrorRendersTheConsumerAsItsSubject(t *testing.T) {
	cause := validation.New().Struct(streamOrder{Reference: streamPayloadMarker})
	require.Error(t, cause)

	err := newPayloadError(testConsumerName, payloaderr.NewValidate(cause))

	assert.Equal(t, PayloadStageValidate, err.Stage)
	assert.Equal(t, testConsumerName, err.Consumer)
	assert.Equal(t,
		fmt.Sprintf(`streams: validate failed for consumer %q (fields: streamOrder.Reference, streamOrder.Amount)`, testConsumerName),
		err.Error())
	// The validator's own text carries the rejected value; the rendering does not.
	assert.NotContains(t, err.Error(), streamPayloadMarker)
}

func TestPayloadErrorUnwrapReachesCause(t *testing.T) {
	inner := errors.New("inner cause")
	err := newPayloadError(testConsumerName, payloaderr.NewDecode(inner, ""))

	require.Same(t, inner, err.Unwrap())
	assert.ErrorIs(t, err, inner)
	// The sentinel mapping must survive alongside the unwrap chain.
	assert.ErrorIs(t, err, ErrPayloadUndecodable)
	assert.Contains(t, err.Error(), payloaderr.UnauditedDecoderSummary)
}

// A nil receiver is reachable from a caller holding a typed nil, and must render
// rather than panic on the log path.
func TestPayloadErrorNilReceiverIsInert(t *testing.T) {
	var err *PayloadError

	assert.Equal(t, "streams: <nil> payload error", err.Error())
	assert.Nil(t, err.Fields())
	assert.NoError(t, err.Unwrap())
	assert.False(t, err.Is(ErrPayloadUndecodable))
}

// isPayloadFailure is what keeps poison out of the hold, so it has to see
// through both wraps a delivery can arrive with: the Permanent marker the typed
// handler applies, and a lane caller's own %w.
func TestIsPayloadFailureReadsThroughTheWrapChain(t *testing.T) {
	payloadErr := newPayloadError(testConsumerName, payloaderr.NewDecode(errors.New("boom"), "summary"))

	assert.True(t, isPayloadFailure(payloadErr))
	assert.True(t, isPayloadFailure(delivery.Permanent(payloadErr)))
	assert.True(t, isPayloadFailure(fmt.Errorf("consumer %q: %w", testConsumerName, payloadErr)))
	assert.True(t, isPayloadFailure(delivery.Permanent(fmt.Errorf("wrapped: %w", payloadErr))))

	assert.False(t, isPayloadFailure(nil))
	assert.False(t, isPayloadFailure(errHandlerFailed))
	assert.False(t, isPayloadFailure(delivery.Permanent(errHandlerFailed)))
}
