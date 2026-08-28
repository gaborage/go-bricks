package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

func guardTestConfig() *config.Config {
	return &config.Config{App: config.AppConfig{Name: "test", Env: "production"}}
}

// TestOutermostRecoverRendersTheStandardEnvelope pins #1144's first half: a panic
// in a middleware registered OUTSIDE Echo's Recover used to unwind to net/http,
// which printed the value to stderr and dropped the connection. The outermost
// guard answers with the framework's own 500 envelope instead.
func TestOutermostRecoverRendersTheStandardEnvelope(t *testing.T) {
	log := &testLogger{}
	e := echo.New()
	e.Use(outermostRecoverEcho(log, guardTestConfig()))
	e.GET("/boom", func(*echo.Context) error { panic(recoverProbeSecret) })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", http.NoBody))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "the response carries the standard error envelope")
	assert.NotEmpty(t, errObj["code"])
	assert.NotContains(t, rec.Body.String(), recoverProbeSecret, "the panic value never reaches the caller")
}

// TestOutermostRecoverLogsTheTypeNotTheValue is ADR-081 at the new site: the log
// line names the panic's TYPE and the request that produced it, and the value —
// consumer-chosen, so the sensitive-data filter cannot help — appears nowhere.
func TestOutermostRecoverLogsTheTypeNotTheValue(t *testing.T) {
	log := &testLogger{}
	e := echo.New()
	e.Use(outermostRecoverEcho(log, guardTestConfig()))
	e.GET("/boom", func(*echo.Context) error { panic(recoverProbeSecret) })

	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", http.NoBody))

	entries := log.logEntries()
	require.Len(t, entries, 1, "exactly one ERROR line per recovered panic")
	entry := entries[0]
	assert.Equal(t, "error", entry.level)
	assert.Equal(t, "string", entry.values["panic_type"])
	assert.Equal(t, http.MethodGet, entry.values["method"])
	assert.Equal(t, "/boom", entry.values["path"])
	for key, value := range entry.values {
		assert.NotContains(t, value, recoverProbeSecret, "field %q carries the panic value", key)
	}
	assert.NotContains(t, strings.Join(entry.fields, ","), recoverProbeSecret)
}

// TestOutermostRecoverRepanicsAbortHandler keeps net/http's abort contract: the
// sentinel must reach the server unchanged so the connection is dropped without
// a response, exactly as sanitizePanicValue does one layer down.
func TestOutermostRecoverRepanicsAbortHandler(t *testing.T) {
	log := &testLogger{}
	e := echo.New()
	e.Use(outermostRecoverEcho(log, guardTestConfig()))
	e.GET("/abort", func(*echo.Context) error { panic(http.ErrAbortHandler) })

	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", http.NoBody))
	})
	assert.Empty(t, log.logEntries(), "an abort is not a panic to report")
}

// panicTenantResolver is a consumer-supplied resolver that panics — the shape
// #1144 verified against a real deployment. multitenant.TenantResolver is a
// public seam, and the tenant middleware runs BEFORE Echo's Recover, so this is
// the panic that used to reach net/http.
type panicTenantResolver struct{}

func (panicTenantResolver) ResolveTenant(context.Context, *http.Request) (string, error) {
	panic(recoverProbeSecret)
}

// TestOutermostRecoverStopsAPreRecoverPanicReachingNetHTTP drives a real server
// over the wire with the chain SetupMiddlewares composes for the tenant door:
// the guard outermost, the tenant middleware inside it and outside Recover.
// Before the guard, net/http printed `http: panic serving <addr>: <value>` with
// a stack to the standard logger and closed the connection, so the client got
// EOF and the panic VALUE was rendered by a sink outside this framework.
func TestOutermostRecoverStopsAPreRecoverPanicReachingNetHTTP(t *testing.T) {
	var stdLog bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&stdLog)
	t.Cleanup(func() { log.SetOutput(previous) })

	frameworkLog := &testLogger{}
	e := echo.New()
	e.Use(outermostRecoverEcho(frameworkLog, guardTestConfig()))
	e.Use(tenantMiddlewareEcho(panicTenantResolver{}, nil, frameworkLog))
	e.GET("/tenant", func(c *echo.Context) error { return c.String(http.StatusOK, "unreachable") })

	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/tenant")
	require.NoError(t, err, "the connection is answered, not dropped")
	t.Cleanup(func() { _ = resp.Body.Close() })
	payload, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Contains(t, string(payload), `"error"`, "the standard envelope, not an empty body")
	assert.NotContains(t, string(payload), recoverProbeSecret)
	assert.NotContains(t, stdLog.String(), recoverProbeSecret, "net/http never renders the value")
	assert.Empty(t, stdLog.String(), "net/http prints nothing at all")

	entries := frameworkLog.logEntries()
	require.Len(t, entries, 1, "the guard is the only thing that logs on this path")
	assert.Equal(t, "string", entries[0].values["panic_type"])
	for key, value := range entries[0].values {
		assert.NotContains(t, value, recoverProbeSecret, "field %q carries the panic value", key)
	}
}

// TestOutermostRecoverMessageMatchesTheStandardFiveHundred pins the parity the
// guard's own renderer could otherwise break: a caller must not be able to tell
// WHICH recovery layer caught the panic from the message it gets back, so the
// guard's body carries the text classifyError produces for an unhandled 500 in
// the same posture.
func TestOutermostRecoverMessageMatchesTheStandardFiveHundred(t *testing.T) {
	// The expected strings are the wire contract, written out rather than read
	// back from the constants the code uses — an assertion against those would
	// pass whatever they said.
	tests := []struct {
		name  string
		debug bool
		want  string
	}{
		{name: "production_hides_the_wording", debug: false, want: "An error occurred while processing your request"},
		{name: "debug_keeps_it", debug: true, want: "Internal server error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{App: config.AppConfig{Name: "test", Env: "production", Debug: tt.debug}}
			e := echo.New()
			e.Use(outermostRecoverEcho(&testLogger{}, cfg))
			e.GET("/boom", func(*echo.Context) error { panic(recoverProbeSecret) })

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", http.NoBody))

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			errObj, ok := body["error"].(map[string]any)
			require.True(t, ok)

			assert.Equal(t, tt.want, errObj["message"])
		})
	}
}

// panickingLogger is a consumer logger whose Error() panics — the shape that
// would otherwise take a spent recover() straight back to net/http.
type panickingLogger struct{ testLogger }

func (l *panickingLogger) Error() logger.LogEvent { panic("logger exploded") }

// TestOutermostRecoverSurvivesAPanickingLogger keeps the guard from reopening
// #1144 through its own reporting call: the logger is consumer-supplied, and a
// panic from it runs inside an already-spent recover, so without containment it
// would unwind past Echo into net/http — printing the LOGGER's panic value and
// dropping the connection. The request must still get its 500 envelope.
func TestOutermostRecoverSurvivesAPanickingLogger(t *testing.T) {
	e := echo.New()
	e.Use(outermostRecoverEcho(&panickingLogger{}, guardTestConfig()))
	e.GET("/boom", func(*echo.Context) error { panic(recoverProbeSecret) })

	rec := httptest.NewRecorder()
	require.NotPanics(t, func() {
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", http.NoBody))
	})

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error"`)
	assert.NotContains(t, rec.Body.String(), recoverProbeSecret)
}
