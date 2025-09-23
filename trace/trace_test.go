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

func TestEnsureTraceIDUsesExisting(t *testing.T) {
	ctx := WithTraceID(context.Background(), "existing-trace-id")
	got := EnsureTraceID(ctx)
	assert.Equal(t, "existing-trace-id", got)
}

func TestEnsureTraceIDGeneratesWhenMissing(t *testing.T) {
	got := EnsureTraceID(context.Background())
	// UUID v4 format: 36 chars with hyphens
	re := regexp.MustCompile(`^[a-f0-9\-]{36}$`)
	assert.True(t, re.MatchString(strings.ToLower(got)))
}

func TestTraceParentContextRoundTrip(t *testing.T) {
	in := "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	ctx := WithTraceParent(context.Background(), in)
	out, ok := ParentFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, in, out)
}

func TestTraceStateContextRoundTrip(t *testing.T) {
	in := "vendor=a:b,c=d"
	ctx := WithTraceState(context.Background(), in)
	out, ok := StateFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, in, out)
}

func TestGenerateTraceParentFormat(t *testing.T) {
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

func TestIDFromContextMissing(t *testing.T) {
	_, ok := IDFromContext(context.Background())
	assert.False(t, ok)
}

func TestInjectIntoHeadersWithOptionsPreservePreservesExisting(t *testing.T) {
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

func TestInjectIntoHeadersWithOptionsPreserveFillsMissing(t *testing.T) {
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

func (a *httpHeaderAccessor) Get(key string) any { return a.h.Get(key) }
func (a *httpHeaderAccessor) Set(key string, value any) {
	switch v := value.(type) {
	case string:
		a.h.Set(key, v)
	default:
		a.h.Set(key, safeToString(v))
	}
}

// Additional tests merged from trace_extra_test.go

// Simple map-based HeaderAccessor for tests
type mapAccessor struct{ m map[string]any }

func (a *mapAccessor) Get(key string) any {
	if a.m == nil {
		return nil
	}
	return a.m[key]
}
func (a *mapAccessor) Set(key string, value any) {
	if a.m == nil {
		a.m = map[string]any{}
	}
	a.m[key] = value
}

func TestExtractFromHeadersAllPresent(t *testing.T) {
	acc := &mapAccessor{m: map[string]any{
		HeaderXRequestID:  "rid-123",
		HeaderTraceParent: "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01",
		HeaderTraceState:  "vendor=a:b",
	}}

	ctx := ExtractFromHeaders(context.Background(), acc)

	tid, ok := IDFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "rid-123", tid)

	tp, ok := ParentFromContext(ctx)
	require.True(t, ok)
	assert.NotEmpty(t, tp)

	ts, ok := StateFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "vendor=a:b", ts)
}

func TestExtractFromHeadersDeriveIDFromParent(t *testing.T) {
	acc := &mapAccessor{m: map[string]any{
		HeaderTraceParent: "00-deadbeefdeadbeefdeadbeefdeadbeef-0123456789abcdef-01",
	}}
	ctx := ExtractFromHeaders(context.Background(), acc)
	tid, ok := IDFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "deadbeefdeadbeefdeadbeefdeadbeef", tid)
}

func TestExtractFromHeadersNilHeaders(t *testing.T) {
	ctx := ExtractFromHeaders(context.Background(), nil)
	_, ok := IDFromContext(ctx)
	assert.False(t, ok)
}

func TestInjectIntoHeadersForceMode(t *testing.T) {
	// Context with parent and state; force mode aligns X-Request-ID with parent
	ctx := WithTraceParent(context.Background(), "00-aabbccddeeffaabbccddeeffaabbccdd-1122334455667788-01")
	ctx = WithTraceState(ctx, "vendor=test")

	acc := &mapAccessor{m: map[string]any{}}
	InjectIntoHeaders(ctx, acc) // wrapper (force mode)

	assert.Equal(t, "aabbccddeeffaabbccddeeffaabbccdd", acc.m[HeaderXRequestID])
	assert.Equal(t, "00-aabbccddeeffaabbccddeeffaabbccdd-1122334455667788-01", acc.m[HeaderTraceParent])
	assert.Equal(t, "vendor=test", acc.m[HeaderTraceState])
}

func TestComputeHelpers(t *testing.T) {
	// computeTraceParent: header > context > generated
	acc := &mapAccessor{m: map[string]any{HeaderTraceParent: "00-11111111111111111111111111111111-2222222222222222-01"}}
	assert.Equal(t, "00-11111111111111111111111111111111-2222222222222222-01", computeTraceParent(context.Background(), acc))

	ctx := WithTraceParent(context.Background(), "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
	assert.Equal(t, "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01", computeTraceParent(ctx, &mapAccessor{}))

	// Fallback generates a valid 00-... string
	gen := computeTraceParent(context.Background(), &mapAccessor{})
	ok, _ := regexp.MatchString(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`, gen)
	assert.True(t, ok)

	// computeTraceIDPreserve: header > context > derived > generated
	acc2 := &mapAccessor{m: map[string]any{HeaderXRequestID: "hdr-id"}}
	assert.Equal(t, "hdr-id", computeTraceIDPreserve(context.Background(), acc2, "00-deadbeefdeadbeefdeadbeefdeadbeef-0123456789abcdef-01"))

	ctx2 := WithTraceID(context.Background(), "ctx-id")
	assert.Equal(t, "ctx-id", computeTraceIDPreserve(ctx2, &mapAccessor{}, "00-deadbeefdeadbeefdeadbeefdeadbeef-0123456789abcdef-01"))

	assert.Equal(t, "deadbeefdeadbeefdeadbeefdeadbeef", computeTraceIDPreserve(context.Background(), &mapAccessor{}, "00-deadbeefdeadbeefdeadbeefdeadbeef-0123456789abcdef-01"))

	// generated when nothing present
	got := computeTraceIDPreserve(context.Background(), &mapAccessor{}, "invalid-parent")
	assert.NotEmpty(t, got)
}

func TestHeaderStringAndSafeToString(t *testing.T) {
	acc := &mapAccessor{m: map[string]any{HeaderXRequestID: []byte("bytes-id")}}
	assert.Equal(t, "bytes-id", headerString(acc, HeaderXRequestID))
	assert.Equal(t, "", headerString(&mapAccessor{}, "missing"))

	assert.Equal(t, "str", safeToString("str"))
	assert.Equal(t, "abc", safeToString([]byte("abc")))
	assert.Equal(t, "123", safeToString(123))
	var p *int
	assert.Equal(t, "", safeToString(p))
}

func TestExtractTraceIDAndForceAlign(t *testing.T) {
	// extractTraceIDFromParent
	assert.Equal(t, "0123456789abcdef0123456789abcdef", extractTraceIDFromParent("00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"))
	assert.Equal(t, "", extractTraceIDFromParent("bad-parent"))

	// forceAlignTraceID
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", forceAlignTraceID("orig", "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"))
	assert.Equal(t, "orig", forceAlignTraceID("orig", ""))
}
