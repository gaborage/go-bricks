package messaging

import (
	"errors"
	"fmt"

	"github.com/gaborage/go-bricks/messaging/internal/payloaderr"
)

// PayloadStage names the half of the typed-payload pipeline that failed.
type PayloadStage string

// Stage values carried by PayloadError.Stage. A PayloadError whose Stage is
// none of these matches neither sentinel.
const (
	PayloadStageDecode   PayloadStage = PayloadStage(payloaderr.StageDecode)
	PayloadStageValidate PayloadStage = PayloadStage(payloaderr.StageValidate)
)

var (
	// ErrPayloadUndecodable reports a message body that could not be decoded into
	// the consumer's payload type. Match it with errors.Is.
	ErrPayloadUndecodable = errors.New("messaging: payload could not be decoded")

	// ErrPayloadInvalid reports a payload that decoded but failed struct
	// validation. Match it with errors.Is.
	ErrPayloadInvalid = errors.New("messaging: payload failed validation")
)

// PayloadError describes why a message body could not be turned into a typed
// payload. It is this lane's thin surface over payloaderr.Body, which owns the
// rendering rules and the SECURITY rationale behind them: Error() and Fields()
// are safe to log, Unwrap() is not.
type PayloadError struct {
	// EventType is the declared consumer event type the body was routed to.
	EventType string

	// Stage is where the failure happened. It is exported to label logs and
	// metrics with "decode" or "validate"; for control flow, match errors.Is
	// against ErrPayloadUndecodable or ErrPayloadInvalid instead.
	Stage PayloadStage

	// body carries the cause, the codec's payload-free summary and the raw
	// validator namespaces. Nil for a hand-built PayloadError, which every
	// accessor treats as carrying nothing.
	body *payloaderr.Body
}

// newPayloadError is the one place a Body becomes this lane's error, so the
// stage cannot be copied from one field and the body from another.
func newPayloadError(eventType string, body *payloaderr.Body) *PayloadError {
	return &PayloadError{
		EventType: eventType,
		Stage:     PayloadStage(body.Stage),
		body:      body,
	}
}

// Fields returns the validator field namespaces that failed, e.g.
// ["CreateReq.Amount"], with every bracketed span redacted. It is empty for
// decode failures and for a nil receiver.
func (e *PayloadError) Fields() []string {
	if e == nil {
		return nil
	}

	return e.body.Fields()
}

func (e *PayloadError) Error() string {
	if e == nil {
		return "messaging: <nil> payload error"
	}

	return e.body.Message("messaging", string(e.Stage), fmt.Sprintf("event %q", e.EventType))
}

// Unwrap exposes the underlying decode or validation error so errors.As can
// reach the cause.
//
// SECURITY: the returned error MAY carry payload-derived text. It is the
// deliberate escape hatch for a caller that needs the raw diagnostic; logging
// it is opt-in and on the caller.
func (e *PayloadError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.body.Unwrap()
}

// Is maps the stage onto its sentinel so consumers can discriminate the two
// failure modes without reading Stage.
func (e *PayloadError) Is(target error) bool {
	if e == nil {
		return false
	}

	switch e.Stage {
	case PayloadStageDecode:
		return target == ErrPayloadUndecodable
	case PayloadStageValidate:
		return target == ErrPayloadInvalid
	default:
		return false
	}
}
