package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
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
			e.Use(rateLimitEcho(tt.requestsPerSec))

			e.GET("/test", func(c *echo.Context) error {
				return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
			})

			allowedCount := 0
			blockedCount := 0

			for i := 0; i < tt.requestCount; i++ {
				req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
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
	e.Use(rateLimitEcho(2)) // Very low limit to trigger easily

	e.GET("/test", func(c *echo.Context) error {
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
				req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
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
			assert.Positive(t, blockedCount, "Some requests should be blocked")
		})
	}
}

func TestRateLimitErrorResponse(t *testing.T) {
	e := echo.New()
	e.Use(rateLimitEcho(1)) // Very restrictive limit

	e.GET("/test", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Make requests to exceed rate limit
	ip := testIP
	var blockedResponse *httptest.ResponseRecorder

	for i := 0; i < 5; i++ {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
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

// TestRateLimitIPExtraction runs the limiter behind the extractor New() actually
// installs, and asserts the address each case is keyed by — not only that some
// requests were throttled. The allowed/blocked counts alone hold for any
// extractor, so without the keyed-identity assertion this test would pass
// unchanged if the engine reverted to trusting raw X-Forwarded-For.
func TestRateLimitIPExtraction(t *testing.T) {
	srv := newIPExtractorServer()
	srv.echo.Use(rateLimitEcho(2))

	var keyedIP string
	srv.echo.GET("/test", func(c *echo.Context) error {
		keyedIP = c.RealIP()
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	tests := []struct {
		name       string
		setupReq   func(*http.Request)
		expectedIP string
	}{
		{
			// X-Real-IP is not honored at all, so it cannot move the key off
			// the peer that actually opened the connection.
			name: "x_real_ip_header_does_not_move_key_off_peer",
			setupReq: func(req *http.Request) {
				req.Header.Set(HeaderXRealIP, "203.0.113.1")
				req.RemoteAddr = "192.168.1.1:8080"
			},
			expectedIP: "192.168.1.1",
		},
		{
			// The peer is a private-range proxy, so the client address it
			// relayed is trusted and becomes the key.
			name: "x_forwarded_for_header_via_trusted_private_peer",
			setupReq: func(req *http.Request) {
				req.Header.Set(HeaderXForwardedFor, "203.0.113.2, 192.168.1.1")
				req.RemoteAddr = "192.168.1.1:8080"
			},
			expectedIP: "203.0.113.2",
		},
		{
			name: "remote_addr_fallback",
			setupReq: func(req *http.Request) {
				req.RemoteAddr = "203.0.113.3:8080"
			},
			expectedIP: "203.0.113.3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyedIP = ""
			allowedCount := 0
			blockedCount := 0

			// Make multiple requests to test rate limiting per IP
			for i := 0; i < 6; i++ {
				req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
				tt.setupReq(req)
				rec := httptest.NewRecorder()

				srv.echo.ServeHTTP(rec, req)

				switch rec.Code {
				case http.StatusOK:
					allowedCount++
				case http.StatusTooManyRequests:
					blockedCount++
				}
			}

			// Should have some allowed and some blocked requests
			assert.Positive(t, allowedCount, "Should have some allowed requests")
			assert.Positive(t, blockedCount, "Should have some blocked requests")

			// The bucket identity itself — the assertion that fails if the
			// engine stops deriving the client IP through trusted proxies.
			assert.Equal(t, tt.expectedIP, keyedIP, "rate limit must be keyed by the trusted-proxy-derived client IP")
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
			e.Use(rateLimitEcho(0))

			callCount := 0
			handler := func(c *echo.Context) error {
				if tt.trackCalls {
					callCount++
					return c.NoContent(tt.expectedStatus)
				}
				return c.JSON(tt.expectedStatus, map[string]string{"status": "ok"})
			}

			e.GET("/test", handler)

			for i := 0; i < tt.requestCount; i++ {
				req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
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
	e.Use(rateLimitEcho(2)) // 2 requests per second

	e.GET("/test", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	ip := testIP

	// Exhaust rate limit
	for i := 0; i < 6; i++ {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
		req.Header.Set(HeaderXRealIP, ip)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
	}

	// Last request should be blocked
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
	req.Header.Set(HeaderXRealIP, ip)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	// Wait for rate limiter to reset (slightly more than 1 second)
	time.Sleep(1100 * time.Millisecond)

	// Request should be allowed again
	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
	req.Header.Set(HeaderXRealIP, ip)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

const (
	limitedPath     = "/limited"
	clientAAddr     = "198.51.100.7:443"
	clientBAddr     = "198.51.100.8:443"
	authorizedToken = "Bearer test"
)

// newLimitedGroup builds a server whose root chain resolves the tenant from
// X-Tenant-ID (when tenanted) and runs the built-in limiter at limit rps, and
// registers limitedPath under a group at prefix with mw. It returns the route's path.
func newLimitedGroup(limit int, tenanted bool, prefix string, mw ...MiddlewareFunc) (srv *Server, path string) {
	cfg := newTestConfig("", "", "")
	cfg.App.Rate.Limit = limit
	cfg.Multitenant = config.MultitenantConfig{
		Enabled:  tenanted,
		Resolver: config.ResolverConfig{Type: config.ResolverTypeHeader},
	}
	srv = New(cfg, &testLogger{})
	srv.ModuleGroup().Group(prefix, mw...).Add(http.MethodGet, limitedPath, func(c HandlerContext) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	return srv, prefix + limitedPath
}

func serveLimited(srv *Server, path, remoteAddr, tenant string, authenticated bool) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	req.RemoteAddr = remoteAddr
	if tenant != "" {
		req.Header.Set(HeaderXTenantID, tenant)
	}
	if authenticated {
		req.Header.Set(echo.HeaderAuthorization, authorizedToken)
	}
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)
	return rec
}

func codesAcrossTenants(srv *Server, path, remoteAddr string, tenants ...string) []int {
	codes := make([]int, 0, len(tenants))
	for _, tenant := range tenants {
		codes = append(codes, serveLimited(srv, path, remoteAddr, tenant, false).Code)
	}
	return codes
}

func TestRateLimitKeysOnClientIPWhenTenantRotates(t *testing.T) {
	srv, path := newLimitedGroup(1, true, "")

	assert.Equal(t, []int{
		http.StatusOK, http.StatusOK,
		http.StatusTooManyRequests, http.StatusTooManyRequests, http.StatusTooManyRequests, http.StatusTooManyRequests,
	}, codesAcrossTenants(srv, path, clientAAddr, "r1", "r2", "r3", "r4", "r5", "r6"),
		"rotating the tenant must not buy one address a fresh bucket")
}

func TestRateLimitTenantBudgetNotSharedAcrossClientIPs(t *testing.T) {
	srv, path := newLimitedGroup(1, true, "")

	for range 2 {
		require.Equal(t, http.StatusOK, serveLimited(srv, path, clientAAddr, "acme", false).Code)
	}
	require.Equal(t, http.StatusTooManyRequests, serveLimited(srv, path, clientAAddr, "acme", false).Code)

	rec := serveLimited(srv, path, clientBAddr, "acme", false)
	assert.Equal(t, http.StatusOK, rec.Code, "client A spending acme must not spend client B's budget")
	assert.Equal(t, "1", rec.Header().Get("X-RateLimit-Remaining"), "B's headers describe only B's own address bucket")
}

func TestRateLimitUnauthenticatedFloodSparesOtherClients(t *testing.T) {
	srv, path := newLimitedGroup(1, true, "/secure", blockUnlessAuthorized)

	for range 5 {
		code := serveLimited(srv, path, clientAAddr, "x", false).Code
		require.Contains(t, []int{http.StatusUnauthorized, http.StatusTooManyRequests}, code)
	}

	assert.Equal(t, http.StatusOK, serveLimited(srv, path, clientBAddr, "x", true).Code,
		"an unauthenticated flood naming x must not hold x at 429 for other addresses")
}

func TestRateLimitPublicKeysOnClientIP(t *testing.T) {
	srv, path := newLimitedGroup(0, true, "/public", RateLimit(1))

	assert.Equal(t, []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests, http.StatusTooManyRequests},
		codesAcrossTenants(srv, path, clientAAddr, "r1", "r2", "r3", "r4"),
		"server.RateLimit keys on client IP like the built-in limiter")
}

func TestTenantRateLimitAfterAuthChargesOnlyAuthenticated(t *testing.T) {
	srv, path := newLimitedGroup(0, true, "/tenant", blockUnlessAuthorized, TenantRateLimit(1))

	for range 3 {
		rec := serveLimited(srv, path, clientAAddr, "acme", false)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		require.Empty(t, rec.Header().Get("X-RateLimit-Limit"), "auth rejects before the tenant bucket is consulted")
	}

	codes := make([]int, 0, 3)
	for range 3 {
		codes = append(codes, serveLimited(srv, path, clientAAddr, "acme", true).Code)
	}
	assert.Equal(t, []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests}, codes,
		"unauthenticated requests must leave acme's full burst for authenticated ones")
}

func TestTenantRateLimitSharesTenantBudgetAcrossClientIPs(t *testing.T) {
	srv, path := newLimitedGroup(0, true, "/tenant", blockUnlessAuthorized, TenantRateLimit(1))

	assert.Equal(t, http.StatusOK, serveLimited(srv, path, clientAAddr, "acme", true).Code)
	assert.Equal(t, http.StatusOK, serveLimited(srv, path, clientBAddr, "acme", true).Code)
	assert.Equal(t, http.StatusTooManyRequests, serveLimited(srv, path, clientAAddr, "acme", true).Code,
		"two addresses under one tenant share that tenant's budget")
	assert.Equal(t, http.StatusOK, serveLimited(srv, path, clientAAddr, "other", true).Code,
		"another tenant from the same address keeps its own budget")
}

func TestTenantRateLimitPassesThrough(t *testing.T) {
	tests := []struct {
		name     string
		tenanted bool
		tenant   string
		rps      int
	}{
		{name: "without_tenant", rps: 1},
		{name: "disabled", tenanted: true, tenant: "acme", rps: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, path := newLimitedGroup(0, tt.tenanted, "/tenant", TenantRateLimit(tt.rps))

			for i := range 5 {
				rec := serveLimited(srv, path, clientAAddr, tt.tenant, false)
				require.Equalf(t, http.StatusOK, rec.Code, "request %d must pass through", i+1)
				require.Emptyf(t, rec.Header().Get("X-RateLimit-Limit"), "request %d charges no bucket", i+1)
			}
		})
	}
}
