package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/log/logtest"
	"go.opentelemetry.io/otel/trace"
)

const (
	logTypeAttr = "log.type"
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

func (m *mockProcessor) Enabled(_ context.Context, _ *sdklog.Record) bool {
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

// TestExtractLogType verifies log type extraction with different attribute scenarios
func TestExtractLogType(t *testing.T) {
	tests := []struct {
		name       string
		attributes []log.KeyValue
		expected   string
	}{
		{
			name:       "missing log.type defaults to trace",
			attributes: []log.KeyValue{},
			expected:   "trace",
		},
		{
			name:       "explicit action log type",
			attributes: []log.KeyValue{log.String(logTypeAttr, "action")},
			expected:   "action",
		},
		{
			name:       "unknown log type preserved",
			attributes: []log.KeyValue{log.String(logTypeAttr, "custom")},
			expected:   "custom",
		},
		{
			name:       "non string log type defaults to trace",
			attributes: []log.KeyValue{log.Int(logTypeAttr, 123)},
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
			result := extractLogType(&rec)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestDualModeLogProcessor_Enabled verifies Enabled() method severity filtering
func TestDualModeLogProcessorEnabled(t *testing.T) {
	actionProc := &mockProcessor{}
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

	tests := []struct {
		name       string
		severity   log.Severity
		attributes []log.KeyValue
		expected   bool
	}{
		{"action INFO enabled", log.SeverityInfo, []log.KeyValue{log.String(logTypeAttr, "action")}, true},
		{"trace INFO disabled with rate 0", log.SeverityInfo, []log.KeyValue{log.String(logTypeAttr, "trace")}, false},
		{"trace WARN enabled", log.SeverityWarn, []log.KeyValue{log.String(logTypeAttr, "trace")}, true},
		{"trace ERROR enabled", log.SeverityError, []log.KeyValue{log.String(logTypeAttr, "trace")}, true},
		{"unknown WARN treated as trace", log.SeverityWarn, []log.KeyValue{log.String(logTypeAttr, "unknown")}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factory := logtest.RecordFactory{
				Severity:   tt.severity,
				Attributes: tt.attributes,
			}
			rec := factory.NewRecord()

			result := dualProc.Enabled(context.Background(), &rec)

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
	assert.Equal(t, 0.5, dualProc.samplingRate)
}

func TestDualModeLogProcessorRoutesActionLogs(t *testing.T) {
	actionProc := &mockProcessor{}
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, traceProc, 0.0)

	factory := logtest.RecordFactory{
		Severity:   log.SeverityInfo,
		Attributes: []log.KeyValue{log.String(logTypeAttr, "action")},
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
		Attributes: []log.KeyValue{log.String(logTypeAttr, "trace")},
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
		Attributes: []log.KeyValue{log.String(logTypeAttr, "trace")},
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

// TestEnrichTraceContext verifies that trace context is properly populated
// in the SDK log record from the context parameter
func TestEnrichTraceContext(t *testing.T) {
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
				traceID, _ := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
				spanID, _ := trace.SpanIDFromHex("0123456789abcdef")
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

			// Call enrichTraceContext
			enrichTraceContext(ctx, &rec)

			// Verify trace fields
			traceID := rec.TraceID()
			spanID := rec.SpanID()
			flags := rec.TraceFlags()

			if tt.expectTraceID {
				assert.True(t, traceID.IsValid(), "TraceID should be valid")
				assert.Equal(t, tt.expectedTraceID, traceID.String())
			} else {
				assert.False(t, traceID.IsValid(), "TraceID should be invalid (zero)")
			}

			if tt.expectSpanID {
				assert.True(t, spanID.IsValid(), "SpanID should be valid")
				assert.Equal(t, tt.expectedSpanID, spanID.String())
			} else {
				assert.False(t, spanID.IsValid(), "SpanID should be invalid (zero)")
			}

			if tt.expectTraceID && tt.expectSpanID {
				assert.Equal(t, tt.expectedFlags, flags)
			}
		})
	}
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
	traceID, _ := trace.TraceIDFromHex("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1")
	spanID, _ := trace.SpanIDFromHex("bbbbbbbbbbbbbb01")
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)

	// Emit action log (routes to action processor)
	factory := logtest.RecordFactory{
		Severity:   log.SeverityInfo,
		Attributes: []log.KeyValue{log.String(logTypeAttr, "action")},
	}
	rec := factory.NewRecord()

	err := dualProc.OnEmit(ctx, &rec)
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

func (c *capturingProcessor) Enabled(_ context.Context, _ *sdklog.Record) bool {
	return true
}

func (c *capturingProcessor) Shutdown(_ context.Context) error {
	return nil
}

func (c *capturingProcessor) ForceFlush(_ context.Context) error {
	return nil
}

// TestEnrichFromAttributes verifies trace enrichment from record attributes (fallback path)
func TestEnrichFromAttributes(t *testing.T) {
	tests := []struct {
		name            string
		attributes      []log.KeyValue
		expectTraceID   bool
		expectedTraceID string
		expectedSpanID  string
		expectedFlags   trace.TraceFlags
	}{
		{
			name: "valid trace attributes",
			attributes: []log.KeyValue{
				log.String("trace_id", "0123456789abcdef0123456789abcdef"),
				log.String("span_id", "fedcba9876543210"),
				log.Int64("trace_flags", 1),
			},
			expectTraceID:   true,
			expectedTraceID: "0123456789abcdef0123456789abcdef",
			expectedSpanID:  "fedcba9876543210",
			expectedFlags:   trace.TraceFlags(1),
		},
		{
			name: "trace attributes without flags",
			attributes: []log.KeyValue{
				log.String("trace_id", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"),
				log.String("span_id", "bbbbbbbbbbbbbb01"),
			},
			expectTraceID:   true,
			expectedTraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1",
			expectedSpanID:  "bbbbbbbbbbbbbb01",
			expectedFlags:   trace.TraceFlags(0), // Default when not provided
		},
		{
			name: "missing trace_id attribute",
			attributes: []log.KeyValue{
				log.String("span_id", "fedcba9876543210"),
			},
			expectTraceID: false,
		},
		{
			name: "missing span_id attribute",
			attributes: []log.KeyValue{
				log.String("trace_id", "0123456789abcdef0123456789abcdef"),
			},
			expectTraceID: false,
		},
		{
			name: "invalid trace_id format",
			attributes: []log.KeyValue{
				log.String("trace_id", "invalid-hex"),
				log.String("span_id", "fedcba9876543210"),
			},
			expectTraceID: false,
		},
		{
			name: "invalid span_id format",
			attributes: []log.KeyValue{
				log.String("trace_id", "0123456789abcdef0123456789abcdef"),
				log.String("span_id", "invalid"),
			},
			expectTraceID: false,
		},
		{
			name: "trace_id as non-string type",
			attributes: []log.KeyValue{
				log.Int64("trace_id", 123),
				log.String("span_id", "fedcba9876543210"),
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

			// Call enrichFromAttributes directly (no context)
			enrichFromAttributes(&rec)

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
	contextTraceID, _ := trace.TraceIDFromHex("cccccccccccccccccccccccccccccccc")
	contextSpanID, _ := trace.SpanIDFromHex("cccccccccccccccc")
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    contextTraceID,
		SpanID:     contextSpanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)

	// Create record with different trace in attributes (attribute source)
	factory := logtest.RecordFactory{
		Severity: log.SeverityInfo,
		Attributes: []log.KeyValue{
			log.String("trace_id", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"),
			log.String("span_id", "bbbbbbbbbbbbbb01"),
			log.Int64("trace_flags", 0),
		},
	}
	rec := factory.NewRecord()

	// Enrich should prefer context over attributes
	enrichTraceContext(ctx, &rec)

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
		Attributes: []log.KeyValue{
			log.String("trace_id", "dddddddddddddddddddddddddddddddd"),
			log.String("span_id", "dddddddddddddddd"),
			log.Int64("trace_flags", 1),
		},
	}
	rec := factory.NewRecord()

	// Should fall back to attributes
	enrichTraceContext(ctx, &rec)

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
		Attributes: []log.KeyValue{log.String(logTypeAttr, "trace")},
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
	traceID, _ := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	spanID, _ := trace.SpanIDFromHex("fedcba9876543210")

	// First call with this trace
	factory := logtest.RecordFactory{
		Severity:   log.SeverityInfo,
		Attributes: []log.KeyValue{log.String(logTypeAttr, "trace")},
		TraceID:    traceID,
		SpanID:     spanID,
	}
	rec1 := factory.NewRecord()
	_ = dualProc.OnEmit(context.Background(), &rec1)
	firstCount := traceProc.emitCount

	// Second call with SAME trace ID should produce same decision
	rec2 := factory.NewRecord()
	_ = dualProc.OnEmit(context.Background(), &rec2)
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
		Attributes: []log.KeyValue{log.String(logTypeAttr, "trace")},
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
		Attributes: []log.KeyValue{log.String(logTypeAttr, "trace")},
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
		Attributes: []log.KeyValue{log.String(logTypeAttr, "action")},
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

	factory := logtest.RecordFactory{
		Severity:   log.SeverityInfo,
		Attributes: []log.KeyValue{log.String(logTypeAttr, "trace")},
	}
	rec := factory.NewRecord()

	assert.True(t, dualProcWithRate.Enabled(context.Background(), &rec),
		"INFO should be enabled when sampling rate > 0")

	// With rate = 0, INFO should be disabled
	dualProcNoRate := NewDualModeLogProcessor(&mockProcessor{}, &mockProcessor{}, 0.0)
	assert.False(t, dualProcNoRate.Enabled(context.Background(), &rec),
		"INFO should be disabled when sampling rate = 0")
}
