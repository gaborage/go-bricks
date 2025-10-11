package logger

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

// TestOTelBridge_ValidJSON tests parsing of valid zerolog JSON output
func TestOTelBridgeValidJSON(t *testing.T) {
	provider := sdklog.NewLoggerProvider()
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	bridge := NewOTelBridge(provider)
	require.NotNil(t, bridge)

	// Simulate zerolog JSON output
	zerologJSON := `{"level":"info","time":"2025-10-10T12:00:00.123456789Z","message":"User logged in","user_id":"123","method":"POST"}`

	n, err := bridge.Write([]byte(zerologJSON))
	require.NoError(t, err)
	assert.Equal(t, len(zerologJSON), n, "Should return full byte count")

	// NOTE: Without an in-memory exporter, we can't verify the actual records
	// This test verifies that the bridge doesn't panic and processes JSON correctly
}

// TestOTelBridge_MalformedJSON tests handling of malformed JSON input
func TestOTelBridgeMalformedJSON(t *testing.T) {
	provider := sdklog.NewLoggerProvider()
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	bridge := NewOTelBridge(provider)

	malformedJSON := `{"level":"info","unclosed`

	// Should not panic, should return full byte count
	n, err := bridge.Write([]byte(malformedJSON))
	require.NoError(t, err)
	assert.Equal(t, len(malformedJSON), n)
}

// TestMapZerologLevelToOTel tests all severity level mappings
func TestMapZerologLevelToOTel(t *testing.T) {
	tests := []struct {
		zerologLevel string
		expected     log.Severity
	}{
		{"trace", log.SeverityTrace},  // 1
		{"debug", log.SeverityDebug},  // 5
		{"info", log.SeverityInfo},    // 9
		{"warn", log.SeverityWarn},    // 13
		{"warning", log.SeverityWarn}, // 13 (alternative)
		{"error", log.SeverityError},  // 17
		{"fatal", log.SeverityFatal},  // 21
		{"panic", log.SeverityFatal},  // 21
		{"unknown", log.SeverityInfo}, // Default to Info
	}

	for _, tt := range tests {
		t.Run(tt.zerologLevel, func(t *testing.T) {
			result := mapZerologLevelToOTel(tt.zerologLevel)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestOTelBridge_NilProvider tests graceful handling of nil provider
func TestOTelBridgeNilProvider(t *testing.T) {
	bridge := NewOTelBridge(nil)
	assert.Nil(t, bridge, "NewOTelBridge should return nil for nil provider")
}

// TestOTelBridge_WriterContract tests that io.Writer contract is satisfied
func TestOTelBridgeWriterContract(t *testing.T) {
	provider := sdklog.NewLoggerProvider()
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	bridge := NewOTelBridge(provider)

	// io.Writer must return (n, err) where n is the number of bytes consumed
	input := []byte(`{"level":"info","message":"test"}`)
	n, err := bridge.Write(input)

	require.NoError(t, err)
	assert.Equal(t, len(input), n, "Write must return the full byte count")
}

func TestBuildLogRecordWithTraceFields(t *testing.T) {
	entry := map[string]any{
		"trace_id": "0123456789abcdef0123456789abcdef",
		"span_id":  "0123456789abcdef",
		"message":  "hello",
	}

	_, ctx := buildLogRecord(entry)

	spanCtx := trace.SpanContextFromContext(ctx)
	require.True(t, spanCtx.IsValid(), "span context should be valid when trace_id/span_id are present")
	assert.Equal(t, "0123456789abcdef0123456789abcdef", spanCtx.TraceID().String())
	assert.Equal(t, "0123456789abcdef", spanCtx.SpanID().String())
}

func TestBuildLogRecordWithTraceParentFallback(t *testing.T) {
	entry := map[string]any{
		"traceparent": "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01",
		"message":     "hello",
	}

	_, ctx := buildLogRecord(entry)

	spanCtx := trace.SpanContextFromContext(ctx)
	require.True(t, spanCtx.IsValid(), "span context should be derived from traceparent")
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", spanCtx.TraceID().String())
	assert.Equal(t, "bbbbbbbbbbbbbbbb", spanCtx.SpanID().String())
	assert.Equal(t, trace.TraceFlags(0x01), spanCtx.TraceFlags())
}

func TestBuildLogRecordAddsTraceAttributes(t *testing.T) {
	entry := map[string]any{
		"trace_id":    "0123456789abcdef0123456789abcdef",
		"span_id":     "fedcba9876543210",
		"trace_flags": "1",
		"message":     "test message",
		"level":       "info",
	}

	rec, ctx := buildLogRecord(entry)

	// Verify context has span context
	spanCtx := trace.SpanContextFromContext(ctx)
	require.True(t, spanCtx.IsValid(), "span context should be populated")

	// Verify trace attributes are added to the record
	var foundTraceID, foundSpanID, foundTraceFlags bool
	var traceIDValue, spanIDValue string
	var traceFlagsValue int64

	rec.WalkAttributes(func(kv log.KeyValue) bool {
		switch kv.Key {
		case "trace_id":
			if kv.Value.Kind() == log.KindString {
				traceIDValue = kv.Value.AsString()
				foundTraceID = true
			}
		case "span_id":
			if kv.Value.Kind() == log.KindString {
				spanIDValue = kv.Value.AsString()
				foundSpanID = true
			}
		case "trace_flags":
			if kv.Value.Kind() == log.KindInt64 {
				traceFlagsValue = kv.Value.AsInt64()
				foundTraceFlags = true
			}
		}
		return true // Continue iteration
	})

	assert.True(t, foundTraceID, "trace_id attribute should be present")
	assert.True(t, foundSpanID, "span_id attribute should be present")
	assert.True(t, foundTraceFlags, "trace_flags attribute should be present")

	assert.Equal(t, "0123456789abcdef0123456789abcdef", traceIDValue)
	assert.Equal(t, "fedcba9876543210", spanIDValue)
	assert.Equal(t, int64(1), traceFlagsValue)
}

func TestBuildLogRecordWithoutTraceContext(t *testing.T) {
	entry := map[string]any{
		"message": "test message without trace",
		"level":   "info",
	}

	rec, ctx := buildLogRecord(entry)

	// Verify context has no span context
	spanCtx := trace.SpanContextFromContext(ctx)
	assert.False(t, spanCtx.IsValid(), "span context should not be present")

	// Verify no trace attributes are added
	var foundTraceAttr bool
	rec.WalkAttributes(func(kv log.KeyValue) bool {
		if kv.Key == "trace_id" || kv.Key == "span_id" || kv.Key == "trace_flags" {
			foundTraceAttr = true
			return false // Stop iteration
		}
		return true
	})

	assert.False(t, foundTraceAttr, "no trace attributes should be added when trace context is absent")
}
