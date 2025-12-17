package scheduler

import (
	"net"
	"net/http"
	"strings"

	"github.com/gaborage/go-bricks/server"
	"github.com/labstack/echo/v4"
)

// CIDRMiddleware creates middleware that restricts access based on CIDR allowlist.
// Per clarification #2: Empty allowlist = localhost-only access (127.0.0.1, ::1).
// Non-empty allowlist = restrict to matching IP ranges only.
//
// trustedProxies: CIDR ranges of reverse proxies that can be trusted to provide
// X-Forwarded-For/X-Real-IP headers. If empty, proxy headers are IGNORED to prevent spoofing.
func CIDRMiddleware(allowlist, trustedProxies []string) echo.MiddlewareFunc {
	allowedNets, localhostOnly := parseCIDRAllowlist(allowlist)
	trustedNets := parseTrustedProxies(trustedProxies)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return createIPCheckHandler(next, allowedNets, trustedNets, localhostOnly)
	}
}

// parseCIDRAllowlist parses CIDR ranges and determines if localhost-only mode should be used
func parseCIDRAllowlist(allowlist []string) ([]*net.IPNet, bool) {
	if len(allowlist) == 0 {
		return nil, true
	}

	allowedNets := make([]*net.IPNet, 0, len(allowlist))
	for _, cidr := range allowlist {
		cidr = strings.TrimSpace(cidr)
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue // Invalid CIDR - skip it
		}
		allowedNets = append(allowedNets, ipNet)
	}

	// If all CIDRs were invalid, fall back to localhost-only
	localhostOnly := len(allowedNets) == 0
	return allowedNets, localhostOnly
}

// parseTrustedProxies parses trusted proxy CIDR ranges for header validation
func parseTrustedProxies(trustedProxies []string) []*net.IPNet {
	if len(trustedProxies) == 0 {
		return nil // No trusted proxies = ignore all proxy headers
	}

	trustedNets := make([]*net.IPNet, 0, len(trustedProxies))
	for _, cidr := range trustedProxies {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue // Invalid CIDR - skip it
		}
		trustedNets = append(trustedNets, ipNet)
	}

	return trustedNets
}

// createIPCheckHandler creates the handler function that validates IP addresses
func createIPCheckHandler(next echo.HandlerFunc, allowedNets, trustedNets []*net.IPNet, localhostOnly bool) echo.HandlerFunc {
	return func(c echo.Context) error {
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
