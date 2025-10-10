package observability

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"google.golang.org/grpc/credentials/insecure"
)

// Re-export insecure credentials to avoid variable shadowing
var grpcInsecureCredentials = insecure.NewCredentials

// initLogProvider initializes the OpenTelemetry logger provider.
func (p *provider) initLogProvider() error {
	// Create resource with service information (reuse from trace provider)
	res, err := p.createResource()
	if err != nil {
		return fmt.Errorf("failed to create resource: %w", err)
	}

	// Create log exporter
	exporter, err := p.createLogExporter()
	if err != nil {
		return fmt.Errorf("failed to create log exporter: %w", err)
	}

	// Create processor with severity-based sampling
	processor := p.createLogProcessor(exporter)

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
		return stdoutlog.New(
			stdoutlog.WithPrettyPrint(),
		)
	}

	// Create OTLP exporter based on protocol
	protocol := p.config.Logs.Protocol
	debugLogger.Printf("Creating OTLP log exporter: protocol=%s, endpoint=%s, insecure=%v",
		protocol, endpoint, p.config.Logs.Insecure != nil && *p.config.Logs.Insecure)

	switch protocol {
	case ProtocolHTTP:
		return p.createOTLPHTTPLogExporter()
	case ProtocolGRPC:
		return p.createOTLPGRPCLogExporter()
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

	debugLogger.Printf("Creating OTLP HTTP log exporter: endpoint=%s, insecure=%v, headers_count=%d",
		p.config.Logs.Endpoint, useInsecure, len(p.config.Logs.Headers))

	opts := []otlploghttp.Option{
		otlploghttp.WithEndpoint(p.config.Logs.Endpoint),
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

	debugLogger.Printf("Creating OTLP gRPC log exporter: endpoint=%s, insecure=%v, headers_count=%d",
		p.config.Logs.Endpoint, useInsecure, len(p.config.Logs.Headers))

	opts := []otlploggrpc.Option{
		otlploggrpc.WithEndpoint(p.config.Logs.Endpoint),
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

// createLogProcessor creates a log processor with batching and sampling.
func (p *provider) createLogProcessor(exporter sdklog.Exporter) sdklog.Processor {
	// Wrap exporter with severity filter if sampling is configured
	filteredExporter := p.wrapWithSeverityFilter(exporter)

	// Create batch processor with configured options
	debugLogger.Printf("Creating BatchProcessor for logs with timeout=%v, queue_size=%d, batch_size=%d",
		p.config.Logs.Batch.Timeout, p.config.Logs.Max.Queue.Size, p.config.Logs.Max.Batch.Size)

	return sdklog.NewBatchProcessor(
		filteredExporter,
		sdklog.WithExportTimeout(p.config.Logs.Export.Timeout),
		sdklog.WithExportInterval(p.config.Logs.Batch.Timeout),
		sdklog.WithMaxQueueSize(p.config.Logs.Max.Queue.Size),
		sdklog.WithExportMaxBatchSize(p.config.Logs.Max.Batch.Size),
	)
}

// severityFilterExporter wraps an exporter with severity-based sampling logic.
// It filters log records based on their severity level and configured sample rate.
type severityFilterExporter struct {
	wrapped          sdklog.Exporter
	sampleRate       float64
	alwaysSampleHigh bool
}

const mantissaMask = (uint64(1) << 53) - 1

// Export filters log records based on severity and sample rate before exporting.
func (e *severityFilterExporter) Export(ctx context.Context, records []sdklog.Record) error {
	// If sample rate is 1.0 and we're not doing severity filtering, pass through
	if e.sampleRate >= 1.0 && !e.alwaysSampleHigh {
		return e.wrapped.Export(ctx, records)
	}

	// Filter records based on severity and sample rate
	filtered := make([]sdklog.Record, 0, len(records))
	for i := range records {
		if e.shouldExport(&records[i]) {
			filtered = append(filtered, records[i])
		}
	}

	if len(filtered) == 0 {
		return nil
	}

	return e.wrapped.Export(ctx, filtered)
}

// shouldExport determines if a log record should be exported based on severity and sampling.
func (e *severityFilterExporter) shouldExport(rec *sdklog.Record) bool {
	// Note: OpenTelemetry log severity levels:
	// Trace=1, Debug=5, Info=9, Warn=13, Error=17, Fatal=21
	// We consider Warn (13) and above as "high severity"

	severity := rec.Severity()

	// Always export high-severity logs (WARN, ERROR, FATAL) if configured
	if e.alwaysSampleHigh && severity >= 13 { // 13 = WARN
		return true
	}

	// For lower severity logs (or if alwaysSampleHigh is false), apply sampling
	// Full sampling - accept all logs
	if e.sampleRate >= 1.0 {
		return true
	}

	// Zero sampling - reject all logs
	if e.sampleRate <= 0.0 {
		return false
	}

	// Deterministic hash-based sampling for rates between 0.0 and 1.0
	key := recordSampleKey(rec)
	fraction := float64(key&mantissaMask) / float64(mantissaMask)
	return fraction < e.sampleRate
}

// Shutdown shuts down the wrapped exporter.
func (e *severityFilterExporter) Shutdown(ctx context.Context) error {
	return e.wrapped.Shutdown(ctx)
}

// ForceFlush flushes the wrapped exporter.
func (e *severityFilterExporter) ForceFlush(ctx context.Context) error {
	return e.wrapped.ForceFlush(ctx)
}

// wrapWithSeverityFilter wraps an exporter with severity-based sampling.
// Returns the original exporter if no sampling is needed.
func (p *provider) wrapWithSeverityFilter(exporter sdklog.Exporter) sdklog.Exporter {
	sampleRate := 1.0
	if p.config.Logs.Sample.Rate != nil {
		sampleRate = *p.config.Logs.Sample.Rate
	}

	alwaysSampleHigh := true
	if p.config.Logs.Sample.AlwaysSampleHigh != nil {
		alwaysSampleHigh = *p.config.Logs.Sample.AlwaysSampleHigh
	}

	// If sample rate is 1.0 and always_sample_high is false, no filtering needed
	if sampleRate == 1.0 && !alwaysSampleHigh {
		return exporter
	}

	debugLogger.Printf("Wrapping log exporter with severity filter: sample_rate=%.2f, always_sample_high=%v",
		sampleRate, alwaysSampleHigh)

	return &severityFilterExporter{
		wrapped:          exporter,
		sampleRate:       sampleRate,
		alwaysSampleHigh: alwaysSampleHigh,
	}
}

func recordSampleKey(rec *sdklog.Record) uint64 {
	if tid := rec.TraceID(); tid.IsValid() {
		return binary.LittleEndian.Uint64(tid[:8])
	}

	if sid := rec.SpanID(); sid.IsValid() {
		return binary.LittleEndian.Uint64(sid[:])
	}

	h := fnv.New64a()
	writeToken := func(s string) {
		if s == "" {
			return
		}
		h.Write([]byte(s))
		h.Write([]byte{0})
	}

	writeToken(rec.EventName())
	writeToken(rec.SeverityText())

	if body := rec.Body(); body.Kind() != log.KindEmpty {
		writeStableValue(writeToken, body)
	}

	if rec.AttributesLen() > 0 {
		attrs := make([]log.KeyValue, 0, rec.AttributesLen())
		rec.WalkAttributes(func(kv log.KeyValue) bool {
			attrs = append(attrs, kv)
			return true
		})
		sort.Slice(attrs, func(i, j int) bool {
			return attrs[i].Key < attrs[j].Key
		})
		for i := range attrs {
			writeToken(attrs[i].Key)
			writeStableValue(writeToken, attrs[i].Value)
		}
	}

	key := h.Sum64()
	if key == 0 {
		if ts := rec.Timestamp(); !ts.IsZero() {
			writeToken(ts.Format(time.RFC3339Nano))
			key = h.Sum64()
		}
	}
	if key == 0 {
		if ots := rec.ObservedTimestamp(); !ots.IsZero() {
			writeToken(ots.Format(time.RFC3339Nano))
			key = h.Sum64()
		}
	}
	if key == 0 {
		key = 1 // avoid returning zero
	}

	return key
}

func writeStableValue(writeToken func(string), value log.Value) {
	switch value.Kind() {
	case log.KindString:
		writeToken(value.AsString())
	case log.KindInt64:
		writeToken(strconv.FormatInt(value.AsInt64(), 10))
	case log.KindFloat64:
		writeToken(strconv.FormatFloat(value.AsFloat64(), 'g', -1, 64))
	case log.KindBool:
		writeToken(strconv.FormatBool(value.AsBool()))
	case log.KindBytes:
		writeToken(base64.StdEncoding.EncodeToString(value.AsBytes()))
	case log.KindSlice:
		sliceValues := value.AsSlice()
		writeToken(strconv.Itoa(len(sliceValues)))
		for _, v := range sliceValues {
			writeStableValue(writeToken, v)
		}
	case log.KindMap:
		original := value.AsMap()
		kvs := make([]log.KeyValue, len(original))
		copy(kvs, original)
		sort.Slice(kvs, func(i, j int) bool {
			return kvs[i].Key < kvs[j].Key
		})
		writeToken(strconv.Itoa(len(kvs)))
		for _, kv := range kvs {
			writeToken(kv.Key)
			writeStableValue(writeToken, kv.Value)
		}
	default:
		writeToken(value.String())
	}
}
