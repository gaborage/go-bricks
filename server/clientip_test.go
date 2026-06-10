package server

import (
	"context"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustParseCIDRs(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	nets, invalid := ParseCIDRs(cidrs)
	require.Empty(t, invalid, "all test CIDRs must parse")
	return nets
}

// TestClientIPIgnoresSpoofedHeadersFromUntrustedPeer is the core security guarantee:
// X-Forwarded-For / X-Real-IP are honored ONLY when the immediate peer (RemoteAddr) is
// a configured trusted proxy. An attacker connecting directly cannot spoof their IP by
// setting XFF, so the value used for access-control (debug allowlist) / rate limiting
// cannot be forged.
func TestClientIPIgnoresSpoofedHeadersFromUntrustedPeer(t *testing.T) {
	cases := []struct {
		name           string
		remoteAddr     string
		xff            string
		xRealIP        string
		trustedProxies []string
		want           string
	}{
		{
			name:       "no_trusted_proxies_ignores_xff_spoof",
			remoteAddr: "203.0.113.9:54321",
			xff:        "127.0.0.1",
			want:       "203.0.113.9",
		},
		{
			name:           "untrusted_peer_ignores_xff_despite_configured_proxies",
			remoteAddr:     "203.0.113.9:54321",
			xff:            "127.0.0.1",
			trustedProxies: []string{"10.0.0.0/8"},
			want:           "203.0.113.9",
		},
		{
			name:           "trusted_peer_honors_xff_real_client",
			remoteAddr:     "10.0.0.5:443",
			xff:            "203.0.113.7",
			trustedProxies: []string{"10.0.0.0/8"},
			want:           "203.0.113.7",
		},
		{
			name:           "trusted_peer_walks_xff_right_to_left_to_first_untrusted",
			remoteAddr:     "10.0.0.5:443",
			xff:            "203.0.113.7, 10.1.2.3, 10.0.0.9",
			trustedProxies: []string{"10.0.0.0/8"},
			want:           "203.0.113.7",
		},
		{
			name:           "trusted_peer_honors_x_real_ip_when_no_xff",
			remoteAddr:     "10.0.0.5:443",
			xRealIP:        "203.0.113.7",
			trustedProxies: []string{"10.0.0.0/8"},
			want:           "203.0.113.7",
		},
		{
			// A present-but-blank X-Real-IP must NOT silently fall through to the trusted
			// proxy's own IP (fail-open); it yields an empty string so the caller's
			// net.ParseIP fails and the access-control decision fails closed.
			name:           "trusted_peer_blank_x_real_ip_fails_closed",
			remoteAddr:     "10.0.0.5:443",
			xRealIP:        "   ",
			trustedProxies: []string{"10.0.0.0/8"},
			want:           "",
		},
		{
			name:       "no_headers_returns_peer",
			remoteAddr: "192.168.1.50:1234",
			want:       "192.168.1.50",
		},
		{
			name:       "no_headers_no_port_returns_peer",
			remoteAddr: "192.168.1.50",
			want:       "192.168.1.50",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
			require.NoError(t, err)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set(HeaderXForwardedFor, tc.xff)
			}
			if tc.xRealIP != "" {
				req.Header.Set(HeaderXRealIP, tc.xRealIP)
			}

			var trusted []*net.IPNet
			if len(tc.trustedProxies) > 0 {
				trusted = mustParseCIDRs(t, tc.trustedProxies...)
			}

			assert.Equal(t, tc.want, ClientIP(req, trusted))
		})
	}
}

// TestParseCIDRsSeparatesValidFromInvalid verifies the parse helper returns parsed nets
// and surfaces invalid entries (so callers can WARN rather than silently degrade).
func TestParseCIDRsSeparatesValidFromInvalid(t *testing.T) {
	nets, invalid := ParseCIDRs([]string{"10.0.0.0/8", " 192.168.0.0/16 ", "not-a-cidr", ""})
	assert.Len(t, nets, 2, "two valid CIDRs (whitespace trimmed)")
	assert.Equal(t, []string{"not-a-cidr", ""}, invalid)

	nets, invalid = ParseCIDRs(nil)
	assert.Empty(t, nets)
	assert.Empty(t, invalid)
}
