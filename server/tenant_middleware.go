package server

import (
	"github.com/labstack/echo/v4"

	"github.com/gaborage/go-bricks/multitenant"
)

// SkipperFunc defines a function to skip middleware processing for certain requests.
type SkipperFunc func(c echo.Context) bool

// TenantMiddleware resolves the tenant ID and injects it into the request context.
// If a skipper function is provided, certain routes (e.g., health probes) can bypass tenant resolution.
// If skipper is nil, all routes will undergo tenant resolution.
func TenantMiddleware(resolver multitenant.TenantResolver, skipper SkipperFunc) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Skip tenant resolution for specified routes
			if skipper != nil && skipper(c) {
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
	return func(c echo.Context) bool {
		path := c.Request().URL.Path

		// Use exact path matching for precision and performance
		return path == healthPath || path == readyPath
	}
}
