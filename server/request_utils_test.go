package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	mw := RequestIDMiddleware()
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
			mw := RequestIDMiddleware()
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
	mw := RequestIDMiddleware()
	handler := mw(func(_ *echo.Context) error { return nil })

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, handler(c))
	assert.Len(t, rec.Header().Get(echo.HeaderXRequestID), 36)
}
