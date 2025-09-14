package trace

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
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

// TraceIDFromContext returns a trace ID from context if present
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

// TraceParentFromContext returns a traceparent from context if present
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

// TraceStateFromContext returns a tracestate from context if present
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
