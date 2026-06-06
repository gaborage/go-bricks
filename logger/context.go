package logger

import (
	"context"
	"sync/atomic"

	"github.com/rs/zerolog"
)

// contextKey is the type for context keys to avoid collisions
type contextKey string

const (
	// countersKey is the context key for the shared per-request counters struct
	// (AMQP/DB operation counts and elapsed time). A single key holds all four
	// values so request enrichment costs one context.WithValue instead of four.
	countersKey contextKey = "request_counters"
	// severityHookKey stores a callback for request-level severity tracking
	severityHookKey contextKey = "severity_hook"
)

// requestCounters holds the per-request AMQP/DB operation counts and elapsed
// times behind a single context value. Fields are atomic.Int64 (rather than bare
// int64) so 64-bit atomic access is correctly aligned by construction on every
// platform. Always stored and passed by pointer — never copy a requestCounters.
type requestCounters struct {
	amqpCount   atomic.Int64
	amqpElapsed atomic.Int64
	dbCount     atomic.Int64
	dbElapsed   atomic.Int64
}

// countersFromContext returns the shared counters struct, or nil when the
// context is nil or was never seeded (preserving nil-safe zero-value reads).
func countersFromContext(ctx context.Context) *requestCounters {
	if ctx == nil {
		return nil
	}
	if c, ok := ctx.Value(countersKey).(*requestCounters); ok {
		return c
	}
	return nil
}

// WithRequestCounters attaches the shared per-request counters struct (AMQP and
// DB operation counts and elapsed times) exactly once. It is idempotent — a second
// call returns the context unchanged, never resetting recorded values — and
// nil-safe (a nil context is returned as-is). This is the single seeder; a
// context seeded once exposes all four counters regardless of which name was used.
func WithRequestCounters(ctx context.Context) context.Context {
	if ctx == nil || countersFromContext(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, countersKey, &requestCounters{})
}

// WithAMQPCounter seeds the shared per-request counters. Retained for backward
// compatibility; prefer WithRequestCounters (both seed the same struct).
func WithAMQPCounter(ctx context.Context) context.Context {
	return WithRequestCounters(ctx)
}

// WithDBCounter seeds the shared per-request counters. Retained for backward
// compatibility; prefer WithRequestCounters (both seed the same struct).
func WithDBCounter(ctx context.Context) context.Context {
	return WithRequestCounters(ctx)
}

// WithSeverityHook attaches a severity hook to the context. The hook is used by the
// logging adapter to propagate WARN/ERROR logs back to request middleware for routing.
func WithSeverityHook(ctx context.Context, hook func(zerolog.Level)) context.Context {
	if ctx == nil || hook == nil {
		return ctx
	}
	return context.WithValue(ctx, severityHookKey, hook)
}

// severityHookFromContext retrieves the severity hook from the context when present.
func severityHookFromContext(ctx context.Context) func(zerolog.Level) {
	if ctx == nil {
		return nil
	}
	if hook, ok := ctx.Value(severityHookKey).(func(zerolog.Level)); ok {
		return hook
	}
	return nil
}

// IncrementAMQPCounter increments the AMQP message counter in the context
func IncrementAMQPCounter(ctx context.Context) {
	if c := countersFromContext(ctx); c != nil {
		c.amqpCount.Add(1)
	}
}

// GetAMQPCounter returns the current AMQP message count from the context
func GetAMQPCounter(ctx context.Context) int64 {
	if c := countersFromContext(ctx); c != nil {
		return c.amqpCount.Load()
	}
	return 0
}

// AddAMQPElapsed adds elapsed nanoseconds to the AMQP elapsed time in the context
func AddAMQPElapsed(ctx context.Context, nanos int64) {
	if c := countersFromContext(ctx); c != nil {
		c.amqpElapsed.Add(nanos)
	}
}

// GetAMQPElapsed returns the current AMQP elapsed time in nanoseconds from the context
func GetAMQPElapsed(ctx context.Context) int64 {
	if c := countersFromContext(ctx); c != nil {
		return c.amqpElapsed.Load()
	}
	return 0
}

// IncrementDBCounter increments the database operation counter in the context
func IncrementDBCounter(ctx context.Context) {
	if c := countersFromContext(ctx); c != nil {
		c.dbCount.Add(1)
	}
}

// GetDBCounter returns the current database operation count from the context
func GetDBCounter(ctx context.Context) int64 {
	if c := countersFromContext(ctx); c != nil {
		return c.dbCount.Load()
	}
	return 0
}

// AddDBElapsed adds elapsed nanoseconds to the database elapsed time in the context
func AddDBElapsed(ctx context.Context, nanos int64) {
	if c := countersFromContext(ctx); c != nil {
		c.dbElapsed.Add(nanos)
	}
}

// GetDBElapsed returns the current database elapsed time in nanoseconds from the context
func GetDBElapsed(ctx context.Context) int64 {
	if c := countersFromContext(ctx); c != nil {
		return c.dbElapsed.Load()
	}
	return 0
}
