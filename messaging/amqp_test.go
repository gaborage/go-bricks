package messaging

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/logger"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// AMQP Trace Injection Tests
// =============================================================================

func TestAMQPTraceInjection_GeneratesDefaults(t *testing.T) {
	pub := amqp.Publishing{Headers: amqp.Table{}}

	ctx := context.Background()
	// Use centralized trace injection
	accessor := &amqpHeaderAccessor{headers: pub.Headers}
	gobrickstrace.InjectIntoHeaders(ctx, accessor)

	// X-Request-ID present and non-empty
	xid, ok := pub.Headers[gobrickstrace.HeaderXRequestID]
	require.True(t, ok)
	xidStr, ok := xid.(string)
	require.True(t, ok)
	assert.NotEmpty(t, xidStr)

	// traceparent present and looks valid-ish
	tp, ok := pub.Headers[gobrickstrace.HeaderTraceParent]
	require.True(t, ok)
	tpStr, ok := tp.(string)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(tpStr, "00-"))
	assert.GreaterOrEqual(t, len(tpStr), 55)

	// tracestate may be empty if not in context
	_, existsTS := pub.Headers[gobrickstrace.HeaderTraceState]
	assert.False(t, existsTS)
}

func TestAMQPTraceInjection_ForceAlignment(t *testing.T) {
	pub := amqp.Publishing{Headers: amqp.Table{
		gobrickstrace.HeaderXRequestID:  "preexisting-xid",
		gobrickstrace.HeaderTraceParent: "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01",
		gobrickstrace.HeaderTraceState:  "vendor=foo",
	}}

	ctx := gobrickstrace.WithTraceID(context.Background(), "ctx-trace")
	// Use centralized trace injection (force mode will align trace ID with traceparent)
	accessor := &amqpHeaderAccessor{headers: pub.Headers}
	gobrickstrace.InjectIntoHeaders(ctx, accessor)

	// With force mode, trace ID should be aligned with traceparent
	assert.Equal(t, "0123456789abcdef0123456789abcdef", pub.Headers[gobrickstrace.HeaderXRequestID])
	assert.Equal(t, "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01", pub.Headers[gobrickstrace.HeaderTraceParent])
	assert.Equal(t, "vendor=foo", pub.Headers[gobrickstrace.HeaderTraceState])
}

func TestAMQPTraceInjection_PropagatesFromContext(t *testing.T) {
	pub := amqp.Publishing{Headers: amqp.Table{}}

	ctx := gobrickstrace.WithTraceParent(context.Background(), "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
	// Use centralized trace injection
	accessor := &amqpHeaderAccessor{headers: pub.Headers}
	gobrickstrace.InjectIntoHeaders(ctx, accessor)

	tp, ok := pub.Headers[gobrickstrace.HeaderTraceParent]
	require.True(t, ok)
	assert.Equal(t, "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01", tp)
}

// =============================================================================
// AMQP Trace Extraction Tests
// =============================================================================

func TestAMQPTraceExtraction_DerivesFromHeaders(t *testing.T) {
	base := context.Background()
	delivery := &amqp.Delivery{Headers: amqp.Table{
		gobrickstrace.HeaderTraceParent: "00-deadbeefdeadbeefdeadbeefdeadbeef-0123456789abcdef-01",
		// No X-Request-ID to force derive from traceparent
	}}

	// Use centralized trace extraction
	accessor := &amqpDeliveryAccessor{headers: delivery.Headers}
	ctx := gobrickstrace.ExtractFromHeaders(base, accessor)

	// Should have traceparent in context
	tp, ok := gobrickstrace.ParentFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "00-deadbeefdeadbeefdeadbeefdeadbeef-0123456789abcdef-01", tp)

	// Should derive trace ID from traceparent's trace-id part when X-Request-ID missing
	tid, ok := gobrickstrace.IDFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "deadbeefdeadbeefdeadbeefdeadbeef", tid)
}

func TestAMQPTraceExtraction_ByteSliceHeaders(t *testing.T) {
	base := context.Background()

	// Test with []byte headers in AMQP delivery
	delivery := &amqp.Delivery{Headers: amqp.Table{
		gobrickstrace.HeaderXRequestID:  []byte("byte-request-id"),
		gobrickstrace.HeaderTraceParent: []byte("00-ffeeddccbbaa9988ffeeddccbbaa9988-9988776655443322-01"),
		gobrickstrace.HeaderTraceState:  []byte("vendor=test"),
	}}

	// Use centralized trace extraction
	accessor := &amqpDeliveryAccessor{headers: delivery.Headers}
	ctx := gobrickstrace.ExtractFromHeaders(base, accessor)

	// Should successfully extract trace information from byte slice headers
	tid, ok := gobrickstrace.IDFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "byte-request-id", tid)

	tp, ok := gobrickstrace.ParentFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "00-ffeeddccbbaa9988ffeeddccbbaa9988-9988776655443322-01", tp)

	ts, ok := gobrickstrace.StateFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "vendor=test", ts)
}

// =============================================================================
// AMQP Header Hardening Tests
// =============================================================================

func TestAMQPHeaderHardening_VariousTypes(t *testing.T) {
	// Test various header value types that could cause runtime issues
	pub := amqp.Publishing{
		Headers: amqp.Table{
			// Different types that the hardened version should safely handle
			gobrickstrace.HeaderXRequestID:  []byte("byte-array-trace-id"),
			gobrickstrace.HeaderTraceParent: "00-abcdefabcdefabcdefabcdefabcdefab-1234567890123456-01",
			"custom-header":                 42,       // integer
			"another-header":                true,     // boolean
			"nil-header":                    nil,      // nil value
			"empty-byte-header":             []byte{}, // empty byte slice
		},
	}

	ctx := gobrickstrace.WithTraceID(context.Background(), "context-trace-id")

	// Test the hardened header injection directly
	accessor := &amqpHeaderAccessor{headers: pub.Headers}
	gobrickstrace.InjectIntoHeaders(ctx, accessor)

	// AMQP-specific: simulate CorrelationId and unique MessageId semantics
	traceID := gobrickstrace.EnsureTraceID(ctx)
	if pub.CorrelationId == "" {
		pub.CorrelationId = traceID
	}
	if pub.MessageId == "" {
		pub.MessageId = uuid.New().String()
	}

	// Verify that force alignment worked correctly
	// The trace ID should be aligned with the traceparent trace-id field
	assert.Equal(t, "abcdefabcdefabcdefabcdefabcdefab", pub.Headers[gobrickstrace.HeaderXRequestID])
	assert.Equal(t, "00-abcdefabcdefabcdefabcdefabcdefab-1234567890123456-01", pub.Headers[gobrickstrace.HeaderTraceParent])

	// AMQP-specific fields: CorrelationId uses context trace ID; MessageId is unique
	assert.Equal(t, "context-trace-id", pub.CorrelationId)
	assert.NotEmpty(t, pub.MessageId)
	assert.NotEqual(t, pub.CorrelationId, pub.MessageId)
}

func TestAMQPHeaderAccessor_NilSafety(t *testing.T) {
	// Given a nil headers map, Set should initialize it and Get should work
	accessor := &amqpHeaderAccessor{headers: nil}
	gobrickstrace.InjectIntoHeaders(context.Background(), accessor)
	// Should not panic and should have set headers
	xid := accessor.Get(gobrickstrace.HeaderXRequestID)
	require.NotNil(t, xid)
	assert.NotEmpty(t, gobrickstrace.EnsureTraceID(context.Background()))
}

func TestAMQPHeaderHardening_SafeConsumption(t *testing.T) {
	// Test consuming messages with various header value types
	delivery := &amqp.Delivery{
		Headers: amqp.Table{
			// Different types that could cause runtime panics in unsafe implementations
			gobrickstrace.HeaderXRequestID:  []byte("consumed-byte-id"), // []byte header
			gobrickstrace.HeaderTraceParent: "00-deadbeefdeadbeefdeadbeefdeadbeef-fedcba9876543210-01",
			gobrickstrace.HeaderTraceState:  []byte("vendor=test-system"), // []byte tracestate
			"int-value":                     123,                          // integer value
			"float-value":                   45.67,                        // float value
			"bool-value":                    false,                        // boolean value
		},
	}

	// Test safe extraction through centralized trace functions
	accessor := &amqpDeliveryAccessor{headers: delivery.Headers}
	ctx := gobrickstrace.ExtractFromHeaders(context.Background(), accessor)

	// Verify that all header types were safely processed
	traceID, ok := gobrickstrace.IDFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "consumed-byte-id", traceID)

	traceparent, ok := gobrickstrace.ParentFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "00-deadbeefdeadbeefdeadbeefdeadbeef-fedcba9876543210-01", traceparent)

	tracestate, ok := gobrickstrace.StateFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "vendor=test-system", tracestate)
}

func TestAMQPHeaderHardening_ByteSliceInjection(t *testing.T) {
	// Test with []byte headers that should be safely converted
	pub := amqp.Publishing{Headers: amqp.Table{
		gobrickstrace.HeaderXRequestID:  []byte("byte-trace-id"),
		gobrickstrace.HeaderTraceParent: []byte("00-aabbccddeeffaabbccddeeffaabbccdd-1122334455667788-01"),
	}}

	ctx := context.Background()
	// Use centralized trace injection (force mode)
	accessor := &amqpHeaderAccessor{headers: pub.Headers}
	gobrickstrace.InjectIntoHeaders(ctx, accessor)

	// Force mode should align trace ID with traceparent
	assert.Equal(t, "aabbccddeeffaabbccddeeffaabbccdd", pub.Headers[gobrickstrace.HeaderXRequestID])
	assert.Equal(t, "00-aabbccddeeffaabbccddeeffaabbccdd-1122334455667788-01", pub.Headers[gobrickstrace.HeaderTraceParent])
}

// =============================================================================
// AMQP Force Alignment Tests
// =============================================================================

func TestAMQPForceAlignment_Consistency(t *testing.T) {
	// Demonstrate that trace IDs are consistently aligned with traceparent
	testCases := []struct {
		name            string
		initialTraceID  any // Could be []byte or string
		traceparent     any // Could be []byte or string
		expectedTraceID string
	}{
		{
			name:            "String headers with UUID trace ID",
			initialTraceID:  "550e8400-e29b-41d4-a716-446655440000",
			traceparent:     "00-fedcbafedcbafedcbafedcbafedcbaaa-1111222233334444-01",
			expectedTraceID: "fedcbafedcbafedcbafedcbafedcbaaa",
		},
		{
			name:            "Byte slice headers",
			initialTraceID:  []byte("byte-trace-id"),
			traceparent:     []byte("00-1a2b3c4d5e6f7a8b1a2b3c4d5e6f7a8b-9876543210abcdef-01"),
			expectedTraceID: "1a2b3c4d5e6f7a8b1a2b3c4d5e6f7a8b",
		},
		{
			name:            "Mixed types force alignment",
			initialTraceID:  "ffffffffffffffffffffffffffffffff", // 32-char hex
			traceparent:     []byte("00-1234567890abcdef1234567890abcdef-0123456789abcdef-01"),
			expectedTraceID: "1234567890abcdef1234567890abcdef", // Force mode always aligns
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pub := amqp.Publishing{
				Headers: amqp.Table{
					gobrickstrace.HeaderXRequestID:  tc.initialTraceID,
					gobrickstrace.HeaderTraceParent: tc.traceparent,
				},
			}

			ctx := context.Background()
			accessor := &amqpHeaderAccessor{headers: pub.Headers}
			gobrickstrace.InjectIntoHeaders(ctx, accessor)

			// Verify force alignment worked as expected
			assert.Equal(t, tc.expectedTraceID, pub.Headers[gobrickstrace.HeaderXRequestID])
		})
	}
}

// =============================================================================
// AMQP Centralized Architecture Tests
// =============================================================================

func TestAMQPCentralizedArchitecture_ExtractAndInject(t *testing.T) {
	// Test that centralized trace functions work correctly with AMQP headers

	// Test injection
	pub := amqp.Publishing{Headers: amqp.Table{}}
	ctx := gobrickstrace.WithTraceID(context.Background(), "test-trace-id")

	accessor := &amqpHeaderAccessor{headers: pub.Headers}
	gobrickstrace.InjectIntoHeaders(ctx, accessor)

	// Should have injected headers
	assert.NotEmpty(t, pub.Headers[gobrickstrace.HeaderXRequestID])
	assert.NotEmpty(t, pub.Headers[gobrickstrace.HeaderTraceParent])

	// Test extraction
	delivery := &amqp.Delivery{Headers: amqp.Table{
		gobrickstrace.HeaderXRequestID:  "extracted-id",
		gobrickstrace.HeaderTraceParent: "00-ffeeddccbbaa9988ffeeddccbbaa9988-1122334455667788-01",
	}}

	deliveryAccessor := &amqpDeliveryAccessor{headers: delivery.Headers}
	extractedCtx := gobrickstrace.ExtractFromHeaders(context.Background(), deliveryAccessor)

	traceID, ok := gobrickstrace.IDFromContext(extractedCtx)
	require.True(t, ok)
	assert.Equal(t, "extracted-id", traceID)

	traceparent, ok := gobrickstrace.ParentFromContext(extractedCtx)
	require.True(t, ok)
	assert.Equal(t, "00-ffeeddccbbaa9988ffeeddccbbaa9988-1122334455667788-01", traceparent)
}

func TestAMQPCentralizedArchitecture_ConsistentProcessing(t *testing.T) {
	// Verify that both AMQP client and registry use the same centralized logic
	headers := amqp.Table{
		gobrickstrace.HeaderXRequestID:  []byte("consistency-test-id"),
		gobrickstrace.HeaderTraceParent: []byte("00-abcdef1234567890abcdef1234567890-fedcba0987654321-01"),
		gobrickstrace.HeaderTraceState:  "vendor=consistency",
	}

	// Test client injection
	pubAccessor := &amqpHeaderAccessor{headers: make(amqp.Table)}
	gobrickstrace.InjectIntoHeaders(context.Background(), pubAccessor)

	// Test registry extraction
	deliveryAccessor := &amqpDeliveryAccessor{headers: headers}
	ctx := gobrickstrace.ExtractFromHeaders(context.Background(), deliveryAccessor)

	// Both should use the same safe string conversion and logic
	traceID, ok := gobrickstrace.IDFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "consistency-test-id", traceID)

	// Verify generated headers follow the same patterns
	assert.NotEmpty(t, pubAccessor.headers[gobrickstrace.HeaderXRequestID])
	assert.NotEmpty(t, pubAccessor.headers[gobrickstrace.HeaderTraceParent])
}

// =============================================================================
// Registry AutoAck Guard Tests
// =============================================================================

// stubLogger implements logger.Logger for testing log message capture
type stubLogger struct {
	entries []string
	mu      sync.RWMutex
}

// reset clears all log entries safely
func (l *stubLogger) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = nil
}

// getEntries returns a copy of the entries safely
func (l *stubLogger) getEntries() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	entriesCopy := make([]string, len(l.entries))
	copy(entriesCopy, l.entries)
	return entriesCopy
}

func (l *stubLogger) Info() logger.LogEvent                     { return &stubEvent{l} }
func (l *stubLogger) Error() logger.LogEvent                    { return &stubEvent{l} }
func (l *stubLogger) Debug() logger.LogEvent                    { return &stubEvent{l} }
func (l *stubLogger) Warn() logger.LogEvent                     { return &stubEvent{l} }
func (l *stubLogger) Fatal() logger.LogEvent                    { return &stubEvent{l} }
func (l *stubLogger) WithContext(_ any) logger.Logger           { return l }
func (l *stubLogger) WithFields(_ map[string]any) logger.Logger { return l }

type stubEvent struct{ l *stubLogger }

func (e *stubEvent) Msg(msg string) {
	e.l.mu.Lock()
	defer e.l.mu.Unlock()
	e.l.entries = append(e.l.entries, msg)
}
func (e *stubEvent) Msgf(format string, args ...any) {
	e.l.mu.Lock()
	defer e.l.mu.Unlock()
	e.l.entries = append(e.l.entries, fmt.Sprintf(format, args...))
}
func (e *stubEvent) Err(_ error) logger.LogEvent                   { return e }
func (e *stubEvent) Str(_, _ string) logger.LogEvent               { return e }
func (e *stubEvent) Int(_ string, _ int) logger.LogEvent           { return e }
func (e *stubEvent) Int64(_ string, _ int64) logger.LogEvent       { return e }
func (e *stubEvent) Uint64(_ string, _ uint64) logger.LogEvent     { return e }
func (e *stubEvent) Dur(_ string, _ time.Duration) logger.LogEvent { return e }
func (e *stubEvent) Interface(_ string, _ any) logger.LogEvent     { return e }
func (e *stubEvent) Bytes(_ string, _ []byte) logger.LogEvent      { return e }

// testHandler is a MessageHandler that returns a configured error
type testHandler struct{ retErr error }

func (h *testHandler) Handle(_ context.Context, _ *amqp.Delivery) error { return h.retErr }
func (h *testHandler) EventType() string                                { return "test" }

// Test helper functions for robust log message checking
func containsAckFailure(entries []string) bool {
	for _, entry := range entries {
		if strings.Contains(entry, "Failed to ack") {
			return true
		}
	}
	return false
}

func containsNackFailure(entries []string) bool {
	for _, entry := range entries {
		if strings.Contains(entry, "Failed to nack") {
			return true
		}
	}
	return false
}

func TestRegistry_ProcessMessage_AutoAckGuard(t *testing.T) {
	// Set up a registry with stub logger
	l := &stubLogger{}
	reg := &Registry{logger: l}

	// Delivery without initialized channel will cause Ack/Nack to return error if called
	delivery := amqp.Delivery{}

	t.Run("AutoAck=true success does not ack", func(t *testing.T) {
		l.reset()
		cons := &ConsumerDeclaration{AutoAck: true, Handler: &testHandler{retErr: nil}}
		reg.processMessage(context.Background(), cons, &delivery, l)
		// Should not attempt ack; check absence of ack error log message
		entries := l.getEntries()
		assert.False(t, containsAckFailure(entries), "Expected no ack failure messages")
	})

	t.Run("AutoAck=true error does not nack", func(t *testing.T) {
		l.reset()
		cons := &ConsumerDeclaration{AutoAck: true, Handler: &testHandler{retErr: assert.AnError}}
		reg.processMessage(context.Background(), cons, &delivery, l)
		// Should not attempt nack; check absence of nack error log message
		entries := l.getEntries()
		assert.False(t, containsNackFailure(entries), "Expected no nack failure messages")
	})

	t.Run("AutoAck=false success acks (may error)", func(t *testing.T) {
		l.reset()
		cons := &ConsumerDeclaration{AutoAck: false, Handler: &testHandler{retErr: nil}}
		reg.processMessage(context.Background(), cons, &delivery, l)
		// With uninitialized delivery, ack will likely fail, so expect log message
		entries := l.getEntries()
		assert.True(t, containsAckFailure(entries), "Expected ack failure message")
	})

	t.Run("AutoAck=false error nacks (may error)", func(t *testing.T) {
		l.reset()
		cons := &ConsumerDeclaration{AutoAck: false, Handler: &testHandler{retErr: assert.AnError}}
		reg.processMessage(context.Background(), cons, &delivery, l)
		entries := l.getEntries()
		assert.True(t, containsNackFailure(entries), "Expected nack failure message")
	})
}
