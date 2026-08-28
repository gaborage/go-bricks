package server

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// outermostRecoverEcho is the panic guard registered as the FIRST middleware, so
// every other middleware — including the seven that run before Echo's Recover:
// request id, OTel, request enrich, CORS, IP pre-guard, tenant resolution,
// forwarded client cert, and the request logger — unwinds into it.
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
func outermostRecoverEcho(log logger.Logger, cfg *config.Config) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) (err error) {
			defer func() {
				r := recover()
				if r == nil {
					return
				}
				// net/http's abort contract: the sentinel must reach the server
				// unchanged, so the connection is dropped with no response.
				//
				// SECURITY: identity, not errors.Is. This is a BYPASS gate, so
				// breadth is a defect — `errors.Is` also matches a WRAPPED
				// sentinel, and re-panicking `fmt.Errorf("%s: %w", secret,
				// http.ErrAbortHandler)` would hand the payload to net/http's own
				// stderr renderer. net/http honors only the exact sentinel too.
				//nolint:errorlint // sentinel bypass: breadth is the bug
				if r == http.ErrAbortHandler {
					panic(r)
				}
				// SECURITY: the TYPE, never the value (ADR-081). The value is
				// consumer-chosen, so the logger's SensitiveDataFilter cannot
				// help — it matches field NAMES, and a bare panic("secret") has
				// none.
				log.Error().
					Str("panic_type", fmt.Sprintf("%T", r)).
					Str("method", c.Request().Method).
					Str("path", c.Request().URL.Path).
					Msg("Panic recovered outside Recover")

				err = formatErrorResponse(c, NewInternalServerError(""), cfg)
			}()
			return next(c)
		}
	}
}
