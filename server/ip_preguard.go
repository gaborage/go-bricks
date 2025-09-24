package server

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

const (
	// IPPreGuardCleanup defines how long to keep IP buckets in memory after last use
	IPPreGuardCleanup = time.Minute * 5
)

// IPPreGuard returns an IP-based rate limiting middleware for early attack prevention.
// This middleware runs before tenant resolution to block obvious attacks and invalid tenant sprays.
// It uses a higher threshold than the main rate limiter and only protects against IP-based attacks.
// If threshold is 0 or negative, IP pre-guard is disabled.
func IPPreGuard(threshold int) echo.MiddlewareFunc {
	// Disable IP pre-guard if threshold is 0 or negative
	if threshold <= 0 {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}

	config := middleware.RateLimiterConfig{
		Skipper: middleware.DefaultSkipper,
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      rate.Limit(threshold),
				Burst:     threshold * 2, // Allow small bursts
				ExpiresIn: IPPreGuardCleanup,
			},
		),
		IdentifierExtractor: func(ctx echo.Context) (string, error) {
			// Simple IP-based limiting only
			return ctx.RealIP(), nil
		},
		ErrorHandler: func(context echo.Context, _ error) error {
			return context.JSON(http.StatusTooManyRequests, map[string]any{
				"error": map[string]any{
					"message":    "IP rate limit exceeded",
					"status":     http.StatusTooManyRequests,
					"request_id": context.Response().Header().Get(echo.HeaderXRequestID),
				},
			})
		},
		DenyHandler: func(context echo.Context, _ string, _ error) error {
			return context.JSON(http.StatusTooManyRequests, map[string]any{
				"error": map[string]any{
					"message":    "Too many requests from this IP",
					"status":     http.StatusTooManyRequests,
					"request_id": context.Response().Header().Get(echo.HeaderXRequestID),
				},
			})
		},
	}

	return middleware.RateLimiterWithConfig(config)
}
