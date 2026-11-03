package server

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

// unsetEnvForTest truly unsets key for the test, restoring it on cleanup.
// t.Setenv(key, "") alone would leave it set-but-empty.
func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "") // registers restore-on-cleanup
	require.NoError(t, os.Unsetenv(key))
}

// serveCORS sends one request to "/" through mw wrapping next, on a context
// built by newEngine, and returns the recorder. headers are key/value pairs
// set on the request in order.
func serveCORS(t *testing.T, mw echo.MiddlewareFunc, next echo.HandlerFunc, method string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	require.Zero(t, len(headers)%2, "headers must be key/value pairs")
	req := httptest.NewRequestWithContext(context.Background(), method, "/", http.NoBody)
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	require.NoError(t, mw(next)(newEngine().NewContext(req, rec)))
	return rec
}

// preflightCORS sends an OPTIONS preflight from origin asking for method
// through corsEcho(false) and returns the recorder.
func preflightCORS(t *testing.T, origin, method string) *httptest.ResponseRecorder {
	t.Helper()
	return serveCORS(t, corsEcho(false), okEchoHandler, http.MethodOptions,
		"Origin", origin, HeaderAccessControlRequestMethod, method)
}

// enableDevWildcard sets a development APP_ENV with the CORS_DEV_WILDCARD
// opt-in (ADR-038) for the test, and unsets CORS_ORIGINS so an inherited
// origin list cannot take the test off the wildcard branch.
func enableDevWildcard(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("CORS_DEV_WILDCARD", "true")
	unsetEnvForTest(t, "CORS_ORIGINS")
}

// captureStdLog redirects the standard logger into a buffer for the test,
// restoring the previous output on cleanup.
func captureStdLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	return &buf
}

func TestCORSDevelopmentEnvironment(t *testing.T) {
	enableDevWildcard(t)

	// Test preflight request
	rec := serveCORS(t, corsEcho(false), okEchoHandler, http.MethodOptions,
		"Origin", "http://localhost:3000",
		HeaderAccessControlRequestMethod, "POST",
		HeaderAccessControlRequestHeaders, "Content-Type")

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
	// Set production environment with custom origins
	t.Setenv("APP_ENV", "production")
	t.Setenv("CORS_ORIGINS", "https://myapp.com,https://admin.myapp.com,https://api.myapp.com")

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
			rec := preflightCORS(t, tt.origin, "POST")

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
	// Set production environment without CORS_ORIGINS — must fail closed.
	t.Setenv("APP_ENV", "production")
	unsetEnvForTest(t, "CORS_ORIGINS")

	// Test with any origin — must NOT echo it back (no AllowOrigins set, no
	// UnsafeAllowOriginFunc registered). Browsers reject cross-origin
	// requests when this header is absent.
	rec := preflightCORS(t, "https://somesite.com", "GET")

	assert.Empty(t, rec.Header().Get(HeaderAccessControlAllowOrigin),
		"non-dev env without CORS_ORIGINS must NOT emit Access-Control-Allow-Origin")
}

// TestCORSNeutralEnvWithoutCustomOriginsFailsClosed verifies that custom
// environment names (anything not in the development or production alias
// sets per ADR-022) also fail closed when CORS_ORIGINS is unset. The
// previous default of "any env without CORS_ORIGINS gets wildcard" shipped
// credential-leaking CORS to staging and regional production envs.
func TestCORSNeutralEnvWithoutCustomOriginsFailsClosed(t *testing.T) {
	envs := []string{"staging", "stg", "production-eu", "tst", ""}
	for _, env := range envs {
		t.Run("env_"+env, func(t *testing.T) {
			t.Setenv("APP_ENV", env)
			unsetEnvForTest(t, "CORS_ORIGINS")

			rec := preflightCORS(t, "https://intruder.example.com", "GET")
			assert.Empty(t, rec.Header().Get(HeaderAccessControlAllowOrigin),
				"env=%q without CORS_ORIGINS must fail closed", env)
		})
	}
}

func TestCORSAllowedHeaders(t *testing.T) {
	enableDevWildcard(t)

	// Test preflight with various headers
	rec := serveCORS(t, corsEcho(false), okEchoHandler, http.MethodOptions,
		"Origin", "http://localhost:3000",
		HeaderAccessControlRequestMethod, "POST",
		HeaderAccessControlRequestHeaders, "Content-Type, Authorization, X-Request-ID")

	// Verify allowed headers
	allowedHeaders := rec.Header().Get(HeaderAccessControlAllowHeaders)
	assert.Contains(t, allowedHeaders, "Origin")
	assert.Contains(t, allowedHeaders, "Content-Type")
	assert.Contains(t, allowedHeaders, "Accept")
	assert.Contains(t, allowedHeaders, "Authorization")
	assert.Contains(t, allowedHeaders, echo.HeaderXRequestID)
}

func TestCORSExposedHeaders(t *testing.T) {
	enableDevWildcard(t)

	// exposeResponseTime=true mirrors server.responsetime.enabled. The handler
	// sets response headers; the request is an actual request (not preflight).
	rec := serveCORS(t, corsEcho(true), func(c *echo.Context) error {
		c.Response().Header().Set("X-Request-ID", "test-123")
		c.Response().Header().Set(HeaderXResponseTime, "50ms")
		return c.String(http.StatusOK, "test")
	}, http.MethodGet, "Origin", "http://localhost:3000")

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
	enableDevWildcard(t)

	rec := serveCORS(t, corsEcho(false), func(c *echo.Context) error {
		c.Response().Header().Set("X-Request-ID", "test-123")
		return c.String(http.StatusOK, "test")
	}, http.MethodGet, "Origin", "http://localhost:3000")

	exposedHeaders := rec.Header().Get(HeaderAccessControlExposeHeaders)
	assert.Contains(t, exposedHeaders, echo.HeaderXRequestID)
	assert.NotContains(t, exposedHeaders, HeaderXResponseTime,
		"X-Response-Time must not be advertised when the Timing middleware is disabled")
}

func TestCORSActualRequestHandling(t *testing.T) {
	enableDevWildcard(t)

	// Create a handler that returns JSON
	corsMiddleware := corsEcho(false)
	handler := func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"message": "success"})
	}

	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	}

	for _, method := range methods {
		t.Run("method_"+method, func(t *testing.T) {
			rec := serveCORS(t, corsMiddleware, handler, method,
				"Origin", "http://localhost:3000", "Content-Type", "application/json")

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
	enableDevWildcard(t)

	rec := preflightCORS(t, "http://localhost:3000", "POST")

	// Verify Max-Age is set to 86400 seconds (24 hours)
	assert.Equal(t, "86400", rec.Header().Get(HeaderAccessControlMaxAge))
}

func TestCORSCredentialsEnabled(t *testing.T) {
	enableDevWildcard(t)

	// Test with credentials
	rec := serveCORS(t, corsEcho(false), okEchoHandler, http.MethodGet,
		"Origin", "http://localhost:3000", "Cookie", "session=abc123")

	// Verify credentials are allowed
	assert.Equal(t, "true", rec.Header().Get(HeaderAccessControlAllowCredentials))
}

func TestCORSEmptyOriginsList(t *testing.T) {
	// Production env with explicitly-empty CORS_ORIGINS — treated the same
	// as unset: fail closed. Empty-string and unset are observationally
	// identical (both make os.Getenv return ""), and operators who set
	// CORS_ORIGINS="" deserve the same loud signal as those who forgot.
	t.Setenv("APP_ENV", "production")
	t.Setenv("CORS_ORIGINS", "")

	rec := preflightCORS(t, "https://test.com", "GET")

	assert.Empty(t, rec.Header().Get(HeaderAccessControlAllowOrigin),
		"empty CORS_ORIGINS in non-dev env must fail closed, not echo origin")
}

func TestCORSSingleOrigin(t *testing.T) {
	// Set production with single origin
	t.Setenv("APP_ENV", "production")
	t.Setenv("CORS_ORIGINS", "https://myapp.com")

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
			rec := preflightCORS(t, tt.origin, "GET")

			if tt.expectedOrigin != "" {
				assert.Equal(t, tt.expectedOrigin, rec.Header().Get(HeaderAccessControlAllowOrigin))
			} else {
				assert.Empty(t, rec.Header().Get(HeaderAccessControlAllowOrigin))
			}
		})
	}
}

func TestCORSMiddlewareIntegration(t *testing.T) {
	enableDevWildcard(t)

	// Setup Echo with CORS middleware
	e := newEngine()
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
	// Opt in once for the whole table: proves the dev case still gets the
	// wildcard AND that the flag does not weaken the non-dev cases below
	// (containment property, ADR-038).
	t.Setenv("CORS_DEV_WILDCARD", "true")

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
			t.Setenv("APP_ENV", tc.env)
			if tc.allowedOrigins != "" {
				t.Setenv("CORS_ORIGINS", tc.allowedOrigins)
			} else {
				unsetEnvForTest(t, "CORS_ORIGINS")
			}

			// Use an origin that's NOT in the allowed list. In strict mode the
			// Echo CORS middleware refuses to echo it back; in wildcard mode the
			// UnsafeAllowOriginFunc echoes any origin.
			rec := preflightCORS(t, "https://intruder.example.com", "POST")

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
// default plus CORS_DEV_WILDCARD=true (ADR-038) — gets the dev wildcard
// rather than fail-closed CORS.
func TestCORSEnvOverrideHonorsConfigValue(t *testing.T) {
	// Deliberately unset APP_ENV — caller passes "development" via the
	// variadic override (simulates SetupMiddlewares passing cfg.App.Env
	// when the operator relies on the Koanf default).
	unsetEnvForTest(t, "APP_ENV")
	unsetEnvForTest(t, "CORS_ORIGINS")
	t.Setenv("CORS_DEV_WILDCARD", "true")

	rec := serveCORS(t, corsEcho(false, "development"), okEchoHandler, http.MethodOptions,
		"Origin", "http://localhost:3000", HeaderAccessControlRequestMethod, "POST")

	assert.Equal(t, "http://localhost:3000", rec.Header().Get(HeaderAccessControlAllowOrigin),
		"explicit dev env override must take precedence over the empty APP_ENV from the OS env")
}

// TestCORSStrictBranchOrigins verifies the strict branch's allowlist parsing:
// "*" is dropped rather than passed to Echo's CORSWithConfig (which would
// panic at startup when combined with AllowCredentials=true), and an operator
// copy-paste error (trailing comma) doesn't panic Echo's CORSWithConfig
// validator, which rejects empty origin strings.
func TestCORSStrictBranchOrigins(t *testing.T) {
	tests := []struct {
		name    string
		origins string
	}{
		{name: "rejects_wildcard_entry", origins: "*,https://myapp.com"},
		{name: "tolerant_of_trailing_comma", origins: "https://myapp.com,"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "production")
			t.Setenv("CORS_ORIGINS", tc.origins)

			// Must not panic at construction time.
			rec := preflightCORS(t, "https://myapp.com", "GET")
			assert.Equal(t, "https://myapp.com", rec.Header().Get(HeaderAccessControlAllowOrigin),
				"explicit origin must survive allowlist sanitizing")
		})
	}
}

// TestCORSStrictBranchAllWildcardFailsClosed verifies CORS_ORIGINS="*"
// (which after dropping '*' becomes empty) falls into the fail-closed
// branch instead of panicking Echo, and that the warn names the invalid
// allowlist as the cause — not the environment.
func TestCORSStrictBranchAllWildcardFailsClosed(t *testing.T) {
	buf := captureStdLog(t)

	t.Setenv("APP_ENV", "production")
	t.Setenv("CORS_ORIGINS", "*")

	// Must not panic.
	rec := preflightCORS(t, "https://anyone.example.com", "GET")

	assert.Empty(t, rec.Header().Get(HeaderAccessControlAllowOrigin),
		"CORS_ORIGINS=* in non-dev env must fail closed, not echo the origin")
	assert.NotEqual(t, "true", rec.Header().Get(HeaderAccessControlAllowCredentials),
		"fail-closed mode must explicitly drop AllowCredentials so the response cannot carry session cookies cross-origin")
	assert.Contains(t, buf.String(), "yielded no valid explicit origins")
	assert.NotContains(t, buf.String(), "is not a development alias",
		"the empty-allowlist warn must name the invalid allowlist as the cause, not the environment")
}

// TestCORSDevPermissiveEmitsWarn verifies the dev-permissive branch (reflect
// any origin + AllowCredentials=true) now logs a WARN instead of staying
// silent, and names the explicitly-set APP_ENV value.
func TestCORSDevPermissiveEmitsWarn(t *testing.T) {
	buf := captureStdLog(t)

	enableDevWildcard(t)
	t.Setenv("CORS_ORIGINS", "")

	_ = CORS(true, "development")

	assert.Contains(t, buf.String(), "reflects ANY origin")
	assert.Contains(t, buf.String(), `APP_ENV="development"`)
}

// TestCORSUnsetEnvWarnsAboutDefaulting verifies the WARN calls out that
// APP_ENV was never set in the process environment (as opposed to being
// explicitly set to a dev alias) — this is the "operator forgot APP_ENV"
// scenario the koanf default silently papers over.
func TestCORSUnsetEnvWarnsAboutDefaulting(t *testing.T) {
	buf := captureStdLog(t)

	unsetEnvForTest(t, "APP_ENV")
	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("CORS_DEV_WILDCARD", "true")

	// The override mimics the production path, where koanf has already
	// defaulted cfg.App.Env to "development" despite APP_ENV being unset.
	_ = CORS(true, "development")

	assert.Contains(t, buf.String(), "not set in the process environment")
}

// TestCORSDevPermissiveWarnNotesProcessOverride verifies that when the
// effective (envOverride) value diverges from the raw process APP_ENV, the
// WARN surfaces both rather than implying the override IS the process value.
func TestCORSDevPermissiveWarnNotesProcessOverride(t *testing.T) {
	buf := captureStdLog(t)

	t.Setenv("APP_ENV", "local")
	t.Setenv("CORS_ORIGINS", "")
	t.Setenv("CORS_DEV_WILDCARD", "true")

	// envOverride ("dev") differs from the raw process APP_ENV ("local").
	_ = CORS(true, "dev")

	assert.Contains(t, buf.String(), `APP_ENV="dev"`)
	assert.Contains(t, buf.String(), `process APP_ENV="local"`)
}

// TestCORSStrictAllowlistNoDevWarn is a regression guard: when CORS_ORIGINS
// is set, the strict-allowlist branch wins regardless of env, so the
// dev-permissive WARN must not fire.
func TestCORSStrictAllowlistNoDevWarn(t *testing.T) {
	buf := captureStdLog(t)

	t.Setenv("CORS_ORIGINS", "https://a.example")

	_ = CORS(true, "development")

	assert.NotContains(t, buf.String(), "reflects ANY origin")
}

// TestCORSDevWildcardOptInMatrix drives the CORS_DEV_WILDCARD opt-in matrix
// (ADR-038): a development-alias env fails closed without the flag, gets the
// reflect-any-origin + credentials posture only with an explicit truthy flag
// (the plan-009 permissive WARN still fires — opting in doesn't buy silence),
// treats an unparseable value as false with a WARN, and ignores the flag
// entirely outside development aliases (the containment property — the flag
// must never grant the wildcard posture for a non-dev env).
func TestCORSDevWildcardOptInMatrix(t *testing.T) {
	tests := []struct {
		name         string
		appEnv       string
		flag         string
		expectEcho   bool
		warnContains string
	}{
		{name: "dev_without_opt_in_fails_closed", appEnv: "development", flag: "", expectEcho: false, warnContains: "CORS_DEV_WILDCARD is not enabled"},
		{name: "dev_opt_in_enables_wildcard", appEnv: "development", flag: "true", expectEcho: true, warnContains: "reflects ANY origin"},
		{name: "dev_opt_in_invalid_value_fails_closed", appEnv: "development", flag: "ture", expectEcho: false, warnContains: "is not a valid boolean"},
		{name: "flag_ignored_outside_dev", appEnv: "production", flag: "true", expectEcho: false, warnContains: "the flag is ignored outside development"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureStdLog(t)

			t.Setenv("APP_ENV", tc.appEnv)
			t.Setenv("CORS_ORIGINS", "")
			// devWildcardOptIn treats empty and unset identically, so "" is
			// the unset case here.
			t.Setenv("CORS_DEV_WILDCARD", tc.flag)

			rec := serveCORS(t, corsEcho(false, tc.appEnv), okEchoHandler, http.MethodOptions,
				"Origin", "https://intruder.example.com", HeaderAccessControlRequestMethod, "GET")
			if tc.expectEcho {
				assert.Equal(t, "https://intruder.example.com", rec.Header().Get(HeaderAccessControlAllowOrigin))
				assert.Equal(t, "true", rec.Header().Get(HeaderAccessControlAllowCredentials))
			} else {
				assert.Empty(t, rec.Header().Get(HeaderAccessControlAllowOrigin),
					"must fail closed: no Access-Control-Allow-Origin")
				assert.NotEqual(t, "true", rec.Header().Get(HeaderAccessControlAllowCredentials))
			}
			assert.Contains(t, buf.String(), tc.warnContains)
		})
	}
}

// capturingLogger records messages sent through the Warn() chain so tests can
// assert CORS startup warnings are rerouted through the framework logger.
// Embeds noopLogger (timeout_test.go) so only Warn() needs overriding.
type capturingLogger struct {
	noopLogger
	warns []string
}

func (c *capturingLogger) Warn() logger.LogEvent { return &capturingLogEvent{sink: c} }

type capturingLogEvent struct {
	noopLogEvent
	sink   *capturingLogger
	fields []string
}

// Str and Int record structured fields as "key=value" so a Msg assertion can
// see them beside the message text (mirrors the stdlib fallback's rendering).
func (e *capturingLogEvent) Str(key, val string) logger.LogEvent {
	e.fields = append(e.fields, key+"="+val)
	return e
}

func (e *capturingLogEvent) Int(key string, val int) logger.LogEvent {
	e.fields = append(e.fields, fmt.Sprintf("%s=%d", key, val))
	return e
}

func (e *capturingLogEvent) Msg(msg string) {
	e.sink.warns = append(e.sink.warns, strings.Join(append([]string{msg}, e.fields...), " "))
}

func (e *capturingLogEvent) Msgf(format string, args ...any) {
	e.sink.warns = append(e.sink.warns, fmt.Sprintf(format, args...))
}

// TestCORSWarnsRouteThroughProvidedLogger verifies corsEchoWithLogger reroutes
// startup warnings through the provided framework logger (structured WARN
// level, SensitiveDataFilter, dual-mode routing) instead of the stdlib
// fallback — rerouted, not duplicated.
func TestCORSWarnsRouteThroughProvidedLogger(t *testing.T) {
	tests := []struct {
		name         string
		flag         string
		warnContains string
	}{
		{name: "without_opt_in_routes_fail_closed_warn", flag: "", warnContains: "CORS_DEV_WILDCARD is not enabled"},
		{name: "with_opt_in_routes_permissive_warn", flag: "true", warnContains: "reflects ANY origin"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureStdLog(t)

			t.Setenv("APP_ENV", "development")
			t.Setenv("CORS_ORIGINS", "")
			t.Setenv("CORS_DEV_WILDCARD", tc.flag)

			capturer := &capturingLogger{}
			_ = corsEchoWithLogger(false, capturer, "development")

			captured := strings.Join(capturer.warns, "\n")
			assert.Contains(t, captured, "[server.cors] ",
				"grep-continuity prefix must survive on the framework-logger path")
			assert.Contains(t, captured, tc.warnContains)
			assert.NotContains(t, buf.String(), tc.warnContains,
				"warn must be rerouted through the provided logger, not duplicated to stdlib")
		})
	}
}
