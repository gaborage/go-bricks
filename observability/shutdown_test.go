package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/metric"
	metricznoop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// mockProvider is a test provider for testing shutdown behavior.
type mockProvider struct {
	shutdownErr    error
	shutdownCalled bool
	flushCalled    bool
}

func (m *mockProvider) TracerProvider() trace.TracerProvider {
	return noop.NewTracerProvider()
}

func (m *mockProvider) MeterProvider() metric.MeterProvider {
	return metricznoop.NewMeterProvider()
}

func (m *mockProvider) LoggerProvider() *sdklog.LoggerProvider {
	return nil
}

func (m *mockProvider) ShouldDisableStdout() bool {
	return false
}

func (m *mockProvider) Shutdown(_ context.Context) error {
	m.shutdownCalled = true
	return m.shutdownErr
}

func (m *mockProvider) ForceFlush(_ context.Context) error {
	m.flushCalled = true
	return nil
}

func TestShutdownSuccess(t *testing.T) {
	mock := &mockProvider{}

	err := Shutdown(mock, 1*time.Second)
	assert.NoError(t, err)
	assert.True(t, mock.shutdownCalled)
}

func TestShutdownNilProvider(t *testing.T) {
	assert.NoError(t, Shutdown(nil, time.Second))
}

func TestShutdownError(t *testing.T) {
	expectedErr := errors.New("shutdown failed")
	mock := &mockProvider{shutdownErr: expectedErr}

	err := Shutdown(mock, 1*time.Second)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "observability shutdown failed")
	assert.True(t, mock.shutdownCalled)
}

func TestShutdownDefaultTimeout(t *testing.T) {
	mock := &mockProvider{}

	err := Shutdown(mock, 0) // Should use DefaultShutdownTimeout
	assert.NoError(t, err)
	assert.True(t, mock.shutdownCalled)
}

func TestShutdownNegativeTimeout(t *testing.T) {
	mock := &mockProvider{}

	err := Shutdown(mock, -1*time.Second) // Should use DefaultShutdownTimeout
	assert.NoError(t, err)
	assert.True(t, mock.shutdownCalled)
}

func TestMustShutdownSuccess(t *testing.T) {
	mock := &mockProvider{}

	assert.NotPanics(t, func() {
		MustShutdown(mock, 1*time.Second)
	})
	assert.True(t, mock.shutdownCalled)
}

func TestMustShutdownNilProvider(t *testing.T) {
	assert.NotPanics(t, func() {
		MustShutdown(nil, time.Second)
	})
}

func TestMustShutdownPanic(t *testing.T) {
	expectedErr := errors.New("shutdown failed")
	mock := &mockProvider{shutdownErr: expectedErr}

	assert.Panics(t, func() {
		MustShutdown(mock, 1*time.Second)
	})
	assert.True(t, mock.shutdownCalled)
}

func TestForceFlushBothProvidersFail(t *testing.T) {
	// Test that ForceFlush aggregates errors from both trace and meter providers
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
	}

	provider, err := NewProvider(cfg)
	assert.NoError(t, err)

	// Create a context that expires immediately to force errors
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately to cause flush errors

	// ForceFlush should return an error aggregating both providers' errors
	err = provider.ForceFlush(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "flush errors")

	// Cleanup
	shutdownCtx := context.Background()
	_ = provider.Shutdown(shutdownCtx)
}
