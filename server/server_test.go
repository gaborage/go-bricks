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

// Test constants for common values
const (
	testHealthRoute = "/health"
	testReadyRoute  = "/ready"
	testAPIV1Path   = "/api/v1"
	testPingPath    = "/ping"
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

// Test helpers for common setup patterns
func newTestConfig(basePath, healthRoute, readyRoute string) *config.Config {
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
			BasePath:     basePath,
			HealthRoute:  healthRoute,
			ReadyRoute:   readyRoute,
		},
	}
}

func newTestServer(basePath, healthRoute, readyRoute string) *Server {
	cfg := newTestConfig(basePath, healthRoute, readyRoute)
	log := &testLogger{}
	return New(cfg, log)
}

func assertHTTPGetResponse(t *testing.T, server *Server, path string, expectedStatus int, expectedBody ...string) {
	req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
	rec := httptest.NewRecorder()
	server.Echo().ServeHTTP(rec, req)

	assert.Equal(t, expectedStatus, rec.Code)
	if len(expectedBody) > 0 {
		assert.Contains(t, rec.Body.String(), expectedBody[0])
	}
}

func assertHealthEndpoints(t *testing.T, server *Server, healthPath, readyPath string) {
	// Test health endpoint
	req := httptest.NewRequest(http.MethodGet, healthPath, http.NoBody)
	rec := httptest.NewRecorder()
	server.Echo().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Test ready endpoint with JSON validation
	req = httptest.NewRequest(http.MethodGet, readyPath, http.NoBody)
	rec = httptest.NewRecorder()
	server.Echo().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var readyPayload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &readyPayload))
	assert.Equal(t, "ready", readyPayload["status"])
	assert.NotZero(t, readyPayload["time"])
}

func TestServerNewInitializesEchoAndRoutes(t *testing.T) {
	srv := newTestServer("", "", "")
	require.NotNil(t, srv)

	e := srv.Echo()
	require.NotNil(t, e)
	assert.True(t, e.HideBanner)
	assert.True(t, e.HidePort)
	require.NotNil(t, e.Validator)

	// Test default health endpoints
	assertHealthEndpoints(t, srv, testHealthRoute, testReadyRoute)
}

func TestServerStartAndShutdown(t *testing.T) {
	srv := newTestServer("", "", "")
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
	srv := newTestServer("", "", "")
	require.NotNil(t, srv)

	e := srv.Echo()
	require.NotNil(t, e)
	assert.Same(t, e, srv.echo)
}

func TestPathNormalization(t *testing.T) {
	t.Run("normalizeBasePath", func(t *testing.T) {
		tests := []struct {
			name     string
			input    string
			expected string
		}{
			{"empty string returns empty", "", ""},
			{"root path returns root", "/", "/"},
			{"adds leading slash", "api", "/api"},
			{"removes trailing slash", "/api/", "/api"},
			{"handles multiple trailing slashes", "/api///", "/api"},
			{"handles path with subdirectories", "api/v1/test", "/api/v1/test"},
			{"handles path with subdirectories and trailing slash", "/api/v1/test/", "/api/v1/test"},
			{"handles already normalized path", "/api/v1", "/api/v1"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := normalizeBasePath(tt.input)
				assert.Equal(t, tt.expected, result)
			})
		}
	})

	t.Run("normalizeRoutePath", func(t *testing.T) {
		tests := []struct {
			name         string
			route        string
			defaultRoute string
			expected     string
		}{
			{"empty route uses default", "", "/health", "/health"},
			{"adds leading slash to route", "custom-health", "/health", "/custom-health"},
			{"preserves route with leading slash", "/custom-health", "/health", "/custom-health"},
			{"handles root route", "/", "/health", "/"},
			{"handles complex route path", "/api/v1/status", "/health", "/api/v1/status"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := normalizeRoutePath(tt.route, tt.defaultRoute)
				assert.Equal(t, tt.expected, result)
			})
		}
	})

	t.Run("buildFullPath", func(t *testing.T) {
		tests := []struct {
			name     string
			basePath string
			route    string
			expected string
		}{
			{"empty base path returns route as-is", "", "/health", "/health"},
			{"combines base path with route", "/api", "/health", "/api/health"},
			{"handles root route with base path", "/api", "/", "/api"},
			{"handles complex paths", "/api/v1", "/users/:id", "/api/v1/users/:id"},
			{"handles root base path", "/", "/health", "/health"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				server := &Server{basePath: tt.basePath}
				result := server.buildFullPath(tt.route)
				assert.Equal(t, tt.expected, result)
			})
		}
	})
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

func TestServerConfiguration(t *testing.T) {
	tests := []struct {
		name               string
		basePath           string
		healthRoute        string
		readyRoute         string
		expectedBasePath   string
		expectedHealthPath string
		expectedReadyPath  string
		finalHealthPath    string
		finalReadyPath     string
	}{
		{
			name:               "defaults",
			basePath:           "",
			healthRoute:        "",
			readyRoute:         "",
			expectedBasePath:   "",
			expectedHealthPath: "/health",
			expectedReadyPath:  "/ready",
			finalHealthPath:    "/health",
			finalReadyPath:     "/ready",
		},
		{
			name:               "base path normalization",
			basePath:           "api/v1/",
			healthRoute:        "",
			readyRoute:         "",
			expectedBasePath:   "/api/v1",
			expectedHealthPath: "/health",
			expectedReadyPath:  "/ready",
			finalHealthPath:    "/api/v1/health",
			finalReadyPath:     "/api/v1/ready",
		},
		{
			name:               "custom routes without base path",
			basePath:           "",
			healthRoute:        "status",
			readyRoute:         "ping",
			expectedBasePath:   "",
			expectedHealthPath: "/status",
			expectedReadyPath:  "/ping",
			finalHealthPath:    "/status",
			finalReadyPath:     "/ping",
		},
		{
			name:               "custom routes with base path",
			basePath:           "/api/v1",
			healthRoute:        "/status",
			readyRoute:         "/ping",
			expectedBasePath:   "/api/v1",
			expectedHealthPath: "/status",
			expectedReadyPath:  "/ping",
			finalHealthPath:    "/api/v1/status",
			finalReadyPath:     "/api/v1/ping",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(tt.basePath, tt.healthRoute, tt.readyRoute)

			// Test configuration values
			assert.Equal(t, tt.expectedBasePath, server.basePath)
			assert.Equal(t, tt.expectedHealthPath, server.healthRoute)
			assert.Equal(t, tt.expectedReadyPath, server.readyRoute)

			// Test actual endpoint behavior
			assertHealthEndpoints(t, server, tt.finalHealthPath, tt.finalReadyPath)

			// Test wrong paths return 404
			if tt.finalHealthPath != testHealthRoute {
				assertHTTPGetResponse(t, server, testHealthRoute, http.StatusNotFound)
			}
			if tt.finalReadyPath != testReadyRoute {
				assertHTTPGetResponse(t, server, testReadyRoute, http.StatusNotFound)
			}
		})
	}
}

func TestModuleGroupBehavior(t *testing.T) {
	t.Run("wrapper implementation", func(t *testing.T) {
		server := newTestServer("/api", "", "")
		group := server.ModuleGroup()
		require.NotNil(t, group)

		// Verify wrapper type, not raw Echo group
		raw := any(group)
		_, isEchoGroup := raw.(*echo.Group)
		assert.False(t, isEchoGroup, "ModuleGroup should not expose raw echo.Group")
		_, isRouteGroup := raw.(*routeGroup)
		assert.True(t, isRouteGroup, "ModuleGroup should return internal routeGroup wrapper")
	})

	t.Run("base path application", func(t *testing.T) {
		tests := []struct {
			name         string
			basePath     string
			registerPath string
			expectedURL  string
		}{
			{"no base path", "", "/ping", "/ping"},
			{"with base path", "/api", "/ping", "/api/ping"},
			{"root base path", "/", "/ping", "/ping"},
			{"versioned api", "/api/v1", "/test", "/api/v1/test"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				server := newTestServer(tt.basePath, "", "")
				group := server.ModuleGroup()

				group.Add(http.MethodGet, tt.registerPath, func(c echo.Context) error {
					return c.NoContent(http.StatusNoContent)
				})

				assertHTTPGetResponse(t, server, tt.expectedURL, http.StatusNoContent)
			})
		}
	})

	t.Run("nested groups and deduplication", func(t *testing.T) {
		server := newTestServer("/api", "", "")
		group := server.ModuleGroup()

		// Test nested group behavior
		nested := group.Group("/v1")
		require.NotNil(t, nested)
		_, isRouteGroup := nested.(*routeGroup)
		assert.True(t, isRouteGroup, "nested group should use wrapper")

		// Add route to nested group
		nested.Add(http.MethodGet, "/resource", func(c echo.Context) error {
			return c.String(http.StatusOK, "nested")
		})
		assertHTTPGetResponse(t, server, testAPIV1Path+"/resource", http.StatusOK, "nested")

		// Test deduplication - path with base prefix shouldn't duplicate
		nested.Add(http.MethodGet, testAPIV1Path+"/dedup", func(c echo.Context) error {
			return c.String(http.StatusOK, "dedup")
		})
		assertHTTPGetResponse(t, server, testAPIV1Path+"/dedup", http.StatusOK, "dedup")

		// Test FullPath method
		assert.Equal(t, "/api/v1/test", nested.FullPath("/test"))
		assert.Equal(t, "/api", group.FullPath("/"))
	})
}
