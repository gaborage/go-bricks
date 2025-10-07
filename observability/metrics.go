package observability

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"google.golang.org/grpc/credentials/insecure"
)

// initMeterProvider initializes the OpenTelemetry meter provider.
func (p *provider) initMeterProvider() error {
	// Create resource with service information (reuse from trace provider)
	res, err := p.createResource()
	if err != nil {
		return fmt.Errorf("failed to create resource: %w", err)
	}

	// Create metric exporter
	exporter, err := p.createMetricExporter()
	if err != nil {
		return fmt.Errorf("failed to create metric exporter: %w", err)
	}

	// Create periodic reader with configured interval
	reader := sdkmetric.NewPeriodicReader(
		exporter,
		sdkmetric.WithInterval(p.config.Metrics.Interval),
		sdkmetric.WithTimeout(p.config.Metrics.ExportTimeout),
	)

	// Create meter provider
	p.meterProvider = sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	)

	return nil
}

// createMetricExporter creates a metric exporter based on the configured endpoint.
func (p *provider) createMetricExporter() (sdkmetric.Exporter, error) {
	endpoint := p.config.Metrics.Endpoint

	// Use stdout exporter for local development
	if endpoint == EndpointStdout {
		return stdoutmetric.New(
			stdoutmetric.WithPrettyPrint(),
		)
	}

	// Create OTLP exporter based on protocol
	// Metrics use the same protocol configuration as traces
	protocol := p.config.Trace.Protocol
	switch protocol {
	case ProtocolHTTP:
		return p.createOTLPHTTPMetricExporter()
	case ProtocolGRPC:
		return p.createOTLPGRPCMetricExporter()
	default:
		return nil, fmt.Errorf("metrics protocol '%s': %w", protocol, ErrInvalidProtocol)
	}
}

// createOTLPHTTPMetricExporter creates an OTLP HTTP metric exporter.
func (p *provider) createOTLPHTTPMetricExporter() (sdkmetric.Exporter, error) {
	opts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(p.config.Metrics.Endpoint),
	}

	// Configure TLS/insecure connection
	if p.config.Trace.Insecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}

	// Add custom headers (e.g., for authentication)
	if len(p.config.Trace.Headers) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(p.config.Trace.Headers))
	}

	return otlpmetrichttp.New(context.Background(), opts...)
}

// createOTLPGRPCMetricExporter creates an OTLP gRPC metric exporter.
func (p *provider) createOTLPGRPCMetricExporter() (sdkmetric.Exporter, error) {
	opts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(p.config.Metrics.Endpoint),
	}

	// Configure TLS/insecure connection
	if p.config.Trace.Insecure {
		opts = append(opts, otlpmetricgrpc.WithTLSCredentials(insecure.NewCredentials()))
	}

	// Add custom headers (e.g., for authentication)
	if len(p.config.Trace.Headers) > 0 {
		opts = append(opts, otlpmetricgrpc.WithHeaders(p.config.Trace.Headers))
	}

	return otlpmetricgrpc.New(context.Background(), opts...)
}

// CreateCounter creates a new counter metric instrument.
// Counters are monotonically increasing values (e.g., request count, error count).
//
// Example:
//
//	counter, err := CreateCounter(meter, "http.requests.total", "Total HTTP requests")
//	if err != nil {
//	    return err
//	}
//	counter.Add(ctx, 1, metric.WithAttributes(
//	    attribute.String("method", "GET"),
//	    attribute.Int("status", 200),
//	))
func CreateCounter(meter metric.Meter, name, description string, opts ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return meter.Int64Counter(
		name,
		append([]metric.Int64CounterOption{
			metric.WithDescription(description),
		}, opts...)...,
	)
}

// CreateHistogram creates a new histogram metric instrument.
// Histograms record distributions of values (e.g., request duration, response size).
//
// Example:
//
//	histogram, err := CreateHistogram(meter, "http.request.duration", "HTTP request duration in milliseconds")
//	if err != nil {
//	    return err
//	}
//	histogram.Record(ctx, 123.45, metric.WithAttributes(
//	    attribute.String("method", "GET"),
//	    attribute.String("path", "/users"),
//	))
func CreateHistogram(meter metric.Meter, name, description string, opts ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	return meter.Float64Histogram(
		name,
		append([]metric.Float64HistogramOption{
			metric.WithDescription(description),
		}, opts...)...,
	)
}

// CreateUpDownCounter creates a new up-down counter metric instrument.
// Up-down counters can increase or decrease (e.g., active connections, queue size).
//
// Example:
//
//	upDownCounter, err := CreateUpDownCounter(meter, "db.connections.active", "Active database connections")
//	if err != nil {
//	    return err
//	}
//	upDownCounter.Add(ctx, 1)  // Connection opened
//	upDownCounter.Add(ctx, -1) // Connection closed
func CreateUpDownCounter(meter metric.Meter, name, description string, opts ...metric.Int64UpDownCounterOption) (metric.Int64UpDownCounter, error) {
	return meter.Int64UpDownCounter(
		name,
		append([]metric.Int64UpDownCounterOption{
			metric.WithDescription(description),
		}, opts...)...,
	)
}

// Note: Observable instruments (gauges, counters) are created directly using meter.Int64ObservableGauge
// or meter.Float64ObservableCounter with callbacks. Refer to OpenTelemetry documentation for callback patterns.
