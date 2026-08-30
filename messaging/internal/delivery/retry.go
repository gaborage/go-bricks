package delivery

import (
	"errors"
	"math"
	"time"
)

// maxDuration is the largest representable backoff, which an uncapped policy
// saturates at instead of wrapping.
const maxDuration = time.Duration(math.MaxInt64)

// Retry bounds how often one delivery's handler is re-invoked in place after a
// HandlerError. MaxAttempts counts the first attempt, so a policy of 1 retries
// nothing; the wait before attempt n (n >= 2) is InitialBackoff doubled n-2
// times, capped at MaxBackoff, and a zero InitialBackoff waits not at all.
type Retry struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// permanentError is a handler's claim that re-running it cannot help. It renders
// and unwraps as its cause, so a lane that neither knows nor cares about the
// claim sees the original error.
type permanentError struct {
	err error
}

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks err as not worth retrying: the delivery ends on the attempt
// that produced it, whatever the policy allows. Permanent(nil) is nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

// IsPermanent reports whether err, or anything it wraps, was marked Permanent.
func IsPermanent(err error) bool {
	var permanent permanentError
	return errors.As(err, &permanent)
}

// backoffFor returns the wait before the given attempt (2-based; attempt 1 never
// waits). The doubling is a bounded shift rather than a loop: MaxBackoff is
// optional, so nothing else would stop a long bound from shifting the duration
// past int64 and wrapping it NEGATIVE — which the wait door reads as "no wait",
// turning the tail of a long policy into a tight loop. A negative InitialBackoff
// is read as none rather than refused here; the lane's own validation rejects it
// where a caller can still be told which field was wrong.
func backoffFor(r *Retry, attempt int) time.Duration {
	if r == nil || attempt < 2 {
		return 0
	}

	base := max(r.InitialBackoff, 0)
	if base == 0 {
		return 0
	}

	// Named for what it is rather than "wait", which is the cancelable sleep this
	// package already has. A shift of 63 or more leaves maxDuration>>shift at zero,
	// which no positive base can be under, so the saturating branch covers it.
	backoff := maxDuration
	if shift := attempt - 2; base <= maxDuration>>shift {
		backoff = base << shift
	}

	if r.MaxBackoff > 0 {
		return min(backoff, r.MaxBackoff)
	}
	return backoff
}

// BackoffBudget reports how long a policy's waits add up to and whether they pass
// over budget. It stops at the wait that proves the crossing, so the running total
// never has to saturate: the duration is the exact total when the policy fits, and
// the sum through the crossing wait — a lower bound — when it does not.
func BackoffBudget(r *Retry, budget time.Duration) (total time.Duration, exceeded bool) {
	if r == nil {
		return 0, false
	}

	for attempt := 2; attempt <= r.MaxAttempts; attempt++ {
		wait := backoffFor(r, attempt)
		if wait == 0 {
			// Waits only grow, so the first zero says every later one is zero too.
			break
		}
		if wait > budget-total {
			return total + wait, true
		}
		total += wait
	}
	return total, false
}
