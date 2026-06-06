package server

import (
	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/logger"
)

// PerformanceStats returns middleware that initializes operation tracking for each request.
// It adds both AMQP message counter and database operation counter to the request context
// that can be incremented by messaging clients and database layer, then logged in the request logger.
func PerformanceStats() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// Seed the shared per-request AMQP/DB counters.
			ctx := logger.WithRequestCounters(c.Request().Context())
			c.SetRequest(c.Request().WithContext(ctx))

			return next(c)
		}
	}
}
