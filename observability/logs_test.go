package observability

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// mockExporter is a test exporter that captures exported records
type mockLogExporter struct {
	records       []sdklog.Record
	shutdownCount int
	flushCount    int
}

func (m *mockLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	m.records = append(m.records, records...)
	return nil
}

func (m *mockLogExporter) Shutdown(_ context.Context) error {
	m.shutdownCount++
	return nil
}

func (m *mockLogExporter) ForceFlush(_ context.Context) error {
	m.flushCount++
	return nil
}

// TestSeverityFilterExporter_AlwaysSampleHigh verifies that high-severity logs
// (WARN/ERROR/FATAL) are always exported regardless of sample rate.
func TestSeverityFilterExporterAlwaysSampleHigh(t *testing.T) {
	mock := &mockLogExporter{}
	filter := &severityFilterExporter{
		wrapped:          mock,
		sampleRate:       0.0, // Zero sample rate for low-severity
		alwaysSampleHigh: true,
	}

	tests := []struct {
		name     string
		severity log.Severity
		expected bool
	}{
		{"TRACE should be filtered", log.SeverityTrace, false},
		{"DEBUG should be filtered", log.SeverityDebug, false},
		{"INFO should be filtered", log.SeverityInfo, false},
		{"WARN should pass", log.SeverityWarn, true},
		{"ERROR should pass", log.SeverityError, true},
		{"FATAL should pass", log.SeverityFatal, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock.records = nil // Clear previous records

			rec := createTestRecord(tt.severity, time.Now())
			result := filter.shouldExport(&rec)

			assert.Equal(t, tt.expected, result, "Severity %d should have export=%v", tt.severity, tt.expected)

			// Verify actual export behavior
			err := filter.Export(context.Background(), []sdklog.Record{rec})
			require.NoError(t, err)

			if tt.expected {
				assert.Len(t, mock.records, 1, "High-severity log should be exported")
			} else {
				assert.Len(t, mock.records, 0, "Low-severity log should be filtered")
			}
		})
	}
}

// TestSeverityFilterExporter_SampleRateDistribution verifies that the hash-based
// sampling produces approximately the expected distribution over many samples.
func TestSeverityFilterExporterSampleRateDistribution(t *testing.T) {
	tests := []struct {
		name         string
		sampleRate   float64
		sampleSize   int
		tolerance    float64 // Acceptable variance from expected rate
		alwaysSample bool
	}{
		{
			name:         "10% sampling (0.1)",
			sampleRate:   0.1,
			sampleSize:   10000,
			tolerance:    0.02, // ±2%
			alwaysSample: false,
		},
		{
			name:         "50% sampling (0.5)",
			sampleRate:   0.5,
			sampleSize:   10000,
			tolerance:    0.02,
			alwaysSample: false,
		},
		{
			name:         "90% sampling (0.9)",
			sampleRate:   0.9,
			sampleSize:   10000,
			tolerance:    0.02,
			alwaysSample: false,
		},
		{
			name:         "100% sampling (1.0)",
			sampleRate:   1.0,
			sampleSize:   1000,
			tolerance:    0.0,
			alwaysSample: false,
		},
		{
			name:         "0% sampling (0.0)",
			sampleRate:   0.0,
			sampleSize:   1000,
			tolerance:    0.0,
			alwaysSample: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := &severityFilterExporter{
				wrapped:          &mockLogExporter{},
				sampleRate:       tt.sampleRate,
				alwaysSampleHigh: tt.alwaysSample,
			}

			// Generate records with varying timestamps to test hash distribution
			baseTime := time.Now()
			exported := 0

			for i := 0; i < tt.sampleSize; i++ {
				// Vary timestamps by nanoseconds to exercise hash function
				timestamp := baseTime.Add(time.Duration(i) * time.Nanosecond)
				rec := createTestRecord(log.SeverityInfo, timestamp)

				if filter.shouldExport(&rec) {
					exported++
				}
			}

			actualRate := float64(exported) / float64(tt.sampleSize)
			expectedRate := tt.sampleRate

			// For 0% and 100% rates, expect exact match
			if tt.sampleRate == 0.0 || tt.sampleRate == 1.0 {
				assert.Equal(t, expectedRate, actualRate,
					"Expected exact rate for %.1f%% sampling", tt.sampleRate*100)
			} else {
				// For intermediate rates, allow tolerance
				assert.InDelta(t, expectedRate, actualRate, tt.tolerance,
					"Sample rate %.1f%% should be within ±%.1f%% tolerance",
					tt.sampleRate*100, tt.tolerance*100)
			}
		})
	}
}

// TestSeverityFilterExporter_DeterministicSampling verifies that the same
// timestamp always produces the same sampling decision (determinism).
func TestSeverityFilterExporterDeterministicSampling(t *testing.T) {
	filter := &severityFilterExporter{
		wrapped:          &mockLogExporter{},
		sampleRate:       0.5,
		alwaysSampleHigh: false,
	}

	timestamp := time.Now()

	// Test the same timestamp multiple times
	var results []bool
	for i := 0; i < 10; i++ {
		rec := createTestRecord(log.SeverityInfo, timestamp)
		results = append(results, filter.shouldExport(&rec))
	}

	// All results should be identical (deterministic)
	firstResult := results[0]
	for i, result := range results {
		assert.Equal(t, firstResult, result,
			"Iteration %d: Same timestamp should always produce same sampling decision", i)
	}
}

// TestSeverityFilterExporter_AlwaysSampleHighOverridesSampleRate tests that
// when alwaysSampleHigh is true, high-severity logs are exported even with 0% sample rate.
func TestSeverityFilterExporterAlwaysSampleHighOverridesSampleRate(t *testing.T) {
	filter := &severityFilterExporter{
		wrapped:          &mockLogExporter{},
		sampleRate:       0.0, // 0% sample rate
		alwaysSampleHigh: true,
	}

	// High-severity log should still be exported
	rec := createTestRecord(log.SeverityError, time.Now())
	result := filter.shouldExport(&rec)

	assert.True(t, result, "AlwaysSampleHigh should override 0% sample rate for ERROR")
}

// TestSeverityFilterExporter_Lifecycle tests the shutdown and flush operations.
func TestSeverityFilterExporterLifecycle(t *testing.T) {
	mock := &mockLogExporter{}
	filter := &severityFilterExporter{
		wrapped:          mock,
		sampleRate:       1.0,
		alwaysSampleHigh: true,
	}

	// Test shutdown
	err := filter.Shutdown(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, mock.shutdownCount, "Shutdown should be called on wrapped exporter")

	// Test flush
	err = filter.ForceFlush(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, mock.flushCount, "ForceFlush should be called on wrapped exporter")
}

// TestWrapWithSeverityFilter_NoWrappingWhenUnnecessary verifies that if
// sample rate is 1.0 and alwaysSampleHigh is false, no wrapper is created.
func TestWrapWithSeverityFilterNoWrappingWhenUnnecessary(t *testing.T) {
	provider := &provider{
		config: Config{
			Logs: LogsConfig{
				Sample: LogSampleConfig{
					Rate:             Float64Ptr(1.0),
					AlwaysSampleHigh: BoolPtr(false),
				},
			},
		},
	}

	mock := &mockLogExporter{}
	result := provider.wrapWithSeverityFilter(mock)

	// Should return the original exporter unwrapped
	assert.Equal(t, mock, result, "Should not wrap when sample_rate=1.0 and always_sample_high=false")
}

// TestWrapWithSeverityFilter_WrapsWhenNeeded verifies that the wrapper is
// created when sampling or alwaysSampleHigh is configured.
func TestWrapWithSeverityFilterWrapsWhenNeeded(t *testing.T) {
	tests := []struct {
		name             string
		sampleRate       float64
		alwaysSampleHigh bool
		shouldWrap       bool
	}{
		{"Wrap for sample rate < 1.0", 0.5, false, true},
		{"Wrap for alwaysSampleHigh", 1.0, true, true},
		{"Wrap for both", 0.5, true, true},
		{"No wrap when both default", 1.0, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &provider{
				config: Config{
					Logs: LogsConfig{
						Sample: LogSampleConfig{
							Rate:             Float64Ptr(tt.sampleRate),
							AlwaysSampleHigh: BoolPtr(tt.alwaysSampleHigh),
						},
					},
				},
			}

			mock := &mockLogExporter{}
			result := provider.wrapWithSeverityFilter(mock)

			if tt.shouldWrap {
				assert.IsType(t, &severityFilterExporter{}, result, "Should wrap exporter")
			} else {
				assert.Equal(t, mock, result, "Should not wrap exporter")
			}
		})
	}
}

// Helper function to create a test log record with specified severity and timestamp
func createTestRecord(severity log.Severity, timestamp time.Time) sdklog.Record {
	var rec sdklog.Record
	rec.SetSeverity(severity)
	rec.SetTimestamp(timestamp)
	rec.SetBody(log.StringValue("test message"))
	return rec
}
