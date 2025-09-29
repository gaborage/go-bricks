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

	"github.com/labstack/echo/v4"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
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
func (d *DebugHandlers) RegisterDebugEndpoints(e *echo.Echo) {
	if !d.config.Enabled {
		d.logger.Info().Msg("Debug endpoints disabled")
		return
	}

	debugGroup := e.Group(d.config.PathPrefix)

	// Apply security middleware
	debugGroup.Use(d.ipWhitelistMiddleware())
	if d.config.BearerToken != "" {
		debugGroup.Use(d.authMiddleware())
	}

	// Register endpoints
	if d.config.Endpoints.Goroutines {
		debugGroup.GET("/goroutines", d.handleGoroutines)
	}
	if d.config.Endpoints.GC {
		debugGroup.GET("/gc", d.handleGC)
		debugGroup.POST("/gc", d.handleForceGC)
	}
	if d.config.Endpoints.Health {
		debugGroup.GET("/health-debug", d.handleHealthDebug)
	}
	if d.config.Endpoints.Info {
		debugGroup.GET("/info", d.handleInfo)
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

// ipWhitelistMiddleware restricts access to allowed IPs
func (d *DebugHandlers) ipWhitelistMiddleware() echo.MiddlewareFunc {
	whitelist := NewIPWhitelist(d.config.AllowedIPs, d.logger)
	if len(d.config.AllowedIPs) == 0 {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				return next(c)
			}
		}
	}
	return d.createIPCheckHandler(whitelist)
}

// createIPCheckHandler creates the middleware handler with IP validation
func (d *DebugHandlers) createIPCheckHandler(whitelist *IPWhitelist) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			clientIP := c.RealIP()
			if allowed, err := d.isIPAllowed(clientIP, whitelist); err != nil {
				return d.handleAccessDenied(clientIP, "invalid IP")
			} else if !allowed {
				return d.handleAccessDenied(clientIP, "IP not whitelisted")
			}

			return next(c)
		}
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
	return echo.NewHTTPError(http.StatusForbidden, "Access denied")
}

// authMiddleware provides bearer token authentication
func (d *DebugHandlers) authMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				return echo.NewHTTPError(http.StatusUnauthorized, "Bearer token required")
			}

			token := strings.TrimPrefix(authHeader, "Bearer ")
			if subtle.ConstantTimeCompare([]byte(token), []byte(d.config.BearerToken)) != 1 {
				d.logger.Warn().Str("client_ip", c.RealIP()).Msg("Debug endpoint access denied: invalid token")
				return echo.NewHTTPError(http.StatusUnauthorized, "Invalid token")
			}

			return next(c)
		}
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
func (d *DebugHandlers) handleInfo(c echo.Context) error {
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
