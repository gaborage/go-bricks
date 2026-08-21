package server

import (
	"context"

	"github.com/labstack/echo/v5"

	gobrickshttp "github.com/gaborage/go-bricks/httpclient"
)

// enrichTraceContext returns the request's context with the resolved trace ID and
// any inbound W3C trace headers (traceparent/tracestate) attached, so outbound
// HTTP clients can propagate them without depending on Echo. Shared by the
// TraceContext and RequestEnrich middlewares so the enrichment cannot diverge.
//
// Both headers are judged by the rules trace.ExtractFromHeaders applies at the
// messaging and outbox doors (ADR-070). The HTTP door read them raw, which made
// it the one ingress that stored a caller's bytes verbatim and re-emitted them on
// every outbound hop. An invalid traceparent is treated as absent — never as a
// reason to reject the request — so the request continues on a freshly minted
// one, byte-identical to the path every untraced request already takes.
func enrichTraceContext(c *echo.Context) context.Context {
	req := c.Request()
	ctx := gobrickshttp.WithTraceID(req.Context(), getTraceID(c))
	// Shadow any inherited tracestate FIRST, unconditionally. At an ingress the
	// request defines the trace, so a tracestate already on the context was not
	// put there by this caller; leaving it would let InjectIntoHeaders emit it
	// alongside a freshly minted traceparent it never annotated. Clearing before
	// the branch — rather than only inside it — is what covers the case where
	// this request brings NO valid parent, which is exactly when a fresh one gets
	// minted downstream. StateFromContext reports "" as absent, so this removes
	// the value rather than propagating an empty one.
	ctx = gobrickshttp.WithTraceState(ctx, "")
	if tp := validateTraceParent(req.Header.Get(gobrickshttp.HeaderTraceParent)); tp != "" {
		ctx = gobrickshttp.WithTraceParent(ctx, tp)
		ctx = gobrickshttp.WithTraceState(ctx, validateTraceState(req.Header.Get(gobrickshttp.HeaderTraceState)))
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
