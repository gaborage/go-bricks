package trace

import (
	"context"
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

// A delivery must belong to ONE trace. The hazard is a context that already
// carries a caller's identifiers meeting a carrier that brings its own: taking
// the parent from the carrier while keeping the id and tracestate from the
// context produces a delivery that straddles both, and nothing errors.
func TestExtractFromHeadersDoesNotMixTraceLineages(t *testing.T) {
	const (
		parentA = "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-1111111111111111-01"
		traceA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		stateA  = "vendorA=alpha"
		parentB = "00-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-2222222222222222-01"
		traceB  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)

	tests := []struct {
		name       string
		headers    map[string]any
		wantID     string
		wantParent string
		wantState  string
	}{
		{
			// The reported case: carrier brings a new parent and no usable id, so
			// the carrier's trace wins outright rather than being half-adopted.
			name:       "carrier_parent_with_unusable_request_id",
			headers:    map[string]any{HeaderTraceParent: parentB, HeaderXRequestID: "!!! not valid !!!"},
			wantID:     traceB,
			wantParent: parentB,
		},
		{
			name:       "carrier_parent_with_no_request_id_at_all",
			headers:    map[string]any{HeaderTraceParent: parentB},
			wantID:     traceB,
			wantParent: parentB,
		},
		{
			// A carrier id is the caller's explicit choice and outranks derivation,
			// so it keeps its own id while still adopting the carrier's parent.
			name:       "carrier_supplies_both",
			headers:    map[string]any{HeaderTraceParent: parentB, HeaderXRequestID: "req-from-carrier"},
			wantID:     "req-from-carrier",
			wantParent: parentB,
		},
		{
			// Nothing on the carrier: the inherited trace is still the live one.
			name:       "empty_carrier_inherits_everything",
			headers:    map[string]any{},
			wantID:     traceA,
			wantParent: parentA,
			wantState:  stateA,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithTraceState(WithTraceParent(WithTraceID(context.Background(), traceA), parentA), stateA)

			got := ExtractFromHeaders(ctx, &mapAccessor{m: tt.headers})

			id, _ := IDFromContext(got)
			tp, _ := ParentFromContext(got)
			ts, _ := StateFromContext(got)
			assert.Equal(t, tt.wantID, id, "the trace id must belong to the same trace as the parent")
			assert.Equal(t, tt.wantParent, tp)
			assert.Equal(t, tt.wantState, ts,
				"tracestate annotates ONE parent; it must never survive onto a different carrier's")

			// And the same must hold on the way out — a downstream hop must not
			// receive one trace's parent carrying another's vendor state.
			out := &mapAccessor{m: map[string]any{}}
			InjectIntoHeaders(got, out)
			assert.Equal(t, tt.wantParent, out.m[HeaderTraceParent])
			if tt.wantState == "" {
				assert.NotContains(t, out.m, HeaderTraceState, "no orphan tracestate is emitted")
			}
		})
	}
}
