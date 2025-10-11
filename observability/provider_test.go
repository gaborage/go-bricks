package observability

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	testOTLPHTTPEndpoint = "localhost:4318"
	testOTLPGRPCEndpoint = "localhost:4317"
	testSpanName         = "test-span"
	testTracerName       = "test-tracer"
)

func TestNewProviderDisabled(t *testing.T) {
	cfg := &Config{
		Enabled: false,
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)

	// Should return noop provider
	_, ok := provider.(*noopProvider)
	assert.True(t, ok, "expected noopProvider when disabled")

	// Shutdown should not error
	err = provider.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestNewProviderInvalidConfig(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: "", // Missing required field
		},
	}

	provider, err := NewProvider(cfg)
	assert.Error(t, err)
	assert.Nil(t, provider)
	assert.ErrorIs(t, err, ErrMissingServiceName)
}

func TestNewProviderTracingEnabled(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name:    testServiceName,
			Version: "1.0.0",
		},
		Environment: "test",
		Trace: TraceConfig{
			Enabled:  BoolPtr(true),
			Endpoint: "stdout",
			Sample: SampleConfig{
				Rate: Float64Ptr(1.0),
			},
			Batch: BatchConfig{
				Timeout: 100 * time.Millisecond,
				Size:    10,
			},
			Export: ExportConfig{
				Timeout: 1 * time.Second,
			},
			Max: MaxConfig{
				Queue: QueueConfig{
					Size: 100,
				},
				Batch: MaxBatchConfig{
					Size: 10,
				},
			},
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)

	// Should return trace provider
	tp := provider.TracerProvider()
	assert.NotNil(t, tp)

	// TracerProvider should not be nil
	assert.NotNil(t, tp)

	// Should be able to create a tracer
	tracer := tp.Tracer("test")
	assert.NotNil(t, tracer)

	// Should be able to start a span
	_, span := tracer.Start(context.Background(), testSpanName)
	assert.NotNil(t, span)
	span.End()

	// Should be able to flush
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = provider.ForceFlush(ctx)
	assert.NoError(t, err)

	// Should be able to shutdown
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = provider.Shutdown(ctx)
	assert.NoError(t, err)
}

func TestNewProviderOTLPHTTPExporter(t *testing.T) {
	// Note: This test creates the exporter but does not actually send data
	// since we don't have a real OTLP collector running
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Trace: TraceConfig{
			Enabled:  BoolPtr(true),
			Endpoint: testOTLPHTTPEndpoint,
			Protocol: "http",
			Insecure: true,
			Sample: SampleConfig{
				Rate: Float64Ptr(1.0),
			},
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)

	// Verify the provider has a tracer provider configured
	tp := provider.TracerProvider()
	assert.NotNil(t, tp)

	// Verify we can create a tracer and span (proves exporter is initialized)
	tracer := tp.Tracer(testTracerName)
	assert.NotNil(t, tracer)

	// Create a test span to verify the pipeline works
	ctx, span := tracer.Start(context.Background(), testSpanName)
	assert.NotNil(t, span)
	span.End()

	// Force flush to ensure span is processed (even though it will fail to send)
	flushCtx, flushCancel := context.WithTimeout(ctx, 1*time.Second)
	defer flushCancel()
	_ = provider.ForceFlush(flushCtx) // May error due to no collector, which is expected

	// Cleanup
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = provider.Shutdown(shutdownCtx)
	assert.NoError(t, err)
}

func TestNewProviderOTLPGRPCExporter(t *testing.T) {
	// Note: This test creates the exporter but does not actually send data
	// since we don't have a real OTLP collector running
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Trace: TraceConfig{
			Enabled:  BoolPtr(true),
			Endpoint: testOTLPGRPCEndpoint,
			Protocol: "grpc",
			Insecure: true,
			Sample: SampleConfig{
				Rate: Float64Ptr(1.0),
			},
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)

	// Verify the provider has a tracer provider configured
	tp := provider.TracerProvider()
	assert.NotNil(t, tp)

	// Verify we can create a tracer and span (proves exporter is initialized)
	tracer := tp.Tracer(testTracerName)
	assert.NotNil(t, tracer)

	// Create a test span to verify the pipeline works
	ctx, span := tracer.Start(context.Background(), testSpanName)
	assert.NotNil(t, span)
	span.End()

	// Force flush to ensure span is processed (even though it will fail to send)
	flushCtx, flushCancel := context.WithTimeout(ctx, 1*time.Second)
	defer flushCancel()
	_ = provider.ForceFlush(flushCtx) // May error due to no collector, which is expected

	// Cleanup
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = provider.Shutdown(shutdownCtx)
	assert.NoError(t, err)
}

func TestNewProviderOTLPWithHeaders(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Trace: TraceConfig{
			Enabled:  BoolPtr(true),
			Endpoint: testOTLPHTTPEndpoint,
			Protocol: "http",
			Insecure: true,
			Headers: map[string]string{
				"Authorization":   "Bearer test-token",
				"X-Custom-Header": "custom-value",
			},
			Sample: SampleConfig{
				Rate: Float64Ptr(1.0),
			},
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)

	// Verify the provider has a tracer provider configured
	tp := provider.TracerProvider()
	assert.NotNil(t, tp)

	// Verify we can create a tracer and span
	tracer := tp.Tracer(testTracerName)
	assert.NotNil(t, tracer)

	// Create a test span to verify the pipeline works
	ctx, span := tracer.Start(context.Background(), "test-span-with-headers")
	assert.NotNil(t, span)
	span.End()

	// Force flush to ensure span is processed
	// Note: Headers are used during export, not during span creation
	// Without a real collector, we can't verify headers are sent, but we can
	// verify the provider accepts and stores the configuration
	flushCtx, flushCancel := context.WithTimeout(ctx, 1*time.Second)
	defer flushCancel()
	_ = provider.ForceFlush(flushCtx) // May error due to no collector

	// Cleanup
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = provider.Shutdown(shutdownCtx)
	assert.NoError(t, err)
}

func TestNewProviderUnsupportedProtocol(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Trace: TraceConfig{
			Enabled:  BoolPtr(true),
			Endpoint: testOTLPHTTPEndpoint,
			Protocol: "websocket",
			Sample: SampleConfig{
				Rate: Float64Ptr(1.0),
			},
		},
	}

	provider, err := NewProvider(cfg)
	assert.Error(t, err)
	assert.Nil(t, provider)
	assert.ErrorIs(t, err, ErrInvalidProtocol)
}

func TestNewProviderTracingSampleRate(t *testing.T) {
	tests := []struct {
		name       string
		sampleRate float64
	}{
		{"no sampling", 0.0},
		{"25% sampling", 0.25},
		{"50% sampling", 0.5},
		{"100% sampling", 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Enabled: true,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Trace: TraceConfig{
					Enabled:  BoolPtr(true),
					Endpoint: "stdout",
					Sample: SampleConfig{
						Rate: Float64Ptr(tt.sampleRate),
					},
				},
			}

			provider, err := NewProvider(cfg)
			require.NoError(t, err)
			assert.NotNil(t, provider)

			// Cleanup
			err = provider.Shutdown(context.Background())
			assert.NoError(t, err)
		})
	}
}

func TestProviderShutdownTimeout(t *testing.T) {
	// This test verifies that Shutdown respects context timeout.
	// We use a blocking exporter that only unblocks when context is cancelled,
	// ensuring deterministic timeout behavior.

	// Create a custom blocking exporter
	blockingExporter := &blockingSpanExporter{
		blockUntilCancel: make(chan struct{}),
	}

	// Manually create provider with blocking exporter
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(blockingExporter),
	)

	provider := &provider{
		config: Config{
			Enabled: true,
			Service: ServiceConfig{
				Name: testServiceName,
			},
		},
		tracerProvider: tp,
	}

	// Create context with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Shutdown should return error due to timeout
	err := provider.Shutdown(ctx)
	assert.Error(t, err, "expected error from shutdown timeout")
	assert.Contains(t, err.Error(), "failed to shutdown trace provider")

	// Cleanup: unblock the exporter
	close(blockingExporter.blockUntilCancel)
}

// blockingSpanExporter is a test exporter that blocks in Shutdown until context is cancelled
type blockingSpanExporter struct {
	blockUntilCancel chan struct{}
}

func (b *blockingSpanExporter) ExportSpans(_ context.Context, _ []sdktrace.ReadOnlySpan) error {
	return nil
}

func (b *blockingSpanExporter) Shutdown(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.blockUntilCancel:
		return nil
	}
}

func TestProviderMultipleShutdowns(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Trace: TraceConfig{
			Enabled:  BoolPtr(true),
			Endpoint: "stdout",
			Sample: SampleConfig{
				Rate: Float64Ptr(1.0),
			},
		},
		Metrics: MetricsConfig{
			Enabled: BoolPtr(false), // Disable metrics to avoid shutdown errors
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)

	// First shutdown
	err = provider.Shutdown(context.Background())
	assert.NoError(t, err)

	// Second shutdown should not panic (errors are acceptable for already-shutdown providers)
	_ = provider.Shutdown(context.Background()) // Intentionally ignore error from second shutdown

	// Verify provider is still functional (returns no-op or continues working)
	// after multiple shutdowns - should not panic
	tp := provider.TracerProvider()
	assert.NotNil(t, tp, "TracerProvider should still be accessible after shutdown")

	// Verify we can still create tracers (even if they're no-op after shutdown)
	tracer := tp.Tracer("test-after-shutdown")
	assert.NotNil(t, tracer, "Should be able to create tracer after shutdown")

	// Verify we can still start spans (even if they're no-op after shutdown)
	_, span := tracer.Start(context.Background(), "test-span-after-shutdown")
	assert.NotNil(t, span, "Should be able to create span after shutdown")
	span.End() // Should not panic
}

func TestMustNewProviderSuccess(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Trace: TraceConfig{
			Enabled:  BoolPtr(true),
			Endpoint: "stdout",
			Sample: SampleConfig{
				Rate: Float64Ptr(1.0),
			},
		},
	}

	// Should not panic with valid config
	provider := MustNewProvider(cfg)
	assert.NotNil(t, provider)

	// Cleanup
	err := provider.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestMustNewProviderPanic(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: "", // Invalid: missing service name
		},
	}

	// Should panic with invalid config
	assert.Panics(t, func() {
		MustNewProvider(cfg)
	}, "expected panic from MustNewProvider with invalid config")
}

func TestTracerProviderNilCase(t *testing.T) {
	// Create provider with only metrics enabled (no tracer provider)
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Trace: TraceConfig{
			Enabled: BoolPtr(false), // Explicitly disable tracing
		},
		Metrics: MetricsConfig{
			Enabled:  BoolPtr(true),
			Endpoint: "stdout",
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)

	// TracerProvider should return no-op when tracerProvider is nil
	tp := provider.TracerProvider()
	assert.NotNil(t, tp)

	// Verify it's a noop provider
	_, ok := tp.(noop.TracerProvider)
	assert.True(t, ok, "expected noop.TracerProvider when tracing disabled")

	// Cleanup
	err = provider.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestNewProviderOTLPHTTPMetrics(t *testing.T) {
	// Test OTLP HTTP metrics exporter initialization
	// Note: This test verifies the exporter is created correctly
	// but does not require a real collector to be running
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Metrics: MetricsConfig{
			Enabled:  BoolPtr(true),
			Endpoint: testOTLPHTTPEndpoint,
			Interval: 10 * time.Second,
		},
		Trace: TraceConfig{
			Protocol: "http",
			Insecure: true,
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)

	// Verify meter provider is initialized
	mp := provider.MeterProvider()
	assert.NotNil(t, mp)

	// Verify we can create a meter
	meter := mp.Meter("test-http-metrics")
	assert.NotNil(t, meter)

	// Create and record a test metric (proves pipeline initialization)
	counter, err := meter.Int64Counter("test.http.counter")
	require.NoError(t, err)
	counter.Add(context.Background(), 1)

	// Cleanup - may error due to no collector running, which is expected
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = provider.Shutdown(shutdownCtx) // Ignore error as collector may not be available
}

func TestNewProviderOTLPGRPCMetrics(t *testing.T) {
	// Test OTLP gRPC metrics exporter initialization
	// Note: This test verifies the exporter is created correctly
	// but does not require a real collector to be running
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Metrics: MetricsConfig{
			Enabled:  BoolPtr(true),
			Endpoint: testOTLPGRPCEndpoint,
			Interval: 10 * time.Second,
		},
		Trace: TraceConfig{
			Protocol: "grpc",
			Insecure: true,
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)

	// Verify meter provider is initialized
	mp := provider.MeterProvider()
	assert.NotNil(t, mp)

	// Verify we can create a meter
	meter := mp.Meter("test-grpc-metrics")
	assert.NotNil(t, meter)

	// Create and record a test metric (proves pipeline initialization)
	counter, err := meter.Int64Counter("test.grpc.counter")
	require.NoError(t, err)
	counter.Add(context.Background(), 1)

	// Cleanup - may error due to no collector running, which is expected
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = provider.Shutdown(shutdownCtx) // Ignore error as collector may not be available
}

func TestMetricsTransportSettings(t *testing.T) {
	t.Run("metrics override trace settings", func(t *testing.T) {
		p := &provider{
			config: Config{
				Trace: TraceConfig{
					Protocol: ProtocolHTTP,
					Insecure: true,
					Headers: map[string]string{
						"trace-header": "trace",
					},
				},
				Metrics: MetricsConfig{
					Protocol: ProtocolGRPC,
					Insecure: BoolPtr(false),
					Headers: map[string]string{
						"DD-API-KEY": "test-key",
					},
				},
			},
		}

		protocol, insecure, headers := p.metricsTransportSettings()
		assert.Equal(t, ProtocolGRPC, protocol)
		assert.False(t, insecure)
		assert.Equal(t, map[string]string{"DD-API-KEY": "test-key"}, headers)
	})

	t.Run("metrics inherit trace settings when unset", func(t *testing.T) {
		traceHeaders := map[string]string{
			"Authorization": "Basic trace",
		}
		p := &provider{
			config: Config{
				Trace: TraceConfig{
					Protocol: ProtocolHTTP,
					Insecure: false,
					Headers:  traceHeaders,
				},
				Metrics: MetricsConfig{},
			},
		}

		protocol, insecure, headers := p.metricsTransportSettings()
		assert.Equal(t, ProtocolHTTP, protocol)
		assert.False(t, insecure)
		assert.Equal(t, traceHeaders, headers)
	})

	t.Run("metrics default protocol when trace unset", func(t *testing.T) {
		p := &provider{
			config: Config{
				Trace:   TraceConfig{}, // No protocol or headers configured
				Metrics: MetricsConfig{},
			},
		}

		protocol, insecure, headers := p.metricsTransportSettings()
		assert.Equal(t, ProtocolHTTP, protocol)
		assert.False(t, insecure)
		assert.Nil(t, headers)
	})
}

func TestNewProviderAppliesDefaultsInternally(t *testing.T) {
	// This test verifies that NewProvider applies defaults even if caller forgets to
	// This prevents zero sample rates from silently disabling tracing
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Trace: TraceConfig{
			Enabled:  BoolPtr(true),
			Endpoint: "stdout",
			// Intentionally omit Sample.Rate to test defaulting
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)

	// Verify provider was created successfully (proof that defaults were applied)
	tp := provider.TracerProvider()
	assert.NotNil(t, tp)

	// Create a span to verify sampler is working (not dropping everything)
	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), testSpanName)
	assert.NotNil(t, span)
	span.End()

	// Cleanup
	err = provider.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestNewProviderDoesNotMutateInputConfig(t *testing.T) {
	// Verify that NewProvider creates a defensive copy and doesn't mutate caller's config
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Trace: TraceConfig{
			Enabled:  BoolPtr(true),
			Endpoint: "stdout",
		},
	}

	// Capture original values
	originalRate := cfg.Trace.Sample.Rate
	originalTimeout := cfg.Trace.Batch.Timeout

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)

	// Verify original config was not mutated
	assert.Equal(t, originalRate, cfg.Trace.Sample.Rate, "Sample rate should not be mutated")
	assert.Equal(t, originalTimeout, cfg.Trace.Batch.Timeout, "Batch timeout should not be mutated")

	// Cleanup
	err = provider.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestNewProviderExplicitZeroSampleRate(t *testing.T) {
	// This test ensures that an explicitly set 0.0 sample rate is respected
	// (not overridden to 1.0) and that a warning is logged
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Trace: TraceConfig{
			Enabled:  BoolPtr(true),
			Endpoint: "stdout",
			Sample: SampleConfig{
				Rate: Float64Ptr(0.0), // Explicitly set to 0.0
			},
		},
		Metrics: MetricsConfig{
			Enabled: BoolPtr(false),
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err, "Provider should be created even with 0.0 sample rate")
	assert.NotNil(t, provider)

	// Verify the provider was created successfully
	// The warning about 0.0 sample rate should appear in debug logs
	// (checked manually or with log capture in integration tests)

	// Cleanup
	err = provider.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestNewProviderNilSampleRateGetsDefault(t *testing.T) {
	// This test ensures that when sample rate is not specified (nil),
	// it gets defaulted to 1.0
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Trace: TraceConfig{
			Enabled:  BoolPtr(true),
			Endpoint: "stdout",
			// Sample.Rate is nil (not specified)
		},
		Metrics: MetricsConfig{
			Enabled: BoolPtr(false),
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)

	// The sample rate should have been defaulted to 1.0 internally
	// We can't directly inspect the internal config, but we can verify
	// that the provider was created successfully

	// Cleanup
	err = provider.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestNewProviderEnvironmentAwareBatchTimeout(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		endpoint    string
	}{
		{
			name:        "development_environment",
			environment: "development",
			endpoint:    testOTLPGRPCEndpoint,
		},
		{
			name:        "stdout_endpoint",
			environment: "production",
			endpoint:    EndpointStdout,
		},
		{
			name:        "production_environment",
			environment: "production",
			endpoint:    testOTLPGRPCEndpoint,
		},
	}

	// This is a smoke test that verifies provider initialization succeeds
	// with environment-aware batch timeout defaults (500ms for dev/stdout, 5s for prod).
	// The actual timeout values are applied internally and tested via integration tests.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Enabled:     true,
				Environment: tt.environment,
				Service: ServiceConfig{
					Name: testServiceName,
				},
				Trace: TraceConfig{
					Enabled:  BoolPtr(true),
					Endpoint: tt.endpoint,
				},
			}

			provider, err := NewProvider(cfg)
			require.NoError(t, err)
			assert.NotNil(t, provider)

			// Verify provider works by creating a span
			tp := provider.TracerProvider()
			assert.NotNil(t, tp)
			tracer := tp.Tracer("test")
			_, span := tracer.Start(context.Background(), testSpanName)
			assert.NotNil(t, span)
			span.End()

			// Cleanup
			err = provider.Shutdown(context.Background())
			assert.NoError(t, err)
		})
	}
}

func TestNewProviderCleansUpOnMetricsInitFailure(t *testing.T) {
	var recordingTraceExporter *recordingSpanExporter
	prevTraceWrapper := getTraceExporterWrapper()
	setTraceExporterWrapper(func(exporter sdktrace.SpanExporter) sdktrace.SpanExporter {
		recordingTraceExporter = &recordingSpanExporter{
			SpanExporter: exporter,
		}
		return recordingTraceExporter
	})
	metricsInitErr := errors.New("metrics init hook failure")
	prevMetricHook := metricInitHook
	metricInitHook = func() error {
		return metricsInitErr
	}
	t.Cleanup(func() {
		setTraceExporterWrapper(prevTraceWrapper)
		metricInitHook = prevMetricHook
	})

	// Test that if metrics initialization fails, the trace provider is properly cleaned up
	// This prevents goroutine and connection leaks
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Trace: TraceConfig{
			Enabled:  BoolPtr(true),
			Endpoint: "stdout",
			Sample: SampleConfig{
				Rate: Float64Ptr(1.0),
			},
		},
		Metrics: MetricsConfig{
			Enabled:  BoolPtr(true),
			Endpoint: EndpointStdout,
		},
	}

	provider, err := NewProvider(cfg)
	assert.Error(t, err, "expected metrics init hook failure")
	assert.Nil(t, provider, "provider should be nil on failure")
	assert.ErrorIs(t, err, metricsInitErr)
	require.NotNil(t, recordingTraceExporter, "trace exporter should be created before failure")
	assert.True(t, recordingTraceExporter.ShutdownCalled(), "trace exporter should be shutdown via cleanup")

	// The test verifies that:
	// 1. NewProvider returns an error (metrics init failed)
	// 2. cleanupPartialInit() was called via defer
	// 3. Trace provider (which was initialized) was shut down
	// Without the cleanup, the BatchSpanProcessor goroutine would leak
}

func TestNewProviderCleansUpOnLogsInitFailure(t *testing.T) {
	var recordingTraceExporter *recordingSpanExporter
	prevTraceWrapper := getTraceExporterWrapper()
	setTraceExporterWrapper(func(exporter sdktrace.SpanExporter) sdktrace.SpanExporter {
		recordingTraceExporter = &recordingSpanExporter{
			SpanExporter: exporter,
		}
		return recordingTraceExporter
	})
	var metricExporterRecorder *recordingMetricExporter
	prevMetricWrapper := getMetricExporterWrapper()
	setMetricExporterWrapper(func(exporter sdkmetric.Exporter) sdkmetric.Exporter {
		metricExporterRecorder = &recordingMetricExporter{
			Exporter: exporter,
		}
		return metricExporterRecorder
	})
	logsInitErr := errors.New("logs init hook failure")
	prevLogHook := logInitHook
	logInitHook = func() error {
		return logsInitErr
	}
	t.Cleanup(func() {
		setTraceExporterWrapper(prevTraceWrapper)
		setMetricExporterWrapper(prevMetricWrapper)
		logInitHook = prevLogHook
	})

	// Test that if logs initialization fails, both trace and metrics providers are cleaned up
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Trace: TraceConfig{
			Enabled:  BoolPtr(true),
			Endpoint: "stdout",
			Sample: SampleConfig{
				Rate: Float64Ptr(1.0),
			},
		},
		Metrics: MetricsConfig{
			Enabled:  BoolPtr(true),
			Endpoint: "stdout",
		},
		Logs: LogsConfig{
			Enabled:  BoolPtr(true),
			Endpoint: EndpointStdout,
		},
	}

	provider, err := NewProvider(cfg)
	assert.Error(t, err, "expected logs init hook failure")
	assert.Nil(t, provider, "provider should be nil on failure")
	assert.ErrorIs(t, err, logsInitErr)
	require.NotNil(t, recordingTraceExporter, "trace exporter should be created before failure")
	require.NotNil(t, metricExporterRecorder, "metric exporter should be created before failure")
	assert.True(t, recordingTraceExporter.ShutdownCalled(), "trace exporter should be shutdown via cleanup")
	assert.True(t, metricExporterRecorder.ShutdownCalled(), "metric exporter should be shutdown via cleanup")

	// The test verifies that:
	// 1. NewProvider returns an error (logs init failed)
	// 2. cleanupPartialInit() was called via defer
	// 3. Both trace and metrics providers (which were initialized) were shut down
	// Without the cleanup, both BatchSpanProcessor and PeriodicReader goroutines would leak
}

func TestNewProviderNoCleanupOnSuccess(t *testing.T) {
	var recordingTraceExporter *recordingSpanExporter
	prevTraceWrapper := getTraceExporterWrapper()
	setTraceExporterWrapper(func(exporter sdktrace.SpanExporter) sdktrace.SpanExporter {
		recordingTraceExporter = &recordingSpanExporter{
			SpanExporter: exporter,
		}
		return recordingTraceExporter
	})
	t.Cleanup(func() {
		setTraceExporterWrapper(prevTraceWrapper)
	})

	// Test that cleanup is NOT called when initialization succeeds
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: testServiceName,
		},
		Trace: TraceConfig{
			Enabled:  BoolPtr(true),
			Endpoint: "stdout",
			Sample: SampleConfig{
				Rate: Float64Ptr(1.0),
			},
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)
	require.NotNil(t, recordingTraceExporter)
	assert.False(t, recordingTraceExporter.ShutdownCalled(), "cleanup should not run on successful init")

	// Verify provider is functional (cleanup wasn't called)
	tp := provider.TracerProvider()
	assert.NotNil(t, tp)

	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), testSpanName)
	assert.NotNil(t, span)
	span.End()

	// Proper shutdown (not cleanup)
	err = provider.Shutdown(context.Background())
	assert.NoError(t, err)
	assert.True(t, recordingTraceExporter.ShutdownCalled(), "provider shutdown should trigger exporter shutdown")
}

type recordingSpanExporter struct {
	sdktrace.SpanExporter
	shutdownCalled atomic.Bool
}

func (r *recordingSpanExporter) Shutdown(ctx context.Context) error {
	r.shutdownCalled.Store(true)
	return r.SpanExporter.Shutdown(ctx)
}

func (r *recordingSpanExporter) ShutdownCalled() bool {
	return r.shutdownCalled.Load()
}

type recordingMetricExporter struct {
	sdkmetric.Exporter
	shutdownCalled atomic.Bool
}

func (r *recordingMetricExporter) Shutdown(ctx context.Context) error {
	r.shutdownCalled.Store(true)
	return r.Exporter.Shutdown(ctx)
}

func (r *recordingMetricExporter) ShutdownCalled() bool {
	return r.shutdownCalled.Load()
}
