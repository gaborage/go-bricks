package server

import (
	"github.com/labstack/echo/v4"

	"github.com/gaborage/go-bricks/multitenant"
)

// TenantMiddleware resolves the tenant ID and injects it into the request context.
func TenantMiddleware(resolver multitenant.TenantResolver) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
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
