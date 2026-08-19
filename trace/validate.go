package trace

import "regexp"

// MaxTraceStateBytes is the W3C-recommended limit on a tracestate header. It is
// a cap and nothing more: the grammar is deliberately NOT validated here, and
// OTel's ParseTraceState is deliberately NOT used, because that would put an
// OpenTelemetry dependency underneath server, messaging and outbox for a value
// this framework only stores and forwards. A cap bounds the real harm —
// unbounded storage and unbounded re-emission on every outbound hop — at a
// fraction of the coupling. That is a deliberate trade, not an oversight
// (ADR-070).
const MaxTraceStateBytes = 512

// requestIDPattern matches a safe X-Request-ID value: ASCII alphanumerics,
// underscores, and hyphens, length 1..128. Caller-supplied values that fail
// this match are discarded — they flow into log envelopes, AMQP CorrelationId,
// JOSE failure records, and rate-limit error bodies, so an attacker who can set
// the header could poison logs or, over 255 bytes, tear down the shared AMQP
// connection every publisher in the process uses.
//
// The bound is byte-for-byte the one server has always applied to inbound HTTP;
// this moves it down so every door gets it, including the ones that have no
// door of their own yet.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// traceParentPattern is the W3C traceparent format, spec-exact: two hex version
// digits, a 32-hex-digit trace-id, a 16-hex-digit parent-id and two hex flag
// digits, 55 characters in total.
var traceParentPattern = regexp.MustCompile(`^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

// The forms the W3C spec declares invalid: the all-zero ids, and version ff,
// which the spec forbids outright rather than treating as a future version.
const (
	zeroTraceID     = "00000000000000000000000000000000"
	zeroParentID    = "0000000000000000"
	forbiddenVerson = "ff"
)

// ValidateRequestID returns id when it is a safe request identifier, otherwise
// "". A caller that gets "" must fall back to a trusted source — a
// traceparent-derived id or a fresh UUID — and must never truncate: truncation
// silently forges correlation by mapping distinct upstream ids onto one, which
// is why W3C, OpenTelemetry and Heroku all independently refuse it.
func ValidateRequestID(id string) string {
	if requestIDPattern.MatchString(id) {
		return id
	}
	return ""
}

// ValidateTraceParent returns tp when it is a well-formed, non-zero W3C
// traceparent, otherwise "". Rejecting the all-zero trace-id and parent-id
// mirrors OpenTelemetry's own Extract, which treats them as absent rather than
// as a trace to join.
func ValidateTraceParent(tp string) string {
	if !traceParentPattern.MatchString(tp) {
		return ""
	}
	if tp[:2] == forbiddenVerson {
		return ""
	}
	parts := splitTraceParent(tp)
	if parts.traceID == zeroTraceID || parts.parentID == zeroParentID {
		return ""
	}
	return tp
}

type traceParentFields struct{ traceID, parentID string }

// splitTraceParent reads the two id fields by offset. The pattern has already
// fixed every length, so indexing is exact and needs no bounds check.
func splitTraceParent(tp string) traceParentFields {
	return traceParentFields{traceID: tp[3:35], parentID: tp[36:52]}
}
