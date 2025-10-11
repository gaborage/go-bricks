package observability

import "time"

const (
	// EndpointStdout is a special endpoint value that outputs to stdout (for local development).
	EndpointStdout = "stdout"

	// ProtocolHTTP specifies OTLP over HTTP/protobuf.
	ProtocolHTTP = "http"

	// ProtocolGRPC specifies OTLP over gRPC.
	ProtocolGRPC = "grpc"

	// EnvironmentDevelopment is the default environment name for development mode.
	EnvironmentDevelopment = "development"
)

// BoolPtr returns a pointer to the provided bool value.
// Helpful when optional boolean configuration fields are used.
func BoolPtr(v bool) *bool {
	return &v
}

// Float64Ptr returns a pointer to the provided float64 value.
// Helpful when optional float64 configuration fields are used.
func Float64Ptr(v float64) *float64 {
	return &v
}

// cloneHeaderMap creates a deep copy of a header map to avoid aliasing.
// Returns nil if the input is nil.
func cloneHeaderMap(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	clone := make(map[string]string, len(headers))
	for k, v := range headers {
		clone[k] = v
	}
	return clone
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

	// Logs contains logging-specific configuration.
	Logs LogsConfig `mapstructure:"logs"`
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
		c.Environment = EnvironmentDevelopment
	}

	// Trace defaults
	c.applyTraceDefaults()

	// Metrics defaults
	c.applyMetricsDefaults()

	// Logs defaults
	c.applyLogsDefaults()
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

	// Sample rate default - only set when nil (not explicitly provided)
	// If explicitly set to 0.0, we respect that choice (and warn in NewProvider)
	if c.Trace.Sample.Rate == nil {
		c.Trace.Sample.Rate = Float64Ptr(1.0)
	}

	// Batch defaults - use environment-aware settings
	// Development: faster export for better debugging experience
	// Production: larger batches for efficiency
	if c.Trace.Batch.Timeout == 0 {
		if c.Environment == EnvironmentDevelopment || c.Trace.Endpoint == EndpointStdout {
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

func (c *Config) applyLogsDefaults() {
	// Set endpoint default first
	if c.Logs.Endpoint == "" {
		c.Logs.Endpoint = EndpointStdout
	}

	// Logs enabled default (true when not explicitly set and observability is enabled)
	// Only set when nil (unset). If explicitly set to false, preserve it.
	if c.Enabled && c.Logs.Enabled == nil {
		c.Logs.Enabled = BoolPtr(true)
	}

	// Protocol default - inherit from trace configuration
	if c.Logs.Protocol == "" {
		c.Logs.Protocol = c.Trace.Protocol
	}
	if c.Logs.Protocol == "" {
		c.Logs.Protocol = ProtocolHTTP
	}

	// Insecure default - inherit from trace configuration
	if c.Logs.Insecure == nil {
		c.Logs.Insecure = BoolPtr(c.Trace.Insecure)
	}

	// Headers default - clone from trace configuration if not set
	// Clone the map to avoid aliasing (mutations to Logs.Headers shouldn't affect Trace.Headers)
	if c.Logs.Headers == nil && c.Trace.Headers != nil {
		c.Logs.Headers = cloneHeaderMap(c.Trace.Headers)
	}

	// Deprecated: Sample rate configuration (dual-mode logging uses different approach)
	if c.Logs.Sample.Rate == nil {
		c.Logs.Sample.Rate = Float64Ptr(1.0)
	} else if *c.Logs.Sample.Rate != 1.0 {
		debugLogger.Println("Deprecated: logs.sample.rate is ignored in dual-mode logging. Action logs are always sampled (100%), trace logs filter by severity (WARN+).")
	}

	// Deprecated: AlwaysSampleHigh configuration (replaced by dual-mode routing)
	if c.Logs.Sample.AlwaysSampleHigh == nil {
		c.Logs.Sample.AlwaysSampleHigh = BoolPtr(true)
	} else if !*c.Logs.Sample.AlwaysSampleHigh {
		debugLogger.Println("Deprecated: logs.sample.always_sample_high is ignored in dual-mode logging. Trace logs are always WARN+ only.")
	}

	// Slow request threshold default (used by action log severity calculation)
	if c.Logs.SlowRequestThreshold == 0 {
		c.Logs.SlowRequestThreshold = 1 * time.Second
	}

	// Apply batch, export, and queue defaults
	c.applyLogsBatchDefaults()
}

// applyLogsBatchDefaults applies batch processing defaults for logs.
// Extracted to reduce cyclomatic complexity of applyLogsDefaults.
func (c *Config) applyLogsBatchDefaults() {
	// Batch timeout - use environment-aware settings (same pattern as traces)
	if c.Logs.Batch.Timeout == 0 {
		if c.Environment == EnvironmentDevelopment || c.Logs.Endpoint == EndpointStdout {
			// Development: 500ms for near-instant log visibility
			c.Logs.Batch.Timeout = 500 * time.Millisecond
		} else {
			// Production: 5s for efficient batching
			c.Logs.Batch.Timeout = 5 * time.Second
		}
	}

	// Batch size default
	if c.Logs.Batch.Size == 0 {
		c.Logs.Batch.Size = 512
	}

	// Export timeout default
	if c.Logs.Export.Timeout == 0 {
		c.Logs.Export.Timeout = 30 * time.Second
	}

	// Max queue size default
	if c.Logs.Max.Queue.Size == 0 {
		c.Logs.Max.Queue.Size = 2048
	}

	// Max batch size default
	if c.Logs.Max.Batch.Size == 0 {
		c.Logs.Max.Batch.Size = 512
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
	// 1.0 means collect all traces, 0.1 means collect 10% of traces, 0.0 means collect nothing.
	// Lower values reduce overhead and costs.
	// nil = apply default (1.0), explicit value = use that value (including 0.0).
	Rate *float64 `mapstructure:"rate"`
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

// LogsConfig defines configuration for log export via OTLP.
type LogsConfig struct {
	// Enabled controls whether OTLP log export is active.
	// Can be used to disable log export while keeping traces/metrics enabled.
	// nil = apply default (true when observability is enabled), false = explicitly disabled.
	Enabled *bool `mapstructure:"enabled"`

	// Endpoint specifies where to send log data.
	// Special value "stdout" enables console logging for local development.
	// For production, use OTLP endpoint (e.g., "http://localhost:4318" for HTTP or "localhost:4317" for gRPC).
	Endpoint string `mapstructure:"endpoint"`

	// Protocol specifies the OTLP protocol to use: "http" or "grpc".
	// If empty, logs inherit the trace protocol.
	Protocol string `mapstructure:"protocol"`

	// Insecure controls whether to use insecure connections (no TLS).
	// Only applicable for OTLP endpoints (http/grpc). Falls back to trace setting when unset.
	Insecure *bool `mapstructure:"insecure"`

	// DisableStdout controls whether to disable stdout logging when OTLP is enabled.
	// When false (default), logs go to both stdout and OTLP (useful for development).
	// When true, logs only go to OTLP (production efficiency).
	DisableStdout bool `mapstructure:"disable_stdout"`

	// Headers allows custom HTTP headers for OTLP exporters.
	// Useful for authentication tokens or API keys.
	// If nil or empty, logs inherit trace headers.
	Headers map[string]string `mapstructure:"headers"`

	// Sample contains sampling configuration for logs.
	// Deprecated: Sampling logic replaced by dual-mode logging in v2.1.
	// Action logs are always sampled (100%), trace logs filter by severity (WARN+ only).
	Sample LogSampleConfig `mapstructure:"sample"`

	// Batch contains batch processing configuration (reused from TraceConfig pattern).
	Batch BatchConfig `mapstructure:"batch"`

	// Export contains export timeout configuration (reused from TraceConfig pattern).
	Export ExportConfig `mapstructure:"export"`

	// Max contains maximum queue and batch size limits (reused from TraceConfig pattern).
	Max MaxConfig `mapstructure:"max"`

	// SlowRequestThreshold defines the latency threshold for marking HTTP requests as slow.
	// Requests exceeding this duration are logged with result_code="WARN" in action logs.
	// This is a system-wide threshold (no per-route overrides).
	// Default: 1 second.
	SlowRequestThreshold time.Duration `mapstructure:"slow_request_threshold"`
}

// LogSampleConfig defines sampling configuration for logs.
// Allows reducing log volume in production while preserving critical logs.
type LogSampleConfig struct {
	// Rate controls what fraction of INFO/DEBUG logs to export (0.0 to 1.0).
	// 1.0 means export all logs, 0.1 means export 10%, 0.0 means export nothing.
	// nil = apply default (1.0).
	Rate *float64 `mapstructure:"rate"`

	// AlwaysSampleHigh controls whether WARN/ERROR/FATAL logs are always exported.
	// When true (default), severity-based filtering ensures critical logs are never sampled out.
	// When false, sample rate applies to all log levels.
	// Nil means "use default".
	AlwaysSampleHigh *bool `mapstructure:"always_sample_high"`
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

	if err := c.validateMetricsConfig(); err != nil {
		return err
	}

	return c.validateLogsConfig()
}

func (c *Config) validateTraceConfig() error {
	// Validate sample rate if explicitly set
	if c.Trace.Sample.Rate != nil {
		rate := *c.Trace.Sample.Rate
		if rate < 0.0 || rate > 1.0 {
			return ErrInvalidSampleRate
		}
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

func (c *Config) validateLogsConfig() error {
	// Treat nil as false, only validate if explicitly enabled
	if c.Logs.Enabled == nil || !*c.Logs.Enabled {
		return nil
	}

	// Validate sample rate if explicitly set
	if c.Logs.Sample.Rate != nil {
		rate := *c.Logs.Sample.Rate
		if rate < 0.0 || rate > 1.0 {
			return ErrInvalidSampleRate
		}
	}

	// Stdout endpoint doesn't require protocol validation
	if c.Logs.Endpoint == EndpointStdout || c.Logs.Endpoint == "" {
		return nil
	}

	// Validate protocol
	protocol := c.Logs.Protocol
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
