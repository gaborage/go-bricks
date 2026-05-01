package server

import (
	"fmt"
	"maps"
	"net/http"
	"runtime"
	"sync/atomic"
)

// stackFrameDepth caps the number of stack frames captured per error.
// Deep enough to span typical handler → service → repository chains; shallow
// enough that capture stays in the low-microsecond range.
const stackFrameDepth = 32

// captureStackTraces is process-global because BaseAPIError construction
// sites are scattered throughout user code and have no access to *config.Config.
var captureStackTraces atomic.Bool

// stackTraceDetailKey is the JSON key under APIErrorResponse.Details where
// captured frames are exposed in development. Tests assert against this name,
// and downstream tooling (dev consoles, IDE plugins) can match on it.
const stackTraceDetailKey = "stackTrace"

// SetCaptureStackTraces toggles stack-trace capture for all subsequently
// created errors. Intended for the framework bootstrap (server.New) and tests.
// Safe for concurrent use.
func SetCaptureStackTraces(enabled bool) {
	captureStackTraces.Store(enabled)
}

// StackTracer is implemented by errors that carry a captured stack trace.
// The formatter uses this interface to surface frames in the response when
// running in development. *BaseAPIError satisfies it, and every wrapper type
// (NotFoundError, BadRequestError, …) inherits it via method promotion.
type StackTracer interface {
	StackTrace() []string
}

// BaseAPIError provides a basic implementation of IAPIError.
type BaseAPIError struct {
	code       string
	message    string
	httpStatus int
	details    map[string]any
	// stackPCs holds raw program counters captured at construction when
	// SetCaptureStackTraces(true) is in effect. Resolving them to file:line
	// strings is deferred to StackTrace() so production paths skip the
	// symbol-lookup cost entirely.
	stackPCs []uintptr
}

// NewBaseAPIError creates a new base API error.
func NewBaseAPIError(code, message string, httpStatus int) *BaseAPIError {
	e := &BaseAPIError{
		code:       code,
		message:    message,
		httpStatus: httpStatus,
		details:    make(map[string]any),
	}
	if captureStackTraces.Load() {
		e.stackPCs = captureStack(3) // skip runtime.Callers, captureStack, NewBaseAPIError
	}
	return e
}

// captureStack returns the call-site PCs above the caller. The skip value
// must account for runtime.Callers itself, captureStack, and the API
// constructor that invokes it; a wrong skip leaks framework internals into
// the user-visible top frame.
func captureStack(skip int) []uintptr {
	pcs := make([]uintptr, stackFrameDepth)
	n := runtime.Callers(skip, pcs)
	if n == 0 {
		return nil
	}
	return pcs[:n]
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

// StackTrace resolves the captured program counters into readable
// "file:line funcName" strings. Returns nil when no stack was captured —
// either because SetCaptureStackTraces was off at construction time, or
// because the error was built without NewBaseAPIError.
func (e *BaseAPIError) StackTrace() []string {
	if e == nil || len(e.stackPCs) == 0 {
		return nil
	}
	frames := runtime.CallersFrames(e.stackPCs)
	out := make([]string, 0, len(e.stackPCs))
	for {
		frame, more := frames.Next()
		out = append(out, fmt.Sprintf("%s:%d %s", frame.File, frame.Line, frame.Function))
		if !more {
			break
		}
	}
	return out
}

// NotFoundError represents resource not found errors.
type NotFoundError struct {
	*BaseAPIError
}

// Error implements the error interface.
func (e *NotFoundError) Error() string {
	return e.BaseAPIError.Error()
}

// NewNotFoundError creates a new not found error.
func NewNotFoundError(resource string) *NotFoundError {
	message := fmt.Sprintf("%s not found", resource)
	return &NotFoundError{
		BaseAPIError: NewBaseAPIError(errCodeNotFound, message, http.StatusNotFound),
	}
}

// ConflictError represents resource conflict errors.
type ConflictError struct {
	*BaseAPIError
}

// Error implements the error interface.
func (e *ConflictError) Error() string {
	return e.BaseAPIError.Error()
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

// Error implements the error interface.
func (e *UnauthorizedError) Error() string {
	return e.BaseAPIError.Error()
}

// NewUnauthorizedError creates a new unauthorized error.
func NewUnauthorizedError(message string) *UnauthorizedError {
	if message == "" {
		message = "Authentication required"
	}
	return &UnauthorizedError{
		BaseAPIError: NewBaseAPIError(errCodeUnauthorized, message, http.StatusUnauthorized),
	}
}

// ForbiddenError represents authorization errors.
type ForbiddenError struct {
	*BaseAPIError
}

// Error implements the error interface.
func (e *ForbiddenError) Error() string {
	return e.BaseAPIError.Error()
}

// NewForbiddenError creates a new forbidden error.
func NewForbiddenError(message string) *ForbiddenError {
	if message == "" {
		message = "Access denied"
	}
	return &ForbiddenError{
		BaseAPIError: NewBaseAPIError(errCodeForbidden, message, http.StatusForbidden),
	}
}

// InternalServerError represents internal server errors.
type InternalServerError struct {
	*BaseAPIError
}

// Error implements the error interface.
func (e *InternalServerError) Error() string {
	return e.BaseAPIError.Error()
}

// NewInternalServerError creates a new internal server error.
func NewInternalServerError(message string) *InternalServerError {
	if message == "" {
		message = "An internal error occurred"
	}
	return &InternalServerError{
		BaseAPIError: NewBaseAPIError(errCodeInternalError, message, http.StatusInternalServerError),
	}
}

// BadRequestError represents bad request errors.
type BadRequestError struct {
	*BaseAPIError
}

// Error implements the error interface.
func (e *BadRequestError) Error() string {
	return e.BaseAPIError.Error()
}

// NewBadRequestError creates a new bad request error.
func NewBadRequestError(message string) *BadRequestError {
	return &BadRequestError{
		BaseAPIError: NewBaseAPIError(errCodeBadRequest, message, http.StatusBadRequest),
	}
}

// ServiceUnavailableError represents service unavailable errors.
type ServiceUnavailableError struct {
	*BaseAPIError
}

// Error implements the error interface.
func (e *ServiceUnavailableError) Error() string {
	return e.BaseAPIError.Error()
}

// NewServiceUnavailableError creates a new service unavailable error.
func NewServiceUnavailableError(message string) *ServiceUnavailableError {
	if message == "" {
		message = "Service temporarily unavailable"
	}
	return &ServiceUnavailableError{
		BaseAPIError: NewBaseAPIError(errCodeServiceUnavailable, message, http.StatusServiceUnavailable),
	}
}

// TooManyRequestsError represents rate limiting errors.
type TooManyRequestsError struct {
	*BaseAPIError
}

// Error implements the error interface.
func (e *TooManyRequestsError) Error() string {
	return e.BaseAPIError.Error()
}

// NewTooManyRequestsError creates a new too many requests error.
func NewTooManyRequestsError(message string) *TooManyRequestsError {
	if message == "" {
		message = msgRateLimitExceeded
	}
	return &TooManyRequestsError{
		BaseAPIError: NewBaseAPIError(errCodeTooManyRequests, message, http.StatusTooManyRequests),
	}
}

// BusinessLogicError represents domain-specific business logic errors.
type BusinessLogicError struct {
	*BaseAPIError
}

// Error implements the error interface.
func (e *BusinessLogicError) Error() string {
	return e.BaseAPIError.Error()
}

// NewBusinessLogicError creates a new business logic error.
func NewBusinessLogicError(code, message string) *BusinessLogicError {
	return &BusinessLogicError{
		BaseAPIError: NewBaseAPIError(code, message, http.StatusUnprocessableEntity),
	}
}

// Compile-time interface assertions
var (
	_ IAPIError   = (*BaseAPIError)(nil)
	_ StackTracer = (*BaseAPIError)(nil)
)
