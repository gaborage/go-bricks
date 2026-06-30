package server

import (
	"time"

	"github.com/labstack/echo/v5"
)

// Timing returns a middleware that adds response time headers to HTTP responses.
// It measures request processing time and adds an X-Response-Time header.
//
// The returned MiddlewareFunc is the framework-neutral (echo-free) form; the
// echo-native logic lives in timingEcho, which SetupMiddlewares wires directly on
// the default request path (ADR-026, no per-request baton).
func Timing() MiddlewareFunc {
	return fromEchoMiddleware(timingEcho())
}

// timingEcho is the echo-native response-time middleware constructor. Public callers
// use Timing (echo-free); SetupMiddlewares uses this form to keep the default chain
// baton-free.
func timingEcho() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			start := time.Now()
			err := next(c)
			duration := time.Since(start)

			// SAFETY: Check if response is still valid (may be nil after timeout)
			// This middleware runs AFTER the timeout middleware in the chain
			if resp := c.Response(); resp != nil {
				resp.Header().Set(HeaderXResponseTime, duration.String())
			}
			return err
		}
	}
}
