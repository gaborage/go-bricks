package trace

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

// derivedFromValidParent is the trace-id field of validParent, which is what a
// rejected X-Request-ID must fall through to.
const derivedFromValidParent = "4bf92f3577b34da6a3ce929d0e0e4736"

func TestValidateRequestIDRejectsWhatCannotBeCorrelated(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{name: "plain", id: "rid-123", want: "rid-123"},
		{name: "uuid", id: "0b3e1d2c-4f5a-6789-abcd-ef0123456789", want: "0b3e1d2c-4f5a-6789-abcd-ef0123456789"},
		{name: "underscores", id: "svc_1", want: "svc_1"},
		{name: "at_the_limit", id: strings.Repeat("a", 128), want: strings.Repeat("a", 128)},
		{name: "one_over_the_limit", id: strings.Repeat("a", 129)},
		// 256 bytes is the size that tears down the shared AMQP connection.
		{name: "over_the_amqp_shortstr_limit", id: strings.Repeat("a", 256)},
		{name: "empty", id: ""},
		{name: "newline", id: "rid\n123"},
		{name: "quote", id: `rid"123`},
		{name: "space", id: "rid 123"},
		{name: "colon_and_brackets_from_a_coerced_table", id: "map[k:v]"},
		{name: "non_ascii", id: "rid-ünïcode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ValidateRequestID(tt.id))
		})
	}
}

func TestValidateTraceParentIsSpecExact(t *testing.T) {
	tests := []struct {
		name string
		tp   string
		want string
	}{
		{name: "valid", tp: validParent, want: validParent},
		{name: "uppercase_hex", tp: "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01"},
		// 32 non-hex characters passed the old length-only check, which left an
		// attacker 32 arbitrary bytes inside a "valid" traceparent.
		{name: "non_hex_trace_id", tp: "00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-00f067aa0ba902b7-01"},
		{name: "all_zero_trace_id", tp: "00-" + zeroTraceID + "-00f067aa0ba902b7-01"},
		{name: "all_zero_parent_id", tp: "00-4bf92f3577b34da6a3ce929d0e0e4736-" + zeroParentID + "-01"},
		// The spec forbids version ff outright rather than reserving it.
		{name: "forbidden_version_ff", tp: "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "future_version_is_accepted", tp: "fe-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			want: "fe-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "too_short", tp: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7"},
		{name: "trailing_junk", tp: validParent + "-extra"},
		{name: "empty", tp: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ValidateTraceParent(tt.tp))
		})
	}
}

// A rejected X-Request-ID must not merely be absent — the framework has to
// substitute something correlatable. Asserting "no id" would pass for a rejected
// id AND for one that was never planted, so these assert what won instead.
func TestExtractFromHeadersSubstitutesWhenTheRequestIDIsRejected(t *testing.T) {
	oversized := strings.Repeat("a", 256)

	t.Run("falls_through_to_the_traceparent_derived_id", func(t *testing.T) {
		acc := &mapAccessor{m: map[string]any{
			HeaderXRequestID:  oversized,
			HeaderTraceParent: validParent,
		}}

		id, ok := IDFromContext(ExtractFromHeaders(context.Background(), acc))

		require.True(t, ok, "a rejected id must not leave the delivery uncorrelated")
		assert.Equal(t, derivedFromValidParent, id, "the traceparent-derived id wins")
	})

	t.Run("falls_through_to_a_fresh_uuid", func(t *testing.T) {
		acc := &mapAccessor{m: map[string]any{HeaderXRequestID: oversized}}

		ctx := ExtractFromHeaders(context.Background(), acc)
		_, planted := IDFromContext(ctx)
		minted := EnsureTraceID(ctx)

		assert.False(t, planted, "the rejected value is not stored")
		assert.NotEqual(t, oversized, minted)
		assert.Equal(t, minted, ValidateRequestID(minted), "the substitute is itself valid")
	})
}

// safeToString renders any AMQP field type, so validation has to run after
// coercion — the charset then rejects the renderings naturally.
func TestExtractFromHeadersRejectsCoercedNonStringValues(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "oversized_bytes", value: []byte(strings.Repeat("a", 256))},
		{name: "nested_table_renders_with_brackets", value: map[string]any{"k": "v"}},
		{name: "bytes_with_a_newline", value: []byte("rid\n123")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := &mapAccessor{m: map[string]any{HeaderXRequestID: tt.value}}

			_, planted := IDFromContext(ExtractFromHeaders(context.Background(), acc))

			assert.False(t, planted, "a coerced value that fails the charset is discarded")
		})
	}

	// An int coerces to digits, which the charset ACCEPTS — recorded so the
	// behavior is deliberate rather than assumed.
	acc := &mapAccessor{m: map[string]any{HeaderXRequestID: 42}}
	id, planted := IDFromContext(ExtractFromHeaders(context.Background(), acc))
	assert.True(t, planted)
	assert.Equal(t, "42", id)
}

func TestExtractFromHeadersDropsAMalformedTraceParentEntirely(t *testing.T) {
	acc := &mapAccessor{m: map[string]any{
		HeaderTraceParent: "00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-00f067aa0ba902b7-01",
	}}

	ctx := ExtractFromHeaders(context.Background(), acc)

	_, storedParent := ParentFromContext(ctx)
	_, derivedID := IDFromContext(ctx)
	assert.False(t, storedParent, "a malformed traceparent is not stored, so it is not re-emitted")
	assert.False(t, derivedID, "and nothing is derived from it")
}

func TestExtractFromHeadersCapsTraceState(t *testing.T) {
	tests := []struct {
		name       string
		state      string
		parent     string
		wantStored bool
	}{
		{name: "at_the_cap", state: strings.Repeat("a", MaxTraceStateBytes), parent: validParent, wantStored: true},
		{name: "one_over_the_cap", state: strings.Repeat("a", MaxTraceStateBytes+1), parent: validParent},
		// tracestate is meaningless without the traceparent it accompanies:
		// storing it would re-emit it attached to a trace it never belonged to.
		{name: "no_traceparent_at_all", state: "vendor=a:b"},
		{name: "malformed_traceparent", state: "vendor=a:b", parent: "00-zzzz-00f067aa0ba902b7-01"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := map[string]any{HeaderTraceState: tt.state}
			if tt.parent != "" {
				headers[HeaderTraceParent] = tt.parent
			}
			acc := &mapAccessor{m: headers}

			_, stored := StateFromContext(ExtractFromHeaders(context.Background(), acc))

			assert.Equal(t, tt.wantStored, stored)
		})
	}
}

// The whole point of the bound: an oversized id must never reach a value
// amqp091 would refuse to write, because it answers a frame-write error by
// tearing down the shared Connection.
func TestAnOversizedIDNeverSurvivesIntoAnOutboundHeader(t *testing.T) {
	oversized := strings.Repeat("a", 256)
	in := &mapAccessor{m: map[string]any{HeaderXRequestID: oversized}}

	ctx := ExtractFromHeaders(context.Background(), in)
	out := &mapAccessor{m: map[string]any{}}
	InjectIntoHeaders(ctx, out)

	emitted := safeToString(out.Get(HeaderXRequestID))
	assert.NotEqual(t, oversized, emitted)
	assert.LessOrEqual(t, len(emitted), 255, "amqp091 refuses a shortstr over 255 bytes")
	assert.Equal(t, emitted, ValidateRequestID(emitted), "what is re-emitted is itself valid")
}
