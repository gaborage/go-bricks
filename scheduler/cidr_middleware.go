package scheduler

import (
	"net"
	"net/http"
	"strings"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/server"
	"github.com/labstack/echo/v5"
)

// CIDRMiddleware creates middleware that restricts access based on CIDR allowlist.
// Per clarification #2: Empty allowlist = localhost-only access (127.0.0.1, ::1).
// Non-empty allowlist = restrict to matching IP ranges only.
//
// trustedProxies: CIDR ranges of reverse proxies that can be trusted to provide
// X-Forwarded-For/X-Real-IP headers. If empty, proxy headers are IGNORED to prevent spoofing.
//
// Invalid CIDR entries in either list are skipped (not fatal). The supplied logger
// receives a WARN with the invalid entries so operators see misconfigurations rather
// than silently degrading to a more restrictive posture (allowlist) or losing real
// client IPs from behind a reverse proxy (trustedProxies).
func CIDRMiddleware(log logger.Logger, allowlist, trustedProxies []string) echo.MiddlewareFunc {
	allowedNets, localhostOnly, invalidAllowlist := parseCIDRAllowlist(allowlist)
	trustedNets, invalidProxies := parseTrustedProxies(trustedProxies)

	if log != nil {
		if len(invalidAllowlist) > 0 {
			log.Warn().
				Int("count", len(invalidAllowlist)).
				Interface("entries", invalidAllowlist).
				Bool("falling_back_to_localhost_only", localhostOnly).
				Msg("Scheduler CIDR allowlist contains invalid entries; they were skipped")
		}
		if len(invalidProxies) > 0 {
			log.Warn().
				Int("count", len(invalidProxies)).
				Interface("entries", invalidProxies).
				Msg("Scheduler trusted-proxies list contains invalid CIDR entries; proxy headers from those ranges will not be trusted")
		}
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return createIPCheckHandler(next, allowedNets, trustedNets, localhostOnly)
	}
}

// parseCIDRAllowlist parses CIDR ranges and determines if localhost-only mode should be used.
// Returns the parsed nets, the localhost-only flag, and any input entries that failed to parse
// (so the caller can surface them — silent fallback is a security visibility gap).
func parseCIDRAllowlist(allowlist []string) (nets []*net.IPNet, localhostOnly bool, invalid []string) {
	if len(allowlist) == 0 {
		return nil, true, nil
	}

	nets = make([]*net.IPNet, 0, len(allowlist))
	for _, cidr := range allowlist {
		cidr = strings.TrimSpace(cidr)
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			invalid = append(invalid, cidr)
			continue
		}
		nets = append(nets, ipNet)
	}

	// If all CIDRs were invalid, fall back to localhost-only
	localhostOnly = len(nets) == 0
	return nets, localhostOnly, invalid
}

// parseTrustedProxies parses trusted proxy CIDR ranges for header validation.
// Returns the parsed nets and any input entries that failed to parse.
func parseTrustedProxies(trustedProxies []string) (nets []*net.IPNet, invalid []string) {
	if len(trustedProxies) == 0 {
		return nil, nil
	}

	nets = make([]*net.IPNet, 0, len(trustedProxies))
	for _, cidr := range trustedProxies {
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

// createIPCheckHandler creates the handler function that validates IP addresses
func createIPCheckHandler(next echo.HandlerFunc, allowedNets, trustedNets []*net.IPNet, localhostOnly bool) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ip, err := parseRemoteIP(c.Request(), trustedNets)
		if err != nil {
			return echo.NewHTTPError(http.StatusForbidden, "Invalid IP address")
		}

		if !isIPAllowed(ip, allowedNets, localhostOnly) {
			return createAccessDeniedError(localhostOnly)
		}

		return next(c)
	}
}

// parseRemoteIP extracts and parses the remote IP from the request
func parseRemoteIP(r *http.Request, trustedNets []*net.IPNet) (net.IP, error) {
	remoteIPStr := extractRemoteIP(r, trustedNets)
	ip := net.ParseIP(remoteIPStr)
	if ip == nil {
		return nil, echo.NewHTTPError(http.StatusForbidden, "Invalid IP address")
	}
	return ip, nil
}

// isIPAllowed checks if an IP is allowed based on the configuration
func isIPAllowed(ip net.IP, allowedNets []*net.IPNet, localhostOnly bool) bool {
	if localhostOnly {
		return isLocalhost(ip)
	}
	return isIPInAllowedNets(ip, allowedNets)
}

// createAccessDeniedError creates the appropriate error message
func createAccessDeniedError(localhostOnly bool) error {
	if localhostOnly {
		return echo.NewHTTPError(http.StatusForbidden, "Access denied: localhost-only")
	}
	return echo.NewHTTPError(http.StatusForbidden, "Access denied: IP not in allowlist")
}

// extractRemoteIP extracts the real client IP from the request with trusted proxy chain verification.
// This prevents header spoofing attacks by only honoring proxy headers when the immediate peer is trusted.
//
// Algorithm:
// 1. Extract the immediate peer IP from RemoteAddr
// 2. If peer is NOT in trustedNets, return peer IP (ignore all headers - prevents spoofing)
// 3. If peer IS trusted, parse X-Forwarded-For right-to-left to find the first untrusted IP
// 4. If no X-Forwarded-For or all IPs are trusted, check X-Real-IP
// 5. Fall back to RemoteAddr if no valid headers
func extractRemoteIP(r *http.Request, trustedNets []*net.IPNet) string {
	// Extract immediate peer IP from RemoteAddr
	peerIP := extractPeerIP(r.RemoteAddr)
	if peerIP == "" {
		return r.RemoteAddr // Fallback to raw address
	}

	// If no trusted proxies configured, never trust headers (prevents spoofing)
	if len(trustedNets) == 0 {
		return peerIP
	}

	// Check if immediate peer is trusted
	peer := net.ParseIP(peerIP)
	if peer == nil || !isIPInAllowedNets(peer, trustedNets) {
		// Peer is NOT trusted - ignore all proxy headers to prevent spoofing
		return peerIP
	}

	// Peer is trusted - check proxy headers
	// Parse X-Forwarded-For right-to-left to find first untrusted IP
	if xff := r.Header.Get(server.HeaderXForwardedFor); xff != "" {
		if clientIP := resolveXForwardedFor(xff, trustedNets); clientIP != "" {
			return clientIP
		}
	}

	// Check X-Real-IP if X-Forwarded-For not present
	if xri := r.Header.Get(server.HeaderXRealIP); xri != "" {
		return strings.TrimSpace(xri)
	}

	// No valid headers - return peer IP
	return peerIP
}

// extractPeerIP extracts the IP address from RemoteAddr (format: "IP:port" or just "IP")
func extractPeerIP(remoteAddr string) string {
	ip, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// If SplitHostPort fails, RemoteAddr might be just IP (no port)
		return remoteAddr
	}
	return ip
}

// resolveXForwardedFor parses X-Forwarded-For header right-to-left to find the first untrusted IP.
// This implements proper proxy chain verification as recommended by RFC 7239.
//
// Example: X-Forwarded-For: "203.0.113.1, 192.168.1.1, 10.0.0.5"
// With trustedNets=[10.0.0.0/8, 192.168.0.0/16]:
// - Walk right-to-left: 10.0.0.5 (trusted), 192.168.1.1 (trusted), 203.0.113.1 (untrusted)
// - Return: "203.0.113.1" (real client IP)
func resolveXForwardedFor(xff string, trustedNets []*net.IPNet) string {
	// Split by comma and trim spaces
	ips := strings.Split(xff, ",")

	// Walk right-to-left to find first untrusted IP
	for i := len(ips) - 1; i >= 0; i-- {
		ipStr := strings.TrimSpace(ips[i])
		ip := net.ParseIP(ipStr)

		if ip == nil {
			continue // Invalid IP - skip
		}

		// Check if this IP is trusted
		if !isIPInAllowedNets(ip, trustedNets) {
			// Found first untrusted IP - this is the real client
			return ipStr
		}
	}

	// All IPs in chain are trusted (or none parsed successfully)
	// Return leftmost IP as fallback
	if len(ips) > 0 {
		return strings.TrimSpace(ips[0])
	}

	return ""
}

// isLocalhost checks if an IP is localhost (127.0.0.1 or ::1)
func isLocalhost(ip net.IP) bool {
	// Check IPv4 localhost (127.0.0.0/8)
	if ip.To4() != nil {
		return ip.IsLoopback()
	}

	// Check IPv6 localhost (::1)
	return ip.IsLoopback()
}

// isIPInAllowedNets checks if an IP is in any of the allowed CIDR ranges
func isIPInAllowedNets(ip net.IP, allowedNets []*net.IPNet) bool {
	for _, ipNet := range allowedNets {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}
