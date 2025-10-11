package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/log/logtest"
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
	dualProc := NewDualModeLogProcessor(actionProc, traceProc)

	err := dualProc.Shutdown(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, actionProc.shutdownCount, "Action processor should be shut down")
	assert.Equal(t, 1, traceProc.shutdownCount, "Trace processor should be shut down")
}

// TestDualModeLogProcessor_ForceFlush verifies flush calls both processors
func TestDualModeLogProcessorForceFlush(t *testing.T) {
	actionProc := &mockProcessor{}
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, traceProc)

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
	dualProc := NewDualModeLogProcessor(actionProc, traceProc)

	tests := []struct {
		name       string
		severity   log.Severity
		attributes []log.KeyValue
		expected   bool
	}{
		{"action INFO enabled", log.SeverityInfo, []log.KeyValue{log.String(logTypeAttr, "action")}, true},
		{"trace INFO disabled", log.SeverityInfo, []log.KeyValue{log.String(logTypeAttr, "trace")}, false},
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
	dualProc := NewDualModeLogProcessor(actionProc, traceProc)

	assert.NotNil(t, dualProc)
	assert.NotNil(t, dualProc.actionProcessor)
	assert.NotNil(t, dualProc.traceProcessor)
}

func TestDualModeLogProcessorRoutesActionLogs(t *testing.T) {
	actionProc := &mockProcessor{}
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, traceProc)

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
	dualProc := NewDualModeLogProcessor(actionProc, traceProc)

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

func TestDualModeLogProcessorDropsTraceInfo(t *testing.T) {
	actionProc := &mockProcessor{}
	traceProc := &mockProcessor{}
	dualProc := NewDualModeLogProcessor(actionProc, traceProc)

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
	dualProc := NewDualModeLogProcessor(actionProc, traceProc)

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
