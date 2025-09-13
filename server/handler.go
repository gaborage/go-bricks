// Package server provides enhanced HTTP handler functionality with type-safe request/response handling.
package server

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/gaborage/go-bricks/config"
	gobrickshttp "github.com/gaborage/go-bricks/http"
)

// IAPIError defines the interface for API errors with structured information.
type IAPIError interface {
	ErrorCode() string
	Message() string
	HTTPStatus() int
	Details() map[string]interface{}
}

// APIResponse represents the standardized API response format.
type APIResponse struct {
	Data  interface{}            `json:"data,omitempty"`
	Error *APIErrorResponse      `json:"error,omitempty"`
	Meta  map[string]interface{} `json:"meta"`
}

// APIErrorResponse represents the error portion of an API response.
type APIErrorResponse struct {
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// HandlerFunc defines the new handler signature that focuses on business logic.
type HandlerFunc[T any, R any] func(request T, ctx HandlerContext) (R, IAPIError)

// HandlerContext provides access to Echo context and additional utilities when needed.
type HandlerContext struct {
	Echo   echo.Context
	Config *config.Config
}

// RequestBinder handles binding request data to structs with validation.
type RequestBinder struct{}

// NewRequestBinder creates a new request binder with the given validator.
func NewRequestBinder() *RequestBinder { return &RequestBinder{} }

// WrapHandler wraps a business logic handler into an Echo-compatible handler.
// It handles request binding, validation, response formatting, and error handling.
func WrapHandler[T any, R any](
	handlerFunc HandlerFunc[T, R],
	binder *RequestBinder,
	cfg *config.Config,
) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Create request instance
		var request T

		// Bind request data
		if err := binder.bindRequest(c, &request); err != nil {
			return formatErrorResponse(c, NewBadRequestError("Invalid request data").WithDetails("error", err.Error()), cfg)
		}

		// Validate request using Echo's configured validator
		if err := c.Validate(&request); err != nil {
			vErr := NewBadRequestError("Request validation failed")
			var ve *ValidationError
			if errors.As(err, &ve) {
				_ = vErr.WithDetails("validation_errors", ve.Errors)
			} else {
				_ = vErr.WithDetails("error", err.Error())
			}
			return formatErrorResponse(c, vErr, cfg)
		}

		// Create handler context
		handlerCtx := HandlerContext{
			Echo:   c,
			Config: cfg,
		}

		// Call the business logic handler
		response, apiErr := handlerFunc(request, handlerCtx)

		// Handle errors
		if apiErr != nil {
			return formatErrorResponse(c, apiErr, cfg)
		}

		// Allow handlers to control status and headers by returning a Result-like value
		if rl, ok := any(response).(ResultLike); ok {
			status, headers, data := rl.ResultMeta()
			return formatSuccessResponseWithStatus(c, data, status, headers)
		}

		// Default success response
		return formatSuccessResponse(c, response)
	}
}

// bindRequest binds request data from various sources to the target struct.
//
//nolint:gocyclo // Coordinating multiple binding sources; readability preferred.
func (rb *RequestBinder) bindRequest(c echo.Context, target interface{}) error {
	targetValue := reflect.ValueOf(target).Elem()
	targetType := targetValue.Type()

	// Bind JSON body if present (tolerate parameters like charset)
	if ct := c.Request().Header.Get("Content-Type"); ct != "" {
		if mt, _, _ := mime.ParseMediaType(ct); mt == "application/json" || strings.HasSuffix(mt, "+json") {
			if err := c.Bind(target); err != nil {
				return fmt.Errorf("failed to bind JSON body: %w", err)
			}
		}
	}

	// Bind path parameters, query parameters, and headers using struct tags
	for i := 0; i < targetType.NumField(); i++ {
		field := targetType.Field(i)
		fieldValue := targetValue.Field(i)

		if !fieldValue.CanSet() {
			continue
		}

		// Check for path parameter tag
		if paramName := field.Tag.Get("param"); paramName != "" {
			if value := c.Param(paramName); value != "" {
				if err := setFieldValue(fieldValue, value); err != nil {
					return fmt.Errorf("failed to set path param %s: %w", paramName, err)
				}
			}
		}

		// Check for query parameter tag
		if queryName := field.Tag.Get("query"); queryName != "" {
			// Support []string binding from repeated query parameters
			if fieldValue.Kind() == reflect.Slice && fieldValue.Type().Elem().Kind() == reflect.String {
				values := c.QueryParams()[queryName]
				if len(values) > 0 {
					slice := reflect.MakeSlice(fieldValue.Type(), len(values), len(values))
					for i, v := range values {
						slice.Index(i).SetString(v)
					}
					fieldValue.Set(slice)
				}
			} else {
				if value := c.QueryParam(queryName); value != "" {
					if err := setFieldValue(fieldValue, value); err != nil {
						return fmt.Errorf("failed to set query param %s: %w", queryName, err)
					}
				}
			}
		}

		// Check for header tag
		if headerName := field.Tag.Get("header"); headerName != "" {
			if values := c.Request().Header.Values(headerName); len(values) > 0 {
				// Support comma-separated list for []string headers
				if fieldValue.Kind() == reflect.Slice && fieldValue.Type().Elem().Kind() == reflect.String {
					slice := reflect.MakeSlice(fieldValue.Type(), 0, 8)
					for _, raw := range values {
						for _, p := range strings.Split(raw, ",") {
							p = strings.TrimSpace(p)
							if p != "" {
								slice = reflect.Append(slice, reflect.ValueOf(p))
							}
						}
					}
					fieldValue.Set(slice)
				} else {
					if err := setFieldValue(fieldValue, values[0]); err != nil {
						return fmt.Errorf("failed to set header %s: %w", headerName, err)
					}
				}
			}
		}
	}

	return nil
}

// setFieldValue sets a reflect.Value from a string value, handling type conversion.
//
//nolint:gocyclo,exhaustive // Multiple kinds handled explicitly; unsupported kinds return errors.
func setFieldValue(fieldValue reflect.Value, value string) error {
	// Handle pointers by allocating and setting the underlying value
	if fieldValue.Kind() == reflect.Ptr {
		if fieldValue.IsNil() {
			fieldValue.Set(reflect.New(fieldValue.Type().Elem()))
		}
		return setFieldValue(fieldValue.Elem(), value)
	}

	// Special type handling: time.Time
	if fieldValue.Type() == reflect.TypeOf(time.Time{}) {
		t, err := parseTime(value)
		if err != nil {
			return err
		}
		fieldValue.Set(reflect.ValueOf(t))
		return nil
	}

	switch fieldValue.Kind() {
	case reflect.String:
		fieldValue.SetString(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		intVal, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return err
		}
		fieldValue.SetInt(intVal)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		uintVal, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return err
		}
		fieldValue.SetUint(uintVal)
	case reflect.Float32, reflect.Float64:
		floatVal, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return err
		}
		fieldValue.SetFloat(floatVal)
	case reflect.Bool:
		boolVal, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		fieldValue.SetBool(boolVal)
	case reflect.Struct:
		// time.Time handled above; other structs unsupported
		return fmt.Errorf("unsupported struct type: %s", fieldValue.Type())
	case reflect.Slice:
		// Slice assignment from single string not supported here; handled by bindRequest for []string
		return fmt.Errorf("unsupported assignment to slice from string for kind: %s", fieldValue.Kind())
	case reflect.Invalid, reflect.Uintptr, reflect.Complex64, reflect.Complex128,
		reflect.Array, reflect.Chan, reflect.Func, reflect.Interface,
		reflect.Map, reflect.UnsafePointer:
		return fmt.Errorf("unsupported field type: %s", fieldValue.Kind())
	default:
		return fmt.Errorf("unsupported field type: %s", fieldValue.Kind())
	}
	return nil
}

func parseTime(s string) (time.Time, error) {
	// Try common layouts
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		time.DateTime, // "2006-01-02 15:04:05"
		"2006-01-02",
	}
	var lastErr error
	for _, layout := range layouts {
		t, err := time.Parse(layout, s)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("unable to parse time")
	}
	return time.Time{}, lastErr
}

// formatSuccessResponse formats a successful response with standardized structure.
func formatSuccessResponse(c echo.Context, data interface{}) error {
	ensureTraceParentHeader(c)
	response := APIResponse{
		Data: data,
		Meta: map[string]interface{}{
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"traceId":   getTraceID(c),
		},
	}

	return c.JSON(http.StatusOK, response)
}

// formatSuccessResponseWithStatus formats a successful response with a custom status and headers.
func formatSuccessResponseWithStatus(c echo.Context, data interface{}, status int, headers http.Header) error {
	if status == 0 {
		status = http.StatusOK
	}
	for k, vals := range headers {
		for _, v := range vals {
			c.Response().Header().Add(k, v)
		}
	}
	if status == http.StatusNoContent {
		return c.NoContent(http.StatusNoContent)
	}
	ensureTraceParentHeader(c)
	response := APIResponse{
		Data: data,
		Meta: map[string]interface{}{
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"traceId":   getTraceID(c),
		},
	}
	return c.JSON(status, response)
}

// formatErrorResponse formats an error response with standardized structure.
func formatErrorResponse(c echo.Context, apiErr IAPIError, cfg *config.Config) error {
	errorResp := &APIErrorResponse{
		Code:    apiErr.ErrorCode(),
		Message: apiErr.Message(),
	}

	// Include details only in development environment
	if cfg.App.Env == "development" || cfg.App.Env == "dev" {
		if details := apiErr.Details(); details != nil {
			errorResp.Details = details
		}
	}

	response := APIResponse{
		Error: errorResp,
		Meta: map[string]interface{}{
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"traceId":   getTraceID(c),
		},
	}

	ensureTraceParentHeader(c)
	return c.JSON(apiErr.HTTPStatus(), response)
}

// getTraceID extracts or generates a trace ID for the request.
func getTraceID(c echo.Context) string {
	// Prefer incoming request header set by upstream/proxy/middleware
	if requestID := c.Request().Header.Get(echo.HeaderXRequestID); requestID != "" {
		return requestID
	}
	// Then try the response header (may be set by request ID middleware)
	if requestID := c.Response().Header().Get(echo.HeaderXRequestID); requestID != "" {
		return requestID
	}
	// Generate a new UUID if none provided
	newID := uuid.New().String()
	// Set it so downstream might pick it up
	c.Response().Header().Set(echo.HeaderXRequestID, newID)
	return newID
}

// ensureTraceParentHeader ensures the response contains a W3C traceparent header.
// It propagates the inbound header when present, otherwise generates a new one.
func ensureTraceParentHeader(c echo.Context) {
	// If already set, do nothing
	if c.Response().Header().Get(gobrickshttp.HeaderTraceParent) != "" {
		return
	}
	// Prefer inbound header
	if tp := c.Request().Header.Get(gobrickshttp.HeaderTraceParent); tp != "" {
		c.Response().Header().Set(gobrickshttp.HeaderTraceParent, tp)
		return
	}
	// Generate new traceparent and set
	c.Response().Header().Set(gobrickshttp.HeaderTraceParent, gobrickshttp.GenerateTraceParent())
}

// HandlerRegistry manages enhanced handlers and provides registration utilities.
type HandlerRegistry struct {
	binder *RequestBinder
	cfg    *config.Config
}

// NewHandlerRegistry creates a new handler registry with the given validator and config.
func NewHandlerRegistry(cfg *config.Config) *HandlerRegistry {
	return &HandlerRegistry{
		binder: NewRequestBinder(),
		cfg:    cfg,
	}
}

// Register registers a typed handler with the Echo instance.
func RegisterHandler[T any, R any](
	hr *HandlerRegistry,
	e *echo.Echo,
	method, path string,
	handler HandlerFunc[T, R],
) {
	wrappedHandler := WrapHandler(handler, hr.binder, hr.cfg)
	e.Add(method, path, wrappedHandler)
}

// GET registers a GET handler.
func GET[T any, R any](hr *HandlerRegistry, e *echo.Echo, path string, handler HandlerFunc[T, R]) {
	RegisterHandler(hr, e, http.MethodGet, path, handler)
}

// POST registers a POST handler.
func POST[T any, R any](hr *HandlerRegistry, e *echo.Echo, path string, handler HandlerFunc[T, R]) {
	RegisterHandler(hr, e, http.MethodPost, path, handler)
}

// PUT registers a PUT handler.
func PUT[T any, R any](hr *HandlerRegistry, e *echo.Echo, path string, handler HandlerFunc[T, R]) {
	RegisterHandler(hr, e, http.MethodPut, path, handler)
}

// DELETE registers a DELETE handler.
func DELETE[T any, R any](hr *HandlerRegistry, e *echo.Echo, path string, handler HandlerFunc[T, R]) {
	RegisterHandler(hr, e, http.MethodDelete, path, handler)
}

// PATCH registers a PATCH handler.
func PATCH[T any, R any](hr *HandlerRegistry, e *echo.Echo, path string, handler HandlerFunc[T, R]) {
	RegisterHandler(hr, e, http.MethodPatch, path, handler)
}

// (legacy validation formatting helpers removed; validation now centralized via server/validator.go)

// ResultLike exposes status, headers, and payload for successful responses.
type ResultLike interface {
	ResultMeta() (status int, headers http.Header, data any)
}

// Result is a generic success wrapper allowing handlers to customize status and headers
// while preserving type safety of the response payload.
type Result[R any] struct {
	Data    R
	Status  int
	Headers http.Header
}

// ResultMeta implements ResultLike for Result[R].
func (r Result[R]) ResultMeta() (status int, headers http.Header, data any) {
	return r.Status, r.Headers, r.Data
}

// NewResult is a convenience constructor for Result.
func NewResult[R any](status int, data R) Result[R] {
	return Result[R]{
		Data:   data,
		Status: status,
	}
}

// NoContentResult represents a 204 No Content response without a body
type NoContentResult struct{}

// ResultMeta implements ResultLike for NoContentResult
func (NoContentResult) ResultMeta() (status int, headers http.Header, data any) {
	return http.StatusNoContent, nil, nil
}

// Created returns a 201 Created Result for the given data
func Created[R any](data R) Result[R] {
	return Result[R]{
		Data:   data,
		Status: http.StatusCreated,
	}
}

// Accepted returns a 202 Accepted Result for the given data
func Accepted[R any](data R) Result[R] {
	return Result[R]{
		Data:   data,
		Status: http.StatusAccepted,
	}
}

// NoContent returns a 204 No Content result without a response body
func NoContent() NoContentResult { return NoContentResult{} }
