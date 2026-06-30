package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

const (
	usersRoute = "/users"
	childRoute = "/child"
)

func TestRouteGroupAddNormalizesPaths(t *testing.T) {
	e := echo.New()
	base := newRouteGroup(e.Group("/api"), "/api", nil)
	rg, ok := base.(*routeGroup)
	require.True(t, ok)

	var hits int
	rg.Add(http.MethodGet, usersRoute, func(c HandlerContext) error {
		hits++
		return c.String(http.StatusOK, "")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/users", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, hits)

	rg.Add(http.MethodGet, "/api/orders", func(c HandlerContext) error {
		hits++
		return c.String(http.StatusOK, "")
	})

	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/orders", http.NoBody)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 2, hits)

	rg.Add(http.MethodGet, "/", func(c HandlerContext) error {
		hits++
		return c.String(http.StatusOK, "")
	})

	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api", http.NoBody)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 3, hits)
}

func TestRouteGroupGroupCreatesNestedRegistrar(t *testing.T) {
	e := echo.New()
	base := newRouteGroup(e.Group("/api"), "/api", nil)
	parent := base.(*routeGroup)

	child := parent.Group("/v1")
	childGroup, ok := child.(*routeGroup)
	require.True(t, ok)
	assert.Equal(t, "/api/v1", childGroup.prefix)

	var hits int
	childGroup.Add(http.MethodGet, "/widgets", func(c HandlerContext) error {
		hits++
		return c.String(http.StatusOK, "widgets")
	})

	childGroup.Add(http.MethodGet, "/api/v1/analytics", func(c HandlerContext) error {
		hits++
		return c.String(http.StatusOK, "analytics")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/widgets", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "widgets", rec.Body.String())

	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/analytics", http.NoBody)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "analytics", rec.Body.String())
	assert.Equal(t, 2, hits)
}

func TestRouteGroupUseAppliesMiddleware(t *testing.T) {
	e := echo.New()
	base := newRouteGroup(e.Group(""), "", nil)
	rg := base.(*routeGroup)

	var called bool
	rg.Use(func(_ HandlerContext, next func() error) error {
		called = true
		return next()
	})

	rg.Add(http.MethodGet, "/ping", func(c HandlerContext) error {
		return c.String(http.StatusOK, "pong")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ping", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, called)
}

func TestRouteGroupFullPathAndRelative(t *testing.T) {
	rg := &routeGroup{prefix: "/api"}

	assert.Equal(t, "/api/child", rg.FullPath(childRoute))
	assert.Equal(t, "/api", rg.FullPath("/"))
	assert.Equal(t, "/api", rg.FullPath(""))
	assert.Equal(t, childRoute, (&routeGroup{}).FullPath(childRoute))

	assert.Equal(t, "", rg.relativePath("/"))
	assert.Equal(t, "", rg.relativePath("/api"))
	assert.Equal(t, childRoute, rg.relativePath("/api/child"))
	assert.Equal(t, childRoute, rg.relativePath("child"))
	assert.Equal(t, "/apples", rg.relativePath("/apples"))

	assert.Equal(t, "/api", rg.combinePrefix(""))
	assert.Equal(t, "/api/v1", rg.combinePrefix("/v1"))
	assert.Equal(t, "/v1", (&routeGroup{}).combinePrefix("/v1"))
}

// TestTypedHandlerThroughRouteGroupSeam registers a typed handler through a real routeGroup
// (which implements the unexported echoAdder addEcho seam) and hits it via httptest,
// proving the typed hot path produces the standard data/meta envelope through the seam.
func TestTypedHandlerThroughRouteGroupSeam(t *testing.T) {
	srv := newTestServer("", "", "")
	hr := NewHandlerRegistry(srv.cfg)

	GET(hr, srv.ModuleGroup(), "/seam-widget", func(_ EmptyRequest, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "seam-ok"}, nil
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/seam-widget", http.NoBody)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data)
	data, ok := resp.Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "seam-ok", data["message"])
	// Framework envelope meta is present (proves the typed encode path ran via the seam).
	assert.NotEmpty(t, resp.Meta["traceId"])
	assert.NotEmpty(t, resp.Meta["timestamp"])
}

// TestRouteGroupGroupAppliesFlatMiddleware verifies Group(prefix, ...MiddlewareFunc) wires
// flat middleware onto the nested registrar and a raw Handler registered via Add runs.
func TestRouteGroupGroupAppliesFlatMiddleware(t *testing.T) {
	e := echo.New()
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}
	base := newRouteGroup(e.Group(""), "", cfg)

	called := false
	child := base.Group("/v2", func(_ HandlerContext, next func() error) error {
		called = true
		return next()
	})

	child.Add(http.MethodGet, "/ping", func(c HandlerContext) error {
		return c.String(http.StatusOK, "pong")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v2/ping", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "pong", rec.Body.String())
	assert.True(t, called, "flat middleware passed to Group must run")
}

// BenchmarkMiddlewareAdapter contrasts a request routed through one adaptMiddleware-wrapped
// flat MiddlewareFunc against an echo-native middleware. The flat adapter carries a single
// structurally-unavoidable per-request baton allocation (func() error { return next(c) });
// the echo-native form has none. This quantifies the bounded +1 alloc/req/middleware-layer
// documented in migrations.md (the framework's default chain stays echo-native to avoid it).
func BenchmarkMiddlewareAdapter(b *testing.B) {
	cfg := &config.Config{App: config.AppConfig{Env: "development"}}

	build := func(flat bool) (*echo.Echo, *http.Request) {
		e := echo.New()
		rg := newRouteGroup(e.Group(""), "", cfg)
		if flat {
			rg.Use(func(_ HandlerContext, next func() error) error { return next() })
		} else {
			e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c *echo.Context) error { return next(c) }
			})
		}
		rg.Add(http.MethodGet, "/m", func(c HandlerContext) error {
			return c.String(http.StatusOK, "ok")
		})
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/m", http.NoBody)
		return e, req
	}

	b.Run("flat_adapter", func(b *testing.B) {
		e, req := build(true)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
		}
	})

	b.Run("echo_native", func(b *testing.B) {
		e, req := build(false)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
		}
	})
}

// defaultMiddlewareChainMaxAllocs locks the allocation count of the FULL default middleware
// chain (registered echo-native via e.Use inside SetupMiddlewares). It is the measured
// baseline plus a small margin for noise. It will fail if a future change routes the default
// chain through adaptMiddleware (the flat adapter), which would add one baton allocation per
// middleware layer (~10 middlewares ⇒ a clearly detectable jump) — the ADR-026 invariant.
// Measured baseline: 53 allocs/op; the +7 margin still sits below the ~63 a full flat-adapter
// conversion would produce.
const defaultMiddlewareChainMaxAllocs = 60

// TestDefaultMiddlewareChainAllocsStable drives a request through the full default chain of a
// server built via New (SetupMiddlewares) and asserts allocs/op stays at or below the locked
// baseline, enforcing that the default path remains echo-native and baton-free.
func TestDefaultMiddlewareChainAllocsStable(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("testing.AllocsPerRun is unreliable under -race; alloc baseline enforced in the non-race matrix")
	}
	srv := newTestServer("", "", "")
	hr := NewHandlerRegistry(srv.cfg)
	GET(hr, srv.ModuleGroup(), "/chain-bench", func(_ EmptyRequest, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "ok"}, nil
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/chain-bench", http.NoBody)
	// Warm one-time lazy state before measuring.
	srv.echo.ServeHTTP(httptest.NewRecorder(), req)

	got := testing.AllocsPerRun(100, func() {
		rec := httptest.NewRecorder()
		srv.echo.ServeHTTP(rec, req)
	})
	t.Logf("default middleware chain allocs/op = %.1f (ceiling %d)", got, defaultMiddlewareChainMaxAllocs)
	assert.LessOrEqual(t, got, float64(defaultMiddlewareChainMaxAllocs),
		"default middleware chain allocs/op regressed — a flat-adapter on the default path would add one baton alloc per middleware (ADR-026)")
}
