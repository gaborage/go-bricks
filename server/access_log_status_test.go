package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

const (
	accessStatusField = "http.response.status_code"
	panicRecoveredMsg = "Panic recovered"
	accessPanicSecret = "SECRET-VALUE-2213"
)

// newAccessLogServer builds a server through New with every handler outcome #2213 names
// registered as a module route. The middleware timeout is short so the timeout row fires.
func newAccessLogServer(t *testing.T, log logger.Logger, observability bool) *Server {
	t.Helper()
	cfg, err := config.LoadFromMap(map[string]any{
		"observability.enabled":     observability,
		"app.name":                  testServiceName,
		"server.timeout.middleware": "50ms",
	})
	require.NoError(t, err)
	srv := New(cfg, log)
	require.NoError(t, srv.setupErr)

	hr := NewHandlerRegistry(srv.cfg, WithLogger(log))
	r := srv.ModuleGroup()
	r.Add(http.MethodGet, "/raw/panic", func(HandlerContext) error { panic(accessPanicSecret) })
	r.Add(http.MethodGet, "/raw/plain", func(HandlerContext) error { return errors.New("plain failure") })
	r.Add(http.MethodGet, "/raw/canceled", func(HandlerContext) error { return context.Canceled })
	r.Add(http.MethodGet, "/raw/notfound", func(HandlerContext) error { return NewNotFoundError("widget") })
	r.Add(http.MethodGet, "/raw/unknowntenant", func(HandlerContext) error {
		return fmt.Errorf("resolve: %w", multitenant.ErrUnknownTenant)
	})
	r.Add(http.MethodGet, "/raw/deadline", func(HandlerContext) error { return context.DeadlineExceeded })
	r.Add(http.MethodGet, "/raw/timeout", func(c HandlerContext) error {
		<-c.RequestContext().Done()
		return nil
	})
	r.Add(http.MethodGet, "/raw/sent-then-error", func(c HandlerContext) error {
		if err := c.String(http.StatusOK, "sent"); err != nil {
			return err
		}
		return echo.NewHTTPError(http.StatusNotFound, "too late")
	})
	r.Add(http.MethodGet, "/raw/wrapped-httperror", func(HandlerContext) error {
		return fmt.Errorf("wrap: %w", echo.NewHTTPError(http.StatusConflict, "conflict"))
	})
	r.Add(http.MethodGet, "/raw/wrapped-sentinel", func(HandlerContext) error {
		var sentinel error = echo.ErrForbidden
		return fmt.Errorf("wrap: %w", sentinel)
	})
	GET(hr, r, "/typed/panic", func(EmptyRequest, HandlerContext) (helloResp, IAPIError) {
		panic(accessPanicSecret)
	})
	GET(hr, r, "/typed/notfound", func(EmptyRequest, HandlerContext) (helloResp, IAPIError) {
		return helloResp{}, NewNotFoundError("widget")
	})
	return srv
}

// TestAccessLogStatusMatchesTheWire pins #2213: for every handler outcome, the access
// line's status, level and result_code equal what the client received.
func TestAccessLogStatusMatchesTheWire(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantCode  int
		wantLevel string
		wantPanic bool
	}{
		{name: "raw_panic", path: "/raw/panic", wantCode: 500, wantLevel: logger.LevelError, wantPanic: true},
		{name: "typed_panic", path: "/typed/panic", wantCode: 500, wantLevel: logger.LevelError, wantPanic: true},
		{name: "raw_plain_error", path: "/raw/plain", wantCode: 500, wantLevel: logger.LevelError},
		{name: "raw_context_canceled", path: "/raw/canceled", wantCode: 500, wantLevel: logger.LevelError},
		{name: "raw_iapierror_not_found", path: "/raw/notfound", wantCode: 404, wantLevel: logger.LevelWarn},
		{name: "raw_unknown_tenant", path: "/raw/unknowntenant", wantCode: 400, wantLevel: logger.LevelWarn},
		{name: "raw_deadline_exceeded", path: "/raw/deadline", wantCode: 503, wantLevel: logger.LevelError},
		{name: "middleware_timeout", path: "/raw/timeout", wantCode: 503, wantLevel: logger.LevelError},
		{name: "raw_sent_200_then_http_error", path: "/raw/sent-then-error", wantCode: 200, wantLevel: logger.LevelInfo},
		{name: "raw_wrapped_http_error", path: "/raw/wrapped-httperror", wantCode: 409, wantLevel: logger.LevelWarn},
		{name: "raw_wrapped_echo_sentinel", path: "/raw/wrapped-sentinel", wantCode: 403, wantLevel: logger.LevelWarn},
		{name: "typed_iapierror_not_found", path: "/typed/notfound", wantCode: 404, wantLevel: logger.LevelWarn},
		{name: "unmatched_route", path: "/no/such/route", wantCode: 404, wantLevel: logger.LevelWarn},
	}

	for _, observability := range []bool{false, true} {
		t.Run("observability_"+strconv.FormatBool(observability), func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					log := &testLogger{}
					srv := newAccessLogServer(t, log, observability)

					req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tt.path, http.NoBody)
					rec := httptest.NewRecorder()
					srv.echo.ServeHTTP(rec, req)

					require.Equal(t, tt.wantCode, rec.Code, "wire status")
					access := findAccessLine(t, log)
					assert.Equal(t, strconv.Itoa(rec.Code), access.values[accessStatusField],
						"access line status must equal the status the client received")
					assert.Equal(t, tt.wantLevel, access.level, "access line level")
					assert.Equal(t, strings.ToUpper(tt.wantLevel), access.values["result_code"], "result_code")

					assertPanicLines(t, log, rec, tt.wantPanic, access)
				})
			}
		})
	}
}

func findAccessLine(t *testing.T, log *testLogger) testLogEntry {
	t.Helper()
	var found []testLogEntry
	for _, entry := range log.logEntries() {
		if _, ok := entry.values[accessStatusField]; ok {
			found = append(found, entry)
		}
	}
	require.Len(t, found, 1, "exactly one access line per request")
	return found[0]
}

// assertPanicLines checks exactly one "Panic recovered" line per panic (a fix running the
// error handler twice would double it), that the access line's error field keeps the
// C60.23 `panic (type:` shape, and that the panic value reaches no sink (ADR-081).
func assertPanicLines(t *testing.T, log *testLogger, rec *httptest.ResponseRecorder, wantPanic bool, access testLogEntry) {
	t.Helper()
	recovered := 0
	for _, entry := range log.logEntries() {
		if entry.msg == panicRecoveredMsg {
			recovered++
		}
		assert.NotContains(t, entry.msg, accessPanicSecret)
		for _, v := range entry.values {
			assert.NotContains(t, v, accessPanicSecret)
		}
	}
	assert.NotContains(t, rec.Body.String(), accessPanicSecret)
	if !wantPanic {
		assert.Zero(t, recovered, "no panic, no Panic recovered line")
		return
	}
	assert.Equal(t, 1, recovered, "exactly one Panic recovered line per panic")
	assert.Contains(t, access.values["error"], "panic (type:", "C60.23 row 6 error-field shape")
}

// TestStatusForErrorAgreesWithClassifyError pins the parity the access line relies on: the
// status statusForError reports is the status classifyError builds the response with, for
// every arm, including errors that match more than one.
func TestStatusForErrorAgreesWithClassifyError(t *testing.T) {
	var forbidden error = echo.ErrForbidden
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "deadline", err: context.DeadlineExceeded, want: http.StatusServiceUnavailable},
		{name: "deadline_wrapping_iapierror", err: errors.Join(context.DeadlineExceeded, NewNotFoundError("x")), want: http.StatusServiceUnavailable},
		{name: "iapierror", err: NewNotFoundError("x"), want: http.StatusNotFound},
		{name: "iapierror_beside_http_error", err: errors.Join(NewConflictError("x"), echo.NewHTTPError(http.StatusTeapot, "t")), want: http.StatusConflict},
		{name: "unknown_tenant", err: fmt.Errorf("w: %w", multitenant.ErrUnknownTenant), want: http.StatusBadRequest},
		{name: "wrapped_http_error", err: fmt.Errorf("w: %w", echo.NewHTTPError(http.StatusTeapot, "t")), want: http.StatusTeapot},
		{name: "wrapped_sentinel", err: fmt.Errorf("w: %w", forbidden), want: http.StatusForbidden},
		{name: "canceled", err: context.Canceled, want: http.StatusInternalServerError},
		{name: "plain", err: errors.New("boom"), want: http.StatusInternalServerError},
	}
	cfg := newTestConfig("", "", "")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	c := newEngine().NewContext(req, httptest.NewRecorder())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, statusForError(tt.err))
			assert.Equal(t, tt.want, classifyError(tt.err, c, cfg, &testLogger{}).HTTPStatus())
		})
	}
}
