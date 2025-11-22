package observability

import "errors"

// ErrNilConfig is returned when Validate is called on a nil Config pointer.
var ErrNilConfig = errors.New("observability: config is nil")

// ErrMissingServiceName is returned when observability is enabled but no service name is configured.
var ErrMissingServiceName = errors.New("observability: service name is required when observability is enabled")

// ErrInvalidSampleRate is returned when the trace sample rate is outside the valid range [0.0, 1.0].
var ErrInvalidSampleRate = errors.New("observability: trace sample rate must be between 0.0 and 1.0")

// ErrShutdownTimeout is returned when graceful shutdown exceeds the timeout.
var ErrShutdownTimeout = errors.New("observability: shutdown timeout exceeded")

// ErrAlreadyInitialized is returned when attempting to initialize an already initialized provider.
var ErrAlreadyInitialized = errors.New("observability: provider already initialized")

// ErrNotInitialized is returned when attempting to use an uninitialized provider.
var ErrNotInitialized = errors.New("observability: provider not initialized")

// ErrInvalidProtocol is returned when the protocol (trace or metrics) is not "http" or "grpc".
var ErrInvalidProtocol = errors.New("observability: protocol must be either 'http' or 'grpc'")

// ErrInvalidLogSamplingRate is returned when the log sampling rate is outside the valid range [0.0, 1.0].
var ErrInvalidLogSamplingRate = errors.New("observability: log sampling rate must be between 0.0 and 1.0")

// ErrInvalidEndpointFormat is returned when the endpoint format doesn't match the protocol.
// gRPC endpoints must NOT include http:// or https:// scheme (use "host:port" format).
// HTTP endpoints MUST include http:// or https:// scheme.
var ErrInvalidEndpointFormat = errors.New("observability: invalid endpoint format for protocol")

// ErrInvalidCompression is returned when the compression value is not "gzip" or "none".
var ErrInvalidCompression = errors.New("observability: compression must be either 'gzip' or 'none'")

// ErrInvalidTemporality is returned when the temporality value is not "delta" or "cumulative".
var ErrInvalidTemporality = errors.New("observability: temporality must be either 'delta' or 'cumulative'")

// ErrInvalidHistogramAggregation is returned when the histogram aggregation is not "exponential" or "explicit".
var ErrInvalidHistogramAggregation = errors.New("observability: histogram aggregation must be either 'exponential' or 'explicit'")
