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
	// Shadow BOTH inherited W3C keys first, unconditionally. At an ingress the
	// request defines the trace: anything already on the context arrived from the
	// server's base context or an earlier middleware, not from this caller, and
	// leaving it would let InjectIntoHeaders propagate a trace this request is not
	// part of. Clearing before the branch is what covers the invalid- and
	// absent-parent paths, which are exactly when a fresh parent gets minted
	// downstream.
	//
	// Both, not one: clearing only the tracestate would keep an inherited parent
	// while stripping the state that annotates it, which is the mismatched pairing
	// this rule exists to prevent, merely inverted. ParentFromContext and
	// StateFromContext both report "" as absent, so this removes the values rather
	// than propagating empty ones. This is where the HTTP door deliberately parts
	// from trace.ExtractFromHeaders, which leaves an inherited parent alone: that
	// seam serves carriers whose surrounding context legitimately holds a caller's
	// trace, while an HTTP request IS the trace's origin here.
	ctx = gobrickshttp.WithTraceParent(ctx, "")
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
