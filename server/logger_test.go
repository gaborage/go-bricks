package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

const (
	logLevelMismatchMsg = "log level mismatch"
	resultCodeMismatch  = "result code mismatch"
)

// recLogger is a minimal fake logger capturing the last event fields
type recLogger struct{ last *recEvent }

type recEvent struct{ fields map[string]string }

func (r *recLogger) noop() logger.LogEvent {
	r.last = &recEvent{fields: map[string]string{}}
	return r.last
}

func (r *recLogger) Info() logger.LogEvent {
	return r.noop()
}
func (r *recLogger) Error() logger.LogEvent {
	return r.noop()
}
func (r *recLogger) Debug() logger.LogEvent {
	return r.noop()
}
func (r *recLogger) Warn() logger.LogEvent {
	return r.noop()
}
func (r *recLogger) Fatal() logger.LogEvent {
	return r.noop()
}
func (r *recLogger) WithContext(_ any) logger.Logger           { return r }
func (r *recLogger) WithFields(_ map[string]any) logger.Logger { return r }

func (e *recEvent) Msg(_ string) {
	// No-op
}
func (e *recEvent) Msgf(_ string, _ ...any) {
	// No-op
}
func (e *recEvent) Err(_ error) logger.LogEvent                   { return e }
func (e *recEvent) Str(k, v string) logger.LogEvent               { e.fields[k] = v; return e }
func (e *recEvent) Int(_ string, _ int) logger.LogEvent           { return e }
func (e *recEvent) Int64(_ string, _ int64) logger.LogEvent       { return e }
func (e *recEvent) Uint64(_ string, _ uint64) logger.LogEvent     { return e }
func (e *recEvent) Dur(_ string, _ time.Duration) logger.LogEvent { return e }
func (e *recEvent) Interface(_ string, _ any) logger.LogEvent     { return e }
func (e *recEvent) Bytes(_ string, _ []byte) logger.LogEvent      { return e }

// Test that the request logger logs the same correlation_id as the response meta.traceId
func TestRequestLoggerUsesSameCorrelationIDAsResponse(t *testing.T) {
	e := echo.New()
	recLog := &recLogger{}
	e.Use(Logger(recLog, testHealthPath, testReadyPath))

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

	// The logger should have captured the same correlation_id
	require.NotNil(t, recLog.last)
	require.Equal(t, traceID, recLog.last.fields["correlation_id"])
}

func TestRequestLoggerLogsTraceparentWhenInboundPresent(t *testing.T) {
	e := echo.New()
	recLog := &recLogger{}
	e.Use(Logger(recLog, testHealthPath, testReadyPath))

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

func TestRequestLoggerSkipsHealthAndReady(t *testing.T) {
	e := echo.New()
	recLog := &recLogger{}
	e.Use(Logger(recLog, testHealthPath, testReadyPath))

	e.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	e.GET("/ready", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
	})

	api := e.Group("/api")
	api.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	api.GET("/ready", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
	})

	// Test both configured probe paths and ensure suffix matching no longer applies
	for _, path := range []string{testHealthPath, testReadyPath} {
		req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		rec := httptest.NewRecorder()

		// If the middleware logs, recLog.last will be non-nil; reset before each request
		recLog.last = nil
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Nil(t, recLog.last, "configured probe endpoints should not be logged")
	}

	// Test that paths with health/ready suffixes are now logged (no more suffix matching)
	for _, path := range []string{"/api/health", "/api/ready"} {
		req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		rec := httptest.NewRecorder()

		recLog.last = nil
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.NotNil(t, recLog.last, "non-probe endpoints with health/ready suffix should be logged")
	}
}

// TestRequestLoggerEmitsActionLogFor4xxResponsesWithoutExplicitLogs verifies that
// 4xx/5xx responses produce an action log summary even when no explicit WARN+ logs
// were emitted during request execution.
func TestRequestLoggerEmitsActionLogFor4xxResponsesWithoutExplicitLogs(t *testing.T) {
	e := echo.New()
	recLog := &recLogger{}
	e.Use(Logger(recLog, testHealthPath, testReadyPath))

	// Handler returns 404 without emitting any explicit logs
	e.GET("/notfound", func(c echo.Context) error {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	})

	// Handler returns 500 without emitting any explicit logs
	e.GET("/error", func(c echo.Context) error {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "server error"})
	})

	tests := []struct {
		name           string
		path           string
		expectedStatus int
		expectedType   string
	}{
		{
			name:           "404 response emits action log",
			path:           "/notfound",
			expectedStatus: http.StatusNotFound,
			expectedType:   "action",
		},
		{
			name:           "500 response emits action log",
			path:           "/error",
			expectedStatus: http.StatusInternalServerError,
			expectedType:   "action",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, http.NoBody)
			rec := httptest.NewRecorder()

			recLog.last = nil
			e.ServeHTTP(rec, req)

			require.Equal(t, tt.expectedStatus, rec.Code)
			require.NotNil(t, recLog.last, "error responses should emit action log summary")
			require.Equal(t, tt.expectedType, recLog.last.fields["log.type"], "log should be marked as action log")
		})
	}
}

// TestRequestLoggerSuppressesActionLogWhenExplicitWarningLogged verifies that
// if a WARN+ log is explicitly emitted during request execution, the final
// action summary is suppressed (since trace logs already captured the issue).
func TestRequestLoggerSuppressesActionLogWhenExplicitWarningLogged(t *testing.T) {
	e := echo.New()
	recLog := &recLogger{}
	e.Use(Logger(recLog, testHealthPath, testReadyPath))

	// Handler explicitly logs a warning, then returns 200
	e.GET("/explicit-warn", func(c echo.Context) error {
		reqCtx := getRequestLogContext(c)
		// Simulate an explicit WARN log being emitted during request
		reqCtx.escalateSeverity(zerolog.WarnLevel)
		return c.JSON(http.StatusOK, map[string]string{"ok": "yes"})
	})

	req := httptest.NewRequest(http.MethodGet, "/explicit-warn", http.NoBody)
	rec := httptest.NewRecorder()

	recLog.last = nil
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	// Since an explicit WARN was logged, action summary should be suppressed
	require.Nil(t, recLog.last, "action log should be suppressed when explicit WARN+ log occurred")
}

// TestRequestLogContextConcurrency verifies that requestLogContext is thread-safe
// when accessed from multiple goroutines concurrently (e.g., severity hooks from async work).
// This test will fail with -race if proper synchronization is missing.
func TestRequestLogContextConcurrency(t *testing.T) {
	reqCtx := newRequestLogContext()

	// Simulate concurrent access from multiple goroutines
	// (e.g., severity hook callbacks from async request handlers)
	const numGoroutines = 10
	const numIterations = 100

	done := make(chan struct{})

	// Writers: Concurrent escalateSeverity calls
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer func() { done <- struct{}{} }()

			for j := 0; j < numIterations; j++ {
				// Alternate between different severity levels and explicit/status escalations
				if j%2 == 0 {
					reqCtx.escalateSeverity(zerolog.WarnLevel)
				} else {
					reqCtx.escalateSeverityFromStatus(zerolog.ErrorLevel)
				}
			}
		}()
	}

	// Readers: Concurrent reads of fields
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer func() { done <- struct{}{} }()

			for j := 0; j < numIterations; j++ {
				// Read operations that would race without proper locking
				_ = reqCtx.getStartTime()
				_ = reqCtx.hadExplicitWarningOccurred()
			}
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines*2; i++ {
		<-done
	}

	// Verify state is consistent after all concurrent operations
	require.True(t, reqCtx.hadExplicitWarningOccurred(), "Should have explicit warning after concurrent escalations")
}

// TestEscalateSeverityConcurrency verifies that the public EscalateSeverity function
// is thread-safe when called from multiple goroutines.
func TestEscalateSeverityConcurrency(t *testing.T) {
	e := echo.New()
	e.Use(LoggerWithConfig(logger.New("info", false), LoggerConfig{
		SlowRequestThreshold: 100 * time.Millisecond,
	}))

	// Handler that spawns multiple goroutines calling EscalateSeverity
	e.GET("/concurrent", func(c echo.Context) error {
		const numGoroutines = 20
		done := make(chan struct{})

		for i := 0; i < numGoroutines; i++ {
			go func() {
				defer func() { done <- struct{}{} }()
				// Simulate concurrent severity escalations from async work
				EscalateSeverity(c, zerolog.WarnLevel)
			}()
		}

		// Wait for all goroutines
		for i := 0; i < numGoroutines; i++ {
			<-done
		}

		return c.JSON(http.StatusOK, map[string]string{"ok": "concurrent"})
	})

	req := httptest.NewRequest(http.MethodGet, "/concurrent", http.NoBody)
	rec := httptest.NewRecorder()

	// This test primarily validates no race conditions with -race flag
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestDetermineSeveritySlowRequestDisabled verifies that zero or negative
// threshold disables slow request detection (all requests return INFO/INFO).
func TestDetermineSeveritySlowRequestDisabled(t *testing.T) {
	tests := []struct {
		name      string
		threshold time.Duration
		latency   time.Duration
		wantLevel string
		wantCode  string
	}{
		{
			name:      "zero threshold with fast request",
			threshold: 0,
			latency:   10 * time.Millisecond,
			wantLevel: "info",
			wantCode:  "INFO",
		},
		{
			name:      "zero threshold with slow request",
			threshold: 0,
			latency:   5 * time.Second, // Very slow, but threshold is disabled
			wantLevel: "info",
			wantCode:  "INFO", // Should NOT be WARN
		},
		{
			name:      "negative threshold with slow request",
			threshold: -1 * time.Second,
			latency:   10 * time.Second,
			wantLevel: "info",
			wantCode:  "INFO", // Should NOT be WARN
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, code := determineSeverity(200, tt.latency, tt.threshold, nil)
			require.Equal(t, tt.wantLevel, level, logLevelMismatchMsg)
			require.Equal(t, tt.wantCode, code, resultCodeMismatch)
		})
	}
}

// TestDetermineSeveritySlowRequestEnabled verifies that positive threshold
// enables slow request detection and marks slow requests with WARN result_code.
func TestDetermineSeveritySlowRequestEnabled(t *testing.T) {
	threshold := 100 * time.Millisecond

	tests := []struct {
		name      string
		latency   time.Duration
		wantLevel string
		wantCode  string
	}{
		{
			name:      "fast request below threshold",
			latency:   50 * time.Millisecond,
			wantLevel: "info",
			wantCode:  "INFO",
		},
		{
			name:      "request exactly at threshold",
			latency:   100 * time.Millisecond,
			wantLevel: "info",
			wantCode:  "INFO", // Equal is not greater
		},
		{
			name:      "slow request above threshold",
			latency:   150 * time.Millisecond,
			wantLevel: "info", // Log level stays INFO
			wantCode:  "WARN", // But result_code is WARN
		},
		{
			name:      "very slow request",
			latency:   1 * time.Second,
			wantLevel: "info",
			wantCode:  "WARN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, code := determineSeverity(200, tt.latency, threshold, nil)
			require.Equal(t, tt.wantLevel, level, logLevelMismatchMsg)
			require.Equal(t, tt.wantCode, code, resultCodeMismatch)
		})
	}
}

// TestDetermineSeverityPrecedence verifies that HTTP status takes precedence
// over slow request detection.
func TestDetermineSeverityPrecedence(t *testing.T) {
	threshold := 100 * time.Millisecond
	slowLatency := 500 * time.Millisecond

	tests := []struct {
		name      string
		status    int
		err       error
		wantLevel string
		wantCode  string
	}{
		{
			name:      "500 error overrides slow request",
			status:    500,
			err:       nil,
			wantLevel: "error",
			wantCode:  "ERROR",
		},
		{
			name:      "400 warning overrides slow request",
			status:    400,
			err:       nil,
			wantLevel: "warn",
			wantCode:  "WARN",
		},
		{
			name:      "unhandled error with zero status",
			status:    0,
			err:       http.ErrServerClosed,
			wantLevel: "error",
			wantCode:  "ERROR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, code := determineSeverity(tt.status, slowLatency, threshold, tt.err)
			require.Equal(t, tt.wantLevel, level, logLevelMismatchMsg)
			require.Equal(t, tt.wantCode, code, resultCodeMismatch)
		})
	}
}

// TestFormatStatusForMessage verifies proper formatting of status codes for log messages.
// Standard HTTP codes (100-599) are formatted as "Nxx", while non-standard codes
// use the full status number.
func TestFormatStatusForMessage(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		// Edge case: status = 0 (no response)
		{
			name:   "zero status code",
			status: 0,
			want:   "0",
		},
		// Edge case: status < 100 (invalid)
		{
			name:   "status code 99",
			status: 99,
			want:   "99",
		},
		// Standard HTTP status codes (100-599)
		{
			name:   "informational 1xx",
			status: 100,
			want:   "1xx",
		},
		{
			name:   "informational 101",
			status: 101,
			want:   "1xx",
		},
		{
			name:   "success 200",
			status: 200,
			want:   "2xx",
		},
		{
			name:   "success 201",
			status: 201,
			want:   "2xx",
		},
		{
			name:   "redirect 301",
			status: 301,
			want:   "3xx",
		},
		{
			name:   "client error 400",
			status: 400,
			want:   "4xx",
		},
		{
			name:   "client error 404",
			status: 404,
			want:   "4xx",
		},
		{
			name:   "server error 500",
			status: 500,
			want:   "5xx",
		},
		{
			name:   "server error 503",
			status: 503,
			want:   "5xx",
		},
		{
			name:   "boundary 599",
			status: 599,
			want:   "5xx",
		},
		// Edge case: status >= 600 (non-standard)
		{
			name:   "non-standard 600",
			status: 600,
			want:   "600",
		},
		{
			name:   "non-standard 999",
			status: 999,
			want:   "999",
		},
		{
			name:   "very large status code",
			status: 1234,
			want:   "1234",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatStatusForMessage(tt.status)
			require.Equal(t, tt.want, got, "unexpected status format")
		})
	}
}

// TestCreateActionMessage verifies the complete message formatting including
// method, path, latency, and status code handling.
func TestCreateActionMessage(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		path    string
		latency time.Duration
		status  int
		want    string
	}{
		{
			name:    "standard success request",
			method:  "GET",
			path:    "/api/users",
			latency: 123 * time.Millisecond,
			status:  200,
			want:    "GET /api/users completed in 123ms with status 2xx",
		},
		{
			name:    "post request with 201",
			method:  "POST",
			path:    "/api/users",
			latency: 456 * time.Millisecond,
			status:  201,
			want:    "POST /api/users completed in 456ms with status 2xx",
		},
		{
			name:    "client error 404",
			method:  "GET",
			path:    "/api/not-found",
			latency: 50 * time.Millisecond,
			status:  404,
			want:    "GET /api/not-found completed in 50ms with status 4xx",
		},
		{
			name:    "server error 500",
			method:  "POST",
			path:    "/api/error",
			latency: 100 * time.Millisecond,
			status:  500,
			want:    "POST /api/error completed in 100ms with status 5xx",
		},
		{
			name:    "edge case: zero status",
			method:  "GET",
			path:    "/api/timeout",
			latency: 5 * time.Second,
			status:  0,
			want:    "GET /api/timeout completed in 5s with status 0",
		},
		{
			name:    "edge case: status 99",
			method:  "GET",
			path:    "/api/invalid",
			latency: 10 * time.Millisecond,
			status:  99,
			want:    "GET /api/invalid completed in 10ms with status 99",
		},
		{
			name:    "edge case: status 600",
			method:  "GET",
			path:    "/api/custom",
			latency: 200 * time.Millisecond,
			status:  600,
			want:    "GET /api/custom completed in 200ms with status 600",
		},
		{
			name:    "edge case: very large status",
			method:  "DELETE",
			path:    "/api/weird",
			latency: 1 * time.Millisecond,
			status:  999,
			want:    "DELETE /api/weird completed in 1ms with status 999",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := createActionMessage(tt.method, tt.path, tt.latency, tt.status)
			require.Equal(t, tt.want, got, "unexpected action message format")
		})
	}
}
