package server

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gaborage/go-bricks/config"
)

// ParseCIDRs parses a list of CIDR strings into *net.IPNet values, trimming surrounding
// whitespace on each entry. Entries that fail to parse are returned separately in
// `invalid` so callers can surface a WARN instead of silently degrading (a dropped
// trusted-proxy range silently weakens spoofing protection). A nil/empty input yields
// nil, nil.
func ParseCIDRs(cidrs []string) (nets []*net.IPNet, invalid []string) {
	if len(cidrs) == 0 {
		return nil, nil
	}
	nets = make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		cidr = strings.TrimSpace(cidr)
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			invalid = append(invalid, cidr)
			continue
		}
		nets = append(nets, ipNet)
	}

	// A trust list covering an entire address family trusts every peer, which makes the
	// forwarding headers believable from a direct connection — the bypass ADR-080 closes.
	// config.Validate refuses it at startup, but this is the ENFORCEMENT point: ParseCIDRs
	// is exported, takes raw strings, and is what both access-control consumers call, so a
	// caller outside app construction (or a dynamic config source delivering a list after
	// startup) reaches here without ever passing through validation.
	//
	// Trusting everyone is treated as trusting no one: returning no nets makes ClientIP
	// ignore the headers entirely and answer with the peer, which is the fail-closed
	// reading. Every entry is reported invalid so the caller's existing WARN names them.
	for _, bits := range []int{net.IPv4len * 8, net.IPv6len * 8} {
		if config.CoversAddressFamily(nets, bits) {
			// Report the trimmed entries once, not the raw input appended to whatever the
			// parse loop already rejected: the caller's WARN says "invalid CIDR entries",
			// and an operator whose syntax is perfect would go hunting for a typo instead
			// of looking at their coverage.
			total := make([]string, 0, len(cidrs))
			for _, c := range cidrs {
				total = append(total, strings.TrimSpace(c))
			}
			return nil, total
		}
	}
	return nets, invalid
}

// ClientIP returns the real client IP for r, with trusted-proxy-chain verification that
// prevents X-Forwarded-For spoofing. Proxy headers are honored ONLY when the immediate
// peer (RemoteAddr) is within trustedProxies. Its two consumers are both access-control
// checks: the debug-endpoint IP allowlist and the scheduler's CIDR middleware guarding
// /_sys/job. (Rate limiting does NOT use this function — it runs through echo's own
// extractor since #965/ADR-057.)
//
// The governing rule, and the one every branch below serves: the answer is either an
// identified untrusted hop, or the peer address we actually observed — never a value the
// caller wrote (ADR-080).
//
// Algorithm (RFC 7239):
//  1. Extract the immediate peer IP from RemoteAddr.
//  2. If no trusted proxies are configured, OR the peer is not trusted, return the peer IP
//     and ignore all forwarding headers (an attacker connecting directly cannot forge it).
//  3. If the peer is trusted, walk X-Forwarded-For right-to-left and return the first
//     untrusted IP (the real client); otherwise the peer IP. X-Real-IP is never consulted.
func ClientIP(r *http.Request, trustedProxies []*net.IPNet) string {
	peerIP := extractPeerIP(r.RemoteAddr)
	if peerIP == "" {
		return r.RemoteAddr
	}

	// No trusted proxies configured: never trust headers (prevents spoofing).
	if len(trustedProxies) == 0 {
		return peerIP
	}

	// Immediate peer must be a trusted proxy before any header is believed.
	peer := net.ParseIP(peerIP)
	if peer == nil || !ipInNets(peer, trustedProxies) {
		return peerIP
	}

	// No X-Forwarded-For at all: the answer is the peer we observed. X-Real-IP is
	// deliberately NOT consulted — ADR-057 decided it "is not honored at all... No
	// fallback is added", which server/server.go already implements and this function
	// never did. A trusted peer vouches for the chain it appends to, not for a
	// single-valued header any upstream party can set.
	xffs := r.Header.Values(HeaderXForwardedFor)
	if len(xffs) == 0 {
		return peerIP
	}

	// Peer is trusted, so the chain is authoritative and its answer is final. Every value
	// resolveXForwardedFor can return is non-empty, so there is no fall-through from here.
	return resolveXForwardedFor(strings.Join(xffs, ","), peerIP, trustedProxies)
}

// extractPeerIP extracts the IP from RemoteAddr ("IP:port" or bare "IP").
func extractPeerIP(remoteAddr string) string {
	ip, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// RemoteAddr may be a bare IP with no port.
		return remoteAddr
	}
	return ip
}

// resolveXForwardedFor walks the XFF chain right-to-left and returns the first untrusted
// IP — the real client per RFC 7239. When the chain names no untrusted client, because
// every hop is trusted or because a hop will not parse, it returns peerIP: the left-most
// entry is only whatever the earliest party chose to write, and handing that to an
// access-control decision is the whole class this function exists to refuse.
//
// It never returns "", which is what lets ClientIP treat the walk's answer as final.
func resolveXForwardedFor(xff, peerIP string, trustedProxies []*net.IPNet) string {
	ips := strings.Split(xff, ",")
	for i := len(ips) - 1; i >= 0; i-- {
		// RFC 9110 §5.6.1: an empty list element is ignored, not read. A trailing comma or
		// a blank X-Forwarded-For line would otherwise look like an unreadable hop and stop
		// the walk at the proxy's own address — denying legitimate traffic, and letting a
		// non-appending proxy's client end the chain early. An element with no content
		// cannot carry a caller's claim, so skipping it leaks nothing.
		if strings.TrimSpace(ips[i]) == "" {
			continue
		}
		ipStr, ip := parseChainEntry(ips[i])
		if ip == nil {
			// Unparseable hop: the chain cannot be read, so stop rather than walking left
			// into whatever the caller wrote. Echo takes the same posture ("Unable to parse
			// IP; cannot trust entire records"). The peer is a trusted proxy and will
			// normally fail the caller's allowlist — the correct fail-closed outcome for a
			// chain we cannot interpret.
			return peerIP
		}
		if !ipInNets(ip, trustedProxies) {
			// An untrusted hop is the client — unless it is an address no proxy could
			// have observed a real client at. Loopback, unspecified and link-local
			// entries are the shapes an attacker writes to impersonate a local caller,
			// and they are exactly what the shipped allowlists contain. Answering with
			// the peer instead keeps a caller-authored value out of the decision even
			// when the trust set is wider than it should be (ADR-080).
			if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
				ip.IsMulticast() || ip.IsLinkLocalMulticast() {
				return peerIP
			}
			return ipStr
		}
	}
	// Every hop is trusted: the chain names no client we can attribute.
	return peerIP
}

// parseChainEntry reads one X-Forwarded-For entry into an address, returning a nil IP when
// the entry carries no address at all.
//
// Two shapes need normalizing before net.ParseIP will accept them, and both are emitted by
// real proxies rather than by attackers:
//
//   - Brackets around an IPv6 entry. ParseIP rejects "[2001:db8::1]".
//   - A port suffix. AWS ALB's routing.http.xff_client_port.enabled appends
//     "client_ip:port" on every request (ADR-057, "a proxy that writes a non-IP
//     XFF entry keys the entire fleet on the proxy").
//
// Refusing to read those would deny a correctly-configured deployment on every request,
// which is not a fail-closed posture — it is exporting this parser's limitation to the
// operator as an incident. The fail-closed stop is for entries that are UNREADABLE, such
// as RFC 7239's "for=_hidden" or "unknown": those carry no address, and no amount of
// parsing produces one.
//
// Order matters twice. ParseIP runs FIRST because net.SplitHostPort rejects a bare IPv6
// address ("too many colons"), so a split-first reading would break the commonest IPv6
// shape. And the SplitHostPort fallback runs on the RAW entry, never on the
// bracket-trimmed one: trimming "[2001:db8::1]:443" yields "2001:db8::1]:443" — the
// leading bracket goes, the trailing one is not final so it stays — and feeding that to
// SplitHostPort fails too, leaving the shape broken while looking handled.
func parseChainEntry(entry string) (ipStr string, ip net.IP) {
	raw := strings.TrimSpace(entry)

	// Brackets must be PAIRED. strings.Trim would strip an unmatched one from either end,
	// so "127.0.0.1[" would read as a clean address — turning a malformed entry into a
	// grant. Unbalanced brackets are malformed, and malformed entries stop the walk.
	trimmed := raw
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		trimmed = raw[1 : len(raw)-1]
	}
	if parsed := net.ParseIP(trimmed); parsed != nil {
		return trimmed, parsed
	}

	// Port fallback, on the RAW entry: bracket-trimming "[2001:db8::1]:443" leaves a
	// trailing "]" that defeats both parsers. net.SplitHostPort does not validate the
	// port, so check it here — "127.0.0.1:notaport" is malformed, and reading an address
	// out of it would again turn a malformed entry into a grant.
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return trimmed, nil
	}
	// ParseUint, not Atoi: Atoi accepts "+80", which would read a malformed entry as clean.
	if n, convErr := strconv.ParseUint(port, 10, 16); convErr != nil || n == 0 {
		return trimmed, nil
	}
	if parsed := net.ParseIP(host); parsed != nil {
		return host, parsed
	}
	return trimmed, nil
}

// ipInNets reports whether ip falls within any of nets.
func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
