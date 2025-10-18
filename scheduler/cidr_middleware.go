package scheduler

import (
	"net"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// CIDRMiddleware creates middleware that restricts access based on CIDR allowlist.
// Per clarification #2: Empty allowlist = localhost-only access (127.0.0.1, ::1).
// Non-empty allowlist = restrict to matching IP ranges only.
func CIDRMiddleware(allowlist []string) echo.MiddlewareFunc {
	allowedNets, localhostOnly := parseCIDRAllowlist(allowlist)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return createIPCheckHandler(next, allowedNets, localhostOnly)
	}
}

// parseCIDRAllowlist parses CIDR ranges and determines if localhost-only mode should be used
func parseCIDRAllowlist(allowlist []string) ([]*net.IPNet, bool) {
	if len(allowlist) == 0 {
		return nil, true
	}

	allowedNets := make([]*net.IPNet, 0, len(allowlist))
	for _, cidr := range allowlist {
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

// createIPCheckHandler creates the handler function that validates IP addresses
func createIPCheckHandler(next echo.HandlerFunc, allowedNets []*net.IPNet, localhostOnly bool) echo.HandlerFunc {
	return func(c echo.Context) error {
		ip, err := parseRemoteIP(c.Request())
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
func parseRemoteIP(r *http.Request) (net.IP, error) {
	remoteIPStr := extractRemoteIP(r)
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

// extractRemoteIP extracts the remote IP from the request.
// Handles X-Forwarded-For and X-Real-IP headers for proxy scenarios.
func extractRemoteIP(r *http.Request) string {
	// Check X-Forwarded-For header (may contain multiple IPs)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP (client's original IP)
		ips := strings.Split(xff, ",")
		return strings.TrimSpace(ips[0])
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}

	// Fall back to RemoteAddr
	// RemoteAddr format: "IP:port"
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// If SplitHostPort fails, RemoteAddr might be just IP
		return r.RemoteAddr
	}

	return ip
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
