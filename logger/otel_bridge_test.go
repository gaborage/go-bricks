package logger

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

// captureProcessor records every emitted log record for assertions.
type captureProcessor struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (p *captureProcessor) OnEmit(_ context.Context, rec *sdklog.Record) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records = append(p.records, rec.Clone())
	return nil
}

//nolint:gocritic // hugeParam: EnabledParameters passed by value per OTel SDK interface contract
func (p *captureProcessor) Enabled(_ context.Context, _ sdklog.EnabledParameters) bool {
	return true
}

func (p *captureProcessor) Shutdown(_ context.Context) error   { return nil }
func (p *captureProcessor) ForceFlush(_ context.Context) error { return nil }

func (p *captureProcessor) snapshot() []sdklog.Record {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]sdklog.Record(nil), p.records...)
}

func newCaptureBridge(t *testing.T) (*OTelBridge, *captureProcessor) {
	t.Helper()
	proc := &captureProcessor{}
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(proc))
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	bridge := NewOTelBridge(provider)
	require.NotNil(t, bridge)
	return bridge, proc
}

// dataRecord returns the first record NOT emitted at WARN severity, so tests
// don't depend on the data-before-WARN emission order.
func dataRecord(records []sdklog.Record) (sdklog.Record, bool) {
	for i := range records {
		if records[i].Severity() != log.SeverityWarn {
			return records[i], true
		}
	}
	return sdklog.Record{}, false
}

// warnRecords filters the records emitted at WARN severity (the remap WARN).
func warnRecords(records []sdklog.Record) []sdklog.Record {
	var warns []sdklog.Record
	for i := range records {
		if records[i].Severity() == log.SeverityWarn {
			warns = append(warns, records[i])
		}
	}
	return warns
}

func recordAttrValue(rec *sdklog.Record, key string) (attribute.Value, bool) {
	var val attribute.Value
	found := false
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		if string(kv.Key) == key {
			val = kv.Value
			found = true
			return false
		}
		return true
	})
	return val, found
}

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

	_, ctx, _ := buildLogRecord(entry)

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

	_, ctx, _ := buildLogRecord(entry)

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

	rec, ctx, _ := buildLogRecord(entry)

	// Verify context has span context
	spanCtx := trace.SpanContextFromContext(ctx)
	require.True(t, spanCtx.IsValid(), "span context should be populated")

	// Verify trace attributes are added to the record
	var foundTraceID, foundSpanID, foundTraceFlags bool
	var traceIDValue, spanIDValue string
	var traceFlagsValue int64

	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		switch kv.Key {
		case "trace_id":
			if kv.Value.Type() == attribute.STRING {
				traceIDValue = kv.Value.AsString()
				foundTraceID = true
			}
		case "span_id":
			if kv.Value.Type() == attribute.STRING {
				spanIDValue = kv.Value.AsString()
				foundSpanID = true
			}
		case "trace_flags":
			if kv.Value.Type() == attribute.INT64 {
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

// TestBuildLogRecordDefaultsLogTypeToTrace pins the default direction of the
// Step 1 presence-only lookup: absent log.type gets defaulted to "trace",
// and a present-but-non-string log.type is left alone (not overwritten) —
// the direction a `.(string)` type assertion would silently break, because
// a failed assertion would (wrongly) look like "absent" and add a second,
// conflicting log.type attribute.
func TestBuildLogRecordDefaultsLogTypeToTrace(t *testing.T) {
	t.Run("absent_log_type_defaults_to_trace", func(t *testing.T) {
		entry := map[string]any{
			"level":   "info",
			"message": "no log.type set",
		}

		rec, _, _ := buildLogRecord(entry)

		var found bool
		var val attribute.Value
		rec.WalkAttributes(func(kv attribute.KeyValue) bool {
			if kv.Key == logTypeAttrKey {
				found = true
				val = kv.Value
				return false
			}
			return true
		})
		require.True(t, found, "log.type attribute should be present")
		assert.Equal(t, "trace", val.AsString())
	})

	t.Run("non_string_log_type_is_not_overwritten", func(t *testing.T) {
		entry := map[string]any{
			"level":    "info",
			"message":  "numeric log.type",
			"log.type": 7,
		}

		rec, _, _ := buildLogRecord(entry)

		var count int
		var lastValue attribute.Value
		rec.WalkAttributes(func(kv attribute.KeyValue) bool {
			if string(kv.Key) == "log.type" {
				count++
				lastValue = kv.Value
			}
			return true
		})

		require.Equal(t, 1, count, "log.type must appear exactly once — a type-assertion mistake would add a second, defaulted occurrence")
		assert.Equal(t, attribute.INT64, lastValue.Type())
		assert.Equal(t, int64(7), lastValue.AsInt64())
	})
}

func TestOTelBridgeReservedKeyRemapPolicy(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		value     string
		wantRemap bool
	}{
		{name: "service_name_remapped", key: "service.name", value: "spoofed-svc", wantRemap: true},
		{name: "service_instance_id_remapped", key: "service.instance.id", value: "spoofed-instance", wantRemap: true},
		{name: "telemetry_sdk_name_remapped", key: "telemetry.sdk.name", value: "spoofed-sdk", wantRemap: true},
		{name: "deployment_environment_name_remapped", key: "deployment.environment.name", value: "spoofed-env", wantRemap: true},
		{name: "bare_service_verbatim", key: "service", value: "v", wantRemap: false},
		{name: "servicex_verbatim", key: "servicex", value: "v", wantRemap: false},
		{name: "services_count_verbatim", key: "services.count", value: "v", wantRemap: false},
		{name: "telemetry_sdkx_verbatim", key: "telemetry.sdkx", value: "v", wantRemap: false},
		{name: "deployment_environment_verbatim", key: "deployment.environment", value: "v", wantRemap: false},
		{name: "deployment_environment_namex_verbatim", key: "deployment.environment.namex", value: "v", wantRemap: false},
		{name: "log_type_stays_caller_settable", key: "log.type", value: "action", wantRemap: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bridge, proc := newCaptureBridge(t)

			line := fmt.Sprintf(`{"level":"info","message":"m",%q:%q}`, tt.key, tt.value)
			_, err := bridge.Write([]byte(line))
			require.NoError(t, err)

			records := proc.snapshot()
			require.NotEmpty(t, records)
			rec, foundData := dataRecord(records)
			require.True(t, foundData, "data record should be present")

			wantKey, forbiddenKey := tt.key, "app."+tt.key
			if tt.wantRemap {
				wantKey, forbiddenKey = "app."+tt.key, tt.key
			}

			val, found := recordAttrValue(&rec, wantKey)
			require.True(t, found, "key %s should be present", wantKey)
			assert.Equal(t, tt.value, val.AsString(), "value must be preserved under %s", wantKey)

			_, present := recordAttrValue(&rec, forbiddenKey)
			assert.False(t, present, "key %s must not be present", forbiddenKey)
		})
	}
}

func TestOTelBridgeWarnsOnceOnReservedKeyRemap(t *testing.T) {
	bridge, proc := newCaptureBridge(t)

	colliding := []byte(`{"level":"info","message":"m","service.name":"evil-svc"}`)
	clean := []byte(`{"level":"info","message":"clean"}`)

	for i := 0; i < 2; i++ {
		_, err := bridge.Write(colliding)
		require.NoError(t, err)
	}
	_, err := bridge.Write(clean)
	require.NoError(t, err)

	records := proc.snapshot()
	require.Len(t, records, 4, "3 data records + exactly 1 WARN")

	warns := warnRecords(records)
	require.Len(t, warns, 1, "remap WARN must fire exactly once per bridge instance")

	warn := warns[0]
	keys, found := recordAttrValue(&warn, "reserved.keys")
	require.True(t, found, "WARN must name the offending keys")
	assert.Contains(t, keys.AsString(), "service.name")
	assert.NotContains(t, keys.AsString(), "evil-svc", "WARN must never carry the field value (bypasses SensitiveDataFilter)")
	assert.NotContains(t, warn.Body().AsString(), "evil-svc")

	logType, found := recordAttrValue(&warn, "log.type")
	require.True(t, found, "WARN must be routable by dual-mode processing")
	assert.Equal(t, "trace", logType.AsString())
}

func TestOTelBridgeRemapWarnTruncatesKeyList(t *testing.T) {
	bridge, proc := newCaptureBridge(t)

	entry := map[string]any{"level": "info", "message": "m"}
	for i := 0; i < 10; i++ {
		entry[fmt.Sprintf("service.padding.%02d.%s", i, strings.Repeat("k", 40))] = "v"
	}
	line, err := json.Marshal(entry)
	require.NoError(t, err)

	_, err = bridge.Write(line)
	require.NoError(t, err)

	warns := warnRecords(proc.snapshot())
	require.Len(t, warns, 1, "remap WARN expected")

	keys, found := recordAttrValue(&warns[0], "reserved.keys")
	require.True(t, found)
	assert.LessOrEqual(t, len(keys.AsString()), maxRemapWarnKeysLen,
		"caller-influenced key list must be length-bounded on the WARN record")
	assert.Contains(t, keys.AsString(), "service.padding.00.")
}

func TestOTelBridgeNoWarnWithoutCollision(t *testing.T) {
	bridge, proc := newCaptureBridge(t)

	_, err := bridge.Write([]byte(`{"level":"info","message":"m","user_id":"123"}`))
	require.NoError(t, err)

	assert.Empty(t, warnRecords(proc.snapshot()), "clean writes must not emit the remap WARN")
}

func TestOTelBridgeConcurrentRemapWarnsOnce(t *testing.T) {
	bridge, proc := newCaptureBridge(t)

	const goroutines = 8
	const writesPerGoroutine = 5

	var wg sync.WaitGroup
	var writeErrs atomic.Int32
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < writesPerGoroutine; i++ {
				if _, err := bridge.Write([]byte(`{"level":"info","message":"m","service.name":"evil"}`)); err != nil {
					writeErrs.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	require.Zero(t, writeErrs.Load(), "no Write may fail under concurrency")

	records := proc.snapshot()
	require.Len(t, records, goroutines*writesPerGoroutine+1, "all data records plus exactly one WARN")
	assert.Len(t, warnRecords(records), 1)
}

func TestBuildLogRecordWithoutTraceContext(t *testing.T) {
	entry := map[string]any{
		"message": "test message without trace",
		"level":   "info",
	}

	rec, ctx, _ := buildLogRecord(entry)

	// Verify context has no span context
	spanCtx := trace.SpanContextFromContext(ctx)
	assert.False(t, spanCtx.IsValid(), "span context should not be present")

	// Verify no trace attributes are added
	var foundTraceAttr bool
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		if kv.Key == "trace_id" || kv.Key == "span_id" || kv.Key == "trace_flags" {
			foundTraceAttr = true
			return false // Stop iteration
		}
		return true
	})

	assert.False(t, foundTraceAttr, "no trace attributes should be added when trace context is absent")
}

// benchNoopProcessor discards every record; keeps BenchmarkOTelBridgeWrite's
// allocation profile isolated to buildLogRecord + Emit, not processor-side
// bookkeeping.
type benchNoopProcessor struct{}

func (benchNoopProcessor) OnEmit(context.Context, *sdklog.Record) error           { return nil }
func (benchNoopProcessor) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }
func (benchNoopProcessor) Shutdown(context.Context) error                         { return nil }
func (benchNoopProcessor) ForceFlush(context.Context) error                       { return nil }

// BenchmarkOTelBridgeWrite measures buildLogRecord's per-line cost through
// the public Write entry point, with and without trace context — the input
// shape that determines how many attributes the log.type check has to
// traverse before defaulting (pre-Step-1: a WalkAttributes scan; post: an
// O(1) map lookup on entry).
func BenchmarkOTelBridgeWrite(b *testing.B) {
	cases := []struct {
		name string
		line []byte
	}{
		{
			name: "without_trace_context",
			line: []byte(`{"level":"info","time":"2025-10-10T12:00:00.123456789Z","message":"benchmark line","user_id":"123","method":"POST"}`),
		},
		{
			name: "with_trace_context",
			line: []byte(`{"level":"info","time":"2025-10-10T12:00:00.123456789Z","message":"benchmark line","user_id":"123","method":"POST",` +
				`"trace_id":"0123456789abcdef0123456789abcdef","span_id":"0123456789abcdef","trace_flags":"1"}`),
		},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(benchNoopProcessor{}))
			b.Cleanup(func() {
				_ = provider.Shutdown(context.Background())
			})
			bridge := NewOTelBridge(provider)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = bridge.Write(tc.line)
			}
		})
	}
}
