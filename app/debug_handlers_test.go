package app

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
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

// TestAuthMiddlewareRejectsWhenNoTokenConfigured pins the defense-in-depth guard shut.
// Without it, an authMiddleware built against a blank BearerToken would AUTHENTICATE the
// matching blank header — strings.Cut yields the same blank token and ConstantTimeCompare
// returns 1. Both blank spellings must trip it: "" and a whitespace-only value, which
// bearerTokenConfigured collapses to the same "no credential" verdict. RegisterDebugEndpoints
// never wires the middleware in either state, so this is unreachable through the framework;
// the trap survives any future re-wiring that registers it unconditionally.
func TestAuthMiddlewareRejectsWhenNoTokenConfigured(t *testing.T) {
	// The guard returns before the Authorization header is ever read, so the header cases
	// exist to document the shapes that would otherwise slip through — not to multiply
	// coverage. The last case is the pairing that actually matters: a whitespace-only
	// configured token against the blank header that would match it byte for byte.
	tests := []struct {
		name            string
		configuredToken string
		authHeader      string
	}{
		{name: "bearer_with_trailing_space", authHeader: "Bearer "},
		{name: "bearer_with_no_token", authHeader: "Bearer"},
		{name: "no_header_at_all", authHeader: ""},
		{name: "bearer_with_arbitrary_token", authHeader: "Bearer anything"},
		{name: "whitespace_only_token_against_its_matching_header", configuredToken: "  ", authHeader: "Bearer  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &App{logger: logger.New("info", false)}
			debugConfig := &config.DebugConfig{Enabled: true, PathPrefix: debugPath, BearerToken: tt.configuredToken}
			authMiddleware := NewDebugHandlers(app, debugConfig, app.logger).authMiddleware(nil)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
			req.RemoteAddr = testIPAddress
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			nextCalled, err := invokeDebugMiddleware(authMiddleware, req)

			assert.False(t, nextCalled, "next must never run when no bearer token is configured")
			assertAPIErrorStatus(t, err, http.StatusUnauthorized)
		})
	}
}

// loggedEvent returns the first recorded log event whose message contains substr.
func loggedEvent(rec *recLogger, substr string) (recEvent, bool) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.events {
		if strings.Contains(e.msg, substr) {
			return e, true
		}
	}
	return recEvent{}, false
}

// loggedMsgContains reports whether rec recorded any log line whose message
// contains substr.
func loggedMsgContains(rec *recLogger, substr string) bool {
	_, ok := loggedEvent(rec, substr)
	return ok
}

// loggedCount reports how many log lines rec recorded whose message contains
// substr — the "exactly once" form of loggedEvent, for call sites that must tell
// one emission of a line apart from two.
func loggedCount(rec *recLogger, substr string) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	n := 0
	for _, e := range rec.events {
		if strings.Contains(e.msg, substr) {
			n++
		}
	}
	return n
}

// debugProbe is one request issued against a registered debug group: the peer address and
// Authorization header it carries, and the HTTP status the access-control chain must answer.
type debugProbe struct {
	name       string
	remoteAddr string
	authHeader string
	wantStatus int
}

// TestRegisterDebugEndpointsAccessControl covers the four access-control config states.
// Registration is refused outright when debug endpoints would be exposed with neither an IP
// allowlist nor a bearer token (ADR-049) — the state that used to register the group behind a
// pass-through middleware and a startup WARN. The three configured states still register, and
// the both-set case asserts the two controls compose (neither bypasses the other).
func TestRegisterDebugEndpointsAccessControl(t *testing.T) {
	const (
		token       = "s3cret-token"
		allowedPeer = testIPAddress
		otherPeer   = "203.0.113.9:54321"
	)

	tests := []struct {
		name        string
		disabled    bool
		allowedIPs  []string
		bearerToken string
		endpoints   config.DebugEndpointsConfig
		wantErr     bool
		wantExposed string
		// wantRegisteredLog is the exact rendered success line. Empty means the line must be
		// absent. Both values are formatted into the message by Msgf rather than recorded as
		// structured fields, so the assertion pins the rendered text — see the block below.
		wantRegisteredLog string
		probes            []debugProbe
	}{
		{
			name:              "allowlist_set_no_token",
			allowedIPs:        []string{localhostIPV4},
			endpoints:         config.DebugEndpointsConfig{Info: true},
			wantRegisteredLog: "Debug endpoints registered (allowed_ips=1, auth_enabled=false)",
			probes: []debugProbe{
				{name: "allowlisted_peer_needs_no_token", remoteAddr: allowedPeer, wantStatus: http.StatusOK},
				{name: "other_peer_denied", remoteAddr: otherPeer, wantStatus: http.StatusForbidden},
			},
		},
		{
			name:              "token_set_no_allowlist",
			bearerToken:       token,
			endpoints:         config.DebugEndpointsConfig{Info: true},
			wantRegisteredLog: "Debug endpoints registered (allowed_ips=0, auth_enabled=true)",
			probes: []debugProbe{
				// An empty allowlist must neither pass everyone through nor deny everyone:
				// the bearer token alone decides.
				{name: "correct_token_from_any_peer", remoteAddr: otherPeer, authHeader: "Bearer " + token, wantStatus: http.StatusOK},
				{name: "wrong_token_denied", remoteAddr: otherPeer, authHeader: "Bearer wrong", wantStatus: http.StatusUnauthorized},
				{name: "absent_token_denied", remoteAddr: allowedPeer, wantStatus: http.StatusUnauthorized},
			},
		},
		{
			name:              "both_set",
			allowedIPs:        []string{localhostIPV4},
			bearerToken:       token,
			endpoints:         config.DebugEndpointsConfig{Info: true},
			wantRegisteredLog: "Debug endpoints registered (allowed_ips=1, auth_enabled=true)",
			probes: []debugProbe{
				{name: "allowlisted_peer_with_token", remoteAddr: allowedPeer, authHeader: "Bearer " + token, wantStatus: http.StatusOK},
				// Each control still bites with the other satisfied — they compose.
				{name: "allowlisted_peer_without_token_denied", remoteAddr: allowedPeer, wantStatus: http.StatusUnauthorized},
				{name: "other_peer_with_token_denied", remoteAddr: otherPeer, authHeader: "Bearer " + token, wantStatus: http.StatusForbidden},
			},
		},
		{
			name:        "neither_set",
			endpoints:   config.DebugEndpointsConfig{Info: true},
			wantErr:     true,
			wantExposed: "build info",
		},
		{
			// SECURITY: a whitespace-only token is not a credential — `Authorization: Bearer  `
			// splits into scheme "Bearer" and token " ", which ConstantTimeCompare matches. It
			// must not satisfy the gate, so this is refused exactly like an unset token.
			name:        "whitespace_only_token_does_not_satisfy_the_gate",
			bearerToken: "   ",
			endpoints:   config.DebugEndpointsConfig{Info: true},
			wantErr:     true,
			wantExposed: "build info",
		},
		{
			// With the allowlist already satisfying the gate, a whitespace-only token must
			// still not wire authMiddleware — doing so would make `Bearer  ` a valid
			// credential. The allowlisted peer probe carries no token and must pass.
			name:              "whitespace_only_token_wires_no_auth",
			allowedIPs:        []string{localhostIPV4},
			bearerToken:       "  ",
			endpoints:         config.DebugEndpointsConfig{Info: true},
			wantRegisteredLog: "Debug endpoints registered (allowed_ips=1, auth_enabled=false)",
			probes: []debugProbe{
				{name: "allowlisted_peer_needs_no_token", remoteAddr: allowedPeer, wantStatus: http.StatusOK},
			},
		},
		{
			name:        "neither_set_lists_every_enabled_endpoint",
			endpoints:   config.DebugEndpointsConfig{Goroutines: true, GC: true, Health: true, Info: true},
			wantErr:     true,
			wantExposed: "goroutine dumps, GC endpoints, enhanced health, build info",
		},
		{
			// Nothing is enabled, so nothing is exposed and there is nothing to protect.
			name:              "neither_set_no_endpoints_enabled",
			endpoints:         config.DebugEndpointsConfig{},
			wantRegisteredLog: "Debug endpoints registered (allowed_ips=0, auth_enabled=false)",
			probes: []debugProbe{
				{name: "info_not_registered", remoteAddr: otherPeer, wantStatus: http.StatusNotFound},
			},
		},
		{
			// The default posture: debug off. The refusal must not fire for it.
			name:      "disabled_debug_is_unaffected",
			disabled:  true,
			endpoints: config.DebugEndpointsConfig{Goroutines: true, GC: true, Health: true, Info: true},
			probes: []debugProbe{
				{name: "info_not_registered", remoteAddr: otherPeer, wantStatus: http.StatusNotFound},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			debugConfig := &config.DebugConfig{
				Enabled:     !tt.disabled,
				PathPrefix:  debugPath,
				AllowedIPs:  tt.allowedIPs,
				BearerToken: tt.bearerToken,
				Endpoints:   tt.endpoints,
			}

			rec := &recLogger{}
			debugHandlers := NewDebugHandlers(&App{logger: rec}, debugConfig, rec)
			root := newRecordingRegistrar()
			err := debugHandlers.RegisterDebugEndpoints(root)

			if tt.wantErr {
				require.Error(t, err)
				// The refusal names what would have been exposed and both keys that fix it.
				// Anchored on both sides of the %s so an over-stated list (naming an endpoint
				// that is off) fails too — a bare Contains would accept any superset.
				assert.Contains(t, err.Error(), "would expose "+tt.wantExposed+" at ")
				assert.Contains(t, err.Error(), "debug.allowedips")
				assert.Contains(t, err.Error(), "debug.bearertoken")
				assert.Empty(t, root.children, "no route group may be registered when the refusal fires")
				return
			}

			require.NoError(t, err)
			if tt.disabled {
				assert.True(t, loggedMsgContains(rec, "Debug endpoints disabled"))
			}

			// The success line is the operator-facing statement of this group's security
			// posture — it is what an auditor greps to confirm a deployment is protected —
			// so assert its VALUES, not merely that it was emitted. An inverted
			// auth_enabled would report the opposite of the truth. allowed_ips and
			// auth_enabled are rendered into the message by Msgf and are not recorded as
			// separate structured fields (recEvent captures only Str fields), so the
			// rendered text is the only surface available; prefix IS a Str field and is
			// asserted as one.
			registered, logged := loggedEvent(rec, "Debug endpoints registered")
			if tt.wantRegisteredLog == "" {
				assert.False(t, logged, "no registration line may be logged when nothing is registered")
			} else {
				require.True(t, logged, "the registration line must be logged")
				assert.Equal(t, tt.wantRegisteredLog, registered.msg)
				assert.Equal(t, debugPath, registered.str["prefix"])
			}

			for _, p := range tt.probes {
				t.Run(p.name, func(t *testing.T) {
					status, _ := root.serveWith(http.MethodGet, debugInfoPath, p.remoteAddr, p.authHeader)
					assert.Equal(t, p.wantStatus, status)
				})
			}
		})
	}
}

// TestDebugJSONWireShapeIsStable pins the debug endpoints' wire contract by key name
// only. Decoding into anonymous maps is deliberate: referencing the response types
// would make this test follow a Go-side rename instead of catching one.
func TestDebugJSONWireShapeIsStable(t *testing.T) {
	app := &App{logger: logger.New("info", false)}
	handlers := NewDebugHandlers(app, &config.DebugConfig{Enabled: true, PathPrefix: "/_debug"}, app.logger)

	t.Run("gc_response", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/gc", http.NoBody)
		rec := httptest.NewRecorder()
		require.NoError(t, handlers.handleGC(server.NewHandlerContextForTest(rec, req, nil)))

		var envelope map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		assert.ElementsMatch(t, []string{"timestamp", "duration", "data"}, slices.Collect(maps.Keys(envelope)),
			"the debug envelope's key set is the wire contract (error is omitempty)")

		var data map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(envelope["data"], &data))
		assert.ElementsMatch(t,
			[]string{"stats", "mem_before", "mem_after", "forced", "heap_objects", "heap_size"},
			slices.Collect(maps.Keys(data)))
	})

	t.Run("goroutines_response", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/goroutines", http.NoBody)
		rec := httptest.NewRecorder()
		require.NoError(t, handlers.handleGoroutines(server.NewHandlerContextForTest(rec, req, nil)))

		var envelope struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		assert.ElementsMatch(t, []string{"count", "by_state", "by_function"}, slices.Collect(maps.Keys(envelope.Data)),
			"stacks and potential_leaks are omitempty and absent without ?stacks / ?leaks")
	})
}
