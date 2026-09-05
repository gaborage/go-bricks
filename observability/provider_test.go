package observability

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
)

const (
	// The two default OTLP ports, used here because nothing is expected to be
	// listening on them: these tests prove the pipeline is WIRED, not that a
	// collector answers. A developer running a real local collector changes what
	// they exercise — the export then succeeds instead of failing — but not
	// whether they pass, since each of them bounds the export below its own
	// Shutdown budget.
	testOTLPHTTPEndpoint = "http://localhost:4318"
	testOTLPGRPCEndpoint = "localhost:4317"
	testSpanName         = "test-span"
	testTracerName       = "test-tracer"
)

// The three durations the OTLP exporter tests use to make one race deterministic
// (#1162). Those tests aim a real exporter at a port with nothing listening, which
// is the point — they prove the pipeline is wired, not that a collector answers.
//
// Once the batch processor's timer starts an export, ForceFlush cannot take it
// back and Shutdown inherits it; that export runs on a BACKGROUND context, so the
// caller's Shutdown deadline does not cap it. With the development defaults —
// 500ms batch, 10s export — and the OTLP client's ~5s retry after a refused dial,
// Shutdown blocked past its 2s budget and returned "context deadline exceeded",
// but only when the timer happened to beat ForceFlush, which is why it flaked
// under CI load and passed locally.
//
// So the tests force that ordering instead of hoping to avoid it: the batch
// timeout makes the timer own the export, the tests then WAIT for the export to
// actually start before calling ForceFlush, and the export timeout bounds what
// Shutdown inherits, well under the 2s budget.
//
// The wait is a signal from the exporter, not a sleep. A sleep only proves time
// passed: if the processor's worker started late, ForceFlush would run the export
// itself on the test goroutine and both assertions would pass without the
// timer-owned path ever being exercised — the test would then guard nothing while
// looking like it did.
const (
	timerOwnedBatchTimeout    = time.Millisecond
	boundedInheritedExportTTL = 500 * time.Millisecond
	// A LIVENESS bound, not pacing. The wait ends on the export's own signal —
	// 0.01s on HTTP, 0.51s on gRPC — so nothing is spent by setting this
	// generously, and a tight value is actively wrong here: at 2s these tests
	// failed under a concurrent `make check`, which is the same contention the
	// flake they guard against needs. Its only job is turning a genuine hang into
	// a named failure well inside Go's 10m package timeout.
	timerOwnershipDeadline = 30 * time.Second
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

// TestNewProviderDelegatesToWithContext verifies that NewProvider still works
// end-to-end after being reduced to a NewProviderWithContext(background, cfg)
// delegation: a disabled config yields the no-op provider.
func TestNewProviderDelegatesToWithContext(t *testing.T) {
	cfg := &Config{Enabled: false}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)
	require.NotNil(t, provider)

	_, ok := provider.(*noopProvider)
	assert.True(t, ok, "NewProvider must still return the no-op provider when disabled")

	assert.NoError(t, provider.Shutdown(context.Background()))
}

// TestNewProviderWithContextDisabledIgnoresContext verifies that when
// observability is disabled, NewProviderWithContext returns the no-op provider
// regardless of the supplied context — even an already-canceled one. Disabled
// short-circuits before any resource detection or exporter setup, so the
// deadline is never consulted.
func TestNewProviderWithContextDisabledIgnoresContext(t *testing.T) {
	cfg := &Config{Enabled: false}

	// An already-canceled context must NOT cause an error on the disabled path.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	provider, err := NewProviderWithContext(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, provider)

	_, ok := provider.(*noopProvider)
	assert.True(t, ok, "disabled config must yield the no-op provider regardless of ctx")

	assert.NoError(t, provider.Shutdown(context.Background()))
}

func TestNewProviderWithContextNilConfigReturnsError(t *testing.T) {
	provider, err := NewProviderWithContext(context.Background(), nil)

	require.Error(t, err, "nil config must return a typed error, not panic")
	assert.Nil(t, provider)
	assert.Contains(t, err.Error(), "nil", "error should explain the nil config")
}

func TestNewProviderInvalidConfig(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Service: ServiceConfig{
			Name: "", // Missing required field
		},
	}

	provider, err := NewProvider(cfg)
	require.Error(t, err)
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
	tracer := tp.Tracer(testTracerName)
	assert.NotNil(t, tracer)

	// Should be able to start a span
	_, span := tracer.Start(context.Background(), testSpanName)
	assert.NotNil(t, span)
	span.End()

	// Should be able to flush
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = provider.ForceFlush(ctx)
	require.NoError(t, err)

	// Should be able to shutdown
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = provider.Shutdown(ctx)
	assert.NoError(t, err)
}

func TestNewProviderOTLPHTTPExporter(t *testing.T) {
	// Note: This test creates the exporter but does not actually send data
	// since we don't have a real OTLP collector running
	// Aimed at a dead port on purpose, with the three timing constants above
	// making the timer-versus-ForceFlush ordering deterministic (#1162).
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
			Batch:    BatchConfig{Timeout: timerOwnedBatchTimeout},
			Export:   ExportConfig{Timeout: boundedInheritedExportTTL},
			Sample: SampleConfig{
				Rate: Float64Ptr(1.0),
			},
		},
	}

	exportStart := installExportStartSignal(t)

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

	// Block until the batch timer has actually entered ExportSpans, so ForceFlush
	// below cannot be the thing that exports. See the const block above.
	awaitTimerOwnedExport(t, exportStart)

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
	// Aimed at a dead port on purpose, with the three timing constants above
	// making the timer-versus-ForceFlush ordering deterministic (#1162).
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
			Batch:    BatchConfig{Timeout: timerOwnedBatchTimeout},
			Export:   ExportConfig{Timeout: boundedInheritedExportTTL},
			Sample: SampleConfig{
				Rate: Float64Ptr(1.0),
			},
		},
	}

	exportStart := installExportStartSignal(t)

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

	// Block until the batch timer has actually entered ExportSpans, so ForceFlush
	// below cannot be the thing that exports. See the const block above.
	awaitTimerOwnedExport(t, exportStart)

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
			// Same dead port and the same 2s Shutdown assertion as the two exporter
			// tests, so the same timer-owned export can overrun it (#1162). This test
			// is about headers, not about the race, so it only needs the export
			// BOUNDED — it does not force the hostile ordering the way they do.
			Export: ExportConfig{Timeout: boundedInheritedExportTTL},
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
	require.Error(t, err)
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
	// We use a blocking exporter that only unblocks when context is canceled,
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
	require.Error(t, err, "expected error from shutdown timeout")
	assert.Contains(t, err.Error(), "failed to shutdown trace provider")

	// Cleanup: unblock the exporter
	close(blockingExporter.blockUntilCancel)
}

// blockingSpanExporter is a test exporter that blocks in Shutdown until context is canceled
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
	require.NoError(t, err)

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
	tracer := tp.Tracer(testTracerName)
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

// startUnimplementedGRPCServer serves gRPC on an ephemeral loopback port with no
// services registered, and returns its "host:port". Every RPC is answered with
// codes.Unimplemented, so an OTLP export fails at once instead of retrying a
// refused dial.
func startUnimplementedGRPCServer(t *testing.T) string {
	t.Helper()

	lc := net.ListenConfig{}
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer()
	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(srv.Stop)

	return lis.Addr().String()
}

func TestNewProviderEnvironmentAwareBatchTimeout(t *testing.T) {
	// A local gRPC server with no services registered: the export RPC fails
	// immediately with codes.Unimplemented, which the OTLP client does not
	// retry. A dead port instead makes Shutdown's inherited export burn the
	// full export timeout on dial retries (10s per gRPC case).
	grpcEndpoint := startUnimplementedGRPCServer(t)

	tests := []struct {
		name        string
		environment string
		endpoint    string
	}{
		{
			name:        "development_environment",
			environment: "development",
			endpoint:    grpcEndpoint,
		},
		{
			name:        "stdout_endpoint",
			environment: "production",
			endpoint:    EndpointStdout,
		},
		{
			name:        "production_environment",
			environment: "production",
			endpoint:    grpcEndpoint,
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
					Protocol: ProtocolGRPC, // gRPC for non-stdout tests
					Insecure: true,         // plain-TCP local listener, no TLS
				},
			}

			provider, err := NewProvider(cfg)
			require.NoError(t, err)
			assert.NotNil(t, provider)

			// Verify provider works by creating a span
			tp := provider.TracerProvider()
			assert.NotNil(t, tp)
			tracer := tp.Tracer(testTracerName)
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
	require.Error(t, err, "expected metrics init hook failure")
	assert.Nil(t, provider, "provider should be nil on failure")
	require.ErrorIs(t, err, metricsInitErr)
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
	require.Error(t, err, "expected logs init hook failure")
	assert.Nil(t, provider, "provider should be nil on failure")
	require.ErrorIs(t, err, logsInitErr)
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

	tracer := tp.Tracer(testTracerName)
	_, span := tracer.Start(context.Background(), testSpanName)
	assert.NotNil(t, span)
	span.End()

	// Proper shutdown (not cleanup)
	err = provider.Shutdown(context.Background())
	assert.True(t, recordingTraceExporter.ShutdownCalled(), "provider shutdown should trigger exporter shutdown")
	assert.NoError(t, err)
}

// exportStartedSpanExporter announces the first ExportSpans call. The OTLP
// exporter tests use it to observe that the BatchSpanProcessor's own timer
// claimed the export, which a sleep cannot establish — it signals on ENTRY, so
// the wait ends before the refused dial the export then blocks on.
type exportStartedSpanExporter struct {
	sdktrace.SpanExporter
	once    sync.Once
	started chan struct{}
}

func (e *exportStartedSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.once.Do(func() { close(e.started) })
	return e.SpanExporter.ExportSpans(ctx, spans)
}

// installExportStartSignal wraps the next provider's trace exporter so its first
// export is observable. It must run BEFORE NewProvider — the wrapper is consulted
// while the provider builds the exporter — and restores the previous wrapper on
// cleanup, since it is process-global.
func installExportStartSignal(t *testing.T) *exportStartedSpanExporter {
	t.Helper()

	signaling := &exportStartedSpanExporter{started: make(chan struct{})}
	prev := getTraceExporterWrapper()
	setTraceExporterWrapper(func(exporter sdktrace.SpanExporter) sdktrace.SpanExporter {
		// Composed, not replaced: saving prev only to restore it would still drop
		// whatever it does for the provider built while this one is installed. The
		// default is an identity wrapper (provider.go), and the production call
		// sites invoke the result unguarded, so prev is never nil by contract.
		signaling.SpanExporter = prev(exporter)
		return signaling
	})
	t.Cleanup(func() { setTraceExporterWrapper(prev) })

	return signaling
}

// awaitTimerOwnedExport blocks until the batch timer has entered ExportSpans, and
// fails the test rather than hanging if it never does — a test that silently
// proceeded here would be back to proving nothing about the ordering.
func awaitTimerOwnedExport(t *testing.T, e *exportStartedSpanExporter) {
	t.Helper()

	select {
	case <-e.started:
	case <-time.After(timerOwnershipDeadline):
		t.Fatal("the batch processor's timer never started an export; the ordering this test exists to exercise did not happen")
	}
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

// TestTracerProviderDoesNotRecordPanicValues pins ADR-081 at the provider seam.
// The OTel SDK's own span.End() calls recover() and stamps
// semconv.ExceptionMessage(fmt.Sprint(recovered)) — the VALUE — on any span that
// unwinds with a live panic, then re-raises. That reaches four framework
// `defer span.End()` sites with no first-party recover at all, so no call-site
// convention can cover it; only the provider option can.
func TestTracerProviderDoesNotRecordPanicValues(t *testing.T) {
	const secret = "not-a-real-secret-9021"

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		append(FrameworkTracerProviderOptions(), sdktrace.WithSyncer(exporter))...,
	)
	_, span := tp.Tracer("panic-recording").Start(context.Background(), "unwinding")

	func() {
		defer func() { _ = recover() }()
		defer span.End() // runs first, sees the live panic
		panic(secret)
	}()

	spans := exporter.GetSpans()
	// Without this the whole check is vacuous: an unexported span makes every
	// assertion below unreachable and the test passes having proven nothing.
	require.NotEmpty(t, spans, "the unwinding span never reached the exporter")

	for _, s := range spans {
		eventNames := make([]string, 0, len(s.Events))
		for _, ev := range s.Events {
			eventNames = append(eventNames, ev.Name)
			for _, attr := range ev.Attributes {
				assert.NotContains(t, attr.Value.String(), secret,
					"event attribute %q discloses the panic value", attr.Key)
			}
		}
		// Asserted over the COLLECTED names rather than inside the loop above:
		// WithoutPanicRecording leaves no events at all on the passing path, so a
		// per-event assertion only ever runs once the property is already broken.
		assert.NotContains(t, eventNames, "exception",
			"the SDK recorded an exception event for an unwinding panic")
		assert.NotContains(t, s.Status.Description, secret,
			"span status description discloses the panic value")
		for _, attr := range s.Attributes {
			assert.NotContains(t, attr.Value.String(), secret,
				"span attribute %q discloses the panic value", attr.Key)
		}
	}
}
