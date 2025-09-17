package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

// recLogger is a minimal fake logger capturing the last event fields
type recLogger struct{ last *recEvent }

type recEvent struct{ fields map[string]string }

func (r *recLogger) Info() logger.LogEvent {
	r.last = &recEvent{fields: map[string]string{}}
	return r.last
}
func (r *recLogger) Error() logger.LogEvent {
	r.last = &recEvent{fields: map[string]string{}}
	return r.last
}
func (r *recLogger) Debug() logger.LogEvent {
	r.last = &recEvent{fields: map[string]string{}}
	return r.last
}
func (r *recLogger) Warn() logger.LogEvent {
	r.last = &recEvent{fields: map[string]string{}}
	return r.last
}
func (r *recLogger) Fatal() logger.LogEvent {
	r.last = &recEvent{fields: map[string]string{}}
	return r.last
}
func (r *recLogger) WithContext(_ interface{}) logger.Logger           { return r }
func (r *recLogger) WithFields(_ map[string]interface{}) logger.Logger { return r }

func (e *recEvent) Msg(_ string)                                      {}
func (e *recEvent) Msgf(_ string, _ ...interface{})                   {}
func (e *recEvent) Err(_ error) logger.LogEvent                       { return e }
func (e *recEvent) Str(k, v string) logger.LogEvent                   { e.fields[k] = v; return e }
func (e *recEvent) Int(_ string, _ int) logger.LogEvent               { return e }
func (e *recEvent) Int64(_ string, _ int64) logger.LogEvent           { return e }
func (e *recEvent) Uint64(_ string, _ uint64) logger.LogEvent         { return e }
func (e *recEvent) Dur(_ string, _ time.Duration) logger.LogEvent     { return e }
func (e *recEvent) Interface(_ string, _ interface{}) logger.LogEvent { return e }
func (e *recEvent) Bytes(_ string, _ []byte) logger.LogEvent          { return e }

// Test that the request logger logs the same trace_id as the response meta.traceId
func TestRequestLogger_UsesSameTraceIDAsResponse(t *testing.T) {
	e := echo.New()
	recLog := &recLogger{}
	e.Use(Logger(recLog))

	// Simple handler that emits a success envelope (adds meta with traceId)
	e.GET("/t", func(c echo.Context) error {
		return formatSuccessResponse(c, map[string]string{"ok": "yes"})
	})

	// Provide an inbound request ID so getTraceID returns this value for both logger and meta
	req := httptest.NewRequest(http.MethodGet, "/t", http.NoBody)
	req.Header.Set(echo.HeaderXRequestID, "fixed-req-id")
	rec := httptest.NewRecorder()
	_ = e.NewContext(req, rec)

	// Serve the request through Echo to trigger middleware logging
	e.ServeHTTP(rec, req)

	// Parse response and read meta.traceId
	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	traceID, _ := resp.Meta["traceId"].(string)
	require.NotEmpty(t, traceID)

	// The logger should have captured the same trace_id
	require.NotNil(t, recLog.last)
	require.Equal(t, traceID, recLog.last.fields["trace_id"])
}

func TestRequestLogger_LogsTraceparentWhenInboundPresent(t *testing.T) {
	e := echo.New()
	recLog := &recLogger{}
	e.Use(Logger(recLog))

	// Handler emits success envelope which sets/propagates traceparent on response
	e.GET("/tp", func(c echo.Context) error {
		return formatSuccessResponse(c, map[string]string{"ok": "yes"})
	})

	inboundTP := "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	req := httptest.NewRequest(http.MethodGet, "/tp", http.NoBody)
	req.Header.Set("traceparent", inboundTP)
	rec := httptest.NewRecorder()

	// Serve request
	e.ServeHTTP(rec, req)

	// Logger should have captured the propagated traceparent
	require.NotNil(t, recLog.last)
	require.Equal(t, inboundTP, recLog.last.fields["traceparent"])
}

func TestRequestLogger_SkipsHealthAndReady(t *testing.T) {
	e := echo.New()
	recLog := &recLogger{}
	e.Use(Logger(recLog))

	e.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	e.GET("/ready", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
	})

	for _, path := range []string{"/health", "/ready"} {
		req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		rec := httptest.NewRecorder()

		// If the middleware logs, recLog.last will be non-nil; reset before each request
		recLog.last = nil
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Nil(t, recLog.last, "health-style endpoints should not be logged")
	}
}
