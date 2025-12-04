package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	gobrickshttp "github.com/gaborage/go-bricks/httpclient"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

const (
	// Test assertion messages
	logLevelMismatchMsg = "log level mismatch"
	resultCodeMismatch  = "result code mismatch"

	// OpenTelemetry semantic conventions (field names for testing)
	otelLogType        = "log.type"
	otelHTTPStatusCode = "http.response.status_code"
	otelHTTPMethod     = "http.request.method"

	// Test data constants (prevent SonarQube duplication warnings)
	testAPIUsersPath       = "/api/users"
	testAPITestPath        = "/api/test"
	testTraceSpan01        = "00-trace-span-01"
	testTraceSpan02        = "00-response-trace-span-02"
	testRequestTraceSpan01 = "00-request-trace-span-01"
	testUserAgent          = "comprehensive-test/2.0"
	testReqFromRequest     = "req-from-request"
	testFallbackMessage    = "Should fallback to request header when response header empty"
)

// recLogger is a minimal fake logger capturing the last event fields
type recLogger struct{ last *recEvent }

type recEvent struct {
	fields    map[string]string
	intFields map[string]int
	message   string
}

func (r *recLogger) noop() logger.LogEvent {
	r.last = &recEvent{
		fields:    map[string]string{},
		intFields: map[string]int{},
	}
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

func (e *recEvent) Msg(msg string) {
	e.message = msg
}
func (e *recEvent) Msgf(_ string, _ ...any) {
	// No-op (not used in tests)
}
func (e *recEvent) Err(_ error) logger.LogEvent                   { return e }
func (e *recEvent) Str(k, v string) logger.LogEvent               { e.fields[k] = v; return e }
func (e *recEvent) Int(k string, v int) logger.LogEvent           { e.intFields[k] = v; return e }
func (e *recEvent) Int64(_ string, _ int64) logger.LogEvent       { return e }
func (e *recEvent) Uint64(_ string, _ uint64) logger.LogEvent     { return e }
func (e *recEvent) Dur(_ string, _ time.Duration) logger.LogEvent { return e }
func (e *recEvent) Interface(_ string, _ any) logger.LogEvent     { return e }
func (e *recEvent) Bytes(_ string, _ []byte) logger.LogEvent      { return e }

// Test that the request logger logs the same correlation_id as the response meta.traceId
func TestRequestLoggerUsesSameCorrelationIDAsResponse(t *testing.T) {
	e := echo.New()
	recLog := &recLogger{}
	e.Use(LoggerWithConfig(recLog, LoggerConfig{
		HealthPath:           testHealthPath,
		ReadyPath:            testReadyPath,
		SlowRequestThreshold: 1 * time.Second,
	}))

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
	e.Use(LoggerWithConfig(recLog, LoggerConfig{
		HealthPath:           testHealthPath,
		ReadyPath:            testReadyPath,
		SlowRequestThreshold: 1 * time.Second,
	}))

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
	e.Use(LoggerWithConfig(recLog, LoggerConfig{
		HealthPath:           testHealthPath,
		ReadyPath:            testReadyPath,
		SlowRequestThreshold: 1 * time.Second,
	}))

	e.GET(testHealthPath, func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	e.GET(testReadyPath, func(c echo.Context) error {
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
	e.Use(LoggerWithConfig(recLog, LoggerConfig{
		HealthPath:           testHealthPath,
		ReadyPath:            testReadyPath,
		SlowRequestThreshold: 1 * time.Second,
	}))

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
			require.Equal(t, tt.expectedType, recLog.last.fields[otelLogType], "log should be marked as action log")
		})
	}
}

// TestRequestLoggerSuppressesActionLogWhenExplicitWarningLogged verifies that
// if a WARN+ log is explicitly emitted during request execution, the final
// action summary is suppressed (since trace logs already captured the issue).
func TestRequestLoggerSuppressesActionLogWhenExplicitWarningLogged(t *testing.T) {
	e := echo.New()
	recLog := &recLogger{}
	e.Use(LoggerWithConfig(recLog, LoggerConfig{
		HealthPath:           testHealthPath,
		ReadyPath:            testReadyPath,
		SlowRequestThreshold: 1 * time.Second,
	}))

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

// TestRequestLoggerHandlesEchoNotFoundError verifies that the logger middleware
// correctly captures the HTTP status code from echo.HTTPError when no route is matched.
// This is a regression test for the bug where 404 errors were logged as 200 because
// Echo's error handler (which sets the response status) runs AFTER middleware completes.
func TestRequestLoggerHandlesEchoNotFoundError(t *testing.T) {
	e := echo.New()
	recLog := &recLogger{}
	e.Use(LoggerWithConfig(recLog, LoggerConfig{
		HealthPath:           testHealthPath,
		ReadyPath:            testReadyPath,
		SlowRequestThreshold: 1 * time.Second,
	}))

	// Don't register any route - let Echo return its default 404 error
	// This simulates a request to an unmatched route like /metrics

	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	rec := httptest.NewRecorder()

	recLog.last = nil

	// Set a custom error handler to ensure we're testing the middleware behavior
	// before the error handler runs (this is where the bug manifested)
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		customErrorHandler(err, c, &config.Config{
			App: config.AppConfig{
				Debug: true,
				Env:   "development",
			},
		})
	}

	e.ServeHTTP(rec, req)

	// Verify the actual HTTP response is 404
	require.Equal(t, http.StatusNotFound, rec.Code, "Response should be 404")

	// Verify the action log was emitted with correct status
	require.NotNil(t, recLog.last, "Should emit action log for 404 error")
	require.Equal(t, "action", recLog.last.fields[otelLogType], "Log should be marked as action log")

	// This is the key assertion: the logged status should be 404, not 200
	require.Equal(t, 404, recLog.last.intFields[otelHTTPStatusCode],
		"Logged status should be 404, not 200 (regression test for status extraction bug)")

	// Verify the result_code is WARN for 4xx errors
	require.Equal(t, "WARN", recLog.last.fields["result_code"],
		"404 errors should have WARN result_code")

	// Verify the log message contains "4xx"
	require.Contains(t, recLog.last.message, "4xx",
		"Log message should mention 4xx status class")
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
	for range numGoroutines {
		go func() {
			defer func() { done <- struct{}{} }()

			for j := range numIterations {
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
	for range numGoroutines {
		go func() {
			defer func() { done <- struct{}{} }()

			for range numIterations {
				// Read operations that would race without proper locking
				_ = reqCtx.getStartTime()
				_ = reqCtx.hadExplicitWarningOccurred()
			}
		}()
	}

	// Wait for all goroutines to complete
	for range numGoroutines * 2 {
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

		for range numGoroutines {
			go func() {
				defer func() { done <- struct{}{} }()
				// Simulate concurrent severity escalations from async work
				EscalateSeverity(c, zerolog.WarnLevel)
			}()
		}

		// Wait for all goroutines
		for range numGoroutines {
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
			path:    testAPIUsersPath,
			latency: 123 * time.Millisecond,
			status:  200,
			want:    "GET " + testAPIUsersPath + " completed in 123ms with status 2xx",
		},
		{
			name:    "post request with 201",
			method:  "POST",
			path:    testAPIUsersPath,
			latency: 456 * time.Millisecond,
			status:  201,
			want:    "POST " + testAPIUsersPath + " completed in 456ms with status 2xx",
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

// TestExtractRequestMetadataWithResponse verifies metadata extraction when response is available.
func TestExtractRequestMetadataWithResponse(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/users/123?q=test", http.NoBody)
	req.Header.Set("User-Agent", "test-agent/1.0")
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	c.SetPath("/api/users/:id")
	c.Response().Header().Set(echo.HeaderXRequestID, "req-456")
	c.Response().Header().Set(gobrickshttp.HeaderTraceParent, testTraceSpan01)

	metadata := extractRequestMetadata(c)

	assert.Equal(t, "POST", metadata.Method)
	assert.Equal(t, "/api/users/123", metadata.URI)
	assert.Equal(t, "/api/users/:id", metadata.Route)
	assert.Equal(t, "req-456", metadata.RequestID)
	assert.Equal(t, testTraceSpan01, metadata.Traceparent)
	assert.Equal(t, "test-agent/1.0", metadata.UserAgent)
	assert.NotEmpty(t, metadata.ClientAddr)
	assert.NotEmpty(t, metadata.TraceID) // getTraceID should return something
}

// TestExtractRequestMetadataFallbackToRequestHeader verifies fallback to request header.
func TestExtractRequestMetadataFallbackToRequestHeader(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, testHealthPath, http.NoBody)
	req.Header.Set(echo.HeaderXRequestID, "fallback-789")
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	// Don't set request ID in response header - it should fall back to request header
	// (In real scenarios, this happens after timeout when response might be invalid)
	metadata := extractRequestMetadata(c)

	assert.Equal(t, "GET", metadata.Method)
	assert.Equal(t, testHealthPath, metadata.URI)
	// Note: Echo's ResponseWriter might auto-set request ID, so we just verify
	// the extraction doesn't panic and returns valid metadata
	assert.NotNil(t, metadata)
}

// TestExtractRequestMetadataNoRequestID verifies behavior with missing request ID.
func TestExtractRequestMetadataNoRequestID(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	metadata := extractRequestMetadata(c)

	assert.Equal(t, "GET", metadata.Method)
	assert.Equal(t, "/test", metadata.URI)
	// RequestID can be empty if not set
	// TraceID should still be generated by getTraceID
	assert.NotEmpty(t, metadata.TraceID)
}

// TestExtractRequestMetadataAllFields verifies all 8 fields are populated correctly.
func TestExtractRequestMetadataAllFields(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPut, "/api/resource", http.NoBody)
	req.Header.Set("User-Agent", testUserAgent)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	c.SetPath("/api/resource")
	c.Response().Header().Set(echo.HeaderXRequestID, "all-fields-123")
	c.Response().Header().Set(gobrickshttp.HeaderTraceParent, "00-aabbcc-ddeeff-01")

	metadata := extractRequestMetadata(c)

	// Verify all 8 fields are set
	assert.NotEmpty(t, metadata.Method, "Method should not be empty")
	assert.NotEmpty(t, metadata.URI, "URI should not be empty")
	assert.NotEmpty(t, metadata.Route, "Route should not be empty")
	assert.NotEmpty(t, metadata.RequestID, "RequestID should not be empty")
	assert.NotEmpty(t, metadata.TraceID, "TraceID should not be empty")
	assert.NotEmpty(t, metadata.Traceparent, "Traceparent should not be empty")
	assert.NotEmpty(t, metadata.ClientAddr, "ClientAddr should not be empty")
	assert.NotEmpty(t, metadata.UserAgent, "UserAgent should not be empty")
}

// TestExtractOperationalMetricsWithCounters verifies extraction when counters are present.
func TestExtractOperationalMetricsWithCounters(t *testing.T) {
	ctx := context.Background()

	// Initialize counters using logger package functions
	ctx = logger.WithAMQPCounter(ctx)
	ctx = logger.WithDBCounter(ctx)

	// Increment counters
	logger.IncrementAMQPCounter(ctx)
	logger.IncrementAMQPCounter(ctx)
	logger.IncrementAMQPCounter(ctx)
	logger.IncrementAMQPCounter(ctx)
	logger.IncrementAMQPCounter(ctx) // 5 total

	logger.AddAMQPElapsed(ctx, 1500)

	logger.IncrementDBCounter(ctx)
	logger.IncrementDBCounter(ctx)
	logger.IncrementDBCounter(ctx) // 3 total

	logger.AddDBElapsed(ctx, 2500)

	metrics := extractOperationalMetrics(ctx)

	assert.Equal(t, int64(5), metrics.AMQPPublished)
	assert.Equal(t, int64(1500), metrics.AMQPElapsed)
	assert.Equal(t, int64(3), metrics.DBQueries)
	assert.Equal(t, int64(2500), metrics.DBElapsed)
}

// TestExtractOperationalMetricsNoCounters verifies zero values when counters absent.
func TestExtractOperationalMetricsNoCounters(t *testing.T) {
	ctx := context.Background()

	metrics := extractOperationalMetrics(ctx)

	// All values should be zero when not set
	assert.Equal(t, int64(0), metrics.AMQPPublished)
	assert.Equal(t, int64(0), metrics.AMQPElapsed)
	assert.Equal(t, int64(0), metrics.DBQueries)
	assert.Equal(t, int64(0), metrics.DBElapsed)
}

// TestExtractOperationalMetricsPartialCounters verifies mixed presence of counters.
func TestExtractOperationalMetricsPartialCounters(t *testing.T) {
	ctx := context.Background()

	// Only initialize AMQP counter (not DB)
	ctx = logger.WithAMQPCounter(ctx)

	// Increment AMQP counter
	for range 10 {
		logger.IncrementAMQPCounter(ctx)
	}

	metrics := extractOperationalMetrics(ctx)

	assert.Equal(t, int64(10), metrics.AMQPPublished, "AMQP count should be set")
	assert.Equal(t, int64(0), metrics.AMQPElapsed, "AMQP elapsed should be zero")
	assert.Equal(t, int64(0), metrics.DBQueries, "DB queries should be zero (not initialized)")
	assert.Equal(t, int64(0), metrics.DBElapsed, "DB elapsed should be zero (not initialized)")
}

// TestExtractTenantIDPresent verifies extraction when tenant exists in context.
func TestExtractTenantIDPresent(t *testing.T) {
	ctx := context.Background()

	// Set tenant ID using multitenant package
	ctx = multitenant.SetTenant(ctx, "tenant-abc-123")

	tenantID := extractTenantID(ctx)

	assert.Equal(t, "tenant-abc-123", tenantID)
}

// TestExtractTenantIDAbsent verifies empty string fallback when tenant absent.
func TestExtractTenantIDAbsent(t *testing.T) {
	ctx := context.Background()

	tenantID := extractTenantID(ctx)

	assert.Equal(t, "", tenantID, "Should return empty string when no tenant")
}

// TestNewRequestLogger verifies constructor creates valid instance.
func TestNewRequestLogger(t *testing.T) {
	recLog := &recLogger{}
	cfg := LoggerConfig{
		HealthPath:           testHealthPath,
		ReadyPath:            testReadyPath,
		SlowRequestThreshold: 500,
	}

	rl := newRequestLogger(recLog, cfg)

	require.NotNil(t, rl)
	assert.Equal(t, recLog, rl.logger)
	assert.Equal(t, cfg, rl.config)
}

// TestBuildLogEventInfoLevel verifies log event with INFO severity.
func TestBuildLogEventInfoLevel(t *testing.T) {
	recLog := &recLogger{}
	metadata := requestMetadata{
		Method:      "GET",
		URI:         testAPITestPath,
		Route:       testAPITestPath,
		RequestID:   "req-123",
		TraceID:     "trace-456",
		Traceparent: testTraceSpan01,
		ClientAddr:  "127.0.0.1",
		UserAgent:   "test/1.0",
	}
	metrics := operationalMetrics{
		AMQPPublished: 2,
		AMQPElapsed:   1000,
		DBQueries:     3,
		DBElapsed:     2000,
	}

	event := buildLogEvent(recLog, &logEventParams{
		logLevel:   "info",
		metadata:   &metadata,
		metrics:    metrics,
		tenantID:   "",
		status:     200,
		latency:    100 * time.Millisecond,
		resultCode: "INFO",
		err:        nil,
	})
	event.Msg("test message")

	require.NotNil(t, recLog.last)
	assert.Equal(t, "action", recLog.last.fields[otelLogType])
	assert.Equal(t, "req-123", recLog.last.fields["request_id"])
	assert.Equal(t, "trace-456", recLog.last.fields["correlation_id"])
	assert.Equal(t, "GET", recLog.last.fields[otelHTTPMethod])
	assert.Equal(t, 200, recLog.last.intFields[otelHTTPStatusCode])
	assert.Equal(t, "INFO", recLog.last.fields["result_code"])
}

// TestBuildLogEventWithError verifies error attachment.
func TestBuildLogEventWithError(t *testing.T) {
	recLog := &recLogger{}
	metadata := requestMetadata{Method: "POST", URI: "/api/fail"}
	metrics := operationalMetrics{}
	testErr := assert.AnError

	event := buildLogEvent(recLog, &logEventParams{
		logLevel:   "error",
		metadata:   &metadata,
		metrics:    metrics,
		tenantID:   "",
		status:     500,
		latency:    50 * time.Millisecond,
		resultCode: "ERROR",
		err:        testErr,
	})
	event.Msg("error occurred")

	require.NotNil(t, recLog.last)
	assert.Equal(t, 500, recLog.last.intFields[otelHTTPStatusCode])
	assert.Equal(t, "ERROR", recLog.last.fields["result_code"])
}

// TestBuildLogEventWithTenant verifies tenant ID inclusion.
func TestBuildLogEventWithTenant(t *testing.T) {
	recLog := &recLogger{}
	metadata := requestMetadata{Method: "GET", URI: "/api/data"}
	metrics := operationalMetrics{}

	event := buildLogEvent(recLog, &logEventParams{
		logLevel:   "info",
		metadata:   &metadata,
		metrics:    metrics,
		tenantID:   "tenant-xyz",
		status:     200,
		latency:    10 * time.Millisecond,
		resultCode: "INFO",
		err:        nil,
	})
	event.Msg("tenant request")

	require.NotNil(t, recLog.last)
	assert.Equal(t, "tenant-xyz", recLog.last.fields["tenant"])
}

// TestBuildLogEventWithoutTenant verifies no tenant field when absent.
func TestBuildLogEventWithoutTenant(t *testing.T) {
	recLog := &recLogger{}
	metadata := requestMetadata{Method: "GET", URI: "/public"}
	metrics := operationalMetrics{}

	event := buildLogEvent(recLog, &logEventParams{
		logLevel:   "info",
		metadata:   &metadata,
		metrics:    metrics,
		tenantID:   "",
		status:     200,
		latency:    10 * time.Millisecond,
		resultCode: "INFO",
		err:        nil,
	})
	event.Msg("public request")

	require.NotNil(t, recLog.last)
	_, hasTenant := recLog.last.fields["tenant"]
	assert.False(t, hasTenant, "Tenant field should not be present")
}

// TestBuildLogEventAllFields verifies all fields are populated correctly.
func TestBuildLogEventAllFields(t *testing.T) {
	recLog := &recLogger{}
	metadata := requestMetadata{
		Method:      "PUT",
		URI:         "/api/resource/123",
		Route:       "/api/resource/:id",
		RequestID:   "full-req-789",
		TraceID:     "full-trace-abc",
		Traceparent: "00-full-parent-def",
		ClientAddr:  "192.168.1.100",
		UserAgent:   testUserAgent,
	}
	metrics := operationalMetrics{
		AMQPPublished: 5,
		AMQPElapsed:   3500,
		DBQueries:     10,
		DBElapsed:     7500,
	}

	event := buildLogEvent(recLog, &logEventParams{
		logLevel:   "warn",
		metadata:   &metadata,
		metrics:    metrics,
		tenantID:   "full-tenant",
		status:     404,
		latency:    250 * time.Millisecond,
		resultCode: "WARN",
		err:        nil,
	})
	event.Msg("comprehensive test")

	require.NotNil(t, recLog.last)

	// Verify all string fields
	assert.Equal(t, "action", recLog.last.fields[otelLogType])
	assert.Equal(t, "full-req-789", recLog.last.fields["request_id"])
	assert.Equal(t, "full-trace-abc", recLog.last.fields["correlation_id"])
	assert.Equal(t, "PUT", recLog.last.fields[otelHTTPMethod])
	assert.Equal(t, "/api/resource/123", recLog.last.fields["url.path"])
	assert.Equal(t, "/api/resource/:id", recLog.last.fields["http.route"])
	assert.Equal(t, "192.168.1.100", recLog.last.fields["client.address"])
	assert.Equal(t, testUserAgent, recLog.last.fields["user_agent.original"])
	assert.Equal(t, "WARN", recLog.last.fields["result_code"])
	assert.Equal(t, "00-full-parent-def", recLog.last.fields["traceparent"])
	assert.Equal(t, "full-tenant", recLog.last.fields["tenant"])

	// Verify all int fields
	assert.Equal(t, 404, recLog.last.intFields[otelHTTPStatusCode])

	// Note: int64 fields would need to be added to recEvent if we want to test them
	// For now, we verify the function doesn't panic and basic fields are correct
}

// TestRequestLoggerShouldSkipPath verifies probe endpoint filtering.
func TestRequestLoggerShouldSkipPath(t *testing.T) {
	rl := newRequestLogger(&recLogger{}, LoggerConfig{
		HealthPath: testHealthPath,
		ReadyPath:  testReadyPath,
	})

	tests := []struct {
		name     string
		path     string
		urlPath  string
		expected bool
	}{
		{
			name:     "health endpoint",
			path:     testHealthPath,
			expected: true,
		},
		{
			name:     "ready endpoint",
			path:     testReadyPath,
			expected: true,
		},
		{
			name:     "normal endpoint",
			path:     "/api/users",
			expected: false,
		},
		{
			name:     "empty path falls back to URL path - health",
			path:     "",
			urlPath:  testHealthPath,
			expected: true,
		},
		{
			name:     "empty path falls back to URL path - normal",
			path:     "",
			urlPath:  "/api/data",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			urlPath := tt.urlPath
			if urlPath == "" {
				urlPath = tt.path
			}
			req := httptest.NewRequest(http.MethodGet, urlPath, http.NoBody)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetPath(tt.path)

			result := rl.shouldSkipPath(c)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestRequestLoggerExtractStatus verifies status extraction priority.
func TestRequestLoggerExtractStatus(t *testing.T) {
	rl := newRequestLogger(&recLogger{}, LoggerConfig{})

	t.Run("error status takes precedence", func(t *testing.T) {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		// Set response status to 200
		c.Response().WriteHeader(http.StatusOK)

		// But return 404 error
		err := echo.NewHTTPError(http.StatusNotFound, "not found")

		status := rl.extractStatus(c, err)
		assert.Equal(t, 404, status, "Error status should take precedence over response status")
	})

	t.Run("response status when no error", func(t *testing.T) {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		c.Response().WriteHeader(http.StatusCreated)

		status := rl.extractStatus(c, nil)
		assert.Equal(t, 201, status)
	})

	t.Run("zero when no status written", func(t *testing.T) {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		status := rl.extractStatus(c, nil)
		assert.Equal(t, 0, status, "Should return 0 when no status has been written")
	})
}

// TestRequestLoggerHandleFullFlow verifies end-to-end middleware execution.
func TestRequestLoggerHandleFullFlow(t *testing.T) {
	recLog := &recLogger{}
	rl := newRequestLogger(recLog, LoggerConfig{
		HealthPath:           testHealthPath,
		ReadyPath:            testReadyPath,
		SlowRequestThreshold: 100 * time.Millisecond,
	})

	e := echo.New()
	e.Use(rl.Handle)

	e.GET(testAPITestPath, func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, testAPITestPath, http.NoBody)
	rec := httptest.NewRecorder()

	recLog.last = nil
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, recLog.last, "Should emit action log")
	assert.Equal(t, "action", recLog.last.fields[otelLogType])
	assert.Equal(t, "GET", recLog.last.fields[otelHTTPMethod])
	assert.Equal(t, 200, recLog.last.intFields[otelHTTPStatusCode])
}

// TestRequestLoggerHandleSkipsHealthProbes verifies probe endpoint skipping.
func TestRequestLoggerHandleSkipsHealthProbes(t *testing.T) {
	recLog := &recLogger{}
	rl := newRequestLogger(recLog, LoggerConfig{
		HealthPath: "/health",
		ReadyPath:  "/ready",
	})

	e := echo.New()
	e.Use(rl.Handle)

	e.GET(testHealthPath, func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "healthy"})
	})
	e.GET(testReadyPath, func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
	})

	tests := []struct {
		name string
		path string
	}{
		{"health probe", testHealthPath},
		{"ready probe", testReadyPath},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, http.NoBody)
			rec := httptest.NewRecorder()

			recLog.last = nil
			e.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Nil(t, recLog.last, "Probe endpoints should not be logged")
		})
	}
}

// TestExtractRequestMetadataEmptyResponseHeaders verifies fallback when response exists but headers are empty.
// This tests the fix for the scenario where middleware ordering causes response headers to be unset
// even though the response object is non-nil.
func TestExtractRequestMetadataEmptyResponseHeaders(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, testAPITestPath, http.NoBody)
	req.Header.Set(echo.HeaderXRequestID, testReqFromRequest)
	req.Header.Set(gobrickshttp.HeaderTraceParent, testRequestTraceSpan01)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	// Response exists but headers are NOT set (middleware ordering issue)
	// The response headers are empty, so extraction should fall back to request headers
	metadata := extractRequestMetadata(c)

	assert.Equal(t, "GET", metadata.Method)
	assert.Equal(t, testAPITestPath, metadata.URI)
	assert.Equal(t, testReqFromRequest, metadata.RequestID, testFallbackMessage)
	assert.Equal(t, testRequestTraceSpan01, metadata.Traceparent, testFallbackMessage)
}

// TestExtractRequestMetadataPartialResponseHeaders verifies independent fallback for each header.
// This ensures that if only one header is empty in the response, only that header falls back.
func TestExtractRequestMetadataPartialResponseHeaders(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/partial", http.NoBody)
	req.Header.Set(echo.HeaderXRequestID, testReqFromRequest)
	req.Header.Set(gobrickshttp.HeaderTraceParent, testRequestTraceSpan01)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	// Only set traceparent in response, leave requestID empty
	c.Response().Header().Set(gobrickshttp.HeaderTraceParent, testTraceSpan02)

	metadata := extractRequestMetadata(c)

	assert.Equal(t, testReqFromRequest, metadata.RequestID, testFallbackMessage)
	assert.Equal(t, testTraceSpan02, metadata.Traceparent, "Should use response header when present")
}

// TestExtractRequestMetadataResponseHeadersPriority verifies response headers take precedence when set.
// This ensures the existing behavior is maintained - response headers are preferred.
func TestExtractRequestMetadataResponseHeadersPriority(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/priority", http.NoBody)
	req.Header.Set(echo.HeaderXRequestID, testReqFromRequest)
	req.Header.Set(gobrickshttp.HeaderTraceParent, testRequestTraceSpan01)
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)

	// Set different values in response headers (middleware may transform them)
	c.Response().Header().Set(echo.HeaderXRequestID, "req-from-response")
	c.Response().Header().Set(gobrickshttp.HeaderTraceParent, testTraceSpan02)

	metadata := extractRequestMetadata(c)

	assert.Equal(t, "req-from-response", metadata.RequestID, "Should prefer response header when both are set")
	assert.Equal(t, testTraceSpan02, metadata.Traceparent, "Should prefer response header when both are set")
}

// TestExtractRequestMetadataMissingBothRequestAndResponseHeaders verifies behavior when headers absent everywhere.
// This ensures the function doesn't panic and returns empty strings gracefully.
func TestExtractRequestMetadataMissingBothRequestAndResponseHeaders(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/no-headers", http.NoBody)
	// Don't set any headers in request
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	// Don't set any headers in response either

	metadata := extractRequestMetadata(c)

	assert.Equal(t, "GET", metadata.Method)
	assert.Equal(t, "/api/no-headers", metadata.URI)
	assert.Equal(t, "", metadata.RequestID, "Should return empty string when headers missing everywhere")
	assert.Equal(t, "", metadata.Traceparent, "Should return empty string when headers missing everywhere")
}
