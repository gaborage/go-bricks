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
