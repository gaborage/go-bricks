package server

import "time"

// HTTP Server Default Timeouts
//
// These constants define default timeout values for the Echo HTTP server.
// They are used in server configuration and tests to ensure consistent behavior.

const (
	// DefaultReadTimeout is the maximum duration for reading the entire request.
	// This includes reading the request body and headers.
	// Used in server.go and server_test.go (28+ occurrences).
	DefaultReadTimeout = 15 * time.Second

	// DefaultWriteTimeout is the maximum duration before timing out writes of the response.
	// This is set from the start of the request handler to the end of the response write.
	DefaultWriteTimeout = 15 * time.Second

	// DefaultIdleTimeout is the maximum amount of time to wait for the next request
	// when keep-alives are enabled.
	DefaultIdleTimeout = 60 * time.Second

	// DefaultShutdownTimeout is the maximum time to wait for graceful shutdown.
	// This allows in-flight requests to complete before forceful termination.
	DefaultShutdownTimeout = 30 * time.Second

	// DefaultAPITimeout is the maximum duration for external API calls.
	// Used in HTTP client configurations for backend service communication.
	DefaultAPITimeout = 30 * time.Second
)

// Test-Specific Timeouts
//
// These constants are used exclusively in test files for simulating
// timeout scenarios and synchronization.

const (
	// TestShortTimeout is a very short timeout for testing timeout behavior.
	// Used in handler tests to verify proper timeout error handling.
	TestShortTimeout = 100 * time.Millisecond

	// TestMediumTimeout is a moderate timeout for async operations in tests.
	TestMediumTimeout = 1 * time.Second

	// TestLongTimeout is a generous timeout for slow operations in tests.
	// Used when testing complex handlers or integration scenarios.
	TestLongTimeout = 5 * time.Second
)
