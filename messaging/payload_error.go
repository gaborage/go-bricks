package messaging

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"

	"github.com/gaborage/go-bricks/internal/saferender"
)

// PayloadStage names the half of the typed-payload pipeline that failed.
type PayloadStage string

// Stage values carried by PayloadError.Stage. A PayloadError whose Stage is
// none of these matches neither sentinel.
const (
	PayloadStageDecode   PayloadStage = "decode"
	PayloadStageValidate PayloadStage = "validate"
)

// unauditedDecoderSummary is the fail-closed rendering for a decode error whose
// shape has not been audited for payload content.
const unauditedDecoderSummary = "cause withheld (unaudited decoder); use errors.Unwrap for the raw error"

var (
	// ErrPayloadUndecodable reports a message body that could not be decoded into
	// the consumer's payload type. Match it with errors.Is.
	ErrPayloadUndecodable = errors.New("messaging: payload could not be decoded")

	// ErrPayloadInvalid reports a payload that decoded but failed struct
	// validation. Match it with errors.Is.
	ErrPayloadInvalid = errors.New("messaging: payload failed validation")
)

// PayloadError describes why a message body could not be turned into a typed
// payload.
//
// SECURITY: AMQP bodies are partner PII/PCI data, so the framework's own
// rendering must stay free of them. Error() and Fields() are safe to log;
// Unwrap() is not. Error() composes its text from schema facts only — it never
// renders the wrapped cause verbatim, because every producer in reach echoes
// payload bytes in at least one shape:
//   - json.UnmarshalTypeError.Value carries the raw literal ("number 1234.56")
//     and, for integer-keyed maps, the raw key; its Field is schema-only for a
//     map-free destination and carries the input key for a map one — or for a
//     field decoding itself — which is why the summary's field path is gated
//     on the payload type.
//   - json.SyntaxError quotes the offending payload byte.
//   - json.Decoder.DisallowUnknownFields reports the partner-supplied key verbatim.
//   - validator namespaces interpolate map keys verbatim ("Limits[4111...]"),
//     which is why the namespace list is unexported and redacted on read.
//
// The rendering itself lives on the codec seam (codec.summarize), so a new codec
// (issue #346) must supply its own audited phrasing; until it does, the
// constructor substitutes the fail-closed phrase and the cause itself is never
// rendered.
type PayloadError struct {
	// EventType is the declared consumer event type the body was routed to.
	EventType string

	// Stage is where the failure happened. It is exported to label logs and
	// metrics with "decode" or "validate"; for control flow, match errors.Is
	// against ErrPayloadUndecodable or ErrPayloadInvalid instead.
	Stage PayloadStage

	// fields holds the RAW validator namespaces, which may embed payload values:
	// validator interpolates map keys into bracketed segments verbatim, so a
	// dived map yields "CreateReq.Limits[4111111111111111]". Fields() is the
	// only safe read.
	fields []string

	// summary is the codec's payload-free rendering of a decode cause. It is what
	// Error() prints; the cause itself is never rendered.
	summary string

	err error
}

// newPayloadDecodeError wraps a decode failure. The cause survives for Unwrap
// only; Error() prints summary instead, which the codec produced.
//
// SECURITY: an empty summary means the codec did not audit this error shape, so
// the fail-closed phrase substitutes here rather than at the call site — no
// caller can render an unaudited cause by forgetting the fallback.
func newPayloadDecodeError(eventType string, cause error, summary string) *PayloadError {
	if summary == "" {
		summary = unauditedDecoderSummary
	}

	return &PayloadError{
		EventType: eventType,
		Stage:     PayloadStageDecode,
		summary:   summary,
		err:       cause,
	}
}

// newPayloadValidateError wraps a validation failure and records the validator's
// own field namespaces verbatim. Redaction is Fields()' job, not the
// constructor's, so no assembly path can produce a PayloadError whose namespace
// list reads back unsanitized.
func newPayloadValidateError(eventType string, cause error) *PayloadError {
	var verrs validator.ValidationErrors
	var fields []string
	if errors.As(cause, &verrs) {
		fields = make([]string, 0, len(verrs))
		for _, fe := range verrs {
			fields = append(fields, fe.Namespace())
		}
	}

	return &PayloadError{
		EventType: eventType,
		Stage:     PayloadStageValidate,
		fields:    fields,
		err:       cause,
	}
}

// Fields returns the validator field namespaces that failed, e.g.
// ["CreateReq.Amount"]. It is empty for decode failures and for a nil receiver.
//
// SECURITY: the bracketed span is redacted to [*] on the way out, and the result
// is a fresh slice, so the redaction survives whatever the caller does with it.
// This is the only read path onto the namespaces.
func (e *PayloadError) Fields() []string {
	if e == nil || len(e.fields) == 0 {
		return nil
	}

	sanitized := make([]string, len(e.fields))
	for i, ns := range e.fields {
		sanitized[i] = saferender.RedactNamespace(ns)
	}

	return sanitized
}

func (e *PayloadError) Error() string {
	if e == nil {
		return "messaging: <nil> payload error"
	}

	msg := fmt.Sprintf("messaging: %s failed for event %q", e.Stage, e.EventType)
	if fields := e.Fields(); len(fields) > 0 {
		msg += fmt.Sprintf(" (fields: %s)", strings.Join(fields, ", "))
	}
	if e.Stage == PayloadStageDecode && e.summary != "" {
		msg += ": " + e.summary
	}

	return msg
}

// Unwrap exposes the underlying decode or validation error so errors.As can
// reach the cause.
//
// SECURITY: the returned error MAY carry payload-derived text — a rejected
// numeric literal, an offending byte, an unknown key, a map key. It is the
// deliberate escape hatch for a caller that needs the raw diagnostic; logging
// it is opt-in and on the caller.
func (e *PayloadError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.err
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
