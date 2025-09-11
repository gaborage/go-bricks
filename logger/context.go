package logger

import (
	"context"
	"sync/atomic"
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
)

// WithAMQPCounter creates a new context with an AMQP message counter and elapsed time tracker
func WithAMQPCounter(ctx context.Context) context.Context {
	counter := int64(0)
	elapsed := int64(0)
	ctx = context.WithValue(ctx, amqpCounterKey, &counter)
	ctx = context.WithValue(ctx, amqpElapsedKey, &elapsed)
	return ctx
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
