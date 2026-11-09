package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
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
	URLPath     string
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

func newRequestLogger(log logger.Logger, cfg LoggerConfig) *requestLogger {
	return &requestLogger{
		logger: log,
		config: cfg,
	}
}

// Handle returns the middleware handler function for request logging.
// This method replaces the nested closure pattern with a struct-based approach.
func (rl *requestLogger) Handle(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		reqCtx := newRequestLogContext()
		c.Set(RequestLogContextKey, reqCtx)

		// Attach the severity hook so a WARN+ line logged through the request context
		// raises the access line's level
		ctxWithHook := logger.WithSeverityHook(c.Request().Context(), reqCtx.escalateSeverity)
		c.SetRequest(c.Request().WithContext(ctxWithHook))

		// Skip probe endpoints from logs
		if rl.shouldSkipPath(c) {
			return next(c)
		}

		reqCtx.writer.ResponseWriter = c.Response()
		c.SetResponse(&reqCtx.writer)
		err := next(c)

		// Calculate request latency (thread-safe read)
		latency := time.Since(reqCtx.getStartTime())

		status := rl.extractStatus(c, err)

		// Always write the access line (#2212); escalation only raises its level.
		// logActionSummary still drops it when log.level disables the level it lands
		// at (an INFO line under log.level: warn).
		logActionSummary(c, rl.logger, rl.config, latency, status, err, reqCtx.getPeakSeverity())

		return err
	}
}

// shouldSkipPath checks whether the request should be excluded from logging: it is one of
// this engine's own probes, decided by the same predicate every other framework seat uses
// (isProbeRequest), so the probe exemption has one key in one place.
//
// The old form was template-first with a decoded-URL fallback when the template was empty,
// so it kept two shapes out of the log that now appear: an UNMATCHED request whose decoded
// URL reads as a probe path (the fallback is gone — nothing matched, so it is not a probe),
// and a non-probe method on the probe route (echo leaves c.Path() set to the best-match
// template on a top-level 405, so POST <base>/health is logged because the method check
// rejects it, not because the template differs).
func (rl *requestLogger) shouldSkipPath(c *echo.Context) bool {
	return isProbeRequest(c, rl.config.HealthPath, rl.config.ReadyPath)
}

// extractStatus returns the status the client receives. Echo's error handler runs only after
// this middleware returns, so an unsent response still holds the pooled reset 200: an error
// then resolves through statusForError, the classification the error handler answers with
// (#2213). Everything else is echo's own ResolveResponseStatus, whose committed-status-wins
// rule is the sent-first rule; a flush commits first (flushCommitWriter), so a flushed
// response counts as sent.
func (rl *requestLogger) extractStatus(c *echo.Context, err error) int {
	if err != nil && !isResponseCommitted(c) {
		return statusForError(err)
	}
	_, status := echo.ResolveResponseStatus(c.Response(), nil)
	return status
}

// LoggerWithConfig returns a request logging middleware with custom configuration.
// Every request the middleware does not skip as a probe gets one action log summary at
// completion. The middleware tracks request-scoped severity escalation: a WARN+ line logged
// through the request context during the request (enabled by log.level or not), or
// HandlerContext.EscalateSeverity, raises the summary's level and result_code; it never
// replaces the summary.
//
// The returned MiddlewareFunc is the framework-neutral (echo-free) form; the
// echo-native logic lives in loggerWithConfigEcho, which setupMiddlewares wires
// directly on the default request path (ADR-026, no per-request baton).
func LoggerWithConfig(log logger.Logger, cfg LoggerConfig) MiddlewareFunc {
	return fromEchoMiddleware(loggerWithConfigEcho(log, cfg))
}

// loggerWithConfigEcho is the echo-native request-logging middleware constructor.
// Public callers use LoggerWithConfig (echo-free); setupMiddlewares uses this form
// to keep the default chain baton-free.
func loggerWithConfigEcho(log logger.Logger, cfg LoggerConfig) echo.MiddlewareFunc {
	rl := newRequestLogger(log, cfg)
	return rl.Handle
}

// logActionSummary emits a synthesized action log with OpenTelemetry semantic conventions.
// This provides a high-level request summary (100% sampling) for every non-probe request.
func logActionSummary(
	c *echo.Context,
	log logger.Logger,
	cfg LoggerConfig,
	latency time.Duration,
	status int,
	err error,
	peak zerolog.Level,
) {
	ctx := c.Request().Context()

	// Determine severity, then create the event first so we can short-circuit
	// before the expensive extraction + field building when the action-log level
	// is disabled (e.g. LOG_LEVEL=warn on a healthy 2xx request). WithContext must
	// run before the check: a context-bound logger may carry a different level, so
	// enablement is only accurate after binding.
	logLevel, resultCode := determineSeverity(status, latency, cfg.SlowRequestThreshold, peak)
	event := createLogEvent(log.WithContext(ctx), logLevel)
	if !event.Enabled() {
		return
	}

	// Extract all data using helper functions (skipped when the event is disabled)
	metadata := extractRequestMetadata(c)
	metrics := extractOperationalMetrics(ctx)
	tenantID := extractTenantID(ctx)

	// Populate and emit the log event
	event = buildLogEvent(event, &logEventParams{
		metadata:   &metadata,
		metrics:    metrics,
		tenantID:   tenantID,
		status:     status,
		latency:    latency,
		resultCode: resultCode,
		err:        err,
	})

	event.Msg(createActionMessage(metadata.Method, metadata.URLPath, latency, status))
}

// buildLogEvent populates the given event with all OpenTelemetry action-log fields.
// The caller creates the event (deciding its level) so enablement can be checked
// before the expensive extraction; buildLogEvent only adds fields.
func buildLogEvent(event logger.LogEvent, params *logEventParams) logger.LogEvent {
	if params.err != nil {
		event = event.Err(params.err)
	}

	if params.tenantID != "" {
		event = event.Str("tenant", params.tenantID)
	}

	// Emit action log with OpenTelemetry HTTP semantic conventions
	return event.
		Str("log.type", "action"). // Mark as action log for dual-mode routing
		Str("request_id", params.metadata.RequestID).
		Str(logger.FieldCorrelationID, params.metadata.TraceID).
		Str("http.request.method", params.metadata.Method).
		Int("http.response.status_code", params.status).
		Int64("http.server.request.duration", params.latency.Nanoseconds()). // OTel uses nanoseconds
		Str("url.path", params.metadata.URLPath).
		Str(fieldHTTPRoute, params.metadata.Route).
		Str("client.address", params.metadata.ClientAddr).
		Str("user_agent.original", params.metadata.UserAgent).
		Str("result_code", params.resultCode).
		Int64("amqp_published", params.metrics.AMQPPublished).
		Int64("amqp_elapsed", params.metrics.AMQPElapsed).
		Int64("db_queries", params.metrics.DBQueries).
		Int64("db_elapsed", params.metrics.DBElapsed).
		Str("traceparent", params.metadata.Traceparent)
}

// determineSeverity calculates log severity and result_code based on HTTP status, latency,
// and peak, the highest level the request escalated to (zerolog.InfoLevel when it
// did not). Returns (logLevel, resultCode) tuple.
func determineSeverity(
	status int,
	latency, threshold time.Duration,
	peak zerolog.Level,
) (logLevel, resultCode string) {
	const (
		codeError = "ERROR"
		codeWarn  = "WARN"
		codeInfo  = "INFO"
	)

	// ERROR: 5xx status (an unhandled error included: extractStatus resolves it to the
	// status the error handler writes), a status no response carries (0 included: a
	// backstop, since the error handler answers an out-of-range status with 500), or an
	// escalation to ERROR+
	if status >= 500 || status < 100 || peak >= zerolog.ErrorLevel {
		return logger.LevelError, codeError
	}

	// WARN: 4xx status or an escalation to WARN
	if status >= 400 || peak >= zerolog.WarnLevel {
		return logger.LevelWarn, codeWarn
	}

	// WARN (result_code): Slow request (latency exceeds threshold)
	// Log level stays INFO, but result_code is WARN for filtering slow requests
	// Only check if threshold is positive (zero or negative disables slow request detection)
	if threshold > 0 && latency > threshold {
		return logger.LevelInfo, codeWarn
	}

	// INFO: Normal successful request
	return logger.LevelInfo, codeInfo
}

func createLogEvent(log logger.Logger, level string) logger.LogEvent {
	switch level {
	case logger.LevelError:
		return log.Error()
	case logger.LevelWarn:
		return log.Warn()
	case logger.LevelInfo:
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

// otherMethod is how the OTel HTTP semantic conventions render a request method outside the
// known set.
const otherMethod = "_OTHER"

// knownMethods is the semantic conventions' default known set (RFC 9110, PATCH, QUERY), the
// table echo-otel judges the span's http.request.method against, so the access line and the
// span agree. Echo's router also routes PROPFIND and REPORT, but semconv does not know them.
var knownMethods = map[string]struct{}{
	http.MethodConnect: {},
	http.MethodDelete:  {},
	http.MethodGet:     {},
	http.MethodHead:    {},
	http.MethodOptions: {},
	http.MethodPatch:   {},
	http.MethodPost:    {},
	http.MethodPut:     {},
	http.MethodTrace:   {},
	echo.QUERY:         {},
}

// semconvMethod returns method when it is in the known set and otherMethod otherwise, so no
// framework line or metric naming the request method follows a caller-chosen token in
// cardinality. The match is exact, as the router's is: a case variant renders otherMethod.
func semconvMethod(method string) string {
	if _, ok := knownMethods[method]; ok {
		return method
	}
	return otherMethod
}

// extractRequestMetadata extracts HTTP request metadata from Echo context.
// Handles nil response and empty headers safely by falling back to request headers.
func extractRequestMetadata(c *echo.Context) requestMetadata {
	var requestID, traceparent string

	// SAFETY: Try response headers first (may be transformed by middleware),
	// fall back to request headers if response is nil or headers empty.
	// This handles middleware ordering and timeout scenarios.
	// X-Request-ID and traceparent are validated at every read — both the response
	// and request headers can carry caller-controlled bytes (the response header is
	// normally set by RequestIDMiddleware and ensureTraceParentHeader to a validated
	// value, but defense in depth here closes the gap if any earlier middleware
	// reflects the inbound value verbatim). The request-header read is the one that
	// needs it outright: it is a raw inbound value on its way into a log field.
	if resp := c.Response(); resp != nil {
		requestID = validateRequestID(resp.Header().Get(echo.HeaderXRequestID))
		traceparent = validateTraceParent(resp.Header().Get(gobrickstrace.HeaderTraceParent))
	}

	// Fallback to request headers (always available, source of truth)
	if requestID == "" {
		requestID = validateRequestID(c.Request().Header.Get(echo.HeaderXRequestID))
	}
	if traceparent == "" {
		traceparent = validateTraceParent(c.Request().Header.Get(gobrickstrace.HeaderTraceParent))
	}

	return requestMetadata{
		Method: semconvMethod(c.Request().Method),
		// url.path is the matched template with each parameter value and the wildcard tail
		// masked, never request bytes (#2212): a PAN in a segment cannot reach the line.
		URLPath:     maskedPath(c),
		Route:       c.Path(), // Echo route pattern (e.g., /api/users/:id)
		RequestID:   requestID,
		TraceID:     getTraceID(c),
		Traceparent: traceparent,
		ClientAddr:  clientAddress(c),
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
	if tenantID, _ := multitenant.GetTenant(ctx); tenantID != "" { // NOSONAR: Error intentionally ignored - empty tenant ID is valid fallback for single-tenant apps
		return tenantID
	}
	return ""
}
