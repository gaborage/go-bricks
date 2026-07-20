package server

import (
	"fmt"
	"log"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// SkipperFunc decides whether middleware processing is skipped for a request. It receives
// the stdlib *http.Request — all a skip decision needs — so application code never names an
// echo type and the decision never depends on per-request state that may be unpopulated.
type SkipperFunc func(r *http.Request) bool

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
	return fromEchoMiddleware(tenantMiddlewareEcho(resolver, skipper, nil))
}

// tenantMiddlewareEcho is the echo-native tenant-resolution middleware constructor.
// Public callers use TenantMiddleware (echo-free, nil logger); SetupMiddlewares
// passes its framework logger so rejected (400) requests leave a structured WARN
// trail — this middleware is registered outer to the access logger
// (server/middleware.go), so a short-circuited request never reaches it and would
// otherwise leave zero server-side trail when observability is disabled. The
// SkipperFunc is invoked with c.Request(). l may be nil, in which case the
// warning falls back to the stdlib log package (mirrors corsWarnf).
func tenantMiddlewareEcho(resolver multitenant.TenantResolver, skipper SkipperFunc, l logger.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// Skip tenant resolution for specified routes
			if skipper != nil && skipper(c.Request()) {
				return next(c)
			}

			if resolver == nil {
				return NewInternalServerError("tenant resolver not configured")
			}

			tenantID, err := resolver.ResolveTenant(c.Request().Context(), c.Request())
			if err != nil || tenantID == "" {
				logTenantRejection(l, c, err)
				return NewBadRequestError("Invalid tenant")
			}

			ctx := multitenant.SetTenant(c.Request().Context(), tenantID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

// logTenantRejection emits one WARN per request rejected by tenant resolution
// (400). This middleware is registered outer to the access logger and never
// calls next() on reject, so without this the request leaves no server-side
// trail when observability is disabled. The resolved tenant is never logged
// here — there is none on this path; only the failure reason (empty tenant vs
// resolver error) is included, never the resolver error's message (which may
// carry caller-controlled request data). With a framework logger it routes
// through structured WARN-level logging (SensitiveDataFilter, dual-mode
// routing); with nil (public TenantMiddleware construction) it falls back to
// the stdlib log package, mirroring corsWarnf in cors.go.
func logTenantRejection(l logger.Logger, c *echo.Context, resolveErr error) {
	reason := "empty tenant"
	if resolveErr != nil {
		reason = "resolver error"
	}
	req := c.Request()
	msg := fmt.Sprintf("[server.tenant] request rejected: invalid tenant method=%s path=%s client=%s status=%d reason=%q",
		req.Method, req.URL.Path, c.RealIP(), http.StatusBadRequest, reason)
	if l == nil {
		log.Printf("WARN %s", msg)
		return
	}
	l.Warn().Msg(msg)
}

// CreateProbeSkipper creates a skipper function for health probe endpoints.
// It returns true for paths that should bypass tenant middleware (health, ready).
// The implementation uses exact path matching for optimal performance and precision.
func CreateProbeSkipper(healthPath, readyPath string) SkipperFunc {
	return func(r *http.Request) bool {
		path := r.URL.Path

		// Use exact path matching for precision and performance
		return path == healthPath || path == readyPath
	}
}
