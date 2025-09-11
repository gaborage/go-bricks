package server

import (
	"time"

	"github.com/labstack/echo/v4"
)

// Timing returns a middleware that adds response time headers to HTTP responses.
// It measures request processing time and adds an X-Response-Time header.
func Timing() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			duration := time.Since(start)

			c.Response().Header().Set("X-Response-Time", duration.String())
			return err
		}
	}
}
