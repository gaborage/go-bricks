package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

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

	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// debugLogger is a simple logger for observability debugging.
// It only outputs when GOBRICKS_DEBUG environment variable is set to "true".
// This prevents noisy [OBSERVABILITY] logs in production environments.
var debugLogger = initDebugLogger()

// Exporter wrappers enable tests to intercept exporter construction without affecting production code.
// Access is protected by wrapperMu to prevent data races when tests modify wrappers concurrently.
var (
	wrapperMu             sync.RWMutex
	traceExporterWrapper  = func(exporter sdktrace.SpanExporter) sdktrace.SpanExporter { return exporter }
	metricExporterWrapper = func(exporter sdkmetric.Exporter) sdkmetric.Exporter { return exporter }
	logExporterWrapper    = func(exporter sdklog.Exporter) sdklog.Exporter { return exporter }
)

const (
	cleanupTimeout = 5 * time.Second
)

// Thread-safe getters for exporter wrappers
func getTraceExporterWrapper() func(sdktrace.SpanExporter) sdktrace.SpanExporter {
	wrapperMu.RLock()
	defer wrapperMu.RUnlock()
	return traceExporterWrapper
}

func getMetricExporterWrapper() func(sdkmetric.Exporter) sdkmetric.Exporter {
	wrapperMu.RLock()
	defer wrapperMu.RUnlock()
	return metricExporterWrapper
}

func getLogExporterWrapper() func(sdklog.Exporter) sdklog.Exporter {
	wrapperMu.RLock()
	defer wrapperMu.RUnlock()
	return logExporterWrapper
}

// Thread-safe setters for exporter wrappers (test use only)
func setTraceExporterWrapper(wrapper func(sdktrace.SpanExporter) sdktrace.SpanExporter) {
	wrapperMu.Lock()
	defer wrapperMu.Unlock()
	traceExporterWrapper = wrapper
}

func setMetricExporterWrapper(wrapper func(sdkmetric.Exporter) sdkmetric.Exporter) {
	wrapperMu.Lock()
	defer wrapperMu.Unlock()
	metricExporterWrapper = wrapper
}

// initDebugLogger initializes the debug logger based on environment variables.
// Returns a logger that writes to stderr if debugging is enabled, or a no-op logger otherwise.
func initDebugLogger() *log.Logger {
	// Check if debug logging is enabled via environment variable
	debug := os.Getenv("GOBRICKS_DEBUG")
	if debug == "true" || debug == "1" {
		return log.New(os.Stderr, "[OBSERVABILITY] ", log.LstdFlags|log.Lmsgprefix)
	}
	// Return a no-op logger that discards all output
	return log.New(io.Discard, "", 0)
}

// Provider is the interface for observability providers.
// It manages the lifecycle of tracing, metrics, and logging providers.
type Provider interface {
	// TracerProvider returns the configured trace provider.
	TracerProvider() trace.TracerProvider

	// MeterProvider returns the configured meter provider.
	MeterProvider() metric.MeterProvider

	// LoggerProvider returns the configured logger provider.
	// Returns nil if logging is disabled.
	LoggerProvider() *sdklog.LoggerProvider

	// ShouldDisableStdout returns true if stdout should be disabled when OTLP is enabled.
	// This method provides configuration access for logger integration.
	ShouldDisableStdout() bool

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
	loggerProvider *sdklog.LoggerProvider
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
	debugLogger.Printf("Config after applying defaults - sample_rate=%s, batch_timeout=%v",
		formatSampleRate(safeCfg.Trace.Sample.Rate), safeCfg.Trace.Batch.Timeout)

	// Now validate the defaulted config
	if err := safeCfg.Validate(); err != nil {
		debugLogger.Printf("Config validation failed: %v", err)
		return nil, fmt.Errorf("invalid observability config: %w", err)
	}

	// Defensive check: warn if sample rate is explicitly zero (drops all spans)
	warnIfZeroSampleRate(&safeCfg)

	// Return no-op provider if observability is disabled
	if !safeCfg.Enabled {
		debugLogger.Println("Observability disabled, returning no-op provider")
		return newNoopProvider(), nil
	}

	p := &provider{
		config: safeCfg, // Use the defaulted config
	}

	// Set up cleanup for partial initialization failures
	// This ensures we don't leak goroutines/connections if later init steps fail
	var initSuccess bool
	defer func() {
		if !initSuccess {
			p.cleanupPartialInit()
		}
	}()

	// Initialize trace provider if tracing is enabled
	traceEnabled := isProviderEnabled(safeCfg.Trace.Enabled)
	traceConfig := fmt.Sprintf("enabled=%v, endpoint=%s, protocol=%s, insecure=%v",
		traceEnabled, safeCfg.Trace.Endpoint, safeCfg.Trace.Protocol, safeCfg.Trace.Insecure)
	if err := initializeProvider("Trace", traceEnabled, traceConfig, p.initTraceProvider); err != nil {
		return nil, err
	}

	// Initialize meter provider if metrics are enabled
	metricsEnabled := isProviderEnabled(safeCfg.Metrics.Enabled)
	metricsConfig := fmt.Sprintf("enabled=%v, endpoint=%s, protocol=%s",
		metricsEnabled, safeCfg.Metrics.Endpoint, safeCfg.Metrics.Protocol)
	if err := initializeProvider("Metrics", metricsEnabled, metricsConfig, p.initMeterProvider); err != nil {
		return nil, err
	}

	// Initialize logger provider if logs are enabled
	logsEnabled := isProviderEnabled(safeCfg.Logs.Enabled)
	logsConfig := fmt.Sprintf("enabled=%v, endpoint=%s, protocol=%s",
		logsEnabled, safeCfg.Logs.Endpoint, safeCfg.Logs.Protocol)
	if err := initializeProvider("Logs", logsEnabled, logsConfig, p.initLogProvider); err != nil {
		return nil, err
	}

	// Register global providers and propagator
	p.registerGlobalProviders()

	// Mark initialization as successful to skip cleanup
	initSuccess = true
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

// isProviderEnabled safely checks if a provider is enabled by handling nil pointer checks.
func isProviderEnabled(enabled *bool) bool {
	return enabled != nil && *enabled
}

// initializeProvider handles the common pattern of initializing a provider with logging.
// It logs the configuration, attempts initialization, and returns wrapped errors on failure.
func initializeProvider(
	name string,
	enabled bool,
	config string,
	initFunc func() error,
) error {
	debugLogger.Printf("%s configuration: %s", name, config)

	if !enabled {
		debugLogger.Printf("%s provider skipped (disabled)", name)
		return nil
	}

	debugLogger.Printf("Initializing %s provider...", name)
	if err := initFunc(); err != nil {
		debugLogger.Printf("Failed to initialize %s provider: %v", name, err)
		return fmt.Errorf("failed to initialize %s provider: %w", name, err)
	}

	debugLogger.Printf("%s provider initialized successfully", name)
	return nil
}

// registerGlobalProviders sets up the global OpenTelemetry providers and propagator.
func (p *provider) registerGlobalProviders() {
	// Set global providers
	if p.tracerProvider != nil {
		debugLogger.Println("Setting global tracer provider")
		otel.SetTracerProvider(p.tracerProvider)
	}
	if p.meterProvider != nil {
		debugLogger.Println("Setting global meter provider")
		otel.SetMeterProvider(p.meterProvider)
	}
	// Note: OTel doesn't have a global logger provider setter like traces/metrics

	// Set global propagator for W3C trace context
	debugLogger.Println("Setting W3C trace context propagator")
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// initTraceProvider initializes the OpenTelemetry trace provider.
func (p *provider) initTraceProvider() error {
	// Create resource with service information
	res, err := p.createResource()
	if err != nil {
		return fmt.Errorf(errCreateResourceFmt, err)
	}

	// Create trace exporter
	exporter, err := p.createTraceExporter()
	if err != nil {
		return fmt.Errorf("failed to create trace exporter: %w", err)
	}

	// Create batch span processor with configured options
	debugLogger.Printf("Creating BatchSpanProcessor with batch_timeout=%v, export_timeout=%v, queue_size=%d, batch_size=%d",
		p.config.Trace.Batch.Timeout, p.config.Trace.Export.Timeout, p.config.Trace.Max.Queue.Size, p.config.Trace.Max.Batch.Size)

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
	// Sample rate should always be set by ApplyDefaults, but use 1.0 as final fallback
	sampleRate := 1.0
	if p.config.Trace.Sample.Rate != nil {
		sampleRate = *p.config.Trace.Sample.Rate
	}
	sampler := sdktrace.TraceIDRatioBased(sampleRate)
	debugLogger.Printf("Creating TracerProvider with sampler rate=%.2f", sampleRate)

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
		exporter, err := stdouttrace.New(
			stdouttrace.WithPrettyPrint(),
		)
		if err != nil {
			debugLogger.Printf("Failed to create stdout trace exporter: %v", err)
			return nil, err
		}
		return getTraceExporterWrapper()(exporter), nil
	}

	// Create OTLP exporter based on protocol
	protocol := p.config.Trace.Protocol
	debugLogger.Printf("Creating OTLP trace exporter: protocol=%s, endpoint=%s, insecure=%v",
		protocol, endpoint, p.config.Trace.Insecure)

	switch protocol {
	case ProtocolHTTP:
		exporter, err := p.createOTLPHTTPExporter()
		if err != nil {
			return nil, err
		}
		return getTraceExporterWrapper()(exporter), nil
	case ProtocolGRPC:
		exporter, err := p.createOTLPGRPCExporter()
		if err != nil {
			return nil, err
		}
		return getTraceExporterWrapper()(exporter), nil
	default:
		debugLogger.Printf("Invalid trace protocol: %s", protocol)
		return nil, fmt.Errorf("trace protocol '%s': %w", protocol, ErrInvalidProtocol)
	}
}

// stripScheme removes http:// or https:// prefix from endpoint.
// OTEL HTTP exporters expect host:port without scheme - they add it automatically based on WithInsecure().
func stripScheme(endpoint string) string {
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	return endpoint
}

// createOTLPHTTPExporter creates an OTLP HTTP trace exporter.
func (p *provider) createOTLPHTTPExporter() (sdktrace.SpanExporter, error) {
	debugLogger.Printf("Creating OTLP HTTP trace exporter: endpoint=%s, insecure=%v, compression=%s, headers_count=%d",
		p.config.Trace.Endpoint, p.config.Trace.Insecure, p.config.Trace.Compression, len(p.config.Trace.Headers))

	// Strip scheme - OTEL HTTP exporter adds it automatically based on WithInsecure()
	endpoint := stripScheme(p.config.Trace.Endpoint)

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint),
	}

	// Configure compression
	if p.config.Trace.Compression == CompressionGzip {
		opts = append(opts, otlptracehttp.WithCompression(otlptracehttp.GzipCompression))
		debugLogger.Println("Enabled gzip compression for trace export")
	} else {
		opts = append(opts, otlptracehttp.WithCompression(otlptracehttp.NoCompression))
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
	debugLogger.Printf("Creating OTLP gRPC trace exporter: endpoint=%s, insecure=%v, compression=%s, headers_count=%d",
		p.config.Trace.Endpoint, p.config.Trace.Insecure, p.config.Trace.Compression, len(p.config.Trace.Headers))

	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(p.config.Trace.Endpoint),
	}

	// Configure compression
	if p.config.Trace.Compression == CompressionGzip {
		opts = append(opts, otlptracegrpc.WithCompressor("gzip"))
		debugLogger.Println("Enabled gzip compression for trace export")
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

// LoggerProvider returns the configured logger provider.
// Returns nil if logging is disabled (caller should check for nil).
func (p *provider) LoggerProvider() *sdklog.LoggerProvider {
	return p.loggerProvider
}

// ShouldDisableStdout returns true if stdout should be disabled when OTLP is enabled.
// This implements the logger.OTelProvider interface to provide configuration access
// without exposing the entire Config struct publicly.
func (p *provider) ShouldDisableStdout() bool {
	return p.config.Logs.DisableStdout
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

	if p.loggerProvider != nil {
		if err := p.loggerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to shutdown logger provider: %w", err))
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

	if p.loggerProvider != nil {
		if err := p.loggerProvider.ForceFlush(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to flush logger provider: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("flush errors: %w", errors.Join(errs...))
	}

	return nil
}

// cleanupPartialInit safely shuts down any partially initialized providers.
// Called when provider initialization fails mid-way to prevent resource leaks.
// Uses a background context with timeout to ensure cleanup completes even if the
// initialization context was cancelled.
func (p *provider) cleanupPartialInit() {
	debugLogger.Println("Cleaning up partially initialized providers due to init error")

	// Use background context with timeout for cleanup (don't inherit cancelled context)
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	if err := p.Shutdown(ctx); err != nil {
		// Log cleanup errors but don't fail - we're already handling an error
		debugLogger.Printf("Cleanup errors (non-fatal): %v", err)
	} else {
		debugLogger.Println("Partial initialization cleanup completed successfully")
	}
}

// formatSampleRate formats a sample rate pointer for logging.
// Returns a user-friendly string representation.
func formatSampleRate(rate *float64) string {
	if rate == nil {
		return "nil (will use default)"
	}
	return fmt.Sprintf("%.2f", *rate)
}

// warnIfZeroSampleRate logs a warning if the sample rate is explicitly set to 0.0.
// This helps users understand why no spans are being recorded.
func warnIfZeroSampleRate(cfg *Config) {
	if cfg.Enabled && cfg.Trace.Enabled != nil && *cfg.Trace.Enabled {
		if cfg.Trace.Sample.Rate != nil && *cfg.Trace.Sample.Rate == 0.0 {
			debugLogger.Println("WARNING: Trace sample rate is explicitly set to 0.0")
			debugLogger.Println("         This means NO SPANS will be recorded or exported")
			debugLogger.Println("         If this is unintentional, remove 'trace.sample.rate: 0.0' from your config")
		}
	}
}
