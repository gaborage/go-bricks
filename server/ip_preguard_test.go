package server

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

const (
	testIPPreGuardIP = "10.0.0.100"

	// Connection constants
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
			e := newEngine()
			e.Use(ipPreGuardEcho(tt.requestsPerSec, nil))

			e.GET("/test", func(c *echo.Context) error {
				return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
			})

			allowedCount := 0
			blockedCount := 0

			for range tt.requestCount {
				req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
				// Use same IP to trigger rate limiting
				req.Header.Set(HeaderXRealIP, testIPPreGuardIP)
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
	e := newEngine()
	e.Use(ipPreGuardEcho(2, nil)) // Very low limit to trigger easily

	e.GET("/test", func(c *echo.Context) error {
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
				req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
				req.Header.Set(HeaderXRealIP, ip)
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
	e := newEngine()
	e.Use(ipPreGuardEcho(1, nil)) // Very restrictive limit

	e.GET("/test", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Make requests to exceed rate limit
	ip := testIPPreGuardIP
	var blockedResponse *httptest.ResponseRecorder

	for range 5 {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
		req.Header.Set(HeaderXRealIP, ip)
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
	e := newEngine()

	// Test that IPPreGuard works correctly when combined with other middleware
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			c.Response().Header().Set(headerTestMiddleware, "present")
			return next(c)
		}
	})
	e.Use(ipPreGuardEcho(3, nil))

	e.GET("/test", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Test that requests below limit work normally
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
	req.Header.Set(HeaderXRealIP, "192.168.1.50")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "present", rec.Header().Get(headerTestMiddleware))

	// Test that rate limited requests still have middleware headers
	for range 10 {
		req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
		req.Header.Set(HeaderXRealIP, "192.168.1.50")
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

// TestIPPreGuardLogsRejection verifies a 429 rejection emits one WARN through
// the provided framework logger, carrying the path and status. This is the
// audit trail for the observability-off blind spot: ipPreGuardEcho is
// registered outer to the access logger (server/middleware.go) and never
// calls next() on reject, so without this WARN the request leaves zero
// server-side trail. capturingLogger is defined in cors_test.go.
func TestIPPreGuardLogsRejection(t *testing.T) {
	capturer := &capturingLogger{}
	line := tripIPPreGuard(t, capturer, "/test", "10.0.0.200", func() []string { return capturer.warns })
	assert.Contains(t, line, `method="GET"`)
	assert.Contains(t, line, `client="10.0.0.200"`)
	assert.Contains(t, line, "status=429")
}

// TestIPPreGuardNilLoggerDoesNotPanic verifies the nil-logger path (public
// IPPreGuard construction, which threads nil through to ipPreGuardEcho) still
// rejects with 429 and does not panic — guards against a future refactor
// reintroducing an unconditional l.Warn() call.
func TestIPPreGuardNilLoggerDoesNotPanic(t *testing.T) {
	e := newEngine()
	e.Use(ipPreGuardEcho(1, nil))

	e.GET("/test", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	ip := "10.0.0.201"
	rejected := false
	assert.NotPanics(t, func() {
		for range 5 {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
			req.Header.Set(HeaderXRealIP, ip)
			req.RemoteAddr = ip + portSuffix
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			if rec.Code == http.StatusTooManyRequests {
				rejected = true
				break
			}
		}
	})
	assert.True(t, rejected, "expected a 429 rejection even with a nil logger")
}

// TestIPPreGuardRejectionLogEscapesRequestValues varies the attacker-written
// dimension of the rejection log: a path carrying a space-separated forged
// field, a newline, a forged log prefix and a control byte, plus a spoofed X-Forwarded-For with a newline.
// Both the framework-logger path and the stdlib fallback must emit exactly ONE
// line with the client's injected bytes rendered as escape sequences, and an
// oversized client must be capped (CodeQL go/log-injection). No byte of the
// forged path reaches the line, since it renders from the matched template
// (#2212).
func TestIPPreGuardRejectionLogEscapesRequestValues(t *testing.T) {
	tests := []struct {
		name string
		path string
		ip   string
		want []string
	}{
		{
			name: "newline_and_control_byte_are_escaped",
			path: "/test status=200\n[server.ip_preguard] forged=true\x00",
			ip:   "10.0.0.202\nclient=1.1.1.1",
			want: []string{
				`client="10.0.0.202\nclient=1.1.1.1"`,
			},
		},
		{
			name: "oversized_client_is_capped",
			path: "/test",
			ip:   strings.Repeat("a", 2*logSafeValueMaxBytes),
			want: []string{"...\""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Run("framework_logger", func(t *testing.T) {
				capturer := &capturingLogger{}
				line := tripIPPreGuard(t, capturer, tt.path, tt.ip, func() []string { return capturer.warns })
				for _, w := range tt.want {
					assert.Contains(t, line, w)
				}
			})
			t.Run("nil_logger_stdlib_fallback", func(t *testing.T) {
				var buf bytes.Buffer
				prev := log.Writer()
				log.SetOutput(&buf)
				t.Cleanup(func() { log.SetOutput(prev) })
				line := tripIPPreGuard(t, nil, tt.path, tt.ip, func() []string {
					return strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
				})
				assert.Contains(t, line, "WARN [server.ip_preguard]")
				for _, w := range tt.want {
					assert.Contains(t, line, w)
				}
			})
		})
	}
}

// tripIPPreGuard drives requests from ip at path until a 429 is produced,
// then asserts the captured output is exactly one line and returns it.
func tripIPPreGuard(t *testing.T, l logger.Logger, path, ip string, lines func() []string) string {
	t.Helper()
	e := newEngine()
	// Echo's default RealIP is the peer address only; a deployment's IPExtractor
	// (server.go wires ExtractIPFromXFFHeader) is what makes it header-derived.
	// A raw-header extractor models the worst case: the value reaches the log
	// exactly as the caller wrote it.
	e.IPExtractor = func(r *http.Request) string { return r.Header.Get(HeaderXRealIP) }
	e.Use(ipPreGuardEcho(1, l))
	e.GET("/*", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })

	rejected := false
	for range 5 {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
		req.URL.Path = path // NewRequest rejects control bytes in the target; set the path directly
		req.Header.Set(HeaderXRealIP, ip)
		req.RemoteAddr = "10.0.0.1" + portSuffix
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			rejected = true
			break
		}
	}
	require.True(t, rejected, "expected a 429 rejection to trip the WARN log")

	got := lines()
	require.Len(t, got, 1, "injected newline must not forge a second log line: %q", got)
	assert.NotContains(t, got[0], "\n")
	assert.NotContains(t, got[0], "\x00")
	// The path renders from the matched template "/*" (#2212), never from request bytes.
	assert.Contains(t, got[0], `path="/`+maskToken+`"`)
	assert.NotContains(t, got[0], "forged=true", "no request path byte may reach the line")
	_, after, _ := strings.Cut(got[0], "client=")
	field, _, _ := strings.Cut(after, " ")
	assert.LessOrEqual(t, len(field), logSafeValueMaxBytes+2, "client field must be capped (plus its quotes)")
	return got[0]
}

// TestIPPreGuardRejectionCorrelatesItsLine pins the pre-guard's 429 WARN to the request on both
// logger paths.
func TestIPPreGuardRejectionCorrelatesItsLine(t *testing.T) {
	// trip serves until the first 429 and returns how many times the client address was read;
	// the limiter itself reads it once per request.
	trip := func(t *testing.T, l logger.Logger) int32 {
		t.Helper()
		e := newEngine()
		var reads atomic.Int32
		e.IPExtractor = func(r *http.Request) string {
			reads.Add(1)
			return echo.ExtractIPDirect()(r)
		}
		e.Use(ipPreGuardEcho(1, l))
		e.GET(correlationRoute, func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
		for i := range 5 {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, correlatedRequest(t, true))
			if rec.Code == http.StatusTooManyRequests {
				return reads.Load() - int32(i+1)
			}
		}
		t.Fatal("the pre-guard never rejected")
		return 0
	}
	t.Run("framework_logger", func(t *testing.T) {
		l := &testLogger{}
		trip(t, l)
		entries := l.logEntries()
		require.Len(t, entries, 1)
		assert.Equal(t, "[server.ip_preguard] request rejected by IP pre-guard", entries[0].msg)
		assertLineCorrelated(t, entries[0])
	})
	t.Run("nil_logger_stdlib_fallback", func(t *testing.T) {
		buf := captureStdlibLog(t)
		trip(t, nil)
		line := strings.TrimSuffix(buf.String(), "\n")
		assert.True(t, strings.HasSuffix(line, "status=429"+correlationSuffix), "got %q", line)
	})
	t.Run("disabled_warn_level_formats_nothing", func(t *testing.T) {
		dl := &disabledLogger{ev: &disabledEvent{}}
		assert.Zero(t, trip(t, dl), "a disabled WARN level must not read the client address")
		assert.False(t, dl.ev.msgCalled)
		assert.Equal(t, int32(1), trip(t, &testLogger{}), "an enabled WARN reads the client address once")
	})
}

// TestIPPreGuardRejectionRendersUnknownMethodAsOther pins the semconv known-method rule on the
// IP pre-guard WARN: a method outside the known set prints `_OTHER`.
func TestIPPreGuardRejectionRendersUnknownMethodAsOther(t *testing.T) {
	assertLineRendersMethodOther(t, logIPPreGuardRejection)
}
