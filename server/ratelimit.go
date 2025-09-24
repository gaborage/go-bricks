package server

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"

	"github.com/gaborage/go-bricks/multitenant"
)

const (
	BurstMultiplier  = 2
	RateLimitCleanup = time.Minute * 3
)

// RateLimit returns a rate limiting middleware with the specified requests per second.
// It limits the number of requests from each IP address to prevent abuse.
// If requestsPerSecond is 0 or negative, rate limiting is disabled.
func RateLimit(requestsPerSecond int) echo.MiddlewareFunc {
	// Disable rate limiting if requestsPerSecond is 0 or negative
	if requestsPerSecond <= 0 {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}

	config := middleware.RateLimiterConfig{
		Skipper: middleware.DefaultSkipper,
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      rate.Limit(requestsPerSecond),
				Burst:     requestsPerSecond * BurstMultiplier,
				ExpiresIn: RateLimitCleanup,
			},
		),
		IdentifierExtractor: func(ctx echo.Context) (string, error) {
			if tenantID, ok := multitenant.GetTenant(ctx.Request().Context()); ok {
				return tenantID, nil
			}
			return ctx.RealIP(), nil
		},
		ErrorHandler: func(context echo.Context, _ error) error {
			return context.JSON(http.StatusTooManyRequests, map[string]any{
				"error": map[string]any{
					"message":    "Rate limit exceeded",
					"status":     http.StatusTooManyRequests,
					"request_id": context.Response().Header().Get(echo.HeaderXRequestID),
				},
			})
		},
		DenyHandler: func(context echo.Context, _ string, _ error) error {
			return context.JSON(http.StatusTooManyRequests, map[string]any{
				"error": map[string]any{
					"message":    "Too many requests",
					"status":     http.StatusTooManyRequests,
					"request_id": context.Response().Header().Get(echo.HeaderXRequestID),
				},
			})
		},
	}

	return middleware.RateLimiterWithConfig(config)
}
