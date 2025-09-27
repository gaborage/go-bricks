package app

import (
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
	Timestamp time.Time   `json:"timestamp"`
	Duration  string      `json:"duration"`
	Data      interface{} `json:"data"`
	Error     string      `json:"error,omitempty"`
}

// GoroutineInfo contains information about goroutines
type GoroutineInfo struct {
	Count      int                `json:"count"`
	ByState    map[string]int     `json:"by_state"`
	ByFunction map[string]int     `json:"by_function"`
	Stacks     []GoroutineStack   `json:"stacks,omitempty"`
	Leaks      []PotentialLeak    `json:"potential_leaks,omitempty"`
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
func NewDebugHandlers(app *App, cfg *config.DebugConfig, logger logger.Logger) *DebugHandlers {
	return &DebugHandlers{
		app:    app,
		config: cfg,
		logger: logger,
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
	debugGroup.GET("/goroutines", d.handleGoroutines)
	debugGroup.GET("/gc", d.handleGC)
	debugGroup.POST("/gc", d.handleForceGC)
	debugGroup.GET("/health-debug", d.handleHealthDebug)
	debugGroup.GET("/info", d.handleInfo)

	d.logger.Info().
		Str("prefix", d.config.PathPrefix).
		Msgf("Debug endpoints registered (allowed_ips=%d, auth_enabled=%t)",
			len(d.config.AllowedIPs), d.config.BearerToken != "")
}

// ipWhitelistMiddleware restricts access to allowed IPs
func (d *DebugHandlers) ipWhitelistMiddleware() echo.MiddlewareFunc {
	allowedNets := make([]*net.IPNet, 0, len(d.config.AllowedIPs))

	for _, ipStr := range d.config.AllowedIPs {
		ipStr = strings.TrimSpace(ipStr)
		// Remove any surrounding quotes that might be included from config parsing
		ipStr = strings.Trim(ipStr, "\"'")
		if ipStr == "" {
			continue
		}

		// Handle CIDR notation or single IPs
		if !strings.Contains(ipStr, "/") {
			if strings.Contains(ipStr, ":") {
				// IPv6
				ipStr += "/128"
			} else {
				// IPv4
				ipStr += "/32"
			}
		}

		_, ipNet, err := net.ParseCIDR(ipStr)
		if err != nil {
			d.logger.Warn().Str("ip", ipStr).Err(err).Msg("Invalid IP in whitelist")
			continue
		}
		allowedNets = append(allowedNets, ipNet)
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			clientIP := c.RealIP()
			ip := net.ParseIP(clientIP)
			if ip == nil {
				d.logger.Warn().Str("client_ip", clientIP).Msg("Debug endpoint access denied: invalid IP")
				return echo.NewHTTPError(http.StatusForbidden, "Access denied")
			}

			allowed := false
			for _, ipNet := range allowedNets {
				if ipNet.Contains(ip) {
					allowed = true
					break
				}
			}

			if !allowed {
				d.logger.Warn().Str("client_ip", clientIP).Msg("Debug endpoint access denied: IP not whitelisted")
				return echo.NewHTTPError(http.StatusForbidden, "Access denied")
			}

			return next(c)
		}
	}
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
			if token != d.config.BearerToken {
				d.logger.Warn().Str("client_ip", c.RealIP()).Msg("Debug endpoint access denied: invalid token")
				return echo.NewHTTPError(http.StatusUnauthorized, "Invalid token")
			}

			return next(c)
		}
	}
}

// newDebugResponse creates a standardized debug response
func (d *DebugHandlers) newDebugResponse(start time.Time, data interface{}, err error) *DebugResponse {
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

	info := map[string]interface{}{
		"goroutines":    runtime.NumGoroutine(),
		"go_version":    runtime.Version(),
		"go_os":         runtime.GOOS,
		"go_arch":       runtime.GOARCH,
		"num_cpu":       runtime.NumCPU(),
		"debug_config": map[string]interface{}{
			"enabled":       d.config.Enabled,
			"path_prefix":   d.config.PathPrefix,
			"auth_enabled":  d.config.BearerToken != "",
			"allowed_ips":   len(d.config.AllowedIPs),
		},
	}

	// Add app-specific info if available
	if d.app.cfg != nil {
		info["app"] = map[string]interface{}{
			"name":        d.app.cfg.App.Name,
			"environment": d.app.cfg.App.Env,
			"version":     d.app.cfg.App.Version,
		}
	}

	resp := d.newDebugResponse(start, info, nil)
	return c.JSON(http.StatusOK, resp)
}