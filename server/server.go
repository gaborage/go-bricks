// Package server provides HTTP server functionality using Echo framework.
// It includes middleware setup, routing, and request handling.
package server

import (
	"context"
	goerrors "errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// Server represents an HTTP server instance with Echo framework.
// It manages server lifecycle, configuration, and request handling.
type Server struct {
	echo         *echo.Echo
	httpServer   atomic.Pointer[http.Server] // Store the actual http.Server instance for proper shutdown with race-free access
	cfg          *config.Config
	logger       logger.Logger
	basePath     string
	healthRoute  string
	readyRoute   string
	readyMu      sync.RWMutex
	readyHandler echo.HandlerFunc
}

// normalizeBasePath ensures the base path starts with "/" and doesn't end with "/"
// unless it's the root path. Empty string is returned as-is (no prefix).
func normalizeBasePath(basePath string) string {
	if basePath == "" {
		return ""
	}

	// Ensure it starts with "/"
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}

	// Remove trailing "/" unless it's just "/"
	if len(basePath) > 1 {
		basePath = strings.TrimRight(basePath, "/")
	}

	return basePath
}

// normalizeRoutePath ensures a route path starts with "/" and handles empty paths
func normalizeRoutePath(route, defaultRoute string) string {
	if route == "" {
		route = defaultRoute
	}

	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}

	return route
}

// buildFullPath combines base path with route path
func (s *Server) buildFullPath(route string) string {
	if s.basePath == "" || s.basePath == "/" {
		return route
	}

	// If route is just "/", don't append it to avoid double slashes
	if route == "/" {
		return s.basePath
	}

	return s.basePath + route
}

// New creates a new HTTP server instance with the given configuration and logger.
// It initializes Echo with middlewares, error handling, and health check endpoints.
func New(cfg *config.Config, log logger.Logger) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	// Use an error handler that emits standardized APIResponse envelopes
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		customErrorHandler(err, c, cfg)
	}
	if v := NewValidator(); v != nil {
		e.Validator = v
	} else {
		log.Fatal().Msg("failed to initialize request validator")
	}

	// Initialize server with path configuration
	basePath := normalizeBasePath(cfg.Server.Path.Base)
	healthRoute := normalizeRoutePath(cfg.Server.Path.Health, "/health")
	readyRoute := normalizeRoutePath(cfg.Server.Path.Ready, "/ready")

	s := &Server{
		echo:         e,
		cfg:          cfg,
		logger:       log,
		basePath:     basePath,
		healthRoute:  healthRoute,
		readyRoute:   readyRoute,
		readyHandler: nil,
	}

	// Compute full paths for probe endpoints before middleware setup
	healthPath := s.buildFullPath(healthRoute)
	readyPath := s.buildFullPath(readyRoute)

	// Setup middlewares with probe endpoint paths for tenant skipper
	SetupMiddlewares(e, log, cfg, healthPath, readyPath)

	s.RegisterReadyHandler(s.readyCheck)

	e.GET(healthPath, s.healthCheck)
	e.HEAD(healthPath, s.healthCheck)
	e.GET(readyPath, s.dispatchReady)
	e.HEAD(readyPath, s.dispatchReady)

	log.Debug().
		Str("base_path", basePath).
		Str("health_path", healthPath).
		Str("ready_path", readyPath).
		Msg("Server paths configured")

	return s
}

// Echo returns the underlying Echo instance for route registration.
// This allows modules to register their routes with the server.
func (s *Server) Echo() *echo.Echo {
	return s.echo
}

// ModuleGroup returns an Echo group with the base path applied for module route registration.
// If no base path is configured, it returns a group with empty prefix.
func (s *Server) ModuleGroup() RouteRegistrar {
	if s.basePath == "" || s.basePath == "/" {
		return newRouteGroup(s.echo.Group(""), "")
	}
	return newRouteGroup(s.echo.Group(s.basePath), s.basePath)
}

// RegisterReadyHandler overrides the ready endpoint handler.
// Passing nil restores the default handler.
func (s *Server) RegisterReadyHandler(handler echo.HandlerFunc) {
	if handler == nil {
		handler = s.readyCheck
	}
	s.readyMu.Lock()
	s.readyHandler = handler
	s.readyMu.Unlock()
}

// dispatchReady executes the currently registered ready handler.
func (s *Server) dispatchReady(c echo.Context) error {
	s.readyMu.RLock()
	handler := s.readyHandler
	s.readyMu.RUnlock()
	return handler(c)
}

// Start starts the HTTP server and begins accepting requests.
// It blocks until the server is shut down or encounters an error.
func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)

	s.logger.Info().
		Str("service", s.cfg.App.Name).
		Str("version", s.cfg.App.Version).
		Str("env", s.cfg.App.Env).
		Str("port", fmt.Sprint(s.cfg.Server.Port)).
		Str("address", addr).
		Msg("Starting server...")

	server := &http.Server{
		Addr:              addr,
		ReadTimeout:       s.cfg.Server.Timeout.Read,
		WriteTimeout:      s.cfg.Server.Timeout.Write,
		IdleTimeout:       s.cfg.Server.Timeout.Idle,
		ReadHeaderTimeout: s.cfg.Server.Timeout.Read,
	}

	// Store the server instance for proper shutdown using atomic operation
	s.httpServer.Store(server)

	return s.echo.StartServer(server)
}

// Shutdown gracefully shuts down the HTTP server with the given context.
// It waits for existing connections to finish within the context timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	// First try Echo's shutdown (for compatibility)
	err := s.echo.Shutdown(ctx)

	// Then ensure our actual http.Server instance is shut down using atomic load
	// This fixes the issue where Echo's StartServer doesn't store the server properly
	if srv := s.httpServer.Load(); srv != nil {
		if httpErr := srv.Shutdown(ctx); httpErr != nil && !goerrors.Is(httpErr, http.ErrServerClosed) {
			return httpErr
		}
	}
	if goerrors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) healthCheck(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"status": "ok",
	})
}

func (s *Server) readyCheck(c echo.Context) error {
	// This will be extended in App to check DB connection
	return c.JSON(http.StatusOK, map[string]any{
		"status": "ready",
		"time":   time.Now().Unix(),
	})
}

func customErrorHandler(err error, c echo.Context, cfg *config.Config) {
	// If this is a structured API error, reuse its fields
	var apiErr IAPIError
	if goerrors.As(err, &apiErr) {
		_ = formatErrorResponse(c, apiErr, cfg)
		return
	}

	// Map echo.HTTPError and untyped errors to standardized envelope
	status := http.StatusInternalServerError
	msg := "Internal server error"
	var he *echo.HTTPError
	if goerrors.As(err, &he) {
		status = he.Code
		switch m := he.Message.(type) {
		case string:
			msg = m
		case error:
			msg = m.Error()
		default:
			// keep default
		}
	}

	// In non-debug (production) hide internal details for 500s
	if !cfg.App.Debug && status == http.StatusInternalServerError {
		msg = "An error occurred while processing your request"
	}

	if status >= http.StatusInternalServerError {
		c.Echo().Logger.Errorf("unhandled error: %v", err)
	}

	code := statusToErrorCode(status)
	base := NewBaseAPIError(code, msg, status)
	// Include raw error details in development
	if isDevelopmentEnv(cfg.App.Env) {
		_ = base.WithDetails("error", err.Error())
	}

	_ = formatErrorResponse(c, base, cfg)
}

func statusToErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "BAD_REQUEST"
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusTooManyRequests:
		return "TOO_MANY_REQUESTS"
	case http.StatusServiceUnavailable:
		return "SERVICE_UNAVAILABLE"
	default:
		return "INTERNAL_ERROR"
	}
}
