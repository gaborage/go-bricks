package trace

import (
	"context"
	nethttp "net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeaderConstants(t *testing.T) {
	assert.Equal(t, "X-Request-ID", HeaderXRequestID)
	assert.Equal(t, "traceparent", HeaderTraceParent)
	assert.Equal(t, "tracestate", HeaderTraceState)
}

func TestEnsureTraceID_UsesExisting(t *testing.T) {
	ctx := WithTraceID(context.Background(), "existing-trace-id")
	got := EnsureTraceID(ctx)
	assert.Equal(t, "existing-trace-id", got)
}

func TestEnsureTraceID_GeneratesWhenMissing(t *testing.T) {
	got := EnsureTraceID(context.Background())
	// UUID v4 format: 36 chars with hyphens
	re := regexp.MustCompile(`^[a-f0-9\-]{36}$`)
	assert.True(t, re.MatchString(strings.ToLower(got)))
}

func TestTraceParent_ContextRoundTrip(t *testing.T) {
	in := "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	ctx := WithTraceParent(context.Background(), in)
	out, ok := ParentFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, in, out)
}

func TestTraceState_ContextRoundTrip(t *testing.T) {
	in := "vendor=a:b,c=d"
	ctx := WithTraceState(context.Background(), in)
	out, ok := StateFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, in, out)
}

func TestGenerateTraceParent_Format(t *testing.T) {
	tp := GenerateTraceParent()
	// Basic format checks
	assert.True(t, strings.HasPrefix(tp, "00-"))
	parts := strings.Split(tp, "-")
	require.Len(t, parts, 4)
	// version, trace-id, span-id, flags
	assert.Equal(t, 2, len(parts[0]))
	assert.Equal(t, 32, len(parts[1]))
	assert.Equal(t, 16, len(parts[2]))
	assert.Equal(t, 2, len(parts[3]))
	// Lowercase hex
	hexRe := regexp.MustCompile(`^[0-9a-f]+$`)
	assert.True(t, hexRe.MatchString(parts[1]))
	assert.True(t, hexRe.MatchString(parts[2]))
	assert.Equal(t, "01", parts[3])
}

func TestIDFromContext_Missing(t *testing.T) {
	_, ok := IDFromContext(context.Background())
	assert.False(t, ok)
}

func TestInjectIntoHeadersWithOptions_Preserve_PreservesExisting(t *testing.T) {
	headers := nethttp.Header{}
	// Pre-populate headers
	headers.Set(HeaderXRequestID, "pre-xid")
	headers.Set(HeaderTraceParent, "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	headers.Set(HeaderTraceState, "vendor=a:b")

	// adapter
	acc := httpHeaderAccessor{h: headers}

	// Context has different values – should not overwrite in preserve mode
	ctx := WithTraceID(context.Background(), "ctx-xid")
	ctx = WithTraceParent(ctx, "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
	ctx = WithTraceState(ctx, "vendor=ctx")

	InjectIntoHeadersWithOptions(ctx, &acc, InjectOptions{Mode: InjectPreserve})

	assert.Equal(t, "pre-xid", headers.Get(HeaderXRequestID))
	assert.Equal(t, "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01", headers.Get(HeaderTraceParent))
	assert.Equal(t, "vendor=a:b", headers.Get(HeaderTraceState))
}

func TestInjectIntoHeadersWithOptions_Preserve_FillsMissing(t *testing.T) {
	headers := nethttp.Header{}
	acc := httpHeaderAccessor{h: headers}

	// Context supplies traceparent and tracestate
	ctx := WithTraceParent(context.Background(), "00-deadbeefdeadbeefdeadbeefdeadbeef-0123456789abcdef-01")
	ctx = WithTraceState(ctx, "vendor=x")

	InjectIntoHeadersWithOptions(ctx, &acc, InjectOptions{Mode: InjectPreserve})

	assert.Equal(t, "00-deadbeefdeadbeefdeadbeefdeadbeef-0123456789abcdef-01", headers.Get(HeaderTraceParent))
	// X-Request-ID should be derived from traceparent when missing
	assert.Equal(t, "deadbeefdeadbeefdeadbeefdeadbeef", headers.Get(HeaderXRequestID))
	assert.Equal(t, "vendor=x", headers.Get(HeaderTraceState))
}

// Minimal http header accessor for tests
type httpHeaderAccessor struct{ h nethttp.Header }

func (a *httpHeaderAccessor) Get(key string) interface{} { return a.h.Get(key) }
func (a *httpHeaderAccessor) Set(key string, value interface{}) {
	switch v := value.(type) {
	case string:
		a.h.Set(key, v)
	default:
		a.h.Set(key, safeToString(v))
	}
}
