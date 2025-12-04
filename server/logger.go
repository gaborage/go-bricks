package server

import (
	"context"
	goerrors "errors"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog"

	gobrickshttp "github.com/gaborage/go-bricks/httpclient"
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

// requestMetadata bundles HTTP request metadata for logging.
// This reduces parameter passing and improves code clarity.
type requestMetadata struct {
	Method      string
	URI         string
	Route       string
	RequestID   string
	TraceID     string
	Traceparent string
	ClientAddr  string
	UserAgent   string
}

// operationalMetrics bundles operational counters for logging.
// This provides a clear aggregation of AMQP and database metrics.
type operationalMetrics struct {
	AMQPPublished int64
	AMQPElapsed   int64
	DBQueries     int64
	DBElapsed     int64
}

// logEventParams bundles parameters needed to construct a log event.
// This reduces parameter count and improves code organization.
type logEventParams struct {
	logLevel   string
	metadata   *requestMetadata
	metrics    operationalMetrics
	tenantID   string
	status     int
	latency    time.Duration
	resultCode string
	err        error
}

// requestLogger encapsulates logging middleware logic with configuration.
// This struct-based approach reduces nesting and improves testability.
type requestLogger struct {
	logger logger.Logger
	config LoggerConfig
}

// newRequestLogger creates a new request logger with the specified configuration.
func newRequestLogger(log logger.Logger, cfg LoggerConfig) *requestLogger {
	return &requestLogger{
		logger: log,
		config: cfg,
	}
}

// Handle returns the middleware handler function for request logging.
// This method replaces the nested closure pattern with a struct-based approach.
func (rl *requestLogger) Handle(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Initialize request logging context
		reqCtx := newRequestLogContext()
		c.Set(RequestLogContextKey, reqCtx)

		// Attach severity hook to request context so WARN+ logs suppress action summaries
		ctxWithHook := logger.WithSeverityHook(c.Request().Context(), reqCtx.escalateSeverity)
		c.SetRequest(c.Request().WithContext(ctxWithHook))

		// Skip probe endpoints from logs
		if rl.shouldSkipPath(c) {
			return next(c)
		}

		// Execute request
		err := next(c)

		// Calculate request latency (thread-safe read)
		latency := time.Since(reqCtx.getStartTime())

		// Extract status code from error or response
		status := rl.extractStatus(c, err)

		// Update peak severity based on final HTTP status
		updateSeverityFromStatus(reqCtx, status, err)

		// Emit action log summary if no explicit WARN+ logs occurred
		shouldLogActionSummary := !reqCtx.hadExplicitWarningOccurred()
		if shouldLogActionSummary {
			logActionSummary(c, rl.logger, rl.config, latency, status, err)
		}

		return err
	}
}

// shouldSkipPath checks if the request path should be excluded from logging.
// Returns true for health and readiness probe endpoints.
func (rl *requestLogger) shouldSkipPath(c echo.Context) bool {
	path := c.Path()
	if path == "" {
		path = c.Request().URL.Path
	}
	return path == rl.config.HealthPath || path == rl.config.ReadyPath
}

// extractStatus extracts HTTP status code from error or response.
// Prioritizes error status (for unmatched routes) over response status.
func (rl *requestLogger) extractStatus(c echo.Context, err error) int {
	// Try error first (handles Echo's default 404 for unmatched routes)
	if status := extractStatusFromError(err); status != 0 {
		return status
	}

	// Fallback to response status (handles explicit c.JSON(status, ...))
	if resp := c.Response(); resp != nil {
		return resp.Status
	}

	return 0
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
	rl := newRequestLogger(log, cfg)
	return rl.Handle
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
	latency time.Duration,
	status int,
	err error,
) {
	ctx := c.Request().Context()

	// Extract all data using helper functions
	metadata := extractRequestMetadata(c)
	metrics := extractOperationalMetrics(ctx)
	tenantID := extractTenantID(ctx)

	// Determine log severity and result_code
	logLevel, resultCode := determineSeverity(status, latency, cfg.SlowRequestThreshold, err)

	// Build and emit log event
	event := buildLogEvent(log.WithContext(ctx), &logEventParams{
		logLevel:   logLevel,
		metadata:   &metadata,
		metrics:    metrics,
		tenantID:   tenantID,
		status:     status,
		latency:    latency,
		resultCode: resultCode,
		err:        err,
	})

	event.Msg(createActionMessage(metadata.Method, metadata.URI, latency, status))
}

// buildLogEvent constructs a log event with all OpenTelemetry fields populated.
// This function is pure - it takes extracted data and produces a configured log event.
func buildLogEvent(log logger.Logger, params *logEventParams) logger.LogEvent {
	// Create log event with appropriate severity
	event := createLogEvent(log, params.logLevel)

	// Add error if present
	if params.err != nil {
		event = event.Err(params.err)
	}

	// Add tenant context if present
	if params.tenantID != "" {
		event = event.Str("tenant", params.tenantID)
	}

	// Emit action log with OpenTelemetry HTTP semantic conventions
	return event.
		Str("log.type", "action"). // Mark as action log for dual-mode routing
		Str("request_id", params.metadata.RequestID).
		Str("correlation_id", params.metadata.TraceID).
		Str("http.request.method", params.metadata.Method).
		Int("http.response.status_code", params.status).
		Int64("http.server.request.duration", params.latency.Nanoseconds()). // OTel uses nanoseconds
		Str("url.path", params.metadata.URI).
		Str("http.route", params.metadata.Route).
		Str("client.address", params.metadata.ClientAddr).
		Str("user_agent.original", params.metadata.UserAgent).
		Str("result_code", params.resultCode).
		Int64("amqp_published", params.metrics.AMQPPublished).
		Int64("amqp_elapsed", params.metrics.AMQPElapsed).
		Int64("db_queries", params.metrics.DBQueries).
		Int64("db_elapsed", params.metrics.DBElapsed).
		Str("traceparent", params.metadata.Traceparent)
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
// Example: "GET /api/users completed in 123ms with status 2xx"
// For non-standard status codes (< 100 or >= 600), the full status code is used.
func createActionMessage(method, path string, latency time.Duration, status int) string {
	statusStr := formatStatusForMessage(status)
	return method + " " + path + " completed in " + latency.String() + " with status " + statusStr
}

// formatStatusForMessage formats HTTP status codes for log messages.
// Standard codes (100-599) are formatted as "Nxx" (e.g., "2xx", "4xx").
// Non-standard codes return the full status string (e.g., "0", "600").
func formatStatusForMessage(status int) string {
	if status >= 100 && status < 600 {
		return strconv.Itoa(status/100) + "xx"
	}
	return strconv.Itoa(status)
}

// extractRequestMetadata extracts HTTP request metadata from Echo context.
// Handles nil response and empty headers safely by falling back to request headers.
func extractRequestMetadata(c echo.Context) requestMetadata {
	var requestID, traceparent string

	// SAFETY: Try response headers first (may be transformed by middleware),
	// fall back to request headers if response is nil or headers empty.
	// This handles middleware ordering and timeout scenarios.
	if resp := c.Response(); resp != nil {
		requestID = resp.Header().Get(echo.HeaderXRequestID)
		traceparent = resp.Header().Get(gobrickshttp.HeaderTraceParent)
	}

	// Fallback to request headers (always available, source of truth)
	if requestID == "" {
		requestID = c.Request().Header.Get(echo.HeaderXRequestID)
	}
	if traceparent == "" {
		traceparent = c.Request().Header.Get(gobrickshttp.HeaderTraceParent)
	}

	return requestMetadata{
		Method:      c.Request().Method,
		URI:         c.Request().URL.Path,
		Route:       c.Path(), // Echo route pattern (e.g., /api/users/:id)
		RequestID:   requestID,
		TraceID:     getTraceID(c),
		Traceparent: traceparent,
		ClientAddr:  c.RealIP(),
		UserAgent:   c.Request().UserAgent(),
	}
}

// extractOperationalMetrics extracts operational counters from request context.
// Returns zero values if counters are not present.
func extractOperationalMetrics(ctx context.Context) operationalMetrics {
	return operationalMetrics{
		AMQPPublished: logger.GetAMQPCounter(ctx),
		AMQPElapsed:   logger.GetAMQPElapsed(ctx),
		DBQueries:     logger.GetDBCounter(ctx),
		DBElapsed:     logger.GetDBElapsed(ctx),
	}
}

// extractTenantID extracts tenant ID from request context.
// Returns empty string if tenant is not present.
func extractTenantID(ctx context.Context) string {
	//nolint:S8148 // NOSONAR: Error intentionally ignored - empty tenant ID is valid fallback for single-tenant apps
	if tenantID, _ := multitenant.GetTenant(ctx); tenantID != "" {
		return tenantID
	}
	return ""
}

// extractStatusFromError extracts HTTP status code from echo.HTTPError.
// Returns 0 if err is nil or not an echo.HTTPError.
//
// This is necessary because Echo's error handler runs AFTER middleware completes,
// so c.Response().Status may not be set yet when logger middleware reads it.
// By extracting the status from the error directly, we can log the correct status
// even before the error handler updates the response.
func extractStatusFromError(err error) int {
	if err == nil {
		return 0
	}
	var he *echo.HTTPError
	if goerrors.As(err, &he) {
		return he.Code
	}
	return 0
}
