package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// msgInvalidTenant is the 400 message for a malformed tenant and for an unknown one.
const msgInvalidTenant = "Invalid tenant"

// TenantMiddleware resolves the tenant ID and injects it into the request context.
// If a skipper function is provided, certain routes (e.g., health probes) can bypass tenant resolution.
// If skipper is nil, all routes will undergo tenant resolution.
//
// The returned MiddlewareFunc is the framework-neutral (echo-free) form; the
// echo-native logic lives in tenantMiddlewareEcho, which setupMiddlewares wires
// directly on the default request path (ADR-026, no per-request baton).
// Constructed here with a nil logger — see tenantMiddlewareEcho for the
// framework-logger form.
func TenantMiddleware(resolver multitenant.TenantResolver, skipper SkipperFunc) MiddlewareFunc {
	return fromEchoMiddleware(tenantMiddlewareEcho(resolver, skipperFromRequest(skipper), nil, nil))
}

// tenantMiddlewareEcho is the echo-native tenant-resolution middleware constructor.
// Public callers use TenantMiddleware (echo-free, nil logger); setupMiddlewares
// passes its framework logger so rejected (400) requests leave a structured WARN
// trail — this middleware is registered outer to the access logger
// (server/middleware.go), so a short-circuited request never reaches it and would
// otherwise leave zero server-side trail when observability is disabled.
// The skipper is the echo-native form (see probeSkipper): the framework passes the
// template-keyed probe predicate, and a public SkipperFunc arrives through
// skipperFromRequest. owned reports whether a matched route template belongs to a tenant-owned
// family, whose own steps resolve the tenant: the seat skips it and leaves seatSkippedKey as
// evidence. A nil owned skips nothing. l may be nil, in which case the warning falls back to
// the stdlib log package (mirrors corsWarnf).
func tenantMiddlewareEcho(resolver multitenant.TenantResolver, skipper probeSkipper, owned func(template string) bool, l logger.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// Skip tenant resolution for specified routes
			if skipper != nil && skipper(c) {
				return next(c)
			}
			if owned != nil && owned(c.Path()) {
				c.Set(seatSkippedKey, true)
				return next(c)
			}

			if resolver == nil {
				return NewInternalServerError("tenant resolver not configured")
			}

			tenantID, err := resolver.ResolveTenant(c.Request().Context(), c.Request())
			if err != nil || tenantID == "" {
				logTenantRejection(l, c, tenantRejectionReason(err), c.Request().URL.Path)
				return NewBadRequestError(msgInvalidTenant)
			}

			ctx := multitenant.SetTenant(c.Request().Context(), tenantID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

// seatSkippedKey marks a request the tenant seat skipped because a tenant-owned family owns its
// matched route.
const seatSkippedKey = "_tenant_seat_skipped"

// tenantRejectionReason names the seat's failure class (empty tenant, untrusted
// X-Forwarded-Host peer, or resolver error), never the resolver error's message.
func tenantRejectionReason(resolveErr error) string {
	if errors.Is(resolveErr, multitenant.ErrUntrustedForwardedHost) {
		return "X-Forwarded-Host from a peer outside server.trustedproxies"
	}
	if resolveErr != nil {
		return "resolver error"
	}
	return "empty tenant"
}

// logTenantRejection emits one WARN per request refused for its tenant (400), carrying the
// reason class and the caller-chosen path, never a tenant ID or an error message. With a nil
// logger (public TenantMiddleware construction) it falls back to stdlib log, like corsWarnf.
func logTenantRejection(l logger.Logger, c *echo.Context, reason, path string) {
	req := c.Request()
	msg := fmt.Sprintf("[server.tenant] request rejected: invalid tenant method=%s path=%s client=%s status=%d reason=%q",
		logSafeValue(req.Method), logSafeValue(path), logSafeValue(c.RealIP()), http.StatusBadRequest, reason)
	if l == nil {
		log.Printf("WARN %s", msg)
		return
	}
	l.Warn().Msg(msg)
}

// logUnknownTenantRefusal writes the one line an UnknownTenantError leaves. A 400
// logs the tenant-rejection WARN with the route template, since a path resolver
// puts the tenant ID in the URL. A 500 (a cause that is not the sentinel) logs
// classifyError's unhandled-error line, the error type only outside debug.
func logUnknownTenantRefusal(apiErr *UnknownTenantError, c *echo.Context, l logger.Logger, debug bool) {
	if apiErr.HTTPStatus() == http.StatusBadRequest {
		logTenantRejection(l, c, "unknown tenant", matchedRouteTemplate(c))
		return
	}
	logUnhandledError(l, c, apiErr.cause, debug)
}

// logUnhandledError writes the 5xx "unhandled error" line, the error type only outside
// debug; with a nil logger it falls back to stdlib log.
func logUnhandledError(l logger.Logger, c *echo.Context, err error, debug bool) {
	if l == nil {
		log.Printf("ERROR unhandled error request_id=%s error_type=%T", logSafeValue(safeGetRequestID(c)), err)
		return
	}
	appendErrorDetail(l.Error().Str("request_id", safeGetRequestID(c)), err, debug).Msg("unhandled error")
}
