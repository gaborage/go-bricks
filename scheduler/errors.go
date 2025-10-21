package scheduler

import (
	"errors"
	"fmt"
)

// Sentinel errors for testability and error type checking
var (
	// ErrInvalidScheduleType indicates an unknown or unsupported schedule type
	ErrInvalidScheduleType = errors.New("scheduler: invalid schedule type")

	// ErrInvalidInterval indicates an invalid interval for fixed-rate schedules
	ErrInvalidInterval = errors.New("scheduler: invalid interval")

	// ErrInvalidTimeRange indicates a time value (hour, minute, day) is out of valid range
	ErrInvalidTimeRange = errors.New("scheduler: invalid time range")
)

// ValidationError represents a validation error during job registration or schedule validation.
// Per Constitution VII: Error messages follow format "scheduler: <field> <message>. <action>"
type ValidationError struct {
	Field   string // The field that failed validation (e.g., "hour", "minute", "interval")
	Message string // Description of what's wrong (e.g., "must be 0-23")
	Action  string // Optional: Actionable guidance (e.g., "Choose a value between 0 and 23")
	Err     error  // Optional: Wrapped sentinel error for errors.Is checking
}

func (e *ValidationError) Error() string {
	msg := fmt.Sprintf("scheduler: %s %s", e.Field, e.Message)
	if e.Action != "" {
		msg += ". " + e.Action
	}
	return msg
}

// Unwrap returns the wrapped error for errors.Is/As support
func (e *ValidationError) Unwrap() error {
	return e.Err
}

// NewValidationError creates a new ValidationError with the specified field, message, and action
func NewValidationError(field, message, action string) *ValidationError {
	return &ValidationError{
		Field:   field,
		Message: message,
		Action:  action,
	}
}

// NewRangeError creates a ValidationError for values outside valid ranges
func NewRangeError(field string, minVal, maxVal, actual any) *ValidationError {
	return &ValidationError{
		Field:   field,
		Message: fmt.Sprintf("must be %v-%v (got %v)", minVal, maxVal, actual),
		Action:  fmt.Sprintf("Choose a value between %v and %v", minVal, maxVal),
		Err:     ErrInvalidTimeRange,
	}
}

// NewInvalidValueError creates a ValidationError for invalid values
func NewInvalidValueError(field string, value any, expected string) *ValidationError {
	return &ValidationError{
		Field:   field,
		Message: fmt.Sprintf("invalid value %v", value),
		Action:  expected,
		Err:     ErrInvalidTimeRange,
	}
}
