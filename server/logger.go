package server

import (
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	gobrickshttp "github.com/gaborage/go-bricks/http"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// Logger returns a request logging middleware using structured logging.
// It logs HTTP requests with method, URI, status, latency, and request ID.
// healthPath and readyPath specify probe endpoints that should be excluded from logging.
func Logger(log logger.Logger, healthPath, readyPath string) echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI:       true,
		LogStatus:    true,
		LogMethod:    true,
		LogLatency:   true,
		LogRequestID: true,
		LogError:     true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			// Skip probe endpoints from logs
			path := c.Path()
			if path == "" {
				path = v.URI
			}
			if path == healthPath || path == readyPath {
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
			ctx := c.Request().Context()
			if tenantID, _ := multitenant.GetTenant(ctx); tenantID != "" {
				event = event.Str("tenant", tenantID)
			}

			// Get AMQP message count and DB operation count from request context
			amqpCount := logger.GetAMQPCounter(ctx)
			dbCount := logger.GetDBCounter(ctx)
			amqpElapsed := logger.GetAMQPElapsed(ctx)
			dbElapsed := logger.GetDBElapsed(ctx)

			// Resolve the same trace ID used in API responses
			traceID := getTraceID(c)

			// SAFETY: Response may be nil after timeout, safely extract traceparent
			traceparent := ""
			if resp := c.Response(); resp != nil {
				traceparent = resp.Header().Get(gobrickshttp.HeaderTraceParent)
			}

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
				Str("ip", c.RealIP()).
				Str("user_agent", c.Request().UserAgent()).
				// Log unified trace ID (matches response meta.traceId)
				Str("trace_id", traceID).
				// Also log W3C traceparent if present for distributed tracing
				Str("traceparent", traceparent).
				Msg("Request")
			return nil
		},
	})
}
