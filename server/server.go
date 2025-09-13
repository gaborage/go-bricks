// Package server provides HTTP server functionality using Echo framework.
// It includes middleware setup, routing, and request handling.
package server

import (
	"context"
	goerrors "errors"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// Server represents an HTTP server instance with Echo framework.
// It manages server lifecycle, configuration, and request handling.
type Server struct {
	echo   *echo.Echo
	cfg    *config.Config
	logger logger.Logger
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

	SetupMiddlewares(e, log, cfg)

	s := &Server{
		echo:   e,
		cfg:    cfg,
		logger: log,
	}

	// Health check
	e.GET("/health", s.healthCheck)
	e.GET("/ready", s.readyCheck)

	return s
}

// Echo returns the underlying Echo instance for route registration.
// This allows modules to register their routes with the server.
func (s *Server) Echo() *echo.Echo {
	return s.echo
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
		Addr:         addr,
		ReadTimeout:  s.cfg.Server.ReadTimeout,
		WriteTimeout: s.cfg.Server.WriteTimeout,
	}

	return s.echo.StartServer(server)
}

// Shutdown gracefully shuts down the HTTP server with the given context.
// It waits for existing connections to finish within the context timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.echo.Shutdown(ctx)
}

func (s *Server) healthCheck(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"status": "ok",
	})
}

func (s *Server) readyCheck(c echo.Context) error {
	// This will be extended in App to check DB connection
	return c.JSON(http.StatusOK, map[string]interface{}{
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
	if cfg.App.Env == "development" || cfg.App.Env == "dev" {
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
