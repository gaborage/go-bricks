package app

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/server"
)

// DebugResponse represents a standard debug endpoint response
type DebugResponse struct {
	Timestamp time.Time `json:"timestamp"`
	Duration  string    `json:"duration"`
	Data      any       `json:"data"`
	Error     string    `json:"error,omitempty"`
}

// GoroutineInfo contains information about goroutines
type GoroutineInfo struct {
	Count      int              `json:"count"`
	ByState    map[string]int   `json:"by_state"`
	ByFunction map[string]int   `json:"by_function"`
	Stacks     []GoroutineStack `json:"stacks,omitempty"`
	Leaks      []PotentialLeak  `json:"potential_leaks,omitempty"`
}

// GoroutineStack represents a single goroutine's stack trace
type GoroutineStack struct {
	ID       int      `json:"id"`
	State    string   `json:"state"`
	Function string   `json:"function"`
	Stack    []string `json:"stack"`
}

// PotentialLeak represents a goroutine that might be leaked
type PotentialLeak struct {
	ID       int    `json:"id"`
	Function string `json:"function"`
	Duration string `json:"duration"`
	Reason   string `json:"reason"`
}

// GCInfo contains garbage collection information
type GCInfo struct {
	Stats       debug.GCStats `json:"stats"`
	MemBefore   uint64        `json:"mem_before"`
	MemAfter    uint64        `json:"mem_after"`
	Forced      bool          `json:"forced"`
	HeapObjects uint64        `json:"heap_objects"`
	HeapSize    uint64        `json:"heap_size"`
}

// DebugHandlers manages debug endpoints
type DebugHandlers struct {
	app    *App
	config *config.DebugConfig
	logger logger.Logger
}

// NewDebugHandlers creates a new debug handlers instance
func NewDebugHandlers(app *App, cfg *config.DebugConfig, log logger.Logger) *DebugHandlers {
	return &DebugHandlers{
		app:    app,
		config: cfg,
		logger: log,
	}
}

// RegisterDebugEndpoints registers all debug endpoints if enabled
func (d *DebugHandlers) RegisterDebugEndpoints(r server.RouteRegistrar) {
	if !d.config.Enabled {
		d.logger.Info().Msg("Debug endpoints disabled")
		return
	}

	g := r.Group(d.config.PathPrefix)

	// Parse trusted proxies once and surface any invalid entries here so the warning is
	// logged a single time at registration rather than on every middleware construction.
	// The parsed nets are threaded into both security middlewares so client-IP derivation
	// is trusted-proxy aware (server.ClientIP) rather than echo's spoofable c.RealIP().
	trustedNets, invalid := server.ParseCIDRs(d.config.TrustedProxies)
	if len(invalid) > 0 {
		d.logger.Warn().
			Int("count", len(invalid)).
			Interface("entries", invalid).
			Msg("Debug endpoint trustedproxies list contains invalid CIDR entries; proxy headers from those ranges will not be trusted")
	}

	// Apply security middleware (ipWhitelist before auth — ordering preserved via Use order).
	g.Use(d.ipWhitelistMiddleware(trustedNets))
	if d.config.BearerToken != "" {
		g.Use(d.authMiddleware(trustedNets))
	}

	// Register endpoints
	if d.config.Endpoints.Goroutines {
		g.Add(http.MethodGet, "/goroutines", d.handleGoroutines)
	}
	if d.config.Endpoints.GC {
		g.Add(http.MethodGet, "/gc", d.handleGC)
		g.Add(http.MethodPost, "/gc", d.handleForceGC)
	}
	if d.config.Endpoints.Health {
		g.Add(http.MethodGet, "/health-debug", d.handleHealthDebug)
	}
	if d.config.Endpoints.Info {
		g.Add(http.MethodGet, "/info", d.handleInfo)
	}

	d.logger.Info().
		Str("prefix", d.config.PathPrefix).
		Msgf("Debug endpoints registered (allowed_ips=%d, auth_enabled=%t)",
			len(d.config.AllowedIPs), d.config.BearerToken != "")
}

// IPWhitelist manages a list of allowed IP networks for access control
type IPWhitelist struct {
	networks []*net.IPNet
	logger   logger.Logger
}

// NewIPWhitelist creates a new IP whitelist from a list of IP strings
func NewIPWhitelist(ips []string, log logger.Logger) *IPWhitelist {
	whitelist := &IPWhitelist{
		networks: make([]*net.IPNet, 0, len(ips)),
		logger:   log,
	}

	for _, ipStr := range ips {
		if network := whitelist.parseIPNetwork(ipStr); network != nil {
			whitelist.networks = append(whitelist.networks, network)
		}
	}

	return whitelist
}

// Contains checks if the given IP is allowed by this whitelist
func (w *IPWhitelist) Contains(ip net.IP) bool {
	for _, network := range w.networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// parseIPNetwork parses and validates a single IP network string
func (w *IPWhitelist) parseIPNetwork(ipStr string) *net.IPNet {
	cleanIP := w.cleanIPString(ipStr)
	if cleanIP == "" {
		return nil
	}

	cidrIP := w.normalizeToCIDR(cleanIP)
	_, network, err := net.ParseCIDR(cidrIP)
	if err != nil {
		w.logger.Warn().Str("ip", ipStr).Err(err).Msg("Invalid IP in whitelist")
		return nil
	}

	return network
}

// cleanIPString removes whitespace and quotes from IP string
func (w *IPWhitelist) cleanIPString(ipStr string) string {
	cleanIP := strings.TrimSpace(ipStr)
	cleanIP = strings.Trim(cleanIP, "\"'")
	return cleanIP
}

// normalizeToCIDR converts single IPs to CIDR notation
func (w *IPWhitelist) normalizeToCIDR(ipStr string) string {
	if strings.Contains(ipStr, "/") {
		return ipStr
	}

	if strings.Contains(ipStr, ":") {
		return ipStr + "/128" // IPv6
	}
	return ipStr + "/32" // IPv4
}

// ipWhitelistMiddleware restricts access to allowed IPs. The trusted-proxy nets are parsed
// once by RegisterDebugEndpoints and threaded in so client-IP derivation is spoof-resistant.
func (d *DebugHandlers) ipWhitelistMiddleware(trustedNets []*net.IPNet) server.MiddlewareFunc {
	if len(d.config.AllowedIPs) == 0 {
		return func(_ server.HandlerContext, next func() error) error {
			return next()
		}
	}

	whitelist := NewIPWhitelist(d.config.AllowedIPs, d.logger)
	return d.createIPCheckHandler(whitelist, trustedNets)
}

// createIPCheckHandler creates the middleware handler with IP validation.
// The client IP is derived with trusted-proxy verification (server.ClientIP) rather than
// echo's spoofable c.RealIP(): X-Forwarded-For / X-Real-IP are honored only when the
// immediate peer is a configured trusted proxy, so the allowlist cannot be bypassed by an
// unauthenticated client sending a forged X-Forwarded-For header.
func (d *DebugHandlers) createIPCheckHandler(whitelist *IPWhitelist, trustedNets []*net.IPNet) server.MiddlewareFunc {
	return func(c server.HandlerContext, next func() error) error {
		clientIP := server.ClientIP(c.Request(), trustedNets)
		if allowed, err := d.isIPAllowed(clientIP, whitelist); err != nil {
			return d.handleAccessDenied(clientIP, "invalid IP")
		} else if !allowed {
			return d.handleAccessDenied(clientIP, "IP not whitelisted")
		}

		return next()
	}
}

// isIPAllowed checks if a client IP string is allowed by the whitelist
func (d *DebugHandlers) isIPAllowed(clientIP string, whitelist *IPWhitelist) (bool, error) {
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false, fmt.Errorf("invalid IP format: %s", clientIP)
	}
	return whitelist.Contains(ip), nil
}

// handleAccessDenied logs the denial and returns a forbidden error
func (d *DebugHandlers) handleAccessDenied(clientIP, reason string) error {
	d.logger.Warn().Str("client_ip", clientIP).Msgf("Debug endpoint access denied: %s", reason)
	return server.NewForbiddenError("Access denied")
}

// authMiddleware provides bearer token authentication. The trusted-proxy nets are threaded
// in so the invalid-token denial log records the trusted-proxy-aware client IP
// (server.ClientIP) instead of echo's spoofable c.RealIP().
func (d *DebugHandlers) authMiddleware(trustedNets []*net.IPNet) server.MiddlewareFunc {
	return func(c server.HandlerContext, next func() error) error {
		authHeader := c.RequestHeader("Authorization")
		// The Authorization scheme is case-insensitive per RFC 7235; match it with EqualFold.
		scheme, token, ok := strings.Cut(authHeader, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			return server.NewUnauthorizedError("Bearer token required")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(d.config.BearerToken)) != 1 {
			d.logger.Warn().Str("client_ip", server.ClientIP(c.Request(), trustedNets)).Msg("Debug endpoint access denied: invalid token")
			return server.NewUnauthorizedError("Invalid token")
		}

		return next()
	}
}

// newDebugResponse creates a standardized debug response
func (d *DebugHandlers) newDebugResponse(start time.Time, data any, err error) *DebugResponse {
	resp := &DebugResponse{
		Timestamp: start,
		Duration:  time.Since(start).String(),
		Data:      data,
	}
	if err != nil {
		resp.Error = err.Error()
	}
	return resp
}

// handleInfo returns basic system information
func (d *DebugHandlers) handleInfo(c server.HandlerContext) error {
	start := time.Now()

	info := map[string]any{
		"goroutines": runtime.NumGoroutine(),
		"go_version": runtime.Version(),
		"go_os":      runtime.GOOS,
		"go_arch":    runtime.GOARCH,
		"num_cpu":    runtime.NumCPU(),
		"debug_config": map[string]any{
			"enabled":      d.config.Enabled,
			"path_prefix":  d.config.PathPrefix,
			"auth_enabled": d.config.BearerToken != "",
			"allowed_ips":  len(d.config.AllowedIPs),
		},
	}

	// Add app-specific info if available
	if d.app.cfg != nil {
		info["app"] = map[string]any{
			"name":        d.app.cfg.App.Name,
			"environment": d.app.cfg.App.Env,
			"version":     d.app.cfg.App.Version,
		}
	}

	resp := d.newDebugResponse(start, info, nil)
	return c.JSON(http.StatusOK, resp)
}
