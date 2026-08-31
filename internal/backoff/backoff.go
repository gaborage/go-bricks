// Package backoff is the saturating exponential used wherever the framework
// waits and retries. It computes a raw delay only: jitter, zero-value defaults,
// and per-site reset stay at the caller.
//
// Audit of the three adopters (gaborage/go-bricks#1249):
//
//	Site                         Jitter                         Reset                         base <= 0
//	messaging computeBackoff     math/rand/v2 full jitter       reconnect resets on success;  package default
//	                             (#nosec G404 stays at caller)  publish-retry is per-operation
//	httpclient backoffDelay      crypto/rand full jitter        per-operation                 package default
//	                             (RNG-failure fallback stays)
//	delivery backoffFor          none                           per-operation                 0
//
// The outbox relay idle path (ADR-088) is a later consumer: it waits a fixed
// poll interval today, not this series. The helper is (base, cap, shift) so
// that path can adopt it without this package knowing about ledgers or locks.
package backoff

import (
	"math"
	"time"
)

// MaxDuration is the largest representable backoff. An uncapped series
// saturates here instead of wrapping past int64.
const MaxDuration = time.Duration(math.MaxInt64)

// Saturating returns base shifted shift times, saturating at MaxDuration
// instead of wrapping. maxDelay > 0 clamps the result; maxDelay <= 0 leaves
// it uncapped (the delivery policy's optional MaxBackoff).
//
// A non-positive base is 0: callers that want a package default apply it
// first. A negative shift is no doublings, so a computed exponent cannot
// wrap through uint and saturate by accident.
func Saturating(base, maxDelay time.Duration, shift int) time.Duration {
	if base <= 0 {
		return 0
	}
	if shift < 0 {
		shift = 0
	}

	// A shift of 63 or more leaves MaxDuration>>shift at zero, which no
	// positive base can be under, so the saturating branch covers it.
	d := MaxDuration
	if base <= MaxDuration>>shift {
		d = base << shift
	}
	if maxDelay > 0 {
		return min(d, maxDelay)
	}
	return d
}
