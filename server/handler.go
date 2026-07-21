// Package server provides enhanced HTTP handler functionality with type-safe request/response handling.
package server

import (
	"context"
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
	"github.com/labstack/echo/v5"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/config"
	gobrickshttp "github.com/gaborage/go-bricks/httpclient"
	"github.com/gaborage/go-bricks/jose"
	"github.com/gaborage/go-bricks/logger"
)

// IAPIError defines the interface for API errors with structured information.
type IAPIError interface {
	ErrorCode() string
	Message() string
	HTTPStatus() int
	Details() map[string]any
}

// APIResponse represents the standardized API response format.
type APIResponse struct {
	Data  any               `json:"data,omitempty"`
	Error *APIErrorResponse `json:"error,omitempty"`
	Meta  map[string]any    `json:"meta"`
}

// frameworkMeta is the typed form of the default envelope meta. Field order
// (Timestamp, TraceID) reproduces encoding/json's sorted map-key order
// ("timestamp" < "traceId") so output is byte-identical to the map form.
//
// The json tags MUST stay equal to the fieldTimestamp/fieldTraceID constants
// (server/constants.go); tags must be string literals, so they are duplicated
// here rather than referenced.
type frameworkMeta struct {
	Timestamp string `json:"timestamp"`
	TraceID   string `json:"traceId"`
}

// frameworkEnvelope is the internal, unexported wire DTO for the default
// success/error encode paths. It mirrors APIResponse's field ORDER but carries
// the typed frameworkMeta (zero map allocation, zero per-key any-boxing). The
// JSON shape is byte-identical to APIResponse with a map[string]any meta: the
// omitempty Data/Error are dropped when zero, field order is data,error,meta,
// and meta key order is timestamp,traceId.
type frameworkEnvelope struct {
	Data  any               `json:"data,omitempty"`
	Error *APIErrorResponse `json:"error,omitempty"`
	Meta  frameworkMeta     `json:"meta"`
}

// APIErrorResponse represents the error portion of an API response.
type APIErrorResponse struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// HandlerFunc defines the new handler signature that focuses on business logic.
type HandlerFunc[T any, R any] func(request T, ctx HandlerContext) (R, IAPIError)

// MiddlewareFunc is the framework-neutral middleware signature. A middleware runs
// its own logic, then calls next() to invoke the rest of the chain — or returns an
// IAPIError WITHOUT calling next to abort the request (the returned error flows through
// the standard error handler, producing the usual envelope). It is a flat baton-pass
// rather than Echo's nested func(next) next form; the framework adapts it to Echo
// internally, so application code never names an echo type.
type MiddlewareFunc func(c HandlerContext, next func() error) error

// Handler is the untyped, echo-free handler signature for raw routes, the readiness
// probe, and debug/system endpoints. It writes its own response (via c.JSON / c.String)
// and is the low-level counterpart to the typed HandlerFunc[T, R].
//
// Handler is NOT related to Raw Response Mode (WithRawResponse / RouteDescriptor.RawResponse,
// which bypasses the APIResponse envelope for Strangler-Fig routes): the two concepts share
// no machinery. Handler simply means "an echo-free handler the framework adapts to Echo."
type Handler func(c HandlerContext) error

// HandlerContext gives handlers and middleware framework-neutral access to the request,
// response, and per-request state. Standard-library types (*http.Request,
// http.ResponseWriter) are the currency at this boundary; the underlying Echo engine is
// reachable only through the unexported escape hatch, so application code cannot name an
// echo type.
type HandlerContext struct {
	Config *config.Config
	ectx   *echo.Context // unexported escape hatch; application code cannot name it
}

// newHandlerContext wraps an Echo context as a HandlerContext. Framework-internal.
func newHandlerContext(c *echo.Context, cfg *config.Config) HandlerContext {
	return HandlerContext{Config: cfg, ectx: c}
}

// TestContextOption customizes a HandlerContext built by NewHandlerContextForTest. It
// seeds pre-routing state the engine would otherwise populate during matching, so unit
// tests can exercise code that reads that state without standing up a router. Test-support
// only: options are applied at test construction, never on a live request context.
type TestContextOption func(*HandlerContext)

// WithRouteTemplate stamps the matched route template so RouteTemplate() reports it on an
// otherwise-unrouted test context (which never routes and would report ""). On a live
// request the router owns this value; the option is reachable only through the test
// constructor, so it cannot mutate a routed context's identity. See issue #639.
func WithRouteTemplate(template string) TestContextOption {
	return func(c *HandlerContext) { c.ectx.SetPath(template) }
}

// NewHandlerContextForTest builds a HandlerContext backed by a real Echo context for use
// in external-package tests (e.g. app/, scheduler/) that exercise Handler / MiddlewareFunc
// code but cannot name the unexported escape hatch. It keeps the echo dependency confined
// to package server. Test-support only — not for production wiring. To seed routing state
// the synthetic context would otherwise leave empty, use NewHandlerContextForTestWithOptions.
func NewHandlerContextForTest(w http.ResponseWriter, r *http.Request, cfg *config.Config) HandlerContext {
	return NewHandlerContextForTestWithOptions(w, r, cfg)
}

// NewHandlerContextForTestWithOptions is NewHandlerContextForTest plus TestContextOption
// values (e.g. WithRouteTemplate) that seed pre-routing state the engine would otherwise
// populate during matching. It exists as a separate constructor rather than a variadic on
// NewHandlerContextForTest so the already-released signature stays API-compatible (adding a
// variadic changes a function's type identity — apidiff classifies it as incompatible).
// Test-support only — not for production wiring.
func NewHandlerContextForTestWithOptions(w http.ResponseWriter, r *http.Request, cfg *config.Config, opts ...TestContextOption) HandlerContext {
	e := echo.New()
	// Register the framework validator so contexts built here drive the typed pipeline
	// (which calls c.Validate) exactly as a live request would.
	if v := NewValidator(); v != nil {
		e.Validator = v
	}
	c := newHandlerContext(e.NewContext(r, w), cfg)
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// Request returns the underlying *http.Request.
func (c HandlerContext) Request() *http.Request { return c.ectx.Request() }

// RequestContext returns the request's context.Context. Convenience for the dominant
// pattern (deadline/cancellation, trace and tenant propagation) so callers rarely need
// Request() directly.
func (c HandlerContext) RequestContext() context.Context { return c.ectx.Request().Context() }

// ResponseWriter returns the http.ResponseWriter for the current response.
func (c HandlerContext) ResponseWriter() http.ResponseWriter { return c.ectx.Response() }

// Param returns the path parameter for name (empty string if absent).
func (c HandlerContext) Param(name string) string { return c.ectx.Param(name) }

// Query returns the first query-string value for name (empty string if absent).
func (c HandlerContext) Query(name string) string { return c.ectx.QueryParam(name) }

// RequestHeader returns the first request header value for name (empty string if absent).
func (c HandlerContext) RequestHeader(name string) string { return c.ectx.Request().Header.Get(name) }

// Get returns a per-request value previously stored with Set (nil if absent).
func (c HandlerContext) Get(key string) any { return c.ectx.Get(key) }

// Set stores a per-request value retrievable with Get. Note: this is the request-scoped
// store, NOT the request's context.Context — to propagate values to downstream handlers
// and tenant-aware resources (deps.DB(ctx) etc.), use SetRequestContext.
func (c HandlerContext) Set(key string, val any) { c.ectx.Set(key, val) }

// SetRequest replaces the underlying *http.Request (e.g. to attach a derived context).
func (c HandlerContext) SetRequest(r *http.Request) { c.ectx.SetRequest(r) }

// SetRequestContext replaces the request's context.Context, the canonical way for
// middleware to inject values (tenant ID, authenticated principal) that downstream
// handlers and context-aware resources observe. Equivalent to
// SetRequest(Request().WithContext(ctx)).
func (c HandlerContext) SetRequestContext(ctx context.Context) {
	c.ectx.SetRequest(c.ectx.Request().WithContext(ctx))
}

// JSON writes v as a JSON response with the given status code. For raw/system handlers;
// typed handlers return (R, IAPIError) and let the framework encode.
func (c HandlerContext) JSON(code int, v any) error { return c.ectx.JSON(code, v) }

// String writes s as a text/plain response with the given status code.
func (c HandlerContext) String(code int, s string) error { return c.ectx.String(code, s) }

// PathParam is one matched path parameter. Ordered slices preserve
// route-template order.
type PathParam struct {
	Name  string
	Value string
}

// RouteTemplate returns the registered route path that matched this request,
// including any group/base-path prefix (e.g. "/api/cards/:cardId/status").
// It is the template the application registered, NOT the concrete URL
// (use Request().URL.Path for that). Empty before routing completes and on
// unmatched (404) requests; on a top-level 405 the engine sets the best-matching
// route's template (engine-defined, not a contract). Note the asymmetry under a
// middleware-bearing group: a wrong-method or unmatched sub-path there resolves
// to the group's implicit catch-all (a 404), so it returns "" rather than a
// best-match template.
func (c HandlerContext) RouteTemplate() string {
	if c.ectx.RouteInfo().Method == echo.RouteNotFound {
		return "" // group implicit catch-all (echo v5.3.0): unmatched, no template
	}
	return c.ectx.Path()
}

// isUnmatchedRoute reports whether the request did not resolve to a real
// application route: the global 404/405 sentinel fallbacks OR a group's
// implicit catch-all. echo v5.3.0 restored v4 per-group auto-404 routes for
// middleware-bearing groups; their RouteInfo has an empty Name but
// Method == echo.RouteNotFound, which the old Name-only check missed.
func (c HandlerContext) isUnmatchedRoute() bool {
	ri := c.ectx.RouteInfo()
	return ri.Name == echo.NotFoundRouteName ||
		ri.Name == echo.MethodNotAllowedRouteName ||
		ri.Method == echo.RouteNotFound
}

// PathParams returns the matched path parameters in route-template order.
// The returned slice is a defensive copy: safe to retain past the request;
// mutating it does not affect Param() or struct-tag binding. Empty when no
// route matched (pre-route, 404, 405) — parameter state is only meaningful
// for a matched route.
func (c HandlerContext) PathParams() []PathParam {
	// Param names are stamped only on a real method+path match. On an unmatched
	// request — the global 404/405 fallback, or a middleware-bearing group's
	// implicit catch-all — echo may still leave values in the pooled slots: stale
	// names from a prior pooled request, or the catch-all's own synthetic wildcard
	// ("*") capture. Neither is a real application parameter, so treat unmatched
	// requests as having none.
	if c.isUnmatchedRoute() {
		return []PathParam{}
	}
	// echo's PathValues() returns a slice header ALIASING the pooled context's
	// backing array (reused across requests) — element-wise copy is mandatory.
	values := c.ectx.PathValues()
	params := make([]PathParam, len(values))
	for i, v := range values {
		params[i] = PathParam{Name: v.Name, Value: v.Value}
	}
	return params
}

// SetPathParams replaces the request's path parameters. Subsequent Param(name)
// calls and param:"name" struct-tag binding observe the new set. The input
// slice is copied; nil clears all parameters. Injected params are
// application-supplied — treat them with the same trust as their source value.
func (c HandlerContext) SetPathParams(params []PathParam) {
	// echo's SetPathValues panics on nil, so always hand it a non-nil slice
	// (len 0 clears). Echo copies the input into its pooled array, so the
	// temporary is safe.
	values := make(echo.PathValues, len(params))
	for i, p := range params {
		values[i] = echo.PathValue{Name: p.Name, Value: p.Value}
	}
	c.ectx.SetPathValues(values)
}

// echoContext returns the underlying Echo context. UNEXPORTED escape hatch for
// package-server framework code only (adapters, test helpers); greppable and unnameable
// from other packages.
func (c HandlerContext) echoContext() *echo.Context { return c.ectx }

// adaptHandler converts a go-bricks Handler into an echo.HandlerFunc for registration.
func adaptHandler(h Handler, cfg *config.Config) echo.HandlerFunc {
	return func(c *echo.Context) error { return h(newHandlerContext(c, cfg)) }
}

// adaptMiddleware converts a flat MiddlewareFunc into an echo.MiddlewareFunc. The
// per-request baton closure (func() error { return next(c) }) is the one allocation this
// adapter adds; it lands only on middleware-bearing routes, never the framework's typed
// default path (which registers echo handlers directly via the addEcho seam).
func adaptMiddleware(m MiddlewareFunc, cfg *config.Config) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			return m(newHandlerContext(c, cfg), func() error { return next(c) })
		}
	}
}

// fromEchoMiddleware exposes an echo-native middleware as a flat MiddlewareFunc so the
// framework's own middleware constructors keep an echo-free public signature while their
// logic stays echo-native (and baton-free) on the default path inside SetupMiddlewares.
func fromEchoMiddleware(em echo.MiddlewareFunc) MiddlewareFunc {
	return func(c HandlerContext, next func() error) error {
		h := em(func(*echo.Context) error { return next() })
		return h(c.echoContext())
	}
}

// RequestBinder handles binding request data to structs with validation.
type RequestBinder struct{}

// NewRequestBinder creates a new request binder with the given validator.
func NewRequestBinder() *RequestBinder { return &RequestBinder{} }

// contextChecker handles context cancellation detection at various stages of request processing.
type contextChecker struct {
	cfg *config.Config
}

// newContextChecker creates a new context checker.
func newContextChecker(cfg *config.Config) *contextChecker {
	return &contextChecker{cfg: cfg}
}

// checkCancellation checks if the request context has been canceled or timed out.
// Returns an API error if canceled, nil otherwise.
func (cc *contextChecker) checkCancellation(c *echo.Context, stage string) IAPIError {
	if c.Request().Context().Err() != nil {
		msg := fmt.Sprintf("Request timeout %s", stage)
		return NewServiceUnavailableError(msg)
	}
	return nil
}

// requestAllocator handles type detection, memory allocation, and nil validation for request types.
// It uses reflection once during initialization to determine if T is a pointer type.
type requestAllocator[T any] struct {
	isPointer bool
	elemType  reflect.Type
}

// newRequestAllocator creates a new request allocator for type T.
func newRequestAllocator[T any]() *requestAllocator[T] {
	rt := reflect.TypeOf((*T)(nil)).Elem()
	return &requestAllocator[T]{
		isPointer: rt.Kind() == reflect.Pointer,
		elemType:  rt,
	}
}

// allocate creates a new instance of type T and returns both the typed value and a pointer for binding.
// For pointer types (T = *Request), allocates the underlying type and returns the pointer.
// For value types (T = Request), returns the zero value and a pointer to it.
func (ra *requestAllocator[T]) allocate() (request T, requestPtr any) {
	if ra.isPointer {
		// T is a pointer type (e.g., *Request)
		elem := reflect.New(ra.elemType.Elem())
		requestPtr = elem.Interface()
		request = elem.Interface().(T)
	} else {
		// T is a value type (e.g., Request)
		requestPtr = &request
	}
	return request, requestPtr
}

// validateNotNil validates that pointer-type requests are not nil.
// Returns an error for nil pointers, nil for value types or non-nil pointers.
func (ra *requestAllocator[T]) validateNotNil(request T) error {
	if ra.isPointer && reflect.ValueOf(request).IsNil() {
		return fmt.Errorf("request cannot be nil")
	}
	return nil
}

// responseHandler handles response formatting for both success and error cases.
type responseHandler struct {
	cfg *config.Config
	log logger.Logger // nilable; used only to warn on envelope-meta reserved-key collisions
}

// newResponseHandler creates a new response handler.
func newResponseHandler(cfg *config.Config, log logger.Logger) *responseHandler {
	return &responseHandler{cfg: cfg, log: log}
}

// handleResponse formats and sends the HTTP response based on the handler result.
// Dispatch precedence:
//  1. API error → error envelope
//  2. ResultEnvelopeProvider → success envelope with merged meta (handler meta ∪ framework meta)
//  3. ResultMetaProvider → success envelope with framework meta only
//  4. Default → 200 OK with framework meta only
func (rh *responseHandler) handleResponse(c *echo.Context, response any, apiErr IAPIError) error {
	if apiErr != nil {
		return formatErrorResponse(c, apiErr, rh.cfg)
	}

	if re, ok := response.(ResultEnvelopeProvider); ok {
		status, headers, data, meta := re.ResultEnvelope()
		return formatSuccessEnvelopeWithStatus(c, data, status, headers, meta, rh.log)
	}

	if rl, ok := response.(ResultMetaProvider); ok {
		status, headers, data := rl.ResultMeta()
		return formatSuccessResponseWithStatus(c, data, status, headers)
	}

	return formatSuccessResponse(c, response)
}

// bindSource identifies which tag-driven source a boundField reads from.
type bindSource int

const (
	bindSourceParam bindSource = iota
	bindSourceQuery
	bindSourceHeader
)

// boundField is one precomputed tag-binding instruction for a request struct field.
// Built once per route (buildBindingPlan); replayed per request without re-parsing
// struct tags. isSlice is only meaningful for query/header sources and mirrors
// isStringSliceField, computed here from the static field type instead of a runtime
// reflect.Value.
type boundField struct {
	index   int
	source  bindSource
	name    string
	isSlice bool
}

// buildBindingPlan walks a request struct type once at route-registration time and
// captures, per exported field, which of the param/query/header tags are present and
// what each needs at bind time. Fields are visited in declaration order and, within a
// field, instructions are appended param, then query, then header — matching
// bindFieldFromTags's precedence so replay order is unchanged. Unexported fields are
// skipped (equivalent to today's per-request CanSet skip). Anonymous/embedded fields
// are treated as a single field, same as today — no recursion.
//
// Non-struct t is a no-op (nil plan): NumField() on a non-struct type panics (known
// gap, F26), and that panic must stay at request time, not move here to registration
// time. See bindRequest's isStruct-gated fallback in requestProcessor.process.
func buildBindingPlan(t reflect.Type) []boundField {
	if t.Kind() != reflect.Struct {
		return nil
	}

	var plan []boundField
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		if paramName := field.Tag.Get("param"); paramName != "" {
			plan = append(plan, boundField{index: i, source: bindSourceParam, name: paramName})
		}
		if queryName := field.Tag.Get("query"); queryName != "" {
			plan = append(plan, boundField{
				index: i, source: bindSourceQuery, name: queryName, isSlice: isStringSliceType(field.Type),
			})
		}
		if headerName := field.Tag.Get("header"); headerName != "" {
			plan = append(plan, boundField{
				index: i, source: bindSourceHeader, name: headerName, isSlice: isStringSliceType(field.Type),
			})
		}
	}
	return plan
}

// requestProcessor orchestrates the complete request processing pipeline:
// allocation, binding, nil validation, and request validation.
type requestProcessor[T any] struct {
	allocator *requestAllocator[T]
	binder    *RequestBinder
	cfg       *config.Config
	plan      []boundField
	isStruct  bool
}

// newRequestProcessor creates a new request processor for type T. The tag-binding plan
// is computed once here (not per request) from T's underlying struct type, reusing the
// allocator's elemType/pointer-unwrap logic so pointer-typed T gets the struct's plan.
func newRequestProcessor[T any](binder *RequestBinder, cfg *config.Config) *requestProcessor[T] {
	allocator := newRequestAllocator[T]()

	structType := allocator.elemType
	if structType.Kind() == reflect.Pointer {
		structType = structType.Elem()
	}
	isStruct := structType.Kind() == reflect.Struct

	var plan []boundField
	if isStruct {
		plan = buildBindingPlan(structType)
	}

	return &requestProcessor[T]{
		allocator: allocator,
		binder:    binder,
		cfg:       cfg,
		plan:      plan,
		isStruct:  isStruct,
	}
}

// process executes the full request processing pipeline and returns the bound, validated request.
// Returns an API error if any step fails (allocation, binding, nil check, validation).
func (rp *requestProcessor[T]) process(c *echo.Context) (T, IAPIError) {
	var empty T

	// Allocate request instance
	request, requestPtr := rp.allocator.allocate()

	// Bind request data from multiple sources (JSON, query, params, headers).
	// Struct T takes the precomputed-plan fast path; non-struct T (F26, untested
	// backlog gap) falls back to the legacy reflect-per-request path so its
	// request-time NumField() panic stays exactly where it is today.
	var bindErr error
	if rp.isStruct {
		bindErr = rp.binder.bindRequestPlanned(c, requestPtr, rp.plan)
	} else {
		bindErr = rp.binder.bindRequest(c, requestPtr)
	}
	if bindErr != nil {
		return empty, NewBadRequestError("Invalid request data").WithDetails("error", bindErr.Error())
	}

	// For value types, we need to get the bound value back from the pointer
	if !rp.allocator.isPointer {
		request = reflect.ValueOf(requestPtr).Elem().Interface().(T)
	}

	// Validate not nil (for pointer types)
	if err := rp.allocator.validateNotNil(request); err != nil {
		return empty, NewBadRequestError(err.Error())
	}

	// Validate request using Echo's configured validator
	if err := c.Validate(requestPtr); err != nil {
		vErr := NewBadRequestError("Request validation failed")
		var ve *ValidationError
		if errors.As(err, &ve) {
			_ = vErr.WithDetails("validationErrors", ve.Errors)
		} else {
			_ = vErr.WithDetails("error", err.Error())
		}
		return empty, vErr
	}

	return request, nil
}

// rawResponseContextKey is used to signal raw response mode to the global error handler.
// It is set on the Echo context before any processing so that even panics that escape
// to customErrorHandler can detect it.
const rawResponseContextKey = "_raw_response"

// joseRouteConfig bundles the per-route JOSE state. Threaded as a single nil-safe pointer
// through the wrapper so non-JOSE routes pay zero state cost and the constructor signature
// stays compact.
type joseRouteConfig struct {
	Inbound  *jose.Policy
	Outbound *jose.Policy
	Resolver jose.KeyResolver
	Obs      *joseObservability
}

func (j *joseRouteConfig) inbound() *jose.Policy {
	if j == nil {
		return nil
	}
	return j.Inbound
}
func (j *joseRouteConfig) outbound() *jose.Policy {
	if j == nil {
		return nil
	}
	return j.Outbound
}
func (j *joseRouteConfig) resolver() jose.KeyResolver {
	if j == nil {
		return nil
	}
	return j.Resolver
}
func (j *joseRouteConfig) obs() *joseObservability {
	if j == nil {
		return nil
	}
	return j.Obs
}

// handlerWrapper composes all request processing components to create an Echo-compatible handler.
// It orchestrates: context checking, request processing, business logic execution, and response handling.
type handlerWrapper[T any, R any] struct {
	processor   *requestProcessor[T]
	responder   *responseHandler
	checker     *contextChecker
	rawResponse bool
	jose        *joseRouteConfig
}

// newHandlerWrapper creates a new handler wrapper with all processing components initialized.
// log is forwarded to the responseHandler and is used only to warn on envelope-meta
// reserved-key collisions; nil is tolerated and silences those warnings.
func newHandlerWrapper[T, R any](binder *RequestBinder, cfg *config.Config, log logger.Logger, rawResponse bool, joseCfg *joseRouteConfig) *handlerWrapper[T, R] {
	return &handlerWrapper[T, R]{
		processor:   newRequestProcessor[T](binder, cfg),
		responder:   newResponseHandler(cfg, log),
		checker:     newContextChecker(cfg),
		rawResponse: rawResponse,
		jose:        joseCfg,
	}
}

// wrap converts a business logic handler into an Echo-compatible handler function.
// Orchestration is broken into helper methods to keep cognitive complexity manageable
// (SonarCloud S3776) and so each phase of the pipeline reads in isolation.
func (hw *handlerWrapper[T, R]) wrap(handlerFunc HandlerFunc[T, R]) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if hw.rawResponse {
			c.Set(rawResponseContextKey, true)
		}
		formatErr := hw.selectErrorFormatter()

		if apiErr := hw.checker.checkCancellation(c, "or canceled"); apiErr != nil {
			return formatErr(c, apiErr, hw.responder.cfg)
		}
		if apiErr := hw.runJOSEInbound(c); apiErr != nil {
			return formatJOSEPlaintextError(c, apiErr)
		}

		request, apiErr := hw.processor.process(c)
		if apiErr != nil {
			return formatErr(c, apiErr, hw.responder.cfg)
		}
		if cancelErr := hw.checker.checkCancellation(c, "during validation"); cancelErr != nil {
			return formatErr(c, cancelErr, hw.responder.cfg)
		}

		response, apiErr := handlerFunc(request, newHandlerContext(c, hw.responder.cfg))
		if c.Request().Context().Err() != nil {
			return formatErr(c, NewServiceUnavailableError("Request timeout or canceled during handler execution"), hw.responder.cfg)
		}
		return hw.dispatchResponse(c, response, apiErr)
	}
}

// runJOSEInbound runs the JOSE decrypt+verify step when the route has an inbound policy,
// recording the failure and returning the IAPIError so the caller can emit the plaintext
// minimal envelope. Returns nil for non-JOSE routes or successful decode.
func (hw *handlerWrapper[T, R]) runJOSEInbound(c *echo.Context) IAPIError {
	policy := hw.jose.inbound()
	if policy == nil {
		return nil
	}
	apiErr := joseDecodeRequestWithObs(c, policy, hw.jose.resolver(), hw.jose.obs())
	if apiErr != nil {
		hw.jose.obs().recordFailure(c.Request().Context(), c, "inbound", apiErr)
	}
	return apiErr
}

// dispatchResponse picks the correct response writer based on JOSE / raw configuration.
func (hw *handlerWrapper[T, R]) dispatchResponse(c *echo.Context, response any, apiErr IAPIError) error {
	if outbound := hw.jose.outbound(); outbound != nil {
		return hw.responder.joseHandleResponseWithObs(c, response, apiErr, outbound, hw.jose.resolver(), hw.jose.obs())
	}
	if hw.rawResponse {
		return hw.responder.handleRawResponse(c, response, apiErr)
	}
	return hw.responder.handleResponse(c, response, apiErr)
}

// selectErrorFormatter returns the appropriate error formatter for this wrapper's
// JOSE / raw configuration. The returned closure inspects per-request context state
// (inbound-verified flag) at call time so a single formatter handles both pre-trust
// and post-trust JOSE failure paths correctly.
func (hw *handlerWrapper[T, R]) selectErrorFormatter() func(*echo.Context, IAPIError, *config.Config) error {
	switch {
	case hw.jose.outbound() != nil:
		outbound := hw.jose.outbound()
		resolver := hw.jose.resolver()
		obs := hw.jose.obs()
		return func(c *echo.Context, apiErr IAPIError, cfg *config.Config) error {
			if jose.IsInboundVerified(c.Request().Context()) {
				return formatJOSEPostTrustError(c, apiErr, outbound, resolver, cfg, obs)
			}
			return formatJOSEPlaintextError(c, apiErr)
		}
	case hw.rawResponse:
		return formatRawErrorResponse
	default:
		return formatErrorResponse
	}
}

// WrapHandler wraps a business logic handler into an Echo-compatible handler.
// It handles request binding, validation, response formatting, and error handling.
// Supports both value and pointer types for requests (T) and responses (R).
// Pointer types eliminate copy overhead for large payloads (>1KB recommended).
//
// This function delegates to handlerWrapper which composes specialized components:
// - contextChecker: Detects request cancellation/timeout
// - requestProcessor: Allocates, binds, and validates requests
// - responseHandler: Formats success and error responses
func WrapHandler[T any, R any](
	handlerFunc HandlerFunc[T, R],
	binder *RequestBinder,
	cfg *config.Config,
) echo.HandlerFunc {
	return wrapHandler(handlerFunc, binder, cfg, nil, false)
}

// wrapHandler is the internal implementation that supports raw response mode; WrapHandler
// always passes rawResponse=false here. RegisterHandler instead calls wrapHandlerWithJOSE,
// which additionally threads a joseRouteConfig.
func wrapHandler[T any, R any](
	handlerFunc HandlerFunc[T, R],
	binder *RequestBinder,
	cfg *config.Config,
	log logger.Logger,
	rawResponse bool,
) echo.HandlerFunc {
	wrapper := newHandlerWrapper[T, R](binder, cfg, log, rawResponse, nil)
	return wrapper.wrap(handlerFunc)
}

// wrapHandlerWithJOSE is the JOSE-aware variant invoked from RegisterHandler when a
// route has resolved JOSE policies. The joseRouteConfig bundle keeps the constructor
// signature small (SonarCloud S107) and lets future JOSE fields land without further
// signature changes.
func wrapHandlerWithJOSE[T any, R any](
	handlerFunc HandlerFunc[T, R],
	binder *RequestBinder,
	cfg *config.Config,
	log logger.Logger,
	rawResponse bool,
	joseCfg *joseRouteConfig,
) echo.HandlerFunc {
	wrapper := newHandlerWrapper[T, R](binder, cfg, log, rawResponse, joseCfg)
	return wrapper.wrap(handlerFunc)
}

// bindRequest binds request data from various sources to the target struct. It is the
// legacy per-request-reflecting path, kept alive as requestProcessor.process's fallback
// for non-struct T (F26): bindStructFields's NumField() call panics at request time for
// non-struct types, and that panic must stay exactly there. Struct T never reaches this
// path — it uses bindRequestPlanned instead.
func (rb *RequestBinder) bindRequest(c *echo.Context, target any) error {
	targetValue := reflect.ValueOf(target).Elem()
	targetType := targetValue.Type()

	// Bind JSON body if present
	if err := rb.bindJSONBody(c, target); err != nil {
		return err
	}

	// Bind struct field tags (param, query, header)
	return rb.bindStructFields(c, targetType, targetValue)
}

// bindRequestPlanned binds request data using a precomputed binding plan (buildBindingPlan),
// avoiding per-request struct-tag reflection. When plan is empty — the common case, e.g. a
// JSON-body-only struct — only bindJSONBody runs.
func (rb *RequestBinder) bindRequestPlanned(c *echo.Context, target any, plan []boundField) error {
	if err := rb.bindJSONBody(c, target); err != nil {
		return err
	}
	if len(plan) == 0 {
		return nil
	}

	targetValue := reflect.ValueOf(target).Elem()
	for i := range plan {
		bf := &plan[i]
		fieldValue := targetValue.Field(bf.index)

		var err error
		switch bf.source {
		case bindSourceParam:
			err = rb.bindParamValue(c, bf.name, fieldValue)
		case bindSourceQuery:
			err = rb.bindQueryValue(c, bf.name, bf.isSlice, fieldValue)
		case bindSourceHeader:
			err = rb.bindHeaderValue(c, bf.name, bf.isSlice, fieldValue)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// bindJSONBody binds JSON request body if Content-Type indicates JSON
func (rb *RequestBinder) bindJSONBody(c *echo.Context, target any) error {
	ct := c.Request().Header.Get(echo.HeaderContentType)
	if ct == "" {
		return nil
	}

	mt, _, _ := mime.ParseMediaType(ct)
	if mt == echo.MIMEApplicationJSON || strings.HasSuffix(mt, "+json") {
		if err := c.Bind(target); err != nil {
			return fmt.Errorf("failed to bind JSON body: %w", err)
		}
	}
	return nil
}

// bindStructFields binds path parameters, query parameters, and headers using struct tags
func (rb *RequestBinder) bindStructFields(c *echo.Context, targetType reflect.Type, targetValue reflect.Value) error {
	for i := 0; i < targetType.NumField(); i++ {
		field := targetType.Field(i)
		fieldValue := targetValue.Field(i)

		if !fieldValue.CanSet() {
			continue
		}

		if err := rb.bindFieldFromTags(c, &field, fieldValue); err != nil {
			return err
		}
	}
	return nil
}

// bindFieldFromTags binds a single field from various tag sources
func (rb *RequestBinder) bindFieldFromTags(c *echo.Context, field *reflect.StructField, fieldValue reflect.Value) error {
	if err := rb.bindParamTag(c, field, fieldValue); err != nil {
		return err
	}
	if err := rb.bindQueryTag(c, field, fieldValue); err != nil {
		return err
	}
	if err := rb.bindHeaderTag(c, field, fieldValue); err != nil {
		return err
	}
	return nil
}

// bindParamTag binds path parameters using the "param" tag
func (rb *RequestBinder) bindParamTag(c *echo.Context, field *reflect.StructField, fieldValue reflect.Value) error {
	paramName := field.Tag.Get("param")
	if paramName == "" {
		return nil
	}
	return rb.bindParamValue(c, paramName, fieldValue)
}

// bindParamValue is the value-setting core shared by bindParamTag (legacy, tag-parsing)
// and bindRequestPlanned (precomputed name from buildBindingPlan).
func (rb *RequestBinder) bindParamValue(c *echo.Context, paramName string, fieldValue reflect.Value) error {
	value := c.Param(paramName)
	if value != "" {
		if err := setFieldValue(fieldValue, value); err != nil {
			return fmt.Errorf("failed to set path param %s: %w", paramName, err)
		}
	}
	return nil
}

// bindQueryTag binds query parameters using the "query" tag
func (rb *RequestBinder) bindQueryTag(c *echo.Context, field *reflect.StructField, fieldValue reflect.Value) error {
	queryName := field.Tag.Get("query")
	if queryName == "" {
		return nil
	}
	return rb.bindQueryValue(c, queryName, rb.isStringSliceField(fieldValue), fieldValue)
}

// bindQueryValue is the value-setting core shared by bindQueryTag (legacy, tag-parsing)
// and bindRequestPlanned (precomputed name/isSlice from buildBindingPlan).
func (rb *RequestBinder) bindQueryValue(c *echo.Context, queryName string, isSlice bool, fieldValue reflect.Value) error {
	// Support []string binding from repeated query parameters
	if isSlice {
		return rb.bindQueryStringSlice(c, queryName, fieldValue)
	}

	value := c.QueryParam(queryName)
	if value != "" {
		if err := setFieldValue(fieldValue, value); err != nil {
			return fmt.Errorf("failed to set query param %s: %w", queryName, err)
		}
	}
	return nil
}

// bindQueryStringSlice binds repeated query parameters to a []string field
func (rb *RequestBinder) bindQueryStringSlice(c *echo.Context, queryName string, fieldValue reflect.Value) error {
	values := c.QueryParams()[queryName]
	if len(values) > 0 {
		slice := reflect.MakeSlice(fieldValue.Type(), len(values), len(values))
		for i, v := range values {
			slice.Index(i).SetString(v)
		}
		fieldValue.Set(slice)
	}
	return nil
}

// bindHeaderTag binds headers using the "header" tag
func (rb *RequestBinder) bindHeaderTag(c *echo.Context, field *reflect.StructField, fieldValue reflect.Value) error {
	headerName := field.Tag.Get("header")
	if headerName == "" {
		return nil
	}
	return rb.bindHeaderValue(c, headerName, rb.isStringSliceField(fieldValue), fieldValue)
}

// bindHeaderValue is the value-setting core shared by bindHeaderTag (legacy, tag-parsing)
// and bindRequestPlanned (precomputed name/isSlice from buildBindingPlan).
func (rb *RequestBinder) bindHeaderValue(c *echo.Context, headerName string, isSlice bool, fieldValue reflect.Value) error {
	values := c.Request().Header.Values(headerName)
	if len(values) == 0 {
		return nil
	}

	// Support comma-separated list for []string headers
	if isSlice {
		return rb.bindHeaderStringSlice(values, fieldValue)
	}

	if err := setFieldValue(fieldValue, values[0]); err != nil {
		return fmt.Errorf("failed to set header %s: %w", headerName, err)
	}
	return nil
}

// bindHeaderStringSlice binds comma-separated header values to a []string field
func (rb *RequestBinder) bindHeaderStringSlice(values []string, fieldValue reflect.Value) error {
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
	return nil
}

// isStringSliceField checks if a field is a []string slice
func (rb *RequestBinder) isStringSliceField(fieldValue reflect.Value) bool {
	return isStringSliceType(fieldValue.Type())
}

// isStringSliceType is isStringSliceField's build-time twin: same check, driven by the
// static field type (buildBindingPlan) instead of a runtime reflect.Value.
func isStringSliceType(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String
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
	if fieldValue.Kind() == reflect.Pointer {
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

// setSpecialType handles special types like time.Time.
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

// setStringValue sets a string field value.
func setStringValue(fieldValue reflect.Value, value string) error {
	fieldValue.SetString(value)
	return nil
}

// setSignedIntValue sets a signed integer field value.
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

// setUnsignedIntValue sets an unsigned integer field value.
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

// setFloatValue sets a float field value.
func setFloatValue(fieldValue reflect.Value, value string) error {
	floatVal, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return err
	}
	fieldValue.SetFloat(floatVal)
	return nil
}

// setBoolValue sets a boolean field value.
func setBoolValue(fieldValue reflect.Value, value string) error {
	boolVal, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	fieldValue.SetBool(boolVal)
	return nil
}

// parseTime attempts to parse a string into time.Time using common layouts.
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
//
// The default (non-merge) success path encodes via frameworkEnvelope so it avoids the
// map allocation and per-key any-boxing of APIResponse.Meta; the wire shape is identical
// ({"data":...,"meta":{"timestamp":...,"traceId":...}}).
func formatSuccessResponse(c *echo.Context, data any) error {
	ensureTraceParentHeader(c)
	return c.JSON(http.StatusOK, frameworkEnvelope{
		Data: data,
		Meta: newFrameworkMeta(c),
	})
}

// formatSuccessResponseWithStatus formats a successful response with a custom status and headers.
func formatSuccessResponseWithStatus(c *echo.Context, data any, status int, headers http.Header) error {
	return formatSuccessEnvelopeWithStatus(c, data, status, headers, nil, nil)
}

// formatSuccessEnvelopeWithStatus is the shared writer for success envelopes. It accepts
// an optional userMeta map; reserved keys (timestamp/traceId) are dropped before merging,
// with a structured warning emitted via log when supplied. log may be nil — the warning is
// then suppressed but reserved keys are still dropped (framework keys remain authoritative).
func formatSuccessEnvelopeWithStatus(c *echo.Context, data any, status int, headers http.Header, userMeta map[string]any, log logger.Logger) error {
	if status == 0 {
		status = http.StatusOK
	}
	// SAFETY: Check if response is still valid before adding headers
	if resp := c.Response(); resp != nil {
		for k, vals := range headers {
			for _, v := range vals {
				resp.Header().Add(k, v)
			}
		}
	}

	ensureTraceParentHeader(c)
	// 204 No Content and 304 Not Modified MUST NOT carry a body per RFC 7230 §3.3.3.
	// Short-circuit BEFORE building the meta map so handler-supplied userMeta is silently
	// dropped (the wire shape mandates it) and reserved-key WARNs don't fire for a body
	// that will never ship.
	if status == http.StatusNoContent {
		return c.NoContent(http.StatusNoContent)
	}
	if status == http.StatusNotModified {
		return c.NoContent(http.StatusNotModified)
	}

	response := APIResponse{
		Data: data,
		Meta: mergeEnvelopeMeta(c, userMeta, log),
	}
	return c.JSON(status, response)
}

// buildFrameworkMeta returns the framework-managed envelope meta map populated with
// timestamp and traceId. Callers that need to merge handler-supplied meta should use
// mergeEnvelopeMeta instead.
func buildFrameworkMeta(c *echo.Context) map[string]any {
	return map[string]any{
		fieldTimestamp: time.Now().UTC().Format(time.RFC3339),
		fieldTraceID:   getTraceID(c),
	}
}

// newFrameworkMeta builds the typed form of the default envelope meta. It computes
// the same values as buildFrameworkMeta (RFC3339 UTC timestamp + traceId) so the wire
// output is identical, but as a struct it avoids the map allocation and per-key
// any-boxing on the default success/error encode paths.
func newFrameworkMeta(c *echo.Context) frameworkMeta {
	return frameworkMeta{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		TraceID:   getTraceID(c),
	}
}

// mergeEnvelopeMeta returns the union of handler-supplied meta and framework-managed meta.
// Framework keys (reservedMetaKeys) are authoritative: any handler value for those keys is
// dropped, and a structured WARN is emitted via log identifying the offending key and the
// route path so SREs can locate the misbehaving handler. log==nil disables the warning but
// preserves the drop. Trailing writes to fieldTimestamp/fieldTraceID are the authoritative
// guarantee; the per-key drop in the loop exists to emit the WARN signal — a maintainer
// relaxing the loop guard would NOT regress the framework-key invariant.
func mergeEnvelopeMeta(c *echo.Context, userMeta map[string]any, log logger.Logger) map[string]any {
	out := make(map[string]any, len(userMeta)+len(reservedMetaKeys))
	for k, v := range userMeta {
		if _, reserved := reservedMetaKeys[k]; reserved {
			if log != nil {
				// SAFETY: c.Request() can be nil after Echo's TimeoutHandler invalidates
				// the underlying writer. Fall back to a background context so the WARN
				// still ships (without trace correlation) instead of panicking.
				ctx := context.Background()
				if req := c.Request(); req != nil {
					ctx = req.Context()
				}
				log.WithContext(ctx).Warn().
					Str("key", k).
					Str("route", c.Path()).
					Msg("envelope meta key collides with framework-managed key; handler value dropped")
			}
			continue
		}
		out[k] = v
	}
	out[fieldTimestamp] = time.Now().UTC().Format(time.RFC3339)
	out[fieldTraceID] = getTraceID(c)
	return out
}

// formatErrorResponse formats an error response with standardized structure.
func formatErrorResponse(c *echo.Context, apiErr IAPIError, cfg *config.Config) error {
	// SAFETY: Prevent double-writes if response already committed.
	// Defense in depth - primary check is in customErrorHandler.
	if isResponseCommitted(c) {
		return nil
	}

	errorResp := &APIErrorResponse{
		Code:    apiErr.ErrorCode(),
		Message: apiErr.Message(),
	}

	if cfg.App.IsDevelopment() {
		errorResp.Details = devDetails(apiErr)
	}

	// Default (non-merge) error path: encode via the typed frameworkEnvelope to skip the
	// meta map allocation. Wire shape is identical to APIResponse{Error, Meta:map} —
	// {"error":{...},"meta":{"timestamp":...,"traceId":...}}.
	ensureTraceParentHeader(c)
	return c.JSON(apiErr.HTTPStatus(), frameworkEnvelope{
		Error: errorResp,
		Meta:  newFrameworkMeta(c),
	})
}

// devDetails returns the dev-only details payload: caller-supplied details merged
// with a "stackTrace" entry if the error implements StackTracer and a stack
// was captured. Returns nil when nothing would be rendered, so callers can rely
// on json `omitempty` to drop the field cleanly.
//
// The IAPIError interface does not guarantee Details() returns a copy
// (BaseAPIError happens to, but user-defined error types may not), so we always
// copy before injecting stackTrace to avoid mutating caller-owned state.
func devDetails(apiErr IAPIError) map[string]any {
	src := apiErr.Details()
	st, hasStack := apiErr.(StackTracer)
	var frames []string
	if hasStack {
		frames = st.StackTrace()
	}
	if len(src) == 0 && len(frames) == 0 {
		return nil
	}

	out := make(map[string]any, len(src)+1)
	for k, v := range src {
		out[k] = v
	}
	if len(frames) > 0 {
		out[stackTraceDetailKey] = frames
	}
	return out
}

// handleRawResponse formats and sends the HTTP response without the APIResponse envelope.
// Mirrors handleResponse logic but writes the response data directly as JSON.
//
// Raw mode strips the envelope by definition. Handlers returning ResultEnvelopeProvider
// (e.g. ResultWithMeta) on a raw route have their meta map silently dropped; the bare data
// is serialized. A debug-level log notes the misconfiguration when a logger is wired so
// the divergence is at least traceable in development.
func (rh *responseHandler) handleRawResponse(c *echo.Context, response any, apiErr IAPIError) error {
	if apiErr != nil {
		return formatRawErrorResponse(c, apiErr, rh.cfg)
	}

	if re, ok := response.(ResultEnvelopeProvider); ok {
		status, headers, data, meta := re.ResultEnvelope()
		if len(meta) > 0 && rh.log != nil {
			// SAFETY: tolerate nil c.Request() under Echo TimeoutHandler teardown.
			ctx := context.Background()
			if req := c.Request(); req != nil {
				ctx = req.Context()
			}
			rh.log.WithContext(ctx).Debug().
				Str("route", c.Path()).
				Str("type", fmt.Sprintf("%T", response)).
				Int("dropped_meta_keys", len(meta)).
				Msg("raw response mode dropped envelope meta from ResultEnvelopeProvider")
		}
		return formatRawSuccessResponseWithStatus(c, data, status, headers)
	}

	if rl, ok := response.(ResultMetaProvider); ok {
		status, headers, data := rl.ResultMeta()
		return formatRawSuccessResponseWithStatus(c, data, status, headers)
	}

	return formatRawSuccessResponse(c, response)
}

// formatRawSuccessResponse writes the response data directly as JSON without the APIResponse envelope.
func formatRawSuccessResponse(c *echo.Context, data any) error {
	ensureTraceParentHeader(c)
	return c.JSON(http.StatusOK, data)
}

// formatRawSuccessResponseWithStatus writes the response data directly with a custom status and headers.
func formatRawSuccessResponseWithStatus(c *echo.Context, data any, status int, headers http.Header) error {
	if status == 0 {
		status = http.StatusOK
	}
	if resp := c.Response(); resp != nil {
		for k, vals := range headers {
			for _, v := range vals {
				resp.Header().Add(k, v)
			}
		}
	}

	ensureTraceParentHeader(c)
	// Aligned with formatSuccessEnvelopeWithStatus and joseHandleResponse: 204/304 MUST
	// NOT carry a body per RFC 7230 §3.3.3. Raw mode is no exception — the writer-parity
	// contract is what makes Strangler Fig migrations predictable.
	if status == http.StatusNoContent {
		return c.NoContent(http.StatusNoContent)
	}
	if status == http.StatusNotModified {
		return c.NoContent(http.StatusNotModified)
	}

	return c.JSON(status, data)
}

// rawErrorPayload is the minimal error structure used in raw response mode.
type rawErrorPayload struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// formatRawErrorResponse formats an error without the APIResponse envelope.
// Produces minimal JSON: {"code": "...", "message": "..."} with optional details in development.
func formatRawErrorResponse(c *echo.Context, apiErr IAPIError, cfg *config.Config) error {
	if isResponseCommitted(c) {
		return nil
	}

	payload := rawErrorPayload{
		Code:    apiErr.ErrorCode(),
		Message: apiErr.Message(),
	}

	if cfg.App.IsDevelopment() {
		payload.Details = devDetails(apiErr)
	}

	ensureTraceParentHeader(c)
	return c.JSON(apiErr.HTTPStatus(), payload)
}

// getTraceID extracts or generates a trace ID for the request.
//
// The inbound X-Request-ID header is caller-controlled, so it's validated
// against a strict charset/length pattern before use. Values that fail
// validation are discarded and a fresh UUID is generated — preventing
// attackers from poisoning logs or reusing a victim's request ID for
// correlation-confusion attacks. The response-header read is ALSO validated
// as defense in depth: the framework's RequestIDMiddleware should populate
// it with a known-good value, but the validation is cheap and protects
// downstream consumers if the middleware is ever misconfigured or replaced.
func getTraceID(c *echo.Context) string {
	// Prefer incoming request header — but validate it first.
	if requestID := validateRequestID(c.Request().Header.Get(echo.HeaderXRequestID)); requestID != "" {
		return requestID
	}
	// Then try the response header (normally set by RequestIDMiddleware).
	// SAFETY: After a timeout, c.Response() may be nil due to timeoutHandler invalidating
	// the underlying ResponseWriter. We check for nil to prevent panic.
	if resp := c.Response(); resp != nil {
		if requestID := validateRequestID(resp.Header().Get(echo.HeaderXRequestID)); requestID != "" {
			return requestID
		}
	}
	newID := uuid.New().String()
	// Set it so downstream might pick it up (only if response is still valid)
	if resp := c.Response(); resp != nil {
		resp.Header().Set(echo.HeaderXRequestID, newID)
	}
	return newID
}

// ensureTraceParentHeader ensures the response contains a W3C traceparent header.
// It propagates the inbound header when present, otherwise generates a new one.
func ensureTraceParentHeader(c *echo.Context) {
	// SAFETY: Check if response is still valid (may be nil after timeout)
	resp := c.Response()
	if resp == nil {
		return
	}
	// If already set, do nothing
	if resp.Header().Get(gobrickshttp.HeaderTraceParent) != "" {
		return
	}
	// Prefer inbound header
	if tp := c.Request().Header.Get(gobrickshttp.HeaderTraceParent); tp != "" {
		resp.Header().Set(gobrickshttp.HeaderTraceParent, tp)
		return
	}
	// Generate new traceparent and set
	resp.Header().Set(gobrickshttp.HeaderTraceParent, gobrickshttp.GenerateTraceParent())
}

// RouteRegistrar abstracts the subset of Echo's routing features that modules need
// while allowing the server to enforce common behavior such as base-path handling.
// Implementations wrap Echo groups internally; the public surface is echo-free —
// handlers are server.Handler and middleware is server.MiddlewareFunc.
//
// Add does not return a route handle: the value was never consumed anywhere and exposing
// echo.RouteInfo would re-introduce the leak this abstraction removes.
type RouteRegistrar interface {
	Add(method, path string, handler Handler, middleware ...MiddlewareFunc)
	Group(prefix string, middleware ...MiddlewareFunc) RouteRegistrar
	Use(middleware ...MiddlewareFunc)
	FullPath(path string) string
}

// echoAdder is the unexported, echo-direct registration seam. Only routeGroup
// implements it (within package server), so the framework's typed-handler hot path can
// register a pre-built echo.HandlerFunc with zero per-request adapter overhead (honoring
// ADR-026), while consumers use the echo-free RouteRegistrar.Add. It is an optional
// interface upgrade in the style of io.ReaderFrom.
type echoAdder interface {
	addEcho(method, path string, h echo.HandlerFunc)
}

// HandlerRegistry manages enhanced handlers and provides registration utilities.
type HandlerRegistry struct {
	binder            *RequestBinder
	cfg               *config.Config
	log               logger.Logger // general-purpose logger; nilable, used for envelope-meta warnings
	joseResolver      jose.KeyResolver
	joseLogger        logger.Logger
	joseTracer        trace.Tracer
	joseMeterProvider metric.MeterProvider
	joseObs           *joseObservability // computed lazily on first JOSE registration
}

// NewHandlerRegistry creates a new handler registry with the given validator and config.
// Optional HandlerRegistryOption values (e.g., WithJOSEResolver) configure additional
// behaviors. Existing callers passing only cfg are unaffected.
func NewHandlerRegistry(cfg *config.Config, opts ...HandlerRegistryOption) *HandlerRegistry {
	hr := &HandlerRegistry{
		binder: NewRequestBinder(),
		cfg:    cfg,
	}
	for _, opt := range opts {
		opt(hr)
	}
	return hr
}

// RegisterHandler registers a typed handler with the route registrar and captures metadata.
func RegisterHandler[T any, R any](
	hr *HandlerRegistry,
	r RouteRegistrar,
	method, path string,
	handler HandlerFunc[T, R],
	opts ...RouteOption,
) {
	var reqType T
	var respType R

	// Determine final path after registrar adjustments (e.g. base path prefixes)
	fullPath := r.FullPath(path)

	// Create descriptor with type information
	descriptor := RouteDescriptor{
		Method:       method,
		Path:         fullPath,
		HandlerID:    formatHandlerID(method, fullPath),
		RequestType:  reflect.TypeOf(reqType),
		ResponseType: reflect.TypeOf(respType),
		Package:      getCallerPackage(3), // getCallerPackage → RegisterHandler → GET/POST/etc → module
		HandlerName:  extractHandlerName(handler),
	}

	// Apply options
	for _, opt := range opts {
		opt(&descriptor)
	}

	// Scan request/response types for jose: tags and resolve every kid against the
	// registry's resolver. Panics at startup on any failure (Fail Fast principle).
	scanRouteJOSE(&descriptor, hr.joseResolver)

	// Register with global registry
	DefaultRouteRegistry.Register(&descriptor)

	// Lazy-init observability bundle on the first JOSE-tagged route registration so
	// non-JOSE deployments pay zero cost for instrument creation. Subsequent JOSE
	// registrations reuse the same bundle (counters and histograms are process-global).
	if descriptor.InboundJOSE != nil && hr.joseObs == nil {
		hr.joseObs = newJOSEObservability(hr.joseLogger, hr.joseTracer, hr.joseMeterProvider)
	}

	// Register with route registrar (works with both Echo instances and Groups)
	var joseCfg *joseRouteConfig
	if descriptor.InboundJOSE != nil {
		joseCfg = &joseRouteConfig{
			Inbound:  descriptor.InboundJOSE,
			Outbound: descriptor.OutboundJOSE,
			Resolver: hr.joseResolver,
			Obs:      hr.joseObs,
		}
	}
	wrappedHandler := wrapHandlerWithJOSE(handler, hr.binder, hr.cfg, hr.log, descriptor.RawResponse, joseCfg)
	// Hot path: register the echo handler directly through the unexported seam so the typed
	// handler path adds zero per-request adapter overhead (ADR-026). The fallback adapts for
	// non-routeGroup registrars (test fakes); it round-trips through the unexported escape
	// hatch and never executes on the framework's real registrar.
	if er, ok := r.(echoAdder); ok {
		er.addEcho(method, path, wrappedHandler)
	} else {
		r.Add(method, path, func(c HandlerContext) error {
			ec := c.echoContext()
			if ec == nil {
				return NewInternalServerError("typed handler invoked with a HandlerContext lacking a request context")
			}
			return wrappedHandler(ec)
		})
	}
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

// ResultMetaProvider exposes status, headers, and payload for successful responses.
type ResultMetaProvider interface {
	ResultMeta() (status int, headers http.Header, data any)
}

// ResultEnvelopeProvider is the alternative to ResultMetaProvider for handlers that want
// to contribute extra entries (pagination totals, deprecation notices, etc.) to the response
// envelope's meta map. The two interfaces have disjoint method sets (ResultEnvelope vs
// ResultMeta) and ResultEnvelopeProvider does NOT embed ResultMetaProvider — types wanting
// to be visible to legacy ResultMetaProvider-aware code (e.g. third-party middleware) must
// implement both methods explicitly. ResultWithMeta[R] does so as the reference example.
//
// The framework merges the returned meta map with its own keys (timestamp, traceId);
// reservedMetaKeys are always authoritative — handler values for those keys are dropped
// with a structured WARN.
//
// The framework dispatcher checks ResultEnvelopeProvider BEFORE ResultMetaProvider so a
// type satisfying both routes through the envelope path. Maintainers MUST preserve that
// ordering at every dispatch site (server.handleResponse, server.handleRawResponse,
// JOSE outbound) or ResultWithMeta would silently lose its meta payload.
//
// On JOSE-protected routes, ResultEnvelopeProvider causes the sealed body to ship an
// envelope shape ({"data": ..., "meta": ...}) instead of bare data. In raw-response mode
// (WithRawResponse), the meta map is silently dropped — only data is serialized.
type ResultEnvelopeProvider interface {
	ResultEnvelope() (status int, headers http.Header, data any, meta map[string]any)
}

// Result is a generic success wrapper allowing handlers to customize status and headers
// while preserving type safety of the response payload.
type Result[R any] struct {
	Data    R
	Status  int
	Headers http.Header
}

// ResultMeta implements ResultMetaProvider for Result[R].
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

// ResultMeta implements ResultMetaProvider for NoContentResult
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

// ResultWithMeta is a generic success wrapper that carries handler-supplied envelope meta
// alongside the standard status/headers/data. The framework merges Meta into the response
// envelope's meta map, with timestamp and traceId remaining framework-managed and
// authoritative (handler values for those keys are dropped during merge).
//
// Implements both ResultEnvelopeProvider (preferred by the framework dispatcher) and
// ResultMetaProvider (so third-party middleware that type-asserts the older interface keeps
// working — meta is dropped on that path).
type ResultWithMeta[R any] struct {
	Data    R
	Meta    map[string]any
	Status  int
	Headers http.Header
}

// ResultEnvelope implements ResultEnvelopeProvider for ResultWithMeta[R].
func (r ResultWithMeta[R]) ResultEnvelope() (status int, headers http.Header, data any, meta map[string]any) {
	return r.Status, r.Headers, r.Data, r.Meta
}

// ResultMeta implements ResultMetaProvider for ResultWithMeta[R]. Provided so consumers
// that only type-assert ResultMetaProvider keep functioning; on that path Meta is dropped
// — handlers wanting envelope-meta semantics must reach a ResultEnvelopeProvider-aware writer.
func (r ResultWithMeta[R]) ResultMeta() (status int, headers http.Header, data any) {
	return r.Status, r.Headers, r.Data
}

// NewResultWithMeta is a convenience constructor for ResultWithMeta.
func NewResultWithMeta[R any](status int, data R, meta map[string]any) ResultWithMeta[R] {
	return ResultWithMeta[R]{
		Data:   data,
		Meta:   meta,
		Status: status,
	}
}

// WithLogger attaches a general-purpose logger to the handler registry. Used by the
// response pipeline to emit:
//   - A structured WARN when a handler-supplied envelope meta key collides with a
//     framework-reserved key (timestamp, traceId).
//   - A structured DEBUG when raw-response mode drops envelope meta returned by a
//     ResultEnvelopeProvider (typically misconfigured routes).
//
// Independent of WithJOSELogger. Passing nil is a no-op — reserved keys are still dropped,
// the warning is just suppressed.
func WithLogger(log logger.Logger) HandlerRegistryOption {
	return func(hr *HandlerRegistry) {
		hr.log = log
	}
}

// getCallerPackage extracts the package path of the calling function. skip is the number of
// stack frames above getCallerPackage to inspect: the typed registration path passes 3
// (getCallerPackage → RegisterHandler → GET/POST/etc → module), the raw RouteRegistrar.Add
// path passes 2 (getCallerPackage → Add → module).
func getCallerPackage(skip int) string {
	pc, _, _, ok := runtime.Caller(skip)
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
func extractHandlerName(handler any) string {
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
