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

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

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
	SetCaptureStackTraces(cfg.App.IsDevelopment())

	e := echo.New()
	// Use an error handler that emits standardized APIResponse envelopes.
	// Echo v5's Recover middleware wraps panics in middleware.PanicStackError;
	// we log them here with structured zerolog fields before normal error handling.
	e.HTTPErrorHandler = func(c *echo.Context, err error) {
		var panicErr *middleware.PanicStackError
		if goerrors.As(err, &panicErr) {
			log.Error().
				Err(panicErr.Unwrap()).
				Bytes("stack", panicErr.Stack).
				Str("request_id", safeGetRequestID(c)).
				Msg("Panic recovered")
		}
		customErrorHandler(c, err, cfg)
	}

	// LegacyIPExtractor restores v4-compatible IP extraction behavior for RealIP() calls.
	// Without this, v5.1.0+ only returns request.RemoteAddr.
	// Trusted-proxy-aware extractors are a follow-up item documented in ADR-015.
	e.IPExtractor = echo.LegacyIPExtractor()
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
func (s *Server) dispatchReady(c *echo.Context) error {
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

	sc := echo.StartConfig{
		Address:    addr,
		HideBanner: true,
		HidePort:   true,
		BeforeServeFunc: func(srv *http.Server) error {
			// Configure timeouts on the http.Server (StartConfig doesn't expose these)
			srv.ReadTimeout = s.cfg.Server.Timeout.Read
			srv.WriteTimeout = s.cfg.Server.Timeout.Write
			srv.IdleTimeout = s.cfg.Server.Timeout.Idle
			srv.ReadHeaderTimeout = s.cfg.Server.Timeout.Read
			// Capture the server instance for proper shutdown
			s.httpServer.Store(srv)
			return nil
		},
	}

	return sc.Start(context.Background(), s.echo)
}

// Shutdown gracefully shuts down the HTTP server with the given context.
// It waits for existing connections to finish within the context timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	// In v5, Echo no longer has a Shutdown method. Shut down the http.Server directly.
	if srv := s.httpServer.Load(); srv != nil {
		if err := srv.Shutdown(ctx); err != nil && !goerrors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}

// healthCheck is the default health probe handler.
func (s *Server) healthCheck(c *echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		fieldStatus: statusOK,
	})
}

// readyCheck is the default readiness probe handler.
func (s *Server) readyCheck(c *echo.Context) error {
	// This will be extended in App to check DB connection
	return c.JSON(http.StatusOK, map[string]any{
		fieldStatus: statusReady,
		"time":      time.Now().Unix(),
	})
}

// customErrorHandler is a centralized error handler that formats errors
// into standardized APIResponse envelopes based on error type and server configuration.
// When the request context carries the raw response flag (set by handlerWrapper.wrap),
// it uses formatRawErrorResponse which writes minimal JSON without the envelope.
func customErrorHandler(c *echo.Context, err error, cfg *config.Config) {
	// SAFETY: Prevent double-writes if error handler is invoked multiple times.
	// This can happen with certain middleware combinations (e.g., otelecho).
	// Matches Echo's default error handler behavior.
	if isResponseCommitted(c) {
		return
	}

	// Select formatter based on raw response mode (set early in handlerWrapper.wrap)
	formatter := formatErrorResponse
	if raw, ok := c.Get(rawResponseContextKey).(bool); ok && raw {
		formatter = formatRawErrorResponse
	}

	apiErr := classifyError(err, c, cfg)
	_ = formatter(c, apiErr, cfg)
}

// classifyError converts an arbitrary error into a structured IAPIError.
// It handles context.DeadlineExceeded, IAPIError, echo.HTTPError, and
// untyped errors, applying production sanitization and server-error logging.
func classifyError(err error, c *echo.Context, cfg *config.Config) IAPIError {
	// Context deadline exceeded (timeout errors)
	if goerrors.Is(err, context.DeadlineExceeded) {
		return NewServiceUnavailableError("Request processing timed out")
	}

	// Already a structured API error — use as-is
	var apiErr IAPIError
	if goerrors.As(err, &apiErr) {
		return apiErr
	}

	// Map echo.HTTPError, echo.HTTPStatusCoder, and untyped errors.
	// In v5, sentinel errors like ErrNotFound are httpError (lowercase) which
	// implements HTTPStatusCoder but NOT *HTTPError, so we check both interfaces.
	status := http.StatusInternalServerError
	msg := "Internal server error"
	var he *echo.HTTPError
	if goerrors.As(err, &he) {
		status = he.Code
		// In v5, HTTPError.Message is always a string
		if he.Message != "" {
			msg = he.Message
		}
	} else if sc := echo.StatusCode(err); sc != 0 {
		// Handles httpError sentinels (ErrNotFound, ErrMethodNotAllowed, etc.)
		status = sc
		msg = http.StatusText(sc)
	}

	// In non-debug (production) hide internal details for 500s
	if !cfg.App.Debug && status == http.StatusInternalServerError {
		msg = "An error occurred while processing your request"
	}

	if status >= http.StatusInternalServerError {
		c.Echo().Logger.Error("unhandled error", "error", err)
	}

	code := statusToErrorCode(status)
	base := NewBaseAPIError(code, msg, status)
	// Include raw error details in development
	if cfg.App.IsDevelopment() {
		_ = base.WithDetails("error", err.Error())
	}

	return base
}

// statusToErrorCode maps HTTP status codes to standardized error codes.
func statusToErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return errCodeBadRequest
	case http.StatusUnauthorized:
		return errCodeUnauthorized
	case http.StatusForbidden:
		return errCodeForbidden
	case http.StatusNotFound:
		return errCodeNotFound
	case http.StatusConflict:
		return errCodeConflict
	case http.StatusTooManyRequests:
		return errCodeTooManyRequests
	case http.StatusServiceUnavailable:
		return errCodeServiceUnavailable
	default:
		return errCodeInternalError
	}
}
