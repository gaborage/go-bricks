package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/labstack/echo/v5"
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
	corsMiddleware := corsEcho(false)

	// Create a simple handler
	handler := corsMiddleware(func(c *echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	// Test preflight request
	req := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/", http.NoBody)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set(HeaderAccessControlRequestMethod, "POST")
	req.Header.Set(HeaderAccessControlRequestHeaders, "Content-Type")

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	// Verify CORS headers allow origin in development (echoed back via UnsafeAllowOriginFunc)
	assert.Equal(t, "http://localhost:3000", rec.Header().Get(HeaderAccessControlAllowOrigin))
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
	corsMiddleware := corsEcho(false)

	// Create a simple handler
	handler := corsMiddleware(func(c *echo.Context) error {
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
			req := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/", http.NoBody)
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

	// Set production environment without CORS_ORIGINS — must fail closed.
	os.Setenv("APP_ENV", "production")
	os.Unsetenv("CORS_ORIGINS")

	// Setup Echo
	e := echo.New()
	corsMiddleware := corsEcho(false)

	// Create a simple handler
	handler := corsMiddleware(func(c *echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	// Test with any origin — must NOT echo it back (no AllowOrigins set, no
	// UnsafeAllowOriginFunc registered). Browsers reject cross-origin
	// requests when this header is absent.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/", http.NoBody)
	req.Header.Set("Origin", "https://somesite.com")
	req.Header.Set(HeaderAccessControlRequestMethod, "GET")

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	assert.Empty(t, rec.Header().Get(HeaderAccessControlAllowOrigin),
		"non-dev env without CORS_ORIGINS must NOT emit Access-Control-Allow-Origin")
}

// TestCORSNeutralEnvWithoutCustomOriginsFailsClosed verifies that custom
// environment names (anything not in the development or production alias
// sets per ADR-022) also fail closed when CORS_ORIGINS is unset. The
// previous default of "any env without CORS_ORIGINS gets wildcard" shipped
// credential-leaking CORS to staging and regional production envs.
func TestCORSNeutralEnvWithoutCustomOriginsFailsClosed(t *testing.T) {
	originalAppEnv := os.Getenv("APP_ENV")
	originalCorsOrigins := os.Getenv("CORS_ORIGINS")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
		os.Setenv("CORS_ORIGINS", originalCorsOrigins)
	}()

	envs := []string{"staging", "stg", "production-eu", "tst", ""}
	for _, env := range envs {
		t.Run("env_"+env, func(t *testing.T) {
			os.Setenv("APP_ENV", env)
			os.Unsetenv("CORS_ORIGINS")

			e := echo.New()
			handler := corsEcho(false)(func(c *echo.Context) error {
				return c.String(http.StatusOK, "test")
			})

			req := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/", http.NoBody)
			req.Header.Set("Origin", "https://intruder.example.com")
			req.Header.Set(HeaderAccessControlRequestMethod, "GET")
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			require.NoError(t, handler(c))
			assert.Empty(t, rec.Header().Get(HeaderAccessControlAllowOrigin),
				"env=%q without CORS_ORIGINS must fail closed", env)
		})
	}
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
	corsMiddleware := corsEcho(false)

	// Create a simple handler
	handler := corsMiddleware(func(c *echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	// Test preflight with various headers
	req := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/", http.NoBody)
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

	// Setup Echo — exposeResponseTime=true mirrors server.responsetime.enabled.
	e := echo.New()
	corsMiddleware := corsEcho(true)

	// Create a simple handler that sets response headers
	handler := corsMiddleware(func(c *echo.Context) error {
		c.Response().Header().Set("X-Request-ID", "test-123")
		c.Response().Header().Set(HeaderXResponseTime, "50ms")
		return c.String(http.StatusOK, "test")
	})

	// Test actual request (not preflight)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
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

// TestCORSExposedHeadersResponseTimeDisabled verifies that when the Timing
// middleware is opt-out (exposeResponseTime=false, the default), CORS does not
// advertise X-Response-Time in Access-Control-Expose-Headers — keeping the CORS
// contract aligned with what the server actually emits. X-Request-ID stays.
func TestCORSExposedHeadersResponseTimeDisabled(t *testing.T) {
	originalAppEnv := os.Getenv("APP_ENV")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
	}()

	os.Setenv("APP_ENV", "development")

	e := echo.New()
	corsMiddleware := corsEcho(false)

	handler := corsMiddleware(func(c *echo.Context) error {
		c.Response().Header().Set("X-Request-ID", "test-123")
		return c.String(http.StatusOK, "test")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	req.Header.Set("Origin", "http://localhost:3000")

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, handler(c))

	exposedHeaders := rec.Header().Get(HeaderAccessControlExposeHeaders)
	assert.Contains(t, exposedHeaders, echo.HeaderXRequestID)
	assert.NotContains(t, exposedHeaders, HeaderXResponseTime,
		"X-Response-Time must not be advertised when the Timing middleware is disabled")
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
	corsMiddleware := corsEcho(false)

	// Create a handler that returns JSON
	handler := corsMiddleware(func(c *echo.Context) error {
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
			req := httptest.NewRequestWithContext(context.Background(), method, "/", http.NoBody)
			req.Header.Set("Origin", "http://localhost:3000")
			req.Header.Set("Content-Type", "application/json")

			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := handler(c)
			require.NoError(t, err)

			// Verify CORS headers are set for actual requests (origin echoed back)
			assert.Equal(t, "http://localhost:3000", rec.Header().Get(HeaderAccessControlAllowOrigin))
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
	corsMiddleware := corsEcho(false)

	handler := corsMiddleware(func(c *echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	// Test preflight request
	req := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/", http.NoBody)
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
	corsMiddleware := corsEcho(false)

	handler := corsMiddleware(func(c *echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	// Test with credentials
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
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

	// Production env with explicitly-empty CORS_ORIGINS — treated the same
	// as unset: fail closed. Empty-string and unset are observationally
	// identical (both make os.Getenv return ""), and operators who set
	// CORS_ORIGINS="" deserve the same loud signal as those who forgot.
	os.Setenv("APP_ENV", "production")
	os.Setenv("CORS_ORIGINS", "")

	// Setup Echo
	e := echo.New()
	corsMiddleware := corsEcho(false)

	handler := corsMiddleware(func(c *echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/", http.NoBody)
	req.Header.Set("Origin", "https://test.com")
	req.Header.Set(HeaderAccessControlRequestMethod, "GET")

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	require.NoError(t, err)

	assert.Empty(t, rec.Header().Get(HeaderAccessControlAllowOrigin),
		"empty CORS_ORIGINS in non-dev env must fail closed, not echo origin")
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
	corsMiddleware := corsEcho(false)

	handler := corsMiddleware(func(c *echo.Context) error {
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
			req := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/", http.NoBody)
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
	e.Use(corsEcho(false))

	// Add a route
	e.GET("/api/test", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Test the actual integration
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/test", http.NoBody)
	req.Header.Set("Origin", "http://localhost:3000")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Verify response
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "ok")

	// Verify CORS headers (origin echoed back via UnsafeAllowOriginFunc)
	assert.Equal(t, "http://localhost:3000", rec.Header().Get(HeaderAccessControlAllowOrigin))
	assert.Equal(t, "true", rec.Header().Get(HeaderAccessControlAllowCredentials))
}

// TestCORSProductionAliasesTriggerStrictMode verifies that production env aliases
// (prd, prod) drive the same strict-origin behavior as the canonical "production"
// value. Dev aliases (local) use wildcard echo. Neutral envs (tst) without
// CORS_ORIGINS fail closed — the observable effect (empty Allow-Origin for an
// unlisted origin) is the same as strict mode, encoded here as expectStrict=true.
func TestCORSProductionAliasesTriggerStrictMode(t *testing.T) {
	originalAppEnv := os.Getenv("APP_ENV")
	originalCorsOrigins := os.Getenv("CORS_ORIGINS")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
		os.Setenv("CORS_ORIGINS", originalCorsOrigins)
	}()

	tests := []struct {
		name           string
		env            string
		expectStrict   bool
		allowedOrigins string
	}{
		{name: "prd_alias_triggers_strict", env: "prd", expectStrict: true, allowedOrigins: "https://app.example.com"},
		{name: "prod_alias_triggers_strict", env: "prod", expectStrict: true, allowedOrigins: "https://app.example.com"},
		{name: "canonical_production_triggers_strict", env: "production", expectStrict: true, allowedOrigins: "https://app.example.com"},
		{name: "local_dev_alias_uses_wildcard_echo", env: "local", expectStrict: false, allowedOrigins: ""},
		{name: "tst_neutral_fails_closed", env: "tst", expectStrict: true, allowedOrigins: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			os.Setenv("APP_ENV", tc.env)
			if tc.allowedOrigins != "" {
				os.Setenv("CORS_ORIGINS", tc.allowedOrigins)
			} else {
				os.Unsetenv("CORS_ORIGINS")
			}

			e := echo.New()
			corsMiddleware := corsEcho(false)
			handler := corsMiddleware(func(c *echo.Context) error {
				return c.String(http.StatusOK, "test")
			})

			req := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/", http.NoBody)
			// Use an origin that's NOT in the allowed list. In strict mode the
			// Echo CORS middleware refuses to echo it back; in wildcard mode the
			// UnsafeAllowOriginFunc echoes any origin.
			req.Header.Set("Origin", "https://intruder.example.com")
			req.Header.Set(HeaderAccessControlRequestMethod, "POST")
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			require.NoError(t, handler(c))

			gotOrigin := rec.Header().Get(HeaderAccessControlAllowOrigin)
			if tc.expectStrict {
				assert.Empty(t, gotOrigin,
					"strict mode must not set Access-Control-Allow-Origin for an unlisted origin")
			} else {
				assert.Equal(t, "https://intruder.example.com", gotOrigin,
					"wildcard mode must echo any origin")
			}
		})
	}
}

// TestCORSEnvOverrideHonorsConfigValue verifies that the variadic
// envOverride parameter (passed by SetupMiddlewares from cfg.App.Env)
// takes precedence over APP_ENV from the OS env. This ensures the
// common `go run` workflow — which relies on Koanf's EnvDevelopment
// default — gets the dev wildcard rather than fail-closed CORS.
func TestCORSEnvOverrideHonorsConfigValue(t *testing.T) {
	originalAppEnv := os.Getenv("APP_ENV")
	originalCorsOrigins := os.Getenv("CORS_ORIGINS")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
		os.Setenv("CORS_ORIGINS", originalCorsOrigins)
	}()

	// Deliberately unset APP_ENV — caller passes "development" via the
	// variadic override (simulates SetupMiddlewares passing cfg.App.Env
	// when the operator relies on the Koanf default).
	os.Unsetenv("APP_ENV")
	os.Unsetenv("CORS_ORIGINS")

	e := echo.New()
	handler := corsEcho(false, "development")(func(c *echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/", http.NoBody)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set(HeaderAccessControlRequestMethod, "POST")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, handler(c))
	assert.Equal(t, "http://localhost:3000", rec.Header().Get(HeaderAccessControlAllowOrigin),
		"explicit dev env override must take precedence over the empty APP_ENV from the OS env")
}

// TestCORSStrictBranchRejectsWildcardEntry verifies the strict branch
// drops "*" rather than passing it to Echo's CORSWithConfig (which would
// panic at startup when combined with AllowCredentials=true).
func TestCORSStrictBranchRejectsWildcardEntry(t *testing.T) {
	originalAppEnv := os.Getenv("APP_ENV")
	originalCorsOrigins := os.Getenv("CORS_ORIGINS")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
		os.Setenv("CORS_ORIGINS", originalCorsOrigins)
	}()

	os.Setenv("APP_ENV", "production")
	os.Setenv("CORS_ORIGINS", "*,https://myapp.com")

	// Must not panic at construction time.
	e := echo.New()
	handler := corsEcho(false)(func(c *echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	// Verify the explicit non-wildcard origin still works.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/", http.NoBody)
	req.Header.Set("Origin", "https://myapp.com")
	req.Header.Set(HeaderAccessControlRequestMethod, "GET")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, handler(c))
	assert.Equal(t, "https://myapp.com", rec.Header().Get(HeaderAccessControlAllowOrigin),
		"explicit non-wildcard origin must still be allowed after '*' is dropped")
}

// TestCORSStrictBranchTolerantOfTrailingComma verifies operator copy-paste
// errors (trailing comma) don't panic Echo's CORSWithConfig validator,
// which rejects empty origin strings.
func TestCORSStrictBranchTolerantOfTrailingComma(t *testing.T) {
	originalAppEnv := os.Getenv("APP_ENV")
	originalCorsOrigins := os.Getenv("CORS_ORIGINS")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
		os.Setenv("CORS_ORIGINS", originalCorsOrigins)
	}()

	os.Setenv("APP_ENV", "production")
	os.Setenv("CORS_ORIGINS", "https://myapp.com,")

	// Must not panic at construction time.
	e := echo.New()
	handler := corsEcho(false)(func(c *echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/", http.NoBody)
	req.Header.Set("Origin", "https://myapp.com")
	req.Header.Set(HeaderAccessControlRequestMethod, "GET")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, handler(c))
	assert.Equal(t, "https://myapp.com", rec.Header().Get(HeaderAccessControlAllowOrigin))
}

// TestCORSStrictBranchAllWildcardFailsClosed verifies CORS_ORIGINS="*"
// (which after dropping '*' becomes empty) falls into the fail-closed
// branch instead of panicking Echo.
func TestCORSStrictBranchAllWildcardFailsClosed(t *testing.T) {
	originalAppEnv := os.Getenv("APP_ENV")
	originalCorsOrigins := os.Getenv("CORS_ORIGINS")
	defer func() {
		os.Setenv("APP_ENV", originalAppEnv)
		os.Setenv("CORS_ORIGINS", originalCorsOrigins)
	}()

	os.Setenv("APP_ENV", "production")
	os.Setenv("CORS_ORIGINS", "*")

	// Must not panic.
	e := echo.New()
	handler := corsEcho(false)(func(c *echo.Context) error {
		return c.String(http.StatusOK, "test")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/", http.NoBody)
	req.Header.Set("Origin", "https://anyone.example.com")
	req.Header.Set(HeaderAccessControlRequestMethod, "GET")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, handler(c))
	assert.Empty(t, rec.Header().Get(HeaderAccessControlAllowOrigin),
		"CORS_ORIGINS=* in non-dev env must fail closed, not echo the origin")
	assert.NotEqual(t, "true", rec.Header().Get(HeaderAccessControlAllowCredentials),
		"fail-closed mode must explicitly drop AllowCredentials so the response cannot carry session cookies cross-origin")
}
