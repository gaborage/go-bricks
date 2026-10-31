package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/config"
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
// echo-native logic lives in tenantMiddlewareEcho, which SetupMiddlewares wires
// directly on the default request path (ADR-026, no per-request baton).
// Constructed here with a nil logger — see tenantMiddlewareEcho for the
// framework-logger form.
func TenantMiddleware(resolver multitenant.TenantResolver, skipper SkipperFunc) MiddlewareFunc {
	return fromEchoMiddleware(tenantMiddlewareEcho(resolver, skipperFromRequest(skipper), nil))
}

// tenantMiddlewareEcho is the echo-native tenant-resolution middleware constructor.
// Public callers use TenantMiddleware (echo-free, nil logger); SetupMiddlewares
// passes its framework logger so rejected (400) requests leave a structured WARN
// trail — this middleware is registered outer to the access logger
// (server/middleware.go), so a short-circuited request never reaches it and would
// otherwise leave zero server-side trail when observability is disabled. The
// The skipper is the echo-native form (see probeSkipper): the framework passes the
// template-keyed probe predicate, and a public SkipperFunc arrives through
// skipperFromRequest. l may be nil, in which case the warning falls back to the stdlib log
// package (mirrors corsWarnf).
func tenantMiddlewareEcho(resolver multitenant.TenantResolver, skipper probeSkipper, l logger.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// Skip tenant resolution for specified routes
			if skipper != nil && skipper(c) {
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

// logTenantRejection emits one WARN per request refused for its tenant (400):
// by the seat, which is registered outer to the access logger and never calls
// next() on reject, so without this the request leaves no server-side trail
// when observability is disabled; or by logUnknownTenantRefusal. The line
// carries the reason class and the caller-chosen path, never a tenant ID or an
// error message (either may carry caller-controlled request data). With a
// framework logger it routes through structured WARN-level logging
// (SensitiveDataFilter, dual-mode routing); with nil (public TenantMiddleware
// construction) it falls back to the stdlib log package, mirroring corsWarnf in
// cors.go.
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
func logUnknownTenantRefusal(apiErr *UnknownTenantError, c *echo.Context, l logger.Logger, cfg *config.Config) {
	if errors.Is(apiErr.cause, multitenant.ErrUnknownTenant) {
		logTenantRejection(l, c, "unknown tenant", matchedRouteTemplate(c))
		return
	}
	if l == nil {
		log.Printf("ERROR unhandled error request_id=%s error_type=%T", logSafeValue(safeGetRequestID(c)), apiErr.cause)
		return
	}
	appendErrorDetail(l.Error().Str("request_id", safeGetRequestID(c)), apiErr.cause, cfg.App.Debug).Msg("unhandled error")
}
