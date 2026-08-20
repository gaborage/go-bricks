package trace

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// contextKey is the type for context keys to avoid collisions
type contextKey string

const (
	// traceIDKey is the context key for trace ID values
	traceIDKey contextKey = "trace_id"
	// traceParentKey is the context key for W3C Trace Context header value
	traceParentKey contextKey = "traceparent"
	// traceStateKey is the context key for W3C tracestate header value
	traceStateKey contextKey = "tracestate"
	// HeaderXRequestID is the standard header name for request tracing
	HeaderXRequestID = "X-Request-ID"
	// HeaderTraceParent is the W3C trace context header name
	HeaderTraceParent = "traceparent"
	// HeaderTraceState is the W3C trace context "tracestate" header name
	HeaderTraceState = "tracestate"
)

// WithTraceID adds a trace ID to the context
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// IDFromContext returns a trace ID from context if present
func IDFromContext(ctx context.Context) (string, bool) {
	if traceID, ok := ctx.Value(traceIDKey).(string); ok && traceID != "" {
		return traceID, true
	}
	return "", false
}

// EnsureTraceID returns an existing trace ID from context or generates a new one
func EnsureTraceID(ctx context.Context) string {
	if traceID, ok := IDFromContext(ctx); ok {
		return traceID
	}
	return uuid.New().String()
}

// WithTraceParent adds a W3C traceparent value to the context
func WithTraceParent(ctx context.Context, traceParent string) context.Context {
	return context.WithValue(ctx, traceParentKey, traceParent)
}

// ParentFromContext returns a traceparent from context if present
func ParentFromContext(ctx context.Context) (string, bool) {
	if tp, ok := ctx.Value(traceParentKey).(string); ok && tp != "" {
		return tp, true
	}
	return "", false
}

// WithTraceState adds a W3C tracestate value to the context
func WithTraceState(ctx context.Context, traceState string) context.Context {
	return context.WithValue(ctx, traceStateKey, traceState)
}

// StateFromContext returns a tracestate from context if present
func StateFromContext(ctx context.Context) (string, bool) {
	if ts, ok := ctx.Value(traceStateKey).(string); ok && ts != "" {
		return ts, true
	}
	return "", false
}

// GenerateTraceParent creates a minimal W3C traceparent header value.
// Format: version(2)-trace-id(32)-span-id(16)-flags(2), e.g., "00-<32>-<16>-01"
func GenerateTraceParent() string {
	traceID := make([]byte, 16)
	spanID := make([]byte, 8)
	if _, err := crand.Read(traceID); err != nil {
		traceID = []byte(strings.Repeat("\x00", 16))
	}
	if _, err := crand.Read(spanID); err != nil {
		spanID = []byte(strings.Repeat("\x00", 8))
	}
	if allZero(traceID) {
		traceID[len(traceID)-1] = 0x01
	}
	if allZero(spanID) {
		spanID[len(spanID)-1] = 0x01
	}
	return "00-" + strings.ToLower(hex.EncodeToString(traceID)) + "-" + strings.ToLower(hex.EncodeToString(spanID)) + "-01"
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// HeaderAccessor provides a simple interface for reading and writing headers
type HeaderAccessor interface {
	Get(key string) any
	Set(key string, value any)
}

// ExtractFromHeaders extracts trace context from transport headers
func ExtractFromHeaders(ctx context.Context, headers HeaderAccessor) context.Context {
	if headers == nil {
		return ctx
	}

	// Each step reports whether THIS carrier supplied the value, never whether one
	// is present in ctx. A ctx arriving with a caller's identifiers is normal, and
	// mixing those with a carrier's produces a delivery that straddles two traces:
	// the handler logging under one while the span parent is another, and the
	// first trace's tracestate re-emitted under the second.
	traceCtx, carriedID := extractRequestID(ctx, headers)
	traceCtx, carriedParent := extractTraceParent(traceCtx, headers, carriedID)
	return extractTraceState(traceCtx, headers, carriedParent)
}

// extractRequestID extracts the X-Request-ID header. Validation runs AFTER
// coercion, because safeToString renders any AMQP field type — []byte, ints, a
// nested Table as map[k:v] — and the charset rejects those renderings naturally.
//
// A rejected value plants nothing and returns ctx unchanged, which is what opens
// extractTraceParent's derivation guard so the traceparent-derived id wins
// instead. Discarding rather than truncating is deliberate: see ValidateRequestID.
func extractRequestID(ctx context.Context, headers HeaderAccessor) (traceCtx context.Context, carried bool) {
	if v := headers.Get(HeaderXRequestID); v != nil {
		if traceID := ValidateRequestID(safeToString(v)); traceID != "" {
			return WithTraceID(ctx, traceID), true
		}
	}
	return ctx, false
}

// extractTraceParent extracts traceparent header and derives trace ID if needed.
// It reports whether THIS carrier supplied a valid traceparent, which is what
// gates the tracestate that accompanies it.
func extractTraceParent(ctx context.Context, headers HeaderAccessor, carriedID bool) (traceCtx context.Context, carried bool) {
	v := headers.Get(HeaderTraceParent)
	if v == nil {
		return ctx, false
	}

	// Validate before storing anything: the raw traceparent was previously kept
	// verbatim and re-emitted on every outbound hop, and forceAlignTraceID
	// re-emits the raw request id whenever the traceparent is malformed — the one
	// condition under which a poisoned value escapes onto the next hop.
	tp := ValidateTraceParent(safeToString(v))
	if tp == "" {
		return ctx, false
	}

	ctx = WithTraceParent(ctx, tp)

	// Derive the trace ID from THIS carrier's traceparent unless this same carrier
	// also supplied a valid request id. Testing IDFromContext instead would let an
	// id inherited from the caller outrank the carrier's own parent, so the handler
	// would log under the caller's trace while its span hung under the carrier's.
	if !carriedID {
		if traceID := extractTraceIDFromParent(tp); traceID != "" {
			ctx = WithTraceID(ctx, traceID)
		}
	}

	return ctx, true
}

// extractTraceState extracts tracestate header. carriedParent says whether the
// SAME carrier supplied a valid traceparent.
func extractTraceState(ctx context.Context, headers HeaderAccessor, carriedParent bool) context.Context {
	// tracestate only means something alongside the traceparent it accompanies.
	// Storing it without one leaves orphan state that InjectIntoHeaders would
	// re-emit attached to a trace it never belonged to. Reading the context here
	// instead would fail open twice over: a carrier with no traceparent, or with
	// a malformed one, would have its tracestate adopted by whatever traceparent
	// the caller's context already held.
	if !carriedParent {
		return ctx
	}
	if v := headers.Get(HeaderTraceState); v != nil {
		// Cap only, no grammar — see MaxTraceStateBytes for why.
		if ts := safeToString(v); ts != "" && len(ts) <= MaxTraceStateBytes {
			return WithTraceState(ctx, ts)
		}
	}
	// This carrier brought a traceparent but no usable tracestate. Any tracestate
	// already in ctx annotates the CALLER's parent, so leaving it would re-emit one
	// trace's vendor state under another's traceparent. Shadow it with empty:
	// StateFromContext reports absent for "", so nothing downstream carries it.
	return WithTraceState(ctx, "")
}

// InjectIntoHeaders writes the trace context (X-Request-ID, traceparent, and
// optionally tracestate) into the given transport headers. Always overwrites
// any existing values — the trace ID is forced to align with the traceparent
// so log/metric correlation across services stays consistent.
//
// HeaderXRequestID is always written, always as a Go string: a caller needing
// the outbound trace identity — the AMQP publish path does, for the message's
// CorrelationId — reads it back from that header rather than deriving its own,
// which would skip the alignment above.
//
// Historically this routed through an InjectIntoHeadersWithOptions variant
// that supported a Preserve mode (set-if-missing). Preserve mode had zero
// callers across the framework, tools, and tests, so the mode-selector API
// was removed in W4-H. Add it back with a fresh design if a real consumer
// need surfaces.
func InjectIntoHeaders(ctx context.Context, headers HeaderAccessor) {
	if headers == nil {
		return
	}

	traceparent := computeTraceParent(ctx, headers)
	alignedTraceID := forceAlignTraceID(EnsureTraceID(ctx), traceparent)

	headers.Set(HeaderXRequestID, alignedTraceID)
	headers.Set(HeaderTraceParent, traceparent)
	if ts, ok := StateFromContext(ctx); ok {
		headers.Set(HeaderTraceState, ts)
	}
}

// computeTraceParent determines the traceparent value to use (existing header > context > generated)
func computeTraceParent(ctx context.Context, headers HeaderAccessor) string {
	if v := headerString(headers, HeaderTraceParent); v != "" {
		return v
	}
	if tp, ok := ParentFromContext(ctx); ok && tp != "" {
		return tp
	}
	return GenerateTraceParent()
}

// headerString gets a header as string using safe conversion
func headerString(headers HeaderAccessor, key string) string {
	if v := headers.Get(key); v != nil {
		return safeToString(v)
	}
	return ""
}

// safeToString safely converts any to string, handling []byte
func safeToString(value any) string {
	if value == nil {
		return ""
	}

	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		if str := fmt.Sprintf("%v", v); str != "<nil>" {
			return str
		}
		return ""
	}
}

// extractTraceIDFromParent extracts the trace ID from a W3C traceparent header
func extractTraceIDFromParent(traceparent string) string {
	parts := strings.Split(traceparent, "-")
	if len(parts) >= 4 && len(parts[1]) == 32 {
		return parts[1]
	}
	return ""
}

// forceAlignTraceID aligns trace ID with traceparent (force mode)
func forceAlignTraceID(traceID, traceparent string) string {
	if traceparent == "" {
		return traceID
	}

	if parentTraceID := extractTraceIDFromParent(traceparent); parentTraceID != "" {
		return parentTraceID
	}

	return traceID
}
