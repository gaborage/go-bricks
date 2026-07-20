package server

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	"github.com/gaborage/go-bricks/logger"
)

const (
	// IPPreGuardCleanup defines how long to keep IP buckets in memory after last use
	IPPreGuardCleanup = time.Minute * 5
)

// IPPreGuard returns an IP-based rate limiting middleware for early attack prevention.
// This middleware runs before tenant resolution to block obvious attacks and invalid tenant sprays.
// It uses a higher threshold than the main rate limiter and only protects against IP-based attacks.
// If threshold is 0 or negative, IP pre-guard is disabled.
//
// The returned MiddlewareFunc is the framework-neutral (echo-free) form; the
// echo-native logic lives in ipPreGuardEcho, which SetupMiddlewares wires directly
// on the default request path (ADR-026, no per-request baton). Constructed here
// with a nil logger — see ipPreGuardEcho for the framework-logger form.
func IPPreGuard(threshold int) MiddlewareFunc {
	return fromEchoMiddleware(ipPreGuardEcho(threshold, nil))
}

// ipPreGuardEcho is the echo-native IP pre-guard middleware constructor. Public
// callers use IPPreGuard (echo-free, nil logger); SetupMiddlewares passes its
// framework logger so rejected (429) requests leave a structured WARN trail —
// this middleware is registered outer to the access logger (server/middleware.go),
// so a short-circuited request never reaches it and would otherwise leave zero
// server-side trail when observability is disabled. l may be nil, in which case
// the warning falls back to the stdlib log package (mirrors corsWarnf).
func ipPreGuardEcho(threshold int, l logger.Logger) echo.MiddlewareFunc {
	if threshold <= 0 {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}

	config := middleware.RateLimiterConfig{
		Skipper: middleware.DefaultSkipper,
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      float64(threshold),
				Burst:     threshold * 2, // Allow small bursts
				ExpiresIn: IPPreGuardCleanup,
			},
		),
		IdentifierExtractor: func(ctx *echo.Context) (string, error) {
			// Simple IP-based limiting only
			return ctx.RealIP(), nil
		},
		ErrorHandler: func(context *echo.Context, _ error) error {
			logIPPreGuardRejection(l, context)
			return context.JSON(http.StatusTooManyRequests, map[string]any{
				fieldError: map[string]any{
					fieldMessage:   "IP rate limit exceeded",
					fieldStatus:    http.StatusTooManyRequests,
					fieldRequestID: safeGetRequestID(context),
				},
			})
		},
		DenyHandler: func(context *echo.Context, _ string, _ error) error {
			logIPPreGuardRejection(l, context)
			return context.JSON(http.StatusTooManyRequests, map[string]any{
				fieldError: map[string]any{
					fieldMessage:   "Too many requests from this IP",
					fieldStatus:    http.StatusTooManyRequests,
					fieldRequestID: safeGetRequestID(context),
				},
			})
		},
	}

	return middleware.RateLimiterWithConfig(config)
}

// logIPPreGuardRejection emits one WARN per request rejected by the IP
// pre-guard (429). This middleware is registered outer to the access logger
// and never calls next() on reject, so without this the request leaves no
// server-side trail when observability is disabled. With a framework logger
// it routes through structured WARN-level logging (SensitiveDataFilter,
// dual-mode routing); with nil (public IPPreGuard construction) it falls
// back to the stdlib log package, mirroring corsWarnf in cors.go.
func logIPPreGuardRejection(l logger.Logger, c *echo.Context) {
	req := c.Request()
	msg := fmt.Sprintf("[server.ip_preguard] request rejected by IP pre-guard method=%s path=%s client=%s status=%d",
		req.Method, req.URL.Path, c.RealIP(), http.StatusTooManyRequests)
	if l == nil {
		log.Printf("WARN %s", msg)
		return
	}
	l.Warn().Msg(msg)
}
