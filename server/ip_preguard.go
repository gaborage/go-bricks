package server

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
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
// on the default request path (ADR-026, no per-request baton).
func IPPreGuard(threshold int) MiddlewareFunc {
	return fromEchoMiddleware(ipPreGuardEcho(threshold))
}

// ipPreGuardEcho is the echo-native IP pre-guard middleware constructor. Public
// callers use IPPreGuard (echo-free); SetupMiddlewares uses this form to keep the
// default chain baton-free.
func ipPreGuardEcho(threshold int) echo.MiddlewareFunc {
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
			return context.JSON(http.StatusTooManyRequests, map[string]any{
				fieldError: map[string]any{
					fieldMessage:   "IP rate limit exceeded",
					fieldStatus:    http.StatusTooManyRequests,
					fieldRequestID: safeGetRequestID(context),
				},
			})
		},
		DenyHandler: func(context *echo.Context, _ string, _ error) error {
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
