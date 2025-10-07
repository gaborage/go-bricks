package observability

import (
	"context"

	"go.opentelemetry.io/otel/metric"
	metricznoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// noopProvider implements Provider with no-op operations.
// Used when observability is disabled, ensuring zero overhead.
type noopProvider struct {
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
}

// newNoopProvider creates a new no-op provider.
// All operations are no-ops with minimal overhead.
func newNoopProvider() *noopProvider {
	return &noopProvider{
		tracerProvider: noop.NewTracerProvider(),
		meterProvider:  metricznoop.NewMeterProvider(),
	}
}

// TracerProvider returns a no-op tracer provider.
func (n *noopProvider) TracerProvider() trace.TracerProvider {
	return n.tracerProvider
}

// MeterProvider returns a no-op meter provider.
func (n *noopProvider) MeterProvider() metric.MeterProvider {
	return n.meterProvider
}

// Shutdown is a no-op for the no-op provider.
// Always returns nil since there's nothing to clean up.
func (n *noopProvider) Shutdown(_ context.Context) error {
	return nil
}

// ForceFlush is a no-op for the no-op provider.
// Always returns nil since there's nothing to flush.
func (n *noopProvider) ForceFlush(_ context.Context) error {
	return nil
}
