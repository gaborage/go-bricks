package scheduler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

const (
	testLocalhostAddr  = "127.0.0.1:12345"
	testPrivateNetCIDR = "192.168.1.0/24"
	testPrivateAddr    = "10.0.0.1:12345"
)

// TestCIDRMiddlewareLocalhostOnly verifies localhost-only access when allowlist is empty
func TestCIDRMiddlewareLocalhostOnly(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		expectCode int
	}{
		{
			name:       "allows localhost IPv4",
			remoteAddr: testLocalhostAddr,
			expectCode: http.StatusOK,
		},
		{
			name:       "allows localhost IPv6",
			remoteAddr: "[::1]:12345",
			expectCode: http.StatusOK,
		},
		{
			name:       "blocks external IP",
			remoteAddr: "192.168.1.100:12345",
			expectCode: http.StatusForbidden,
		},
		{
			name:       "blocks public IP",
			remoteAddr: "8.8.8.8:12345",
			expectCode: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			middleware := CIDRMiddleware([]string{}) // Empty allowlist = localhost-only

			handler := middleware(func(c echo.Context) error {
				return c.String(http.StatusOK, "OK")
			})

			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
			req.RemoteAddr = tt.remoteAddr
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := handler(c)

			if tt.expectCode == http.StatusOK {
				assert.NoError(t, err)
				assert.Equal(t, http.StatusOK, rec.Code)
			} else {
				assert.Error(t, err)
				httpErr, ok := err.(*echo.HTTPError)
				assert.True(t, ok)
				assert.Equal(t, tt.expectCode, httpErr.Code)
			}
		})
	}
}

// TestCIDRMiddlewareAllowlistMode verifies CIDR allowlist filtering
func TestCIDRMiddlewareAllowlistMode(t *testing.T) {
	tests := []struct {
		name       string
		allowlist  []string
		remoteAddr string
		expectCode int
	}{
		{
			name:       "allows IP in allowlist range",
			allowlist:  []string{testPrivateNetCIDR},
			remoteAddr: "192.168.1.100:12345",
			expectCode: http.StatusOK,
		},
		{
			name:       "blocks IP outside allowlist range",
			allowlist:  []string{testPrivateNetCIDR},
			remoteAddr: "192.168.2.100:12345",
			expectCode: http.StatusForbidden,
		},
		{
			name:       "allows IP in multiple ranges",
			allowlist:  []string{testPrivateNetCIDR, "10.0.0.0/8"},
			remoteAddr: "10.1.2.3:12345",
			expectCode: http.StatusOK,
		},
		{
			name:       "allows specific IP with /32",
			allowlist:  []string{"203.0.113.42/32"},
			remoteAddr: "203.0.113.42:12345",
			expectCode: http.StatusOK,
		},
		{
			name:       "blocks localhost when not in allowlist",
			allowlist:  []string{testPrivateNetCIDR},
			remoteAddr: testLocalhostAddr,
			expectCode: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			middleware := CIDRMiddleware(tt.allowlist)

			handler := middleware(func(c echo.Context) error {
				return c.String(http.StatusOK, "OK")
			})

			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
			req.RemoteAddr = tt.remoteAddr
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := handler(c)

			if tt.expectCode == http.StatusOK {
				assert.NoError(t, err)
				assert.Equal(t, http.StatusOK, rec.Code)
			} else {
				assert.Error(t, err)
				httpErr, ok := err.(*echo.HTTPError)
				assert.True(t, ok)
				assert.Equal(t, tt.expectCode, httpErr.Code)
			}
		})
	}
}

// TestCIDRMiddlewareProxyHeaders verifies X-Forwarded-For and X-Real-IP handling
func TestCIDRMiddlewareProxyHeaders(t *testing.T) {
	tests := []struct {
		name          string
		allowlist     []string
		remoteAddr    string
		xForwardedFor string
		xRealIP       string
		expectCode    int
		description   string
	}{
		{
			name:          "uses X-Forwarded-For when present",
			allowlist:     []string{testPrivateNetCIDR},
			remoteAddr:    testPrivateAddr,
			xForwardedFor: "192.168.1.100, 10.0.0.5",
			expectCode:    http.StatusOK,
			description:   "Should use first IP from X-Forwarded-For",
		},
		{
			name:       "uses X-Real-IP when X-Forwarded-For absent",
			allowlist:  []string{testPrivateNetCIDR},
			remoteAddr: testPrivateAddr,
			xRealIP:    "192.168.1.100",
			expectCode: http.StatusOK,
		},
		{
			name:       "uses RemoteAddr when no proxy headers",
			allowlist:  []string{"10.0.0.0/8"},
			remoteAddr: testPrivateAddr,
			expectCode: http.StatusOK,
		},
		{
			name:          "blocks when X-Forwarded-For IP not in allowlist",
			allowlist:     []string{testPrivateNetCIDR},
			remoteAddr:    "192.168.1.50:12345",
			xForwardedFor: "203.0.113.1",
			expectCode:    http.StatusForbidden,
			description:   "X-Forwarded-For takes precedence over RemoteAddr",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			middleware := CIDRMiddleware(tt.allowlist)

			handler := middleware(func(c echo.Context) error {
				return c.String(http.StatusOK, "OK")
			})

			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
			req.RemoteAddr = tt.remoteAddr
			if tt.xForwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.xForwardedFor)
			}
			if tt.xRealIP != "" {
				req.Header.Set("X-Real-IP", tt.xRealIP)
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := handler(c)

			if tt.expectCode == http.StatusOK {
				assert.NoError(t, err, tt.description)
				assert.Equal(t, http.StatusOK, rec.Code)
			} else {
				assert.Error(t, err, tt.description)
				httpErr, ok := err.(*echo.HTTPError)
				assert.True(t, ok)
				assert.Equal(t, tt.expectCode, httpErr.Code)
			}
		})
	}
}

// TestCIDRMiddlewareInvalidCIDR verifies fallback to localhost-only for invalid CIDRs
func TestCIDRMiddlewareInvalidCIDR(t *testing.T) {
	e := echo.New()
	// Invalid CIDR should fall back to localhost-only
	middleware := CIDRMiddleware([]string{"not-a-valid-cidr", "also invalid"})

	handler := middleware(func(c echo.Context) error {
		return c.String(http.StatusOK, "OK")
	})

	// Test localhost is allowed
	req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
	req.RemoteAddr = testLocalhostAddr
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := handler(c)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Test external IP is blocked
	req2 := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
	req2.RemoteAddr = "192.168.1.1:12345"
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(req2, rec2)

	err2 := handler(c2)
	assert.Error(t, err2)
	httpErr, ok := err2.(*echo.HTTPError)
	assert.True(t, ok)
	assert.Equal(t, http.StatusForbidden, httpErr.Code)
}
