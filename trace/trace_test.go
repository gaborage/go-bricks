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
	fromHeader := computeTraceParent(context.Background(), acc)
	assert.Equal(t, "11111111111111111111111111111111", fromHeader.traceID)
	assert.Equal(t, "00-11111111111111111111111111111111-2222222222222222-01", fromHeader.value)
	assert.True(t, fromHeader.fromHeader)
	assert.False(t, fromHeader.rejected)

	ctx := WithTraceParent(context.Background(), "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
	fromContext := computeTraceParent(ctx, &mapAccessor{})
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", fromContext.traceID)
	assert.Equal(t, "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01", fromContext.value)
	assert.False(t, fromContext.fromHeader)
	assert.False(t, fromContext.rejected)

	// Fallback generates a valid 00-... string
	generated := computeTraceParent(context.Background(), &mapAccessor{})
	gen := generated.value
	assert.Equal(t, gen[3:35], generated.traceID)
	assert.False(t, generated.fromHeader)
	assert.False(t, generated.rejected)
	ok, _ := regexp.MatchString(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`, gen)
	assert.True(t, ok)
}

func TestHeaderStringAndSafeToString(t *testing.T) {
	acc := &mapAccessor{m: map[string]any{HeaderXRequestID: []byte("bytes-id")}}
	value, carried := headerString(acc, HeaderXRequestID)
	assert.Equal(t, "bytes-id", value)
	assert.True(t, carried)
	value, carried = headerString(&mapAccessor{}, "missing")
	assert.Equal(t, "", value)
	assert.False(t, carried)

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

	// alignTraceID
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", alignTraceID("orig", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	assert.Equal(t, "orig", alignTraceID("orig", ""))
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

// TestInjectIntoHeadersDiscardsMalformedHeaderTraceParent pins the emit-side
// half of ADR-070 (#1121): a traceparent already sitting in the header map is
// caller-supplied — first-party code hand-setting PublishOptions.Headers, or an
// outbox row persisted before the ingress fix — so it is validated like every
// other door. An unusable one falls through to the context value rather than
// being re-emitted.
func TestInjectIntoHeadersDiscardsMalformedHeaderTraceParent(t *testing.T) {
	const contextParent = "00-aaaabbbbccccddddeeeeffff00001111-0011223344556677-01"
	acc := &mapAccessor{m: map[string]any{
		// 32 characters in the trace-id position, none of them hex.
		HeaderTraceParent: "00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-0011223344556677-01",
	}}
	ctx := WithTraceParent(context.Background(), contextParent)

	InjectIntoHeaders(ctx, acc)

	assert.Equal(t, contextParent, acc.Get(HeaderTraceParent))
	assert.Equal(t, "aaaabbbbccccddddeeeeffff00001111", acc.Get(HeaderXRequestID))
}

// TestInjectIntoHeadersDoesNotAlignRequestIDToNonHexTraceID is the belt to the
// braces above (#1121): WithTraceParent is exported, so a context value the
// ingress seam never saw still reaches the alignment. Aligning on length alone
// let 32 arbitrary bytes become the outbound X-Request-ID, which the publish
// side then refuses — shipping an empty CorrelationId. The alignment now
// requires the traceparent charset, so an unusable parent leaves the context's
// own trace ID in place.
func TestInjectIntoHeadersDoesNotAlignRequestIDToNonHexTraceID(t *testing.T) {
	ctx := WithTraceID(context.Background(), "rid-abc")
	ctx = WithTraceParent(ctx, "00-ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ-0011223344556677-01")
	acc := &mapAccessor{}

	InjectIntoHeaders(ctx, acc)

	assert.Equal(t, "rid-abc", acc.Get(HeaderXRequestID))
}

// TestInjectIntoHeadersDropsTraceStateBesideARejectedTraceParent pins the other
// half of the emit-side rule (#1121): a tracestate is only meaningful for the
// traceparent it was written beside. When that traceparent is refused and the
// context carries no state of its own, the stale state must not ride along with
// the value that replaced it — and with no context parent either, the emitted
// traceparent is a freshly generated one.
func TestInjectIntoHeadersDropsTraceStateBesideARejectedTraceParent(t *testing.T) {
	acc := &mapAccessor{m: map[string]any{
		HeaderTraceParent: "00-not-a-traceparent-at-all-01",
		HeaderTraceState:  "vendor=stale",
	}}

	InjectIntoHeaders(context.Background(), acc)

	emitted, ok := acc.Get(HeaderTraceParent).(string)
	require.True(t, ok)
	assert.Equal(t, emitted, ValidateTraceParent(emitted), "the emitted traceparent is well-formed")
	assert.Empty(t, acc.Get(HeaderTraceState))
}

// TestInjectIntoHeadersKeepsTheTraceStateOfAnAcceptedHeaderParent is the other
// side of the carrier rule (#1121): a tracestate annotates ONE traceparent. When
// the caller's pre-set traceparent is ACCEPTED it is the value going out, so the
// tracestate written beside it stays — the context's state belongs to a
// different parent and overwriting with it would re-emit one trace's vendor
// state under another's, which is what ADR-070 refuses on ingress.
func TestInjectIntoHeadersKeepsTheTraceStateOfAnAcceptedHeaderParent(t *testing.T) {
	const headerParent = "00-11111111111111111111111111111111-2222222222222222-01"
	acc := &mapAccessor{m: map[string]any{
		HeaderTraceParent: headerParent,
		HeaderTraceState:  "vendor=carrier",
	}}
	ctx := WithTraceParent(context.Background(), "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
	ctx = WithTraceState(ctx, "vendor=context")

	InjectIntoHeaders(ctx, acc)

	assert.Equal(t, headerParent, acc.Get(HeaderTraceParent), "a valid pre-set parent still wins")
	assert.Equal(t, "vendor=carrier", acc.Get(HeaderTraceState))
}

// TestInjectIntoHeadersDropsTraceStateBesideAnEmptyHeaderTraceParent covers the
// carrier rule's third shape: a header map that carries the traceparent key with
// an EMPTY value still carried it, so a tracestate written beside it annotates a
// parent this hop is not continuing. Presence, not emptiness, decides.
func TestInjectIntoHeadersDropsTraceStateBesideAnEmptyHeaderTraceParent(t *testing.T) {
	acc := &mapAccessor{m: map[string]any{
		HeaderTraceParent: "",
		HeaderTraceState:  "vendor=stale",
	}}

	InjectIntoHeaders(context.Background(), acc)

	emitted, ok := acc.Get(HeaderTraceParent).(string)
	require.True(t, ok)
	assert.Equal(t, emitted, ValidateTraceParent(emitted))
	assert.Empty(t, acc.Get(HeaderTraceState))
}
