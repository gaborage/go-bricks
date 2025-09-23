package server

import (
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// SetupMiddlewares configures and registers all HTTP middlewares for the Echo server.
// It sets up CORS, logging, recovery, security headers, rate limiting, and other essential middleware.
func SetupMiddlewares(e *echo.Echo, log logger.Logger, cfg *config.Config) {
	// Request ID
	e.Use(middleware.RequestID())

	// Inject trace context into request context for outbound propagation
	e.Use(TraceContext())

	// Operation Tracker - Initialize AMQP and DB operation tracking for each request
	e.Use(PerformanceStats())

	// CORS
	e.Use(CORS())

	// Logger middleware with zerolog
	e.Use(Logger(log))

	// Recovery
	e.Use(middleware.RecoverWithConfig(middleware.RecoverConfig{
		LogErrorFunc: func(c echo.Context, err error, stack []byte) error {
			log.Error().
				Err(err).
				Str("request_id", c.Response().Header().Get(echo.HeaderXRequestID)).
				Bytes("stack", stack).
				Msg("Panic recovered")
			return err
		},
	}))

	// Security headers
	e.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		XSSProtection:         "1; mode=block",
		ContentTypeNosniff:    "nosniff",
		XFrameOptions:         "SAMEORIGIN",
		HSTSMaxAge:            3600,
		ContentSecurityPolicy: "default-src 'self'",
	}))

	// Body limit
	e.Use(middleware.BodyLimit("10M"))

	// Timeout
	e.Use(middleware.TimeoutWithConfig(middleware.TimeoutConfig{
		Timeout: cfg.Server.Timeout.Middleware,
	}))

	// Gzip
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{
		Level: 5,
	}))

	// Rate limit
	e.Use(RateLimit(cfg.App.Rate.Limit))

	// Timing
	e.Use(Timing())
}
