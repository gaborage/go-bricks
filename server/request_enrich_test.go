package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gobrickshttp "github.com/gaborage/go-bricks/httpclient"
	"github.com/gaborage/go-bricks/logger"
)

// TestRequestEnrichInjectsTraceAndCounters verifies the combined middleware seeds
// both the trace context and the per-request counters in a single pass.
func TestRequestEnrichInjectsTraceAndCounters(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var captured context.Context
	handler := RequestEnrich()(func(c *echo.Context) error {
		captured = c.Request().Context()
		logger.IncrementAMQPCounter(captured)
		logger.IncrementDBCounter(captured)
		return nil
	})

	require.NoError(t, handler(c))

	// Trace ID injected for outbound propagation.
	traceID, ok := gobrickshttp.TraceIDFromContext(captured)
	assert.True(t, ok, "trace ID should be present after RequestEnrich")
	assert.NotEmpty(t, traceID)

	// Counters seeded and usable.
	assert.Equal(t, int64(1), logger.GetAMQPCounter(captured))
	assert.Equal(t, int64(1), logger.GetDBCounter(captured))
}

// TestRequestEnrichPropagatesW3CHeaders verifies inbound traceparent/tracestate
// headers are carried into the request context.
func TestRequestEnrichPropagatesW3CHeaders(t *testing.T) {
	const (
		traceparent = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
		tracestate  = "rojo=00f067aa0ba902b7"
	)
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
	req.Header.Set(gobrickshttp.HeaderTraceParent, traceparent)
	req.Header.Set(gobrickshttp.HeaderTraceState, tracestate)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var captured context.Context
	handler := RequestEnrich()(func(c *echo.Context) error {
		captured = c.Request().Context()
		return nil
	})
	require.NoError(t, handler(c))

	gotTP, okTP := gobrickshttp.TraceParentFromContext(captured)
	assert.True(t, okTP)
	assert.Equal(t, traceparent, gotTP)

	gotTS, okTS := gobrickshttp.TraceStateFromContext(captured)
	assert.True(t, okTS)
	assert.Equal(t, tracestate, gotTS)
}

// TestRequestEnrichShortCircuitsOnCancelledContext verifies the canceled-context
// guard: an already-canceled inbound request returns the context error without
// invoking the next handler. (This guard path was previously untested.)
func TestRequestEnrichShortCircuitsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the request runs

	e := echo.New()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/test", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	nextCalled := false
	handler := RequestEnrich()(func(_ *echo.Context) error {
		nextCalled = true
		return nil
	})

	err := handler(c)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, nextCalled, "next handler must not run for an already-canceled context")
}
