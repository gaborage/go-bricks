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

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// Test constants for common values
const (
	testReadyRoute    = "/ready"
	testAPIV1Path     = "/api/v1"
	apiTestURL        = "/api/v1/test"
	healthRoute       = "/health"
	customHealthRoute = "/custom-health"
	apiRoute          = "/api/v1/status"
	statusRoute       = "/status"
)

// testLogEntry captures a single log emission with its level, message, structured field
// keys, and (for Str fields) the values actually recorded — post-filtering when the
// owning testLogger carries a SensitiveDataFilter, so tests can assert on masking.
type testLogEntry struct {
	level  string
	msg    string
	fields []string
	values map[string]string
}

// testLogger is a fake logger.Logger. When filter is non-nil, Str() applies the real
// logger.SensitiveDataFilter (mirroring logger.LogEventAdapter.Str) so tests can verify
// that values routed through the framework logger are actually masked.
type testLogger struct {
	mu      sync.Mutex
	entries []string
	logs    []testLogEntry
	filter  *logger.SensitiveDataFilter
}

type testLogEvent struct {
	logger *testLogger
	level  string
	fields []string
	values map[string]string
}

func (l *testLogger) Info() logger.LogEvent  { return &testLogEvent{logger: l, level: "info"} }
func (l *testLogger) Error() logger.LogEvent { return &testLogEvent{logger: l, level: "error"} }
func (l *testLogger) Debug() logger.LogEvent { return &testLogEvent{logger: l, level: "debug"} }
func (l *testLogger) Warn() logger.LogEvent  { return &testLogEvent{logger: l, level: "warn"} }
func (l *testLogger) Fatal() logger.LogEvent { return &testLogEvent{logger: l, level: "fatal"} }
func (l *testLogger) WithContext(any) logger.Logger {
	return l
}
func (l *testLogger) WithFields(map[string]any) logger.Logger {
	return l
}

// logEntries returns a copy of all captured log entries.
func (l *testLogger) logEntries() []testLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	cp := make([]testLogEntry, len(l.logs))
	copy(cp, l.logs)
	return cp
}

func (e *testLogEvent) Msg(msg string) {
	e.logger.mu.Lock()
	defer e.logger.mu.Unlock()
	e.logger.entries = append(e.logger.entries, fmt.Sprintf("%s:%s", e.level, msg))
	e.logger.logs = append(e.logger.logs, testLogEntry{level: e.level, msg: msg, fields: e.fields, values: e.values})
}

func (e *testLogEvent) Msgf(format string, args ...any) { e.Msg(fmt.Sprintf(format, args...)) }
func (e *testLogEvent) Err(error) logger.LogEvent {
	e.fields = append(e.fields, "error")
	return e
}
func (e *testLogEvent) Str(key, value string) logger.LogEvent {
	e.fields = append(e.fields, key)
	if e.logger.filter != nil {
		value = e.logger.filter.FilterString(key, value)
	}
	if e.values == nil {
		e.values = make(map[string]string)
	}
	e.values[key] = value
	return e
}
func (e *testLogEvent) Int(key string, _ int) logger.LogEvent {
	e.fields = append(e.fields, key)
	return e
}
func (e *testLogEvent) Int64(key string, _ int64) logger.LogEvent {
	e.fields = append(e.fields, key)
	return e
}
func (e *testLogEvent) Uint64(key string, _ uint64) logger.LogEvent {
	e.fields = append(e.fields, key)
	return e
}
func (e *testLogEvent) Dur(key string, _ time.Duration) logger.LogEvent {
	e.fields = append(e.fields, key)
	return e
}
func (e *testLogEvent) Interface(key string, _ any) logger.LogEvent {
	e.fields = append(e.fields, key)
	return e
}
func (e *testLogEvent) Bytes(key string, _ []byte) logger.LogEvent {
	e.fields = append(e.fields, key)
	return e
}
func (e *testLogEvent) Bool(key string, _ bool) logger.LogEvent {
	e.fields = append(e.fields, key)
	return e
}
func (e *testLogEvent) Enabled() bool { return true }

// Test helpers for common setup patterns
func newTestConfig(basePath, healthRoute, readyRoute string) *config.Config {
	return &config.Config{
		App: config.AppConfig{
			Name:    "test-service",
			Version: "1.0.0",
			Env:     "development",
		},
		Server: config.ServerConfig{
			Host: "127.0.0.1",
			Port: 0,
			Timeout: config.TimeoutConfig{
				Read:  50 * time.Millisecond,
				Write: 50 * time.Millisecond,
			},
			Path: config.PathConfig{
				Base:   basePath,
				Health: healthRoute,
				Ready:  readyRoute,
			},
		},
	}
}

func newTestServer(basePath, healthRoute, readyRoute string) *Server {
	cfg := newTestConfig(basePath, healthRoute, readyRoute)
	log := &testLogger{}
	return New(cfg, log)
}

func assertHTTPGetResponse(t *testing.T, server *Server, path string, expectedStatus int, expectedBody ...string) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	rec := httptest.NewRecorder()
	server.echo.ServeHTTP(rec, req)

	assert.Equal(t, expectedStatus, rec.Code)
	if len(expectedBody) > 0 {
		assert.Contains(t, rec.Body.String(), expectedBody[0])
	}
}

func assertHealthEndpoints(t *testing.T, server *Server, healthPath, readyPath string) {
	// Test health endpoint
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, healthPath, http.NoBody)
	rec := httptest.NewRecorder()
	server.echo.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Test ready endpoint with JSON validation
	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, readyPath, http.NoBody)
	rec = httptest.NewRecorder()
	server.echo.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var readyPayload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &readyPayload))
	assert.Equal(t, "ready", readyPayload["status"])
	assert.NotZero(t, readyPayload["time"])
}

func TestServerNewInitializesEchoAndRoutes(t *testing.T) {
	srv := newTestServer("", "", "")
	require.NotNil(t, srv)

	e := srv.echo
	require.NotNil(t, e)
	// HideBanner/HidePort moved to StartConfig in Echo v5
	require.NotNil(t, e.Validator)

	// Test default health endpoints
	assertHealthEndpoints(t, srv, healthRoute, testReadyRoute)
}

func TestServerStartAndShutdown(t *testing.T) {
	srv := newTestServer("", "", "")
	require.NotNil(t, srv)

	errCh := make(chan error, 1)

	go func() {
		errCh <- srv.Start()
	}()

	// Wait until BeforeServeFunc stores the *http.Server, which fires
	// immediately before the server starts accepting connections. This
	// replaces a fragile time.Sleep with a deterministic readiness check.
	deadline := time.Now().Add(2 * time.Second)
	for srv.httpServer.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("server did not become ready within timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)

	require.NoError(t, srv.Shutdown(ctx))

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("unexpected error from server: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down in time")
	}
}

// TestRootGroupRegistersAtURLRoot verifies RootGroup() returns a working registrar
// rooted at the engine with NO base path applied, even when a base path is configured.
// This replaces the former Echo() accessor for framework-internal root endpoints.
func TestRootGroupRegistersAtURLRoot(t *testing.T) {
	srv := newTestServer("/api", "", "") // base path set — RootGroup must ignore it
	root := srv.RootGroup()
	require.NotNil(t, root)
	_, isRouteGroup := any(root).(*routeGroup)
	assert.True(t, isRouteGroup, "RootGroup should return the internal routeGroup wrapper")

	root.Add(http.MethodGet, "/_sys/ping", func(c HandlerContext) error {
		return c.String(http.StatusOK, "pong")
	})

	// Endpoint must sit at the URL root, NOT under the /api base path.
	assertHTTPGetResponse(t, srv, "/_sys/ping", http.StatusOK, "pong")
	assertHTTPGetResponse(t, srv, "/api/_sys/ping", http.StatusNotFound)
}

// TestRegisterReadyHandlerOverrideAndRestore verifies a custom go-bricks Handler
// overrides the readiness endpoint, and passing nil restores the framework default.
func TestRegisterReadyHandlerOverrideAndRestore(t *testing.T) {
	srv := newTestServer("", "", "")

	// Override with a custom handler.
	srv.RegisterReadyHandler(func(c HandlerContext) error {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"status": "draining"})
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, testReadyRoute, http.NoBody)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var payload map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "draining", payload["status"])

	// nil restores the default ready handler (200 + status:ready).
	srv.RegisterReadyHandler(nil)
	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, testReadyRoute, http.NoBody)
	rec = httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var restored map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &restored))
	assert.Equal(t, "ready", restored["status"])
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
			{"handles path with subdirectories", "api/v1/test", apiTestURL},
			{"handles path with subdirectories and trailing slash", "/api/v1/test/", apiTestURL},
			{"handles already normalized path", testAPIV1Path, testAPIV1Path},
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
			{"empty route uses default", "", healthRoute, healthRoute},
			{"adds leading slash to route", "custom-health", healthRoute, customHealthRoute},
			{"preserves route with leading slash", customHealthRoute, healthRoute, customHealthRoute},
			{"handles root route", "/", healthRoute, "/"},
			{"handles complex route path", apiRoute, healthRoute, apiRoute},
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
			{"empty base path returns route as-is", "", healthRoute, healthRoute},
			{"combines base path with route", "/api", healthRoute, "/api/health"},
			{"handles root route with base path", "/api", "/", "/api"},
			{"handles complex paths", testAPIV1Path, "/users/:id", "/api/v1/users/:id"},
			{"handles root base path", "/", healthRoute, healthRoute},
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
			expectedHealthPath: healthRoute,
			expectedReadyPath:  testReadyRoute,
			finalHealthPath:    healthRoute,
			finalReadyPath:     testReadyRoute,
		},
		{
			name:               "base path normalization",
			basePath:           "api/v1/",
			healthRoute:        "",
			readyRoute:         "",
			expectedBasePath:   testAPIV1Path,
			expectedHealthPath: healthRoute,
			expectedReadyPath:  testReadyRoute,
			finalHealthPath:    "/api/v1/health",
			finalReadyPath:     "/api/v1/ready",
		},
		{
			name:               "custom routes without base path",
			basePath:           "",
			healthRoute:        "status",
			readyRoute:         "ping",
			expectedBasePath:   "",
			expectedHealthPath: statusRoute,
			expectedReadyPath:  "/ping",
			finalHealthPath:    statusRoute,
			finalReadyPath:     "/ping",
		},
		{
			name:               "custom routes with base path",
			basePath:           testAPIV1Path,
			healthRoute:        statusRoute,
			readyRoute:         "/ping",
			expectedBasePath:   testAPIV1Path,
			expectedHealthPath: statusRoute,
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
			if tt.finalHealthPath != healthRoute {
				assertHTTPGetResponse(t, server, healthRoute, http.StatusNotFound)
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
			{"versioned api", testAPIV1Path, "/test", apiTestURL},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				server := newTestServer(tt.basePath, "", "")
				group := server.ModuleGroup()

				group.Add(http.MethodGet, tt.registerPath, func(c HandlerContext) error {
					return c.String(http.StatusNoContent, "")
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
		nested.Add(http.MethodGet, "/resource", func(c HandlerContext) error {
			return c.String(http.StatusOK, "nested")
		})
		assertHTTPGetResponse(t, server, testAPIV1Path+"/resource", http.StatusOK, "nested")

		// Test deduplication - path with base prefix shouldn't duplicate
		nested.Add(http.MethodGet, testAPIV1Path+"/dedup", func(c HandlerContext) error {
			return c.String(http.StatusOK, "dedup")
		})
		assertHTTPGetResponse(t, server, testAPIV1Path+"/dedup", http.StatusOK, "dedup")

		// Test FullPath method
		assert.Equal(t, apiTestURL, nested.FullPath("/test"))
		assert.Equal(t, "/api", group.FullPath("/"))
	})
}

// ==================== Raw Mode Custom Error Handler Tests ====================

func TestCustomErrorHandlerRawMode(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development", Debug: true}}
	e := echo.New()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Set raw response mode (as handlerWrapper.wrap does)
	c.Set(rawResponseContextKey, true)

	// Simulate an IAPIError
	apiErr := NewNotFoundError("Resource")
	customErrorHandler(c, apiErr, cfg, &testLogger{})

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assert.Equal(t, "NOT_FOUND", raw["code"])
	assert.Equal(t, "Resource not found", raw["message"])

	// No envelope keys
	_, hasMeta := raw["meta"]
	_, hasError := raw["error"]
	assert.False(t, hasMeta, "raw error handler should not produce meta key")
	assert.False(t, hasError, "raw error handler should not produce error envelope key")
}

func TestCustomErrorHandlerRawModeTimeout(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	e := echo.New()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	c.Set(rawResponseContextKey, true)

	customErrorHandler(c, context.DeadlineExceeded, cfg, &testLogger{})

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assert.Equal(t, "SERVICE_UNAVAILABLE", raw["code"])
	assert.Equal(t, "Request processing timed out", raw["message"])

	_, hasMeta := raw["meta"]
	assert.False(t, hasMeta, "raw timeout error should not produce meta key")
}

func TestCustomErrorHandlerRawModeEchoHTTPError(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	e := echo.New()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	c.Set(rawResponseContextKey, true)

	echoErr := echo.NewHTTPError(http.StatusForbidden, "access denied")
	customErrorHandler(c, echoErr, cfg, &testLogger{})

	assert.Equal(t, http.StatusForbidden, rec.Code)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assert.Equal(t, "FORBIDDEN", raw["code"])
	assert.Equal(t, "access denied", raw["message"])

	_, hasMeta := raw["meta"]
	assert.False(t, hasMeta, "raw echo error should not produce meta key")
}

// ==================== 5xx Error Logging Security Tests ====================

// findLogEntry returns the first captured entry with the given message, or nil.
func findLogEntry(entries []testLogEntry, msg string) *testLogEntry {
	for i := range entries {
		if entries[i].msg == msg {
			return &entries[i]
		}
	}
	return nil
}

// TestClassifyError5xxUsesFilteredLogger proves the unhandled-5xx log line goes through
// the injected framework logger (instead of Echo's stock logger, which had no filter
// attached and wrote raw to stdout unconditionally) AND that, under the framework's real
// default filter config in non-debug mode (the realistic default for most deployments),
// a driver error embedding PII/PCI never reaches the log verbatim. The SensitiveDataFilter
// only masks by field name — it cannot scan message content — so the guarantee here comes
// from omitting the raw error text in non-debug mode, not from field-name matching alone.
func TestClassifyError5xxUsesFilteredLogger(t *testing.T) {
	const sensitiveValue = "topsecret-driver-value-1234"

	testLog := &testLogger{filter: logger.NewSensitiveDataFilter(logger.DefaultFilterConfig())}

	cfg := newTestConfig("", "", "") // Debug defaults to false — the realistic production case
	server := New(cfg, testLog)
	server.ModuleGroup().Add(http.MethodGet, "/boom", func(HandlerContext) error {
		return fmt.Errorf("duplicate key value violates unique constraint: %s", sensitiveValue)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/boom", http.NoBody)
	rec := httptest.NewRecorder()
	server.echo.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	found := findLogEntry(testLog.logEntries(), "unhandled error")
	require.NotNil(t, found, "expected the framework logger to receive the unhandled-error log line")
	assert.Equal(t, "error", found.level)
	assert.NotContains(t, found.values["error"], sensitiveValue, "raw sensitive value must not appear verbatim in the log")
	for _, v := range found.values {
		assert.NotContains(t, v, sensitiveValue, "raw sensitive value must not appear in any logged field")
	}
}

// TestClassifyError5xxDebugModeLogsFullError documents the accepted trade-off: debug mode
// (opt-in, mirrors the response-body redaction toggle above it) restores full error detail
// for troubleshooting, subject only to the field-name filter as defense-in-depth.
func TestClassifyError5xxDebugModeLogsFullError(t *testing.T) {
	testLog := &testLogger{filter: logger.NewSensitiveDataFilter(logger.DefaultFilterConfig())}

	cfg := newTestConfig("", "", "")
	cfg.App.Debug = true
	server := New(cfg, testLog)
	server.ModuleGroup().Add(http.MethodGet, "/boom", func(HandlerContext) error {
		return fmt.Errorf("boom detail")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/boom", http.NoBody)
	rec := httptest.NewRecorder()
	server.echo.ServeHTTP(rec, req)

	found := findLogEntry(testLog.logEntries(), "unhandled error")
	require.NotNil(t, found, "expected the framework logger to receive the unhandled-error log line")
	assert.Equal(t, "boom detail", found.values["error"])
}

// TestClassifyError4xxDoesNotLogAsServerError ensures 4xx responses never emit the
// server-error log line (only >=500 status codes should).
func TestClassifyError4xxDoesNotLogAsServerError(t *testing.T) {
	testLog := &testLogger{}
	cfg := newTestConfig("", "", "")
	server := New(cfg, testLog)
	server.ModuleGroup().Add(http.MethodGet, "/missing", func(HandlerContext) error {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/missing", http.NoBody)
	rec := httptest.NewRecorder()
	server.echo.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	for _, entry := range testLog.logEntries() {
		assert.NotEqual(t, "unhandled error", entry.msg, "4xx responses must not emit the server-error log line")
	}
}

// TestAppendErrorDetailRedactsByDebugMode verifies the shared error-detail helper
// used by BOTH the panic-recovery and unhandled-5xx log paths: production logs the
// error type only (never the raw message, which can embed driver PII/PCI), debug
// restores the raw message, and a nil error adds no field.
func TestAppendErrorDetailRedactsByDebugMode(t *testing.T) {
	sensitive := fmt.Errorf("duplicate key value: Key (pan)=(4111111111111111)")

	t.Run("prod_logs_type_not_message", func(t *testing.T) {
		tl := &testLogger{}
		appendErrorDetail(tl.Error().Str("request_id", "r1"), sensitive, false).Msg("unhandled error")
		e := findLogEntry(tl.logEntries(), "unhandled error")
		require.NotNil(t, e)
		assert.Contains(t, e.fields, "error_type")
		assert.NotContains(t, e.fields, "error")
		assert.NotContains(t, e.values["error_type"], "4111111111111111")
	})

	t.Run("debug_logs_raw_message", func(t *testing.T) {
		tl := &testLogger{}
		appendErrorDetail(tl.Error().Str("request_id", "r1"), sensitive, true).Msg("unhandled error")
		e := findLogEntry(tl.logEntries(), "unhandled error")
		require.NotNil(t, e)
		assert.Equal(t, sensitive.Error(), e.values["error"])
	})

	t.Run("nil_error_adds_no_field", func(t *testing.T) {
		tl := &testLogger{}
		appendErrorDetail(tl.Error().Str("request_id", "r1"), nil, false).Msg("done")
		e := findLogEntry(tl.logEntries(), "done")
		require.NotNil(t, e)
		assert.NotContains(t, e.fields, "error")
		assert.NotContains(t, e.fields, "error_type")
	})
}
