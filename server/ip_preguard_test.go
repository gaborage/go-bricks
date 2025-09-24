package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testIPPreGuardIP = "10.0.0.100"

	// Header and connection constants
	headerXRealIP        = "X-Real-IP"
	portSuffix           = ":12345"
	headerTestMiddleware = "X-Test-Middleware"
)

func TestIPPreGuard(t *testing.T) {
	tests := []struct {
		name           string
		requestsPerSec int
		requestCount   int
		expectAllowed  int
		expectBlocked  int
		sleepBetween   time.Duration
	}{
		{
			name:           "below_limit_allows_all",
			requestsPerSec: 10,
			requestCount:   5,
			expectAllowed:  5,
			expectBlocked:  0,
		},
		{
			name:           "above_limit_blocks_excess",
			requestsPerSec: 2,
			requestCount:   8,
			expectAllowed:  4, // 2 requests/sec + burst of 2
			expectBlocked:  4,
		},
		{
			name:           "disabled_allows_all",
			requestsPerSec: 0, // Disabled
			requestCount:   10,
			expectAllowed:  10,
			expectBlocked:  0,
		},
		{
			name:           "negative_disables",
			requestsPerSec: -5, // Disabled
			requestCount:   10,
			expectAllowed:  10,
			expectBlocked:  0,
		},
		{
			name:           "with_sleep_allows_refill",
			requestsPerSec: 5,
			requestCount:   3,
			expectAllowed:  3,
			expectBlocked:  0,
			sleepBetween:   100 * time.Millisecond, // Allow rate limiter to refill
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create fresh Echo instance for each test to avoid interference
			e := echo.New()
			e.Use(IPPreGuard(tt.requestsPerSec))

			e.GET("/test", func(c echo.Context) error {
				return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
			})

			allowedCount := 0
			blockedCount := 0

			for range tt.requestCount {
				req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
				// Use same IP to trigger rate limiting
				req.Header.Set(headerXRealIP, testIPPreGuardIP)
				req.RemoteAddr = testIPPreGuardIP + portSuffix
				rec := httptest.NewRecorder()

				e.ServeHTTP(rec, req)

				switch rec.Code {
				case http.StatusOK:
					allowedCount++
				case http.StatusTooManyRequests:
					blockedCount++
				}

				if tt.sleepBetween > 0 {
					time.Sleep(tt.sleepBetween)
				}
			}

			assert.Equal(t, tt.expectAllowed, allowedCount, "allowed request count mismatch")
			assert.Equal(t, tt.expectBlocked, blockedCount, "blocked request count mismatch")
		})
	}
}

func TestIPPreGuardDifferentIPs(t *testing.T) {
	e := echo.New()
	e.Use(IPPreGuard(2)) // Very low limit to trigger easily

	e.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Test requests from different IPs should each get their own rate limit bucket
	ips := []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"}

	for _, ip := range ips {
		t.Run("ip_"+ip, func(t *testing.T) {
			allowedCount := 0
			blockedCount := 0

			// Make multiple requests to test rate limiting per IP
			for range 6 {
				req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
				req.Header.Set(headerXRealIP, ip)
				req.RemoteAddr = ip + portSuffix
				rec := httptest.NewRecorder()

				e.ServeHTTP(rec, req)

				switch rec.Code {
				case http.StatusOK:
					allowedCount++
				case http.StatusTooManyRequests:
					blockedCount++
				}
			}

			// Should allow burst + initial rate (2 + 2 = 4), block the rest (2)
			assert.Equal(t, 4, allowedCount, "IP should get its own rate limit bucket")
			assert.Equal(t, 2, blockedCount, "Excess requests should be blocked")
		})
	}
}

func TestIPPreGuardErrorResponse(t *testing.T) {
	e := echo.New()
	e.Use(IPPreGuard(1)) // Very restrictive limit

	e.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Make requests to exceed rate limit
	ip := testIPPreGuardIP
	var blockedResponse *httptest.ResponseRecorder

	for range 5 {
		req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
		req.Header.Set(headerXRealIP, ip)
		req.RemoteAddr = ip + portSuffix
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		if rec.Code == http.StatusTooManyRequests {
			blockedResponse = rec
			break
		}
	}

	require.NotNil(t, blockedResponse, "Should have received an IP pre-guard rate limited response")

	// Verify error response structure
	assert.Equal(t, http.StatusTooManyRequests, blockedResponse.Code)
	assert.Contains(t, blockedResponse.Body.String(), "error")
	// Should contain IP-specific rate limit message
	responseBody := blockedResponse.Body.String()
	assert.True(t,
		strings.Contains(responseBody, "IP rate limit exceeded") || strings.Contains(responseBody, "Too many requests from this IP"),
		"Response should contain IP rate limit error message")
	assert.Contains(t, blockedResponse.Body.String(), "request_id")

	// Verify Content-Type is JSON
	assert.Contains(t, blockedResponse.Header().Get("Content-Type"), "application/json")
}

func TestIPPreGuardIntegrationWithOtherMiddleware(t *testing.T) {
	e := echo.New()

	// Test that IPPreGuard works correctly when combined with other middleware
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set(headerTestMiddleware, "present")
			return next(c)
		}
	})
	e.Use(IPPreGuard(3))

	e.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Test that requests below limit work normally
	req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
	req.Header.Set(headerXRealIP, "192.168.1.50")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "present", rec.Header().Get(headerTestMiddleware))

	// Test that rate limited requests still have middleware headers
	for range 10 {
		req = httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
		req.Header.Set(headerXRealIP, "192.168.1.50")
		rec = httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		if rec.Code == http.StatusTooManyRequests {
			// Even rate limited responses should have other middleware headers
			assert.Equal(t, "present", rec.Header().Get(headerTestMiddleware))
			return
		}
	}

	t.Fatal("Expected to receive a rate limited response")
}
