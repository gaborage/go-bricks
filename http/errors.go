package http

import (
	"errors"
	"fmt"
	"time"
)

// ClientError represents different types of REST client errors
type ClientError interface {
	error
	Type() ErrorType
}

// ErrorType defines the category of client error
type ErrorType string

const (
	NetworkError     ErrorType = "network"
	TimeoutError     ErrorType = "timeout"
	HTTPError        ErrorType = "http"
	ValidationError  ErrorType = "validation"
	InterceptorError ErrorType = "interceptor"
)

// networkError represents network-related errors
type networkError struct {
	message string
	wrapped error
}

func (e *networkError) Error() string {
	if e.wrapped != nil {
		return fmt.Sprintf("network error: %s: %v", e.message, e.wrapped)
	}
	return fmt.Sprintf("network error: %s", e.message)
}

func (e *networkError) Type() ErrorType {
	return NetworkError
}

func (e *networkError) Unwrap() error {
	return e.wrapped
}

// timeoutError represents timeout-related errors
type timeoutError struct {
	message string
	timeout time.Duration
}

func (e *timeoutError) Error() string {
	return fmt.Sprintf("timeout error: %s (timeout: %v)", e.message, e.timeout)
}

func (e *timeoutError) Type() ErrorType {
	return TimeoutError
}

// httpError represents HTTP status-related errors
type httpError struct {
	message    string
	statusCode int
	body       []byte
}

func (e *httpError) Error() string {
	return fmt.Sprintf("HTTP error: %s (status: %d)", e.message, e.statusCode)
}

func (e *httpError) Type() ErrorType {
	return HTTPError
}

func (e *httpError) StatusCode() int {
	return e.statusCode
}

func (e *httpError) Body() []byte {
	return e.body
}

// validationError represents request validation errors
type validationError struct {
	message string
	field   string
}

func (e *validationError) Error() string {
	if e.field != "" {
		return fmt.Sprintf("validation error: %s (field: %s)", e.message, e.field)
	}
	return fmt.Sprintf("validation error: %s", e.message)
}

func (e *validationError) Type() ErrorType {
	return ValidationError
}

// interceptorError represents interceptor-related errors
type interceptorError struct {
	message string
	wrapped error
	stage   string
}

func (e *interceptorError) Error() string {
	return fmt.Sprintf("interceptor error: %s (stage: %s): %v", e.message, e.stage, e.wrapped)
}

func (e *interceptorError) Type() ErrorType {
	return InterceptorError
}

func (e *interceptorError) Unwrap() error {
	return e.wrapped
}

// NewNetworkError creates a new network error
func NewNetworkError(message string, wrapped error) ClientError {
	return &networkError{
		message: message,
		wrapped: wrapped,
	}
}

// NewTimeoutError creates a new timeout error
func NewTimeoutError(message string, timeout time.Duration) ClientError {
	return &timeoutError{
		message: message,
		timeout: timeout,
	}
}

// NewHTTPError creates a new HTTP error
func NewHTTPError(message string, statusCode int, body []byte) ClientError {
	return &httpError{
		message:    message,
		statusCode: statusCode,
		body:       body,
	}
}

// NewValidationError creates a new validation error
func NewValidationError(message, field string) ClientError {
	return &validationError{
		message: message,
		field:   field,
	}
}

// NewInterceptorError creates a new interceptor error
func NewInterceptorError(message, stage string, wrapped error) ClientError {
	return &interceptorError{
		message: message,
		wrapped: wrapped,
		stage:   stage,
	}
}

// IsErrorType checks if an error is of a specific type
func IsErrorType(err error, errorType ErrorType) bool {
	if err == nil {
		return false
	}
	var clientErr ClientError
	if errors.As(err, &clientErr) {
		return clientErr.Type() == errorType
	}
	return false
}

// IsHTTPStatusError checks if an error is an HTTP error with a specific status code
func IsHTTPStatusError(err error, statusCode int) bool {
	var httpErr *httpError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode() == statusCode
	}
	return false
}

// IsSuccessStatus checks if a status code represents success (2xx)
func IsSuccessStatus(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}
