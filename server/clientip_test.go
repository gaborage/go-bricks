package server

import (
	"context"
	"net"
	"net/http"
	"strings"
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
// X-Forwarded-For is honored ONLY when the immediate peer (RemoteAddr) is a configured
// trusted proxy, and X-Real-IP is not honored at all (ADR-057, completed by ADR-080). An
// attacker connecting directly cannot spoof their IP, so the value used by the two
// access-control consumers — the debug allowlist and the scheduler CIDR middleware —
// cannot be forged. Rate limiting does not use this function; it runs through echo's own
// extractor (#965).
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
			// ADR-057 decided X-Real-IP "is not honored at all... No fallback is added",
			// and server/server.go implements exactly that. ClientIP never got the memo.
			// The answer is the peer we OBSERVED, never a value the caller wrote — even
			// when the peer is trusted, because a trusted peer vouches for the chain it
			// appends to, not for a single-valued header anyone upstream can set.
			name:           "trusted_peer_ignores_x_real_ip_and_returns_peer",
			remoteAddr:     "10.0.0.5:443",
			xRealIP:        "203.0.113.7",
			trustedProxies: []string{"10.0.0.0/8"},
			want:           "10.0.0.5",
		},
		{
			// Every hop in the chain is inside the trusted set, so there is no identified
			// untrusted hop to report. The old code returned the LEFT-MOST entry, which is
			// a value the caller wrote; the rule is that the answer is either an identified
			// untrusted hop or the peer we observed, so this falls through to the peer.
			name:           "all_hops_trusted_returns_peer_not_leftmost_entry",
			remoteAddr:     "10.0.0.5:443",
			xff:            "10.1.2.3, 10.0.0.9",
			trustedProxies: []string{"10.0.0.0/8"},
			want:           "10.0.0.5",
		},
		{
			// Echo's posture: "Unable to parse IP; cannot trust entire records". An
			// unparseable hop means the chain cannot be read, so the walk STOPS rather
			// than continuing left into an attacker-authored entry. AWS ALB's
			// routing.http.xff_client_port.enabled appends "client_ip:port" on every
			// request, so a non-IP entry is a shipping configuration, not a hypothetical
			// (ADR-057's comparison table row "An XFF entry that fails to parse", and its
			// "non-IP XFF entry keys the entire fleet" consequence).
			name:           "unparseable_hop_stops_the_walk_and_returns_peer",
			remoteAddr:     "10.0.0.5:443",
			xff:            "203.0.113.7, not-an-ip, 10.0.0.9",
			trustedProxies: []string{"10.0.0.0/8"},
			want:           "10.0.0.5",
		},
		{
			// Defect (c), an AVAILABILITY bug rather than an access-control one: some
			// proxies bracket IPv6 XFF entries, net.ParseIP("[2001:db8::1]") returns nil,
			// and a legitimate IPv6 client was skipped and then 403'd. Echo strips the
			// brackets per entry. Note this now interacts with the fail-closed stop: an
			// unstripped bracket reads as unparseable, so the client is denied rather
			// than merely misidentified.
			name:           "bracketed_ipv6_entry_is_parsed_not_rejected",
			remoteAddr:     "10.0.0.5:443",
			xff:            "[2001:db8::1], 10.0.0.9",
			trustedProxies: []string{"10.0.0.0/8"},
			want:           "2001:db8::1",
		},
		{
			// The plan's "fifth issue": XFF present but resolving to nothing. The single
			// entry trims to "", fails to parse, and the walk used to run out and hand
			// back the left-most entry — an empty string — which failed the emptiness
			// check and fell through to X-Real-IP, returned verbatim with no parse or
			// trust check. Both halves of that path are gone; the answer is the peer.
			name:           "whitespace_xff_does_not_fall_through_to_x_real_ip",
			remoteAddr:     "10.0.0.5:443",
			xff:            "   ",
			xRealIP:        "203.0.113.7",
			trustedProxies: []string{"10.0.0.0/8"},
			want:           "10.0.0.5",
		},
		{
			// An IPv6 peer arrives bracketed WITH a port in RemoteAddr; net.SplitHostPort
			// unwraps it. Pinned because extractPeerIP falls back to the raw RemoteAddr
			// when SplitHostPort errors, and that fallback would hand the caller's
			// allowlist an unparseable "[::1]:443" instead of an address.
			name:           "ipv6_peer_with_port_is_extracted",
			remoteAddr:     "[2001:db8::5]:443",
			trustedProxies: []string{"2001:db8::/32"},
			want:           "2001:db8::5",
		},
		{
			// AWS ALB's routing.http.xff_client_port.enabled appends "client_ip:port" on
			// EVERY request (ADR-057, "a proxy that writes a non-IP XFF entry keys the
			// entire fleet"). The address is perfectly readable; only our
			// parser could not read it. Assert the PARSED VALUE, not merely that the
			// request was denied — a denial assertion passes identically for "correctly
			// rejected" and "silently corrupted", which is how the bracket mangling below
			// stayed hidden.
			name:           "ipv4_entry_with_port_is_parsed",
			remoteAddr:     "10.0.0.5:443",
			xff:            "192.0.2.1:443",
			trustedProxies: []string{"10.0.0.0/8"},
			want:           "192.0.2.1",
		},
		{
			// The bracket-strip alone turns "[2001:db8::1]:443" into "2001:db8::1]:443" —
			// the leading bracket is trimmed, the trailing one is not final so it survives.
			// That is corruption, not rejection. SplitHostPort must therefore run on the
			// RAW entry, not on the trimmed form, or this shape stays broken while looking
			// fixed.
			name:           "bracketed_ipv6_entry_with_port_is_parsed",
			remoteAddr:     "10.0.0.5:443",
			xff:            "[2001:db8::1]:443",
			trustedProxies: []string{"10.0.0.0/8"},
			want:           "2001:db8::1",
		},
		{
			// The fail-closed stop must SURVIVE port handling. An RFC 7239 obfuscated
			// identifier is unreadable by construction — there is no address in it — so it
			// still stops the walk and yields the peer. Unreadable earns the stop; merely
			// port-suffixed never did.
			name:           "obfuscated_identifier_still_stops_the_walk",
			remoteAddr:     "10.0.0.5:443",
			xff:            "203.0.113.7, for=_hidden, 10.0.0.9",
			trustedProxies: []string{"10.0.0.0/8"},
			want:           "10.0.0.5",
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

// TestClientIPReadsEveryXForwardedForLine covers defect (a): r.Header.Get returns only
// the FIRST value for a key, so when a client sends its own X-Forwarded-For line and the
// fronting proxy adds a SECOND line rather than appending to the first, the real chain is
// invisible and the attacker's line is parsed alone. Echo reads every line and joins them
// (ip.go:246,250).
//
// This is the one defect with no operational tell: legitimate traffic keeps working, so
// nothing surfaces until someone exploits it. It needs its own test because the shared
// table's fixture sets a single header value via Set.
func TestClientIPReadsEveryXForwardedForLine(t *testing.T) {
	trusted := mustParseCIDRs(t, "10.0.0.0/8")

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	require.NoError(t, err)
	req.RemoteAddr = "10.0.0.5:443"
	// Line 1 is the attacker's and claims a TRUSTED-looking address; line 2 is what the
	// proxy appended, the real peer it observed. The attacker's choice of a trusted-looking
	// value is what makes this discriminating: reading line 1 alone yields "every hop
	// trusted", which before this fix returned the left-most entry — the attacker's. With
	// both lines read, 203.0.113.7 is present and is the first untrusted hop.
	//
	// An attacker line claiming an UNTRUSTED address would be returned either way, which is
	// why that shape is not tested here: it cannot tell the two implementations apart.
	req.Header.Add(HeaderXForwardedFor, "10.1.1.1")
	req.Header.Add(HeaderXForwardedFor, "203.0.113.7")

	assert.Equal(t, "203.0.113.7", ClientIP(req, trusted))
}

// TestClientIPRefusesNonRoutableHops pins the cross-family bypass the security audit found
// AFTER the first fix landed. net.IPNet.Contains is family-asymmetric — an IPv4 net never
// contains an IPv6 address — so an IPv6 entry judged against an IPv4 trust set is never
// "trusted", was therefore returned as the identified untrusted hop, and satisfied the
// shipped debug.allowedips default ["127.0.0.1","::1"].
//
// The trust set here is the one ADR-080 originally documented as a safe residual and which
// config.Validate accepted: two properly-masked, non-default-route entries covering all of
// IPv4. That claim was wrong, which is why the coverage check now rejects this list AND
// why ClientIP refuses to answer with a non-routable hop. Either fix alone leaves a gap:
// this one also covers a trust set that is broad but not total.
func TestClientIPRefusesNonRoutableHops(t *testing.T) {
	trusted := mustParseCIDRs(t, "10.0.0.0/8")

	for _, xff := range []string{"::1", "[::1]", "[::1]:443", "0:0:0:0:0:0:0:1", "127.0.0.1", "0.0.0.0", "::", "fe80::1"} {
		t.Run(safeName(xff), func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
			require.NoError(t, err)
			req.RemoteAddr = "10.0.0.5:443"
			req.Header.Set(HeaderXForwardedFor, xff)

			assert.Equal(t, "10.0.0.5", ClientIP(req, trusted),
				"a non-routable hop is a value no proxy could observe a real client at; answer with the peer")
		})
	}
}

// TestParseCIDRsRefusesTotalCoverage pins the ENFORCEMENT-point guard. ParseCIDRs is
// exported and takes raw strings, so a caller outside app construction — or a dynamic
// config source delivering a list after startup — reaches it without passing through
// config.Validate. Trusting every address is treated as trusting none.
func TestParseCIDRsRefusesTotalCoverage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []string
		wantNil bool
	}{
		{name: "explicit_default_route", entries: []string{"0.0.0.0/0"}, wantNil: true},
		{name: "two_halves_of_ipv4", entries: []string{"0.0.0.0/1", "128.0.0.0/1"}, wantNil: true},
		{name: "three_way_split", entries: []string{"0.0.0.0/1", "128.0.0.0/2", "192.0.0.0/2"}, wantNil: true},
		{name: "v4_mapped_default", entries: []string{"::ffff:0.0.0.0/96"}, wantNil: true},
		{name: "ipv6_halves", entries: []string{"::/1", "8000::/1"}, wantNil: true},
		{name: "realistic_rfc1918", entries: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}},
		{name: "half_of_ipv4_is_fine", entries: []string{"0.0.0.0/1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nets, invalid := ParseCIDRs(tc.entries)
			if tc.wantNil {
				assert.Nil(t, nets, "a list trusting an entire family must trust nobody")
				assert.NotEmpty(t, invalid, "the entries must be reported so the caller's WARN names them")
				return
			}
			assert.Len(t, nets, len(tc.entries))
			assert.Empty(t, invalid)
		})
	}
}

// TestParseChainEntryRejectsMalformedShapes pins the audit's F4: strings.Trim strips an
// UNPAIRED bracket and net.SplitHostPort accepts any port text, so "127.0.0.1[" and
// "127.0.0.1:notaport" read as clean addresses — turning a malformed entry into a grant
// where it previously stopped the walk.
func TestParseChainEntryRejectsMalformedShapes(t *testing.T) {
	trusted := mustParseCIDRs(t, "10.0.0.0/8")

	for _, xff := range []string{
		"203.0.113.7[, 10.0.0.6", "]203.0.113.7[, 10.0.0.6",
		"203.0.113.7:, 10.0.0.6", "203.0.113.7:notaport, 10.0.0.6",
		"[203.0.113.7]:x, 10.0.0.6", "203.0.113.7:0, 10.0.0.6", "203.0.113.7:99999, 10.0.0.6",
	} {
		t.Run(safeName(xff), func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
			require.NoError(t, err)
			req.RemoteAddr = "10.0.0.5:443"
			req.Header.Set(HeaderXForwardedFor, xff)

			assert.Equal(t, "10.0.0.5", ClientIP(req, trusted),
				"a malformed entry stops the walk; it must not be read as an address")
		})
	}
}

// safeName makes a header value usable as a subtest name: Go treats "/" as the subtest
// separator, and spaces render awkwardly in -run patterns.
func safeName(s string) string {
	return strings.NewReplacer("/", "_", ":", "-", " ", "_", ",", "+", "[", "", "]", "").Replace(s)
}
