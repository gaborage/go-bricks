package server

import (
	"net"
	"net/http"
	"strings"
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
	return nets, invalid
}

// ClientIP returns the real client IP for r, with trusted-proxy-chain verification that
// prevents X-Forwarded-For / X-Real-IP header spoofing. Unlike echo's LegacyIPExtractor
// (which trusts the left-most XFF entry unconditionally), proxy headers are honored ONLY
// when the immediate peer (RemoteAddr) is within trustedProxies. This is the secure
// extraction that access-control decisions (debug allowlist) and rate limiting must use.
//
// Algorithm (RFC 7239):
//  1. Extract the immediate peer IP from RemoteAddr.
//  2. If no trusted proxies are configured, OR the peer is not trusted, return the peer IP
//     and ignore all forwarding headers (an attacker connecting directly cannot forge it).
//  3. If the peer is trusted, walk X-Forwarded-For right-to-left and return the first
//     untrusted IP (the real client). Fall back to X-Real-IP, then the peer IP.
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

	// Peer is trusted: walk X-Forwarded-For right-to-left for the first untrusted hop.
	if xff := r.Header.Get(HeaderXForwardedFor); xff != "" {
		if clientIP := resolveXForwardedFor(xff, trustedProxies); clientIP != "" {
			return clientIP
		}
	}

	// Fall back to X-Real-IP, then the peer IP. The emptiness check is on the raw header
	// value so a present-but-blank X-Real-IP yields "" (caller's net.ParseIP then fails →
	// access control fails closed) rather than silently falling through to the proxy IP.
	if xri := r.Header.Get(HeaderXRealIP); xri != "" {
		return strings.TrimSpace(xri)
	}
	return peerIP
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
// IP — the real client per RFC 7239. If every hop is trusted (or none parse), it returns
// the left-most entry as a best-effort fallback.
func resolveXForwardedFor(xff string, trustedProxies []*net.IPNet) string {
	ips := strings.Split(xff, ",")
	for i := len(ips) - 1; i >= 0; i-- {
		ipStr := strings.TrimSpace(ips[i])
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if !ipInNets(ip, trustedProxies) {
			return ipStr
		}
	}
	if len(ips) > 0 {
		return strings.TrimSpace(ips[0])
	}
	return ""
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
