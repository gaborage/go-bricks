package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestNoopProviderTracerProvider(t *testing.T) {
	provider := newNoopProvider()
	tp := provider.TracerProvider()
	assert.NotNil(t, tp)

	// Should return noop tracer provider
	_, ok := tp.(noop.TracerProvider)
	assert.True(t, ok, "expected noop.TracerProvider")
}

func TestNoopProviderMeterProvider(t *testing.T) {
	provider := newNoopProvider()
	mp := provider.MeterProvider()
	assert.NotNil(t, mp)

	// Should be able to create a meter from the provider
	meter := mp.Meter("test-meter")
	assert.NotNil(t, meter)
}

func TestNoopProviderShutdown(t *testing.T) {
	provider := newNoopProvider()
	err := provider.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestNoopProviderForceFlush(t *testing.T) {
	provider := newNoopProvider()
	err := provider.ForceFlush(context.Background())
	assert.NoError(t, err)
}

func TestNoopProviderMultipleOperations(t *testing.T) {
	provider := newNoopProvider()

	// Multiple calls should not error
	err := provider.ForceFlush(context.Background())
	assert.NoError(t, err)

	err = provider.Shutdown(context.Background())
	assert.NoError(t, err)

	err = provider.Shutdown(context.Background())
	assert.NoError(t, err)

	// TracerProvider should still work after shutdown
	tp := provider.TracerProvider()
	assert.NotNil(t, tp)
}
