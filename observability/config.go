package observability

import "time"

const (
	// EndpointStdout is a special endpoint value that outputs to stdout (for local development).
	EndpointStdout = "stdout"

	// ProtocolHTTP specifies OTLP over HTTP/protobuf.
	ProtocolHTTP = "http"

	// ProtocolGRPC specifies OTLP over gRPC.
	ProtocolGRPC = "grpc"
)

// BoolPtr returns a pointer to the provided bool value.
// Helpful when optional boolean configuration fields are used.
func BoolPtr(v bool) *bool {
	return &v
}

// Config defines the configuration for observability features.
// It supports automatic unmarshaling via the GoBricks config system using mapstructure tags.
type Config struct {
	// Enabled controls whether observability is active.
	// When false, all observability operations become no-ops.
	Enabled bool `mapstructure:"enabled"`

	// Service contains service identification metadata.
	Service ServiceConfig `mapstructure:"service"`

	// Environment indicates the deployment environment (e.g., production, staging, development).
	Environment string `mapstructure:"environment"`

	// Trace contains tracing-specific configuration.
	Trace TraceConfig `mapstructure:"trace"`

	// Metrics contains metrics-specific configuration.
	Metrics MetricsConfig `mapstructure:"metrics"`
}

// ServiceConfig contains service identification metadata.
type ServiceConfig struct {
	// Name identifies the service in traces and metrics.
	// This is required when observability is enabled.
	Name string `mapstructure:"name"`

	// Version specifies the version of the service.
	// Used for filtering and grouping in observability backends.
	Version string `mapstructure:"version"`
}

// ApplyDefaults sets default values for any config fields that are not specified.
// This is called after unmarshaling to ensure all fields have sensible defaults.
func (c *Config) ApplyDefaults() {
	// Service defaults
	if c.Service.Version == "" {
		c.Service.Version = "unknown"
	}

	// Environment default
	if c.Environment == "" {
		c.Environment = "development"
	}

	// Trace defaults
	c.applyTraceDefaults()

	// Metrics defaults
	c.applyMetricsDefaults()
}

func (c *Config) applyTraceDefaults() {
	// Set endpoint default first
	if c.Trace.Endpoint == "" {
		c.Trace.Endpoint = EndpointStdout
	}

	// Trace enabled default (true when not explicitly set and observability is enabled)
	// Only set when nil (unset). If explicitly set to false, preserve it.
	if c.Enabled && c.Trace.Enabled == nil {
		c.Trace.Enabled = BoolPtr(true)
	}

	if c.Trace.Protocol == "" {
		c.Trace.Protocol = ProtocolHTTP
	}

	// Insecure defaults to true for local development
	// Note: With mapstructure, we can't distinguish between explicitly false and unset
	// We'll assume insecure=true as default for stdout endpoint
	if c.Trace.Endpoint == EndpointStdout {
		c.Trace.Insecure = true
	}

	// Sample rate default - ALWAYS set to 1.0 if zero to prevent silent span dropping
	if c.Trace.Sample.Rate == 0.0 {
		c.Trace.Sample.Rate = 1.0
	}

	// Batch defaults - use environment-aware settings
	// Development: faster export for better debugging experience
	// Production: larger batches for efficiency
	if c.Trace.Batch.Timeout == 0 {
		if c.Environment == "development" || c.Trace.Endpoint == EndpointStdout {
			// Development: 500ms for near-instant span visibility
			c.Trace.Batch.Timeout = 500 * time.Millisecond
		} else {
			// Production: 5s for efficient batching
			c.Trace.Batch.Timeout = 5 * time.Second
		}
	}
	if c.Trace.Batch.Size == 0 {
		c.Trace.Batch.Size = 512
	}

	// Export timeout default
	if c.Trace.Export.Timeout == 0 {
		c.Trace.Export.Timeout = 30 * time.Second
	}

	// Max queue and batch size defaults
	if c.Trace.Max.Queue.Size == 0 {
		c.Trace.Max.Queue.Size = 2048
	}
	if c.Trace.Max.Batch.Size == 0 {
		c.Trace.Max.Batch.Size = 512
	}
}

func (c *Config) applyMetricsDefaults() {
	// Set endpoint default first
	if c.Metrics.Endpoint == "" {
		c.Metrics.Endpoint = EndpointStdout
	}

	// Metrics enabled default (true when not explicitly set and observability is enabled)
	// Only set when nil (unset). If explicitly set to false, preserve it.
	if c.Enabled && c.Metrics.Enabled == nil {
		c.Metrics.Enabled = BoolPtr(true)
	}

	// Interval default
	if c.Metrics.Interval == 0 {
		c.Metrics.Interval = 10 * time.Second
	}

	// Export timeout default
	if c.Metrics.Export.Timeout == 0 {
		c.Metrics.Export.Timeout = 30 * time.Second
	}
}

// TraceConfig defines configuration for distributed tracing.
type TraceConfig struct {
	// Enabled controls whether tracing is active.
	// Can be used to disable tracing while keeping metrics enabled.
	// nil = apply default (true when observability is enabled), false = explicitly disabled.
	Enabled *bool `mapstructure:"enabled"`

	// Endpoint specifies where to send trace data.
	// Special value "stdout" enables console logging for local development.
	// For production, use OTLP endpoint (e.g., "http://localhost:4318" for HTTP or "localhost:4317" for gRPC).
	Endpoint string `mapstructure:"endpoint"`

	// Protocol specifies the OTLP protocol to use: "http" or "grpc".
	// Only used when Endpoint is not "stdout".
	// HTTP uses OTLP/HTTP protocol (default port 4318).
	// gRPC uses OTLP/gRPC protocol (default port 4317).
	Protocol string `mapstructure:"protocol"`

	// Insecure controls whether to use insecure connections (no TLS).
	// Only applicable for OTLP endpoints (http/grpc).
	// Set to true for local development without TLS.
	Insecure bool `mapstructure:"insecure"`

	// Headers allows custom HTTP headers for OTLP exporters.
	// Useful for authentication tokens or API keys.
	// Format: map of header name to header value.
	Headers map[string]string `mapstructure:"headers"`

	// Sample contains sampling configuration.
	Sample SampleConfig `mapstructure:"sample"`

	// Batch contains batch processing configuration.
	Batch BatchConfig `mapstructure:"batch"`

	// Export contains export timeout configuration.
	Export ExportConfig `mapstructure:"export"`

	// Max contains maximum queue and batch size limits.
	Max MaxConfig `mapstructure:"max"`
}

// SampleConfig defines sampling configuration for traces.
type SampleConfig struct {
	// Rate controls what fraction of traces to collect (0.0 to 1.0).
	// 1.0 means collect all traces, 0.1 means collect 10% of traces.
	// Lower values reduce overhead and costs.
	Rate float64 `mapstructure:"rate"`
}

// BatchConfig defines batch processing configuration for traces.
type BatchConfig struct {
	// Timeout specifies how long to wait before sending a batch of spans.
	// Lower values reduce latency but increase network overhead.
	Timeout time.Duration `mapstructure:"timeout"`

	// Size limits the number of spans per export batch.
	// Smaller batches reduce latency, larger batches reduce overhead.
	Size int `mapstructure:"size"`
}

// ExportConfig defines export timeout configuration.
type ExportConfig struct {
	// Timeout specifies the maximum time to wait for span export.
	// Prevents slow backends from blocking the application.
	Timeout time.Duration `mapstructure:"timeout"`
}

// MaxConfig defines maximum queue and batch size limits.
type MaxConfig struct {
	// Queue contains queue size configuration.
	Queue QueueConfig `mapstructure:"queue"`

	// Batch contains batch size configuration.
	Batch MaxBatchConfig `mapstructure:"batch"`
}

// QueueConfig defines queue size configuration.
type QueueConfig struct {
	// Size limits the number of spans buffered for export.
	// Prevents memory exhaustion under high load.
	Size int `mapstructure:"size"`
}

// MaxBatchConfig defines batch size configuration.
type MaxBatchConfig struct {
	// Size limits the number of spans per export batch.
	// Smaller batches reduce latency, larger batches reduce overhead.
	Size int `mapstructure:"size"`
}

// MetricsConfig defines configuration for metrics collection.
type MetricsConfig struct {
	// Enabled controls whether metrics collection is active.
	// Can be used to disable metrics while keeping tracing enabled.
	// nil = apply default (true when observability is enabled), false = explicitly disabled.
	Enabled *bool `mapstructure:"enabled"`

	// Endpoint specifies where to send metric data.
	// Special value "stdout" enables console logging for local development.
	// For production, use OTLP endpoint (e.g., "http://localhost:4318").
	Endpoint string `mapstructure:"endpoint"`

	// Protocol specifies the OTLP protocol to use: "http" or "grpc".
	// If empty, metrics inherit the trace protocol.
	Protocol string `mapstructure:"protocol"`

	// Insecure controls whether to use insecure connections (no TLS).
	// Only applicable for OTLP endpoints (http/grpc). Falls back to trace setting when unset.
	Insecure *bool `mapstructure:"insecure"`

	// Headers allows custom headers for OTLP exporters (e.g., DataDog API keys).
	// If nil or empty, metrics inherit trace headers.
	Headers map[string]string `mapstructure:"headers"`

	// Interval specifies how often to export metrics.
	// Shorter intervals provide more real-time data but increase overhead.
	Interval time.Duration `mapstructure:"interval"`

	// Export contains export timeout configuration.
	Export MetricsExportConfig `mapstructure:"export"`
}

// MetricsExportConfig defines export timeout configuration for metrics.
type MetricsExportConfig struct {
	// Timeout specifies the maximum time to wait for metric export.
	// Prevents slow backends from blocking the application.
	Timeout time.Duration `mapstructure:"timeout"`
}

// Validate checks the configuration for common errors.
// Returns an error if the configuration is invalid.
func (c *Config) Validate() error {
	if c == nil {
		return ErrNilConfig
	}

	if !c.Enabled {
		return nil // No validation needed when disabled
	}

	if c.Service.Name == "" {
		return ErrMissingServiceName
	}

	if err := c.validateTraceConfig(); err != nil {
		return err
	}

	return c.validateMetricsConfig()
}

func (c *Config) validateTraceConfig() error {
	if c.Trace.Sample.Rate < 0.0 || c.Trace.Sample.Rate > 1.0 {
		return ErrInvalidSampleRate
	}

	if c.Trace.Endpoint == EndpointStdout || c.Trace.Endpoint == "" {
		return nil
	}

	protocol := c.Trace.Protocol
	if protocol == "" {
		protocol = ProtocolHTTP
	}

	switch protocol {
	case ProtocolHTTP, ProtocolGRPC:
		return nil
	default:
		return ErrInvalidProtocol
	}
}

func (c *Config) validateMetricsConfig() error {
	// Treat nil as false, only validate if explicitly enabled
	if c.Metrics.Enabled == nil || !*c.Metrics.Enabled {
		return nil
	}

	if c.Metrics.Endpoint == EndpointStdout || c.Metrics.Endpoint == "" {
		return nil
	}

	protocol := c.Metrics.Protocol
	if protocol == "" {
		protocol = c.Trace.Protocol
	}
	if protocol == "" {
		protocol = ProtocolHTTP
	}

	if protocol != ProtocolHTTP && protocol != ProtocolGRPC {
		return ErrInvalidProtocol
	}

	return nil
}
