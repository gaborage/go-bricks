package server

import (
	"fmt"
	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// reportPanic writes the guard's single ERROR line, and cannot itself take the
// process back to the failure this guard exists to prevent.
//
// The logger is consumer-supplied, and this call runs inside an already-spent
// recover(): a logger that panics here would unwind past Echo into net/http,
// which is exactly the outcome #1144 closes — with the panic value it prints
// being the LOGGER's, not the handler's. ADR-079 guarded the framework's other
// two panic-reporting calls for this reason; this is the third. The recovered
// value is discarded rather than reported, since reporting it needs the same
// logger that just failed.
//
// The message matches the HTTPErrorHandler's own panic line, so an alert keyed
// on it catches the panics from BOTH sides of Recover; request_id comes from the
// same helper that line uses, and is empty when the request-id middleware —
// which runs inside this guard — had not run yet.
func reportPanic(log logger.Logger, c *echo.Context, r any) {
	defer func() { _ = recover() }()

	req := c.Request()
	log.Error().
		Str("panic_type", fmt.Sprintf("%T", r)).
		Str("request_id", safeGetRequestID(c)).
		Str("method", req.Method).
		Str("path", req.URL.Path).
		Msg("Panic recovered")
}

// outermostRecoverEcho is the panic guard registered as the FIRST middleware, so
// every other middleware — including the eight that run before Echo's Recover:
// request id, OTel, request enrich, CORS, IP pre-guard, tenant resolution,
// forwarded client cert, and the request logger (three of them conditional on
// config) — unwinds into it.
//
// Echo v5 has no top-level recover, so a panic in any of those reached net/http,
// which prints `http: panic serving <addr>: <value>` with a stack to stderr and
// drops the connection: the caller saw EOF, no access-log line was written, and
// the panic VALUE was rendered by a sink outside this framework's control —
// which is the one thing ADR-081 refuses. `http.Server.ErrorLog` cannot fix that,
// because net/http formats the value into the string before any adapter sees it.
//
// It holds the logger and config in its closure rather than reading them from
// the request context: the middlewares that populate that context are exactly the
// ones whose panics land here, so nothing about the request can be assumed.
//
// It deliberately does not touch the span. A panic before Recover ends the span
// without an error status, which the issue accepts rather than reaching around
// the OTel middleware from outside it.
//
// Moving Echo's own Recover to the front instead of adding this guard is the
// obvious alternative and it is wrong. Recover turns a panic into a plain error
// RETURN for everything registered after it, and the access logger is registered
// BEFORE it precisely so its `err := next(c)` observes that return and still
// writes a line for a panicking request. Put Recover in front of the logger and
// the panic unwinds through the logger's frame instead, which has no defer of
// its own — silently dropping the access-log line for EVERY recovered panic,
// including the handler panics that are logged correctly today.
func outermostRecoverEcho(log logger.Logger, cfg *config.Config) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) (err error) {
			defer func() {
				r := recover()
				if r == nil {
					return
				}
				// net/http's abort contract: the sentinel must reach the server
				// unchanged, so the connection is dropped with no response. The
				// predicate is shared with sanitizePanicValue — see isAbortSentinel
				// for why it is identity and not errors.Is.
				if isAbortSentinel(r) {
					panic(r)
				}
				// SECURITY: the TYPE, never the value (ADR-081). The value is
				// consumer-chosen, so the logger's SensitiveDataFilter cannot
				// help — it matches field NAMES, and a bare panic("secret") has
				// none.
				reportPanic(log, c, r)

				// The same message classifyError would have produced for this
				// status, so a caller cannot tell which recovery layer caught the
				// panic from the body it gets back.
				//
				// formatErrorResponse rather than customErrorHandler because the
				// error is already classified and there is nothing to log twice.
				// That skips the raw-mode formatter switch, which is correct HERE
				// and only here: rawResponseContextKey is set inside
				// handlerWrapper.wrap, far downstream of Recover, so a panic
				// reaching THIS guard cannot have passed through it. A change that
				// sets raw mode earlier has to revisit this line.
				err = formatErrorResponse(c, NewInternalServerError(internalErrorMessage(cfg)), cfg)
			}()
			return next(c)
		}
	}
}
