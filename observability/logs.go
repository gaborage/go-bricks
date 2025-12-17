package observability

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	"google.golang.org/grpc/credentials/insecure"
)

// Re-export insecure credentials to avoid variable shadowing
var grpcInsecureCredentials = insecure.NewCredentials

// logInitHook allows tests to inject failures after exporter creation but before provider setup.
var logInitHook func() error

// initLogProvider initializes the OpenTelemetry logger provider with dual-mode logging.
func (p *provider) initLogProvider() error {
	// Create resource with service information (reuse from trace provider)
	res, err := p.createResource()
	if err != nil {
		return fmt.Errorf(errCreateResourceFmt, err)
	}

	// Create log exporter
	exporter, err := p.createLogExporter()
	if err != nil {
		return fmt.Errorf("failed to create log exporter: %w", err)
	}

	// Create dual-mode processor (action logs + trace logs)
	processor, err := p.createDualModeProcessor(exporter)
	if err != nil {
		return fmt.Errorf("failed to create dual-mode processor: %w", err)
	}

	if logInitHook != nil {
		if hookErr := logInitHook(); hookErr != nil {
			return hookErr
		}
	}

	// Create logger provider
	p.loggerProvider = sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(processor),
	)

	return nil
}

// createLogExporter creates a log exporter based on the configured endpoint.
func (p *provider) createLogExporter() (sdklog.Exporter, error) {
	endpoint := p.config.Logs.Endpoint
	debugLogger.Printf("Creating log exporter for endpoint: %s", endpoint)

	// Use stdout exporter for local development
	if endpoint == EndpointStdout {
		debugLogger.Println("Using stdout log exporter (pretty print)")
		exporter, err := stdoutlog.New(
			stdoutlog.WithPrettyPrint(),
		)
		if err != nil {
			debugLogger.Printf("Failed to create stdout log exporter: %v", err)
			return nil, err
		}
		return getLogExporterWrapper()(exporter), nil
	}

	// Create OTLP exporter based on protocol
	protocol := p.config.Logs.Protocol
	debugLogger.Printf("Creating OTLP log exporter: protocol=%s, endpoint=%s, insecure=%v",
		protocol, endpoint, p.config.Logs.Insecure != nil && *p.config.Logs.Insecure)

	switch protocol {
	case ProtocolHTTP:
		exporter, err := p.createOTLPHTTPLogExporter()
		if err != nil {
			return nil, err
		}
		return getLogExporterWrapper()(exporter), nil
	case ProtocolGRPC:
		exporter, err := p.createOTLPGRPCLogExporter()
		if err != nil {
			return nil, err
		}
		return getLogExporterWrapper()(exporter), nil
	default:
		debugLogger.Printf("Invalid log protocol: %s", protocol)
		return nil, fmt.Errorf("log protocol '%s': %w", protocol, ErrInvalidProtocol)
	}
}

// createOTLPHTTPLogExporter creates an OTLP HTTP log exporter.
func (p *provider) createOTLPHTTPLogExporter() (sdklog.Exporter, error) {
	useInsecure := false
	if p.config.Logs.Insecure != nil {
		useInsecure = *p.config.Logs.Insecure
	}

	debugLogger.Printf("Creating OTLP HTTP log exporter: endpoint=%s, insecure=%v, compression=%s, headers_count=%d",
		p.config.Logs.Endpoint, useInsecure, p.config.Logs.Compression, len(p.config.Logs.Headers))

	// Strip scheme - OTEL HTTP exporter adds it automatically based on WithInsecure()
	endpoint := stripScheme(p.config.Logs.Endpoint)

	opts := []otlploghttp.Option{
		otlploghttp.WithEndpoint(endpoint),
	}

	// Configure compression
	if p.config.Logs.Compression == CompressionGzip {
		opts = append(opts, otlploghttp.WithCompression(otlploghttp.GzipCompression))
		debugLogger.Println("Enabled gzip compression for log export")
	} else {
		opts = append(opts, otlploghttp.WithCompression(otlploghttp.NoCompression))
	}

	// Configure TLS/insecure connection
	if useInsecure {
		opts = append(opts, otlploghttp.WithInsecure())
	}

	// Add custom headers (e.g., for authentication)
	if len(p.config.Logs.Headers) > 0 {
		opts = append(opts, otlploghttp.WithHeaders(p.config.Logs.Headers))
	}

	exporter, err := otlploghttp.New(context.Background(), opts...)
	if err != nil {
		debugLogger.Printf("Failed to create OTLP HTTP log exporter: %v", err)
		return nil, err
	}

	debugLogger.Println("OTLP HTTP log exporter created successfully")
	return exporter, nil
}

// createOTLPGRPCLogExporter creates an OTLP gRPC log exporter.
func (p *provider) createOTLPGRPCLogExporter() (sdklog.Exporter, error) {
	useInsecure := false
	if p.config.Logs.Insecure != nil {
		useInsecure = *p.config.Logs.Insecure
	}

	debugLogger.Printf("Creating OTLP gRPC log exporter: endpoint=%s, insecure=%v, compression=%s, headers_count=%d",
		p.config.Logs.Endpoint, useInsecure, p.config.Logs.Compression, len(p.config.Logs.Headers))

	opts := []otlploggrpc.Option{
		otlploggrpc.WithEndpoint(p.config.Logs.Endpoint),
	}

	// Configure compression
	if p.config.Logs.Compression == CompressionGzip {
		opts = append(opts, otlploggrpc.WithCompressor("gzip"))
		debugLogger.Println("Enabled gzip compression for log export")
	}

	// Configure TLS/insecure connection
	if useInsecure {
		opts = append(opts, otlploggrpc.WithTLSCredentials(grpcInsecureCredentials()))
		debugLogger.Println("Using insecure gRPC credentials for logs (no TLS)")
	}

	// Add custom headers (e.g., for authentication)
	if len(p.config.Logs.Headers) > 0 {
		opts = append(opts, otlploggrpc.WithHeaders(p.config.Logs.Headers))
		debugLogger.Printf("Added %d custom headers to logs gRPC exporter", len(p.config.Logs.Headers))
	}

	exporter, err := otlploggrpc.New(context.Background(), opts...)
	if err != nil {
		debugLogger.Printf("Failed to create OTLP gRPC log exporter: %v", err)
		return nil, err
	}

	debugLogger.Println("OTLP gRPC log exporter created successfully")
	return exporter, nil
}

// createDualModeProcessor creates a dual-mode log processor with separate processors for action and trace logs.
func (p *provider) createDualModeProcessor(baseExporter sdklog.Exporter) (sdklog.Processor, error) {
	debugLogger.Println("Creating dual-mode log processor (action logs + trace logs)")

	// Create resource for action logs (log.type="action")
	actionResource, err := p.createLogResource("action")
	if err != nil {
		return nil, fmt.Errorf("failed to create action log resource: %w", err)
	}

	// Create resource for trace logs (log.type="trace")
	traceResource, err := p.createLogResource("trace")
	if err != nil {
		return nil, fmt.Errorf("failed to create trace log resource: %w", err)
	}

	// Create batch processor for action logs (100% sampling, all severities)
	actionProcessor := p.createBatchProcessorWithResource(baseExporter, actionResource, "action")

	// Create batch processor for trace logs (WARN+ always, INFO/DEBUG sampled)
	traceProcessor := p.createBatchProcessorWithResource(baseExporter, traceResource, "trace")

	// Get sampling rate for INFO/DEBUG trace logs (default 0.0 = drop all)
	samplingRate := 0.0
	if p.config.Logs.SamplingRate != nil {
		samplingRate = *p.config.Logs.SamplingRate
	}

	debugLogger.Printf("Dual-mode log processor created successfully (sampling_rate=%.2f)", samplingRate)
	return NewDualModeLogProcessor(actionProcessor, traceProcessor, samplingRate), nil
}

// createLogResource creates a resource with the specified log.type attribute.
// This merges the base service resource with log-type-specific attributes.
func (p *provider) createLogResource(logType string) (*resource.Resource, error) {
	baseRes, err := p.createResource()
	if err != nil {
		return nil, err
	}

	// Create log-type-specific resource
	typeRes, err := resource.Merge(
		baseRes,
		resource.NewWithAttributes(
			baseRes.SchemaURL(),
			attribute.String("log.type", logType),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to merge resources: %w", err)
	}

	debugLogger.Printf("Created log resource with log.type=%s", logType)
	return typeRes, nil
}

// createBatchProcessorWithResource creates a batch processor with resource attribute enrichment.
func (p *provider) createBatchProcessorWithResource(
	baseExporter sdklog.Exporter,
	res *resource.Resource,
	logType string,
) sdklog.Processor {
	// Wrap exporter with resource attribute injection
	enrichedExporter := newResourceAttributeExporter(baseExporter, res)

	// Create batch processor with configured options
	debugLogger.Printf("Creating BatchProcessor for %s logs: timeout=%v, queue_size=%d, batch_size=%d",
		logType, p.config.Logs.Batch.Timeout, p.config.Logs.Max.Queue.Size, p.config.Logs.Max.Batch.Size)

	return sdklog.NewBatchProcessor(
		enrichedExporter,
		sdklog.WithExportTimeout(p.config.Logs.Export.Timeout),
		sdklog.WithExportInterval(p.config.Logs.Batch.Timeout),
		sdklog.WithMaxQueueSize(p.config.Logs.Max.Queue.Size),
		sdklog.WithExportMaxBatchSize(p.config.Logs.Max.Batch.Size),
	)
}
