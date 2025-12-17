package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCORSDevelopmentEnvironment(t *testing.T) {
	// Save original env vars
	originalAppEnv := os.Getenv("APP_ENV")
	originalCorsOrigins := os.Getenv("CORS_ORIGINS")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
		os.Setenv("CORS_ORIGINS", originalCorsOrigins)
	}()

	// Set development environment
	os.Setenv("APP_ENV", "development")
	os.Unsetenv("CORS_ORIGINS")

	// Setup Echo
	e := echo.New()
	corsMiddleware := CORS()

	// Create a simple handler
	handler := corsMiddleware(func(c echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	// Test preflight request
	req := httptest.NewRequest(http.MethodOptions, "/", http.NoBody)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set(HeaderAccessControlRequestMethod, "POST")
	req.Header.Set(HeaderAccessControlRequestHeaders, "Content-Type")

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	// Verify CORS headers allow all origins in development
	assert.Equal(t, "*", rec.Header().Get(HeaderAccessControlAllowOrigin))
	assert.Equal(t, "true", rec.Header().Get(HeaderAccessControlAllowCredentials))
	assert.Equal(t, "86400", rec.Header().Get(HeaderAccessControlMaxAge))

	// Verify allowed methods
	allowedMethods := rec.Header().Get(HeaderAccessControlAllowMethods)
	assert.Contains(t, allowedMethods, "GET")
	assert.Contains(t, allowedMethods, "POST")
	assert.Contains(t, allowedMethods, "PUT")
	assert.Contains(t, allowedMethods, "PATCH")
	assert.Contains(t, allowedMethods, "DELETE")
	assert.Contains(t, allowedMethods, "OPTIONS")
}

func TestCORSProductionEnvironmentWithCustomOrigins(t *testing.T) {
	// Save original env vars
	originalAppEnv := os.Getenv("APP_ENV")
	originalCorsOrigins := os.Getenv("CORS_ORIGINS")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
		os.Setenv("CORS_ORIGINS", originalCorsOrigins)
	}()

	// Set production environment with custom origins
	os.Setenv("APP_ENV", "production")
	os.Setenv("CORS_ORIGINS", "https://myapp.com,https://admin.myapp.com,https://api.myapp.com")

	// Setup Echo
	e := echo.New()
	corsMiddleware := CORS()

	// Create a simple handler
	handler := corsMiddleware(func(c echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	tests := []struct {
		name           string
		origin         string
		expectedOrigin string
		expectHeaders  bool
	}{
		{
			name:           "allowed_origin_exact_match",
			origin:         "https://myapp.com",
			expectedOrigin: "https://myapp.com",
			expectHeaders:  true,
		},
		{
			name:           "allowed_origin_admin",
			origin:         "https://admin.myapp.com",
			expectedOrigin: "https://admin.myapp.com",
			expectHeaders:  true,
		},
		{
			name:           "allowed_origin_api",
			origin:         "https://api.myapp.com",
			expectedOrigin: "https://api.myapp.com",
			expectHeaders:  true,
		},
		{
			name:           "disallowed_origin",
			origin:         "https://malicious.com",
			expectedOrigin: "",
			expectHeaders:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "/", http.NoBody)
			req.Header.Set("Origin", tt.origin)
			req.Header.Set(HeaderAccessControlRequestMethod, "POST")

			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := handler(c)
			require.NoError(t, err)

			if tt.expectHeaders {
				assert.Equal(t, tt.expectedOrigin, rec.Header().Get(HeaderAccessControlAllowOrigin))
				assert.Equal(t, "true", rec.Header().Get(HeaderAccessControlAllowCredentials))
			} else {
				// For disallowed origins, the middleware should not set CORS headers
				assert.Empty(t, rec.Header().Get(HeaderAccessControlAllowOrigin))
			}
		})
	}
}

func TestCORSProductionEnvironmentWithoutCustomOrigins(t *testing.T) {
	// Save original env vars
	originalAppEnv := os.Getenv("APP_ENV")
	originalCorsOrigins := os.Getenv("CORS_ORIGINS")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
		os.Setenv("CORS_ORIGINS", originalCorsOrigins)
	}()

	// Set production environment without CORS_ORIGINS
	os.Setenv("APP_ENV", "production")
	os.Unsetenv("CORS_ORIGINS")

	// Setup Echo
	e := echo.New()
	corsMiddleware := CORS()

	// Create a simple handler
	handler := corsMiddleware(func(c echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	// Test with any origin - should allow all since no specific origins are set
	req := httptest.NewRequest(http.MethodOptions, "/", http.NoBody)
	req.Header.Set("Origin", "https://somesite.com")
	req.Header.Set(HeaderAccessControlRequestMethod, "GET")

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	// Should still allow * when no CORS_ORIGINS is set in production
	assert.Equal(t, "*", rec.Header().Get(HeaderAccessControlAllowOrigin))
}

func TestCORSAllowedHeaders(t *testing.T) {
	// Save original env vars
	originalAppEnv := os.Getenv("APP_ENV")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
	}()

	os.Setenv("APP_ENV", "development")

	// Setup Echo
	e := echo.New()
	corsMiddleware := CORS()

	// Create a simple handler
	handler := corsMiddleware(func(c echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	// Test preflight with various headers
	req := httptest.NewRequest(http.MethodOptions, "/", http.NoBody)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set(HeaderAccessControlRequestMethod, "POST")
	req.Header.Set(HeaderAccessControlRequestHeaders, "Content-Type, Authorization, X-Request-ID")

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	// Verify allowed headers
	allowedHeaders := rec.Header().Get(HeaderAccessControlAllowHeaders)
	assert.Contains(t, allowedHeaders, "Origin")
	assert.Contains(t, allowedHeaders, "Content-Type")
	assert.Contains(t, allowedHeaders, "Accept")
	assert.Contains(t, allowedHeaders, "Authorization")
	assert.Contains(t, allowedHeaders, echo.HeaderXRequestID)
}

func TestCORSExposedHeaders(t *testing.T) {
	// Save original env vars
	originalAppEnv := os.Getenv("APP_ENV")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
	}()

	os.Setenv("APP_ENV", "development")

	// Setup Echo
	e := echo.New()
	corsMiddleware := CORS()

	// Create a simple handler that sets response headers
	handler := corsMiddleware(func(c echo.Context) error {
		c.Response().Header().Set("X-Request-ID", "test-123")
		c.Response().Header().Set(HeaderXResponseTime, "50ms")
		return c.String(http.StatusOK, "test")
	})

	// Test actual request (not preflight)
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set("Origin", "http://localhost:3000")

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	// Verify exposed headers
	exposedHeaders := rec.Header().Get(HeaderAccessControlExposeHeaders)
	assert.Contains(t, exposedHeaders, echo.HeaderXRequestID)
	assert.Contains(t, exposedHeaders, HeaderXResponseTime)
}

func TestCORSActualRequestHandling(t *testing.T) {
	// Save original env vars
	originalAppEnv := os.Getenv("APP_ENV")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
	}()

	os.Setenv("APP_ENV", "development")

	// Setup Echo
	e := echo.New()
	corsMiddleware := CORS()

	// Create a handler that returns JSON
	handler := corsMiddleware(func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"message": "success"})
	})

	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	}

	for _, method := range methods {
		t.Run("method_"+method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/", http.NoBody)
			req.Header.Set("Origin", "http://localhost:3000")
			req.Header.Set("Content-Type", "application/json")

			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := handler(c)
			require.NoError(t, err)

			// Verify CORS headers are set for actual requests
			assert.Equal(t, "*", rec.Header().Get(HeaderAccessControlAllowOrigin))
			assert.Equal(t, "true", rec.Header().Get(HeaderAccessControlAllowCredentials))

			// Verify handler was executed
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), "success")
		})
	}
}

func TestCORSMaxAge(t *testing.T) {
	// Save original env vars
	originalAppEnv := os.Getenv("APP_ENV")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
	}()

	os.Setenv("APP_ENV", "development")

	// Setup Echo
	e := echo.New()
	corsMiddleware := CORS()

	handler := corsMiddleware(func(c echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	// Test preflight request
	req := httptest.NewRequest(http.MethodOptions, "/", http.NoBody)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set(HeaderAccessControlRequestMethod, "POST")

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	// Verify Max-Age is set to 86400 seconds (24 hours)
	assert.Equal(t, "86400", rec.Header().Get(HeaderAccessControlMaxAge))
}

func TestCORSCredentialsEnabled(t *testing.T) {
	// Save original env vars
	originalAppEnv := os.Getenv("APP_ENV")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
	}()

	os.Setenv("APP_ENV", "development")

	// Setup Echo
	e := echo.New()
	corsMiddleware := CORS()

	handler := corsMiddleware(func(c echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	// Test with credentials
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Cookie", "session=abc123")

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	// Verify credentials are allowed
	assert.Equal(t, "true", rec.Header().Get(HeaderAccessControlAllowCredentials))
}

func TestCORSEmptyOriginsList(t *testing.T) {
	// Save original env vars
	originalAppEnv := os.Getenv("APP_ENV")
	originalCorsOrigins := os.Getenv("CORS_ORIGINS")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
		os.Setenv("CORS_ORIGINS", originalCorsOrigins)
	}()

	// Set production with empty CORS_ORIGINS
	os.Setenv("APP_ENV", "production")
	os.Setenv("CORS_ORIGINS", "")

	// Setup Echo
	e := echo.New()
	corsMiddleware := CORS()

	handler := corsMiddleware(func(c echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	req := httptest.NewRequest(http.MethodOptions, "/", http.NoBody)
	req.Header.Set("Origin", "https://test.com")
	req.Header.Set(HeaderAccessControlRequestMethod, "GET")

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	// When CORS_ORIGINS is empty string, should still allow all
	assert.Equal(t, "*", rec.Header().Get(HeaderAccessControlAllowOrigin))
}

func TestCORSSingleOrigin(t *testing.T) {
	// Save original env vars
	originalAppEnv := os.Getenv("APP_ENV")
	originalCorsOrigins := os.Getenv("CORS_ORIGINS")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
		os.Setenv("CORS_ORIGINS", originalCorsOrigins)
	}()

	// Set production with single origin
	os.Setenv("APP_ENV", "production")
	os.Setenv("CORS_ORIGINS", "https://myapp.com")

	// Setup Echo
	e := echo.New()
	corsMiddleware := CORS()

	handler := corsMiddleware(func(c echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	tests := []struct {
		name           string
		origin         string
		expectedOrigin string
	}{
		{
			name:           "matching_origin",
			origin:         "https://myapp.com",
			expectedOrigin: "https://myapp.com",
		},
		{
			name:           "non_matching_origin",
			origin:         "https://evil.com",
			expectedOrigin: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "/", http.NoBody)
			req.Header.Set("Origin", tt.origin)
			req.Header.Set(HeaderAccessControlRequestMethod, "GET")

			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := handler(c)
			require.NoError(t, err)

			if tt.expectedOrigin != "" {
				assert.Equal(t, tt.expectedOrigin, rec.Header().Get(HeaderAccessControlAllowOrigin))
			} else {
				assert.Empty(t, rec.Header().Get(HeaderAccessControlAllowOrigin))
			}
		})
	}
}

func TestCORSMiddlewareIntegration(t *testing.T) {
	// Save original env vars
	originalAppEnv := os.Getenv("APP_ENV")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
	}()

	os.Setenv("APP_ENV", "development")

	// Setup Echo with CORS middleware
	e := echo.New()
	e.Use(CORS())

	// Add a route
	e.GET("/api/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Test the actual integration
	req := httptest.NewRequest(http.MethodGet, "/api/test", http.NoBody)
	req.Header.Set("Origin", "http://localhost:3000")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Verify response
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "ok")

	// Verify CORS headers
	assert.Equal(t, "*", rec.Header().Get(HeaderAccessControlAllowOrigin))
	assert.Equal(t, "true", rec.Header().Get(HeaderAccessControlAllowCredentials))
}
