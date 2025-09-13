package server

import (
	gobrickshttp "github.com/gaborage/go-bricks/http"
	"github.com/labstack/echo/v4"
)

// TraceContext injects the resolved trace ID and W3C trace context headers
// from the Echo request/response into the request context, so that outbound
// HTTP clients can propagate them without depending on Echo.
func TraceContext() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()

			// Resolve or generate the trace ID using existing server logic
			traceID := getTraceID(c)

			// Attach trace ID to request context
			ctx := gobrickshttp.WithTraceID(req.Context(), traceID)

			// Propagate W3C trace headers if present on inbound request
			if tp := req.Header.Get(gobrickshttp.HeaderTraceParent); tp != "" {
				ctx = gobrickshttp.WithTraceParent(ctx, tp)
			}
			if ts := req.Header.Get(gobrickshttp.HeaderTraceState); ts != "" {
				ctx = gobrickshttp.WithTraceState(ctx, ts)
			}

			c.SetRequest(req.WithContext(ctx))
			return next(c)
		}
	}
}
