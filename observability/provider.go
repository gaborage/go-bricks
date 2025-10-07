package observability

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	metricznoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc/credentials/insecure"
)

// Provider is the interface for observability providers.
// It manages the lifecycle of tracing and metrics providers.
type Provider interface {
	// TracerProvider returns the configured trace provider.
	TracerProvider() trace.TracerProvider

	// MeterProvider returns the configured meter provider.
	MeterProvider() metric.MeterProvider

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
	meterProvider  *sdkmetric.MeterProvider
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

	// Initialize meter provider if metrics are enabled
	if cfg.Metrics.Enabled {
		if err := p.initMeterProvider(); err != nil {
			return nil, fmt.Errorf("failed to initialize meter provider: %w", err)
		}
	}

	// Set global providers
	if p.tracerProvider != nil {
		otel.SetTracerProvider(p.tracerProvider)
	}
	if p.meterProvider != nil {
		otel.SetMeterProvider(p.meterProvider)
	}

	// Set global propagator for W3C trace context
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return p, nil
}

// MustNewProvider creates a new observability provider and panics on error.
// This is useful for initialization where provider creation must succeed or fail fast.
func MustNewProvider(cfg *Config) Provider {
	p, err := NewProvider(cfg)
	if err != nil {
		panic(fmt.Errorf("failed to create observability provider: %w", err))
	}
	return p
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
	if endpoint == EndpointStdout {
		return stdouttrace.New(
			stdouttrace.WithPrettyPrint(),
		)
	}

	// Create OTLP exporter based on protocol
	protocol := p.config.Trace.Protocol
	switch protocol {
	case ProtocolHTTP:
		return p.createOTLPHTTPExporter()
	case ProtocolGRPC:
		return p.createOTLPGRPCExporter()
	default:
		return nil, fmt.Errorf("trace protocol '%s': %w", protocol, ErrInvalidProtocol)
	}
}

// createOTLPHTTPExporter creates an OTLP HTTP trace exporter.
func (p *provider) createOTLPHTTPExporter() (sdktrace.SpanExporter, error) {
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(p.config.Trace.Endpoint),
	}

	// Configure TLS/insecure connection
	if p.config.Trace.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	// Add custom headers (e.g., for authentication)
	if len(p.config.Trace.Headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(p.config.Trace.Headers))
	}

	return otlptracehttp.New(context.Background(), opts...)
}

// createOTLPGRPCExporter creates an OTLP gRPC trace exporter.
func (p *provider) createOTLPGRPCExporter() (sdktrace.SpanExporter, error) {
	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(p.config.Trace.Endpoint),
	}

	// Configure TLS/insecure connection
	if p.config.Trace.Insecure {
		opts = append(opts, otlptracegrpc.WithTLSCredentials(insecure.NewCredentials()))
	}

	// Add custom headers (e.g., for authentication)
	if len(p.config.Trace.Headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(p.config.Trace.Headers))
	}

	return otlptracegrpc.New(context.Background(), opts...)
}

// TracerProvider returns the configured trace provider.
func (p *provider) TracerProvider() trace.TracerProvider {
	if p.tracerProvider == nil {
		return noop.NewTracerProvider()
	}
	return p.tracerProvider
}

// MeterProvider returns the configured meter provider.
func (p *provider) MeterProvider() metric.MeterProvider {
	if p.meterProvider == nil {
		return metricznoop.NewMeterProvider()
	}
	return p.meterProvider
}

// Shutdown gracefully shuts down the provider.
//
//nolint:dupl // Shutdown and ForceFlush have similar structure but different semantics
func (p *provider) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var errs []error

	if p.tracerProvider != nil {
		if err := p.tracerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to shutdown trace provider: %w", err))
		}
	}

	if p.meterProvider != nil {
		if err := p.meterProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to shutdown meter provider: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}

	return nil
}

// ForceFlush immediately flushes any pending telemetry data.
//
//nolint:dupl // Shutdown and ForceFlush have similar structure but different semantics
func (p *provider) ForceFlush(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var errs []error

	if p.tracerProvider != nil {
		if err := p.tracerProvider.ForceFlush(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to flush trace provider: %w", err))
		}
	}

	if p.meterProvider != nil {
		if err := p.meterProvider.ForceFlush(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to flush meter provider: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("flush errors: %v", errs)
	}

	return nil
}
