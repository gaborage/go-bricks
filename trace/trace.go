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

// HeaderAccessor provides a simple interface for reading and writing headers
type HeaderAccessor interface {
	Get(key string) any
	Set(key string, value any)
}

// InjectMode controls how headers are written by InjectIntoHeaders
type InjectMode int

const (
	// InjectForce overwrites or aligns headers to ensure consistency with traceparent
	InjectForce InjectMode = iota
	// InjectPreserve sets headers only if missing; does not overwrite existing values
	InjectPreserve
)

// InjectOptions configures how trace context is injected into headers
type InjectOptions struct {
	Mode InjectMode
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

// InjectIntoHeaders injects trace context into transport headers (default: force mode)
// Backward compatible wrapper over InjectIntoHeadersWithOptions.
func InjectIntoHeaders(ctx context.Context, headers HeaderAccessor) {
	InjectIntoHeadersWithOptions(ctx, headers, InjectOptions{Mode: InjectForce})
}

// InjectIntoHeadersWithOptions injects trace context into headers with configurable behavior
func InjectIntoHeadersWithOptions(ctx context.Context, headers HeaderAccessor, opts InjectOptions) {
	if headers == nil {
		return
	}

	// Normalize mode (default to force)
	mode := opts.Mode
	if mode != InjectPreserve {
		mode = InjectForce
	}

	// Resolve values once
	traceparent := computeTraceParent(ctx, headers)

	if mode == InjectForce {
		// Align trace ID with traceparent and overwrite headers
		alignedTraceID := forceAlignTraceID(EnsureTraceID(ctx), traceparent)
		setHeader(headers, HeaderXRequestID, alignedTraceID, false)
		setHeader(headers, HeaderTraceParent, traceparent, false)
		if ts, ok := StateFromContext(ctx); ok {
			setHeader(headers, HeaderTraceState, ts, false)
		}
		return
	}

	// Preserve mode: only set when missing
	effectiveTraceID := computeTraceIDPreserve(ctx, headers, traceparent)
	setHeader(headers, HeaderXRequestID, effectiveTraceID, true)
	setHeader(headers, HeaderTraceParent, traceparent, true)
	if ts, ok := StateFromContext(ctx); ok {
		setHeader(headers, HeaderTraceState, ts, true)
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

// computeTraceIDPreserve returns the trace ID to use in preserve mode.
// Preference: existing header > context > derived from traceparent > generated
func computeTraceIDPreserve(ctx context.Context, headers HeaderAccessor, traceparent string) string {
	if v := headerString(headers, HeaderXRequestID); v != "" {
		return v
	}
	if tid, ok := IDFromContext(ctx); ok && tid != "" {
		return tid
	}
	if tid := extractTraceIDFromParent(traceparent); tid != "" {
		return tid
	}
	return EnsureTraceID(ctx)
}

// setHeader writes a header value, honoring preserve=true to avoid overwrites
func setHeader(headers HeaderAccessor, key, value string, preserve bool) {
	if preserve {
		if existing := headerString(headers, key); existing != "" {
			return
		}
	}
	headers.Set(key, value)
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
