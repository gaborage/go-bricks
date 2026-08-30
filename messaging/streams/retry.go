package streams

import (
	"time"

	"github.com/gaborage/go-bricks/messaging/internal/delivery"
)

// RetryOptions bounds how often a failed delivery's handler is re-invoked in
// place before the lane settles on the failure. MaxAttempts counts the first
// attempt, so 1 retries nothing; the wait before attempt n (n >= 2) is
// InitialBackoff doubled n-2 times, capped at MaxBackoff.
type RetryOptions struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

const (
	// MaxRetryAttempts and MaxRetryWait bound what a declared policy may ask of one
	// partition. The waits happen inside that partition's own delivery callback, so
	// a long policy is a stall every OTHER tenant on the partition pays for; work
	// that needs more patience than this belongs in the hold, which parks one
	// tenant and lets the partition move.
	MaxRetryAttempts = 10
	MaxRetryWait     = time.Minute
)

// DefaultHoldRetry is the policy a holding consumer gets when it declares none.
// A consumer that does not hold keeps today's single attempt unless it asks for
// a policy itself.
var DefaultHoldRetry = RetryOptions{
	MaxAttempts:    3,
	InitialBackoff: 200 * time.Millisecond,
	MaxBackoff:     2 * time.Second,
}

// Permanent is the handler's claim that retrying is pointless: the delivery ends
// on the attempt that produced err whatever the policy allows. Permanent(nil) is
// nil.
func Permanent(err error) error { return delivery.Permanent(err) }

// toDeliveryRetry renders a declaration's policy for the pipeline. Nil in, nil
// out — the pipeline reads that as exactly one attempt.
func toDeliveryRetry(opts *RetryOptions) *delivery.Retry {
	if opts == nil {
		return nil
	}
	return &delivery.Retry{
		MaxAttempts:    opts.MaxAttempts,
		InitialBackoff: opts.InitialBackoff,
		MaxBackoff:     opts.MaxBackoff,
	}
}
