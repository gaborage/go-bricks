package observability

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/log/logtest"
	"go.opentelemetry.io/otel/trace"
)

// mockProcessor is a test processor that counts OnEmit calls
type mockProcessor struct {
	emitCount     int
	shutdownCount int
	flushCount    int
}

func (m *mockProcessor) OnEmit(_ context.Context, _ *sdklog.Record) error {
	m.emitCount++
	return nil
}

//nolint:gocritic // hugeParam: EnabledParameters passed by value per OTel SDK interface contract
func (m *mockProcessor) Enabled(_ context.Context, _ sdklog.EnabledParameters) bool {
	return true
}

func (m *mockProcessor) Shutdown(_ context.Context) error {
	m.shutdownCount++
	return nil
}

func (m *mockProcessor) ForceFlush(_ context.Context) error {
	m.flushCount++
	return nil
}

// errorMockProcessor is a test processor that returns configurable errors
type errorMockProcessor struct {
	emitErr     error
	shutdownErr error
	flushErr    error
}

func (m *errorMockProcessor) OnEmit(_ context.Context, _ *sdklog.Record) error {
	return m.emitErr
}

//nolint:gocritic // hugeParam: EnabledParameters passed by value per OTel SDK interface contract
func (m *errorMockProcessor) Enabled(_ context.Context, _ sdklog.EnabledParameters) bool {
	return true
}

func (m *errorMockProcessor) Shutdown(_ context.Context) error {
	return m.shutdownErr
}

func (m *errorMockProcessor) ForceFlush(_ context.Context) error {
	return m.flushErr
}

// TestDualModeLogProcessor_Shutdown verifies shutdown calls both processors
func TestDualModeLogProcessorShutdown(t *testing.T) {
	actionProc := &mockProcessor{}
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

	err := dualProc.Shutdown(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, actionProc.shutdownCount, "Action processor should be shut down")
	assert.Equal(t, 1, traceProc.shutdownCount, "Trace processor should be shut down")
}

// TestDualModeLogProcessor_ForceFlush verifies flush calls both processors
func TestDualModeLogProcessorForceFlush(t *testing.T) {
	actionProc := &mockProcessor{}
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

	err := dualProc.ForceFlush(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, actionProc.flushCount, "Action processor should be flushed")
	assert.Equal(t, 1, traceProc.flushCount, "Trace processor should be flushed")
}

// TestCollectRoutingLogType verifies log type extraction with different attribute scenarios
func TestCollectRoutingLogType(t *testing.T) {
	tests := []struct {
		name       string
		attributes []attribute.KeyValue
		expected   string
	}{
		{
			name:       "missing log.type defaults to trace",
			attributes: []attribute.KeyValue{},
			expected:   "trace",
		},
		{
			name:       "explicit action log type",
			attributes: []attribute.KeyValue{attribute.String(logTypeKey, "action")},
			expected:   "action",
		},
		{
			name:       "unknown log type preserved",
			attributes: []attribute.KeyValue{attribute.String(logTypeKey, "custom")},
			expected:   "custom",
		},
		{
			name:       "non string log type defaults to trace",
			attributes: []attribute.KeyValue{attribute.Int(logTypeKey, 123)},
			expected:   "trace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factory := logtest.RecordFactory{
				Severity:   log.SeverityInfo,
				Attributes: tt.attributes,
			}
			rec := factory.NewRecord()
			result, _ := collectRouting(&rec, false)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestDualModeLogProcessorEnabled verifies Enabled() method severity filtering
// Note: EnabledParameters only provides Severity (no access to attributes like log.type),
// so the test focuses on severity-based filtering. Full log.type routing happens in OnEmit.
func TestDualModeLogProcessorEnabled(t *testing.T) {
	actionProc := &mockProcessor{}
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

	tests := []struct {
		name     string
		severity log.Severity
		expected bool
	}{
		// With sampling rate 0: INFO/DEBUG disabled, WARN/ERROR enabled
		{"INFO disabled with rate 0", log.SeverityInfo, false},
		{"DEBUG disabled with rate 0", log.SeverityDebug, false},
		{"WARN enabled", log.SeverityWarn, true},
		{"ERROR enabled", log.SeverityError, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			param := sdklog.EnabledParameters{}
			param.Severity = tt.severity

			result := dualProc.Enabled(context.Background(), param)

			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestDualModeLogProcessor_Creation verifies processor creation
func TestDualModeLogProcessorCreation(t *testing.T) {
	actionProc := &mockProcessor{}
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.5)

	assert.NotNil(t, dualProc)
	assert.NotNil(t, dualProc.actionProcessor)
	assert.NotNil(t, dualProc.traceProcessor)
	assert.InDelta(t, 0.5, dualProc.samplingRate, 1e-9)
}

func TestDualModeLogProcessorRoutesActionLogs(t *testing.T) {
	actionProc := &mockProcessor{}
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

	factory := logtest.RecordFactory{
		Severity:   log.SeverityInfo,
		Attributes: []attribute.KeyValue{attribute.String(logTypeKey, "action")},
	}
	rec := factory.NewRecord()

	err := dualProc.OnEmit(context.Background(), &rec)
	require.NoError(t, err)

	assert.Equal(t, 1, actionProc.emitCount)
	assert.Equal(t, 0, traceProc.emitCount)
}

func TestDualModeLogProcessorRoutesTraceWarn(t *testing.T) {
	actionProc := &mockProcessor{}
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

	factory := logtest.RecordFactory{
		Severity:   log.SeverityWarn,
		Attributes: []attribute.KeyValue{attribute.String(logTypeKey, "trace")},
	}
	rec := factory.NewRecord()

	err := dualProc.OnEmit(context.Background(), &rec)
	require.NoError(t, err)

	assert.Equal(t, 0, actionProc.emitCount)
	assert.Equal(t, 1, traceProc.emitCount)
}

func TestDualModeLogProcessorDropsTraceInfoWithZeroRate(t *testing.T) {
	actionProc := &mockProcessor{}
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

	factory := logtest.RecordFactory{
		Severity:   log.SeverityInfo,
		Attributes: []attribute.KeyValue{attribute.String(logTypeKey, "trace")},
	}
	rec := factory.NewRecord()

	err := dualProc.OnEmit(context.Background(), &rec)
	require.NoError(t, err)

	assert.Equal(t, 0, actionProc.emitCount)
	assert.Equal(t, 0, traceProc.emitCount)
}

func TestDualModeLogProcessorDefaultsToTrace(t *testing.T) {
	actionProc := &mockProcessor{}
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

	factory := logtest.RecordFactory{
		Severity: log.SeverityError,
		// No log.type attribute set
	}
	rec := factory.NewRecord()

	err := dualProc.OnEmit(context.Background(), &rec)
	require.NoError(t, err)

	assert.Equal(t, 0, actionProc.emitCount)
	assert.Equal(t, 1, traceProc.emitCount)
}

// TestEnrichAndClassifyTraceContext verifies that trace context is properly populated
// in the SDK log record from the context parameter
func TestEnrichAndClassifyTraceContext(t *testing.T) {
	tests := []struct {
		name            string
		setupContext    func() context.Context
		expectTraceID   bool
		expectSpanID    bool
		expectedTraceID string
		expectedSpanID  string
		expectedFlags   trace.TraceFlags
	}{
		{
			name: "valid trace context",
			setupContext: func() context.Context {
				traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
				if err != nil {
					panic("test trace ID parse failed: " + err.Error())
				}
				spanID, err := trace.SpanIDFromHex("0123456789abcdef")
				if err != nil {
					panic("test span ID parse failed: " + err.Error())
				}
				spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
					TraceID:    traceID,
					SpanID:     spanID,
					TraceFlags: trace.FlagsSampled,
					Remote:     true,
				})
				return trace.ContextWithSpanContext(context.Background(), spanCtx)
			},
			expectTraceID:   true,
			expectSpanID:    true,
			expectedTraceID: "0123456789abcdef0123456789abcdef",
			expectedSpanID:  "0123456789abcdef",
			expectedFlags:   trace.FlagsSampled,
		},
		{
			name:          "context without trace",
			setupContext:  context.Background,
			expectTraceID: false,
			expectSpanID:  false,
		},
		{
			name: "invalid span context (zero IDs)",
			setupContext: func() context.Context {
				spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
					TraceID:    trace.TraceID{},
					SpanID:     trace.SpanID{},
					TraceFlags: 0,
				})
				return trace.ContextWithSpanContext(context.Background(), spanCtx)
			},
			expectTraceID: false,
			expectSpanID:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupContext()

			factory := logtest.RecordFactory{
				Severity: log.SeverityInfo,
			}
			rec := factory.NewRecord()

			// Call enrichAndClassify
			enrichAndClassify(ctx, &rec)

			traceID := rec.TraceID()
			spanID := rec.SpanID()

			assertOptionalTraceField(t, "TraceID", tt.expectTraceID, tt.expectedTraceID, traceID.IsValid(), traceID.String())
			assertOptionalTraceField(t, "SpanID", tt.expectSpanID, tt.expectedSpanID, spanID.IsValid(), spanID.String())
			if tt.expectTraceID && tt.expectSpanID {
				assert.Equal(t, tt.expectedFlags, rec.TraceFlags())
			}
		})
	}
}

// assertOptionalTraceField verifies the validity (and, when expected, the
// stringified value) of a trace-ID-shaped field. When expectValid is false,
// only invalidity is asserted — the expected value is ignored.
func assertOptionalTraceField(t *testing.T, name string, expectValid bool, expectedValue string, isValid bool, actualValue string) {
	t.Helper()
	if !expectValid {
		assert.False(t, isValid, "%s should be invalid (zero)", name)
		return
	}
	assert.True(t, isValid, "%s should be valid", name)
	assert.Equal(t, expectedValue, actualValue)
}

// TestDualModeProcessorEnrichesTraceContext verifies that the processor
// enriches log records with trace context during OnEmit
func TestDualModeProcessorEnrichesTraceContext(t *testing.T) {
	// Create a capturing processor to verify the record
	var capturedRecord *sdklog.Record
	capturingProc := &capturingProcessor{
		onEmitFunc: func(_ context.Context, rec *sdklog.Record) error {
			capturedRecord = rec
			return nil
		},
	}

	dualProc := NewDualModeLogProcessor(capturingProc, &mockProcessor{}, 0.0)

	// Create context with trace
	traceID, err := trace.TraceIDFromHex("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("bbbbbbbbbbbbbb01")
	require.NoError(t, err)
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)

	// Emit action log (routes to action processor)
	factory := logtest.RecordFactory{
		Severity:   log.SeverityInfo,
		Attributes: []attribute.KeyValue{attribute.String(logTypeKey, "action")},
	}
	rec := factory.NewRecord()

	err = dualProc.OnEmit(ctx, &rec)
	require.NoError(t, err)
	require.NotNil(t, capturedRecord, "Record should be captured")

	// Verify trace fields were enriched
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1", capturedRecord.TraceID().String())
	assert.Equal(t, "bbbbbbbbbbbbbb01", capturedRecord.SpanID().String())
	assert.Equal(t, trace.FlagsSampled, capturedRecord.TraceFlags())
}

// capturingProcessor captures the record passed to OnEmit for verification
type capturingProcessor struct {
	onEmitFunc func(context.Context, *sdklog.Record) error
}

func (c *capturingProcessor) OnEmit(ctx context.Context, rec *sdklog.Record) error {
	if c.onEmitFunc != nil {
		return c.onEmitFunc(ctx, rec)
	}
	return nil
}

//nolint:gocritic // hugeParam: EnabledParameters passed by value per OTel SDK interface contract
func (c *capturingProcessor) Enabled(_ context.Context, _ sdklog.EnabledParameters) bool {
	return true
}

func (c *capturingProcessor) Shutdown(_ context.Context) error {
	return nil
}

func (c *capturingProcessor) ForceFlush(_ context.Context) error {
	return nil
}

// TestEnrichAndClassifyFromAttributes verifies trace enrichment from record attributes (fallback path)
func TestEnrichAndClassifyFromAttributes(t *testing.T) {
	tests := []struct {
		name            string
		attributes      []attribute.KeyValue
		expectTraceID   bool
		expectedTraceID string
		expectedSpanID  string
		expectedFlags   trace.TraceFlags
	}{
		{
			name: "valid trace attributes",
			attributes: []attribute.KeyValue{
				attribute.String("trace_id", "0123456789abcdef0123456789abcdef"),
				attribute.String("span_id", "fedcba9876543210"),
				attribute.Int64("trace_flags", 1),
			},
			expectTraceID:   true,
			expectedTraceID: "0123456789abcdef0123456789abcdef",
			expectedSpanID:  "fedcba9876543210",
			expectedFlags:   trace.TraceFlags(1),
		},
		{
			name: "trace attributes without flags",
			attributes: []attribute.KeyValue{
				attribute.String("trace_id", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"),
				attribute.String("span_id", "bbbbbbbbbbbbbb01"),
			},
			expectTraceID:   true,
			expectedTraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
			expectedSpanID:  "bbbbbbbbbbbbbb01",
			expectedFlags:   trace.TraceFlags(0), // Default when not provided
		},
		{
			name: "missing trace_id attribute",
			attributes: []attribute.KeyValue{
				attribute.String("span_id", "fedcba9876543210"),
			},
			expectTraceID: false,
		},
		{
			name: "missing span_id attribute",
			attributes: []attribute.KeyValue{
				attribute.String("trace_id", "0123456789abcdef0123456789abcdef"),
			},
			expectTraceID: false,
		},
		{
			name: "invalid trace_id format",
			attributes: []attribute.KeyValue{
				attribute.String("trace_id", "invalid-hex"),
				attribute.String("span_id", "fedcba9876543210"),
			},
			expectTraceID: false,
		},
		{
			name: "invalid span_id format",
			attributes: []attribute.KeyValue{
				attribute.String("trace_id", "0123456789abcdef0123456789abcdef"),
				attribute.String("span_id", "invalid"),
			},
			expectTraceID: false,
		},
		{
			name: "trace_id as non-string type",
			attributes: []attribute.KeyValue{
				attribute.Int64("trace_id", 123),
				attribute.String("span_id", "fedcba9876543210"),
			},
			expectTraceID: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factory := logtest.RecordFactory{
				Severity:   log.SeverityInfo,
				Attributes: tt.attributes,
			}
			rec := factory.NewRecord()

			// Call enrichAndClassify with a background context (no span, so it
			// falls back to attributes) — behaviorally identical to the old
			// enrichFromAttributes(&rec)
			enrichAndClassify(context.Background(), &rec)

			traceID := rec.TraceID()
			spanID := rec.SpanID()
			flags := rec.TraceFlags()

			if tt.expectTraceID {
				assert.True(t, traceID.IsValid(), "TraceID should be valid")
				assert.True(t, spanID.IsValid(), "SpanID should be valid")
				assert.Equal(t, tt.expectedTraceID, traceID.String())
				assert.Equal(t, tt.expectedSpanID, spanID.String())
				assert.Equal(t, tt.expectedFlags, flags)
			} else {
				assert.False(t, traceID.IsValid(), "TraceID should remain invalid (zero)")
				assert.False(t, spanID.IsValid(), "SpanID should remain invalid (zero)")
			}
		})
	}
}

// TestEnrichContextVsAttributes verifies context takes precedence over attributes
func TestEnrichContextVsAttributes(t *testing.T) {
	// Create context with trace (context source)
	contextTraceID, err := trace.TraceIDFromHex("cccccccccccccccccccccccccccccccc")
	require.NoError(t, err)
	contextSpanID, err := trace.SpanIDFromHex("cccccccccccccccc")
	require.NoError(t, err)
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    contextTraceID,
		SpanID:     contextSpanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)

	// Create record with different trace in attributes (attribute source)
	factory := logtest.RecordFactory{
		Severity: log.SeverityInfo,
		Attributes: []attribute.KeyValue{
			attribute.String("trace_id", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"),
			attribute.String("span_id", "bbbbbbbbbbbbbb01"),
			attribute.Int64("trace_flags", 0),
		},
	}
	rec := factory.NewRecord()

	// Enrich should prefer context over attributes
	enrichAndClassify(ctx, &rec)

	// Verify context values were used (not attribute values)
	assert.Equal(t, "cccccccccccccccccccccccccccccccc", rec.TraceID().String())
	assert.Equal(t, "cccccccccccccccc", rec.SpanID().String())
	assert.Equal(t, trace.FlagsSampled, rec.TraceFlags())
}

// TestEnrichAttributesFallback verifies attributes are used when context has no trace
func TestEnrichAttributesFallback(t *testing.T) {
	ctx := context.Background() // No trace in context

	factory := logtest.RecordFactory{
		Severity: log.SeverityInfo,
		Attributes: []attribute.KeyValue{
			attribute.String("trace_id", "dddddddddddddddddddddddddddddddd"),
			attribute.String("span_id", "dddddddddddddddd"),
			attribute.Int64("trace_flags", 1),
		},
	}
	rec := factory.NewRecord()

	// Should fall back to attributes
	enrichAndClassify(ctx, &rec)

	assert.Equal(t, "dddddddddddddddddddddddddddddddd", rec.TraceID().String())
	assert.Equal(t, "dddddddddddddddd", rec.SpanID().String())
	assert.Equal(t, trace.TraceFlags(1), rec.TraceFlags())
}

// TestSamplingRateFullExport verifies rate=1.0 exports all INFO/DEBUG logs
func TestSamplingRateFullExport(t *testing.T) {
	actionProc := &mockProcessor{}
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, traceProc, 1.0) // 100% sampling

	// Emit INFO trace log (should be exported with rate=1.0)
	factory := logtest.RecordFactory{
		Severity:   log.SeverityInfo,
		Attributes: []attribute.KeyValue{attribute.String(logTypeKey, "trace")},
	}
	rec := factory.NewRecord()

	err := dualProc.OnEmit(context.Background(), &rec)
	require.NoError(t, err)

	assert.Equal(t, 0, actionProc.emitCount)
	assert.Equal(t, 1, traceProc.emitCount, "INFO log should be exported with rate=1.0")
}

// TestSamplingRateDeterministic verifies same trace ID produces same sampling decision
func TestSamplingRateDeterministic(t *testing.T) {
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(&mockProcessor{}, traceProc, 0.5) // 50% sampling

	// Create a specific trace ID
	traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("fedcba9876543210")
	require.NoError(t, err)

	// First call with this trace
	factory := logtest.RecordFactory{
		Severity:   log.SeverityInfo,
		Attributes: []attribute.KeyValue{attribute.String(logTypeKey, "trace")},
		TraceID:    traceID,
		SpanID:     spanID,
	}
	rec1 := factory.NewRecord()
	err = dualProc.OnEmit(context.Background(), &rec1)
	require.NoError(t, err)
	firstCount := traceProc.emitCount

	// Second call with SAME trace ID should produce same decision
	rec2 := factory.NewRecord()
	err = dualProc.OnEmit(context.Background(), &rec2)
	require.NoError(t, err)
	secondCount := traceProc.emitCount

	// Both should either be sampled or not (deterministic)
	if firstCount == 1 {
		assert.Equal(t, 2, secondCount, "Same trace ID should produce same sampling decision")
	} else {
		assert.Equal(t, 0, secondCount, "Same trace ID should produce same sampling decision")
	}
}

// TestSamplingWarnAlwaysExported verifies WARN logs are always exported regardless of rate
func TestSamplingWarnAlwaysExported(t *testing.T) {
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(&mockProcessor{}, traceProc, 0.0) // 0% sampling for INFO/DEBUG

	// WARN should still be exported
	factory := logtest.RecordFactory{
		Severity:   log.SeverityWarn,
		Attributes: []attribute.KeyValue{attribute.String(logTypeKey, "trace")},
	}
	rec := factory.NewRecord()

	err := dualProc.OnEmit(context.Background(), &rec)
	require.NoError(t, err)

	assert.Equal(t, 1, traceProc.emitCount, "WARN should always be exported regardless of sampling rate")
}

// TestSamplingErrorAlwaysExported verifies ERROR logs are always exported regardless of rate
func TestSamplingErrorAlwaysExported(t *testing.T) {
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(&mockProcessor{}, traceProc, 0.0) // 0% sampling for INFO/DEBUG

	// ERROR should still be exported
	factory := logtest.RecordFactory{
		Severity:   log.SeverityError,
		Attributes: []attribute.KeyValue{attribute.String(logTypeKey, "trace")},
	}
	rec := factory.NewRecord()

	err := dualProc.OnEmit(context.Background(), &rec)
	require.NoError(t, err)

	assert.Equal(t, 1, traceProc.emitCount, "ERROR should always be exported regardless of sampling rate")
}

// TestSamplingActionLogsUnaffected verifies action logs are always exported at 100%
func TestSamplingActionLogsUnaffected(t *testing.T) {
	actionProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, &mockProcessor{}, 0.0) // 0% sampling

	// Action INFO log should still be exported
	factory := logtest.RecordFactory{
		Severity:   log.SeverityInfo,
		Attributes: []attribute.KeyValue{attribute.String(logTypeKey, "action")},
	}
	rec := factory.NewRecord()

	err := dualProc.OnEmit(context.Background(), &rec)
	require.NoError(t, err)

	assert.Equal(t, 1, actionProc.emitCount, "Action logs should always be exported regardless of sampling rate")
}

// TestEnabledWithSamplingRate verifies Enabled() reflects sampling rate for INFO/DEBUG
func TestEnabledWithSamplingRate(t *testing.T) {
	// With rate > 0, INFO should be enabled (actual sampling happens in OnEmit)
	dualProcWithRate := NewDualModeLogProcessor(&mockProcessor{}, &mockProcessor{}, 0.5)

	param := sdklog.EnabledParameters{}
	param.Severity = log.SeverityInfo

	assert.True(t, dualProcWithRate.Enabled(context.Background(), param),
		"INFO should be enabled when sampling rate > 0")

	// With rate = 0, INFO should be disabled
	dualProcNoRate := NewDualModeLogProcessor(&mockProcessor{}, &mockProcessor{}, 0.0)
	assert.False(t, dualProcNoRate.Enabled(context.Background(), param),
		"INFO should be disabled when sampling rate = 0")
}

// TestNewDualModeLogProcessorPanicsOnNilActionProcessor verifies panic on nil action processor
func TestNewDualModeLogProcessorPanicsOnNilActionProcessor(t *testing.T) {
	assert.Panics(t, func() {
		NewDualModeLogProcessor(nil, &mockProcessor{}, 0.5)
	}, "Should panic when actionProcessor is nil")
}

// TestNewDualModeLogProcessorPanicsOnNilTraceProcessor verifies panic on nil trace processor
func TestNewDualModeLogProcessorPanicsOnNilTraceProcessor(t *testing.T) {
	assert.Panics(t, func() {
		NewDualModeLogProcessor(&mockProcessor{}, nil, 0.5)
	}, "Should panic when traceProcessor is nil")
}

// TestOnEmitErrorPropagation verifies errors from underlying processors are propagated
func TestOnEmitErrorPropagation(t *testing.T) {
	expectedErr := errors.New("processor error")

	t.Run("action processor error", func(t *testing.T) {
		actionProc := &errorMockProcessor{emitErr: expectedErr}
		traceProc := &mockProcessor{}
		dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

		factory := logtest.RecordFactory{
			Severity:   log.SeverityInfo,
			Attributes: []attribute.KeyValue{attribute.String(logTypeKey, "action")},
		}
		rec := factory.NewRecord()

		err := dualProc.OnEmit(context.Background(), &rec)
		assert.ErrorIs(t, err, expectedErr, "Action processor error should be propagated")
	})

	t.Run("trace processor error", func(t *testing.T) {
		actionProc := &mockProcessor{}
		traceProc := &errorMockProcessor{emitErr: expectedErr}
		dualProc := NewDualModeLogProcessor(actionProc, traceProc, 1.0) // 100% sampling to hit trace processor

		factory := logtest.RecordFactory{
			Severity:   log.SeverityInfo,
			Attributes: []attribute.KeyValue{attribute.String(logTypeKey, "trace")},
		}
		rec := factory.NewRecord()

		err := dualProc.OnEmit(context.Background(), &rec)
		assert.ErrorIs(t, err, expectedErr, "Trace processor error should be propagated")
	})
}

// TestShouldSampleEdgeCases verifies sampling behavior edge cases
func TestShouldSampleEdgeCases(t *testing.T) {
	t.Run("negative timestamp fallback", func(t *testing.T) {
		traceProc := &mockProcessor{}
		dualProc := NewDualModeLogProcessor(&mockProcessor{}, traceProc, 0.5)

		// Create record with no trace ID (will use timestamp-based sampling)
		// Use a negative timestamp to test the ts < 0 branch (line 109-111)
		factory := logtest.RecordFactory{
			Severity:   log.SeverityInfo,
			Attributes: []attribute.KeyValue{attribute.String(logTypeKey, "trace")},
			Timestamp:  time.Unix(-1, 0), // Negative timestamp
		}
		rec := factory.NewRecord()

		// Should not panic; negative timestamp should be clamped to 0
		err := dualProc.OnEmit(context.Background(), &rec)
		assert.NoError(t, err)
	})

	t.Run("zero sampling rate drops all without trace ID", func(t *testing.T) {
		traceProc := &mockProcessor{}
		dualProc := NewDualModeLogProcessor(&mockProcessor{}, traceProc, 0.0)

		factory := logtest.RecordFactory{
			Severity:   log.SeverityDebug, // DEBUG level subject to sampling
			Attributes: []attribute.KeyValue{attribute.String(logTypeKey, "trace")},
		}
		rec := factory.NewRecord()

		err := dualProc.OnEmit(context.Background(), &rec)
		require.NoError(t, err)
		assert.Equal(t, 0, traceProc.emitCount, "Zero sampling rate should drop all INFO/DEBUG logs")
	})
}

// TestShutdownWithErrors verifies error aggregation during shutdown
func TestShutdownWithErrors(t *testing.T) {
	errAction := errors.New("action shutdown error")
	errTrace := errors.New("trace shutdown error")

	t.Run("action processor error", func(t *testing.T) {
		actionProc := &errorMockProcessor{shutdownErr: errAction}
		traceProc := &mockProcessor{}
		dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

		err := dualProc.Shutdown(context.Background())
		assert.ErrorIs(t, err, errAction, "Should contain action processor error")
	})

	t.Run("trace processor error", func(t *testing.T) {
		actionProc := &mockProcessor{}
		traceProc := &errorMockProcessor{shutdownErr: errTrace}
		dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

		err := dualProc.Shutdown(context.Background())
		assert.ErrorIs(t, err, errTrace, "Should contain trace processor error")
	})

	t.Run("both processors error", func(t *testing.T) {
		actionProc := &errorMockProcessor{shutdownErr: errAction}
		traceProc := &errorMockProcessor{shutdownErr: errTrace}
		dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

		err := dualProc.Shutdown(context.Background())
		require.ErrorIs(t, err, errAction, "Should contain action processor error")
		assert.ErrorIs(t, err, errTrace, "Should contain trace processor error")
	})
}

// TestForceFlushWithErrors verifies error aggregation during flush
func TestForceFlushWithErrors(t *testing.T) {
	errAction := errors.New("action flush error")
	errTrace := errors.New("trace flush error")

	t.Run("action processor error", func(t *testing.T) {
		actionProc := &errorMockProcessor{flushErr: errAction}
		traceProc := &mockProcessor{}
		dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

		err := dualProc.ForceFlush(context.Background())
		assert.ErrorIs(t, err, errAction, "Should contain action processor error")
	})

	t.Run("trace processor error", func(t *testing.T) {
		actionProc := &mockProcessor{}
		traceProc := &errorMockProcessor{flushErr: errTrace}
		dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

		err := dualProc.ForceFlush(context.Background())
		assert.ErrorIs(t, err, errTrace, "Should contain trace processor error")
	})

	t.Run("both processors error", func(t *testing.T) {
		actionProc := &errorMockProcessor{flushErr: errAction}
		traceProc := &errorMockProcessor{flushErr: errTrace}
		dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

		err := dualProc.ForceFlush(context.Background())
		require.ErrorIs(t, err, errAction, "Should contain action processor error")
		assert.ErrorIs(t, err, errTrace, "Should contain trace processor error")
	})
}

// countKeptTraceID sweeps residues [0, samplingDenominator) through the
// trace-ID branch of shouldSample. hash is little-endian over trace-ID bytes
// 0..7, so PutUint64(tid[:8], uint64(i)) makes hash%samplingDenominator == i.
// tid[15] = 1 keeps the trace ID valid (non-zero) even when i == 0.
func countKeptTraceID(t *testing.T, dualProc *DualModeLogProcessor) int {
	t.Helper()
	kept := 0
	for i := range samplingDenominator {
		var tid trace.TraceID
		binary.LittleEndian.PutUint64(tid[:8], uint64(i))
		tid[15] = 1
		factory := logtest.RecordFactory{
			Severity: log.SeverityInfo,
			TraceID:  tid,
		}
		rec := factory.NewRecord()
		if dualProc.shouldSample(&rec) {
			kept++
		}
	}
	return kept
}

// countKeptTimestamp sweeps residues [0, samplingDenominator) through the
// timestamp-fallback branch of shouldSample (no trace ID set, so
// traceID.IsValid() is false). uint64(ts)%samplingDenominator == i.
func countKeptTimestamp(t *testing.T, dualProc *DualModeLogProcessor) int {
	t.Helper()
	kept := 0
	for i := range samplingDenominator {
		factory := logtest.RecordFactory{
			Severity:  log.SeverityInfo,
			Timestamp: time.Unix(0, int64(i)),
		}
		rec := factory.NewRecord()
		if dualProc.shouldSample(&rec) {
			kept++
		}
	}
	return kept
}

// TestShouldSampleResolution pins the sub-percent sampling resolution
// introduced by samplingDenominator = 10000. Each case asserts an exact kept
// count across the full residue space [0, 10000), driven through both the
// trace-ID branch (dual_processor.go:120) and the timestamp-fallback branch
// (dual_processor.go:111) of shouldSample.
func TestShouldSampleResolution(t *testing.T) {
	cases := []struct {
		name     string
		rate     float64
		wantKept int
	}{
		{name: "sub_percent_rate_exports_half_a_percent", rate: 0.005, wantKept: 50},
		{name: "high_precision_rate_keeps_more_than_99_percent", rate: 0.999, wantKept: 9990},
		{name: "whole_percent_rate_keeps_its_fraction", rate: 0.25, wantKept: 2500},
		{name: "smallest_representable_rate_keeps_one_in_ten_thousand", rate: 0.0001, wantKept: 1},
		{name: "half_unit_rate_rounds_up_to_one", rate: 0.00005, wantKept: 1},
		{name: "sub_resolution_rate_keeps_nothing", rate: 0.00001, wantKept: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("trace_id", func(t *testing.T) {
				dualProc := NewDualModeLogProcessor(&mockProcessor{}, &mockProcessor{}, tc.rate)
				kept := countKeptTraceID(t, dualProc)
				assert.Equal(t, tc.wantKept, kept, "rate %v via trace ID should keep exactly %d of %d", tc.rate, tc.wantKept, samplingDenominator)
			})

			t.Run("timestamp", func(t *testing.T) {
				dualProc := NewDualModeLogProcessor(&mockProcessor{}, &mockProcessor{}, tc.rate)
				kept := countKeptTimestamp(t, dualProc)
				assert.Equal(t, tc.wantKept, kept, "rate %v via timestamp should keep exactly %d of %d", tc.rate, tc.wantKept, samplingDenominator)
			})
		})
	}

	t.Run("fast_path_rate_zero_drops_all", func(t *testing.T) {
		dualProc := NewDualModeLogProcessor(&mockProcessor{}, &mockProcessor{}, 0.0)
		assert.Equal(t, 0, countKeptTraceID(t, dualProc), "rate 0.0 must drop everything via trace ID")
		assert.Equal(t, 0, countKeptTimestamp(t, dualProc), "rate 0.0 must drop everything via timestamp")
	})

	t.Run("fast_path_rate_one_keeps_all", func(t *testing.T) {
		dualProc := NewDualModeLogProcessor(&mockProcessor{}, &mockProcessor{}, 1.0)
		assert.Equal(t, samplingDenominator, countKeptTraceID(t, dualProc), "rate 1.0 must keep everything via trace ID")
		assert.Equal(t, samplingDenominator, countKeptTimestamp(t, dualProc), "rate 1.0 must keep everything via timestamp")
	})
}

// TestOnEmitRoutingDecisionTable pins the consumer-observable routing
// decision through the public OnEmit entry point — which processor (if
// either) receives a record, and what canonical trace fields land on it —
// across the cross-product of log.type, trace source, severity, and
// sampling rate. Unit-testing collectRouting in isolation is not
// sufficient: this is the surface Step 2's walk merge could silently change.
func TestOnEmitRoutingDecisionTable(t *testing.T) {
	const (
		ctxTraceIDHex  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"
		ctxSpanIDHex   = "bbbbbbbbbbbbbb01"
		attrTraceIDHex = "dddddddddddddddddddddddddddddddd"
		attrSpanIDHex  = "dddddddddddddddd"
	)

	bgCtx := context.Background
	ctxSpan := func() context.Context {
		traceID, err := trace.TraceIDFromHex(ctxTraceIDHex)
		require.NoError(t, err)
		spanID, err := trace.SpanIDFromHex(ctxSpanIDHex)
		require.NoError(t, err)
		spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    traceID,
			SpanID:     spanID,
			TraceFlags: trace.FlagsSampled,
		})
		return trace.ContextWithSpanContext(context.Background(), spanCtx)
	}

	attrTraceAttrs := []attribute.KeyValue{
		attribute.String("trace_id", attrTraceIDHex),
		attribute.String("span_id", attrSpanIDHex),
	}

	tests := []struct {
		name             string
		attrs            []attribute.KeyValue
		setupContext     func() context.Context
		severity         log.Severity
		samplingRate     float64
		wantActionCount  int
		wantTraceCount   int
		wantTraceIDValid bool
		wantTraceIDHex   string
		wantSpanIDHex    string
	}{
		{
			name:             "action_ctx_span_info",
			attrs:            []attribute.KeyValue{attribute.String(logTypeKey, "action")},
			setupContext:     ctxSpan,
			severity:         log.SeverityInfo,
			samplingRate:     0.0,
			wantActionCount:  1,
			wantTraceCount:   0,
			wantTraceIDValid: true,
			wantTraceIDHex:   ctxTraceIDHex,
			wantSpanIDHex:    ctxSpanIDHex,
		},
		{
			name:             "action_attrs_only_info",
			attrs:            append([]attribute.KeyValue{attribute.String(logTypeKey, "action")}, attrTraceAttrs...),
			setupContext:     bgCtx,
			severity:         log.SeverityInfo,
			samplingRate:     0.0,
			wantActionCount:  1,
			wantTraceCount:   0,
			wantTraceIDValid: true,
			wantTraceIDHex:   attrTraceIDHex,
			wantSpanIDHex:    attrSpanIDHex,
		},
		{
			name:             "action_none_error",
			attrs:            []attribute.KeyValue{attribute.String(logTypeKey, "action")},
			setupContext:     bgCtx,
			severity:         log.SeverityError,
			samplingRate:     0.0,
			wantActionCount:  1,
			wantTraceCount:   0,
			wantTraceIDValid: false,
		},
		{
			name:             "absent_ctx_span_info_full_rate",
			attrs:            nil,
			setupContext:     ctxSpan,
			severity:         log.SeverityInfo,
			samplingRate:     1.0,
			wantActionCount:  0,
			wantTraceCount:   1,
			wantTraceIDValid: true,
			wantTraceIDHex:   ctxTraceIDHex,
			wantSpanIDHex:    ctxSpanIDHex,
		},
		{
			name:             "absent_ctx_span_warn_zero_rate",
			attrs:            nil,
			setupContext:     ctxSpan,
			severity:         log.SeverityWarn,
			samplingRate:     0.0,
			wantActionCount:  0,
			wantTraceCount:   1,
			wantTraceIDValid: true,
			wantTraceIDHex:   ctxTraceIDHex,
			wantSpanIDHex:    ctxSpanIDHex,
		},
		{
			name:             "absent_attrs_only_info_zero_rate_dropped",
			attrs:            attrTraceAttrs,
			setupContext:     bgCtx,
			severity:         log.SeverityInfo,
			samplingRate:     0.0,
			wantActionCount:  0,
			wantTraceCount:   0,
			wantTraceIDValid: true,
			wantTraceIDHex:   attrTraceIDHex,
			wantSpanIDHex:    attrSpanIDHex,
		},
		{
			name:             "absent_attrs_only_info_full_rate",
			attrs:            attrTraceAttrs,
			setupContext:     bgCtx,
			severity:         log.SeverityInfo,
			samplingRate:     1.0,
			wantActionCount:  0,
			wantTraceCount:   1,
			wantTraceIDValid: true,
			wantTraceIDHex:   attrTraceIDHex,
			wantSpanIDHex:    attrSpanIDHex,
		},
		{
			name:             "explicit_trace_none_debug_dropped",
			attrs:            []attribute.KeyValue{attribute.String(logTypeKey, "trace")},
			setupContext:     bgCtx,
			severity:         log.SeverityDebug,
			samplingRate:     0.0,
			wantActionCount:  0,
			wantTraceCount:   0,
			wantTraceIDValid: false,
		},
		{
			name:             "non_string_log_type_defaults_to_trace",
			attrs:            []attribute.KeyValue{attribute.Int(logTypeKey, 7)},
			setupContext:     bgCtx,
			severity:         log.SeverityError,
			samplingRate:     0.0,
			wantActionCount:  0,
			wantTraceCount:   1,
			wantTraceIDValid: false,
		},
		{
			// All three trace fields complete BEFORE log.type. A walk that stops
			// on c.done() alone would never see log.type and would misroute this
			// record to the trace processor — pins the other half of invariant 3.
			name: "trace_fields_before_log_type_walk_must_not_stop_early",
			attrs: append(
				append(benchPaddingAttrs(3),
					attribute.String("trace_id", attrTraceIDHex),
					attribute.String("span_id", attrSpanIDHex),
					attribute.Int64("trace_flags", 1),
				),
				attribute.String(logTypeKey, "action"),
			),
			setupContext:     bgCtx,
			severity:         log.SeverityInfo,
			samplingRate:     0.0,
			wantActionCount:  1,
			wantTraceCount:   0,
			wantTraceIDValid: true,
			wantTraceIDHex:   attrTraceIDHex,
			wantSpanIDHex:    attrSpanIDHex,
		},
		{
			// log.type sits after 9 padding attributes but BEFORE trace_id/span_id.
			// A walk that stops unconditionally as soon as it finds log.type
			// (ignoring wantTrace) would never reach trace_id/span_id — pins
			// invariant 3 (the early-exit union in collectRouting).
			name: "log_type_before_trace_fields_walk_must_not_stop_early",
			attrs: append(
				append(benchPaddingAttrs(9), attribute.String(logTypeKey, "trace")),
				attrTraceAttrs...,
			),
			setupContext:     bgCtx,
			severity:         log.SeverityInfo,
			samplingRate:     1.0,
			wantActionCount:  0,
			wantTraceCount:   1,
			wantTraceIDValid: true,
			wantTraceIDHex:   attrTraceIDHex,
			wantSpanIDHex:    attrSpanIDHex,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actionProc := &mockProcessor{}
			traceProc := &mockProcessor{}
			dualProc := NewDualModeLogProcessor(actionProc, traceProc, tt.samplingRate)

			factory := logtest.RecordFactory{
				Severity:   tt.severity,
				Attributes: tt.attrs,
			}
			rec := factory.NewRecord()

			err := dualProc.OnEmit(tt.setupContext(), &rec)
			require.NoError(t, err)

			assert.Equal(t, tt.wantActionCount, actionProc.emitCount, "action processor emit count")
			assert.Equal(t, tt.wantTraceCount, traceProc.emitCount, "trace processor emit count")

			traceID := rec.TraceID()
			if tt.wantTraceIDValid {
				assert.True(t, traceID.IsValid(), "record TraceID should be valid")
				assert.Equal(t, tt.wantTraceIDHex, traceID.String())
				assert.Equal(t, tt.wantSpanIDHex, rec.SpanID().String())
			} else {
				assert.False(t, traceID.IsValid(), "record TraceID should remain invalid (zero)")
			}
		})
	}
}

// TestOnEmitEnrichesBeforeSampling is the ordering discriminator: it fails
// if enrichment (which populates rec.TraceID(), consulted by shouldSample)
// ever runs after the sampling decision. The fixture is chosen so the
// trace-ID-based verdict and the timestamp-fallback verdict disagree under
// the live samplingDenominator, so a reordering bug is caught by wrong
// ROUTING, not just a wrong intermediate value:
//   - trace ID's first 8 bytes are all 0xaa: hash 0xAAAAAAAAAAAAAAAA %
//     samplingDenominator = 4410, which is < the rate-0.5 threshold (5000) ->
//     sampled, if TraceID() is valid when shouldSample runs.
//   - timestamp fallback: 9999 % samplingDenominator = 9999, which is NOT <
//     5000 -> dropped, if enrichment has not run yet (TraceID() invalid).
func TestOnEmitEnrichesBeforeSampling(t *testing.T) {
	// Fixture precondition: the two verdicts must disagree, otherwise this
	// test cannot discriminate enrichment order. Derived from the live
	// samplingDenominator and the constructor's rate-to-threshold conversion
	// rather than hardcoded, so a change to either fails this loudly instead
	// of letting the test degrade into a tautology.
	const traceIDHash = uint64(0xAAAAAAAAAAAAAAAA)
	const timestampNanos = int64(9999)
	threshold := uint64(math.Round(0.5 * samplingDenominator))
	require.Less(t, traceIDHash%samplingDenominator, threshold,
		"fixture broken: trace-ID verdict must be 'sampled'")
	require.GreaterOrEqual(t, uint64(timestampNanos)%samplingDenominator, threshold,
		"fixture broken: timestamp-fallback verdict must be 'dropped'")

	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(&mockProcessor{}, traceProc, 0.5)

	factory := logtest.RecordFactory{
		Severity:  log.SeverityInfo,
		Timestamp: time.Unix(0, 9999),
		Attributes: []attribute.KeyValue{
			attribute.String("trace_id", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"),
			attribute.String("span_id", "bbbbbbbbbbbbbb01"),
		},
	}
	rec := factory.NewRecord()

	err := dualProc.OnEmit(context.Background(), &rec)
	require.NoError(t, err)

	assert.Equal(t, 1, traceProc.emitCount,
		"trace-ID-hash verdict (sampled) must win — this only holds if enrichment populated TraceID() before shouldSample ran")
}

// benchPaddingAttrs returns n filler attributes so BenchmarkDualModeProcessorOnEmit's
// walk cost is visible — a too-small attribute set hides it (plans/INDEX.md row 129) —
// and TestOnEmitRoutingDecisionTable's early-exit-union row reuses it to push log.type
// ahead of trace_id/span_id.
func benchPaddingAttrs(n int) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, n)
	for i := range attrs {
		attrs[i] = attribute.String(fmt.Sprintf("field_%02d", i), "value")
	}
	return attrs
}

// BenchmarkDualModeProcessorOnEmit measures OnEmit's per-record cost through
// the public entry point for the two enrichment sources: a valid ctx span
// (context wins, no attribute-trace collection needed) and attrs-only
// (context has no span, so trace fields must be collected from attributes
// during the walk).
func BenchmarkDualModeProcessorOnEmit(b *testing.B) {
	b.Run("ctx_span_valid", func(b *testing.B) {
		traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
		require.NoError(b, err)
		spanID, err := trace.SpanIDFromHex("fedcba9876543210")
		require.NoError(b, err)
		spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    traceID,
			SpanID:     spanID,
			TraceFlags: trace.FlagsSampled,
		})
		ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)

		dualProc := NewDualModeLogProcessor(&mockProcessor{}, &mockProcessor{}, 1.0)
		attrs := benchPaddingAttrs(12)
		factory := logtest.RecordFactory{
			Severity:   log.SeverityInfo,
			Attributes: attrs,
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rec := factory.NewRecord()
			_ = dualProc.OnEmit(ctx, &rec)
		}
	})

	b.Run("attrs_only", func(b *testing.B) {
		dualProc := NewDualModeLogProcessor(&mockProcessor{}, &mockProcessor{}, 1.0)
		attrs := append(benchPaddingAttrs(9),
			attribute.String("trace_id", "0123456789abcdef0123456789abcdef"),
			attribute.String("span_id", "fedcba9876543210"),
			attribute.Int64("trace_flags", 1),
		)
		factory := logtest.RecordFactory{
			Severity:   log.SeverityInfo,
			Attributes: attrs,
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rec := factory.NewRecord()
			_ = dualProc.OnEmit(context.Background(), &rec)
		}
	})
}
