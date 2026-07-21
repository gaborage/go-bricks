package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/server"
)

// invokeDebugMiddleware runs a flat debug MiddlewareFunc against req, reporting whether the
// downstream next() ran (allowed) and the error returned (nil when allowed). Mirrors the
// scheduler's invokeCIDRMiddleware helper for the converted echo-free middleware shape.
func invokeDebugMiddleware(mw server.MiddlewareFunc, req *http.Request) (nextCalled bool, err error) {
	rec := httptest.NewRecorder()
	ctx := server.NewHandlerContextForTest(rec, req, nil)
	err = mw(ctx, func() error {
		nextCalled = true
		return nil
	})
	return nextCalled, err
}

// assertAPIErrorStatus asserts err is a go-bricks IAPIError carrying wantStatus.
func assertAPIErrorStatus(t *testing.T, err error, wantStatus int) {
	t.Helper()
	require.Error(t, err)
	var apiErr server.IAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, wantStatus, apiErr.HTTPStatus())
}

// TestIPWhitelistMiddlewareTrustedProxy verifies that the debug-endpoint IP allowlist
// cannot be bypassed by spoofing X-Forwarded-For. The allowlist is evaluated against the
// trusted-proxy-aware client IP (server.ClientIP): X-Forwarded-For / X-Real-IP are honored
// only when the immediate peer is a configured trusted proxy, mirroring the scheduler's
// CIDR middleware. Regression test for the High audit finding that a direct attacker could
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
			trustedNets, _ := server.ParseCIDRs(tc.trustedProxies)
			mw := debugHandlers.ipWhitelistMiddleware(trustedNets)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_debug/info", http.NoBody)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}

			nextCalled, err := invokeDebugMiddleware(mw, req)

			if tc.wantStatus == http.StatusOK {
				assert.NoError(t, err)
				assert.True(t, nextCalled, "next should run when the client IP is allowed")
			} else {
				assert.False(t, nextCalled, "next must not run when access is denied")
				assertAPIErrorStatus(t, err, tc.wantStatus)
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

	// The auth middleware is flat: trustedNets only affects the denial log's client IP.
	authMiddleware := debugHandlers.authMiddleware(nil)

	tests := []struct {
		name               string
		authHeader         string
		expectedStatusCode int
	}{
		{
			name:               "valid bearer token",
			authHeader:         "Bearer test-secret-token",
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "invalid bearer token",
			authHeader:         "Bearer wrong-token",
			expectedStatusCode: http.StatusUnauthorized,
		},
		{
			name:               "missing bearer prefix",
			authHeader:         "test-secret-token",
			expectedStatusCode: http.StatusUnauthorized,
		},
		{
			name:               "empty authorization header",
			authHeader:         "",
			expectedStatusCode: http.StatusUnauthorized,
		},
		{
			name:               "bearer with no token",
			authHeader:         "Bearer ",
			expectedStatusCode: http.StatusUnauthorized,
		},
		{
			// RFC 7235: the auth scheme is case-insensitive, so a lowercase "bearer"
			// with a valid token is accepted (matched via strings.EqualFold).
			name:               "case_insensitive_bearer_scheme_accepted",
			authHeader:         "bearer test-secret-token",
			expectedStatusCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			// Set a test remote IP for the denial log.
			req.RemoteAddr = "127.0.0.1:12345"

			nextCalled, err := invokeDebugMiddleware(authMiddleware, req)

			if tt.expectedStatusCode == http.StatusOK {
				assert.NoError(t, err)
				assert.True(t, nextCalled, "next should run on valid token")
			} else {
				assert.False(t, nextCalled, "next must not run when auth fails")
				assertAPIErrorStatus(t, err, tt.expectedStatusCode)
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

	authMiddleware := debugHandlers.authMiddleware(nil)

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
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
			req.Header.Set("Authorization", "Bearer "+tt.token)
			req.RemoteAddr = "127.0.0.1:12345"

			nextCalled, err := invokeDebugMiddleware(authMiddleware, req)

			if tt.expected {
				assert.NoError(t, err)
				assert.True(t, nextCalled)
			} else {
				assert.False(t, nextCalled)
				assertAPIErrorStatus(t, err, http.StatusUnauthorized)
			}
		})
	}
}

// loggedMsgContains reports whether rec recorded any log line whose message
// contains substr.
func loggedMsgContains(rec *recLogger, substr string) bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.events {
		if strings.Contains(e.msg, substr) {
			return true
		}
	}
	return false
}

// loggedStr returns the value of the given Str field on the first recorded log
// event whose message contains substr, or "" if none.
func loggedStr(rec *recLogger, substr, field string) string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.events {
		if strings.Contains(e.msg, substr) {
			return e.str[field]
		}
	}
	return ""
}

// TestRegisterDebugEndpointsAccessControlWarn verifies the no-access-control WARN fires only
// when at least one debug endpoint is registered AND neither an IP allowlist nor a bearer token
// is configured, and that the warning's "exposed" field lists exactly the enabled endpoints so
// it cannot over-state the exposure.
func TestRegisterDebugEndpointsAccessControlWarn(t *testing.T) {
	tests := []struct {
		name        string
		allowedIPs  []string
		bearerToken string
		endpoints   config.DebugEndpointsConfig
		wantWarn    bool
		wantExposed string
	}{
		{name: "no_access_control_warns", allowedIPs: nil, bearerToken: "", endpoints: config.DebugEndpointsConfig{Info: true}, wantWarn: true, wantExposed: "build info"},
		{name: "lists_only_enabled_endpoints", allowedIPs: nil, bearerToken: "", endpoints: config.DebugEndpointsConfig{Goroutines: true, Info: true}, wantWarn: true, wantExposed: "goroutine dumps, build info"},
		{name: "allowlist_set_no_warn", allowedIPs: []string{"127.0.0.1"}, bearerToken: "", endpoints: config.DebugEndpointsConfig{Info: true}, wantWarn: false},
		{name: "token_set_no_warn", allowedIPs: nil, bearerToken: "x", endpoints: config.DebugEndpointsConfig{Info: true}, wantWarn: false},
		{name: "no_endpoints_no_warn", allowedIPs: nil, bearerToken: "", endpoints: config.DebugEndpointsConfig{}, wantWarn: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			debugConfig := &config.DebugConfig{
				Enabled:     true,
				PathPrefix:  "/_debug",
				AllowedIPs:  tt.allowedIPs,
				BearerToken: tt.bearerToken,
				Endpoints:   tt.endpoints,
			}

			rec := &recLogger{}
			debugHandlers := NewDebugHandlers(&App{logger: rec}, debugConfig, rec)
			debugHandlers.RegisterDebugEndpoints(newRecordingRegistrar())

			assert.Equal(t, tt.wantWarn, loggedMsgContains(rec, "NO access control"))
			if tt.wantWarn {
				assert.Equal(t, tt.wantExposed, loggedStr(rec, "NO access control", "exposed"))
			}
		})
	}
}
