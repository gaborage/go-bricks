package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gobrickshttp "github.com/gaborage/go-bricks/httpclient"
	"github.com/gaborage/go-bricks/internal/testutil"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

const testTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

// poisonedTraceParent is the #1100 probe: 32 non-hex bytes where the trace-id
// belongs, which the old length-only check accepted and re-emitted as the
// outbound X-Request-ID.
var poisonedTraceParent = "00-" + strings.Repeat("!", 32) + "-00f067aa0ba902b7-01"

func TestTraceContext(t *testing.T) {
	e := echo.New()
	e.Use(traceContextEcho())

	var capturedContext context.Context

	e.GET("/test", func(c *echo.Context) error {
		capturedContext = c.Request().Context()
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	t.Run("trace_id_injected", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		require.NotNil(t, capturedContext)

		// Verify trace ID is present in context
		traceID, ok := gobrickshttp.TraceIDFromContext(capturedContext)
		assert.True(t, ok, "Trace ID should be present in context")
		assert.NotEmpty(t, traceID, "Trace ID should be injected into context")
	})

	t.Run("existing_traceparent_propagated", func(t *testing.T) {
		traceparent := testTraceparent

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
		req.Header.Set(gobrickshttp.HeaderTraceParent, traceparent)
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		require.NotNil(t, capturedContext)

		// Verify traceparent is propagated to context
		contextTraceparent, ok := gobrickshttp.TraceParentFromContext(capturedContext)
		assert.True(t, ok, "Traceparent should be present in context")
		assert.Equal(t, traceparent, contextTraceparent,
			"Traceparent should be propagated from request header to context")
	})

	t.Run("both_headers_propagated", func(t *testing.T) {
		traceparent := testTraceparent
		tracestate := "congo=t61rcWkgMzE,rojo=00f067aa0ba902b7"

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
		req.Header.Set(gobrickshttp.HeaderTraceParent, traceparent)
		req.Header.Set(gobrickshttp.HeaderTraceState, tracestate)
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		require.NotNil(t, capturedContext)

		// Verify both headers are propagated
		contextTraceparent, okParent := gobrickshttp.TraceParentFromContext(capturedContext)
		contextTracestate, okState := gobrickshttp.TraceStateFromContext(capturedContext)

		assert.True(t, okParent, "Traceparent should be present")
		assert.True(t, okState, "Tracestate should be present")
		assert.Equal(t, traceparent, contextTraceparent)
		assert.Equal(t, tracestate, contextTracestate)
	})

	t.Run("missing_headers_handled", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
		// No trace headers
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		require.NotNil(t, capturedContext)

		// Should still have a trace ID generated
		traceID, ok := gobrickshttp.TraceIDFromContext(capturedContext)
		assert.True(t, ok, "Trace ID should be generated even without headers")
		assert.NotEmpty(t, traceID)

		// But no traceparent/tracestate
		_, okParent := gobrickshttp.TraceParentFromContext(capturedContext)
		_, okState := gobrickshttp.TraceStateFromContext(capturedContext)

		assert.False(t, okParent, "Traceparent should not be present")
		assert.False(t, okState, "Tracestate should not be present")
	})
}

func TestTraceContextWithErrorHandler(t *testing.T) {
	e := echo.New()
	e.Use(traceContextEcho())

	var capturedContext context.Context

	e.GET("/error", func(c *echo.Context) error {
		capturedContext = c.Request().Context()
		return echo.NewHTTPError(http.StatusBadRequest, testutil.TestError)
	})

	traceparent := testTraceparent

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/error", http.NoBody)
	req.Header.Set(gobrickshttp.HeaderTraceParent, traceparent)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// Even with error, context should be properly set
	require.NotNil(t, capturedContext)

	contextTraceparent, ok := gobrickshttp.TraceParentFromContext(capturedContext)
	assert.True(t, ok, "Traceparent should be present even on error")
	assert.Equal(t, traceparent, contextTraceparent,
		"Trace context should be set even when handler returns error")

	traceID, okID := gobrickshttp.TraceIDFromContext(capturedContext)
	assert.True(t, okID, "Trace ID should be present even on error")
	assert.NotEmpty(t, traceID, "Trace ID should be set even when handler returns error")
}

func TestTraceContextMiddlewareOrder(t *testing.T) {
	e := echo.New()

	var preTraceContext context.Context
	var postTraceContext context.Context

	// Middleware before trace context
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			preTraceContext = c.Request().Context()
			return next(c)
		}
	})

	e.Use(traceContextEcho())

	// Middleware after trace context
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			postTraceContext = c.Request().Context()
			return next(c)
		}
	})

	e.GET("/test", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
	req.Header.Set(gobrickshttp.HeaderTraceParent, testTraceparent)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	require.NotNil(t, preTraceContext)
	require.NotNil(t, postTraceContext)

	// After trace context middleware, trace info should be available
	postTraceID, okPostID := gobrickshttp.TraceIDFromContext(postTraceContext)
	postTraceparent, okPostParent := gobrickshttp.TraceParentFromContext(postTraceContext)

	// Post-trace context should have trace info
	assert.True(t, okPostID, "Trace ID should be available after trace context middleware")
	assert.NotEmpty(t, postTraceID, "Trace ID should be available after trace context middleware")
	assert.True(t, okPostParent, "Traceparent should be available after trace context middleware")
	assert.NotEmpty(t, postTraceparent, "Traceparent should be available after trace context middleware")

	// The contexts should be different instances
	assert.NotEqual(t, preTraceContext, postTraceContext,
		"Context should be replaced by trace context middleware")
}

// TestTraceContextIngressValidation pins the HTTP door's half of ADR-070: what
// the middleware plants in the context is what trace.ExtractFromHeaders would
// plant at the messaging and outbox doors, never the raw request header.
//
// Drop-and-mint, not reject: an unusable traceparent leaves the context in
// exactly the state an untraced request produces, and the request is served.
func TestTraceContextIngressValidation(t *testing.T) {
	e := echo.New()
	e.Use(traceContextEcho())

	var capturedContext context.Context

	e.GET("/test", func(c *echo.Context) error {
		capturedContext = c.Request().Context()
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	const validState = "congo=t61rcWkgMzE,rojo=00f067aa0ba902b7"

	tests := []struct {
		name        string
		traceparent string
		tracestate  string
		wantParent  string
		wantState   string
	}{
		{
			name:        "valid_traceparent_and_tracestate_propagate_unchanged",
			traceparent: testTraceparent,
			tracestate:  validState,
			wantParent:  testTraceparent,
			wantState:   validState,
		},
		{
			name:        "unparseable_traceparent_dropped",
			traceparent: "invalid-trace-parent",
		},
		{
			name:        "non_hex_trace_id_dropped",
			traceparent: poisonedTraceParent,
		},
		{
			name:        "short_traceparent_dropped",
			traceparent: "00-short-trace-01",
		},
		{
			name:        "all_zero_trace_id_dropped",
			traceparent: "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		},
		{
			name:        "forbidden_version_ff_dropped",
			traceparent: "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		},
		{
			name:        "tracestate_dropped_with_its_invalid_parent",
			traceparent: poisonedTraceParent,
			tracestate:  validState,
		},
		{
			name:       "orphan_tracestate_dropped",
			tracestate: validState,
		},
		{
			name:        "tracestate_at_the_cap_kept",
			traceparent: testTraceparent,
			tracestate:  strings.Repeat("a", gobrickstrace.MaxTraceStateBytes),
			wantParent:  testTraceparent,
			wantState:   strings.Repeat("a", gobrickstrace.MaxTraceStateBytes),
		},
		{
			name:        "tracestate_one_over_the_cap_dropped",
			traceparent: testTraceparent,
			tracestate:  strings.Repeat("a", gobrickstrace.MaxTraceStateBytes+1),
			wantParent:  testTraceparent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capturedContext = nil
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
			if tt.traceparent != "" {
				req.Header.Set(gobrickshttp.HeaderTraceParent, tt.traceparent)
			}
			if tt.tracestate != "" {
				req.Header.Set(gobrickshttp.HeaderTraceState, tt.tracestate)
			}

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code, "an unusable trace header never fails the request")
			require.NotNil(t, capturedContext)

			traceID, okID := gobrickstrace.IDFromContext(capturedContext)
			assert.True(t, okID, "a trace ID is minted regardless of what the caller sent")
			assert.NotEmpty(t, traceID)

			// TraceParentFromContext/TraceStateFromContext report ok == (value != ""),
			// so the value assertions carry the presence claim too.
			contextParent, _ := gobrickshttp.TraceParentFromContext(capturedContext)
			assert.Equal(t, tt.wantParent, contextParent)

			contextState, _ := gobrickshttp.TraceStateFromContext(capturedContext)
			assert.Equal(t, tt.wantState, contextState)
		})
	}
}

func TestTraceContextConcurrentRequests(t *testing.T) {
	e := echo.New()
	e.Use(traceContextEcho())

	type requestResult struct {
		traceID     string
		traceparent string
	}

	results := make(chan requestResult, 10)

	e.GET("/test", func(c *echo.Context) error {
		ctx := c.Request().Context()

		traceID, _ := gobrickshttp.TraceIDFromContext(ctx)
		traceparent, _ := gobrickshttp.TraceParentFromContext(ctx)

		result := requestResult{
			traceID:     traceID,
			traceparent: traceparent,
		}

		results <- result
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Launch concurrent requests with different traceparents
	traceparents := []string{
		testTraceparent,
		"00-1123456789abcdef0123456789abcdef-1123456789abcdef-01",
		"00-2123456789abcdef0123456789abcdef-2123456789abcdef-01",
		"00-3123456789abcdef0123456789abcdef-3123456789abcdef-01",
		"00-4123456789abcdef0123456789abcdef-4123456789abcdef-01",
	}

	for i, tp := range traceparents {
		go func(_ int, traceparent string) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
			req.Header.Set(gobrickshttp.HeaderTraceParent, traceparent)
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)
		}(i, tp)
	}

	// Also send requests without traceparent
	for i := 0; i < 5; i++ {
		go func() {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)
		}()
	}

	// Collect results
	receivedTraceparents := make(map[string]bool)
	receivedTraceIDs := make(map[string]bool)

	for i := 0; i < 10; i++ {
		result := <-results

		assert.NotEmpty(t, result.traceID, "Each request should have a trace ID")

		if result.traceparent != "" {
			receivedTraceparents[result.traceparent] = true
		}
		receivedTraceIDs[result.traceID] = true
	}

	// Verify each traceparent was properly handled
	for _, tp := range traceparents {
		assert.True(t, receivedTraceparents[tp],
			"Traceparent %s should have been processed", tp)
	}

	// All trace IDs should be unique
	assert.Equal(t, 10, len(receivedTraceIDs),
		"All requests should have unique trace IDs")
}

// TestTraceContextShadowsAnInheritedTraceState covers the one case the header
// table cannot reach: a tracestate already in the request context, planted by an
// earlier middleware, annotates a DIFFERENT traceparent. A request bringing its
// own valid parent must not adopt it — the outbound hop would re-emit one trace's
// vendor state under another's parent (ADR-070, amended).
func TestTraceContextShadowsAnInheritedTraceState(t *testing.T) {
	e := echo.New()
	e.Use(traceContextEcho())

	var capturedContext context.Context
	e.GET("/test", func(c *echo.Context) error {
		capturedContext = c.Request().Context()
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// The three inbound-parent shapes that can reach this door. The absent and
	// invalid cases matter most: those are precisely when a FRESH traceparent gets
	// minted downstream, so an inherited tracestate surviving here is emitted
	// annotating a parent that did not exist when it was written.
	for _, tt := range []struct {
		name        string
		traceparent string
	}{
		{name: "request_brings_a_valid_parent", traceparent: testTraceparent},
		{name: "request_brings_an_invalid_parent", traceparent: poisonedTraceParent},
		{name: "request_brings_no_parent"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			capturedContext = nil
			inherited := gobrickshttp.WithTraceState(context.Background(), "inherited=from-another-parent")
			req := httptest.NewRequestWithContext(inherited, http.MethodGet, "/test", http.NoBody)
			if tt.traceparent != "" {
				req.Header.Set(gobrickshttp.HeaderTraceParent, tt.traceparent)
			}

			e.ServeHTTP(httptest.NewRecorder(), req)
			require.NotNil(t, capturedContext)

			got, ok := gobrickshttp.TraceStateFromContext(capturedContext)
			assert.False(t, ok, "an inherited tracestate rode along with a parent it does not annotate: %q", got)
		})
	}
}

// mapHeaders is a trace.HeaderAccessor over a plain map, so a server test can
// drive the same injection the AMQP publish path drives.
type mapHeaders map[string]any

func (m mapHeaders) Get(key string) any        { return m[key] }
func (m mapHeaders) Set(key string, value any) { m[key] = value }

// TestTraceContextIngressYieldsAPublishableIdentity walks the #1100 probe from
// the HTTP door to the publish seam. Before the ingress guard, the poisoned
// traceparent reached the context, forceAlignTraceID aligned X-Request-ID onto
// its 32 non-hex bytes, and the publish-side charset guard then refused that id
// and shipped an EMPTY CorrelationId (the C60.10 symptom) — remote-triggerable
// and free. The minted identity has to be publishable end to end.
func TestTraceContextIngressYieldsAPublishableIdentity(t *testing.T) {
	e := echo.New()
	e.Use(traceContextEcho())

	var capturedContext context.Context
	e.GET("/test", func(c *echo.Context) error {
		capturedContext = c.Request().Context()
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
	req.Header.Set(gobrickshttp.HeaderTraceParent, poisonedTraceParent)
	e.ServeHTTP(httptest.NewRecorder(), req)
	require.NotNil(t, capturedContext)

	out := mapHeaders{}
	gobrickstrace.InjectIntoHeaders(capturedContext, out)

	traceparent, _ := out[gobrickstrace.HeaderTraceParent].(string)
	requestID, _ := out[gobrickstrace.HeaderXRequestID].(string)

	assert.NotEqual(t, poisonedTraceParent, traceparent, "the poisoned value escaped onto the next hop")
	assert.Equal(t, traceparent, gobrickstrace.ValidateTraceParent(traceparent),
		"the outbound traceparent must be spec-exact")
	assert.Equal(t, traceparent[3:35], requestID,
		"X-Request-ID must align onto the minted traceparent's trace-id")
	assert.Equal(t, requestID, gobrickstrace.ValidateRequestID(requestID),
		"the publish-side guard must accept the aligned id, so CorrelationId is populated rather than blank")
}
