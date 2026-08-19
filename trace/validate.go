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

// traceParentPattern matches the four fields every W3C traceparent begins with:
// two hex version digits, a 32-hex-digit trace-id, a 16-hex-digit parent-id and
// two hex flag digits. It is deliberately UNANCHORED at the end — see
// ValidateTraceParent for why a future version may carry more after them.
var traceParentPattern = regexp.MustCompile(`^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}`)

const (
	// The all-zero ids, which the spec declares invalid outright.
	zeroTraceID  = "00000000000000000000000000000000"
	zeroParentID = "0000000000000000"

	// versionZero is the only version defined today, and the only one whose
	// length is fixed. forbiddenVersion is ff, which the spec forbids rather
	// than reserving as a future version.
	versionZero      = "00"
	forbiddenVersion = "ff"

	// traceParentV00Len is len("00-"+32+"-"+16+"-"+2) — the whole of a version-00
	// value and the prefix of every later one.
	traceParentV00Len = 55
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
//
// Version handling follows the spec's forward-compatibility rule rather than a
// flat 55-character match. Version 00 is exactly 55 characters and anything
// longer is malformed. Versions 01..fe are FUTURE versions, which the spec
// defines as additive: a receiver parses trace-id, parent-id and flags from the
// version-00 positions and ignores whatever dash-delimited fields follow. A
// stricter reader would discard traceparents that later versions consider valid,
// dropping real upstream traces the day a version 01 appears on the wire. The
// one structural demand is that the extra fields are actually delimited — a
// 56-character value whose 56th byte is not a dash is not a longer traceparent,
// it is a corrupt one.
func ValidateTraceParent(tp string) string {
	if !traceParentPattern.MatchString(tp) {
		return ""
	}
	switch version := tp[:2]; {
	case version == forbiddenVersion:
		return ""
	case version == versionZero && len(tp) != traceParentV00Len:
		return ""
	case len(tp) > traceParentV00Len && tp[traceParentV00Len] != '-':
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
