package server

import (
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog"
)

// RequestLogContextKey is the public key used to store request logging context in Echo's context.
// This allows external code to access or modify request logging state if needed.
const RequestLogContextKey = "_request_log_ctx"

// requestLogContext tracks request-scoped logging state for dual-mode logging.
// It's stored in Echo's context (via c.Set/c.Get) to track severity escalation
// and determine whether to emit an action log summary or let trace logs flow.
type requestLogContext struct {
	startTime          time.Time     // Request start time
	peakSeverity       zerolog.Level // Highest severity observed during request lifecycle
	hasWarning         bool          // Quick flag: true if any WARN+ log emitted
	hadExplicitWarning bool          // True if WARN+ log was explicitly emitted during request execution
}

// newRequestLogContext creates a new request logging context.
func newRequestLogContext() *requestLogContext {
	return &requestLogContext{
		startTime:          time.Now(),
		peakSeverity:       zerolog.InfoLevel, // Start at INFO
		hasWarning:         false,
		hadExplicitWarning: false,
	}
}

// getRequestLogContext retrieves the request logging context from Echo's context.
// Returns nil if not found (e.g., request not tracked).
func getRequestLogContext(c echo.Context) *requestLogContext {
	if ctx := c.Get(RequestLogContextKey); ctx != nil {
		if reqCtx, ok := ctx.(*requestLogContext); ok {
			return reqCtx
		}
	}
	return nil
}

// escalateSeverity updates the peak severity if the new severity is higher.
// Automatically sets hasWarning=true for WARN+ levels.
// This is called by the severity hook during request execution for explicit logs.
func (r *requestLogContext) escalateSeverity(level zerolog.Level) {
	r.escalateSeverityInternal(level, true)
}

// escalateSeverityFromStatus updates severity based on HTTP status without marking as explicit warning.
// This allows us to distinguish between explicit WARN+ logs during request execution
// and warnings derived from final HTTP status codes.
func (r *requestLogContext) escalateSeverityFromStatus(level zerolog.Level) {
	r.escalateSeverityInternal(level, false)
}

// escalateSeverityInternal is the internal implementation of severity escalation.
func (r *requestLogContext) escalateSeverityInternal(level zerolog.Level, isExplicit bool) {
	if level > r.peakSeverity {
		r.peakSeverity = level
	}

	// Mark as having warning if severity is WARN or higher
	if level >= zerolog.WarnLevel {
		r.hasWarning = true
		if isExplicit {
			r.hadExplicitWarning = true // Track that an explicit WARN+ log occurred
		}
	}
}

// EscalateSeverity allows application code to explicitly escalate request severity.
// Useful for non-logging events that should trigger WARN+ action logs (e.g., rate limiting).
//
// Example:
//
//	if rateLimit.Exceeded() {
//	    server.EscalateSeverity(c, zerolog.WarnLevel)
//	}
func EscalateSeverity(c echo.Context, level zerolog.Level) {
	if reqCtx := getRequestLogContext(c); reqCtx != nil {
		reqCtx.escalateSeverity(level)
	}
}
