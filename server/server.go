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
	"github.com/gaborage/go-bricks/internal/pathutil"
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

// normalizeBasePath cannot use pathutil.NormalizePrefix because that helper
// collapses "/" to "" while buildFullPath treats "/" as a meaningful state
// distinct from the empty no-prefix case. Diverging on purpose.
func normalizeBasePath(basePath string) string {
	if basePath == "" {
		return ""
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	if len(basePath) > 1 {
		basePath = strings.TrimRight(basePath, "/")
	}
	return basePath
}

func normalizeRoutePath(route, defaultRoute string) string {
	if route == "" {
		route = defaultRoute
	}
	return pathutil.EnsureLeadingSlash(route)
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
			// SECURITY: debug-gate the panic cause the same way as the unhandled-5xx
			// path — a panicking driver/downstream error can embed PII/PCI, and the
			// SensitiveDataFilter masks by field name, not message content.
			appendErrorDetail(
				log.Error().Bytes("stack", panicErr.Stack).Str("request_id", safeGetRequestID(c)),
				panicErr.Unwrap(), cfg.App.Debug,
			).Msg("Panic recovered")
		}
		customErrorHandler(c, err, cfg, log)
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

	// Setup middlewares with probe endpoint paths for tenant skipper. The OTel HTTP
	// middleware is registered only when observability is enabled (zero overhead when off).
	SetupMiddlewares(e, log, cfg, cfg.Bool("observability.enabled", false), healthPath, readyPath)

	s.RegisterReadyHandler(nil)

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

// ModuleGroup returns a route registrar with the base path applied for module route
// registration. If no base path is configured, it returns a registrar with empty prefix.
func (s *Server) ModuleGroup() RouteRegistrar {
	if s.basePath == "" || s.basePath == "/" {
		return newRouteGroup(s.echo.Group(""), "", s.cfg)
	}
	return newRouteGroup(s.echo.Group(s.basePath), s.basePath, s.cfg)
}

// RootGroup returns a route registrar rooted at the engine with NO base path applied. It
// is the registration surface for framework-internal endpoints that must sit at the URL
// root regardless of server.path.base — e.g. the debug/system endpoints. It replaces the
// former Echo() accessor for that internal need without exposing the engine.
func (s *Server) RootGroup() RouteRegistrar {
	return newRouteGroup(s.echo.Group(""), "", s.cfg)
}

// RegisterReadyHandler overrides the readiness endpoint handler with a go-bricks Handler.
// Passing nil restores the default handler. The handler is adapted to the engine once here.
func (s *Server) RegisterReadyHandler(handler Handler) {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	if handler == nil {
		s.readyHandler = s.readyCheck
	} else {
		s.readyHandler = adaptHandler(handler, s.cfg)
	}
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
	// App overrides this via RegisterReadyHandler with a probe-driven readiness check
	// (DB, messaging, etc.); see app/lifecycle.go's App.readyCheck. This handler remains
	// the fallback when no override is registered.
	return c.JSON(http.StatusOK, map[string]any{
		fieldStatus: statusReady,
		"time":      time.Now().Unix(),
	})
}

// customErrorHandler is a centralized error handler that formats errors
// into standardized APIResponse envelopes based on error type and server configuration.
// When the request context carries the raw response flag (set by handlerWrapper.wrap),
// it uses formatRawErrorResponse which writes minimal JSON without the envelope.
func customErrorHandler(c *echo.Context, err error, cfg *config.Config, log logger.Logger) {
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

	apiErr := classifyError(err, c, cfg, log)
	_ = formatter(c, apiErr, cfg)
}

// classifyError converts an arbitrary error into a structured IAPIError.
// It handles context.DeadlineExceeded, IAPIError, echo.HTTPError, and
// untyped errors, applying production sanitization and server-error logging.
func classifyError(err error, c *echo.Context, cfg *config.Config, log logger.Logger) IAPIError {
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
		// SECURITY: use the injected framework logger (not Echo's stock logger) so log
		// output is subject to the same lifecycle as the rest of the app. The
		// SensitiveDataFilter only masks by field name, not by message content, so it
		// cannot be trusted to scrub PII/PCI a driver error may embed (e.g. a
		// unique-constraint value). Mirror the response-body redaction above instead:
		// non-debug builds log the error type only, never the raw message; debug builds
		// keep full detail for troubleshooting. Str() still routes through the injected
		// filter as defense-in-depth for any field an operator has marked sensitive.
		appendErrorDetail(log.Error().Str("request_id", safeGetRequestID(c)), err, cfg.App.Debug).
			Msg("unhandled error")
	}

	code := statusToErrorCode(status)
	base := NewBaseAPIError(code, msg, status)
	// Include raw error details in development
	if cfg.App.IsDevelopment() {
		_ = base.WithDetails("error", err.Error())
	}

	return base
}

// appendErrorDetail adds debug-gated error detail to a log event: the raw error
// message only in Debug builds, the error type otherwise. SECURITY: the
// SensitiveDataFilter masks by field name, not message content, so a raw error
// string (which can embed driver-supplied PII/PCI) must never be logged in
// production. Shared by the panic-recovery and unhandled-5xx log paths.
func appendErrorDetail(event logger.LogEvent, err error, debug bool) logger.LogEvent {
	if err == nil {
		return event
	}
	if debug {
		return event.Str("error", err.Error())
	}
	return event.Str("error_type", fmt.Sprintf("%T", err))
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
