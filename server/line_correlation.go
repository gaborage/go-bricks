package server

import (
	"encoding/hex"
	"fmt"
	"log"

	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/logger"
)

// lineCorrelation is what a framework WARN or ERROR line about one request carries, so the line
// joins that request's span and X-Request-ID (#2212), and its access line when one is written (a
// line from the tenant seat, which runs before the access logger, has none): http.route, the
// registered route template exactly as the access line and the JOSE line write it (c.Path(),
// template-only, so it carries no request bytes; ADR-167), the request id, which is also the
// line's correlation_id, and the OTel trace and span ids when the request's span context is valid.
// correlateLine is the one constructor and every such line goes through stamp or text, so a
// field added here reaches every sink at once.
type lineCorrelation struct {
	route     string
	requestID string
	traceID   string // "" unless the span context is valid; spanID is set with it
	spanID    string
}

// correlateLine reads c's correlation. It mints nothing and writes nothing to the response:
// a line written before the request-id middleware set the response's X-Request-ID (an outermost
// panic guard line for a panic in that middleware) carries the inbound X-Request-ID if valid and
// an empty request id otherwise, rather than one no other line of the request shares.
func correlateLine(c *echo.Context) lineCorrelation {
	lc := lineCorrelation{route: c.Path(), requestID: safeGetRequestID(c)}
	if sc := trace.SpanContextFromContext(c.Request().Context()); sc.IsValid() {
		lc.traceID, lc.spanID = spanIDsHex(sc)
	}
	return lc
}

// spanIDsHex renders sc's trace and span ids as lowercase hex, the spelling their String
// methods produce, in one string allocation shared by both instead of one each.
func spanIDsHex(sc trace.SpanContext) (traceID, spanID string) {
	tid, sid := sc.TraceID(), sc.SpanID()
	var buf [2*len(tid) + 2*len(sid)]byte
	hex.Encode(buf[:2*len(tid)], tid[:])
	hex.Encode(buf[2*len(tid):], sid[:])
	s := string(buf[:])
	return s[:2*len(tid)], s[2*len(tid):]
}

// stamp adds the correlation fields to ev. request_id and correlation_id hold the same value,
// as on the access line.
func (lc *lineCorrelation) stamp(ev logger.LogEvent) logger.LogEvent {
	ev = ev.Str(fieldHTTPRoute, lc.route).
		Str(fieldRequestID, lc.requestID).
		Str(logger.FieldCorrelationID, lc.requestID)
	if lc.traceID != "" {
		ev = ev.Str(logger.FieldTraceID, lc.traceID).Str(logger.FieldSpanID, lc.spanID)
	}
	return ev
}

// text renders the same fields for the stdlib fallback a nil logger takes, as a suffix of
// " key=value" pairs with the string values quoted.
func (lc *lineCorrelation) text() string {
	s := fmt.Sprintf(" %s=%q %s=%q %s=%q", fieldHTTPRoute, lc.route,
		fieldRequestID, lc.requestID, logger.FieldCorrelationID, lc.requestID)
	if lc.traceID != "" {
		s += fmt.Sprintf(" %s=%s %s=%s", logger.FieldTraceID, lc.traceID, logger.FieldSpanID, lc.spanID)
	}
	return s
}

// openWarn opens one framework WARN about a request through l. ok is false when l's WARN level
// is disabled: the caller returns before it formats the line or reads the request (RealIP, the
// correlation), so a disabled level pays for neither. With a nil l, ev is nil and ok is true:
// the line takes the stdlib fallback, which has no level to disable.
func openWarn(l logger.Logger) (ev logger.LogEvent, ok bool) {
	if l == nil {
		return nil, true
	}
	ev = l.Warn()
	return ev, ev.Enabled()
}

// warnCorrelated writes msg as one correlated WARN on ev, an event openWarn returned enabled,
// or through the stdlib log package when ev is nil.
func warnCorrelated(ev logger.LogEvent, c *echo.Context, msg string) {
	lc := correlateLine(c)
	if ev == nil {
		log.Printf("WARN %s%s", msg, lc.text())
		return
	}
	lc.stamp(ev).Msg(msg)
}
