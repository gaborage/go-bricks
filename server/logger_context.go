package server

import (
	"bufio"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// RequestLogContextKey is the public key used to store request logging context in Echo's context.
// This allows external code to access or modify request logging state if needed.
const RequestLogContextKey = "_request_log_ctx"

// requestLogContext tracks request-scoped logging state.
// It's stored in Echo's context (via c.Set/c.Get) to track severity escalation, which
// raises the level of the request's action log summary.
//
// Thread-safety: mu protects every field but writer, to allow concurrent access from
// multiple goroutines (e.g., severity hook callbacks, async request handlers); writer is set
// once, by the request logger, before the chain below it runs.
type requestLogContext struct {
	mu            sync.Mutex    // Protects the fields below, writer excepted
	startTime     time.Time     // Request start time
	peakSeverity  zerolog.Level // Highest level escalated to during the request
	clientAddr    string        // c.RealIP(), memoized by clientAddress
	hasClientAddr bool          // True once clientAddr is filled
	// writer is the request's response writer once the request logger installs it; a field,
	// so installing it allocates nothing beyond this struct (ADR-026).
	writer flushCommitWriter
}

// flushCommitWriter commits the echo response before it flushes, so a flushed response counts
// as sent. net/http answers a Flush on an unwritten header with an implicit 200, but echo sets
// Response.Committed only in WriteHeader, so without this a handler that flushed and then
// returned an error was logged with the error's status while the client had a 200 (#2213),
// and the error handler wrote a superfluous header. A hijacked connection has no HTTP status
// to compare, so Hijack only passes through.
type flushCommitWriter struct {
	http.ResponseWriter
}

// Flush writes the pending status first when nothing is written yet, then flushes.
func (w *flushCommitWriter) Flush() {
	if resp, err := echo.UnwrapResponse(w.ResponseWriter); err == nil && !resp.Committed {
		w.WriteHeader(resp.Status)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Hijack passes through, so a w.(http.Hijacker) assertion keeps working.
func (w *flushCommitWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

// Unwrap returns the wrapped writer, so echo.UnwrapResponse and http.ResponseController reach it.
func (w *flushCommitWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func newRequestLogContext() *requestLogContext {
	return &requestLogContext{
		startTime:    time.Now(),
		peakSeverity: zerolog.InfoLevel, // Start at INFO
	}
}

// getRequestLogContext retrieves the request logging context from Echo's context.
// Returns nil if not found (e.g., request not tracked).
func getRequestLogContext(c *echo.Context) *requestLogContext {
	if ctx := c.Get(RequestLogContextKey); ctx != nil {
		if reqCtx, ok := ctx.(*requestLogContext); ok {
			return reqCtx
		}
	}
	return nil
}

// clientAddress returns c.RealIP(), extracted at most once per request. The access logger
// plants a fresh requestLogContext per request before the limiter runs, so the limiter
// fills the memo and the access line's client.address reuses it instead of walking
// X-Forwarded-For a second time. The memo lives on that per-request value, never on the
// pooled echo.Context, so no request can read another's address. Outside the access logger
// (no requestLogContext) it is a plain c.RealIP(). A new client-address read downstream of
// the access logger should call this, not c.RealIP(), or it pays a second extraction.
// When the limiter is on, client.address is fixed at limiter time: a later rewrite of the
// peer address or X-Forwarded-For no longer moves it.
func clientAddress(c *echo.Context) string {
	reqCtx := getRequestLogContext(c)
	if reqCtx == nil {
		return c.RealIP()
	}
	reqCtx.mu.Lock()
	defer reqCtx.mu.Unlock()
	if !reqCtx.hasClientAddr {
		reqCtx.clientAddr = c.RealIP()
		reqCtx.hasClientAddr = true
	}
	return reqCtx.clientAddr
}

// escalateSeverity raises the peak severity to level when level is higher. It is called by
// the severity hook for every WARN+ line logged through the request context, and by
// EscalateSeverity. zerolog.NoLevel and zerolog.Disabled sit above PanicLevel without being
// severities, so they are ignored rather than read as an escalation to ERROR.
// Thread-safe: Protected by mutex to handle concurrent calls from multiple goroutines.
func (r *requestLogContext) escalateSeverity(level zerolog.Level) {
	if level > zerolog.PanicLevel {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if level > r.peakSeverity {
		r.peakSeverity = level
	}
}

// getStartTime returns the request start time in a thread-safe manner.
func (r *requestLogContext) getStartTime() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startTime
}

// getPeakSeverity returns the highest level the request escalated to in a thread-safe manner.
func (r *requestLogContext) getPeakSeverity() zerolog.Level {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peakSeverity
}

// EscalateSeverity allows application code to explicitly escalate request severity: the
// request's action log summary is emitted at level (WARN or ERROR) even on a 2xx response.
// Useful for non-logging events that should surface the request (e.g., rate limiting).
// Severity is monotonic, and a level below WARN changes nothing.
//
// Example:
//
//	if rateLimit.Exceeded() {
//	    ctx.EscalateSeverity(zerolog.WarnLevel)
//	}
//
// Thread-safe: Can be called from multiple goroutines concurrently.
func (c HandlerContext) EscalateSeverity(level zerolog.Level) {
	if reqCtx := getRequestLogContext(c.echoContext()); reqCtx != nil {
		reqCtx.escalateSeverity(level)
	}
}
