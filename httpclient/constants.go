package httpclient

import "time"

// Backoff and retry tuning. The values are the framework-side defaults applied
// when the operator's Config doesn't override them; they were previously inline
// literals scattered through client.go.
const (
	// defaultBackoffBase is the starting delay for exponential backoff when
	// the operator's RetryDelay is unset or non-positive.
	defaultBackoffBase = 50 * time.Millisecond

	// maxBackoffAttempt caps the exponent in `base * 2^attempt` so a very
	// long retry storm cannot overflow the multiplier. 2^20 = ~1M, which
	// combined with the cap below gives ~30s ceiling regardless of attempt.
	maxBackoffAttempt = 20

	// maxBackoffDuration is the upper bound on the per-attempt sleep produced
	// by backoffDelay. Any exponential growth past this is clamped.
	maxBackoffDuration = 30 * time.Second
)

// HTTP-status range constants. These mirror the stdlib's net/http.Status*
// constants but are kept here as named ranges for readability inside the
// retry-eligibility and success-classification predicates.
const (
	// httpStatusServerErrorMin is the lower bound (inclusive) of the 5xx
	// status range — the range that isRetryableStatus considers retry-worthy.
	httpStatusServerErrorMin = 500

	// httpStatusServerErrorMax is the upper bound (exclusive) of the 5xx
	// status range. 600 is the first non-5xx value so the half-open
	// [500, 600) interval excludes 600+ codes.
	httpStatusServerErrorMax = 600
)

// Payload-logging tunables.
const (
	// defaultMaxPayloadLogBytes caps the byte preview written to debug logs
	// when MaxPayloadLogBytes is unset or non-positive. Keeps debug payloads
	// from filling log storage during high-volume request capture.
	defaultMaxPayloadLogBytes = 1024
)

// Retry reason labels passed to tracking.IncRetry. These values match the
// retry.reason attribute values defined in the tracking package.
const (
	retryReasonTimeout       = "timeout"
	retryReasonNetwork       = "network"
	retryReasonBuildResponse = "build_response"
	retryReason5xx           = "5xx"
)

// OTel error.type attribute values returned by classifyError. These are the
// canonical string values emitted on the http.client.request.duration histogram
// and http.client.request.failed counter — kept as named constants so that
// both classifyError and its tests can reference them without duplication.
const (
	// errorTypeContextCanceled indicates the request was cancelled by the caller.
	errorTypeContextCanceled = "context_canceled"

	// errorTypeTimeout covers framework TimeoutError, context.DeadlineExceeded,
	// and generic net.Error timeouts that are not more specifically classified.
	errorTypeTimeout = "timeout"

	// errorTypeNameResolution indicates a DNS lookup failure (including timeouts).
	errorTypeNameResolution = "name_resolution_error"

	// errorTypeTLS indicates a TLS handshake or certificate verification failure.
	errorTypeTLS = "tls_error"

	// errorTypeConnection indicates a TCP dial failure.
	errorTypeConnection = "connection_error"

	// errorTypeInterceptorFailed indicates a request or response interceptor returned an error.
	errorTypeInterceptorFailed = "interceptor_failed"

	// errorTypeOther is the catch-all for unclassified errors.
	errorTypeOther = "_OTHER"
)
