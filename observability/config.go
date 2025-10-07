package observability

import "time"

// Config defines the configuration for observability features.
// It supports automatic injection via the GoBricks config system.
type Config struct {
	// Enabled controls whether observability is active.
	// When false, all observability operations become no-ops.
	Enabled bool `config:"observability.enabled" default:"false"`

	// ServiceName identifies the service in traces and metrics.
	// This is required when observability is enabled.
	ServiceName string `config:"observability.service.name" required:"true"`

	// ServiceVersion specifies the version of the service.
	// Used for filtering and grouping in observability backends.
	ServiceVersion string `config:"observability.service.version" default:"unknown"`

	// Environment indicates the deployment environment (e.g., production, staging, development).
	Environment string `config:"observability.environment" default:"development"`

	// TraceConfig contains tracing-specific configuration.
	Trace TraceConfig `config:"observability.trace"`

	// MetricsConfig contains metrics-specific configuration.
	Metrics MetricsConfig `config:"observability.metrics"`
}

// TraceConfig defines configuration for distributed tracing.
type TraceConfig struct {
	// Enabled controls whether tracing is active.
	// Can be used to disable tracing while keeping metrics enabled.
	Enabled bool `config:"enabled" default:"true"`

	// Endpoint specifies where to send trace data.
	// Special value "stdout" enables console logging for local development.
	// For production, use OTLP endpoint (e.g., "http://localhost:4318" for HTTP or "localhost:4317" for gRPC).
	Endpoint string `config:"endpoint" default:"stdout"`

	// Protocol specifies the OTLP protocol to use: "http" or "grpc".
	// Only used when Endpoint is not "stdout".
	// HTTP uses OTLP/HTTP protocol (default port 4318).
	// gRPC uses OTLP/gRPC protocol (default port 4317).
	Protocol string `config:"protocol" default:"http"`

	// Insecure controls whether to use insecure connections (no TLS).
	// Only applicable for OTLP endpoints (http/grpc).
	// Set to true for local development without TLS.
	Insecure bool `config:"insecure" default:"true"`

	// Headers allows custom HTTP headers for OTLP exporters.
	// Useful for authentication tokens or API keys.
	// Format: map of header name to header value.
	Headers map[string]string `config:"headers"`

	// SampleRate controls what fraction of traces to collect (0.0 to 1.0).
	// 1.0 means collect all traces, 0.1 means collect 10% of traces.
	// Lower values reduce overhead and costs.
	SampleRate float64 `config:"sample.rate" default:"1.0"`

	// BatchTimeout specifies how long to wait before sending a batch of spans.
	// Lower values reduce latency but increase network overhead.
	BatchTimeout time.Duration `config:"batch.timeout" default:"5s"`

	// ExportTimeout specifies the maximum time to wait for span export.
	// Prevents slow backends from blocking the application.
	ExportTimeout time.Duration `config:"export.timeout" default:"30s"`

	// MaxQueueSize limits the number of spans buffered for export.
	// Prevents memory exhaustion under high load.
	MaxQueueSize int `config:"max.queue.size" default:"2048"`

	// MaxBatchSize limits the number of spans per export batch.
	// Smaller batches reduce latency, larger batches reduce overhead.
	MaxBatchSize int `config:"max.batch.size" default:"512"`
}

// MetricsConfig defines configuration for metrics collection.
type MetricsConfig struct {
	// Enabled controls whether metrics collection is active.
	// Can be used to disable metrics while keeping tracing enabled.
	Enabled bool `config:"enabled" default:"true"`

	// Endpoint specifies where to send metric data.
	// Special value "stdout" enables console logging for local development.
	// For production, use OTLP endpoint (e.g., "http://localhost:4318").
	Endpoint string `config:"endpoint" default:"stdout"`

	// Interval specifies how often to export metrics.
	// Shorter intervals provide more real-time data but increase overhead.
	Interval time.Duration `config:"interval" default:"10s"`

	// ExportTimeout specifies the maximum time to wait for metric export.
	// Prevents slow backends from blocking the application.
	ExportTimeout time.Duration `config:"export.timeout" default:"30s"`
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

	if c.ServiceName == "" {
		return ErrMissingServiceName
	}

	if c.Trace.SampleRate < 0.0 || c.Trace.SampleRate > 1.0 {
		return ErrInvalidSampleRate
	}

	// Validate protocol for OTLP endpoints
	if c.Trace.Endpoint != "stdout" && c.Trace.Endpoint != "" {
		protocol := c.Trace.Protocol
		if protocol != "http" && protocol != "grpc" {
			return ErrInvalidProtocol
		}
	}

	return nil
}
