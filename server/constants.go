package server

import "time"

// Standardized API error codes used in APIErrorResponse envelopes.
const (
	errCodeBadRequest         = "BAD_REQUEST"
	errCodeUnauthorized       = "UNAUTHORIZED"
	errCodeForbidden          = "FORBIDDEN"
	errCodeNotFound           = "NOT_FOUND"
	errCodeConflict           = "CONFLICT"
	errCodeTooManyRequests    = "TOO_MANY_REQUESTS"
	errCodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	errCodeMethodNotAllowed   = "METHOD_NOT_ALLOWED"
	errCodeInternalError      = "INTERNAL_ERROR"
)

// Common user-facing error messages.
const (
	msgRateLimitExceeded = "Rate limit exceeded"
)

// Standard log/JSON field key constants used in handlers and middleware.
const (
	fieldStatus    = "status"
	fieldError     = "error"
	fieldMessage   = "message"
	fieldRequestID = "request_id"
	fieldTimestamp = "timestamp"
	fieldTraceID   = "traceId"

	statusOK    = "ok"
	statusReady = "ready"
)

// reservedMetaKeys enumerates envelope meta keys owned by the framework. Handler-supplied
// values for these keys are dropped during merge and a structured warning is emitted so
// observability/SRE tooling sees a single authoritative source for timestamp/traceId.
//
// Immutable after package init; do not mutate (Go has no const map).
var reservedMetaKeys = map[string]struct{}{
	fieldTimestamp: {},
	fieldTraceID:   {},
}

// HTTP Server Default Timeouts
//
// The following constants are exported as a reference for the framework's default values
// and for use by callers that configure the server programmatically.

const (
	// DefaultReadTimeout is the maximum duration for reading the entire request.
	// This includes reading the request body and headers.
	DefaultReadTimeout = 15 * time.Second

	// DefaultWriteTimeout is the maximum duration before timing out writes of the response.
	// This is set from the start of the request handler to the end of the response write.
	DefaultWriteTimeout = 30 * time.Second

	// DefaultIdleTimeout is the maximum amount of time to wait for the next request
	// when keep-alives are enabled.
	DefaultIdleTimeout = 60 * time.Second

	// DefaultShutdownTimeout is the maximum time to wait for graceful shutdown.
	// This allows in-flight requests to complete before forceful termination.
	DefaultShutdownTimeout = 10 * time.Second

	// DefaultAPITimeout is the maximum duration for external API calls.
	DefaultAPITimeout = 30 * time.Second
)
