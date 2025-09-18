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
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string returns empty",
			input:    "",
			expected: "",
		},
		{
			name:     "root path returns root",
			input:    "/",
			expected: "/",
		},
		{
			name:     "adds leading slash",
			input:    "api",
			expected: "/api",
		},
		{
			name:     "removes trailing slash",
			input:    "/api/",
			expected: "/api",
		},
		{
			name:     "handles multiple trailing slashes",
			input:    "/api///",
			expected: "/api",
		},
		{
			name:     "handles path with subdirectories",
			input:    "api/v1/test",
			expected: "/api/v1/test",
		},
		{
			name:     "handles path with subdirectories and trailing slash",
			input:    "/api/v1/test/",
			expected: "/api/v1/test",
		},
		{
			name:     "handles already normalized path",
			input:    "/api/v1",
			expected: "/api/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeBasePath(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNormalizeRoutePath(t *testing.T) {
	tests := []struct {
		name         string
		route        string
		defaultRoute string
		expected     string
	}{
		{
			name:         "empty route uses default",
			route:        "",
			defaultRoute: "/health",
			expected:     "/health",
		},
		{
			name:         "adds leading slash to route",
			route:        "custom-health",
			defaultRoute: "/health",
			expected:     "/custom-health",
		},
		{
			name:         "preserves route with leading slash",
			route:        "/custom-health",
			defaultRoute: "/health",
			expected:     "/custom-health",
		},
		{
			name:         "handles root route",
			route:        "/",
			defaultRoute: "/health",
			expected:     "/",
		},
		{
			name:         "handles complex route path",
			route:        "/api/v1/status",
			defaultRoute: "/health",
			expected:     "/api/v1/status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeRoutePath(tt.route, tt.defaultRoute)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestServerBuildFullPath(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
		route    string
		expected string
	}{
		{
			name:     "empty base path returns route as-is",
			basePath: "",
			route:    "/health",
			expected: "/health",
		},
		{
			name:     "combines base path with route",
			basePath: "/api",
			route:    "/health",
			expected: "/api/health",
		},
		{
			name:     "handles root route with base path",
			basePath: "/api",
			route:    "/",
			expected: "/api",
		},
		{
			name:     "handles complex paths",
			basePath: "/api/v1",
			route:    "/users/:id",
			expected: "/api/v1/users/:id",
		},
		{
			name:     "handles root base path",
			basePath: "/",
			route:    "/health",
			expected: "/health",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &Server{
				basePath: tt.basePath,
			}
			result := server.buildFullPath(tt.route)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestModuleGroupAppliesBasePath(t *testing.T) {
	e := echo.New()
	s := &Server{echo: e, basePath: "/api"}

	group := s.ModuleGroup()
	require.NotNil(t, group)

	group.Add(http.MethodGet, "/ping", func(c echo.Context) error {
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
	group.Add(http.MethodGet, "/ping", func(c echo.Context) error {
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
	group.Add(http.MethodGet, "/ping", func(c echo.Context) error {
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

func TestServer_CustomBasePath(t *testing.T) {
	tests := []struct {
		name             string
		basePath         string
		expectedBasePath string
	}{
		{
			name:             "empty base path",
			basePath:         "",
			expectedBasePath: "",
		},
		{
			name:             "api base path",
			basePath:         "/api",
			expectedBasePath: "/api",
		},
		{
			name:             "versioned api base path",
			basePath:         "/api/v1",
			expectedBasePath: "/api/v1",
		},
		{
			name:             "base path without leading slash",
			basePath:         "api/v2",
			expectedBasePath: "/api/v2",
		},
		{
			name:             "base path with trailing slash",
			basePath:         "/api/v1/",
			expectedBasePath: "/api/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Server: config.ServerConfig{
					BasePath:    tt.basePath,
					HealthRoute: "/health",
					ReadyRoute:  "/ready",
				},
			}

			log := logger.New("error", false)
			server := New(cfg, log)

			assert.Equal(t, tt.expectedBasePath, server.basePath)
		})
	}
}

func TestServer_CustomHealthRoutes(t *testing.T) {
	tests := []struct {
		name               string
		healthRoute        string
		readyRoute         string
		expectedHealthPath string
		expectedReadyPath  string
	}{
		{
			name:               "default routes",
			healthRoute:        "",
			readyRoute:         "",
			expectedHealthPath: "/health",
			expectedReadyPath:  "/ready",
		},
		{
			name:               "custom health route",
			healthRoute:        "/custom-health",
			readyRoute:         "",
			expectedHealthPath: "/custom-health",
			expectedReadyPath:  "/ready",
		},
		{
			name:               "custom ready route",
			healthRoute:        "",
			readyRoute:         "/custom-ready",
			expectedHealthPath: "/health",
			expectedReadyPath:  "/custom-ready",
		},
		{
			name:               "both custom routes",
			healthRoute:        "/status",
			readyRoute:         "/ping",
			expectedHealthPath: "/status",
			expectedReadyPath:  "/ping",
		},
		{
			name:               "routes without leading slash",
			healthRoute:        "health-check",
			readyRoute:         "readiness",
			expectedHealthPath: "/health-check",
			expectedReadyPath:  "/readiness",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Server: config.ServerConfig{
					BasePath:    "",
					HealthRoute: tt.healthRoute,
					ReadyRoute:  tt.readyRoute,
				},
			}

			log := logger.New("error", false)
			server := New(cfg, log)

			assert.Equal(t, tt.expectedHealthPath, server.healthRoute)
			assert.Equal(t, tt.expectedReadyPath, server.readyRoute)
		})
	}
}

func TestServer_HealthEndpointsWithBasePath(t *testing.T) {
	tests := []struct {
		name           string
		basePath       string
		healthRoute    string
		readyRoute     string
		testHealthPath string
		testReadyPath  string
		shouldWork     bool
	}{
		{
			name:           "no base path, default routes",
			basePath:       "",
			healthRoute:    "",
			readyRoute:     "",
			testHealthPath: "/health",
			testReadyPath:  "/ready",
			shouldWork:     true,
		},
		{
			name:           "with base path, default routes",
			basePath:       "/api",
			healthRoute:    "",
			readyRoute:     "",
			testHealthPath: "/api/health",
			testReadyPath:  "/api/ready",
			shouldWork:     true,
		},
		{
			name:           "with base path, custom routes",
			basePath:       "/api/v1",
			healthRoute:    "/status",
			readyRoute:     "/ping",
			testHealthPath: "/api/v1/status",
			testReadyPath:  "/api/v1/ping",
			shouldWork:     true,
		},
		{
			name:           "wrong path should fail",
			basePath:       "/api",
			healthRoute:    "/status",
			readyRoute:     "",
			testHealthPath: "/health", // This should fail
			testReadyPath:  "/ready",  // This should fail
			shouldWork:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Server: config.ServerConfig{
					BasePath:    tt.basePath,
					HealthRoute: tt.healthRoute,
					ReadyRoute:  tt.readyRoute,
				},
			}

			log := logger.New("error", false)
			server := New(cfg, log)

			// Test health endpoint
			req := httptest.NewRequest(http.MethodGet, tt.testHealthPath, http.NoBody)
			rec := httptest.NewRecorder()
			server.Echo().ServeHTTP(rec, req)

			if tt.shouldWork {
				assert.Equal(t, http.StatusOK, rec.Code, "Health endpoint should respond with 200")
			} else {
				assert.Equal(t, http.StatusNotFound, rec.Code, "Health endpoint should respond with 404 for wrong path")
			}

			// Test ready endpoint
			req = httptest.NewRequest(http.MethodGet, tt.testReadyPath, http.NoBody)
			rec = httptest.NewRecorder()
			server.Echo().ServeHTTP(rec, req)

			if tt.shouldWork {
				assert.Equal(t, http.StatusOK, rec.Code, "Ready endpoint should respond with 200")
			} else {
				assert.Equal(t, http.StatusNotFound, rec.Code, "Ready endpoint should respond with 404 for wrong path")
			}
		})
	}
}

func TestServer_ModuleGroup(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
	}{
		{
			name:     "empty base path",
			basePath: "",
		},
		{
			name:     "api base path",
			basePath: "/api",
		},
		{
			name:     "versioned api base path",
			basePath: "/api/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Server: config.ServerConfig{
					BasePath:    tt.basePath,
					HealthRoute: "/health",
					ReadyRoute:  "/ready",
				},
			}

			log := logger.New("error", false)
			server := New(cfg, log)

			group := server.ModuleGroup()
			require.NotNil(t, group)
			raw := any(group)
			_, isEchoGroup := raw.(*echo.Group)
			assert.False(t, isEchoGroup, "ModuleGroup should not expose raw echo.Group")
			_, isRouteGroup := raw.(*routeGroup)
			assert.True(t, isRouteGroup, "ModuleGroup should return internal routeGroup wrapper")

			registrations := []struct {
				name         string
				registerPath string
				expectedPath string
				responseBody string
			}{
				{
					name:         "relative path",
					registerPath: "/test",
					expectedPath: group.FullPath("/test"),
					responseBody: "relative",
				},
			}

			if tt.basePath != "" {
				registrations = append(registrations, struct {
					name         string
					registerPath string
					expectedPath string
					responseBody string
				}{
					name:         "prefixed path",
					registerPath: tt.basePath + "/prefixed",
					expectedPath: group.FullPath(tt.basePath + "/prefixed"),
					responseBody: "prefixed",
				})
			}

			for _, reg := range registrations {
				reg := reg
				group.Add(http.MethodGet, reg.registerPath, func(c echo.Context) error {
					return c.String(http.StatusOK, reg.responseBody)
				})

				req := httptest.NewRequest(http.MethodGet, reg.expectedPath, http.NoBody)
				rec := httptest.NewRecorder()
				server.Echo().ServeHTTP(rec, req)

				assert.Equal(t, http.StatusOK, rec.Code)
				assert.Equal(t, reg.responseBody, rec.Body.String(), reg.name)
			}

			// Nested group should inherit base path and normalize inputs
			nested := group.Group("/v1")
			require.NotNil(t, nested)
			_, nestedIsRouteGroup := nested.(*routeGroup)
			assert.True(t, nestedIsRouteGroup, "nested group should use wrapper")

			nestedPath := nested.FullPath("/resource")
			nested.Add(http.MethodGet, "/resource", func(c echo.Context) error {
				return c.String(http.StatusOK, "nested")
			})

			req := httptest.NewRequest(http.MethodGet, nestedPath, http.NoBody)
			rec := httptest.NewRecorder()
			server.Echo().ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "nested", rec.Body.String())

			// Paths including the base prefix should not duplicate the base path
			if tt.basePath != "" {
				dedupPath := nested.FullPath(tt.basePath + "/v1/dedup")
				nested.Add(http.MethodGet, tt.basePath+"/v1/dedup", func(c echo.Context) error {
					return c.String(http.StatusOK, "dedup")
				})

				req = httptest.NewRequest(http.MethodGet, dedupPath, http.NoBody)
				rec = httptest.NewRecorder()
				server.Echo().ServeHTTP(rec, req)
				assert.Equal(t, http.StatusOK, rec.Code)
				assert.Equal(t, "dedup", rec.Body.String())
			}

			full := group.FullPath("/")
			if tt.basePath == "" {
				assert.Equal(t, "/", full)
			} else {
				assert.Equal(t, normalizeBasePath(tt.basePath), full)
			}
		})
	}
}
