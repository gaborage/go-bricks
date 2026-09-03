package server

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

func TestValidateRequestIDAcceptsSafeValues(t *testing.T) {
	cases := []string{
		"simple",
		"with-hyphens",
		"with_underscores",
		"ABC123",
		"0",
		strings.Repeat("a", 128), // boundary: max length
		"uuid-like-deadbeef-1234-5678",
	}
	for _, id := range cases {
		t.Run("valid_"+id[:min(len(id), 16)], func(t *testing.T) {
			assert.Equal(t, id, validateRequestID(id))
		})
	}
}

func TestValidateRequestIDRejectsUnsafeValues(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "too_long_129", in: strings.Repeat("a", 129)},
		{name: "too_long_5000", in: strings.Repeat("x", 5000)},
		{name: "space", in: "has space"},
		{name: "tab", in: "has\ttab"},
		{name: "newline", in: "has\nnewline"},
		{name: "carriage_return", in: "has\rCR"},
		{name: "null_byte", in: "has\x00null"},
		{name: "slash", in: "path/like"},
		{name: "colon", in: "scheme:value"},
		{name: "angle_brackets", in: "<script>"},
		{name: "quote", in: "has\"quote"},
		{name: "unicode", in: "café"}, // é
		{name: "percent_encoding", in: "has%20space"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Empty(t, validateRequestID(c.in),
				"unsafe X-Request-ID %q must be rejected", c.in)
		})
	}
}

func TestSafeGetRequestIDPrefersResponseHeader(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	// Caller-controlled junk on the request — should NOT be returned because the
	// framework-set response header takes precedence.
	req.Header.Set(echo.HeaderXRequestID, "junk\nattack")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Response().Header().Set(echo.HeaderXRequestID, "framework-trusted-id")

	assert.Equal(t, "framework-trusted-id", safeGetRequestID(c))
}

func TestSafeGetRequestIDValidatesInboundFallback(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	req.Header.Set(echo.HeaderXRequestID, "junk with spaces")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	// No response-header value, so safeGetRequestID falls back to the request
	// header — which must be rejected by validation.

	assert.Empty(t, safeGetRequestID(c),
		"invalid inbound X-Request-ID must NOT propagate through safeGetRequestID")
}

func TestSafeGetRequestIDPassesValidInboundFallback(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	req.Header.Set(echo.HeaderXRequestID, "valid-inbound-id")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	assert.Equal(t, "valid-inbound-id", safeGetRequestID(c))
}

// Regression guard for the critical bypass found in code review: Echo's
// stock middleware.RequestID copies the inbound header into the response
// header verbatim. If a caller swaps RequestIDMiddleware out for that
// middleware (or any other that does verbatim echo), validateRequestID
// must still reject the response-header value rather than trusting it.
func TestSafeGetRequestIDValidatesResponseHeaderToo(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	// Simulate a middleware that echoed an unvalidated inbound value into
	// the response header. safeGetRequestID must NOT return this.
	c.Response().Header().Set(echo.HeaderXRequestID, "junk with spaces")

	assert.Empty(t, safeGetRequestID(c),
		"poisoned response-header values must be rejected as defense in depth")
}

func TestRequestIDMiddlewareValidInboundIsEchoed(t *testing.T) {
	e := echo.New()
	mw := requestIDMiddlewareEcho()
	handler := mw(func(_ *echo.Context) error { return nil })

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	req.Header.Set(echo.HeaderXRequestID, "valid-trace-id-123")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, handler(c))
	assert.Equal(t, "valid-trace-id-123", rec.Header().Get(echo.HeaderXRequestID),
		"valid inbound X-Request-ID must be set on the response header")
}

func TestRequestIDMiddlewareInvalidInboundReplacedWithUUID(t *testing.T) {
	cases := map[string]string{
		"junk_with_spaces": "junk with spaces",
		"crlf_injection":   "id\r\nX-Evil: 1",
		"length_overflow":  strings.Repeat("x", 500),
		"path_traversal":   "../etc/passwd",
		"angle_brackets":   "<script>",
	}
	for name, junk := range cases {
		t.Run(name, func(t *testing.T) {
			e := echo.New()
			mw := requestIDMiddlewareEcho()
			handler := mw(func(_ *echo.Context) error { return nil })

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
			req.Header.Set(echo.HeaderXRequestID, junk)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			require.NoError(t, handler(c))

			got := rec.Header().Get(echo.HeaderXRequestID)
			assert.NotEqual(t, junk, got, "invalid inbound X-Request-ID must NOT be reflected on the response")
			assert.Len(t, got, 36, "expected a generated UUID on the response header")
		})
	}
}

func TestRequestIDMiddlewareMissingInboundGeneratesUUID(t *testing.T) {
	e := echo.New()
	mw := requestIDMiddlewareEcho()
	handler := mw(func(_ *echo.Context) error { return nil })

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, handler(c))
	assert.Len(t, rec.Header().Get(echo.HeaderXRequestID), 36)
}

func TestLogSafeValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain_value_is_quoted", in: "/users/42", want: `"/users/42"`},
		{name: "space_stays_inside_the_quotes", in: "/x status=200", want: `"/x status=200"`},
		{name: "quote_is_escaped", in: `a"b`, want: `"a\"b"`},
		{name: "invalid_utf8_is_hex_escaped", in: "a\xffb", want: `"a\xffb"`},
		{name: "at_cap_is_untouched", in: strings.Repeat("x", logSafeValueMaxBytes), want: `"` + strings.Repeat("x", logSafeValueMaxBytes) + `"`},
		{name: "over_cap_is_truncated_with_marker", in: strings.Repeat("x", logSafeValueMaxBytes+1), want: `"` + strings.Repeat("x", logSafeValueMaxBytes-3) + `..."`},
		{name: "escaping_expands_at_most_four_x", in: strings.Repeat("\x00", logSafeValueMaxBytes+1), want: `"` + strings.Repeat(`\x00`, logSafeValueMaxBytes-3) + `..."`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, logSafeValue(tt.in))
		})
	}
}

// assertRejectionLogEscapesRequestValues drives emit with a request whose
// path carries a space-separated forged field, a newline, a forged log
// prefix and a control byte, and whose
// client IP (raw-header IPExtractor, the worst case) carries a newline, on
// both the framework-logger path and the nil-logger stdlib fallback. Each
// must produce exactly ONE line with the injected bytes rendered as escape
// sequences (CodeQL go/log-injection). Shared by every server WARN sink that
// renders request-derived values through logSafeValue.
func assertRejectionLogEscapesRequestValues(t *testing.T, emit func(l logger.Logger, c *echo.Context)) {
	t.Helper()
	const (
		forgedPath = "/test status=200\n[server] forged=true\x00"
		forgedIP   = "10.0.0.202\nclient=1.1.1.1"
	)
	newCtx := func() *echo.Context {
		e := echo.New()
		e.IPExtractor = func(r *http.Request) string { return r.Header.Get(HeaderXRealIP) }
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
		req.URL.Path = forgedPath // NewRequest rejects control bytes in the target; set the path directly
		req.Header.Set(HeaderXRealIP, forgedIP)
		req.RemoteAddr = forgedIP + ":12345"
		return e.NewContext(req, httptest.NewRecorder())
	}
	check := func(t *testing.T, lines []string) {
		t.Helper()
		require.Len(t, lines, 1, "injected newline must not forge a second log line: %q", lines)
		assert.NotContains(t, lines[0], "\n")
		assert.NotContains(t, lines[0], "\x00")
		assert.Contains(t, lines[0], `path="/test status=200\n[server] forged=true\x00"`, "space and newline stay inside the quoted value")
		assert.Contains(t, lines[0], `client="10.0.0.202\nclient=1.1.1.1`)
	}

	t.Run("framework_logger", func(t *testing.T) {
		capturer := &capturingLogger{}
		emit(capturer, newCtx())
		check(t, capturer.warns)
	})
	t.Run("nil_logger_stdlib_fallback", func(t *testing.T) {
		var buf bytes.Buffer
		prev := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(prev) })
		emit(nil, newCtx())
		lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
		require.NotEmpty(t, lines[0], "stdlib fallback must emit the WARN")
		assert.Contains(t, lines[0], "WARN [server.")
		check(t, lines)
	})
}
