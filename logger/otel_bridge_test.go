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
