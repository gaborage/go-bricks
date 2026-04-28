package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/jose"
	"github.com/labstack/echo/v5"
)

// joseContentType is the IANA-registered media type for compact JOSE serializations.
const joseContentType = "application/jose"

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

// joseDecodeRequest reads the raw HTTP body, validates its Content-Type, decrypts the
// JWE, verifies the inner JWS, and replaces c.Request().Body with the verified
// plaintext so the existing request binder can process it as ordinary JSON.
//
// On success: marks the context inbound-verified and stashes the verified Claims so
// app code can inspect iat/exp/jti.
//
// On failure: returns an IAPIError whose code is the JOSE-specific code (e.g.,
// JOSE_DECRYPT_FAILED) so the pre-trust error formatter can emit it on the wire
// while the wrapper logs the underlying jose.Error.Cause server-side.
func joseDecodeRequest(c *echo.Context, p *jose.Policy, r jose.KeyResolver) IAPIError {
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return &joseAPIError{code: "JOSE_BODY_REQUIRED", message: "Failed to read request body", status: http.StatusBadRequest}
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return &joseAPIError{code: "JOSE_BODY_REQUIRED", message: "Request body required", status: http.StatusBadRequest}
	}

	if !isJOSEContentType(c.Request().Header.Get(echo.HeaderContentType)) {
		return &joseAPIError{code: "JOSE_PLAINTEXT_REJECTED", message: "Request must be application/jose", status: http.StatusUnsupportedMediaType}
	}

	plaintext, claims, _, err := jose.Open(string(body), p, r)
	if err != nil {
		return newJOSEAPIError(err)
	}

	// Replace the body so the standard JSON binder can run unmodified. The Content-Type
	// is rewritten to application/json so binder content-negotiation accepts it; the
	// original Content-Length is updated to match the plaintext length.
	c.Request().Body = io.NopCloser(bytes.NewReader(plaintext))
	c.Request().Header.Set(echo.HeaderContentType, "application/json")
	c.Request().ContentLength = int64(len(plaintext))

	ctx := jose.WithInboundVerified(c.Request().Context())
	ctx = jose.WithClaims(ctx, claims)
	c.SetRequest(c.Request().WithContext(ctx))
	return nil
}

func isJOSEContentType(ct string) bool {
	if ct == "" {
		return false
	}
	// Match application/jose with optional parameters (charset, etc.) and case-insensitively
	// per RFC 7231 §3.1.1.1.
	idx := strings.Index(ct, ";")
	main := ct
	if idx >= 0 {
		main = ct[:idx]
	}
	return strings.EqualFold(strings.TrimSpace(main), joseContentType)
}

// joseHandleResponse routes the post-handler success or error path through the JOSE
// outbound encoder. Enforces the security invariant: refuses to produce an encrypted
// response if inbound was not verified, even if the outbound policy is set.
func (rh *responseHandler) joseHandleResponse(c *echo.Context, response any, apiErr IAPIError, p *jose.Policy, r jose.KeyResolver) error {
	if !jose.IsInboundVerified(c.Request().Context()) {
		// Defense in depth: if the wrapper somehow skipped inbound verification but
		// still routed through joseHandleResponse, refuse to emit ciphertext to a peer
		// whose identity we never confirmed. This should be unreachable given the
		// bidirectional-symmetry registration check.
		return formatJOSEPlaintextError(c,
			&joseAPIError{code: "JOSE_INVARIANT_VIOLATION", message: "Outbound encryption refused: inbound not verified", status: http.StatusInternalServerError})
	}

	if apiErr != nil {
		return formatJOSEPostTrustError(c, apiErr, p, r, rh.cfg)
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

	payload, err := json.Marshal(data)
	if err != nil {
		return formatJOSEPlaintextError(c,
			&joseAPIError{code: "JOSE_OUTBOUND_FAILED", message: "Failed to marshal response", status: http.StatusInternalServerError})
	}

	compact, err := jose.Seal(payload, p, r)
	if err != nil {
		return formatJOSEPlaintextError(c, newJOSEAPIError(err))
	}

	for k, vals := range headers {
		for _, v := range vals {
			c.Response().Header().Add(k, v)
		}
	}
	c.Response().Header().Set(echo.HeaderContentType, joseContentType)
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
func formatJOSEPostTrustError(c *echo.Context, apiErr IAPIError, p *jose.Policy, r jose.KeyResolver, cfg *config.Config) error {
	envelope := buildErrorEnvelope(c, apiErr, cfg)
	payload, err := json.Marshal(envelope)
	if err != nil {
		return formatJOSEPlaintextError(c,
			&joseAPIError{code: "JOSE_OUTBOUND_FAILED", message: "Failed to marshal error envelope", status: http.StatusInternalServerError})
	}

	compact, err := jose.Seal(payload, p, r)
	if err != nil {
		// Falling back to plaintext on seal failure is the safest available response —
		// the peer is authenticated so leakage cost is low; the alternative is no
		// response at all, which forces a timeout-induced retry storm.
		return formatJOSEPlaintextError(c, newJOSEAPIError(err))
	}

	c.Response().Header().Set(echo.HeaderContentType, joseContentType)
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
			"traceId":   c.Response().Header().Get(echo.HeaderXRequestID),
		},
	}
	if details := apiErr.Details(); len(details) > 0 {
		out["error"].(map[string]any)["details"] = details
	}
	return out
}

// reflectTypeName returns a human-readable type name for panic messages, including
// the package path so registration errors are unambiguous.
func reflectTypeName(t reflect.Type) string {
	if t == nil {
		return "<nil>"
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.PkgPath() == "" {
		return t.Name()
	}
	return t.PkgPath() + "." + t.Name()
}

// _ silences unused-symbol warnings during incremental development; these helpers may
// be referenced by tests/observability in subsequent phases.
var _ = reflectTypeName
