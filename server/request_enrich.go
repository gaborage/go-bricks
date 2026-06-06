package server

import (
	"github.com/gaborage/go-bricks/logger"
	"github.com/labstack/echo/v5"
)

// RequestEnrich combines the two adjacent pure-value request enrichers —
// trace-context injection (TraceContext) and per-request operation counters
// (PerformanceStats) — into a single middleware that performs ONE
// Request.WithContext clone instead of two. The standalone TraceContext() and
// PerformanceStats() middlewares remain exported for callers that register them
// individually; SetupMiddlewares wires this combined form on the hot path.
//
// Behavior is the union of both originals: the resolved trace ID and any inbound
// W3C trace headers are attached for outbound propagation, and the shared AMQP/DB
// counters are seeded. TraceContext's cancelled-context early-return is adopted so
// an already-cancelled inbound request short-circuits before any enrichment.
func RequestEnrich() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()

			// SAFETY: if the request context is already cancelled (e.g. by timeout),
			// return early to avoid touching potentially invalidated Echo state.
			select {
			case <-req.Context().Done():
				return req.Context().Err()
			default:
			}

			// Trace ID + W3C header propagation (shared with TraceContext), plus the
			// per-request AMQP/DB counters (from PerformanceStats) — one shared struct.
			ctx := enrichTraceContext(c)
			ctx = logger.WithRequestCounters(ctx)

			c.SetRequest(req.WithContext(ctx))
			return next(c)
		}
	}
}
