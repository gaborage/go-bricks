package observability

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Provider is the interface for observability providers.
// It manages the lifecycle of tracing and metrics providers.
type Provider interface {
	// TracerProvider returns the configured trace provider.
	TracerProvider() trace.TracerProvider

	// Shutdown gracefully shuts down the provider, flushing any pending data.
	// It should be called during application shutdown.
	Shutdown(ctx context.Context) error

	// ForceFlush immediately flushes any pending telemetry data.
	// Useful before critical operations or shutdown.
	ForceFlush(ctx context.Context) error
}

// provider implements Provider with OpenTelemetry SDK.
type provider struct {
	config         Config
	tracerProvider *sdktrace.TracerProvider
	mu             sync.Mutex
}

// NewProvider creates a new observability provider based on the configuration.
// If observability is disabled, returns a no-op provider.
// If enabled, initializes OpenTelemetry with the configured exporters.
func NewProvider(cfg *Config) (Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid observability config: %w", err)
	}

	// Return no-op provider if observability is disabled
	if !cfg.Enabled {
		return newNoopProvider(), nil
	}

	p := &provider{
		config: *cfg,
	}

	// Initialize trace provider if tracing is enabled
	if cfg.Trace.Enabled {
		if err := p.initTraceProvider(); err != nil {
			return nil, fmt.Errorf("failed to initialize trace provider: %w", err)
		}
	}

	// Set global providers
	if p.tracerProvider != nil {
		otel.SetTracerProvider(p.tracerProvider)
	}

	// Set global propagator for W3C trace context
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return p, nil
}

// initTraceProvider initializes the OpenTelemetry trace provider.
func (p *provider) initTraceProvider() error {
	// Create resource with service information
	res, err := p.createResource()
	if err != nil {
		return fmt.Errorf("failed to create resource: %w", err)
	}

	// Create trace exporter
	exporter, err := p.createTraceExporter()
	if err != nil {
		return fmt.Errorf("failed to create trace exporter: %w", err)
	}

	// Create batch span processor with configured options
	bsp := sdktrace.NewBatchSpanProcessor(
		exporter,
		sdktrace.WithBatchTimeout(p.config.Trace.BatchTimeout),
		sdktrace.WithExportTimeout(p.config.Trace.ExportTimeout),
		sdktrace.WithMaxQueueSize(p.config.Trace.MaxQueueSize),
		sdktrace.WithMaxExportBatchSize(p.config.Trace.MaxBatchSize),
	)

	// Create tracer provider with sampler
	sampler := sdktrace.TraceIDRatioBased(p.config.Trace.SampleRate)

	p.tracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(bsp),
		sdktrace.WithSampler(sampler),
	)

	return nil
}

// createResource creates an OpenTelemetry resource with service information.
func (p *provider) createResource() (*resource.Resource, error) {
	// Use default resource and add our attributes without schema URL to avoid conflicts
	defaultRes := resource.Default()

	// Create our custom attributes
	customRes, err := resource.New(
		context.Background(),
		resource.WithAttributes(
			semconv.ServiceName(p.config.ServiceName),
			semconv.ServiceVersion(p.config.ServiceVersion),
			semconv.DeploymentEnvironmentName(p.config.Environment),
		),
	)
	if err != nil {
		return nil, err
	}

	// Merge default and custom resources
	return resource.Merge(defaultRes, customRes)
}

// createTraceExporter creates a trace exporter based on the configured endpoint.
func (p *provider) createTraceExporter() (sdktrace.SpanExporter, error) {
	endpoint := p.config.Trace.Endpoint

	// Use stdout exporter for local development
	if endpoint == "stdout" {
		return stdouttrace.New(
			stdouttrace.WithPrettyPrint(),
		)
	}

	// For other endpoints, we'll add OTLP support in PR #2
	return nil, fmt.Errorf("unsupported trace endpoint: %s (OTLP support coming in next PR)", endpoint)
}

// TracerProvider returns the configured trace provider.
func (p *provider) TracerProvider() trace.TracerProvider {
	if p.tracerProvider == nil {
		return noop.NewTracerProvider()
	}
	return p.tracerProvider
}

// Shutdown gracefully shuts down the provider.
func (p *provider) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.tracerProvider != nil {
		if err := p.tracerProvider.Shutdown(ctx); err != nil {
			return fmt.Errorf("failed to shutdown trace provider: %w", err)
		}
	}

	return nil
}

// ForceFlush immediately flushes any pending telemetry data.
func (p *provider) ForceFlush(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.tracerProvider != nil {
		if err := p.tracerProvider.ForceFlush(ctx); err != nil {
			return fmt.Errorf("failed to flush trace provider: %w", err)
		}
	}

	return nil
}
