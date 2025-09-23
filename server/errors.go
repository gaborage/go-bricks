package server

import (
	"fmt"
	"maps"
	"net/http"
)

// BaseAPIError provides a basic implementation of IAPIError.
type BaseAPIError struct {
	code       string
	message    string
	httpStatus int
	details    map[string]any
}

// NewBaseAPIError creates a new base API error.
func NewBaseAPIError(code, message string, httpStatus int) *BaseAPIError {
	return &BaseAPIError{
		code:       code,
		message:    message,
		httpStatus: httpStatus,
		details:    make(map[string]any),
	}
}

// ErrorCode returns the error code.
func (e *BaseAPIError) ErrorCode() string {
	return e.code
}

// Message returns the error message.
func (e *BaseAPIError) Message() string {
	return e.message
}

// HTTPStatus returns the HTTP status code.
func (e *BaseAPIError) HTTPStatus() int {
	return e.httpStatus
}

// Details returns additional error details.
func (e *BaseAPIError) Details() map[string]any {
	if e.details == nil {
		return nil
	}
	cp := make(map[string]any, len(e.details))
	maps.Copy(cp, e.details)
	return cp
}

// WithDetails adds details to the error.
func (e *BaseAPIError) WithDetails(key string, value any) *BaseAPIError {
	e.details[key] = value
	return e
}

// Error implements the error interface for BaseAPIError.
// It returns a concise representation suitable for logs and debugging.
func (e *BaseAPIError) Error() string {
	if e == nil {
		return ""
	}
	if e.code == "" {
		return e.message
	}
	return e.code + ": " + e.message
}

// NotFoundError represents resource not found errors.
type NotFoundError struct {
	*BaseAPIError
}

// NewNotFoundError creates a new not found error.
func NewNotFoundError(resource string) *NotFoundError {
	message := fmt.Sprintf("%s not found", resource)
	return &NotFoundError{
		BaseAPIError: NewBaseAPIError("NOT_FOUND", message, http.StatusNotFound),
	}
}

// ConflictError represents resource conflict errors.
type ConflictError struct {
	*BaseAPIError
}

// NewConflictError creates a new conflict error.
func NewConflictError(message string) *ConflictError {
	return &ConflictError{
		BaseAPIError: NewBaseAPIError("CONFLICT", message, http.StatusConflict),
	}
}

// UnauthorizedError represents authentication errors.
type UnauthorizedError struct {
	*BaseAPIError
}

// NewUnauthorizedError creates a new unauthorized error.
func NewUnauthorizedError(message string) *UnauthorizedError {
	if message == "" {
		message = "Authentication required"
	}
	return &UnauthorizedError{
		BaseAPIError: NewBaseAPIError("UNAUTHORIZED", message, http.StatusUnauthorized),
	}
}

// ForbiddenError represents authorization errors.
type ForbiddenError struct {
	*BaseAPIError
}

// NewForbiddenError creates a new forbidden error.
func NewForbiddenError(message string) *ForbiddenError {
	if message == "" {
		message = "Access denied"
	}
	return &ForbiddenError{
		BaseAPIError: NewBaseAPIError("FORBIDDEN", message, http.StatusForbidden),
	}
}

// InternalServerError represents internal server errors.
type InternalServerError struct {
	*BaseAPIError
}

// NewInternalServerError creates a new internal server error.
func NewInternalServerError(message string) *InternalServerError {
	if message == "" {
		message = "An internal error occurred"
	}
	return &InternalServerError{
		BaseAPIError: NewBaseAPIError("INTERNAL_ERROR", message, http.StatusInternalServerError),
	}
}

// BadRequestError represents bad request errors.
type BadRequestError struct {
	*BaseAPIError
}

// NewBadRequestError creates a new bad request error.
func NewBadRequestError(message string) *BadRequestError {
	return &BadRequestError{
		BaseAPIError: NewBaseAPIError("BAD_REQUEST", message, http.StatusBadRequest),
	}
}

// ServiceUnavailableError represents service unavailable errors.
type ServiceUnavailableError struct {
	*BaseAPIError
}

// NewServiceUnavailableError creates a new service unavailable error.
func NewServiceUnavailableError(message string) *ServiceUnavailableError {
	if message == "" {
		message = "Service temporarily unavailable"
	}
	return &ServiceUnavailableError{
		BaseAPIError: NewBaseAPIError("SERVICE_UNAVAILABLE", message, http.StatusServiceUnavailable),
	}
}

// TooManyRequestsError represents rate limiting errors.
type TooManyRequestsError struct {
	*BaseAPIError
}

// NewTooManyRequestsError creates a new too many requests error.
func NewTooManyRequestsError(message string) *TooManyRequestsError {
	if message == "" {
		message = "Rate limit exceeded"
	}
	return &TooManyRequestsError{
		BaseAPIError: NewBaseAPIError("TOO_MANY_REQUESTS", message, http.StatusTooManyRequests),
	}
}

// BusinessLogicError represents domain-specific business logic errors.
type BusinessLogicError struct {
	*BaseAPIError
}

// NewBusinessLogicError creates a new business logic error.
func NewBusinessLogicError(code, message string) *BusinessLogicError {
	return &BusinessLogicError{
		BaseAPIError: NewBaseAPIError(code, message, http.StatusUnprocessableEntity),
	}
}

// Compile-time interface assertions
var _ IAPIError = (*BaseAPIError)(nil)
