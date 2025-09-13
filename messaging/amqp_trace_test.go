package messaging

import (
	"context"
	"strings"
	"testing"

	gobrickshttp "github.com/gaborage/go-bricks/http"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInjectTraceHeaders_GeneratesDefaults(t *testing.T) {
	c := &AMQPClientImpl{}
	pub := amqp.Publishing{Headers: amqp.Table{}}

	ctx := context.Background()
	c.injectTraceHeaders(ctx, &pub)

	// X-Request-ID present and non-empty
	xid, ok := pub.Headers[gobrickshttp.HeaderXRequestID]
	require.True(t, ok)
	xidStr, ok := xid.(string)
	require.True(t, ok)
	assert.NotEmpty(t, xidStr)

	// traceparent present and looks valid-ish
	tp, ok := pub.Headers[gobrickshttp.HeaderTraceParent]
	require.True(t, ok)
	tpStr, ok := tp.(string)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(tpStr, "00-"))
	assert.GreaterOrEqual(t, len(tpStr), 55)

	// tracestate may be empty if not in context
	_, existsTS := pub.Headers[gobrickshttp.HeaderTraceState]
	assert.False(t, existsTS)

	// CorrelationId and MessageId should be set to trace ID
	assert.Equal(t, xidStr, pub.CorrelationId)
	assert.Equal(t, xidStr, pub.MessageId)
}

func TestInjectTraceHeaders_PreservesExistingHeadersAndUsesHeaderForCorrelation(t *testing.T) {
	c := &AMQPClientImpl{}
	pub := amqp.Publishing{Headers: amqp.Table{
		gobrickshttp.HeaderXRequestID:  "preexisting-xid",
		gobrickshttp.HeaderTraceParent: "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01",
		gobrickshttp.HeaderTraceState:  "vendor=foo",
	}}

	ctx := gobrickshttp.WithTraceID(context.Background(), "ctx-trace")
	c.injectTraceHeaders(ctx, &pub)

	// Headers must remain unchanged
	assert.Equal(t, "preexisting-xid", pub.Headers[gobrickshttp.HeaderXRequestID])
	assert.Equal(t, "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01", pub.Headers[gobrickshttp.HeaderTraceParent])
	assert.Equal(t, "vendor=foo", pub.Headers[gobrickshttp.HeaderTraceState])

	// Correlation and Message IDs should follow the X-Request-ID header when set
	assert.Equal(t, "preexisting-xid", pub.CorrelationId)
	assert.Equal(t, "preexisting-xid", pub.MessageId)
}

func TestExtractTraceContext_DerivesFromHeaders(t *testing.T) {
	r := &Registry{}
	base := context.Background()
	delivery := &amqp.Delivery{Headers: amqp.Table{
		gobrickshttp.HeaderTraceParent: "00-deadbeefdeadbeefdeadbeefdeadbeef-0123456789abcdef-01",
		// No X-Request-ID to force derive from traceparent
	}}

	ctx := r.extractTraceContext(base, delivery)

	// Should have traceparent in context
	tp, ok := gobrickshttp.TraceParentFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "00-deadbeefdeadbeefdeadbeefdeadbeef-0123456789abcdef-01", tp)

	// Should derive trace ID from traceparent's trace-id part when X-Request-ID missing
	tid, ok := gobrickshttp.TraceIDFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "deadbeefdeadbeefdeadbeefdeadbeef", tid)
}

func TestInjectTraceHeaders_PropagatesTraceParentFromContext(t *testing.T) {
	c := &AMQPClientImpl{}
	pub := amqp.Publishing{Headers: amqp.Table{}}

	ctx := gobrickshttp.WithTraceParent(context.Background(), "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
	c.injectTraceHeaders(ctx, &pub)

	tp, ok := pub.Headers[gobrickshttp.HeaderTraceParent]
	require.True(t, ok)
	assert.Equal(t, "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01", tp)
}
