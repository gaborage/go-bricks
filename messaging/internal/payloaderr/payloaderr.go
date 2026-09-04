// Package payloaderr holds the payload-error core both messaging lanes build
// their typed consumers on: the decode and struct-validation steps, and a
// failure rendering that never echoes the body that caused it.
//
// It exists here rather than in either lane because the two are deliberately
// decoupled — messaging/streams must not import messaging — so what they share
// travels through messaging/internal/*. Each lane exports a thin error type
// over Body and supplies its own prefix, subject and sentinels; the rules that
// keep payload bytes out of a rendering are stated once, here.
package payloaderr

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"

	"github.com/gaborage/go-bricks/internal/saferender"
)

// Stage names the half of the typed-payload pipeline that failed. A lane
// exports its own string type over these two values.
type Stage string

// The stages a Body can carry. A Body whose Stage is none of these is not one
// this package produced.
const (
	StageDecode   Stage = "decode"
	StageValidate Stage = "validate"
	// StageOpen is the sealed-message opener refusing a body before decode: the
	// signature, the key families, the signed slots or the decrypt (ADR-097).
	StageOpen Stage = "open"
)

// UnauditedDecoderSummary is the fail-closed rendering for a decode error whose
// shape has not been audited for payload content.
const UnauditedDecoderSummary = "cause withheld (unaudited decoder); use errors.Unwrap for the raw error"

// Body is the lane-agnostic state of a payload failure: what stage failed, the
// payload-free rendering of why, and the cause itself.
//
// SECURITY: message bodies are partner PII/PCI on both lanes, so the framework's
// own rendering must stay free of them. Message() and Fields() are safe to log;
// Unwrap() is not. Message() composes its text from schema facts only — it never
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
// The decode rendering itself lives on the codec seam (Codec.Summarize), so a new
// codec (issue #346) must supply its own audited phrasing; until it does,
// NewDecode substitutes the fail-closed phrase and the cause is never rendered.
type Body struct {
	// Stage is where the failure happened. A lane exports it to label logs and
	// metrics; for control flow a lane maps it onto its own sentinels.
	Stage Stage

	// fields holds the RAW validator namespaces, which may embed payload values:
	// validator interpolates map keys into bracketed segments verbatim, so a
	// dived map yields "CreateReq.Limits[4111111111111111]". Fields() is the
	// only safe read.
	fields []string

	// summary is the codec's payload-free rendering of a decode cause. It is what
	// Message() prints; the cause itself is never rendered.
	summary string

	err error
}

// NewDecode wraps a decode failure. The cause survives for Unwrap only;
// Message() prints summary instead, which the codec produced.
//
// SECURITY: an empty summary means the codec did not audit this error shape, so
// the fail-closed phrase substitutes here rather than at the call site — no
// caller can render an unaudited cause by forgetting the fallback.
func NewDecode(cause error, summary string) *Body {
	if summary == "" {
		summary = UnauditedDecoderSummary
	}

	return &Body{Stage: StageDecode, summary: summary, err: cause}
}

// NewOpen wraps an opener refusal. The cause renders itself as its code and its
// presence/length details only — the opener seam guarantees no wire value is in
// that text — so its own rendering is the summary Message() prints.
func NewOpen(cause error) *Body {
	return &Body{Stage: StageOpen, summary: cause.Error(), err: cause}
}

// NewValidate wraps a validation failure and records the validator's own field
// namespaces verbatim. Redaction is Fields()' job, not the constructor's, so no
// assembly path can produce a Body whose namespace list reads back unsanitized.
func NewValidate(cause error) *Body {
	var verrs validator.ValidationErrors
	var fields []string
	if errors.As(cause, &verrs) {
		fields = make([]string, 0, len(verrs))
		for _, fe := range verrs {
			fields = append(fields, fe.Namespace())
		}
	}

	return &Body{Stage: StageValidate, fields: fields, err: cause}
}

// Fields returns the validator field namespaces that failed, e.g.
// ["CreateReq.Amount"]. It is empty for decode failures and for a nil receiver.
//
// SECURITY: the bracketed span is redacted to [*] on the way out, and the result
// is a fresh slice, so the redaction survives whatever the caller does with it.
// This is the only read path onto the namespaces.
func (b *Body) Fields() []string {
	if b == nil || len(b.fields) == 0 {
		return nil
	}

	sanitized := make([]string, len(b.fields))
	for i, ns := range b.fields {
		sanitized[i] = saferender.RedactNamespace(ns)
	}

	return sanitized
}

// Message composes the payload-free text a lane's Error() returns. prefix is the
// lane's package name, stage is the lane's own rendering of the stage, and
// subject names what the body was routed to, already quoted by the lane —
// `event "OrderCreated"` on the AMQP lane, `consumer "order-projector"` on the
// streams lane.
//
// The stage is the caller's rather than this Body's, so a lane error assembled
// without a Body — which only a lane's own tests can now do — still renders its
// stage instead of collapsing to a nil rendering.
func (b *Body) Message(prefix, stage, subject string) string {
	msg := fmt.Sprintf("%s: %s failed for %s", prefix, stage, subject)
	if fields := b.Fields(); len(fields) > 0 {
		msg += fmt.Sprintf(" (fields: %s)", strings.Join(fields, ", "))
	}
	if b != nil && (Stage(stage) == StageDecode || Stage(stage) == StageOpen) && b.summary != "" {
		msg += ": " + b.summary
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
func (b *Body) Unwrap() error {
	if b == nil {
		return nil
	}

	return b.err
}
