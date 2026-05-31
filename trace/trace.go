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

	traceCtx := ctx
	traceCtx = extractRequestID(traceCtx, headers)
	traceCtx = extractTraceParent(traceCtx, headers)
	traceCtx = extractTraceState(traceCtx, headers)

	return traceCtx
}

// extractRequestID extracts X-Request-ID header
func extractRequestID(ctx context.Context, headers HeaderAccessor) context.Context {
	if v := headers.Get(HeaderXRequestID); v != nil {
		if traceID := safeToString(v); traceID != "" {
			return WithTraceID(ctx, traceID)
		}
	}
	return ctx
}

// extractTraceParent extracts traceparent header and derives trace ID if needed
func extractTraceParent(ctx context.Context, headers HeaderAccessor) context.Context {
	v := headers.Get(HeaderTraceParent)
	if v == nil {
		return ctx
	}

	tp := safeToString(v)
	if tp == "" {
		return ctx
	}

	ctx = WithTraceParent(ctx, tp)

	// Derive trace ID from traceparent if not already set
	if _, hasTraceID := IDFromContext(ctx); !hasTraceID {
		if traceID := extractTraceIDFromParent(tp); traceID != "" {
			ctx = WithTraceID(ctx, traceID)
		}
	}

	return ctx
}

// extractTraceState extracts tracestate header
func extractTraceState(ctx context.Context, headers HeaderAccessor) context.Context {
	if v := headers.Get(HeaderTraceState); v != nil {
		if ts := safeToString(v); ts != "" {
			return WithTraceState(ctx, ts)
		}
	}
	return ctx
}

// InjectIntoHeaders writes the trace context (X-Request-ID, traceparent, and
// optionally tracestate) into the given transport headers. Always overwrites
// any existing values — the trace ID is forced to align with the traceparent
// so log/metric correlation across services stays consistent.
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

	// Extract trace ID from traceparent
	if parentTraceID := extractTraceIDFromParent(traceparent); parentTraceID != "" {
		return parentTraceID
	}

	return traceID
}
