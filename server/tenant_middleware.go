package server

import (
	"net/http"

	"github.com/labstack/echo/v5"

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
func TenantMiddleware(resolver multitenant.TenantResolver, skipper SkipperFunc) MiddlewareFunc {
	return fromEchoMiddleware(tenantMiddlewareEcho(resolver, skipper))
}

// tenantMiddlewareEcho is the echo-native tenant-resolution middleware constructor.
// Public callers use TenantMiddleware (echo-free); SetupMiddlewares uses this form to
// keep the default chain baton-free. The SkipperFunc is invoked with c.Request().
func tenantMiddlewareEcho(resolver multitenant.TenantResolver, skipper SkipperFunc) echo.MiddlewareFunc {
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
				return NewBadRequestError("Invalid tenant")
			}

			ctx := multitenant.SetTenant(c.Request().Context(), tenantID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
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
