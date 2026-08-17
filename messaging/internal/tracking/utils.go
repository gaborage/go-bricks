package tracking

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Canonical error.type attribute values for well-known context errors.
const (
	errTypeContextCanceled         = "context.Canceled"
	errTypeContextDeadlineExceeded = "context.DeadlineExceeded"
)

// formatDestinationName formats the destination name per OpenTelemetry RabbitMQ conventions.
//
// For producers (no queue):
//   - With exchange: "{exchange}:{routing_key}" (e.g., "user.events:user.created")
//   - Default exchange (empty): ":{routing_key}" (e.g., ":user-queue")
//
// For consumers (with queue):
//   - With exchange: "{exchange}:{routing_key}:{queue}" (e.g., "user.events:user.created:processor")
//   - Default exchange (empty): ":{routing_key}:{queue}" (e.g., ":user.created:processor")
//
// This format allows for hierarchical grouping in metrics queries while maintaining
// compatibility with OpenTelemetry semantic conventions for RabbitMQ.
func formatDestinationName(exchange, routingKey, queue string) string {
	if queue == "" {
		return exchange + ":" + routingKey
	}
	return exchange + ":" + routingKey + ":" + queue
}

// extractErrorType extracts the error type name for the error.type attribute.
// Returns an empty string for nil errors (success case).
//
// For well-known errors, returns the canonical error name.
// For other errors, returns the error type name (e.g., "*amqp.Error").
func extractErrorType(err error) string {
	if err == nil {
		return ""
	}

	// Handle well-known context errors
	switch {
	case errors.Is(err, context.Canceled):
		return errTypeContextCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return errTypeContextDeadlineExceeded
	default:
		return fmt.Sprintf("%T", err)
	}
}

// durationToSeconds converts time.Duration to seconds (float64) per OTel requirement.
// OpenTelemetry duration metrics must use seconds as the unit.
func durationToSeconds(d time.Duration) float64 {
	return float64(d.Nanoseconds()) / 1e9
}
