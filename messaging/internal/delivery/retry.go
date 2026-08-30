package delivery

import (
	"errors"
	"time"
)

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
// waits). The shift is bounded before it runs: a large attempt count would
// otherwise shift the duration out of range and wrap to a nonsense wait.
func backoffFor(r *Retry, attempt int) time.Duration {
	if r == nil || attempt < 2 || r.InitialBackoff <= 0 {
		return 0
	}

	wait := r.InitialBackoff
	for i := 2; i < attempt; i++ {
		wait *= 2
		if r.MaxBackoff > 0 && wait >= r.MaxBackoff {
			return r.MaxBackoff
		}
	}
	if r.MaxBackoff > 0 && wait > r.MaxBackoff {
		return r.MaxBackoff
	}
	return wait
}
