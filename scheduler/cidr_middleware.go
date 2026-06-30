package scheduler

import (
	"net"
	"net/http"
	"strings"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/server"
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
func CIDRMiddleware(log logger.Logger, allowlist, trustedProxies []string) server.MiddlewareFunc {
	allowedNets, localhostOnly, invalidAllowlist := parseCIDRAllowlist(allowlist)
	trustedNets, invalidProxies := server.ParseCIDRs(trustedProxies)

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

	return func(c server.HandlerContext, next func() error) error {
		ip, err := parseRemoteIP(c.Request(), trustedNets)
		if err != nil {
			return err
		}

		if !isIPAllowed(ip, allowedNets, localhostOnly) {
			return createAccessDeniedError(localhostOnly)
		}

		return next()
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

// parseRemoteIP extracts and parses the remote IP from the request, using the shared
// trusted-proxy-aware extractor in package server so the scheduler and the debug
// endpoints share one canonical, spoofing-resistant implementation.
func parseRemoteIP(r *http.Request, trustedNets []*net.IPNet) (net.IP, error) {
	remoteIPStr := server.ClientIP(r, trustedNets)
	ip := net.ParseIP(remoteIPStr)
	if ip == nil {
		return nil, server.NewForbiddenError("Invalid IP address")
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
		return server.NewForbiddenError("Access denied: localhost-only")
	}
	return server.NewForbiddenError("Access denied: IP not in allowlist")
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
