package server

import (
	"context"
	"time"

	"github.com/labstack/echo/v4"
)

// Timeout returns middleware that adds a request-scoped deadline without swapping
// Echo's response writer. When the configured duration elapses the handler will
// observe context cancellation and higher layers will surface a 503 via the
// centralized error handler.
//
// Why not use Echo's middleware.TimeoutWithConfig?
// Echo's timeout wraps net/http.TimeoutHandler which swaps the response writer with
// a timeoutWriter. When timeouts occur, this invalidates Echo's response object
// (c.Response() returns nil), causing panics in logging/middleware that access
// response headers/status. By using context-only timeouts, we maintain response
// validity while still enforcing deadlines.
func Timeout(duration time.Duration) echo.MiddlewareFunc {
	if duration <= 0 {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			parent := c.Request().Context()

			// Short-circuit if the upstream context is already cancelled.
			select {
			case <-parent.Done():
				return parent.Err()
			default:
			}

			ctx, cancel := context.WithTimeout(parent, duration)
			defer cancel()

			c.SetRequest(c.Request().WithContext(ctx))

			err := next(c)

			// If the deadline fired, propagate the timeout so the global error handler
			// can emit a standardized 503 envelope instead of writing to a dead response.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}

			return err
		}
	}
}
