package observability

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
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

// debugLogger is a simple logger for observability debugging
var debugLogger = log.New(os.Stderr, "[OBSERVABILITY] ", log.LstdFlags|log.Lmsgprefix)

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
//
// IMPORTANT: This function applies default values before validation to ensure
// safe configuration (e.g., sample rate defaults to 1.0). Callers do NOT need
// to call ApplyDefaults() first - it's handled internally.
func NewProvider(cfg *Config) (Provider, error) {
	debugLogger.Printf("NewProvider called - enabled=%v, service=%s", cfg.Enabled, cfg.Service.Name)

	// Create a defensive copy and apply defaults BEFORE validation
	// This ensures zero sample rates or missing timeouts get safe defaults
	safeCfg := *cfg // Copy to avoid mutating caller's config
	safeCfg.ApplyDefaults()
	debugLogger.Printf("Config after applying defaults - sample_rate=%.2f, batch_timeout=%v",
		safeCfg.Trace.Sample.Rate, safeCfg.Trace.Batch.Timeout)

	// Now validate the defaulted config
	if err := safeCfg.Validate(); err != nil {
		debugLogger.Printf("Config validation failed: %v", err)
		return nil, fmt.Errorf("invalid observability config: %w", err)
	}

	// Defensive check: warn if sample rate is zero (drops all spans)
	if safeCfg.Enabled && safeCfg.Trace.Enabled != nil && *safeCfg.Trace.Enabled {
		if safeCfg.Trace.Sample.Rate == 0.0 {
			debugLogger.Println("WARNING: Trace sample rate is 0.0 - NO SPANS will be recorded!")
		}
	}

	// Return no-op provider if observability is disabled
	if !safeCfg.Enabled {
		debugLogger.Println("Observability disabled, returning no-op provider")
		return newNoopProvider(), nil
	}

	p := &provider{
		config: safeCfg, // Use the defaulted config
	}

	// Initialize trace provider if tracing is enabled
	traceEnabled := safeCfg.Trace.Enabled != nil && *safeCfg.Trace.Enabled
	debugLogger.Printf("Trace configuration: enabled=%v, endpoint=%s, protocol=%s, insecure=%v",
		traceEnabled, safeCfg.Trace.Endpoint, safeCfg.Trace.Protocol, safeCfg.Trace.Insecure)

	if traceEnabled {
		debugLogger.Println("Initializing trace provider...")
		if err := p.initTraceProvider(); err != nil {
			debugLogger.Printf("Failed to initialize trace provider: %v", err)
			return nil, fmt.Errorf("failed to initialize trace provider: %w", err)
		}
		debugLogger.Println("Trace provider initialized successfully")
	} else {
		debugLogger.Println("Trace provider skipped (disabled)")
	}

	// Initialize meter provider if metrics are enabled
	metricsEnabled := safeCfg.Metrics.Enabled != nil && *safeCfg.Metrics.Enabled
	debugLogger.Printf("Metrics configuration: enabled=%v, endpoint=%s, protocol=%s",
		metricsEnabled, safeCfg.Metrics.Endpoint, safeCfg.Metrics.Protocol)

	if metricsEnabled {
		debugLogger.Println("Initializing meter provider...")
		if err := p.initMeterProvider(); err != nil {
			debugLogger.Printf("Failed to initialize meter provider: %v", err)
			return nil, fmt.Errorf("failed to initialize meter provider: %w", err)
		}
		debugLogger.Println("Meter provider initialized successfully")
	} else {
		debugLogger.Println("Meter provider skipped (disabled)")
	}

	// Set global providers
	if p.tracerProvider != nil {
		debugLogger.Println("Setting global tracer provider")
		otel.SetTracerProvider(p.tracerProvider)
	}
	if p.meterProvider != nil {
		debugLogger.Println("Setting global meter provider")
		otel.SetMeterProvider(p.meterProvider)
	}

	// Set global propagator for W3C trace context
	debugLogger.Println("Setting W3C trace context propagator")
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	debugLogger.Println("Observability provider created successfully")
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
	debugLogger.Printf("Creating BatchSpanProcessor with timeout=%v, queue_size=%d, batch_size=%d",
		p.config.Trace.Batch.Timeout, p.config.Trace.Max.Queue.Size, p.config.Trace.Max.Batch.Size)

	bsp := sdktrace.NewBatchSpanProcessor(
		exporter,
		sdktrace.WithBatchTimeout(p.config.Trace.Batch.Timeout),
		sdktrace.WithExportTimeout(p.config.Trace.Export.Timeout),
		sdktrace.WithMaxQueueSize(p.config.Trace.Max.Queue.Size),
		sdktrace.WithMaxExportBatchSize(p.config.Trace.Max.Batch.Size),
	)

	// Wrap with debug processor for visibility into span lifecycle
	processor := newDebugSpanProcessor(bsp)

	// Create tracer provider with sampler
	sampler := sdktrace.TraceIDRatioBased(p.config.Trace.Sample.Rate)
	debugLogger.Printf("Creating TracerProvider with sampler rate=%.2f", p.config.Trace.Sample.Rate)

	p.tracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(processor),
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
			semconv.ServiceName(p.config.Service.Name),
			semconv.ServiceVersion(p.config.Service.Version),
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
	debugLogger.Printf("Creating trace exporter for endpoint: %s", endpoint)

	// Use stdout exporter for local development
	if endpoint == EndpointStdout {
		debugLogger.Println("Using stdout trace exporter (pretty print)")
		return stdouttrace.New(
			stdouttrace.WithPrettyPrint(),
		)
	}

	// Create OTLP exporter based on protocol
	protocol := p.config.Trace.Protocol
	debugLogger.Printf("Creating OTLP trace exporter: protocol=%s, endpoint=%s, insecure=%v",
		protocol, endpoint, p.config.Trace.Insecure)

	switch protocol {
	case ProtocolHTTP:
		return p.createOTLPHTTPExporter()
	case ProtocolGRPC:
		return p.createOTLPGRPCExporter()
	default:
		debugLogger.Printf("Invalid trace protocol: %s", protocol)
		return nil, fmt.Errorf("trace protocol '%s': %w", protocol, ErrInvalidProtocol)
	}
}

// createOTLPHTTPExporter creates an OTLP HTTP trace exporter.
func (p *provider) createOTLPHTTPExporter() (sdktrace.SpanExporter, error) {
	debugLogger.Printf("Creating OTLP HTTP trace exporter: endpoint=%s, insecure=%v, headers_count=%d",
		p.config.Trace.Endpoint, p.config.Trace.Insecure, len(p.config.Trace.Headers))

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

	exporter, err := otlptracehttp.New(context.Background(), opts...)
	if err != nil {
		debugLogger.Printf("Failed to create OTLP HTTP trace exporter: %v", err)
		return nil, err
	}

	debugLogger.Println("OTLP HTTP trace exporter created successfully")
	return exporter, nil
}

// createOTLPGRPCExporter creates an OTLP gRPC trace exporter.
func (p *provider) createOTLPGRPCExporter() (sdktrace.SpanExporter, error) {
	debugLogger.Printf("Creating OTLP gRPC trace exporter: endpoint=%s, insecure=%v, headers_count=%d",
		p.config.Trace.Endpoint, p.config.Trace.Insecure, len(p.config.Trace.Headers))

	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(p.config.Trace.Endpoint),
	}

	// Configure TLS/insecure connection
	if p.config.Trace.Insecure {
		opts = append(opts, otlptracegrpc.WithTLSCredentials(insecure.NewCredentials()))
		debugLogger.Println("Using insecure gRPC credentials (no TLS)")
	}

	// Add custom headers (e.g., for authentication)
	if len(p.config.Trace.Headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(p.config.Trace.Headers))
		debugLogger.Printf("Added %d custom headers to gRPC exporter", len(p.config.Trace.Headers))
	}

	exporter, err := otlptracegrpc.New(context.Background(), opts...)
	if err != nil {
		debugLogger.Printf("Failed to create OTLP gRPC trace exporter: %v", err)
		return nil, err
	}

	debugLogger.Println("OTLP gRPC trace exporter created successfully")
	return exporter, nil
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
		return fmt.Errorf("shutdown errors: %w", errors.Join(errs...))
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
		return fmt.Errorf("flush errors: %w", errors.Join(errs...))
	}

	return nil
}
