package server

import (
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	gobrickshttp "github.com/gaborage/go-bricks/http"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// Logger returns a request logging middleware using structured logging.
// It logs HTTP requests with method, URI, status, latency, and request ID.
func Logger(log logger.Logger) echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI:       true,
		LogStatus:    true,
		LogMethod:    true,
		LogLatency:   true,
		LogRequestID: true,
		LogError:     true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			// Skip health checks en los logs
			path := c.Path()
			if path == "" {
				path = v.URI
			}
			if strings.HasSuffix(path, "/health") || strings.HasSuffix(path, "/ready") {
				return nil
			}

			event := log.Info()
			resultCode := "OK"

			if v.Status >= 500 {
				event = log.Error()
				resultCode = "ERROR"
			} else if v.Status >= 400 {
				event = log.Warn()
				resultCode = "WARN"
			}

			if v.Error != nil && v.Status >= 500 {
				event = event.Err(v.Error)
				resultCode = "ERROR"
			}

			// Tenant context
			tenantID, _ := multitenant.GetTenant(c.Request().Context())

			// Get AMQP message count and DB operation count from request context
			amqpCount := logger.GetAMQPCounter(c.Request().Context())
			dbCount := logger.GetDBCounter(c.Request().Context())
			amqpElapsed := logger.GetAMQPElapsed(c.Request().Context())
			dbElapsed := logger.GetDBElapsed(c.Request().Context())

			// Resolve the same trace ID used in API responses
			traceID := getTraceID(c)

			event.
				Str("request_id", v.RequestID).
				Str("method", v.Method).
				Str("uri", v.URI).
				Str("result_code", resultCode).
				Int("status", v.Status).
				Int64("http_elapsed", v.Latency.Nanoseconds()).
				Int64("amqp_published", amqpCount).
				Int64("amqp_elapsed", amqpElapsed).
				Int64("db_queries", dbCount).
				Int64("db_elapsed", dbElapsed).
				Str("tenant_id", tenantID).
				Str("ip", c.RealIP()).
				Str("user_agent", c.Request().UserAgent()).
				// Log unified trace ID (matches response meta.traceId)
				Str("trace_id", traceID).
				// Also log W3C traceparent if present for distributed tracing
				Str("traceparent", c.Response().Header().Get(gobrickshttp.HeaderTraceParent)).
				Msg("Request")
			return nil
		},
	})
}
