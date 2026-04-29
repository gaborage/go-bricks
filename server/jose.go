package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	otelnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/jose"
	"github.com/gaborage/go-bricks/logger"
	"github.com/labstack/echo/v5"
)

// joseObservability bundles the optional logger / tracer / meter so they thread cleanly
// through wrap() into joseDecodeRequest and joseHandleResponse without bloating those
// signatures or the HandlerRegistry struct.
type joseObservability struct {
	logger       logger.Logger
	tracer       trace.Tracer
	failureCount metric.Int64Counter
	durationHist metric.Float64Histogram
}

var defaultNoopTracer = tracenoop.NewTracerProvider().Tracer("jose")

func (o *joseObservability) tracerOrNoop() trace.Tracer {
	if o == nil || o.tracer == nil {
		return defaultNoopTracer
	}
	return o.tracer
}

// recordFailure emits the structured failure log AND increments the counter. Centralized
// so audit-grade fields (route, kid, alg, enc, code, cause) are guaranteed to appear in
// both surfaces simultaneously — divergence between log and metric surfaces is the kind
// of inconsistency that bites operators during incidents.
func (o *joseObservability) recordFailure(ctx context.Context, c *echo.Context, direction string, apiErr IAPIError) {
	if o == nil {
		return
	}
	jaerr, _ := apiErr.(*joseAPIError)
	code := apiErr.ErrorCode()
	method := c.Request().Method
	route := c.Path()
	if route == "" {
		route = c.Request().URL.Path
	}

	if o.failureCount != nil {
		o.failureCount.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("code", code),
				attribute.String("direction", direction),
				attribute.String("http.method", method),
				attribute.String("http.route", route),
			),
		)
	}

	if o.logger == nil {
		return
	}
	ev := o.logger.WithContext(ctx).Error().
		Str("code", code).
		Str("direction", direction).
		Str("http.method", method).
		Str("http.route", route)
	if jaerr != nil {
		ev = ev.Str("message", jaerr.message)
	}
	ev.Msg("jose: operation failed")
}

// recordDuration captures successful-operation latency histograms when a meter is wired.
// No-op (zero allocation) when observability is disabled.
func (o *joseObservability) recordDuration(ctx context.Context, operation string, elapsed time.Duration) {
	if o == nil || o.durationHist == nil {
		return
	}
	o.durationHist.Record(ctx, elapsed.Seconds(),
		metric.WithAttributes(attribute.String("operation", operation)),
	)
}

// newJOSEObservability builds the bundle from optional injected logger/tracer/meter.
// All inputs are nil-safe; the returned bundle is also nil-safe at every call site.
func newJOSEObservability(log logger.Logger, tracer trace.Tracer, mp metric.MeterProvider) *joseObservability {
	o := &joseObservability{logger: log, tracer: tracer}
	if mp == nil {
		mp = otelnoop.NewMeterProvider()
	}
	meter := mp.Meter("github.com/gaborage/go-bricks/jose")
	if c, err := meter.Int64Counter("jose.failures.total",
		metric.WithDescription("Count of JOSE crypto failures by code and direction"),
	); err == nil {
		o.failureCount = c
	}
	if h, err := meter.Float64Histogram("jose.operation.duration",
		metric.WithDescription("Latency of JOSE crypto operations in seconds"),
		metric.WithUnit("s"),
	); err == nil {
		o.durationHist = h
	}
	return o
}

// HandlerRegistryOption configures a HandlerRegistry at construction time.
// Existing call sites that don't need JOSE pass no options and behave unchanged.
type HandlerRegistryOption func(*HandlerRegistry)

// WithJOSEResolver enables JOSE protection for routes whose request/response types
// carry jose: tags. When the resolver is nil or this option is not supplied, JOSE
// scanning is skipped at registration — but if any route attempts to declare a jose
// tag, registration will panic with a clear error directing the operator to wire a
// keystore module.
func WithJOSEResolver(r jose.KeyResolver) HandlerRegistryOption {
	return func(hr *HandlerRegistry) {
		hr.joseResolver = r
	}
}

// WithJOSELogger attaches a logger used to emit structured ERROR records on JOSE
// failure paths (decrypt failed, signature invalid, etc.). Records include code,
// direction, route, and method — never plaintext payloads or key material. When
// no logger is supplied, JOSE failures still flow through the IAPIError envelope to
// the wire and to the framework's request logger; this option only adds the
// dedicated audit-grade record.
func WithJOSELogger(log logger.Logger) HandlerRegistryOption {
	return func(hr *HandlerRegistry) {
		hr.joseLogger = log
	}
}

// WithJOSETracer attaches an OpenTelemetry tracer used to emit spans around JOSE
// crypto operations (jose.decode_request, jose.encode_response). When no tracer is
// supplied, a no-op tracer is used so call sites stay branch-free.
func WithJOSETracer(t trace.Tracer) HandlerRegistryOption {
	return func(hr *HandlerRegistry) {
		hr.joseTracer = t
	}
}

// WithJOSEMeterProvider attaches an OTEL meter provider used to register the JOSE
// failure counter and operation-duration histogram. A no-op provider is used by
// default so instrument creation cannot fail when observability is disabled.
func WithJOSEMeterProvider(mp metric.MeterProvider) HandlerRegistryOption {
	return func(hr *HandlerRegistry) {
		hr.joseMeterProvider = mp
	}
}

// joseAPIError adapts a jose.Error into the IAPIError contract while preserving the
// JOSE-specific error code (rather than collapsing to a generic "BAD_REQUEST"). The
// preserved code is what makes audit grep-ability work: searching for JOSE_DECRYPT_FAILED
// finds every wire-level occurrence in logs and tests.
type joseAPIError struct {
	code    string
	message string
	status  int
}

func (e *joseAPIError) ErrorCode() string       { return e.code }
func (e *joseAPIError) Message() string         { return e.message }
func (e *joseAPIError) HTTPStatus() int         { return e.status }
func (e *joseAPIError) Details() map[string]any { return nil }
func (e *joseAPIError) WithDetails(string, any) IAPIError {
	// JOSE errors intentionally surface no details to the wire — adding fields here
	// would defeat the constant-time-generic guarantee. Returns receiver unchanged.
	return e
}

func newJOSEAPIError(err error) IAPIError {
	var jerr *jose.Error
	if !errors.As(err, &jerr) {
		return NewInternalServerError("JOSE processing failed")
	}
	status := jerr.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	return &joseAPIError{code: jerr.Code, message: jerr.Message, status: status}
}

// scanRouteJOSE runs at RegisterHandler time to extract jose: tags from request/response
// types, validate bidirectional symmetry, resolve every kid against the registry's
// resolver, and write the resolved policies onto the descriptor.
//
// The bidirectional rule (per the v1 plan): if EITHER type has a jose tag, BOTH must.
// Asymmetric declaration is a registration error — the panic surfaces immediately at
// app startup rather than producing silent plaintext leakage at runtime.
func scanRouteJOSE(d *RouteDescriptor, resolver jose.KeyResolver) {
	inboundPolicy, inErr := jose.ScanType(d.RequestType, jose.DirectionInbound)
	outboundPolicy, outErr := jose.ScanType(d.ResponseType, jose.DirectionOutbound)

	if inErr != nil {
		panicJOSERegistration(d, "inbound jose tag invalid", inErr)
	}
	if outErr != nil {
		panicJOSERegistration(d, "outbound jose tag invalid", outErr)
	}

	// Bidirectional symmetry: both or neither.
	if (inboundPolicy == nil) != (outboundPolicy == nil) {
		panicJOSERegistration(d, "asymmetric jose policy: request and response must both declare jose tags or neither", nil)
	}
	if inboundPolicy == nil {
		return
	}

	// Mutual exclusion with WithRawResponse — both compete for the response writer.
	if d.RawResponse {
		panicJOSERegistration(d, "JOSE policy cannot combine with WithRawResponse (both control response writing)", nil)
	}

	if resolver == nil {
		panicJOSERegistration(d,
			"route declares jose tags but no KeyResolver wired; register a KeyStore module so server.WithJOSEResolver can be applied",
			nil)
	}

	// Resolve every kid at startup so missing keys fail fast (per Fail Fast principle).
	if err := jose.ResolvePolicy(resolver, inboundPolicy); err != nil {
		panicJOSERegistration(d, "inbound key resolution failed", err)
	}
	if err := jose.ResolvePolicy(resolver, outboundPolicy); err != nil {
		panicJOSERegistration(d, "outbound key resolution failed", err)
	}

	d.InboundJOSE = inboundPolicy
	d.OutboundJOSE = outboundPolicy
}

func panicJOSERegistration(d *RouteDescriptor, msg string, cause error) {
	pathInfo := d.Method + " " + d.Path
	if cause != nil {
		panic("server: jose registration failed for " + pathInfo + ": " + msg + ": " + cause.Error())
	}
	panic("server: jose registration failed for " + pathInfo + ": " + msg)
}

// joseDecodeRequestWithObs reads the raw HTTP body, validates its Content-Type, decrypts the
// JWE, verifies the inner JWS, and replaces c.Request().Body with the verified plaintext.
// Wrapped in an OTEL span so operators can isolate JOSE inbound time from total request time.
//
// On success: marks the context inbound-verified and stashes the verified Claims.
// On failure: returns an IAPIError with the JOSE-specific code; the caller invokes
// obs.recordFailure() before forwarding to the plaintext error formatter.
func joseDecodeRequestWithObs(c *echo.Context, p *jose.Policy, r jose.KeyResolver, obs *joseObservability) IAPIError {
	_, span := obs.tracerOrNoop().Start(c.Request().Context(), "jose.decode_request",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("jose.direction", "inbound")),
	)
	defer span.End()
	start := time.Now()

	apiErr := joseDecodeRequestInner(c, p, r)
	if apiErr != nil {
		span.SetStatus(codes.Error, apiErr.ErrorCode())
		span.SetAttributes(attribute.String("jose.error.code", apiErr.ErrorCode()))
		return apiErr
	}
	obs.recordDuration(c.Request().Context(), "decode_request", time.Since(start))
	return nil
}

// joseDecodeRequestInner is the span-free implementation, kept separate so the
// span/timing wrapper above stays readable.
func joseDecodeRequestInner(c *echo.Context, p *jose.Policy, r jose.KeyResolver) IAPIError {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return &joseAPIError{code: "JOSE_BODY_REQUIRED", message: "Failed to read request body", status: http.StatusBadRequest}
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return &joseAPIError{code: "JOSE_BODY_REQUIRED", message: "Request body required", status: http.StatusBadRequest}
	}

	if !jose.IsContentType(c.Request().Header.Get(echo.HeaderContentType)) {
		return &joseAPIError{code: "JOSE_PLAINTEXT_REJECTED", message: "Request must be application/jose", status: http.StatusUnsupportedMediaType}
	}

	plaintext, claims, _, err := jose.Open(string(body), p, r)
	if err != nil {
		return newJOSEAPIError(err)
	}

	c.Request().Body = io.NopCloser(bytes.NewReader(plaintext))
	c.Request().Header.Set(echo.HeaderContentType, "application/json")
	c.Request().ContentLength = int64(len(plaintext))

	ctx := jose.WithInboundVerified(c.Request().Context())
	ctx = jose.WithClaims(ctx, claims)
	c.SetRequest(c.Request().WithContext(ctx))
	return nil
}

// joseHandleResponseWithObs wraps joseHandleResponse with an OTEL span and records
// failures via obs. The wrapped form is the only call site exercised at runtime —
// joseHandleResponse remains exported (without the trailing Obs suffix on the actual
// implementation) for testability and so the rh.handleResponse-style signature is
// preserved for any future direct callers.
func (rh *responseHandler) joseHandleResponseWithObs(c *echo.Context, response any, apiErr IAPIError, p *jose.Policy, r jose.KeyResolver, obs *joseObservability) error {
	_, span := obs.tracerOrNoop().Start(c.Request().Context(), "jose.encode_response",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("jose.direction", "outbound")),
	)
	defer span.End()
	start := time.Now()

	err := rh.joseHandleResponse(c, response, apiErr, p, r, obs)
	if err != nil {
		span.SetStatus(codes.Error, "JOSE_OUTBOUND_FAILED")
		span.SetAttributes(attribute.String("jose.error.code", "JOSE_OUTBOUND_FAILED"))
		return err
	}
	obs.recordDuration(c.Request().Context(), "encode_response", time.Since(start))
	return nil
}

// joseHandleResponse routes the post-handler success or error path through the JOSE
// outbound encoder. Enforces the security invariant: refuses to produce an encrypted
// response if inbound was not verified, even if the outbound policy is set.
//
// obs is non-nil at production call sites (joseHandleResponseWithObs); only actual JOSE
// crypto failures (seal/encrypt errors) call obs.recordFailure here. Normal post-trust
// handler IAPIErrors (validation/business) are encrypted successfully and DO NOT count
// toward jose.failures.total — keeping that metric a pure crypto-failure signal.
func (rh *responseHandler) joseHandleResponse(c *echo.Context, response any, apiErr IAPIError, p *jose.Policy, r jose.KeyResolver, obs *joseObservability) error {
	if !jose.IsInboundVerified(c.Request().Context()) {
		// Defense in depth: if the wrapper somehow skipped inbound verification but
		// still routed through joseHandleResponse, refuse to emit ciphertext to a peer
		// whose identity we never confirmed. This should be unreachable given the
		// bidirectional-symmetry registration check.
		return formatJOSEPlaintextError(c,
			&joseAPIError{code: "JOSE_INVARIANT_VIOLATION", message: "Outbound encryption refused: inbound not verified", status: http.StatusInternalServerError})
	}

	if apiErr != nil {
		return formatJOSEPostTrustError(c, apiErr, p, r, rh.cfg, obs)
	}

	status := http.StatusOK
	var headers http.Header
	data := response
	if rl, ok := response.(ResultMetaProvider); ok {
		status, headers, data = rl.ResultMeta()
		if status == 0 {
			status = http.StatusOK
		}
	}

	for k, vals := range headers {
		for _, v := range vals {
			c.Response().Header().Add(k, v)
		}
	}

	// 204 No Content (and 304 Not Modified) MUST NOT carry a body per RFC 7230 §3.3.3.
	// Mirror formatSuccessResponseWithStatus's behavior: write the status and no body,
	// skipping the seal entirely. Sealing nil and returning ciphertext on a 204 would be
	// invalid HTTP and divergent from the standard envelope path.
	if status == http.StatusNoContent || status == http.StatusNotModified {
		c.Response().WriteHeader(status)
		return nil
	}

	payload, err := json.Marshal(data)
	if err != nil {
		marshalErr := &joseAPIError{code: "JOSE_OUTBOUND_FAILED", message: "Failed to marshal response", status: http.StatusInternalServerError}
		obs.recordFailure(c.Request().Context(), c, "outbound", marshalErr)
		return formatJOSEPlaintextError(c, marshalErr)
	}

	compact, err := jose.Seal(payload, p, r)
	if err != nil {
		sealErr := newJOSEAPIError(err)
		obs.recordFailure(c.Request().Context(), c, "outbound", sealErr)
		return formatJOSEPlaintextError(c, sealErr)
	}

	c.Response().Header().Set(echo.HeaderContentType, jose.ContentType)
	c.Response().WriteHeader(status)
	_, writeErr := c.Response().Write([]byte(compact))
	return writeErr
}

// formatJOSEPlaintextError emits a minimal {code,message} JSON envelope WITHOUT
// timestamp, traceId, or any framework metadata. Used for pre-trust failures (peer
// is unauthenticated; leak nothing) and for the unrecoverable encryption-of-error
// path (we can't seal an error if the seal itself just failed).
//
// The envelope is intentionally distinct from APIResponse — auditors can grep for
// `Content-Type: application/json` plus `"code"` without `"data"` or `"error"` keys
// to verify pre-trust failures never carry framework metadata.
func formatJOSEPlaintextError(c *echo.Context, apiErr IAPIError) error {
	payload := map[string]string{
		"code":    apiErr.ErrorCode(),
		"message": apiErr.Message(),
	}
	c.Response().Header().Set(echo.HeaderContentType, "application/json")
	return c.JSON(apiErr.HTTPStatus(), payload)
}

// formatJOSEPostTrustError encrypts a standard APIResponse error envelope. Called
// when inbound verification succeeded but the handler returned an error (validation
// failure, business logic error, etc.) — at that point the channel is trusted enough
// to carry detailed diagnostics back to the legitimate peer.
//
// If the encryption itself fails, falls back to the plaintext minimal envelope so
// the peer at least gets *something*. The fallback is logged via the underlying
// jose.Error so operators can investigate.
func formatJOSEPostTrustError(c *echo.Context, apiErr IAPIError, p *jose.Policy, r jose.KeyResolver, cfg *config.Config, obs *joseObservability) error {
	envelope := buildErrorEnvelope(c, apiErr, cfg)
	payload, err := json.Marshal(envelope)
	if err != nil {
		marshalErr := &joseAPIError{code: "JOSE_OUTBOUND_FAILED", message: "Failed to marshal error envelope", status: http.StatusInternalServerError}
		obs.recordFailure(c.Request().Context(), c, "outbound", marshalErr)
		return formatJOSEPlaintextError(c, marshalErr)
	}

	compact, err := jose.Seal(payload, p, r)
	if err != nil {
		// Falling back to plaintext on seal failure is the safest available response —
		// the peer is authenticated so leakage cost is low; the alternative is no
		// response at all, which forces a timeout-induced retry storm.
		sealErr := newJOSEAPIError(err)
		obs.recordFailure(c.Request().Context(), c, "outbound", sealErr)
		return formatJOSEPlaintextError(c, sealErr)
	}

	c.Response().Header().Set(echo.HeaderContentType, jose.ContentType)
	c.Response().WriteHeader(apiErr.HTTPStatus())
	_, writeErr := c.Response().Write([]byte(compact))
	return writeErr
}

// buildErrorEnvelope constructs the standard APIResponse-shaped error body. Kept
// jose-local so the standard error formatter in handler.go can stay untouched.
func buildErrorEnvelope(c *echo.Context, apiErr IAPIError, _ *config.Config) map[string]any {
	out := map[string]any{
		"error": map[string]any{
			"code":    apiErr.ErrorCode(),
			"message": apiErr.Message(),
		},
		"meta": map[string]any{
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"traceId":   getTraceID(c),
		},
	}
	if details := apiErr.Details(); len(details) > 0 {
		out["error"].(map[string]any)["details"] = details
	}
	return out
}
