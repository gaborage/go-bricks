package server

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// validateRequestID returns id when it is a safe request identifier, otherwise
// "". The rule itself lives in trace, so every ingress door applies the same
// bound — this one, the messaging lanes, and the exported extractor a consumer
// can call directly (ADR-070).
func validateRequestID(id string) string {
	return gobrickstrace.ValidateRequestID(id)
}

// validateTraceParent returns tp when it is a spec-exact, non-zero W3C
// traceparent, otherwise "". Every server-side read of an INBOUND traceparent
// goes through here, so the HTTP door applies the bound the messaging lanes and
// the outbox relay already apply (ADR-070).
func validateTraceParent(tp string) string {
	return gobrickstrace.ValidateTraceParent(tp)
}

// validateTraceState returns ts when it is within the tracestate cap, otherwise
// "". Shared with the messaging door as a function, not as a constant: see
// trace.ValidateTraceState.
func validateTraceState(ts string) string {
	return gobrickstrace.ValidateTraceState(ts)
}

// RequestIDMiddleware reads the inbound X-Request-ID header, validates it
// against requestIDPattern, and sets the response header to either the
// validated value or a freshly generated UUID. It MUST replace Echo's
// stock middleware.RequestID() because that middleware echoes the inbound
// header verbatim with no validation, which:
//
//  1. Reflects attacker-controlled bytes back to the client (the response
//     header travels on the wire and lands in CDN logs and browser tools).
//  2. Pre-populates the response header before any framework code runs,
//     defeating downstream validation by getTraceID/safeGetRequestID that
//     would otherwise trust the response-header value.
//
// Register this BEFORE TraceContext and any logger/rate-limit middleware
// so the rest of the stack sees a sanitized value.
//
// The returned MiddlewareFunc is the framework-neutral (echo-free) form; the
// echo-native logic lives in requestIDMiddlewareEcho, which SetupMiddlewares wires
// directly on the default request path (ADR-026, no per-request baton).
func RequestIDMiddleware() MiddlewareFunc {
	return fromEchoMiddleware(requestIDMiddlewareEcho())
}

// requestIDMiddlewareEcho is the echo-native request-ID middleware constructor.
// Public callers use RequestIDMiddleware (echo-free); SetupMiddlewares uses this
// form to keep the default chain baton-free.
func requestIDMiddlewareEcho() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			id := validateRequestID(c.Request().Header.Get(echo.HeaderXRequestID))
			if id == "" {
				id = uuid.New().String()
			}
			c.Response().Header().Set(echo.HeaderXRequestID, id)
			return next(c)
		}
	}
}

// isResponseCommitted returns true if the response has already been written.
// In Echo v5, c.Response() returns http.ResponseWriter; echo.UnwrapResponse
// is needed to access the *echo.Response struct and its Committed field.
func isResponseCommitted(c *echo.Context) bool {
	resp, err := echo.UnwrapResponse(c.Response())
	return err == nil && resp.Committed
}

// safeGetRequestID safely extracts request ID from response or falls back to request header.
// SAFETY: Response may be nil after timeout or in edge cases, so we check before accessing.
// Both the response-header and request-header paths are validated via validateRequestID
// as defense in depth — the response header is normally populated by RequestIDMiddleware
// with a known-good value, but validating again costs almost nothing (UUIDs pass) and
// protects callers from a scenario where the middleware is misconfigured or replaced.
//
// This utility is used across multiple middleware components (rate limiting, IP pre-guard)
// to ensure consistent and safe request ID extraction even in edge cases like timeouts
// where the response object might be nil.
func safeGetRequestID(c *echo.Context) string {
	if resp := c.Response(); resp != nil {
		if id := validateRequestID(resp.Header().Get(echo.HeaderXRequestID)); id != "" {
			return id
		}
	}
	return validateRequestID(c.Request().Header.Get(echo.HeaderXRequestID))
}

// logSafeValueMaxBytes caps a request-derived value before it is escaped for
// a log line, so an oversized path cannot become an unbounded log entry. The
// cap applies to the raw bytes: escaping can expand each byte to at most four
// (\xHH), so the rendered field is bounded by 4x this value.
const logSafeValueMaxBytes = 256

// logSafeValue returns v rendered log-safe: capped at logSafeValueMaxBytes
// (a "..." marker replaces the tail) and Go-quoted via the %q verb, so
// newlines, other control bytes and invalid UTF-8 appear as escape sequences
// (\n, \x00) rather than raw bytes. %q rather than strconv.Quote because it
// is the form CodeQL's go/log-injection rule recognises as a sanitizer; the
// output is identical. The surrounding quotes are kept: a space inside an
// unquoted value would otherwise read as a field separator and let
// "/x status=200" forge a field on the same line.
func logSafeValue(v string) string {
	const marker = "..."
	if len(v) > logSafeValueMaxBytes {
		v = v[:logSafeValueMaxBytes-len(marker)] + marker
	}
	return fmt.Sprintf("%q", v)
}
