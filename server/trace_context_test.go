package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gobrickshttp "github.com/gaborage/go-bricks/httpclient"
	"github.com/gaborage/go-bricks/internal/testutil"
)

const (
	testTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
)

func TestTraceContext(t *testing.T) {
	e := echo.New()
	e.Use(TraceContext())

	var capturedContext context.Context

	e.GET("/test", func(c echo.Context) error {
		capturedContext = c.Request().Context()
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	t.Run("trace_id_injected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
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

		req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
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

	t.Run("existing_tracestate_propagated", func(t *testing.T) {
		tracestate := "congo=t61rcWkgMzE,rojo=00f067aa0ba902b7"

		req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
		req.Header.Set(gobrickshttp.HeaderTraceState, tracestate)
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		require.NotNil(t, capturedContext)

		// Verify tracestate is propagated to context
		contextTracestate, ok := gobrickshttp.TraceStateFromContext(capturedContext)
		assert.True(t, ok, "Tracestate should be present in context")
		assert.Equal(t, tracestate, contextTracestate,
			"Tracestate should be propagated from request header to context")
	})

	t.Run("both_headers_propagated", func(t *testing.T) {
		traceparent := testTraceparent
		tracestate := "congo=t61rcWkgMzE,rojo=00f067aa0ba902b7"

		req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
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
		req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
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
	e.Use(TraceContext())

	var capturedContext context.Context

	e.GET("/error", func(c echo.Context) error {
		capturedContext = c.Request().Context()
		return echo.NewHTTPError(http.StatusBadRequest, testutil.TestError)
	})

	traceparent := testTraceparent

	req := httptest.NewRequest(http.MethodGet, "/error", http.NoBody)
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
		return func(c echo.Context) error {
			preTraceContext = c.Request().Context()
			return next(c)
		}
	})

	e.Use(TraceContext())

	// Middleware after trace context
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			postTraceContext = c.Request().Context()
			return next(c)
		}
	})

	e.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
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

func TestTraceContextInvalidHeaders(t *testing.T) {
	e := echo.New()
	e.Use(TraceContext())

	var capturedContext context.Context

	e.GET("/test", func(c echo.Context) error {
		capturedContext = c.Request().Context()
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	tests := []struct {
		name        string
		traceparent string
		tracestate  string
	}{
		{
			name:        "invalid_traceparent_format",
			traceparent: "invalid-trace-parent",
			tracestate:  "",
		},
		{
			name:        "empty_traceparent",
			traceparent: "",
			tracestate:  "congo=t61rcWkgMzE",
		},
		{
			name:        "malformed_traceparent",
			traceparent: "00-short-trace-01",
			tracestate:  "",
		},
		{
			name:        "only_tracestate",
			traceparent: "",
			tracestate:  "congo=t61rcWkgMzE,rojo=00f067aa0ba902b7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)

			if tt.traceparent != "" {
				req.Header.Set(gobrickshttp.HeaderTraceParent, tt.traceparent)
			}
			if tt.tracestate != "" {
				req.Header.Set(gobrickshttp.HeaderTraceState, tt.tracestate)
			}

			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			require.NotNil(t, capturedContext)

			// Should still generate a trace ID
			traceID, okID := gobrickshttp.TraceIDFromContext(capturedContext)
			assert.True(t, okID, "Should have trace ID even with invalid headers")
			assert.NotEmpty(t, traceID, "Should generate trace ID even with invalid headers")

			// Should propagate headers as-is (no validation in middleware)
			contextTraceparent, okParent := gobrickshttp.TraceParentFromContext(capturedContext)
			contextTracestate, okState := gobrickshttp.TraceStateFromContext(capturedContext)

			if tt.traceparent != "" {
				assert.True(t, okParent, "Traceparent should be present when provided")
				assert.Equal(t, tt.traceparent, contextTraceparent)
			} else {
				assert.False(t, okParent, "Traceparent should not be present when not provided")
			}

			if tt.tracestate != "" {
				assert.True(t, okState, "Tracestate should be present when provided")
				assert.Equal(t, tt.tracestate, contextTracestate)
			} else {
				assert.False(t, okState, "Tracestate should not be present when not provided")
			}
		})
	}
}

func TestTraceContextConcurrentRequests(t *testing.T) {
	e := echo.New()
	e.Use(TraceContext())

	type requestResult struct {
		traceID     string
		traceparent string
	}

	results := make(chan requestResult, 10)

	e.GET("/test", func(c echo.Context) error {
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
			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
			req.Header.Set(gobrickshttp.HeaderTraceParent, traceparent)
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)
		}(i, tp)
	}

	// Also send requests without traceparent
	for i := 0; i < 5; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
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
