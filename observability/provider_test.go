package observability

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
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
		Enabled:     true,
		ServiceName: "", // Missing required field
	}

	provider, err := NewProvider(cfg)
	assert.Error(t, err)
	assert.Nil(t, provider)
	assert.ErrorIs(t, err, ErrMissingServiceName)
}

func TestNewProviderTracingEnabled(t *testing.T) {
	cfg := &Config{
		Enabled:        true,
		ServiceName:    testServiceName,
		ServiceVersion: "1.0.0",
		Environment:    "test",
		Trace: TraceConfig{
			Enabled:       true,
			Endpoint:      "stdout",
			SampleRate:    1.0,
			BatchTimeout:  100 * time.Millisecond,
			ExportTimeout: 1 * time.Second,
			MaxQueueSize:  100,
			MaxBatchSize:  10,
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
		Enabled:     true,
		ServiceName: testServiceName,
		Trace: TraceConfig{
			Enabled:    true,
			Endpoint:   testOTLPHTTPEndpoint,
			Protocol:   "http",
			Insecure:   true,
			SampleRate: 1.0,
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
		Enabled:     true,
		ServiceName: testServiceName,
		Trace: TraceConfig{
			Enabled:    true,
			Endpoint:   testOTLPGRPCEndpoint,
			Protocol:   "grpc",
			Insecure:   true,
			SampleRate: 1.0,
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
		Enabled:     true,
		ServiceName: testServiceName,
		Trace: TraceConfig{
			Enabled:  true,
			Endpoint: testOTLPHTTPEndpoint,
			Protocol: "http",
			Insecure: true,
			Headers: map[string]string{
				"Authorization":   "Bearer test-token",
				"X-Custom-Header": "custom-value",
			},
			SampleRate: 1.0,
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
		Enabled:     true,
		ServiceName: testServiceName,
		Trace: TraceConfig{
			Enabled:    true,
			Endpoint:   testOTLPHTTPEndpoint,
			Protocol:   "websocket",
			SampleRate: 1.0,
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
				Enabled:     true,
				ServiceName: testServiceName,
				Trace: TraceConfig{
					Enabled:    true,
					Endpoint:   "stdout",
					SampleRate: tt.sampleRate,
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
			Enabled:     true,
			ServiceName: testServiceName,
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
		Enabled:     true,
		ServiceName: testServiceName,
		Trace: TraceConfig{
			Enabled:    true,
			Endpoint:   "stdout",
			SampleRate: 1.0,
		},
	}

	provider, err := NewProvider(cfg)
	require.NoError(t, err)

	// First shutdown
	err = provider.Shutdown(context.Background())
	assert.NoError(t, err)

	// Second shutdown should not panic or error
	err = provider.Shutdown(context.Background())
	assert.NoError(t, err)
}
