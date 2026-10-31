package server

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	"github.com/gaborage/go-bricks/multitenant"
)

const (
	BurstMultiplier  = 2
	RateLimitCleanup = time.Minute * 3
)

// RateLimit returns a rate limiting middleware with the specified requests per second.
// It keys on the client IP alone, as the built-in limiter does, whether or not a tenant
// resolved (ADR-160); the IP is derived through server.trustedproxies (ADR-057).
// If requestsPerSecond is 0 or negative, rate limiting is disabled.
//
// The returned MiddlewareFunc is the framework-neutral (echo-free) form; the
// echo-native logic lives in rateLimitEcho, which SetupMiddlewares wires directly
// on the default request path (ADR-026, no per-request baton).
func RateLimit(requestsPerSecond int) MiddlewareFunc {
	return fromEchoMiddleware(rateLimitEcho(requestsPerSecond))
}

// TenantRateLimit returns a rate limiting middleware keyed on the tenant in the
// request context, with RateLimit's rate and burst derivation (ADR-160). A request
// with no tenant passes through without charging any bucket. If requestsPerSecond is
// 0 or negative, it is a pass-through. The framework never installs it.
//
// The tenant is caller-written, so attach it in the same RouteRegistrar.Group call as
// auth.Middleware, listed after it, and after whatever check ties the resolved tenant
// to the caller: auth.Middleware verifies the credential, not the tenant. Its
// X-RateLimit-* headers overwrite the built-in limiter's, so a response's headers
// describe the innermost limiter that ran.
func TenantRateLimit(requestsPerSecond int) MiddlewareFunc {
	return fromEchoMiddleware(rateLimiterEcho(requestsPerSecond, withoutTenant, contextTenant))
}

// rateLimitEcho is the echo-native rate-limiting middleware constructor. Public
// callers use RateLimit (echo-free); SetupMiddlewares uses this form to keep the
// default chain baton-free.
func rateLimitEcho(requestsPerSecond int) echo.MiddlewareFunc {
	return rateLimiterEcho(requestsPerSecond, middleware.DefaultSkipper, (*echo.Context).RealIP)
}

func withoutTenant(ctx *echo.Context) bool {
	_, ok := multitenant.GetTenant(ctx.Request().Context())
	return !ok
}

func contextTenant(ctx *echo.Context) string {
	tenantID, _ := multitenant.GetTenant(ctx.Request().Context())
	return tenantID
}

func rateLimiterEcho(requestsPerSecond int, skipper middleware.Skipper, key func(*echo.Context) string) echo.MiddlewareFunc {
	if requestsPerSecond <= 0 {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return next
		}
	}

	config := middleware.RateLimiterConfig{
		Skipper: skipper,
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      float64(requestsPerSecond),
				Burst:     requestsPerSecond * BurstMultiplier,
				ExpiresIn: RateLimitCleanup,
			},
		),
		IdentifierExtractor: func(ctx *echo.Context) (string, error) {
			return key(ctx), nil
		},
		ErrorHandler: func(context *echo.Context, _ error) error {
			return context.JSON(http.StatusTooManyRequests, map[string]any{
				fieldError: map[string]any{
					fieldMessage:   msgRateLimitExceeded,
					fieldStatus:    http.StatusTooManyRequests,
					fieldRequestID: safeGetRequestID(context),
				},
			})
		},
		DenyHandler: func(context *echo.Context, _ string, _ error) error {
			return context.JSON(http.StatusTooManyRequests, map[string]any{
				fieldError: map[string]any{
					fieldMessage:   "Too many requests",
					fieldStatus:    http.StatusTooManyRequests,
					fieldRequestID: safeGetRequestID(context),
				},
			})
		},
	}

	return middleware.RateLimiterWithConfig(config)
}
