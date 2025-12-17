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
	testIP = "192.168.1.100"
)

func TestRateLimit(t *testing.T) {
	tests := []struct {
		name           string
		requestsPerSec int
		requestCount   int
		expectAllowed  int
		expectBlocked  int
		sleepBetween   time.Duration
	}{
		{
			name:           "requests_within_limit",
			requestsPerSec: 10,
			requestCount:   5,
			expectAllowed:  5,
			expectBlocked:  0,
			sleepBetween:   0,
		},
		{
			name:           "requests_exceed_burst",
			requestsPerSec: 2,
			requestCount:   10,
			expectAllowed:  4, // burst capacity
			expectBlocked:  6,
			sleepBetween:   0,
		},
		{
			name:           "requests_with_delay_allowed",
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
			e.Use(RateLimit(tt.requestsPerSec))

			e.GET("/test", func(c echo.Context) error {
				return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
			})

			allowedCount := 0
			blockedCount := 0

			for i := 0; i < tt.requestCount; i++ {
				req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
				// Use same IP to trigger rate limiting
				req.Header.Set(HeaderXRealIP, "192.168.1.100")
				req.RemoteAddr = "192.168.1.100:12345"
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

			assert.Equal(t, tt.expectAllowed, allowedCount, "Unexpected number of allowed requests")
			assert.Equal(t, tt.expectBlocked, blockedCount, "Unexpected number of blocked requests")
		})
	}
}

func TestRateLimitDifferentIPs(t *testing.T) {
	e := echo.New()
	e.Use(RateLimit(2)) // Very low limit to trigger easily

	e.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Test requests from different IPs should each get their own rate limit bucket
	ips := []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"}

	for _, ip := range ips {
		t.Run("ip_"+ip, func(t *testing.T) {
			allowedCount := 0
			blockedCount := 0

			// Each IP should be able to make burst requests (2 * 2 = 4)
			for i := 0; i < 6; i++ {
				req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
				req.Header.Set(HeaderXRealIP, ip)
				req.RemoteAddr = ip + ":12345"
				rec := httptest.NewRecorder()

				e.ServeHTTP(rec, req)

				switch rec.Code {
				case http.StatusOK:
					allowedCount++
				case http.StatusTooManyRequests:
					blockedCount++
				}
			}

			// Each IP should get some allowed requests (at least burst)
			assert.GreaterOrEqual(t, allowedCount, 4, "Each IP should get at least burst capacity")
			assert.Greater(t, blockedCount, 0, "Some requests should be blocked")
		})
	}
}

func TestRateLimitErrorResponse(t *testing.T) {
	e := echo.New()
	e.Use(RateLimit(1)) // Very restrictive limit

	e.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Make requests to exceed rate limit
	ip := testIP
	var blockedResponse *httptest.ResponseRecorder

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
		req.Header.Set(HeaderXRealIP, ip)
		req.RemoteAddr = ip + ":12345"
		rec := httptest.NewRecorder()

		e.ServeHTTP(rec, req)

		if rec.Code == http.StatusTooManyRequests {
			blockedResponse = rec
			break
		}
	}

	require.NotNil(t, blockedResponse, "Should have received a rate limited response")

	// Verify error response structure
	assert.Equal(t, http.StatusTooManyRequests, blockedResponse.Code)
	assert.Contains(t, blockedResponse.Body.String(), "error")
	// Could be either "Rate limit exceeded" or "Too many requests" depending on which handler is called
	responseBody := blockedResponse.Body.String()
	assert.True(t,
		strings.Contains(responseBody, "Rate limit exceeded") || strings.Contains(responseBody, "Too many requests"),
		"Response should contain rate limit error message")
	assert.Contains(t, blockedResponse.Body.String(), "request_id")

	// Verify Content-Type is JSON
	assert.Contains(t, blockedResponse.Header().Get("Content-Type"), "application/json")
}

func TestRateLimitIPExtraction(t *testing.T) {
	e := echo.New()
	e.Use(RateLimit(2))

	e.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	tests := []struct {
		name     string
		setupReq func(*http.Request)
	}{
		{
			name: "x_real_ip_header",
			setupReq: func(req *http.Request) {
				req.Header.Set(HeaderXRealIP, "203.0.113.1")
				req.RemoteAddr = "192.168.1.1:8080"
			},
		},
		{
			name: "x_forwarded_for_header",
			setupReq: func(req *http.Request) {
				req.Header.Set(HeaderXForwardedFor, "203.0.113.2, 192.168.1.1")
				req.RemoteAddr = "192.168.1.1:8080"
			},
		},
		{
			name: "remote_addr_fallback",
			setupReq: func(req *http.Request) {
				req.RemoteAddr = "203.0.113.3:8080"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowedCount := 0
			blockedCount := 0

			// Make multiple requests to test rate limiting per IP
			for i := 0; i < 6; i++ {
				req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
				tt.setupReq(req)
				rec := httptest.NewRecorder()

				e.ServeHTTP(rec, req)

				switch rec.Code {
				case http.StatusOK:
					allowedCount++
				case http.StatusTooManyRequests:
					blockedCount++
				}
			}

			// Should have some allowed and some blocked requests
			assert.Greater(t, allowedCount, 0, "Should have some allowed requests")
			assert.Greater(t, blockedCount, 0, "Should have some blocked requests")
		})
	}
}

func TestRateLimitDisabled(t *testing.T) {
	tests := []struct {
		name           string
		requestCount   int
		expectedStatus int
		trackCalls     bool
	}{
		{
			name:           "handler_invoked_for_each_request",
			requestCount:   5,
			expectedStatus: http.StatusNoContent,
			trackCalls:     true,
		},
		{
			name:           "responses_allow_when_disabled",
			requestCount:   50,
			expectedStatus: http.StatusOK,
			trackCalls:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			e.Use(RateLimit(0))

			callCount := 0
			handler := func(c echo.Context) error {
				if tt.trackCalls {
					callCount++
					return c.NoContent(tt.expectedStatus)
				}
				return c.JSON(tt.expectedStatus, map[string]string{"status": "ok"})
			}

			e.GET("/test", handler)

			for i := 0; i < tt.requestCount; i++ {
				req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
				req.Header.Set(HeaderXRealIP, testIP)
				req.RemoteAddr = testIP + ":12345"
				rec := httptest.NewRecorder()

				e.ServeHTTP(rec, req)

				require.Equalf(t, tt.expectedStatus, rec.Code,
					"request %d should return status %d when rate limiting is disabled", i+1, tt.expectedStatus)
			}

			if tt.trackCalls {
				assert.Equal(t, tt.requestCount, callCount)
			}
		})
	}
}

func TestRateLimitReset(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping rate limit reset test in short mode")
	}

	e := echo.New()
	e.Use(RateLimit(2)) // 2 requests per second

	e.GET("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	ip := testIP

	// Exhaust rate limit
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
		req.Header.Set(HeaderXRealIP, ip)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
	}

	// Last request should be blocked
	req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
	req.Header.Set(HeaderXRealIP, ip)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	// Wait for rate limiter to reset (slightly more than 1 second)
	time.Sleep(1100 * time.Millisecond)

	// Request should be allowed again
	req = httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
	req.Header.Set(HeaderXRealIP, ip)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}
