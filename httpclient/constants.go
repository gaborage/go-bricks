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
