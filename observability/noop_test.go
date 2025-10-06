package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestNoopProvider_TracerProvider(t *testing.T) {
	provider := newNoopProvider()
	tp := provider.TracerProvider()
	assert.NotNil(t, tp)

	// Should return noop tracer provider
	_, ok := tp.(noop.TracerProvider)
	assert.True(t, ok, "expected noop.TracerProvider")
}

func TestNoopProvider_Shutdown(t *testing.T) {
	provider := newNoopProvider()
	err := provider.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestNoopProvider_ForceFlush(t *testing.T) {
	provider := newNoopProvider()
	err := provider.ForceFlush(context.Background())
	assert.NoError(t, err)
}

func TestNoopProvider_MultipleOperations(t *testing.T) {
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
