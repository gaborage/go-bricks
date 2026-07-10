package server

import (
	"context"

	gobrickshttp "github.com/gaborage/go-bricks/httpclient"
	"github.com/labstack/echo/v5"
)

// enrichTraceContext returns the request's context with the resolved trace ID and
// any inbound W3C trace headers (traceparent/tracestate) attached, so outbound
// HTTP clients can propagate them without depending on Echo. Shared by the
// TraceContext and RequestEnrich middlewares so the enrichment cannot diverge.
func enrichTraceContext(c *echo.Context) context.Context {
	req := c.Request()
	ctx := gobrickshttp.WithTraceID(req.Context(), getTraceID(c))
	if tp := req.Header.Get(gobrickshttp.HeaderTraceParent); tp != "" {
		ctx = gobrickshttp.WithTraceParent(ctx, tp)
	}
	if ts := req.Header.Get(gobrickshttp.HeaderTraceState); ts != "" {
		ctx = gobrickshttp.WithTraceState(ctx, ts)
	}
	return ctx
}

// TraceContext injects the resolved trace ID and W3C trace context headers
// from the Echo request/response into the request context, so that outbound
// HTTP clients can propagate them without depending on Echo.
//
// The returned MiddlewareFunc is the framework-neutral (echo-free) form; the
// echo-native logic lives in traceContextEcho.
func TraceContext() MiddlewareFunc {
	return fromEchoMiddleware(traceContextEcho())
}

// traceContextEcho is the echo-native trace-context middleware constructor. Public
// callers use TraceContext (echo-free).
func traceContextEcho() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()

			// SAFETY: Check if the request context has already been canceled (e.g., by timeout).
			// If so, we should return early to avoid accessing potentially invalidated Echo state.
			select {
			case <-req.Context().Done():
				return req.Context().Err()
			default:
			}

			c.SetRequest(req.WithContext(enrichTraceContext(c)))
			return next(c)
		}
	}
}
