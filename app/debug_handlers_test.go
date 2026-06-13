package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// TestIPWhitelistMiddlewareTrustedProxy verifies that the debug-endpoint IP allowlist
// cannot be bypassed by spoofing X-Forwarded-For. The allowlist must be evaluated against
// the immediate peer IP unless the peer is a configured trusted proxy — mirroring the
// scheduler's CIDR middleware. Regression test for the High audit finding: the handler
// previously used echo's spoofable c.RealIP(), so an attacker connecting directly could
// send "X-Forwarded-For: 127.0.0.1" to satisfy a localhost-only allowlist.
func TestIPWhitelistMiddlewareTrustedProxy(t *testing.T) {
	app := &App{logger: logger.New("info", false)}

	cases := []struct {
		name           string
		allowedIPs     []string
		trustedProxies []string
		remoteAddr     string
		xff            string
		wantStatus     int
	}{
		{
			// THE finding: direct attacker spoofs XFF to impersonate localhost.
			name:       "xff_spoof_from_untrusted_peer_is_denied",
			allowedIPs: []string{"127.0.0.1", "::1"},
			remoteAddr: "203.0.113.9:54321",
			xff:        "127.0.0.1",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "direct_localhost_peer_is_allowed",
			allowedIPs: []string{"127.0.0.1", "::1"},
			remoteAddr: "127.0.0.1:12345",
			wantStatus: http.StatusOK,
		},
		{
			name:       "untrusted_public_peer_no_headers_is_denied",
			allowedIPs: []string{"127.0.0.1"},
			remoteAddr: "203.0.113.9:54321",
			wantStatus: http.StatusForbidden,
		},
		{
			// Behind a configured trusted proxy, the forwarded real client IS evaluated.
			name:           "trusted_proxy_forwards_allowlisted_client_is_allowed",
			allowedIPs:     []string{"203.0.113.7"},
			trustedProxies: []string{"10.0.0.0/8"},
			remoteAddr:     "10.0.0.5:443",
			xff:            "203.0.113.7",
			wantStatus:     http.StatusOK,
		},
		{
			name:           "trusted_proxy_forwards_disallowed_client_is_denied",
			allowedIPs:     []string{"127.0.0.1"},
			trustedProxies: []string{"10.0.0.0/8"},
			remoteAddr:     "10.0.0.5:443",
			xff:            "203.0.113.7",
			wantStatus:     http.StatusForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			debugConfig := &config.DebugConfig{
				Enabled:        true,
				PathPrefix:     "/_debug",
				AllowedIPs:     tc.allowedIPs,
				TrustedProxies: tc.trustedProxies,
			}
			debugHandlers := NewDebugHandlers(app, debugConfig, app.logger)
			wrapped := debugHandlers.ipWhitelistMiddleware()(func(c *echo.Context) error {
				return c.String(http.StatusOK, "success")
			})

			e := echo.New()
			// Replicate the production server config (server/server.go), where
			// LegacyIPExtractor makes c.RealIP() trust X-Forwarded-For unconditionally —
			// the exact setting that makes the allowlist spoofable before the fix.
			e.IPExtractor = echo.LegacyIPExtractor()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_debug/info", http.NoBody)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := wrapped(c)

			if tc.wantStatus == http.StatusOK {
				assert.NoError(t, err)
				assert.Equal(t, http.StatusOK, rec.Code)
			} else {
				assert.Error(t, err)
				var httpErr *echo.HTTPError
				if errors.As(err, &httpErr) {
					assert.Equal(t, tc.wantStatus, httpErr.Code)
				}
			}
		})
	}
}

func TestAuthMiddleware(t *testing.T) {
	// Create test app and handlers with bearer token
	app := &App{
		logger: logger.New("info", false),
	}
	debugConfig := &config.DebugConfig{
		Enabled:     true,
		PathPrefix:  "/_debug",
		BearerToken: "test-secret-token",
	}
	debugHandlers := NewDebugHandlers(app, debugConfig, app.logger)

	// Create a test handler that returns success if auth passes
	testHandler := func(c *echo.Context) error {
		return c.String(http.StatusOK, "success")
	}

	// Get the auth middleware
	authMiddleware := debugHandlers.authMiddleware()
	wrappedHandler := authMiddleware(testHandler)

	tests := []struct {
		name               string
		authHeader         string
		expectedStatusCode int
		expectedBody       string
	}{
		{
			name:               "valid bearer token",
			authHeader:         "Bearer test-secret-token",
			expectedStatusCode: http.StatusOK,
			expectedBody:       "success",
		},
		{
			name:               "invalid bearer token",
			authHeader:         "Bearer wrong-token",
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       "",
		},
		{
			name:               "missing bearer prefix",
			authHeader:         "test-secret-token",
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       "",
		},
		{
			name:               "empty authorization header",
			authHeader:         "",
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       "",
		},
		{
			name:               "bearer with no token",
			authHeader:         "Bearer ",
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       "",
		},
		{
			name:               "case sensitive bearer",
			authHeader:         "bearer test-secret-token",
			expectedStatusCode: http.StatusUnauthorized,
			expectedBody:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test request
			e := echo.New()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			// Set a test remote IP for logging
			req.RemoteAddr = "127.0.0.1:12345"

			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			// Execute the wrapped handler
			err := wrappedHandler(c)

			if tt.expectedStatusCode == http.StatusOK {
				// Should succeed
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedStatusCode, rec.Code)
				assert.Contains(t, rec.Body.String(), tt.expectedBody)
			} else {
				// Should return an HTTP error
				assert.Error(t, err)

				// Check if it's an echo.HTTPError
				var httpErr *echo.HTTPError
				if errors.As(err, &httpErr) {
					assert.Equal(t, tt.expectedStatusCode, httpErr.Code)
				}
			}
		})
	}
}

func TestAuthMiddlewareConstantTimeComparison(t *testing.T) {
	// This test ensures that the constant-time comparison is working
	// We can't easily test timing attacks, but we can verify the behavior

	app := &App{
		logger: logger.New("info", false),
	}
	debugConfig := &config.DebugConfig{
		Enabled:     true,
		PathPrefix:  "/_debug",
		BearerToken: "secret123",
	}
	debugHandlers := NewDebugHandlers(app, debugConfig, app.logger)

	testHandler := func(c *echo.Context) error {
		return c.String(http.StatusOK, "success")
	}

	authMiddleware := debugHandlers.authMiddleware()
	wrappedHandler := authMiddleware(testHandler)

	// Test tokens that are similar but not exact
	tokens := []struct {
		token    string
		expected bool
	}{
		{"secret123", true},   // exact match
		{"secret124", false},  // one char different
		{"secret12", false},   // shorter
		{"secret1234", false}, // longer
		{"SECRET123", false},  // different case
		{"", false},           // empty
	}

	for _, tt := range tokens {
		t.Run("token_"+tt.token, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
			req.Header.Set("Authorization", "Bearer "+tt.token)
			req.RemoteAddr = "127.0.0.1:12345"
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := wrappedHandler(c)

			if tt.expected {
				assert.NoError(t, err)
				assert.Equal(t, http.StatusOK, rec.Code)
			} else {
				assert.Error(t, err)
				var httpErr *echo.HTTPError
				if errors.As(err, &httpErr) {
					assert.Equal(t, http.StatusUnauthorized, httpErr.Code)
				}
			}
		})
	}
}
