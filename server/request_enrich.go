package server

import (
	"github.com/gaborage/go-bricks/internal/leasescope"
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
// counters are seeded. TraceContext's canceled-context early-return is adopted so
// an already-canceled inbound request short-circuits before any enrichment.
//
// It also installs the per-request lease scope (ADR-032): per-tenant resource handles
// borrowed via deps.DB/Cache/Messaging during the request register their release here and
// are released when the handler chain unwinds, so a handle evicted mid-request is not closed
// under the active request. The scope is folded into the existing Request.WithContext clone
// so it adds no extra per-request allocation, and its release slice stays nil until the first
// borrow (a request touching no managed resource pays nothing). Registered early in
// SetupMiddlewares so ReleaseAll runs after the handler and every inner middleware.
func RequestEnrich() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()

			// SAFETY: if the request context is already canceled (e.g. by timeout),
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
			ctx, scope := leasescope.Install(ctx)
			defer scope.ReleaseAll()

			c.SetRequest(req.WithContext(ctx))
			return next(c)
		}
	}
}
