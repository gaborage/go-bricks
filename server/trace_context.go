package server

import (
	gobrickshttp "github.com/gaborage/go-bricks/httpclient"
	"github.com/labstack/echo/v4"
)

// TraceContext injects the resolved trace ID and W3C trace context headers
// from the Echo request/response into the request context, so that outbound
// HTTP clients can propagate them without depending on Echo.
func TraceContext() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()

			// SAFETY: Check if the request context has already been cancelled (e.g., by timeout).
			// If so, we should return early to avoid accessing potentially invalidated Echo state.
			select {
			case <-req.Context().Done():
				// Context already cancelled, return the error without processing
				return req.Context().Err()
			default:
				// Context still active, proceed normally
			}

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
