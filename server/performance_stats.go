package server

import (
	"github.com/labstack/echo/v4"

	"github.com/gaborage/go-bricks/logger"
)

// PerformanceStats returns middleware that initializes operation tracking for each request.
// It adds both AMQP message counter and database operation counter to the request context
// that can be incremented by messaging clients and database layer, then logged in the request logger.
func PerformanceStats() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Add both AMQP counter and DB counter to request context
			ctx := logger.WithAMQPCounter(c.Request().Context())
			ctx = logger.WithDBCounter(ctx)
			c.SetRequest(c.Request().WithContext(ctx))

			return next(c)
		}
	}
}
