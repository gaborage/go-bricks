package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

const (
	debugPath     = "/_debug"
	localhostIPV4 = "127.0.0.1"
	debugInfoPath = debugPath + "/info"
	testIPAddress = "127.0.0.1:12345"
)

func TestDebugEndpointsIntegration(t *testing.T) {
	// Create test configuration with debug enabled
	debugConfig := &config.DebugConfig{
		Enabled:     true,
		PathPrefix:  debugPath,
		AllowedIPs:  []string{localhostIPV4},
		BearerToken: "",
		Endpoints: config.DebugEndpointsConfig{
			Info:       true,
			Goroutines: true,
			GC:         true,
			Health:     true,
		},
	}

	// Create test logger
	testLogger := logger.New("info", false)

	// Create minimal app
	app := &App{
		logger: testLogger,
	}

	// Create debug handlers
	debugHandlers := NewDebugHandlers(app, debugConfig, testLogger)

	// Create Echo instance and register debug endpoints
	e := echo.New()
	debugHandlers.RegisterDebugEndpoints(e)

	// Test that info endpoint is accessible from localhost
	req := httptest.NewRequest(http.MethodGet, debugInfoPath, http.NoBody)
	req.RemoteAddr = testIPAddress
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "goroutines")
	assert.Contains(t, rec.Body.String(), "go_version")
}

func TestDebugEndpointsIPRestriction(t *testing.T) {
	// Create test configuration with debug enabled but restricted IPs
	debugConfig := &config.DebugConfig{
		Enabled:     true,
		PathPrefix:  debugPath,
		AllowedIPs:  []string{"192.168.1.1"}, // Different IP
		BearerToken: "",
	}

	testLogger := logger.New("info", false)
	app := &App{logger: testLogger}
	debugHandlers := NewDebugHandlers(app, debugConfig, testLogger)

	e := echo.New()
	debugHandlers.RegisterDebugEndpoints(e)

	// Test that request from non-allowed IP is rejected
	req := httptest.NewRequest(http.MethodGet, debugInfoPath, http.NoBody)
	req.RemoteAddr = testIPAddress // Not in allowed list
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestDebugEndpointsDisabled(t *testing.T) {
	// Create test configuration with debug disabled
	debugConfig := &config.DebugConfig{
		Enabled:     false,
		PathPrefix:  debugPath,
		AllowedIPs:  []string{localhostIPV4},
		BearerToken: "",
	}

	testLogger := logger.New("info", false)
	app := &App{logger: testLogger}
	debugHandlers := NewDebugHandlers(app, debugConfig, testLogger)

	e := echo.New()
	debugHandlers.RegisterDebugEndpoints(e)

	// Test that endpoints are not registered when disabled
	req := httptest.NewRequest(http.MethodGet, debugInfoPath, http.NoBody)
	req.RemoteAddr = testIPAddress
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// Should return 404 since endpoints are not registered
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestGoroutineEndpoint(t *testing.T) {
	debugConfig := &config.DebugConfig{
		Enabled:     true,
		PathPrefix:  debugPath,
		AllowedIPs:  []string{localhostIPV4},
		BearerToken: "",
		Endpoints: config.DebugEndpointsConfig{
			Goroutines: true,
			Info:       true,
			GC:         true,
			Health:     true,
		},
	}

	testLogger := logger.New("info", false)
	app := &App{logger: testLogger}
	debugHandlers := NewDebugHandlers(app, debugConfig, testLogger)

	e := echo.New()
	debugHandlers.RegisterDebugEndpoints(e)

	// Test goroutines endpoint with JSON format
	req := httptest.NewRequest(http.MethodGet, "/_debug/goroutines", http.NoBody)
	req.RemoteAddr = testIPAddress
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "count")
	assert.Contains(t, rec.Body.String(), "timestamp")

	// Test goroutines endpoint with text format
	req = httptest.NewRequest(http.MethodGet, "/_debug/goroutines?format=text", http.NoBody)
	req.RemoteAddr = testIPAddress
	rec = httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/plain")
	assert.Contains(t, rec.Body.String(), "goroutine")
}
