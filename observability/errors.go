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
