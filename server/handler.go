package server

import (
	"context"
	"encoding"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/saferender"
	"github.com/gaborage/go-bricks/jose"
	"github.com/gaborage/go-bricks/logger"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
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

// TestContextSeeder seeds a HandlerContext built by NewHandlerContextForTest with
// pre-routing state the engine would otherwise populate during matching, so unit tests can
// exercise code that reads that state without standing up a router. Test-support only:
// seeders are applied at test construction, never on a live request context.
//
// The type is sealed (ADR-165): its only method is unexported, so the constructors in this
// package (WithRouteTemplate) are the only source of a working value, and the framework can
// change how a seeder seeds the context without breaking a consumer.
type TestContextSeeder interface {
	seedTestContext(c *HandlerContext)
}

// routeTemplateSeeder is the TestContextSeeder WithRouteTemplate returns.
type routeTemplateSeeder struct{ template string }

func (s routeTemplateSeeder) seedTestContext(c *HandlerContext) {
	c.ectx.SetPath(s.template) //nolint:forbidigo // test seeder: writes the route template a test names, not a caller-written path (#2212)
	c.ectx.Request().Pattern = s.template
}

// WithRouteTemplate stamps the matched route template so RouteTemplate() reports it on an
// otherwise-unrouted test context (which never routes and would report ""). It stamps both
// places the router writes on a live request — the context and r.Pattern, which echo sets for
// standard-library middlewares and which CreateProbeSkipper reads — so a test context cannot
// present a template through one and an empty one through the other. On a live request the
// router owns both values; the seeder is reachable only through the test constructor, so it
// cannot mutate a routed context's identity. See issue #639.
func WithRouteTemplate(template string) TestContextSeeder {
	return routeTemplateSeeder{template: template}
}

// NewHandlerContextForTest builds a HandlerContext backed by a real Echo context for use
// in external-package tests (e.g. app/, scheduler/) that exercise Handler / MiddlewareFunc
// code but cannot name the unexported escape hatch. It keeps the echo dependency confined
// to package server. Test-support only — not for production wiring. TestContextSeeder
// values (e.g. WithRouteTemplate) seed routing state the synthetic context would otherwise
// leave empty.
func NewHandlerContextForTest(w http.ResponseWriter, r *http.Request, cfg *config.Config, opts ...TestContextSeeder) HandlerContext {
	e := newEngine()
	// Register the framework validator so contexts built here drive the typed pipeline
	// (which calls c.Validate) exactly as a live request would.
	e.Validator = NewValidator()
	c := newHandlerContext(e.NewContext(r, w), cfg)
	for _, opt := range opts {
		opt.seedTestContext(&c)
	}
	return c
}

// Request returns the underlying *http.Request.
//
// SECURITY: its URL (URL.Path, RawPath, RawQuery), RequestURI, query and form values are bytes
// the caller wrote, a card number in a path segment included; never log them. Log
// RouteTemplate() instead, the template the access line carries as http.route (ADR-167).
func (c HandlerContext) Request() *http.Request { return c.ectx.Request() }

// RequestContext returns the request's context.Context. Convenience for the dominant
// pattern (deadline/cancellation, trace and tenant propagation) so callers rarely need
// Request() directly.
func (c HandlerContext) RequestContext() context.Context { return c.ectx.Request().Context() }

// ResponseWriter returns the http.ResponseWriter for the current response.
func (c HandlerContext) ResponseWriter() http.ResponseWriter { return c.ectx.Response() }

// Param returns the path parameter for name (empty string if absent).
func (c HandlerContext) Param(name string) string { return c.ectx.Param(name) } //nolint:forbidigo // consumer door: hands the handler its own request value, which no framework sink prints

// Query returns the first query-string value for name (empty string if absent).
func (c HandlerContext) Query(name string) string { return c.ectx.QueryParam(name) } //nolint:forbidigo // consumer door: hands the handler its own request value, which no framework sink prints

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
// It is the template the application registered, NOT the concrete URL: read a
// matched value through Param or PathParams. On a matched route it is the value
// the access line and the framework's correlated WARN and ERROR lines carry as
// http.route, so a log query keyed on it joins them. No framework sink prints the
// request path (ADR-167); a handler that logs Request().URL.Path itself prints
// what the caller sent, a card number in a segment included. Empty before
// routing completes and on unmatched (404) requests; on a top-level 405 the
// engine sets the best-matching
// route's template (engine-defined, not a contract). Note the asymmetry under a
// middleware-bearing group: a wrong-method or unmatched sub-path there resolves
// to the group's implicit catch-all (a 404), so it returns "" rather than a
// best-match template.
func (c HandlerContext) RouteTemplate() string {
	return matchedRouteTemplate(c.ectx)
}

// matchedRouteTemplate reports the registered route template the engine matched for
// this request, or "" when the engine reports no template — an unmatched path, or a
// middleware-bearing group's catch-all. Two callers read it: RouteTemplate() above and
// the JOSE failure recorder (server/jose.go). Both must carry a registered template or
// nothing at all, because the concrete URL path is caller-chosen: falling back to it
// stamps that string on a metric attribute, minting one time series per distinct unmatched
// path (#1816). The per-request access log is the deliberate exception — it reads a bare
// c.Path() (server/logger.go) because RouteInfo() clones its Parameters slice: 0 allocs on
// a static route, but 1 alloc / 16 B on a single-param one, on a path that runs for every
// request against the ADR-026 allocation ceiling.
//
// The RouteNotFound check is deliberately NARROWER than HandlerContext.isUnmatchedRoute
// below, which covers echo v5.3.0's group catch-alls — auto-registered at both <prefix>
// and <prefix>/*; see isUnmatchedRoute for their RouteInfo shape — and is also true on the
// global 404/405 sentinels. Two reasons it cannot be reused here: on those catch-alls
// c.Path() still returns the group's template although nothing matched, which is exactly
// what this check suppresses; and on a top-level 405 the engine leaves the best-matching
// route's template on the context — a registered, bounded value RouteTemplate() is pinned
// to report, so folding isUnmatchedRoute in would flip that behavior.
func matchedRouteTemplate(c *echo.Context) string {
	if c.RouteInfo().Method == echo.RouteNotFound {
		return ""
	}
	return c.Path()
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
	values := c.ectx.PathValues() //nolint:forbidigo // consumer door: hands the handler its own request value, which no framework sink prints
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

// adaptHandler converts a go-bricks Handler into an echo.HandlerFunc for registration. A
// request whose path tenant the seat refused gets the unknown-tenant 400 instead of h
// (refuseUnknownPathTenant), written here, so the access line records the 400, with its WARN
// logged through log.
func adaptHandler(h Handler, cfg *config.Config, log logger.Logger) echo.HandlerFunc {
	return func(c *echo.Context) error {
		if refused := refuseUnknownPathTenant(c); refused != nil {
			logUnknownTenantRefusal(refused, c, log, cfg.App.Debug)
			return formatErrorResponse(c, refused, cfg)
		}
		return h(newHandlerContext(c, cfg))
	}
}

// adaptMiddleware converts a flat MiddlewareFunc into an echo.MiddlewareFunc. The
// per-request baton closure (func() error { return next(c) }) is the one allocation this
// adapter adds; it lands only on middleware-bearing routes, never the framework's typed
// default path (which registers echo handlers directly via routeGroup.addEcho).
func adaptMiddleware(m MiddlewareFunc, cfg *config.Config) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			return m(newHandlerContext(c, cfg), func() error { return next(c) })
		}
	}
}

// fromEchoMiddleware exposes an echo-native middleware as a flat MiddlewareFunc so the
// framework's own middleware constructors keep an echo-free public signature while their
// logic stays echo-native (and baton-free) on the default path inside setupMiddlewares.
func fromEchoMiddleware(em echo.MiddlewareFunc) MiddlewareFunc {
	return func(c HandlerContext, next func() error) error {
		h := em(func(*echo.Context) error { return next() })
		return h(c.echoContext())
	}
}

// RequestBinder handles binding request data to structs with validation.
type RequestBinder struct{}

// NewRequestBinder creates a new request binder.
func NewRequestBinder() *RequestBinder { return &RequestBinder{} }

// contextChecker handles context cancellation detection at various stages of request processing.
type contextChecker struct{}

// newContextChecker creates a new context checker.
func newContextChecker() *contextChecker {
	return &contextChecker{}
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
		// Convert first: for a defined pointer type (type P *Request), reflect.New
		// yields the unnamed *Request, which is not assertable to P. Converting to
		// elemType — T's own reflect.Type — makes the assertion exact for both
		// forms, so discarding ok is safe and the hot path stays branch-free.
		request, _ = elem.Convert(ra.elemType).Interface().(T)
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
		return errors.New("request cannot be nil")
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
// struct tags. index is the field's index path from the request struct (one element for a
// top-level field, more for one promoted from an embed). isSlice is only meaningful for
// query/header sources.
type boundField struct {
	index   []int
	source  bindSource
	name    string
	isSlice bool
}

// buildBindingPlan walks a request struct type once at route-registration time and
// captures, per exported field Go promotes to the top level (reflect.VisibleFields: the
// top-level fields and those promoted from value, pointer and unexported-type embeds), which
// of the param/query/header tags are present and what each needs at bind time. Fields are
// visited in VisibleFields order and, within a field, instructions are appended param, then
// query, then header, so on replay the last-applied source wins. Unexported fields are
// skipped. t must be a struct kind (requestStructType). It first refuses, through
// checkBindingTags, every tag the plan cannot honor (ADR-173).
func buildBindingPlan(t reflect.Type) ([]boundField, error) {
	if err := checkBindingTags(t, t, nil, nil, false, &tagWalk{onPath: map[walkKey]bool{}, clean: map[reflect.Type]bool{}}); err != nil {
		return nil, err
	}
	var plan []boundField
	for _, field := range reflect.VisibleFields(t) {
		if field.IsExported() {
			plan = appendFieldBindings(plan, &field)
		}
	}
	return plan, nil
}

// walkKey is a type in one walk context: a type met at the embed level and again below a named
// field is judged under both rules.
type walkKey struct {
	t      reflect.Type
	nested bool
}

// tagWalk is checkBindingTags' state: onPath holds the types on the current path, each in its
// context, breaking a cycle without hiding a type the walk meets again on another path; clean memoizes the types a
// nested walk found no tag in, where the verdict depends on the type alone.
type tagWalk struct {
	onPath map[walkKey]bool
	clean  map[reflect.Type]bool
}

// checkBindingTags walks every field of cur, reached from the request type root at index path
// prefix, through embeds, named nested structs and the struct elements of slices, arrays and
// maps alike, and reports the first param, query or header tag the plan cannot honor
// (checkTaggedField). names is the Go path to cur, an element hop spelled "[]". nested is set
// below a named (non-embedded) struct field and below an element hop, where prefix stops
// being a reflect index path.
func checkBindingTags(root, cur reflect.Type, prefix []int, names []string, nested bool, w *tagWalk) error {
	key := walkKey{t: cur, nested: nested}
	if w.onPath[key] || (nested && w.clean[cur]) {
		return nil
	}
	w.onPath[key] = true
	defer delete(w.onPath, key)
	for i := range cur.NumField() {
		if err := checkBindingFieldAt(root, cur, i, append(slices.Clone(prefix), i), names, nested, w); err != nil {
			return err
		}
	}
	if nested {
		w.clean[cur] = true
	}
	return nil
}

// checkBindingFieldAt judges cur's field i, at index under root, and walks the struct it leads to.
func checkBindingFieldAt(root, cur reflect.Type, i int, index []int, names []string, nested bool, w *tagWalk) error {
	field := cur.Field(i)
	path := append(slices.Clone(names), field.Name)
	if hasBindingTag(&field) {
		if err := checkTaggedField(root, &field, index, strings.Join(path, "."), nested); err != nil {
			return err
		}
	}
	inner, viaElement, ok := walkedStruct(field.Type)
	if !ok {
		return nil
	}
	if viaElement {
		path[len(path)-1] += "[]"
	}
	return checkBindingTags(root, inner, index, path, nested || viaElement || !field.Anonymous, w)
}

// walkedStruct returns the struct checkBindingTags descends into from a field of type t: t's
// own struct, through at most one pointer, or else the struct element under any depth of
// pointers, slices, arrays and maps (viaElement). Unlike bindsAsStruct it takes every struct,
// time.Time and an encoding.TextUnmarshaler included, because reflect.VisibleFields promotes
// their fields into the plan too.
func walkedStruct(t reflect.Type) (inner reflect.Type, viaElement, ok bool) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct {
		return t, false, true
	}
	for k := t.Kind(); k == reflect.Pointer || k == reflect.Slice || k == reflect.Array || k == reflect.Map; k = t.Kind() {
		t = t.Elem()
	}
	return t, true, t.Kind() == reflect.Struct
}

// checkTaggedField reports why the tagged field at index under root is never bound, or nil.
// The plan cannot bind a field that:
//   - sits inside a named nested struct, which Go does not promote;
//   - is unexported, so reflect cannot set it;
//   - is struct-typed (a tagged embed included), or of a type no single string value fills
//     (bindsFromString);
//   - is promoted through an embedded pointer to an unexported type, which reflect cannot
//     allocate;
//   - is hidden by a shallower field of the same name, or made ambiguous by one at its depth,
//     so reflect does not promote it.
func checkTaggedField(root reflect.Type, field *reflect.StructField, index []int, goPath string, nested bool) error {
	if nested {
		return fmt.Errorf("field %s: param, query and header tags bind only top-level or embedded fields, not fields of a named nested struct", goPath)
	}
	if !field.IsExported() {
		return fmt.Errorf("field %s: a param, query or header tag cannot bind an unexported field", goPath)
	}
	if _, isStruct := bindsAsStruct(field.Type); isStruct {
		return fmt.Errorf("field %s: a param, query or header tag cannot bind a struct-typed field", goPath)
	}
	if !bindsFromString(field) {
		return fmt.Errorf("field %s: type %s cannot bind from a param, query or header value", goPath, field.Type)
	}
	if throughUnexportedPointerEmbed(root, index) {
		return fmt.Errorf("field %s: a param, query or header tag cannot bind through an embedded pointer to an unexported type", goPath)
	}
	if promoted, ok := root.FieldByName(field.Name); !ok || !slices.Equal(promoted.Index, index) {
		return fmt.Errorf("field %s: a param, query or header tag on a field hidden by another field of the same name is never bound", goPath)
	}
	return nil
}

// bindsFromString reports whether the binder can fill field from its values, for every tag it
// carries: through any number of pointers, a kindSetters kind, time.Time or an
// encoding.TextUnmarshaler (setSpecialType); a query or header tag also takes a string-kind
// slice, split by the slice binders.
func bindsFromString(field *reflect.StructField) bool {
	if isStringSliceType(field.Type) && field.Tag.Get("param") == "" {
		return true
	}
	t := field.Type
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if _, ok := kindSetters[t.Kind()]; ok {
		return true
	}
	return t == timeType || reflect.PointerTo(t).Implements(textUnmarshalerType)
}

// appendFieldBindings appends field's plan entries: param, then query, then header.
func appendFieldBindings(plan []boundField, field *reflect.StructField) []boundField {
	if paramName := field.Tag.Get("param"); paramName != "" {
		plan = append(plan, boundField{index: field.Index, source: bindSourceParam, name: paramName})
	}
	if queryName := field.Tag.Get("query"); queryName != "" {
		plan = append(plan, boundField{
			index: field.Index, source: bindSourceQuery, name: queryName, isSlice: isStringSliceType(field.Type),
		})
	}
	if headerName := field.Tag.Get("header"); headerName != "" {
		plan = append(plan, boundField{
			index: field.Index, source: bindSourceHeader, name: headerName, isSlice: isStringSliceType(field.Type),
		})
	}
	return plan
}

// hasBindingTag reports whether field carries a param, query or header tag.
func hasBindingTag(field *reflect.StructField) bool {
	return field.Tag.Get("param") != "" || field.Tag.Get("query") != "" || field.Tag.Get("header") != ""
}

// bindsAsStruct returns the struct t names, through at most one pointer, when the binder
// treats it as a struct rather than a value. An encoding.TextUnmarshaler, time.Time included,
// binds as a value (setSpecialType); unlike requestStructType, a type merely convertible to
// time.Time is a struct here, because setFieldValue would refuse it on every request.
func bindsAsStruct(t reflect.Type) (reflect.Type, bool) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t, t.Kind() == reflect.Struct && !reflect.PointerTo(t).Implements(textUnmarshalerType)
}

// throughUnexportedPointerEmbed reports whether the path to the field at index passes through
// an embedded pointer to an unexported type, which reflect cannot allocate.
func throughUnexportedPointerEmbed(t reflect.Type, index []int) bool {
	for i := range len(index) - 1 {
		f := t.FieldByIndex(index[:i+1])
		if f.Anonymous && !f.IsExported() && f.Type.Kind() == reflect.Pointer {
			return true
		}
	}
	return false
}

// requestProcessor orchestrates the complete request processing pipeline:
// allocation, binding, nil validation, and request validation.
type requestProcessor[T any] struct {
	allocator *requestAllocator[T]
	binder    *RequestBinder
	plan      []boundField
	// fieldPathIsSchema is the JSON decode-summary gate for T, decided once here
	// because it depends on T alone. See saferender.FieldPathIsSchema.
	fieldPathIsSchema bool
}

// requestStructType returns the struct a typed handler binds its request into: T itself,
// or T's element when T is a pointer. ok is false for any other kind, a pointer to a
// pointer, and a struct the validator refuses — time.Time and types convertible to it,
// the same test validator.StructCtx applies.
func requestStructType(t reflect.Type) (structType reflect.Type, ok bool) {
	structType = t
	if structType.Kind() == reflect.Pointer {
		structType = structType.Elem()
	}
	return structType, structType.Kind() == reflect.Struct && !structType.ConvertibleTo(timeType)
}

func unbindableRequestTypeReason(t reflect.Type) string {
	return "request type " + t.String() + " must be a struct or a pointer to a struct (time.Time excluded); wrap the value in a struct field"
}

// newRequestProcessor creates a new request processor for type T. The tag-binding plan
// is computed once here (not per request) from T's underlying struct type. It panics
// when T cannot bind, which covers WrapHandler; RegisterHandler refuses such a T earlier,
// naming the route.
func newRequestProcessor[T any](binder *RequestBinder) *requestProcessor[T] {
	allocator := newRequestAllocator[T]()

	// reflect.TypeFor[T]() rather than allocator.elemType: the allocator's field holds T's own
	// type, so the name would read as a second unwrap that does not happen. Both doors judge the
	// same type, and requestStructType does the one unwrap.
	structType, ok := requestStructType(reflect.TypeFor[T]())
	if !ok {
		panic("server: " + unbindableRequestTypeReason(allocator.elemType))
	}
	plan, err := buildBindingPlan(structType)
	if err != nil {
		panic("server: request type " + allocator.elemType.String() + ": " + err.Error())
	}

	return &requestProcessor[T]{
		allocator:         allocator,
		binder:            binder,
		plan:              plan,
		fieldPathIsSchema: saferender.FieldPathIsSchema(structType),
	}
}

// process executes the full request processing pipeline and returns the bound, validated request.
// Returns an API error if any step fails (allocation, binding, nil check, validation).
func (rp *requestProcessor[T]) process(c *echo.Context) (T, IAPIError) {
	var empty T

	request, requestPtr := rp.allocator.allocate()

	// Bind request data from multiple sources (JSON, query, params, headers).
	if bindErr := rp.binder.bindRequestPlanned(c, requestPtr, rp.plan, rp.fieldPathIsSchema); bindErr != nil {
		// An unknown-length body crossing server.bodylimit fails here, mid-read: answer the
		// 413 a known-length one gets from customErrorHandler, not a bad request.
		if isRequestTooLarge(bindErr) {
			const status = http.StatusRequestEntityTooLarge
			return empty, NewBaseAPIError(statusToErrorCode(status), http.StatusText(status), status)
		}
		// SECURITY: bindErr renders request input; only the summary may be echoed.
		return empty, NewBadRequestError("Invalid request data").WithDetails("error", bindSummary(bindErr))
	}

	// For value types, we need to get the bound value back from the pointer
	if !rp.allocator.isPointer {
		// Cannot fail: for value types allocate returned requestPtr = *T, so
		// Elem() is T. Discarding ok keeps the hot path branch-free.
		request, _ = reflect.ValueOf(requestPtr).Elem().Interface().(T)
	}

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
			// SECURITY: a non-ValidationError cause is the validator's own
			// InvalidValidationError shape; it is not audited for request
			// content, so it renders as a fixed phrase.
			_ = vErr.WithDetails("error", unauditedValidationSummary)
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
		processor:   newRequestProcessor[T](binder),
		responder:   newResponseHandler(cfg, log),
		checker:     newContextChecker(),
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
		if refused := refuseUnknownPathTenant(c); refused != nil {
			return hw.dispatchResponse(c, nil, refused)
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

// dispatchResponse logs an unknown-tenant refusal, then picks the response writer based on JOSE / raw configuration.
func (hw *handlerWrapper[T, R]) dispatchResponse(c *echo.Context, response any, apiErr IAPIError) error {
	if unknownTenant, ok := apiErr.(*UnknownTenantError); ok {
		logUnknownTenantRefusal(unknownTenant, c, hw.responder.log, hw.responder.cfg.App.Debug)
	}
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
// It panics when the wrapper is built if T, after removing one pointer level, is not a
// struct the validator accepts (ADR-121).
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

// bindRequestPlanned binds request data using a precomputed binding plan (buildBindingPlan),
// avoiding per-request struct-tag reflection. When plan is empty — the common case, e.g. a
// JSON-body-only struct — only bindJSONBody runs.
//
// Source precedence is load-bearing and tested (TestRequestBinderPrecedence*,
// TestBindingPlanPathWinsOverBodyAtEveryDepth): the param/query/header overlay runs after the
// JSON step for every promoted field. It first zeroes every param-, query- and header-tagged
// field, so such a field holds only its bound value (zero when the source is empty or absent)
// and never the body's. Do not reorder or drop either loop.
func (rb *RequestBinder) bindRequestPlanned(c *echo.Context, target any, plan []boundField, fieldPathIsSchema bool) error {
	if err := rb.bindJSONBody(c, target, fieldPathIsSchema); err != nil {
		return err
	}
	if len(plan) == 0 {
		return nil
	}

	root := reflect.ValueOf(target).Elem()
	// Every param-, query- or header-tagged field takes only its bound value: whatever the body
	// decode wrote there is dropped first, so an empty path segment, an absent or empty query
	// value and an absent header leave it zero.
	for i := range plan {
		planFieldRef{root: root, index: plan[i].index}.clear()
	}
	for i := range plan {
		bf := &plan[i]
		field := planFieldRef{root: root, index: bf.index}

		var err error
		switch bf.source {
		case bindSourceParam:
			err = rb.bindParamValue(c, bf.name, field)
		case bindSourceQuery:
			err = rb.bindQueryValue(c, bf.name, bf.isSlice, field)
		case bindSourceHeader:
			err = rb.bindHeaderValue(c, bf.name, bf.isSlice, field)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// bindJSONBody is the one JSON body decoder for typed handlers. Its invariants:
//   - An empty Content-Type returns nil without reading the body.
//   - One predicate admits a body as JSON: the parsed media type (lowercased, parameters
//     dropped) is application/json or ends in +json. Any other type returns nil unread.
//   - An admitted request only decodes the body through the engine's JSON serializer,
//     unless Content-Length is 0. Path, query and header values never bind here: the
//     plan's overlay binds them after, under every Content-Type, so they win (ADR-173).
//   - Each failure is wrapped once by newJSONBindError, so a body-limit 413 stays
//     reachable for isRequestTooLarge.
//   - The Content-Type header is never rewritten.
//
// fieldPathIsSchema is the decode-summary gate for the request type, decided once
// per route by newRequestProcessor.
func (rb *RequestBinder) bindJSONBody(c *echo.Context, target any, fieldPathIsSchema bool) error {
	req := c.Request()
	ct := req.Header.Get(echo.HeaderContentType)
	if ct == "" {
		return nil
	}

	mt, _, _ := mime.ParseMediaType(ct)
	if mt != echo.MIMEApplicationJSON && !strings.HasSuffix(mt, "+json") {
		return nil
	}

	if req.ContentLength == 0 {
		return nil
	}
	if err := c.Echo().JSONSerializer.Deserialize(c, target); err != nil {
		return newJSONBindError(err, fieldPathIsSchema)
	}
	return nil
}

// planFieldRef addresses one plan entry's field under the request struct. The field is
// resolved only when a value binds, so a nil pointer embed on its path stays nil otherwise.
type planFieldRef struct {
	root  reflect.Value
	index []int
}

// resolve returns the field, allocating each nil pointer embed on the way down.
func (f planFieldRef) resolve() reflect.Value {
	v := f.root
	for _, x := range f.index {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	return v
}

// clear zeroes the field, stopping at a nil pointer embed on the way down, which holds nothing.
func (f planFieldRef) clear() {
	v := f.root
	for _, x := range f.index {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	v.SetZero()
}

// bindParamValue binds one path parameter into field for a bindRequestPlanned entry.
func (rb *RequestBinder) bindParamValue(c *echo.Context, paramName string, field planFieldRef) error {
	value := c.Param(paramName) //nolint:forbidigo // binding door: copies the value into the handler's request struct, never into a sink
	if value != "" {
		if err := setFieldValue(field.resolve(), value); err != nil {
			return newFieldBindError(bindSourceParam, paramName, err)
		}
	}
	return nil
}

// bindQueryValue binds one query parameter into field for a bindRequestPlanned entry.
func (rb *RequestBinder) bindQueryValue(c *echo.Context, queryName string, isSlice bool, field planFieldRef) error {
	// Support []string binding from repeated query parameters
	if isSlice {
		return rb.bindQueryStringSlice(c, queryName, field)
	}

	value := c.QueryParam(queryName) //nolint:forbidigo // binding door: copies the value into the handler's request struct, never into a sink
	if value != "" {
		if err := setFieldValue(field.resolve(), value); err != nil {
			return newFieldBindError(bindSourceQuery, queryName, err)
		}
	}
	return nil
}

// bindQueryStringSlice binds repeated query parameters to a []string field
func (rb *RequestBinder) bindQueryStringSlice(c *echo.Context, queryName string, field planFieldRef) error {
	values := c.QueryParams()[queryName] //nolint:forbidigo // binding door: copies the value into the handler's request struct, never into a sink
	if len(values) > 0 {
		fieldValue := field.resolve()
		slice := reflect.MakeSlice(fieldValue.Type(), len(values), len(values))
		for i, v := range values {
			slice.Index(i).SetString(v)
		}
		fieldValue.Set(slice)
	}
	return nil
}

// bindHeaderValue binds one header into field for a bindRequestPlanned entry.
func (rb *RequestBinder) bindHeaderValue(c *echo.Context, headerName string, isSlice bool, field planFieldRef) error {
	values := c.Request().Header.Values(headerName)
	if len(values) == 0 {
		return nil
	}

	// Support comma-separated list for []string headers
	fieldValue := field.resolve()
	if isSlice {
		return rb.bindHeaderStringSlice(values, fieldValue)
	}

	if err := setFieldValue(fieldValue, values[0]); err != nil {
		return newFieldBindError(bindSourceHeader, headerName, err)
	}
	return nil
}

// bindHeaderStringSlice binds comma-separated header values to a string-kind slice field.
// Parts are collected first so the slice can be built with SetString (which accepts any
// String-Kind element type) instead of reflect.Append, which rejects a plain string for a
// named element type like `type Scope string`.
func (rb *RequestBinder) bindHeaderStringSlice(values []string, fieldValue reflect.Value) error {
	parts := make([]string, 0, 8)
	for _, raw := range values {
		for _, p := range strings.Split(raw, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				parts = append(parts, p)
			}
		}
	}
	slice := reflect.MakeSlice(fieldValue.Type(), len(parts), len(parts))
	for i, p := range parts {
		slice.Index(i).SetString(p)
	}
	fieldValue.Set(slice)
	return nil
}

// isStringSliceType reports whether t is a slice of a string kind, which query and header
// entries bind from repeated or comma-separated values.
func isStringSliceType(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String
}

type valueSetter func(reflect.Value, string) error

var (
	timeType            = reflect.TypeOf(time.Time{})
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	kindSetters         = map[reflect.Kind]valueSetter{
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
		// String-kind slices bind through bindQueryValue/bindHeaderValue's isSlice branch, not here
		return fmt.Errorf("unsupported assignment to slice from string for kind: %s", kind)
	}

	return fmt.Errorf("unsupported field type: %s", kind)
}

// setSpecialType handles the types that bind as values though their kind may not: time.Time
// (parseTime's layouts) and an addressable encoding.TextUnmarshaler whose kind has no
// kindSetters entry, so a string-, int- or bool-kind type keeps its plain setter.
func setSpecialType(fieldValue reflect.Value, value string) (bool, error) {
	if fieldValue.Type() == timeType {
		t, err := parseTime(value)
		if err != nil {
			return true, err
		}
		fieldValue.Set(reflect.ValueOf(t))
		return true, nil
	}
	if _, hasSetter := kindSetters[fieldValue.Kind()]; !hasSetter && fieldValue.CanAddr() {
		if u, ok := fieldValue.Addr().Interface().(encoding.TextUnmarshaler); ok {
			return true, u.UnmarshalText([]byte(value))
		}
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
	intVal, err := strconv.ParseInt(value, 10, fieldValue.Type().Bits())
	if err != nil {
		return err
	}
	fieldValue.SetInt(intVal)
	return nil
}

// setUnsignedIntValue sets an unsigned integer field value.
func setUnsignedIntValue(fieldValue reflect.Value, value string) error {
	uintVal, err := strconv.ParseUint(value, 10, fieldValue.Type().Bits())
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
				logMetaKeyCollision(c, log, k)
			}
			continue
		}
		out[k] = v
	}
	out[fieldTimestamp] = time.Now().UTC().Format(time.RFC3339)
	out[fieldTraceID] = getTraceID(c)
	return out
}

// logMetaKeyCollision writes the meta-key collision WARN through log, correlated
// (correlateLine) and NOT bound to the request context: a bound WARN would fire the access
// logger's severity hook and raise a 2xx access line to WARN, so the access line stays
// status-derived. SAFETY: c.Request() is nil only after SetRequest(nil) or on a hand-built
// echo.Context; the WARN then still ships, without the correlation, instead of panicking.
func logMetaKeyCollision(c *echo.Context, log logger.Logger, key string) {
	ev, ok := openWarn(log)
	if !ok {
		return
	}
	ev = ev.Str("key", key).Str("route", c.Path())
	if c.Request() != nil {
		lc := correlateLine(c)
		ev = lc.stamp(ev)
	}
	ev.Msg("envelope meta key collides with framework-managed key; handler value dropped")
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

	errorResp.Details = devDetails(apiErr, cfg)

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
// SECURITY: devDetails is the gate on every response detail map — the enveloped,
// the raw and the JOSE post-trust renderers all funnel through it — and it
// requires app.debug AND a development environment — the posture #1161 set for
// the 500 path, now applied to every status (ADR-084). Response bodies never pass
// through the logger's SensitiveDataFilter, so a detail entry that turns out to
// carry request text is exposed to the caller verbatim; the framework's own
// entries are payload-free by construction, and the gate bounds a handler's.
// The pre-trust JOSE plaintext envelope is not a fourth renderer: joseAPIError
// carries no details by construction (jose.go, Details returns nil).
func devDetails(apiErr IAPIError, cfg *config.Config) map[string]any {
	if !cfg.App.Debug || !cfg.App.IsDevelopment() {
		return nil
	}

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
			// SAFETY: tolerate a nil c.Request() (SetRequest(nil), a hand-built echo.Context).
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

	payload.Details = devDetails(apiErr, cfg)

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
	// SAFETY: c.Response() is nil only after SetResponse(nil) or on a hand-built
	// echo.Context; check for nil to prevent a panic.
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
// It propagates the inbound header when it is valid, otherwise generates a new one.
//
// The inbound read is validated here rather than inherited from the context: this
// reads c.Request().Header directly, so the ctx guard in enrichTraceContext does
// not reach it, and every one of this function's call sites writes the result
// onto the wire. Reflecting an unvouched value would hand a caller a response
// header — and, through the access log's response-header read, a log field —
// carrying their own bytes (ADR-070).
func ensureTraceParentHeader(c *echo.Context) {
	// SAFETY: Check if response is still valid (may be nil after timeout)
	resp := c.Response()
	if resp == nil {
		return
	}
	// The response read is validated too, symmetrically with the access-log reader
	// and with validateRequestID's two seams: an earlier middleware may have
	// reflected the inbound value verbatim, and an unvouched value already on the
	// response is the one this function would leave on the wire.
	if validateTraceParent(resp.Header().Get(gobrickstrace.HeaderTraceParent)) != "" {
		return
	}
	// Prefer inbound header
	if tp := validateTraceParent(c.Request().Header.Get(gobrickstrace.HeaderTraceParent)); tp != "" {
		resp.Header().Set(gobrickstrace.HeaderTraceParent, tp)
		return
	}
	resp.Header().Set(gobrickstrace.HeaderTraceParent, gobrickstrace.GenerateTraceParent())
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

// NewHandlerRegistry creates a new handler registry with the given config.
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
// It panics when T, after removing one pointer level, is not a struct the validator accepts.
func RegisterHandler[T any, R any](
	hr *HandlerRegistry,
	r RouteRegistrar,
	method, path string,
	handler HandlerFunc[T, R],
	opts ...RouteOption,
) {
	reqType := reflect.TypeFor[T]()
	var respType R

	// Determine final path after registrar adjustments (e.g. base path prefixes)
	fullPath := r.FullPath(path)

	structType, ok := requestStructType(reqType)
	if !ok {
		panic("server: handler registration failed for " + method + " " + fullPath + ": " + unbindableRequestTypeReason(reqType))
	}
	// WrapHandler below builds the plan again; this first build only names the route in the panic.
	if _, err := buildBindingPlan(structType); err != nil {
		panic("server: handler registration failed for " + method + " " + fullPath + ": request type " + reqType.String() + ": " + err.Error())
	}

	// The route's package is its handler function's, so the GET/POST helpers and a direct
	// RegisterHandler call give one answer: admission decides who may register under a framework
	// route root by it (ADR-168). Set before the options, so an option reads it, and stamped again
	// after them, so no RouteOption forges or clears it. A registrar embedding RouteRegistrar takes
	// the fallback below, whose Add admits the server closure, so admission sees server there.
	pkg := handlerPackage(handler)

	// Create descriptor with type information
	descriptor := RouteDescriptor{
		Method:       method,
		Path:         fullPath,
		HandlerID:    formatHandlerID(method, fullPath),
		RequestType:  reqType,
		ResponseType: reflect.TypeOf(respType),
		Package:      pkg,
		HandlerName:  extractHandlerName(handler),
	}

	for _, opt := range opts {
		opt(&descriptor)
	}
	descriptor.Package = pkg

	// Scan request/response types for jose: tags and resolve every kid against the
	// registry's resolver. Panics at startup on any failure (Fail Fast principle).
	scanRouteJOSE(&descriptor, hr.joseResolver)

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
	// Hot path: register the echo handler directly through routeGroup.addEcho so the typed
	// handler path adds zero per-request adapter overhead (ADR-026); see routeAdmission.admit.
	// The fallback adapts for non-routeGroup registrars (test fakes) and describes the route
	// itself; it round-trips through the unexported escape hatch and never executes on the
	// framework's real registrar.
	if rg, ok := r.(*routeGroup); ok {
		rg.addEcho(method, path, wrappedHandler, &descriptor)
	} else {
		DefaultRouteRegistry.Register(&descriptor)
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
// stack frames above getCallerPackage to inspect: Group and Use pass 2 (getCallerPackage →
// Group/Use → module). A route is attributed from its handler instead (handlerPackage).
func getCallerPackage(skip int) string {
	pc, _, _, ok := runtime.Caller(skip)
	if !ok {
		return ""
	}
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return ""
	}
	return funcPackage(fn.Name())
}

// handlerPackage returns the package path of the function a route registers: the package that
// declares it, a method value's receiver package, or a closure's enclosing function's package.
// Unlike a caller frame it does not depend on who called the registration (ADR-168). A nil
// or non-function handler has none.
func handlerPackage(handler any) string {
	v := reflect.ValueOf(handler)
	if v.Kind() != reflect.Func || v.IsNil() {
		return ""
	}
	fn := runtime.FuncForPC(v.Pointer())
	if fn == nil {
		return ""
	}
	return funcPackage(fn.Name())
}

// funcPackage extracts the package path from a runtime function name
// (example.com/svc/orders.(*Handler).get-fm, example.com/svc/orders.Register.func1,
// example.com/svc/orders.get[...]): everything before the first dot after the last slash. The
// runtime escapes a dot in a path's last element (%2e), so that dot ends the path. A type
// argument list is cut first, since it may name other packages.
func funcPackage(name string) string {
	if bracket := strings.IndexByte(name, '['); bracket >= 0 {
		name = name[:bracket]
	}
	lastSlash := strings.LastIndex(name, "/")
	if dot := strings.IndexByte(name[lastSlash+1:], '.'); dot >= 0 {
		return name[:lastSlash+1+dot]
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
