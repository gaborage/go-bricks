package observability

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	_, span := tracer.Start(context.Background(), "test-span")
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

func TestNewProviderUnsupportedEndpoint(t *testing.T) {
	cfg := &Config{
		Enabled:     true,
		ServiceName: testServiceName,
		Trace: TraceConfig{
			Enabled:    true,
			Endpoint:   "http://localhost:4318", // OTLP not supported yet in PR #1
			SampleRate: 1.0,
		},
	}

	provider, err := NewProvider(cfg)
	assert.Error(t, err)
	assert.Nil(t, provider)
	assert.Contains(t, err.Error(), "unsupported trace endpoint")
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

	// Create a context that will timeout immediately
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	time.Sleep(10 * time.Millisecond) // Ensure context is expired

	// Shutdown might still succeed if it's fast enough, but this tests the timeout path
	_ = provider.Shutdown(ctx)
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
