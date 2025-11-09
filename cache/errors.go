package cache

import (
	"errors"
	"fmt"
)

// Sentinel errors for common cache operations.
// Use errors.Is() to check for these specific error conditions.
var (
	// ErrNotFound is returned when a cache key doesn't exist or has expired.
	// This is not considered a fatal error - callers should handle cache misses gracefully.
	ErrNotFound = errors.New("cache: key not found")

	// ErrCASFailed is returned when a CompareAndSet operation fails because
	// the current value doesn't match the expected value.
	// This indicates a concurrent modification or lock contention.
	ErrCASFailed = errors.New("cache: compare-and-set failed")

	// ErrClosed is returned when attempting to use a closed cache connection.
	ErrClosed = errors.New("cache: connection closed")

	// ErrInvalidTTL is returned when a TTL value is invalid (e.g., negative).
	ErrInvalidTTL = errors.New("cache: invalid TTL")
)

// ConfigError represents a configuration error during cache initialization.
// These errors are fail-fast and should panic at application startup.
type ConfigError struct {
	Field   string // Configuration field that failed validation
	Message string // Human-readable error message
	Err     error  // Underlying error, if any
}

// Error implements the error interface.
func (e *ConfigError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("cache configuration error: %s: %s: %v", e.Field, e.Message, e.Err)
	}
	return fmt.Sprintf("cache configuration error: %s: %s", e.Field, e.Message)
}

// Unwrap returns the underlying error for errors.Is/As support.
func (e *ConfigError) Unwrap() error {
	return e.Err
}

// NewConfigError creates a new configuration error.
func NewConfigError(field, message string, err error) *ConfigError {
	return &ConfigError{
		Field:   field,
		Message: message,
		Err:     err,
	}
}

// ConnectionError represents a cache connection error.
// These errors may be transient and could be retried.
type ConnectionError struct {
	Op      string // Operation that failed (e.g., "dial", "ping")
	Address string // Cache server address
	Err     error  // Underlying error
}

// Error implements the error interface.
func (e *ConnectionError) Error() string {
	return fmt.Sprintf("cache connection error: %s failed for %s: %v", e.Op, e.Address, e.Err)
}

// Unwrap returns the underlying error for errors.Is/As support.
func (e *ConnectionError) Unwrap() error {
	return e.Err
}

// NewConnectionError creates a new connection error.
func NewConnectionError(op, address string, err error) *ConnectionError {
	return &ConnectionError{
		Op:      op,
		Address: address,
		Err:     err,
	}
}

// OperationError represents a cache operation error.
// These errors occur during cache operations (Get, Set, Delete, etc.).
type OperationError struct {
	Op  string // Operation that failed (e.g., "get", "set", "cas")
	Key string // Cache key involved in the operation
	Err error  // Underlying error
}

// Error implements the error interface.
func (e *OperationError) Error() string {
	return fmt.Sprintf("cache operation error: %s failed for key %q: %v", e.Op, e.Key, e.Err)
}

// Unwrap returns the underlying error for errors.Is/As support.
func (e *OperationError) Unwrap() error {
	return e.Err
}

// NewOperationError creates a new operation error.
func NewOperationError(op, key string, err error) *OperationError {
	return &OperationError{
		Op:  op,
		Key: key,
		Err: err,
	}
}
