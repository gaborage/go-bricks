// Package server provides enhanced HTTP handler functionality with type-safe request/response handling.
package server

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"reflect"
	"runtime"
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
				_ = vErr.WithDetails("validationErrors", ve.Errors)
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

type valueSetter func(reflect.Value, string) error

var (
	timeType    = reflect.TypeOf(time.Time{})
	kindSetters = map[reflect.Kind]valueSetter{
		reflect.String:  setStringValue,
		reflect.Int:     setSignedIntValue,
		reflect.Int8:    setSignedIntValue,
		reflect.Int16:   setSignedIntValue,
		reflect.Int32:   setSignedIntValue,
		reflect.Int64:   setSignedIntValue,
		reflect.Uint:    setUnsignedIntValue,
		reflect.Uint8:   setUnsignedIntValue,
		reflect.Uint16:  setUnsignedIntValue,
		reflect.Uint32:  setUnsignedIntValue,
		reflect.Uint64:  setUnsignedIntValue,
		reflect.Float32: setFloatValue,
		reflect.Float64: setFloatValue,
		reflect.Bool:    setBoolValue,
	}
)

// setFieldValue sets a reflect.Value from a string value, handling type conversion.
func setFieldValue(fieldValue reflect.Value, value string) error {
	// Handle pointers by allocating and setting the underlying value
	if fieldValue.Kind() == reflect.Ptr {
		if fieldValue.IsNil() {
			fieldValue.Set(reflect.New(fieldValue.Type().Elem()))
		}
		return setFieldValue(fieldValue.Elem(), value)
	}

	// Special type handling: time.Time
	handled, err := setSpecialType(fieldValue, value)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}

	kind := fieldValue.Kind()
	if setter, ok := kindSetters[kind]; ok {
		return setter(fieldValue, value)
	}

	if kind == reflect.Struct {
		// time.Time handled above; other structs unsupported
		return fmt.Errorf("unsupported struct type: %s", fieldValue.Type())
	}

	if kind == reflect.Slice {
		// Slice assignment from single string not supported here; handled by bindRequest for []string
		return fmt.Errorf("unsupported assignment to slice from string for kind: %s", kind)
	}

	return fmt.Errorf("unsupported field type: %s", kind)
}

func setSpecialType(fieldValue reflect.Value, value string) (bool, error) {
	if fieldValue.Type() == timeType {
		t, err := parseTime(value)
		if err != nil {
			return true, err
		}
		fieldValue.Set(reflect.ValueOf(t))
		return true, nil
	}
	return false, nil
}

func setStringValue(fieldValue reflect.Value, value string) error {
	fieldValue.SetString(value)
	return nil
}

func setSignedIntValue(fieldValue reflect.Value, value string) error {
	bitSize := fieldValue.Type().Bits()
	if bitSize == 0 {
		bitSize = 64
	}
	intVal, err := strconv.ParseInt(value, 10, bitSize)
	if err != nil {
		return err
	}
	fieldValue.SetInt(intVal)
	return nil
}

func setUnsignedIntValue(fieldValue reflect.Value, value string) error {
	bitSize := fieldValue.Type().Bits()
	if bitSize == 0 {
		bitSize = 64
	}
	uintVal, err := strconv.ParseUint(value, 10, bitSize)
	if err != nil {
		return err
	}
	fieldValue.SetUint(uintVal)
	return nil
}

func setFloatValue(fieldValue reflect.Value, value string) error {
	floatVal, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return err
	}
	fieldValue.SetFloat(floatVal)
	return nil
}

func setBoolValue(fieldValue reflect.Value, value string) error {
	boolVal, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	fieldValue.SetBool(boolVal)
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
	if isDevelopmentEnv(cfg.App.Env) {
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

// RouteRegistrar abstracts the subset of Echo's routing features that modules need
// while allowing the server to enforce common behavior such as base-path handling.
// Implementations may wrap Echo groups to ensure routes are consistently registered.
type RouteRegistrar interface {
	Add(method, path string, handler echo.HandlerFunc, middleware ...echo.MiddlewareFunc) *echo.Route
	Group(prefix string, middleware ...echo.MiddlewareFunc) RouteRegistrar
	Use(middleware ...echo.MiddlewareFunc)
	FullPath(path string) string
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

// RegisterHandler registers a typed handler with the route registrar and captures metadata.
func RegisterHandler[T any, R any](
	hr *HandlerRegistry,
	r RouteRegistrar,
	method, path string,
	handler HandlerFunc[T, R],
	opts ...RouteOption,
) {
	// Extract type information
	var reqType T
	var respType R

	// Determine final path after registrar adjustments (e.g. base path prefixes)
	fullPath := r.FullPath(path)

	// Create descriptor with type information
	descriptor := RouteDescriptor{
		Method:       method,
		Path:         fullPath,
		HandlerID:    fmt.Sprintf("%s:%s", method, fullPath),
		RequestType:  reflect.TypeOf(reqType),
		ResponseType: reflect.TypeOf(respType),
		Package:      getCallerPackage(),
		HandlerName:  extractHandlerName(handler),
	}

	// Apply options
	for _, opt := range opts {
		opt(&descriptor)
	}

	// Register with global registry
	DefaultRouteRegistry.Register(&descriptor)

	// Register with route registrar (works with both Echo instances and Groups)
	wrappedHandler := WrapHandler(handler, hr.binder, hr.cfg)
	r.Add(method, path, wrappedHandler)
}

// GET registers a GET handler with optional route configuration.
func GET[T any, R any](hr *HandlerRegistry, r RouteRegistrar, path string, handler HandlerFunc[T, R], opts ...RouteOption) {
	RegisterHandler(hr, r, http.MethodGet, path, handler, opts...)
}

// POST registers a POST handler with optional route configuration.
func POST[T any, R any](hr *HandlerRegistry, r RouteRegistrar, path string, handler HandlerFunc[T, R], opts ...RouteOption) {
	RegisterHandler(hr, r, http.MethodPost, path, handler, opts...)
}

// PUT registers a PUT handler with optional route configuration.
func PUT[T any, R any](hr *HandlerRegistry, r RouteRegistrar, path string, handler HandlerFunc[T, R], opts ...RouteOption) {
	RegisterHandler(hr, r, http.MethodPut, path, handler, opts...)
}

// DELETE registers a DELETE handler with optional route configuration.
func DELETE[T any, R any](hr *HandlerRegistry, r RouteRegistrar, path string, handler HandlerFunc[T, R], opts ...RouteOption) {
	RegisterHandler(hr, r, http.MethodDelete, path, handler, opts...)
}

// PATCH registers a PATCH handler with optional route configuration.
func PATCH[T any, R any](hr *HandlerRegistry, r RouteRegistrar, path string, handler HandlerFunc[T, R], opts ...RouteOption) {
	RegisterHandler(hr, r, http.MethodPatch, path, handler, opts...)
}

// HEAD registers a HEAD handler with optional route configuration.
func HEAD[T any, R any](hr *HandlerRegistry, r RouteRegistrar, path string, handler HandlerFunc[T, R], opts ...RouteOption) {
	RegisterHandler(hr, r, http.MethodHead, path, handler, opts...)
}

// OPTIONS registers an OPTIONS handler with optional route configuration.
func OPTIONS[T any, R any](hr *HandlerRegistry, r RouteRegistrar, path string, handler HandlerFunc[T, R], opts ...RouteOption) {
	RegisterHandler(hr, r, http.MethodOptions, path, handler, opts...)
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

// getCallerPackage extracts the package path of the calling function
func getCallerPackage() string {
	pc, _, _, ok := runtime.Caller(3) // Skip this func + RegisterHandler + GET/POST/etc
	if !ok {
		return ""
	}

	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return ""
	}

	name := fn.Name()

	// Extract package path from function name
	// Function names are typically in the format: package/path.functionName
	lastSlash := strings.LastIndex(name, "/")
	if lastSlash >= 0 {
		// Find the next dot after the last slash to separate package from function
		remaining := name[lastSlash+1:]
		if dot := strings.Index(remaining, "."); dot >= 0 {
			return name[:lastSlash+1+dot]
		}
	}

	// Fallback: try to extract package from the beginning
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		packagePart := name[:dot]
		// Remove receiver type if present (e.g., package.(*Type).method -> package)
		if parenIndex := strings.LastIndex(packagePart, "("); parenIndex >= 0 {
			if dotIndex := strings.LastIndex(packagePart[:parenIndex], "."); dotIndex >= 0 {
				return packagePart[:dotIndex]
			}
		}
		return packagePart
	}

	return ""
}

// extractHandlerName gets the function name from a handler function
func extractHandlerName(handler interface{}) string {
	if handler == nil {
		return ""
	}

	v := reflect.ValueOf(handler)
	if v.Kind() != reflect.Func {
		return ""
	}

	name := runtime.FuncForPC(v.Pointer()).Name()

	// Extract just the function name from the full path
	// e.g., "github.com/example/module.(*Module).getUser" -> "getUser"
	if lastDot := strings.LastIndex(name, "."); lastDot >= 0 {
		return name[lastDot+1:]
	}

	return name
}
