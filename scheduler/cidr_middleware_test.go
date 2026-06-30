package scheduler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testLocalhostAddr  = "127.0.0.1:12345"
	testPrivateNetCIDR = "192.168.1.0/24"
	testPrivateAddr    = "10.0.0.1:12345"
	testAllowList      = "10.0.0.0/8"
	testIPAddress      = "192.168.1.100"

	msgAccessDeniedLocalhost = "Access denied: localhost-only"
	msgAccessDeniedAllowlist = "Access denied: IP not in allowlist"
)

// invokeCIDRMiddleware runs the flat middleware against req, reporting whether the
// downstream next() ran (allowed) and the error returned (nil when allowed).
func invokeCIDRMiddleware(mw server.MiddlewareFunc, req *http.Request) (nextCalled bool, err error) {
	rec := httptest.NewRecorder()
	ctx := server.NewHandlerContextForTest(rec, req, &config.Config{})
	err = mw(ctx, func() error {
		nextCalled = true
		return nil
	})
	return nextCalled, err
}

// assertForbidden asserts err is a go-bricks forbidden (403) IAPIError with wantMessage.
// assertCIDRDecision asserts the allow/deny outcome of a CIDR middleware invocation,
// shared across the gating tests to avoid repeating the same allow/deny block.
func assertCIDRDecision(t *testing.T, nextCalled bool, err error, expectAllowed bool, wantMessage string) {
	t.Helper()
	if expectAllowed {
		assert.True(t, nextCalled, "next should be called when allowed")
		assert.NoError(t, err)
	} else {
		assert.False(t, nextCalled, "next must not be called when denied")
		assertForbidden(t, err, wantMessage)
	}
}

func assertForbidden(t *testing.T, err error, wantMessage string) {
	t.Helper()
	require.Error(t, err)
	var apiErr server.IAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.HTTPStatus())
	assert.Equal(t, wantMessage, apiErr.Message())
}

// TestCIDRMiddlewareLocalhostOnly verifies localhost-only access when allowlist is empty
func TestCIDRMiddlewareLocalhostOnly(t *testing.T) {
	tests := []struct {
		name          string
		remoteAddr    string
		expectAllowed bool
		expectMessage string
	}{
		{
			name:          "allows_localhost_ipv4",
			remoteAddr:    testLocalhostAddr,
			expectAllowed: true,
		},
		{
			name:          "allows_localhost_ipv6",
			remoteAddr:    "[::1]:12345",
			expectAllowed: true,
		},
		{
			name:          "blocks_external_ip",
			remoteAddr:    "192.168.1.100:12345",
			expectAllowed: false,
			expectMessage: msgAccessDeniedLocalhost,
		},
		{
			name:          "blocks_public_ip",
			remoteAddr:    "8.8.8.8:12345",
			expectAllowed: false,
			expectMessage: msgAccessDeniedLocalhost,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Empty allowlist = localhost-only, no trusted proxies
			mw := CIDRMiddleware(nil, []string{}, []string{})

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
			req.RemoteAddr = tt.remoteAddr

			nextCalled, err := invokeCIDRMiddleware(mw, req)

			assertCIDRDecision(t, nextCalled, err, tt.expectAllowed, tt.expectMessage)
		})
	}
}

// TestCIDRMiddlewareAllowlistMode verifies CIDR allowlist filtering
func TestCIDRMiddlewareAllowlistMode(t *testing.T) {
	tests := []struct {
		name          string
		allowlist     []string
		remoteAddr    string
		expectAllowed bool
		expectMessage string
	}{
		{
			name:          "allows_ip_in_allowlist_range",
			allowlist:     []string{testPrivateNetCIDR},
			remoteAddr:    "192.168.1.100:12345",
			expectAllowed: true,
		},
		{
			name:          "blocks_ip_outside_allowlist_range",
			allowlist:     []string{testPrivateNetCIDR},
			remoteAddr:    "192.168.2.100:12345",
			expectAllowed: false,
			expectMessage: msgAccessDeniedAllowlist,
		},
		{
			name:          "allows_ip_in_multiple_ranges",
			allowlist:     []string{testPrivateNetCIDR, testAllowList},
			remoteAddr:    "10.1.2.3:12345",
			expectAllowed: true,
		},
		{
			name:          "allows_specific_ip_with_32",
			allowlist:     []string{"203.0.113.42/32"},
			remoteAddr:    "203.0.113.42:12345",
			expectAllowed: true,
		},
		{
			name:          "blocks_localhost_when_not_in_allowlist",
			allowlist:     []string{testPrivateNetCIDR},
			remoteAddr:    testLocalhostAddr,
			expectAllowed: false,
			expectMessage: msgAccessDeniedAllowlist,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// No trusted proxies in basic tests
			mw := CIDRMiddleware(nil, tt.allowlist, []string{})

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
			req.RemoteAddr = tt.remoteAddr

			nextCalled, err := invokeCIDRMiddleware(mw, req)

			assertCIDRDecision(t, nextCalled, err, tt.expectAllowed, tt.expectMessage)
		})
	}
}

// TestCIDRMiddlewareProxyHeaders verifies X-Forwarded-For and X-Real-IP handling
func TestCIDRMiddlewareProxyHeaders(t *testing.T) {
	tests := []struct {
		name           string
		allowlist      []string
		trustedProxies []string
		remoteAddr     string
		xForwardedFor  string
		xRealIP        string
		expectAllowed  bool
		expectMessage  string
		description    string
	}{
		{
			name:           "uses_xff_when_present_and_peer_is_trusted",
			allowlist:      []string{testPrivateNetCIDR},
			trustedProxies: []string{testAllowList}, // Trust the proxy network
			remoteAddr:     testPrivateAddr,
			xForwardedFor:  "192.168.1.100, 10.0.0.5",
			expectAllowed:  true,
			description:    "Should use first IP from X-Forwarded-For when peer is trusted",
		},
		{
			name:           "uses_xrealip_when_xff_absent_and_peer_is_trusted",
			allowlist:      []string{testPrivateNetCIDR},
			trustedProxies: []string{testAllowList},
			remoteAddr:     testPrivateAddr,
			xRealIP:        testIPAddress,
			expectAllowed:  true,
		},
		{
			name:           "uses_remoteaddr_when_no_proxy_headers",
			allowlist:      []string{testAllowList},
			trustedProxies: []string{},
			remoteAddr:     testPrivateAddr,
			expectAllowed:  true,
		},
		{
			name:           "blocks_when_xff_ip_not_in_allowlist",
			allowlist:      []string{testPrivateNetCIDR},
			trustedProxies: []string{testPrivateNetCIDR}, // Trust proxy network
			remoteAddr:     "192.168.1.50:12345",
			xForwardedFor:  "203.0.113.1",
			expectAllowed:  false,
			expectMessage:  msgAccessDeniedAllowlist,
			description:    "X-Forwarded-For takes precedence over RemoteAddr when peer is trusted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw := CIDRMiddleware(nil, tt.allowlist, tt.trustedProxies)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
			req.RemoteAddr = tt.remoteAddr
			if tt.xForwardedFor != "" {
				req.Header.Set(server.HeaderXForwardedFor, tt.xForwardedFor)
			}
			if tt.xRealIP != "" {
				req.Header.Set(server.HeaderXRealIP, tt.xRealIP)
			}

			nextCalled, err := invokeCIDRMiddleware(mw, req)

			if tt.expectAllowed {
				assert.True(t, nextCalled, tt.description)
				assert.NoError(t, err, tt.description)
			} else {
				assert.False(t, nextCalled, tt.description)
				assertForbidden(t, err, tt.expectMessage)
			}
		})
	}
}

// TestCIDRMiddlewareHeaderSpoofingPrevention verifies proxy headers are ignored without trusted proxies
func TestCIDRMiddlewareHeaderSpoofingPrevention(t *testing.T) {
	tests := []struct {
		name           string
		allowlist      []string
		trustedProxies []string
		remoteAddr     string
		xForwardedFor  string
		xRealIP        string
		expectAllowed  bool
		expectMessage  string
		description    string
	}{
		{
			name:           "blocks_spoofed_xff_when_no_trusted_proxies",
			allowlist:      []string{testPrivateNetCIDR},
			trustedProxies: []string{},          // No trusted proxies - headers should be ignored
			remoteAddr:     "203.0.113.1:12345", // Attacker's IP (not in allowlist)
			xForwardedFor:  testIPAddress,       // Spoofed header claiming to be in allowlist
			expectAllowed:  false,
			expectMessage:  msgAccessDeniedAllowlist,
			description:    "Without trusted proxies, X-Forwarded-For must be ignored to prevent spoofing",
		},
		{
			name:           "blocks_spoofed_xrealip_when_no_trusted_proxies",
			allowlist:      []string{testPrivateNetCIDR},
			trustedProxies: []string{},
			remoteAddr:     "203.0.113.1:12345", // Attacker's IP
			xRealIP:        testIPAddress,       // Spoofed header
			expectAllowed:  false,
			expectMessage:  msgAccessDeniedAllowlist,
			description:    "Without trusted proxies, X-Real-IP must be ignored",
		},
		{
			name:           "allows_legitimate_proxy_with_trusted_configuration",
			allowlist:      []string{testPrivateNetCIDR},
			trustedProxies: []string{testAllowList}, // Trust the proxy network
			remoteAddr:     testPrivateAddr,         // Request from trusted proxy
			xForwardedFor:  testIPAddress,           // Real client IP
			expectAllowed:  true,
			description:    "Trusted proxy can provide X-Forwarded-For",
		},
		{
			name:           "ignores_headers_from_untrusted_proxy",
			allowlist:      []string{testPrivateNetCIDR},
			trustedProxies: []string{"172.16.0.0/12"}, // Trust different network
			remoteAddr:     testPrivateAddr,           // Untrusted proxy (not in 172.16.0.0/12)
			xForwardedFor:  testIPAddress,             // Spoofed header
			expectAllowed:  false,
			expectMessage:  msgAccessDeniedAllowlist,
			description:    "Untrusted proxy's headers must be ignored",
		},
		{
			name:           "right_to_left_xff_resolution_prevents_injection",
			allowlist:      []string{"203.0.113.0/24"},
			trustedProxies: []string{testAllowList, testPrivateNetCIDR},
			remoteAddr:     "10.0.0.5:12345",                      // Trusted proxy
			xForwardedFor:  "203.0.113.42, 192.168.1.1, 10.0.0.5", // Proper chain
			expectAllowed:  true,
			description:    "Walk right-to-left: 10.0.0.5 (trusted), 192.168.1.1 (trusted), 203.0.113.42 (client)",
		},
		{
			name:           "detects_client_ip_when_intermediate_proxy_is_untrusted",
			allowlist:      []string{"198.51.100.0/24"},
			trustedProxies: []string{testAllowList},
			remoteAddr:     "10.0.0.5:12345",                       // Trusted proxy
			xForwardedFor:  "198.51.100.42, 192.168.1.1, 10.0.0.5", // 192.168.1.1 is untrusted
			expectAllowed:  false,
			expectMessage:  msgAccessDeniedAllowlist,
			description:    "Walk right-to-left: 10.0.0.5 (trusted), 192.168.1.1 (untrusted = client), blocks 192.168.1.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw := CIDRMiddleware(nil, tt.allowlist, tt.trustedProxies)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
			req.RemoteAddr = tt.remoteAddr
			if tt.xForwardedFor != "" {
				req.Header.Set(server.HeaderXForwardedFor, tt.xForwardedFor)
			}
			if tt.xRealIP != "" {
				req.Header.Set(server.HeaderXRealIP, tt.xRealIP)
			}

			nextCalled, err := invokeCIDRMiddleware(mw, req)

			if tt.expectAllowed {
				assert.True(t, nextCalled, tt.description)
				assert.NoError(t, err, tt.description)
			} else {
				assert.False(t, nextCalled, tt.description)
				assertForbidden(t, err, tt.expectMessage)
			}
		})
	}
}

// TestCIDRMiddlewareInvalidCIDR verifies fallback to localhost-only for invalid CIDRs
func TestCIDRMiddlewareInvalidCIDR(t *testing.T) {
	// Invalid CIDR should fall back to localhost-only
	mw := CIDRMiddleware(nil, []string{"not-a-valid-cidr", "also invalid"}, []string{})

	// Test localhost is allowed
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
	req.RemoteAddr = testLocalhostAddr

	nextCalled, err := invokeCIDRMiddleware(mw, req)
	assert.True(t, nextCalled, "localhost should be allowed in localhost-only fallback")
	assert.NoError(t, err)

	// Test external IP is blocked
	req2 := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
	req2.RemoteAddr = "192.168.1.1:12345"

	nextCalled2, err2 := invokeCIDRMiddleware(mw, req2)
	assert.False(t, nextCalled2, "external IP must be blocked in localhost-only fallback")
	assertForbidden(t, err2, msgAccessDeniedLocalhost)
}

// TestParseCIDRAllowlistReturnsInvalidEntries verifies the parser surfaces invalid
// CIDR strings so the middleware can log them — silent fallback is a visibility gap.
func TestParseCIDRAllowlistReturnsInvalidEntries(t *testing.T) {
	tests := []struct {
		name              string
		allowlist         []string
		wantInvalid       []string
		wantLocalhostOnly bool
		wantNetCount      int
	}{
		{
			name:              "empty_allowlist_is_localhost_only_no_invalid",
			allowlist:         []string{},
			wantInvalid:       nil,
			wantLocalhostOnly: true,
			wantNetCount:      0,
		},
		{
			name:              "all_valid_no_invalid_reported",
			allowlist:         []string{"10.0.0.0/8", "192.168.0.0/16"},
			wantInvalid:       nil,
			wantLocalhostOnly: false,
			wantNetCount:      2,
		},
		{
			name:              "mixed_valid_and_invalid_surfaces_only_invalid",
			allowlist:         []string{"10.0.0.0/8", "not-a-cidr", "192.168.1.0/240"},
			wantInvalid:       []string{"not-a-cidr", "192.168.1.0/240"},
			wantLocalhostOnly: false,
			wantNetCount:      1,
		},
		{
			name:              "all_invalid_falls_back_to_localhost_and_surfaces_all",
			allowlist:         []string{"bad1", "bad2"},
			wantInvalid:       []string{"bad1", "bad2"},
			wantLocalhostOnly: true,
			wantNetCount:      0,
		},
		{
			name:              "whitespace_around_valid_entry_is_trimmed",
			allowlist:         []string{"  10.0.0.0/8  "},
			wantInvalid:       nil,
			wantLocalhostOnly: false,
			wantNetCount:      1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nets, localhostOnly, invalid := parseCIDRAllowlist(tc.allowlist)
			assert.Equal(t, tc.wantInvalid, invalid, "invalid entries")
			assert.Equal(t, tc.wantLocalhostOnly, localhostOnly, "localhost-only flag")
			assert.Len(t, nets, tc.wantNetCount, "valid nets count")
		})
	}
}

// TestParseTrustedProxiesReturnsInvalidEntries mirrors the allowlist test for proxy CIDRs.
// Critical because silent drops of trusted-proxy CIDRs cause downstream IP-extraction to
// fall back to the immediate peer, masking real client IPs from behind a misconfigured proxy.
func TestParseTrustedProxiesReturnsInvalidEntries(t *testing.T) {
	tests := []struct {
		name         string
		proxies      []string
		wantInvalid  []string
		wantNetCount int
	}{
		{
			name:         "empty_list_no_invalid",
			proxies:      []string{},
			wantInvalid:  nil,
			wantNetCount: 0,
		},
		{
			name:         "all_valid",
			proxies:      []string{"10.0.0.0/8", "172.16.0.0/12"},
			wantInvalid:  nil,
			wantNetCount: 2,
		},
		{
			name:         "mixed_surfaces_only_invalid",
			proxies:      []string{"10.0.0.0/8", "bogus", "192.168.1.0/240"},
			wantInvalid:  []string{"bogus", "192.168.1.0/240"},
			wantNetCount: 1,
		},
		{
			// Parity with parseCIDRAllowlist's whitespace handling — YAML often
			// leaves leading/trailing spaces around list entries.
			name:         "whitespace_around_valid_entry_is_trimmed",
			proxies:      []string{"  10.0.0.0/8  "},
			wantInvalid:  nil,
			wantNetCount: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nets, invalid := server.ParseCIDRs(tc.proxies)
			assert.Equal(t, tc.wantInvalid, invalid, "invalid entries")
			assert.Len(t, nets, tc.wantNetCount, "valid nets count")
		})
	}
}

// TestCIDRMiddlewareLogsInvalidEntries exercises the WARN-logging branches
// of CIDRMiddleware that the other tests skip by passing a nil logger. The
// assertion is structural (middleware constructs and serves without panic)
// rather than message-content based, but this guarantees the logging code
// paths are covered so changes there can't regress silently.
func TestCIDRMiddlewareLogsInvalidEntries(t *testing.T) {
	tests := []struct {
		name           string
		allowlist      []string
		trustedProxies []string
		wantWarn       bool
	}{
		{
			name:           "invalid_allowlist_triggers_warn",
			allowlist:      []string{"not-a-cidr", "192.168.1.0/240"},
			trustedProxies: []string{},
			wantWarn:       true,
		},
		{
			name:           "invalid_trusted_proxy_triggers_warn",
			allowlist:      []string{"10.0.0.0/8"},
			trustedProxies: []string{"bogus"},
			wantWarn:       true,
		},
		{
			name:           "both_invalid_triggers_both_warns",
			allowlist:      []string{"bad-allowlist"},
			trustedProxies: []string{"bad-proxy"},
			wantWarn:       true,
		},
		{
			name:           "all_valid_emits_no_warn",
			allowlist:      []string{"10.0.0.0/8"},
			trustedProxies: []string{"192.168.0.0/16"},
			wantWarn:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// CIDRMiddleware emits the invalid-entry WARNs during construction. logger.New
			// writes to os.Stdout (the logger package exposes no buffer-injectable constructor),
			// so capture stdout and assert the WARN actually fires — otherwise this test would
			// pass even if both WARN branches in CIDRMiddleware were deleted.
			out := captureStdout(t, func() {
				_ = CIDRMiddleware(logger.New("warn", false), tc.allowlist, tc.trustedProxies)
			})
			if tc.wantWarn {
				assert.Contains(t, out, "invalid", "invalid CIDR entries must emit a WARN")
			} else {
				assert.NotContains(t, out, "invalid", "all-valid config must not emit an invalid-entry WARN")
			}
		})
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns everything written.
// These CIDR tests are not parallel, so swapping the process-global writer is safe.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	rp, wp, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = wp
	defer func() { os.Stdout = old }()

	fn()

	require.NoError(t, wp.Close())
	data, readErr := io.ReadAll(rp)
	require.NoError(t, readErr)
	return string(data)
}
