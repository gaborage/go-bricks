package server

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/logger"
)

// The request every correlation test serves: a param route whose value is a needle no
// correlation field may carry, under a valid span context and a request id.
const (
	correlationRoute     = "/cards/:id"
	correlationNeedle    = "needle-4242"
	correlationTarget    = "/cards/" + correlationNeedle
	correlationMasked    = "/cards/" + maskToken
	correlationRequestID = "req-l5-correlation"
	correlationTraceHex  = "0af7651916cd43dd8448eb211c80319c"
	correlationSpanHex   = "b7ad6b7169203331"
)

// correlationSuffix is the stdlib-fallback text a correlated line ends with for the request
// correlatedRequest builds: http.route is the registered template, as on the access line.
const correlationSuffix = ` http.route="` + correlationRoute + `" request_id="` + correlationRequestID +
	`" correlation_id="` + correlationRequestID + `" trace_id=` + correlationTraceHex + ` span_id=` + correlationSpanHex

// correlatedRequest builds a request for correlationTarget carrying correlationRequestID and,
// when withSpan, the valid span context the correlation constants name.
func correlatedRequest(t *testing.T, withSpan bool) *http.Request {
	t.Helper()
	ctx := context.Background()
	if withSpan {
		tid, err := trace.TraceIDFromHex(correlationTraceHex)
		require.NoError(t, err)
		sid, err := trace.SpanIDFromHex(correlationSpanHex)
		require.NoError(t, err)
		ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled,
		}))
	}
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, correlationTarget, http.NoBody)
	req.Header.Set(echo.HeaderXRequestID, correlationRequestID)
	return req
}

// serveCorrelated serves req through mw on a framework engine (newEngine), so the path a line
// carries renders through the masking router. The engine's client address is the
// X-Forwarded-For walk over echo's default trust (loopback, link-local and private peers), as
// behind a load balancer, and serveCorrelated returns how many times it was read.
func serveCorrelated(t *testing.T, mw echo.MiddlewareFunc, req *http.Request) int32 {
	t.Helper()
	e := newEngine()
	var reads atomic.Int32
	xff := echo.ExtractIPFromXFFHeader()
	e.IPExtractor = func(r *http.Request) string {
		reads.Add(1)
		return xff(r)
	}
	e.Use(mw)
	e.GET(correlationRoute, func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
	e.ServeHTTP(httptest.NewRecorder(), req)
	return reads.Load()
}

// assertLineCorrelated checks entry carries the exact correlation of a correlatedRequest with
// a span, and that no field echoes the route value.
func assertLineCorrelated(t *testing.T, entry testLogEntry) {
	t.Helper()
	assert.Equal(t, correlationRoute, entry.values[fieldHTTPRoute])
	assert.Equal(t, correlationRequestID, entry.values[fieldRequestID])
	assert.Equal(t, correlationRequestID, entry.values[logger.FieldCorrelationID])
	assert.Equal(t, correlationTraceHex, entry.values[logger.FieldTraceID])
	assert.Equal(t, correlationSpanHex, entry.values[logger.FieldSpanID])
	for _, key := range []string{fieldHTTPRoute, fieldRequestID, logger.FieldCorrelationID, logger.FieldTraceID, logger.FieldSpanID} {
		assert.NotContains(t, entry.values[key], correlationNeedle, "field %q carries the path value", key)
	}
}

// captureStdlibLog redirects the stdlib log package for the test and returns its buffer.
func captureStdlibLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

func TestCorrelateLineReadsTheMatchedRequest(t *testing.T) {
	tests := []struct {
		name      string
		engine    func() *echo.Echo
		withSpan  bool
		wantRoute string
		wantTrace string
		wantSpan  string
	}{
		{
			name: "valid_span_on_a_framework_engine", engine: newEngine, withSpan: true,
			wantRoute: correlationRoute, wantTrace: correlationTraceHex, wantSpan: correlationSpanHex,
		},
		{name: "no_span_leaves_the_otel_ids_empty", engine: newEngine, wantRoute: correlationRoute},
		{
			// The foreign engine is the case under test: http.route is c.Path(), not the
			// masking router's rendering, so it needs no maskingRouter.
			name: "foreign_engine_reads_the_same_template", engine: echo.New, //nolint:forbidigo // see above
			withSpan: true, wantRoute: correlationRoute, wantTrace: correlationTraceHex, wantSpan: correlationSpanHex,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got lineCorrelation
			e := tc.engine()
			e.GET(correlationRoute, func(c *echo.Context) error {
				got = correlateLine(c)
				return nil
			})
			e.ServeHTTP(httptest.NewRecorder(), correlatedRequest(t, tc.withSpan))
			assert.Equal(t, lineCorrelation{
				route: tc.wantRoute, requestID: correlationRequestID, traceID: tc.wantTrace, spanID: tc.wantSpan,
			}, got)
		})
	}
}

func TestCorrelateLineMintsNoRequestID(t *testing.T) {
	var got lineCorrelation
	var minted string
	e := newEngine()
	e.GET(correlationRoute, func(c *echo.Context) error {
		got = correlateLine(c)
		minted = c.Response().Header().Get(echo.HeaderXRequestID)
		return nil
	})
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, correlationTarget, http.NoBody))
	assert.Empty(t, got.requestID)
	assert.Empty(t, minted, "correlation writes nothing to the response")
}

// TestLineCorrelationOmitsTheOTelIDsWithoutASpan pins the no-span shape on both renderers; the
// sink tests pin the shape with one.
func TestLineCorrelationOmitsTheOTelIDsWithoutASpan(t *testing.T) {
	lc := lineCorrelation{route: correlationRoute, requestID: correlationRequestID}
	assert.Equal(t, ` http.route="`+correlationRoute+`" request_id="`+correlationRequestID+`" correlation_id="`+correlationRequestID+`"`, lc.text())

	l := &testLogger{}
	lc.stamp(l.Warn()).Msg("no span")
	entries := l.logEntries()
	require.Len(t, entries, 1)
	assert.Equal(t, []string{fieldHTTPRoute, fieldRequestID, logger.FieldCorrelationID}, entries[0].fields)
}

func TestOpenWarnGatesOnTheWarnLevel(t *testing.T) {
	ev, ok := openWarn(nil)
	assert.True(t, ok, "the stdlib fallback has no level to disable")
	assert.Nil(t, ev)

	dl := &disabledLogger{ev: &disabledEvent{}}
	_, ok = openWarn(dl)
	assert.False(t, ok, "a disabled WARN level must close the line before it is formatted")

	ev, ok = openWarn(&testLogger{})
	assert.True(t, ok)
	assert.NotNil(t, ev)
}
