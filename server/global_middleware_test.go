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
			ctx := NewHandlerContextForTest(httptest.NewRecorder(), req, cfg, WithRouteTemplate(tc.template))
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

// TestServerStartRefusesTenantFamilyWithGlobalMiddleware pins Start's backstop (ADR-164): a
// tenant-owned route family beside installed global middleware refuses Start before either
// bind, whichever was built first.
func TestServerStartRefusesTenantFamilyWithGlobalMiddleware(t *testing.T) {
	const tail = "global middleware runs before a family resolves its tenant, so it would see no tenant on the family's routes — " +
		"attach each gate per group instead: in the family's Authenticate (before the tenant) or Use (after it), and on every other group that needs it"
	mw := func(_ HandlerContext, next func() error) error { return next() }
	family := func(srv *Server, prefix string) {
		TenantOwnedGroup(srv.ModuleGroup(), prefix, fixedTenantPolicy(familySeatTenant))
	}
	oneFamily := "tenant-owned route family with global middleware: 1 family(ies) (/api/v1/orgs); 1 global middleware installed; " + tail
	tests := []struct {
		name  string
		build func(srv *Server)
		want  string
	}{
		{
			name:  "global_middleware_then_family",
			build: func(srv *Server) { srv.RegisterGlobalMiddleware(mw); family(srv, "/api/v1/orgs") },
			want:  oneFamily,
		},
		{
			name:  "family_then_global_middleware",
			build: func(srv *Server) { family(srv, "/api/v1/orgs"); srv.RegisterGlobalMiddleware(mw) },
			want:  oneFamily,
		},
		{
			name: "two_families",
			build: func(srv *Server) {
				family(srv, "/api/v1/orgs")
				family(srv, "/partner/v2")
				srv.RegisterGlobalMiddleware([]MiddlewareFunc{mw, mw, mw}...)
			},
			want: "tenant-owned route family with global middleware: 2 family(ies) (/api/v1/orgs, /partner/v2); 3 global middleware installed; " + tail,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTrespassTestServer(t)
			tt.build(srv)

			err := requireStartRefusedBeforeBind(t, srv, "Start bound and served despite a tenant-owned family beside global middleware")

			require.ErrorIs(t, err, ErrTenantFamilyWithGlobalMiddleware)
			assert.Equal(t, tt.want, err.Error())
		})
	}
}

// TestServerStartAllowsTenantFamilyWithoutGlobalMiddleware is P32's negative control: a family
// alone, global middleware alone, or only nil global middleware beside a family lets Start serve.
func TestServerStartAllowsTenantFamilyWithoutGlobalMiddleware(t *testing.T) {
	mw := func(_ HandlerContext, next func() error) error { return next() }
	tests := []struct {
		name  string
		build func(srv *Server)
	}{
		{name: "family_without_global_middleware", build: func(srv *Server) { buildTrespassFamily(srv) }},
		{name: "global_middleware_without_family", build: func(srv *Server) { srv.RegisterGlobalMiddleware(mw) }},
		{
			name: "family_with_only_nil_global_middleware",
			build: func(srv *Server) {
				buildTrespassFamily(srv)
				srv.RegisterGlobalMiddleware(nil)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTrespassTestServer(t)
			tt.build(srv)

			errCh := startProbeServer(t, srv)

			assert.NotNil(t, srv.BoundAddr())
			shutdownAndDrain(t, srv, errCh)
		})
	}
}

// TestRegisterGlobalMiddlewareAfterStartPanics pins that after Start, served or not, installing
// global middleware panics, a retry too, and it never runs or counts; beside a family the panic
// matches ErrTenantFamilyWithGlobalMiddleware as well. A nil-only call stays a no-op.
func TestRegisterGlobalMiddlewareAfterStartPanics(t *testing.T) {
	tests := []lateGlobalMiddlewareCase{
		{
			name: "beside_family_panics", family: true, alsoIs: ErrTenantFamilyWithGlobalMiddleware,
			wantPanic: "server: registration after Start: RegisterGlobalMiddleware: tenant-owned route family with global middleware (" +
				trespassFamily + "); " + tenantFamilyGlobalMiddlewareHint,
		},
		{name: "without_family_panics", wantPanic: "server: registration after Start: RegisterGlobalMiddleware"},
		{name: "nil_beside_family_is_noop", family: true},
	}
	for _, seal := range sealServer {
		for _, tt := range tests {
			t.Run(seal.name+"/"+tt.name, func(t *testing.T) { tt.run(t, seal.seal) })
		}
	}
}

// lateGlobalMiddlewareCase is one RegisterGlobalMiddleware call after Start.
type lateGlobalMiddlewareCase struct {
	name      string
	family    bool   // build a tenant-owned family before Start
	alsoIs    error  // a second sentinel the panic matches, beside ErrRegistrationAfterStart
	wantPanic string // empty: register only nil middleware, a no-op
}

func (tt lateGlobalMiddlewareCase) run(t *testing.T, seal func(*testing.T, *Server)) {
	srv := newTrespassTestServer(t)
	srv.ModuleGroup().Add(http.MethodGet, "/open", answerWith("open"))
	if tt.family {
		buildTrespassFamily(srv)
	}
	seal(t, srv)
	mwRan := false
	var mw MiddlewareFunc = func(_ HandlerContext, next func() error) error {
		mwRan = true
		return next()
	}
	register := func() { srv.RegisterGlobalMiddleware(mw) }

	if tt.wantPanic == "" {
		mw = nil
		require.NotPanics(t, register)
	} else {
		var also []error
		if tt.alsoIs != nil {
			also = append(also, tt.alsoIs)
		}
		requireLateRegistrationPanic(t, tt.wantPanic, register, also...)
		requireLateRegistrationPanic(t, tt.wantPanic, register, also...)
	}
	assert.Equal(t, http.StatusOK, serveFamilyRequest(srv, "/open").Code)
	assert.False(t, mwRan, "refused global middleware never runs")
	assert.Zero(t, srv.GlobalMiddlewareCount())
}

// TestRegisterGlobalMiddlewareRacingStartBesideFamily pins that global middleware racing Start
// beside a tenant-owned family installs before Start's check, which then refuses, or meets the
// after-Start panic: the server never serves with both.
func TestRegisterGlobalMiddlewareRacingStartBesideFamily(t *testing.T) {
	mw := func(_ HandlerContext, next func() error) error { return next() }
	for round := range 64 {
		srv := newTrespassTestServer(t)
		buildTrespassFamily(srv)
		errCh := raceStart(srv, round)
		panicked := panicsWith(t, ErrTenantFamilyWithGlobalMiddleware, func() { srv.RegisterGlobalMiddleware(mw) })

		select {
		case err := <-errCh:
			require.ErrorIs(t, err, ErrTenantFamilyWithGlobalMiddleware)
			assert.False(t, panicked)
			assert.Equal(t, 1, srv.GlobalMiddlewareCount())
		case <-srv.ReadyCh():
			assert.True(t, panicked, "the server serves a family beside global middleware")
			assert.Zero(t, srv.GlobalMiddlewareCount())
			shutdownAndDrain(t, srv, errCh)
		}
	}
}
