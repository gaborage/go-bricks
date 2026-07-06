package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/multitenant"
)

func okEchoHandler(c *echo.Context) error { return c.String(http.StatusOK, "ok") }

func blockUnlessAuthorized(c HandlerContext, next func() error) error {
	if c.Request().Header.Get("Authorization") == "" {
		return NewUnauthorizedError("missing token")
	}
	return next()
}

// TestSkipProbesBypassesProbePathsOnly verifies the skipProbes wrapper runs the wrapped
// middleware for normal paths and bypasses it for the health/ready probes.
func TestSkipProbesBypassesProbePathsOnly(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	var mwCalls int
	wrapped := skipProbes(func(_ HandlerContext, next func() error) error {
		mwCalls++
		return next()
	}, CreateProbeSkipper("/health", "/ready"))

	cases := []struct {
		path   string
		wantMW bool
	}{
		{"/health", false},
		{"/ready", false},
		{"/api/x", true},
	}
	for _, tc := range cases {
		mwCalls = 0
		nextCalls := 0
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.path, http.NoBody)
		ctx := NewHandlerContextForTest(httptest.NewRecorder(), req, cfg)
		require.NoError(t, wrapped(ctx, func() error { nextCalls++; return nil }))
		assert.Equal(t, 1, nextCalls, "next must always run for %s", tc.path)
		if tc.wantMW {
			assert.Equal(t, 1, mwCalls, "wrapped middleware must run for %s", tc.path)
		} else {
			assert.Equal(t, 0, mwCalls, "wrapped middleware must be skipped for %s", tc.path)
		}
	}
}

// TestRegisterGlobalMiddlewareRunsOnRequest verifies a registered global middleware runs
// on a normal request and chains to the handler.
func TestRegisterGlobalMiddlewareRunsOnRequest(t *testing.T) {
	srv := newTestServer("", "", "")
	srv.echo.GET("/api/thing", okEchoHandler)
	srv.RegisterGlobalMiddleware(func(c HandlerContext, next func() error) error {
		c.ResponseWriter().Header().Set("X-Global", "ran")
		return next()
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/thing", http.NoBody)
	srv.echo.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ran", rec.Header().Get("X-Global"))
	assert.Equal(t, "ok", rec.Body.String())
}

// TestRegisterGlobalMiddlewareSkipsProbes verifies health/ready bypass the gate while a
// normal route is gated.
func TestRegisterGlobalMiddlewareSkipsProbes(t *testing.T) {
	srv := newTestServer("", "", "")
	srv.echo.GET("/api/thing", okEchoHandler)
	srv.RegisterGlobalMiddleware(blockUnlessAuthorized)

	assertHTTPGetResponse(t, srv, "/health", http.StatusOK)
	assertHTTPGetResponse(t, srv, "/ready", http.StatusOK)
	assertHTTPGetResponse(t, srv, "/api/thing", http.StatusUnauthorized)
}

// TestRegisterGlobalMiddlewareShortCircuits401 verifies a gate that returns an IAPIError
// without calling next aborts the request with the standard envelope.
func TestRegisterGlobalMiddlewareShortCircuits401(t *testing.T) {
	srv := newTestServer("", "", "")
	handlerRan := false
	srv.echo.GET("/api/secure", func(c *echo.Context) error {
		handlerRan = true
		return c.String(http.StatusOK, "secret")
	})
	srv.RegisterGlobalMiddleware(blockUnlessAuthorized)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/secure", http.NoBody)
	srv.echo.ServeHTTP(rec, req)

	assert.False(t, handlerRan, "handler must not run when the gate aborts")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, "UNAUTHORIZED", resp.Error.Code)
}

// TestRegisterGlobalMiddlewareRunsAfterTenantResolution verifies the gate runs after the
// built-in tenant middleware, so it observes the resolved tenant.
func TestRegisterGlobalMiddlewareRunsAfterTenantResolution(t *testing.T) {
	cfg := newTestConfig("", "", "")
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = config.ResolverTypeHeader
	srv := New(cfg, &testLogger{})

	var seen string
	srv.echo.GET("/api/whoami", okEchoHandler)
	srv.RegisterGlobalMiddleware(func(c HandlerContext, next func() error) error {
		seen, _ = multitenant.GetTenant(c.RequestContext())
		return next()
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/whoami", http.NoBody)
	req.Header.Set(HeaderXTenantID, "acme")
	srv.echo.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "acme", seen, "global middleware must observe the tenant resolved earlier in the chain")
}

// TestRegisterGlobalMiddlewareRunsOncePerRequest guards against double-wrapping.
func TestRegisterGlobalMiddlewareRunsOncePerRequest(t *testing.T) {
	srv := newTestServer("", "", "")
	var calls int32
	srv.echo.GET("/api/thing", okEchoHandler)
	srv.RegisterGlobalMiddleware(func(_ HandlerContext, next func() error) error {
		atomic.AddInt32(&calls, 1)
		return next()
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/thing", http.NoBody)
	srv.echo.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

// TestRegisterGlobalMiddlewareAppliesToLaterRoutes proves the root-chain registration is
// order-independent: a route added AFTER the gate is still gated.
func TestRegisterGlobalMiddlewareAppliesToLaterRoutes(t *testing.T) {
	srv := newTestServer("", "", "")
	srv.RegisterGlobalMiddleware(blockUnlessAuthorized)
	srv.echo.GET("/api/late", okEchoHandler)

	assertHTTPGetResponse(t, srv, "/api/late", http.StatusUnauthorized)
}

// TestRegisterGlobalMiddlewareGatesSystemRoutes verifies system/debug routes are gated
// (they are not part of the health/ready probe exemption).
func TestRegisterGlobalMiddlewareGatesSystemRoutes(t *testing.T) {
	srv := newTestServer("", "", "")
	srv.echo.GET("/_sys/job", okEchoHandler)
	srv.echo.GET("/debug/info", okEchoHandler)
	srv.RegisterGlobalMiddleware(blockUnlessAuthorized)

	assertHTTPGetResponse(t, srv, "/_sys/job", http.StatusUnauthorized)
	assertHTTPGetResponse(t, srv, "/debug/info", http.StatusUnauthorized)
}

// TestRegisterGlobalMiddlewareNoopWhenEmpty verifies passing no middleware is a safe no-op.
func TestRegisterGlobalMiddlewareNoopWhenEmpty(t *testing.T) {
	srv := newTestServer("", "", "")
	srv.echo.GET("/api/thing", okEchoHandler)
	srv.RegisterGlobalMiddleware()

	assertHTTPGetResponse(t, srv, "/api/thing", http.StatusOK, "ok")
}

// TestRegisterGlobalMiddlewareSkipsNilEntries verifies nil middleware entries are skipped
// rather than panicking the request.
func TestRegisterGlobalMiddlewareSkipsNilEntries(t *testing.T) {
	srv := newTestServer("", "", "")
	ran := false
	srv.echo.GET("/api/thing", okEchoHandler)
	mws := []MiddlewareFunc{nil, func(_ HandlerContext, next func() error) error {
		ran = true
		return next()
	}, nil}
	srv.RegisterGlobalMiddleware(mws...)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/thing", http.NoBody)
	srv.echo.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, ran, "non-nil middleware must run; nil entries are skipped without panicking")
}
