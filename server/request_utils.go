package server

import (
	"regexp"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// requestIDPattern matches a safe X-Request-ID value: ASCII alphanumerics,
// underscores, and hyphens, length 1..128. Caller-supplied values that fail
// this match are discarded — they flow into log envelopes, JOSE failure
// records, and rate-limit error bodies, so an attacker who can set the
// header could poison logs (arbitrary length/charset) or reuse a victim's
// known request ID to confuse correlation during incident response.
//
// The pattern is deliberately strict (no '.', '=', ':', '+', '/') so the
// framework's request ID surface is always safe to embed in URLs, logs,
// and structured fields without further escaping. Operators behind upstream
// gateways that inject wider charsets should either rewrite the header at
// the proxy or accept the framework-generated UUID.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// validateRequestID returns id when it matches requestIDPattern, otherwise "".
// Callers that get "" should fall back to a trusted source (a previously
// generated UUID in the response header, or a fresh one).
func validateRequestID(id string) string {
	if requestIDPattern.MatchString(id) {
		return id
	}
	return ""
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
func RequestIDMiddleware() echo.MiddlewareFunc {
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
