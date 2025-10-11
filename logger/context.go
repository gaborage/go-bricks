package logger

import (
	"context"
	"sync/atomic"

	"github.com/rs/zerolog"
)

// contextKey is the type for context keys to avoid collisions
type contextKey string

const (
	// amqpCounterKey is the context key for tracking AMQP message count per request
	amqpCounterKey contextKey = "amqp_message_counter"
	// dbCounterKey is the context key for tracking database operation count per request
	dbCounterKey contextKey = "db_operation_counter"
	// amqpElapsedKey is the context key for tracking total AMQP elapsed time per request
	amqpElapsedKey contextKey = "amqp_elapsed_nanos"
	// dbElapsedKey is the context key for tracking total database elapsed time per request
	dbElapsedKey contextKey = "db_elapsed_nanos"
	// severityHookKey stores a callback for request-level severity tracking
	severityHookKey contextKey = "severity_hook"
)

// WithAMQPCounter creates a new context with an AMQP message counter and elapsed time tracker
func WithAMQPCounter(ctx context.Context) context.Context {
	counter := int64(0)
	elapsed := int64(0)
	ctx = context.WithValue(ctx, amqpCounterKey, &counter)
	ctx = context.WithValue(ctx, amqpElapsedKey, &elapsed)
	return ctx
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
	if counter, ok := ctx.Value(amqpCounterKey).(*int64); ok && counter != nil {
		atomic.AddInt64(counter, 1)
	}
}

// GetAMQPCounter returns the current AMQP message count from the context
func GetAMQPCounter(ctx context.Context) int64 {
	if counter, ok := ctx.Value(amqpCounterKey).(*int64); ok && counter != nil {
		return atomic.LoadInt64(counter)
	}
	return 0
}

// AddAMQPElapsed adds elapsed nanoseconds to the AMQP elapsed time in the context
func AddAMQPElapsed(ctx context.Context, nanos int64) {
	if elapsed, ok := ctx.Value(amqpElapsedKey).(*int64); ok && elapsed != nil {
		atomic.AddInt64(elapsed, nanos)
	}
}

// GetAMQPElapsed returns the current AMQP elapsed time in nanoseconds from the context
func GetAMQPElapsed(ctx context.Context) int64 {
	if elapsed, ok := ctx.Value(amqpElapsedKey).(*int64); ok && elapsed != nil {
		return atomic.LoadInt64(elapsed)
	}
	return 0
}

// WithDBCounter creates a new context with a database operation counter and elapsed time tracker
func WithDBCounter(ctx context.Context) context.Context {
	counter := int64(0)
	elapsed := int64(0)
	ctx = context.WithValue(ctx, dbCounterKey, &counter)
	ctx = context.WithValue(ctx, dbElapsedKey, &elapsed)
	return ctx
}

// IncrementDBCounter increments the database operation counter in the context
func IncrementDBCounter(ctx context.Context) {
	if counter, ok := ctx.Value(dbCounterKey).(*int64); ok && counter != nil {
		atomic.AddInt64(counter, 1)
	}
}

// GetDBCounter returns the current database operation count from the context
func GetDBCounter(ctx context.Context) int64 {
	if counter, ok := ctx.Value(dbCounterKey).(*int64); ok && counter != nil {
		return atomic.LoadInt64(counter)
	}
	return 0
}

// AddDBElapsed adds elapsed nanoseconds to the database elapsed time in the context
func AddDBElapsed(ctx context.Context, nanos int64) {
	if elapsed, ok := ctx.Value(dbElapsedKey).(*int64); ok && elapsed != nil {
		atomic.AddInt64(elapsed, nanos)
	}
}

// GetDBElapsed returns the current database elapsed time in nanoseconds from the context
func GetDBElapsed(ctx context.Context) int64 {
	if elapsed, ok := ctx.Value(dbElapsedKey).(*int64); ok && elapsed != nil {
		return atomic.LoadInt64(elapsed)
	}
	return 0
}
