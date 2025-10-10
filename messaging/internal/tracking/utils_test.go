package tracking

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatDestinationName(t *testing.T) {
	tests := []struct {
		name       string
		exchange   string
		routingKey string
		queue      string
		expected   string
	}{
		// Producer formats (no queue)
		{
			name:       "producer_with_exchange",
			exchange:   "user.events",
			routingKey: testRoutingKey,
			queue:      "",
			expected:   "user.events:test.key",
		},
		{
			name:       "producer_default_exchange",
			exchange:   "",
			routingKey: "user-queue",
			queue:      "",
			expected:   ":user-queue",
		},
		{
			name:       "producer_empty_routing_key",
			exchange:   "events",
			routingKey: "",
			queue:      "",
			expected:   "events:",
		},

		// Consumer formats (with queue)
		{
			name:       "consumer_with_exchange",
			exchange:   "user.events",
			routingKey: testRoutingKey,
			queue:      "processor",
			expected:   "user.events:test.key:processor",
		},
		{
			name:       "consumer_default_exchange",
			exchange:   "",
			routingKey: testRoutingKey,
			queue:      "processor",
			expected:   ":test.key:processor",
		},
		{
			name:       "consumer_empty_routing_key",
			exchange:   "events",
			routingKey: "",
			queue:      "processor",
			expected:   "events::processor",
		},
		{
			name:       "consumer_all_empty_except_queue",
			exchange:   "",
			routingKey: "",
			queue:      "my-queue",
			expected:   "::my-queue",
		},

		// Edge cases
		{
			name:       "all_empty_producer",
			exchange:   "",
			routingKey: "",
			queue:      "",
			expected:   ":",
		},
		{
			name:       "special_characters_in_names",
			exchange:   "event.exchange-v2",
			routingKey: "user_created.v1",
			queue:      "queue#1",
			expected:   "event.exchange-v2:user_created.v1:queue#1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDestinationName(tt.exchange, tt.routingKey, tt.queue)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractErrorType(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil_error",
			err:      nil,
			expected: "",
		},
		{
			name:     "context_canceled",
			err:      context.Canceled,
			expected: "context.Canceled",
		},
		{
			name:     "context_deadline_exceeded",
			err:      context.DeadlineExceeded,
			expected: "context.DeadlineExceeded",
		},
		{
			name:     "wrapped_context_canceled",
			err:      errors.Join(context.Canceled, errors.New("additional context")),
			expected: "context.Canceled",
		},
		{
			name:     "wrapped_context_deadline",
			err:      errors.Join(context.DeadlineExceeded, errors.New("timeout")),
			expected: "context.DeadlineExceeded",
		},
		{
			name:     "generic_error",
			err:      errors.New("generic error"),
			expected: "*errors.errorString",
		},
		{
			name:     "custom_error_type",
			err:      &customError{msg: "custom"},
			expected: "*tracking.customError",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractErrorType(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDurationToSeconds(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected float64
	}{
		{
			name:     "zero_duration",
			duration: 0,
			expected: 0.0,
		},
		{
			name:     "one_nanosecond",
			duration: 1 * time.Nanosecond,
			expected: 1e-9,
		},
		{
			name:     "one_microsecond",
			duration: 1 * time.Microsecond,
			expected: 1e-6,
		},
		{
			name:     "one_millisecond",
			duration: 1 * time.Millisecond,
			expected: 0.001,
		},
		{
			name:     "one_second",
			duration: 1 * time.Second,
			expected: 1.0,
		},
		{
			name:     "ten_seconds",
			duration: 10 * time.Second,
			expected: 10.0,
		},
		{
			name:     "one_minute",
			duration: 1 * time.Minute,
			expected: 60.0,
		},
		{
			name:     "fractional_seconds",
			duration: 1500 * time.Millisecond, // 1.5 seconds
			expected: 1.5,
		},
		{
			name:     "very_small_duration",
			duration: 100 * time.Nanosecond,
			expected: 1e-7,
		},
		{
			name:     "large_duration",
			duration: 1 * time.Hour,
			expected: 3600.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := durationToSeconds(tt.duration)
			assert.InDelta(t, tt.expected, result, 1e-10, "Duration conversion should be accurate")
		})
	}
}

// Custom error type for testing
type customError struct {
	msg string
}

func (e *customError) Error() string {
	return e.msg
}
