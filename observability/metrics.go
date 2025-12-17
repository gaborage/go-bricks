package observability

import (
	"context"
	"fmt"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc/credentials/insecure"
)

// metricInitHook allows tests to inject errors after exporter creation but before provider setup.
var metricInitHook func() error

// initMeterProvider initializes the OpenTelemetry meter provider.
func (p *provider) initMeterProvider() error {
	// Create resource with service information (reuse from trace provider)
	res, err := p.createResource()
	if err != nil {
		return fmt.Errorf(errCreateResourceFmt, err)
	}

	// Create metric exporter
	exporter, err := p.createMetricExporter()
	if err != nil {
		return fmt.Errorf("failed to create metric exporter: %w", err)
	}

	if metricInitHook != nil {
		if hookErr := metricInitHook(); hookErr != nil {
			return hookErr
		}
	}

	// Create periodic reader with configured interval
	// Include runtime producer to enable Go scheduler histogram metrics (go.schedule.duration)
	debugLogger.Println("Registering runtime producer for scheduler histogram metrics")
	reader := sdkmetric.NewPeriodicReader(
		exporter,
		sdkmetric.WithInterval(p.config.Metrics.Interval),
		sdkmetric.WithTimeout(p.config.Metrics.Export.Timeout),
		sdkmetric.WithProducer(runtime.NewProducer()),
	)

	// Create meter provider with optional histogram view
	meterOpts := []sdkmetric.Option{
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	}

	// Apply exponential histogram view if configured (New Relic recommendation)
	if p.config.Metrics.HistogramAggregation == HistogramAggregationExponential {
		debugLogger.Println("Configuring exponential histogram aggregation (New Relic recommendation)")
		meterOpts = append(meterOpts, sdkmetric.WithView(p.createExponentialHistogramView()))
	} else {
		debugLogger.Println("Using explicit bucket histogram aggregation (OTEL SDK default)")
	}

	p.meterProvider = sdkmetric.NewMeterProvider(meterOpts...)

	// Start Go runtime metrics collection (memory, GC, goroutines, CPU, config)
	// This automatically exports metrics when the periodic reader triggers:
	//   - go.memory.used, go.memory.limit, go.memory.allocated, go.memory.allocations
	//   - go.memory.gc.goal
	//   - go.goroutine.count
	//   - go.processor.limit (GOMAXPROCS)
	//   - go.config.gogc
	// Default ReadMemStats interval: 15 seconds (performance-safe)
	//
	// Note: runtime.Start() can be called multiple times safely - the meter provider's
	// RegisterCallback() handles duplicate registrations internally. Any error returned
	// indicates a genuine issue with metric instrumentation setup.
	debugLogger.Println("Starting Go runtime metrics collection")
	err = runtime.Start(runtime.WithMeterProvider(p.meterProvider))
	if err != nil {
		return fmt.Errorf("failed to start runtime metrics: %w", err)
	}
	debugLogger.Println("Runtime metrics collection started successfully")

	return nil
}

// createMetricExporter creates a metric exporter based on the configured endpoint.
func (p *provider) createMetricExporter() (sdkmetric.Exporter, error) {
	endpoint := p.config.Metrics.Endpoint
	debugLogger.Printf("Creating metric exporter for endpoint: %s", endpoint)

	// Use stdout exporter for local development
	if endpoint == EndpointStdout {
		debugLogger.Println("Using stdout metric exporter (pretty print)")
		exporter, err := stdoutmetric.New(
			stdoutmetric.WithPrettyPrint(),
		)
		if err != nil {
			debugLogger.Printf("Failed to create stdout metric exporter: %v", err)
			return nil, err
		}
		return getMetricExporterWrapper()(exporter), nil
	}

	// Create OTLP exporter based on protocol
	// Metrics use the same protocol configuration as traces
	protocol, useInsecure, headers := p.metricsTransportSettings()
	debugLogger.Printf("Metrics transport settings: protocol=%s, insecure=%v, headers_count=%d",
		protocol, useInsecure, len(headers))

	switch protocol {
	case ProtocolHTTP:
		exporter, err := p.createOTLPHTTPMetricExporter(useInsecure, headers)
		if err != nil {
			return nil, err
		}
		return getMetricExporterWrapper()(exporter), nil
	case ProtocolGRPC:
		exporter, err := p.createOTLPGRPCMetricExporter(useInsecure, headers)
		if err != nil {
			return nil, err
		}
		return getMetricExporterWrapper()(exporter), nil
	default:
		debugLogger.Printf("Invalid metrics protocol: %s", protocol)
		return nil, fmt.Errorf("metrics protocol '%s': %w", protocol, ErrInvalidProtocol)
	}
}

// createOTLPHTTPMetricExporter creates an OTLP HTTP metric exporter.
func (p *provider) createOTLPHTTPMetricExporter(useInsecure bool, headers map[string]string) (sdkmetric.Exporter, error) {
	debugLogger.Printf("Creating OTLP HTTP metric exporter: endpoint=%s, insecure=%v, compression=%s, temporality=%s, headers_count=%d",
		p.config.Metrics.Endpoint, useInsecure, p.config.Metrics.Compression, p.config.Metrics.Temporality, len(headers))

	// Strip scheme - OTEL HTTP exporter adds it automatically based on WithInsecure()
	endpoint := stripScheme(p.config.Metrics.Endpoint)

	opts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(endpoint),
	}

	// Configure compression
	if p.config.Metrics.Compression == CompressionGzip {
		opts = append(opts, otlpmetrichttp.WithCompression(otlpmetrichttp.GzipCompression))
		debugLogger.Println("Enabled gzip compression for metric export")
	} else {
		opts = append(opts, otlpmetrichttp.WithCompression(otlpmetrichttp.NoCompression))
	}

	// Configure temporality (New Relic recommends delta)
	if p.config.Metrics.Temporality == TemporalityDelta {
		opts = append(opts, otlpmetrichttp.WithTemporalitySelector(p.deltaTemporalitySelector))
		debugLogger.Println("Configured delta temporality for metrics (New Relic recommendation)")
	} else {
		debugLogger.Println("Using cumulative temporality for metrics (OTEL SDK default)")
	}

	// Configure TLS/insecure connection
	if useInsecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}

	// Add custom headers (e.g., for authentication)
	if len(headers) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(headers))
	}

	exporter, err := otlpmetrichttp.New(context.Background(), opts...)
	if err != nil {
		debugLogger.Printf("Failed to create OTLP HTTP metric exporter: %v", err)
		return nil, err
	}

	debugLogger.Println("OTLP HTTP metric exporter created successfully")
	return exporter, nil
}

// createOTLPGRPCMetricExporter creates an OTLP gRPC metric exporter.
func (p *provider) createOTLPGRPCMetricExporter(useInsecure bool, headers map[string]string) (sdkmetric.Exporter, error) {
	debugLogger.Printf("Creating OTLP gRPC metric exporter: endpoint=%s, insecure=%v, compression=%s, temporality=%s, headers_count=%d",
		p.config.Metrics.Endpoint, useInsecure, p.config.Metrics.Compression, p.config.Metrics.Temporality, len(headers))

	opts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(p.config.Metrics.Endpoint),
	}

	// Configure compression
	if p.config.Metrics.Compression == CompressionGzip {
		opts = append(opts, otlpmetricgrpc.WithCompressor("gzip"))
		debugLogger.Println("Enabled gzip compression for metric export")
	}

	// Configure temporality (New Relic recommends delta)
	if p.config.Metrics.Temporality == TemporalityDelta {
		opts = append(opts, otlpmetricgrpc.WithTemporalitySelector(p.deltaTemporalitySelector))
		debugLogger.Println("Configured delta temporality for metrics (New Relic recommendation)")
	} else {
		debugLogger.Println("Using cumulative temporality for metrics (OTEL SDK default)")
	}

	// Configure TLS/insecure connection
	if useInsecure {
		opts = append(opts, otlpmetricgrpc.WithTLSCredentials(insecure.NewCredentials()))
		debugLogger.Println("Using insecure gRPC credentials for metrics (no TLS)")
	}

	// Add custom headers (e.g., for authentication)
	if len(headers) > 0 {
		opts = append(opts, otlpmetricgrpc.WithHeaders(headers))
		debugLogger.Printf("Added %d custom headers to metrics gRPC exporter", len(headers))
	}

	exporter, err := otlpmetricgrpc.New(context.Background(), opts...)
	if err != nil {
		debugLogger.Printf("Failed to create OTLP gRPC metric exporter: %v", err)
		return nil, err
	}

	debugLogger.Println("OTLP gRPC metric exporter created successfully")
	return exporter, nil
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

// metricsTransportSettings resolves protocol, insecure flag, and headers for metrics exporters.
// Metrics-specific fields take precedence, otherwise values fall back to trace configuration,
// and finally to library defaults.
func (p *provider) metricsTransportSettings() (protocol string, useInsecure bool, headers map[string]string) {
	if p.config.Metrics.Protocol != "" {
		protocol = p.config.Metrics.Protocol
	} else if p.config.Trace.Protocol != "" {
		protocol = p.config.Trace.Protocol
	} else {
		protocol = ProtocolHTTP
	}

	useInsecure = p.config.Trace.Insecure
	if p.config.Metrics.Insecure != nil {
		useInsecure = *p.config.Metrics.Insecure
	}

	if p.config.Metrics.Headers != nil {
		headers = p.config.Metrics.Headers
	} else {
		headers = p.config.Trace.Headers
	}

	return protocol, useInsecure, headers
}

// deltaTemporalitySelector returns delta temporality for all instrument kinds.
// This is recommended by New Relic for better performance and lower memory usage.
func (p *provider) deltaTemporalitySelector(_ sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.DeltaTemporality
}

// createExponentialHistogramView creates a view that uses exponential histogram aggregation.
// This is recommended by New Relic for better precision and lower memory overhead.
func (p *provider) createExponentialHistogramView() sdkmetric.View {
	return sdkmetric.NewView(
		sdkmetric.Instrument{Kind: sdkmetric.InstrumentKindHistogram},
		sdkmetric.Stream{
			Aggregation: sdkmetric.AggregationBase2ExponentialHistogram{
				MaxSize:  160, // Default max size (New Relic recommendation)
				MaxScale: 20,  // Default max scale for good precision
			},
		},
	)
}
