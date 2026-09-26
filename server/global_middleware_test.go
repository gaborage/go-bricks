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
// middleware for normal routes and bypasses it for the health/ready probes. The decision
// reads the matched route TEMPLATE, so each case seeds the template the router would have
// stamped; the request URL is deliberately unrelated to it in the last two cases, which is
// the shape a URL-keyed decision got wrong.
func TestSkipProbesBypassesProbePathsOnly(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	var mwCalls int
	wrapped := skipProbes(func(_ HandlerContext, next func() error) error {
		mwCalls++
		return next()
	}, newProbeSkipper("/health", "/ready"))

	cases := []struct {
		name     string
		template string
		url      string
		wantMW   bool
	}{
		{name: "health", template: "/health", url: "/health", wantMW: false},
		{name: "ready", template: "/ready", url: "/ready", wantMW: false},
		{name: "module_route", template: "/api/:id", url: "/api/x", wantMW: true},
		{name: "module_route_url_decodes_to_probe", template: "/api/:id", url: "/api/%72eady", wantMW: true},
		{name: "module_route_url_rewritten_to_probe", template: "/api/:id", url: "/health", wantMW: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mwCalls = 0
			nextCalls := 0
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.url, http.NoBody)
			ctx := NewHandlerContextForTestWithOptions(httptest.NewRecorder(), req, cfg, WithRouteTemplate(tc.template))
			require.NoError(t, wrapped(ctx, func() error { nextCalls++; return nil }))
			assert.Equal(t, 1, nextCalls, "next must always run")
			if tc.wantMW {
				assert.Equal(t, 1, mwCalls, "wrapped middleware must run")
			} else {
				assert.Equal(t, 0, mwCalls, "wrapped middleware must be skipped")
			}
		})
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

// TestRegisterGlobalMiddlewareGatesNonProbeRequests drives the whole default chain: a global
// middleware carrying an auth gate (ADR-036) must run for every request the probe routes do not
// serve — a percent-encoded spelling, a non-probe method — and must keep bypassing the genuine
// probes on both methods they answer.
func TestRegisterGlobalMiddlewareGatesNonProbeRequests(t *testing.T) {
	srv := newTestServer(probeSkipBase, "", "")
	srv.echo.Add(http.MethodGet, probeSkipModuleRoute, okEchoHandler)
	srv.echo.Add(http.MethodHead, probeSkipModuleRoute, okEchoHandler)
	srv.echo.Add(http.MethodPost, probeSkipReady, okEchoHandler)
	srv.RegisterGlobalMiddleware(blockUnlessAuthorized)

	for _, method := range probeMethods {
		for _, tc := range []struct {
			name, path string
			wantCode   int
		}{
			{"registered_health", probeSkipHealth, http.StatusOK},
			{"registered_ready", probeSkipReady, http.StatusOK},
			{"encoded_health", probeSkipEncodedHlth, http.StatusUnauthorized},
			{"encoded_ready", probeSkipEncodedRdy, http.StatusUnauthorized},
		} {
			t.Run(method+"_"+tc.name, func(t *testing.T) {
				assertServeCode(t, srv.echo, method, tc.path, tc.wantCode)
			})
		}
	}

	t.Run("post_on_probe_path", func(t *testing.T) {
		assertServeCode(t, srv.echo, http.MethodPost, probeSkipReady, http.StatusUnauthorized,
			"a module route owning a non-probe method on the probe path stays gated")
	})
}

// TestRegisterGlobalMiddlewareGatesURLRewrittenByEarlierMiddleware covers the chained shape: a
// global middleware runs before the gate and rewrites the request URL onto the probe path. The
// gate's exemption reads the matched route template, so the rewrite cannot exempt the request;
// a URL-keyed exemption would have skipped the gate (and RawPath stays empty here, so comparing
// the raw path would not have helped either).
func TestRegisterGlobalMiddlewareGatesURLRewrittenByEarlierMiddleware(t *testing.T) {
	srv := newTestServer(probeSkipBase, "", "")
	srv.echo.GET(probeSkipModuleRoute, okEchoHandler)

	rewriter := func(c HandlerContext, next func() error) error {
		c.Request().URL.Path = probeSkipHealth
		return next()
	}
	srv.RegisterGlobalMiddleware(rewriter, blockUnlessAuthorized)

	assertServeCode(t, srv.echo, http.MethodGet, probeSkipBase+"/thing", http.StatusUnauthorized,
		"an upstream URL rewrite must not exempt a module route from the gate")
	assertServeCode(t, srv.echo, http.MethodGet, probeSkipHealth, http.StatusOK,
		"the genuine probe still bypasses the gate")
}
