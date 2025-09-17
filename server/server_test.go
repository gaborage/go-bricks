package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

type testLogger struct {
	mu      sync.Mutex
	entries []string
}

type testLogEvent struct {
	logger *testLogger
	level  string
}

type stubListener struct {
	ch   chan struct{}
	once sync.Once
}

func newStubListener() *stubListener {
	return &stubListener{ch: make(chan struct{})}
}

func (l *stubListener) Accept() (net.Conn, error) {
	if _, ok := <-l.ch; !ok {
		return nil, net.ErrClosed
	}
	return nil, nil
}

func (l *stubListener) Close() error {
	l.once.Do(func() {
		close(l.ch)
	})
	return nil
}

func (l *stubListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4zero, Port: 0}
}

func (l *testLogger) Info() logger.LogEvent  { return &testLogEvent{logger: l, level: "info"} }
func (l *testLogger) Error() logger.LogEvent { return &testLogEvent{logger: l, level: "error"} }
func (l *testLogger) Debug() logger.LogEvent { return &testLogEvent{logger: l, level: "debug"} }
func (l *testLogger) Warn() logger.LogEvent  { return &testLogEvent{logger: l, level: "warn"} }
func (l *testLogger) Fatal() logger.LogEvent { return &testLogEvent{logger: l, level: "fatal"} }
func (l *testLogger) WithContext(interface{}) logger.Logger {
	return l
}
func (l *testLogger) WithFields(map[string]interface{}) logger.Logger {
	return l
}

func (e *testLogEvent) Msg(msg string) {
	e.logger.mu.Lock()
	defer e.logger.mu.Unlock()
	e.logger.entries = append(e.logger.entries, fmt.Sprintf("%s:%s", e.level, msg))
}

func (e *testLogEvent) Msgf(format string, args ...interface{})       { e.Msg(fmt.Sprintf(format, args...)) }
func (e *testLogEvent) Err(error) logger.LogEvent                     { return e }
func (e *testLogEvent) Str(string, string) logger.LogEvent            { return e }
func (e *testLogEvent) Int(string, int) logger.LogEvent               { return e }
func (e *testLogEvent) Int64(string, int64) logger.LogEvent           { return e }
func (e *testLogEvent) Uint64(string, uint64) logger.LogEvent         { return e }
func (e *testLogEvent) Dur(string, time.Duration) logger.LogEvent     { return e }
func (e *testLogEvent) Interface(string, interface{}) logger.LogEvent { return e }
func (e *testLogEvent) Bytes(string, []byte) logger.LogEvent          { return e }

func minimalConfig() *config.Config {
	return &config.Config{
		App: config.AppConfig{
			Name:    "test-service",
			Version: "1.0.0",
			Env:     "development",
		},
		Server: config.ServerConfig{
			Host:         "127.0.0.1",
			Port:         0,
			ReadTimeout:  50 * time.Millisecond,
			WriteTimeout: 50 * time.Millisecond,
		},
	}
}

func TestServerNewInitializesEchoAndRoutes(t *testing.T) {
	cfg := minimalConfig()
	log := &testLogger{}

	srv := New(cfg, log)
	require.NotNil(t, srv)

	e := srv.Echo()
	require.NotNil(t, e)
	assert.True(t, e.HideBanner)
	assert.True(t, e.HidePort)
	require.NotNil(t, e.Validator)

	req := httptest.NewRequest(http.MethodGet, "/health", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	reqReady := httptest.NewRequest(http.MethodGet, "/ready", http.NoBody)
	recReady := httptest.NewRecorder()
	e.ServeHTTP(recReady, reqReady)
	require.Equal(t, http.StatusOK, recReady.Code)

	var readyPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(recReady.Body.Bytes(), &readyPayload))
	assert.Equal(t, "ready", readyPayload["status"])
	assert.NotZero(t, readyPayload["time"])
}

func TestServerStartAndShutdown(t *testing.T) {
	cfg := minimalConfig()
	log := &testLogger{}

	srv := New(cfg, log)
	require.NotNil(t, srv)
	listener := newStubListener()
	srv.echo.Listener = listener

	errCh := make(chan error, 1)

	go func() {
		errCh <- srv.Start()
	}()

	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)

	require.NoError(t, listener.Close())
	require.NoError(t, srv.Shutdown(ctx))

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("unexpected error from server: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not shut down in time")
	}
}

func TestServerEchoReturnsUnderlyingInstance(t *testing.T) {
	cfg := minimalConfig()
	log := &testLogger{}

	srv := New(cfg, log)
	require.NotNil(t, srv)

	e := srv.Echo()
	require.NotNil(t, e)
	assert.Same(t, e, srv.echo)
}

func TestNormalizeBasePath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		out  string
	}{
		{name: "empty", in: "", out: ""},
		{name: "already_normalized", in: "/api", out: "/api"},
		{name: "missing_leading", in: "api", out: "/api"},
		{name: "trailing_slash", in: "/api/", out: "/api"},
		{name: "root", in: "/", out: "/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.out, normalizeBasePath(tt.in))
		})
	}
}

func TestNormalizeRoutePath(t *testing.T) {
	tests := []struct {
		name     string
		route    string
		defaultR string
		expected string
	}{
		{name: "empty_uses_default", route: "", defaultR: "/health", expected: "/health"},
		{name: "missing_leading_slash", route: "ready", defaultR: "/ready", expected: "/ready"},
		{name: "already_prefixed", route: "/status", defaultR: "/status", expected: "/status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, normalizeRoutePath(tt.route, tt.defaultR))
		})
	}
}

func TestServerBuildFullPath(t *testing.T) {
	s := &Server{basePath: "/api"}
	assert.Equal(t, "/api/users", s.buildFullPath("/users"))
	assert.Equal(t, "/api", s.buildFullPath("/"))

	s.basePath = ""
	assert.Equal(t, "/users", s.buildFullPath("/users"))
}

func TestModuleGroupAppliesBasePath(t *testing.T) {
	e := echo.New()
	s := &Server{echo: e, basePath: "/api"}

	group := s.ModuleGroup()
	require.NotNil(t, group)

	group.GET("/ping", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/ping", http.NoBody)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)

	// when base path empty, route should remain unchanged
	s.basePath = ""
	e = echo.New()
	s.echo = e

	group = s.ModuleGroup()
	group.GET("/ping", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	req = httptest.NewRequest(http.MethodGet, "/ping", http.NoBody)
	rec = httptest.NewRecorder()

	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)

	// when base path is "/", ensure routes are not double prefixed
	s.basePath = "/"
	e = echo.New()
	s.echo = e

	group = s.ModuleGroup()
	group.GET("/ping", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	req = httptest.NewRequest(http.MethodGet, "/ping", http.NoBody)
	rec = httptest.NewRecorder()

	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestStatusToErrorCodeMappings(t *testing.T) {
	tests := []struct {
		status int
		code   string
	}{
		{status: http.StatusBadRequest, code: "BAD_REQUEST"},
		{status: http.StatusUnauthorized, code: "UNAUTHORIZED"},
		{status: http.StatusForbidden, code: "FORBIDDEN"},
		{status: http.StatusNotFound, code: "NOT_FOUND"},
		{status: http.StatusConflict, code: "CONFLICT"},
		{status: http.StatusTooManyRequests, code: "TOO_MANY_REQUESTS"},
		{status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE"},
		{status: http.StatusTeapot, code: "INTERNAL_ERROR"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.code, statusToErrorCode(tt.status))
	}
}
