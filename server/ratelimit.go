package server

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

// RateLimit returns a rate limiting middleware with the specified requests per second.
// It limits the number of requests from each IP address to prevent abuse.
func RateLimit(requestsPerSecond int) echo.MiddlewareFunc {
	config := middleware.RateLimiterConfig{
		Skipper: middleware.DefaultSkipper,
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      rate.Limit(requestsPerSecond),
				Burst:     requestsPerSecond * 2,
				ExpiresIn: time.Minute * 3,
			},
		),
		IdentifierExtractor: func(ctx echo.Context) (string, error) {
			id := ctx.RealIP()
			return id, nil
		},
		ErrorHandler: func(context echo.Context, _ error) error {
			return context.JSON(http.StatusTooManyRequests, map[string]interface{}{
				"error": map[string]interface{}{
					"message":    "Rate limit exceeded",
					"status":     http.StatusTooManyRequests,
					"request_id": context.Response().Header().Get(echo.HeaderXRequestID),
				},
			})
		},
		DenyHandler: func(context echo.Context, _ string, _ error) error {
			return context.JSON(http.StatusTooManyRequests, map[string]interface{}{
				"error": map[string]interface{}{
					"message":    "Too many requests",
					"status":     http.StatusTooManyRequests,
					"request_id": context.Response().Header().Get(echo.HeaderXRequestID),
				},
			})
		},
	}

	return middleware.RateLimiterWithConfig(config)
}
