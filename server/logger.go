package server

import (
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog"

	gobrickshttp "github.com/gaborage/go-bricks/http"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// LoggerConfig configures the request logging middleware with dual-mode logging support.
type LoggerConfig struct {
	// HealthPath specifies the health probe endpoint to exclude from logging
	HealthPath string

	// ReadyPath specifies the readiness probe endpoint to exclude from logging
	ReadyPath string

	// SlowRequestThreshold defines the latency threshold for marking requests as slow (WARN severity)
	// Requests exceeding this duration are logged with result_code="WARN" even if HTTP status is 2xx
	SlowRequestThreshold time.Duration
}

// Logger returns a request logging middleware using the default configuration.
// It logs HTTP requests with OpenTelemetry semantic conventions and dual-mode logging support.
//
// Deprecated: Use LoggerWithConfig for more control over logging behavior.
func Logger(log logger.Logger, healthPath, readyPath string) echo.MiddlewareFunc {
	return LoggerWithConfig(log, LoggerConfig{
		HealthPath:           healthPath,
		ReadyPath:            readyPath,
		SlowRequestThreshold: 1 * time.Second, // Default threshold
	})
}

// LoggerWithConfig returns a request logging middleware with custom configuration.
// It implements dual-mode logging:
//   - Action logs (log.type="action"): Request summaries for INFO-level requests
//   - Trace logs (log.type="trace"): Application debug logs (WARN+ only)
//
// The middleware tracks request-scoped severity escalation. If any log during the request
// lifecycle reaches WARN+, the request is logged as a trace log. Otherwise, a synthesized
// action log summary is emitted at request completion.
func LoggerWithConfig(log logger.Logger, cfg LoggerConfig) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Initialize request logging context
			reqCtx := newRequestLogContext()
			c.Set(RequestLogContextKey, reqCtx)

			// Attach severity hook to request context so WARN+ logs suppress action summaries
			ctxWithHook := logger.WithSeverityHook(c.Request().Context(), reqCtx.escalateSeverity)
			c.SetRequest(c.Request().WithContext(ctxWithHook))

			// Skip probe endpoints from logs
			path := c.Path()
			if path == "" {
				path = c.Request().URL.Path
			}
			if path == cfg.HealthPath || path == cfg.ReadyPath {
				return next(c)
			}

			// Execute request
			err := next(c)

			// Calculate request latency (thread-safe read)
			latency := time.Since(reqCtx.getStartTime())
			status := c.Response().Status

			// Update peak severity based on final HTTP status
			updateSeverityFromStatus(reqCtx, status, err)

			// Emit action log summary if:
			// 1. No explicit WARN+ logs were emitted during request execution, OR
			// 2. The final status resulted in a WARN/ERROR (4xx/5xx) even if no explicit logs occurred
			//
			// This ensures that error responses always produce a final log entry, either as:
			// - An action log with WARN/ERROR severity (if no explicit logs during execution)
			// - Trace logs already emitted during request lifecycle (if explicit WARN+ logs occurred)
			shouldLogActionSummary := !reqCtx.hadExplicitWarningOccurred()
			if shouldLogActionSummary {
				logActionSummary(c, log, cfg, reqCtx, latency, status, err)
			}

			return err
		}
	}
}

// updateSeverityFromStatus escalates severity based on HTTP status code and error.
// Uses escalateSeverityFromStatus to avoid marking status-derived warnings as explicit.
func updateSeverityFromStatus(reqCtx *requestLogContext, status int, err error) {
	if status >= 500 || (err != nil && status == 0) {
		reqCtx.escalateSeverityFromStatus(zerolog.ErrorLevel)
	} else if status >= 400 {
		reqCtx.escalateSeverityFromStatus(zerolog.WarnLevel)
	}
}

// logActionSummary emits a synthesized action log with OpenTelemetry semantic conventions.
// This provides a high-level request summary (100% sampling) for healthy requests.
func logActionSummary(
	c echo.Context,
	log logger.Logger,
	cfg LoggerConfig,
	_ *requestLogContext,
	latency time.Duration,
	status int,
	err error,
) {
	ctx := c.Request().Context()
	contextLog := log.WithContext(ctx)

	// Determine log severity and result_code based on status + latency
	logLevel, resultCode := determineSeverity(status, latency, cfg.SlowRequestThreshold, err)

	// Create log event with appropriate severity
	event := createLogEvent(contextLog, logLevel)

	// Add error if present
	if err != nil {
		event = event.Err(err)
	}

	// Tenant context
	if tenantID, _ := multitenant.GetTenant(ctx); tenantID != "" {
		event = event.Str("tenant", tenantID)
	}

	// Get operational counters from request context
	amqpCount := logger.GetAMQPCounter(ctx)
	dbCount := logger.GetDBCounter(ctx)
	amqpElapsed := logger.GetAMQPElapsed(ctx)
	dbElapsed := logger.GetDBElapsed(ctx)

	// Resolve trace ID for correlation
	traceID := getTraceID(c)

	// SAFETY: Response may be nil after timeout, safely extract traceparent
	traceparent := ""
	if resp := c.Response(); resp != nil {
		traceparent = resp.Header().Get(gobrickshttp.HeaderTraceParent)
	}

	// Get request details
	method := c.Request().Method
	uri := c.Request().URL.Path
	requestID := c.Response().Header().Get(echo.HeaderXRequestID)
	route := c.Path() // Echo route pattern (e.g., /api/users/:id)

	// Emit action log with OpenTelemetry HTTP semantic conventions
	event.
		Str("log.type", "action"). // Mark as action log for dual-mode routing
		Str("request_id", requestID).
		Str("correlation_id", traceID).
		Str("http.request.method", method).
		Int("http.response.status_code", status).
		Int64("http.server.request.duration", latency.Nanoseconds()). // OTel uses nanoseconds
		Str("url.path", uri).
		Str("http.route", route).
		Str("client.address", c.RealIP()).
		Str("user_agent.original", c.Request().UserAgent()).
		Str("result_code", resultCode).
		Int64("amqp_published", amqpCount).
		Int64("amqp_elapsed", amqpElapsed).
		Int64("db_queries", dbCount).
		Int64("db_elapsed", dbElapsed).
		Str("traceparent", traceparent).
		Msg(createActionMessage(method, uri, latency, status))
}

// determineSeverity calculates log severity and result_code based on HTTP status, latency, and errors.
// Returns (logLevel, resultCode) tuple.
func determineSeverity(
	status int,
	latency, threshold time.Duration,
	err error,
) (logLevel, resultCode string) {
	const (
		levelError = "error"
		levelWarn  = "warn"
		levelInfo  = "info"
		codeError  = "ERROR"
		codeWarn   = "WARN"
		codeInfo   = "INFO"
	)

	// ERROR: 5xx status or unhandled error
	if status >= 500 || (err != nil && status == 0) {
		return levelError, codeError
	}

	// WARN: 4xx status
	if status >= 400 {
		return levelWarn, codeWarn
	}

	// WARN (result_code): Slow request (latency exceeds threshold)
	// Log level stays INFO, but result_code is WARN for filtering slow requests
	// Only check if threshold is positive (zero or negative disables slow request detection)
	if threshold > 0 && latency > threshold {
		return levelInfo, codeWarn
	}

	// INFO: Normal successful request
	return levelInfo, codeInfo
}

// createLogEvent creates a log event with the specified severity level.
func createLogEvent(log logger.Logger, level string) logger.LogEvent {
	switch level {
	case "error":
		return log.Error()
	case "warn":
		return log.Warn()
	case "info":
		return log.Info()
	default:
		return log.Info()
	}
}

// createActionMessage generates a human-readable message for action logs.
// Example: "GET /api/users completed in 123ms with status 200"
func createActionMessage(method, path string, latency time.Duration, status int) string {
	return method + " " + path + " completed in " + latency.String() + " with status " + string(rune('0'+status/100)) + "xx"
}
