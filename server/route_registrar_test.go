package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/racedetect"
	"github.com/gaborage/go-bricks/logger"
)

const (
	usersRoute = "/users"
	childRoute = "/child"
)

func TestRouteGroupAddNormalizesPaths(t *testing.T) {
	e := echo.New()
	rg := newRouteGroup(e.Group("/api"), "/api", nil)

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
	parent := newRouteGroup(e.Group("/api"), "/api", nil)

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
	rg := newRouteGroup(e.Group(""), "", nil)

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

	assert.Empty(t, rg.relativePath("/"))
	assert.Empty(t, rg.relativePath("/api"))
	assert.Equal(t, childRoute, rg.relativePath("/api/child"))
	assert.Equal(t, childRoute, rg.relativePath("child"))
	assert.Equal(t, "/apples", rg.relativePath("/apples"))

	assert.Equal(t, "/api", rg.combinePrefix(""))
	assert.Equal(t, "/api/v1", rg.combinePrefix("/v1"))
	assert.Equal(t, "/v1", (&routeGroup{}).combinePrefix("/v1"))
}

// TestTypedHandlerThroughRouteGroupSeam registers a typed handler through a real routeGroup
// (which registers via the unexported addEcho) and hits it via httptest,
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

// TestRouteGroupAddEmitsRouteDescriptor verifies that a raw route registered through
// RouteRegistrar.Add is recorded in DefaultRouteRegistry (issue #634), mirroring the typed
// GET/POST path so startup inventories keyed by route template see the full surface. Fields
// that only make sense for typed routes stay zero-valued.
func TestRouteGroupAddEmitsRouteDescriptor(t *testing.T) {
	DefaultRouteRegistry.Clear()
	defer DefaultRouteRegistry.Clear()

	e := echo.New()
	base := newRouteGroup(e.Group("/api"), "/api", nil)

	base.Add(http.MethodGet, "/raw-widgets", func(c HandlerContext) error {
		return c.String(http.StatusOK, "ok")
	})

	routes := DefaultRouteRegistry.ByPath("/api/raw-widgets")
	require.Len(t, routes, 1, "raw Add must emit exactly one descriptor")

	d := routes[0]
	assert.Equal(t, http.MethodGet, d.Method)
	assert.Equal(t, "/api/raw-widgets", d.Path)
	assert.Equal(t, "GET:/api/raw-widgets", d.HandlerID)
	assert.NotEmpty(t, d.HandlerName, "HandlerName falls back to the reflected function name")
	// getCallerPackage(2) walks getCallerPackage → Add → this test function. Because the test is
	// itself in package server, the attributed package is the server package. This pins the value
	// but cannot, by construction, catch an under-shot skip that lands on Add's own frame (also
	// package server) — validating that requires a caller in a different package.
	assert.Equal(t, "github.com/gaborage/go-bricks/server", d.Package,
		"Package is derived from the registering caller's frame, same mechanism as typed routes")

	// Typed-only metadata stays zero-valued for raw routes.
	assert.Nil(t, d.RequestType)
	assert.Nil(t, d.ResponseType)
	assert.Nil(t, d.InboundJOSE)
	assert.Nil(t, d.OutboundJOSE)
	assert.False(t, d.RawResponse)
	assert.Empty(t, d.ModuleName)
}

// TestRouteGroupAddDescriptorUsesFullPath verifies the emitted descriptor records the fully
// resolved path (group prefix + route), matching what typed routes store via FullPath.
func TestRouteGroupAddDescriptorUsesFullPath(t *testing.T) {
	DefaultRouteRegistry.Clear()
	defer DefaultRouteRegistry.Clear()

	e := echo.New()
	base := newRouteGroup(e.Group("/api"), "/api", nil)
	child := base.Group("/v1")

	child.Add(http.MethodPost, "/widgets", func(c HandlerContext) error {
		return c.String(http.StatusOK, "ok")
	})

	routes := DefaultRouteRegistry.ByPath("/api/v1/widgets")
	require.Len(t, routes, 1)
	assert.Equal(t, "POST:/api/v1/widgets", routes[0].HandlerID)
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

// guardLogEntry is the record guardLogEvent.Msg appends: testLogEntry (server_test.go)
// minus its values map.
type guardLogEntry struct {
	level  string
	msg    string
	fields []string
}

// guardLogger is the allocation guard's own logger double: testLogger's per-request
// allocation profile — a fresh event struct per level call, a field-name append per field,
// and Msg's two slice appends — with no field VALUES recorded, since that map serves the
// masking assertions in server_test.go (#682), not this measurement. The rest of the
// profile is load-bearing; a double that also drops the Msg bookkeeping measures 54.
// ADR-026's amendment decomposes the number.
type guardLogger struct {
	mu      sync.Mutex
	entries []string
	logs    []guardLogEntry
}

type guardLogEvent struct {
	logger *guardLogger
	level  string
	fields []string
}

func (l *guardLogger) Info() logger.LogEvent  { return &guardLogEvent{logger: l, level: "info"} }
func (l *guardLogger) Error() logger.LogEvent { return &guardLogEvent{logger: l, level: "error"} }
func (l *guardLogger) Debug() logger.LogEvent { return &guardLogEvent{logger: l, level: "debug"} }
func (l *guardLogger) Warn() logger.LogEvent  { return &guardLogEvent{logger: l, level: "warn"} }
func (l *guardLogger) Fatal() logger.LogEvent { return &guardLogEvent{logger: l, level: "fatal"} }

func (l *guardLogger) WithContext(any) logger.Logger { return l }

func (l *guardLogger) WithFields(map[string]any) logger.Logger { return l }

// entryCount reports how many entries the double recorded, under the lock.
func (l *guardLogger) entryCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// warnCount reports how many WARN entries whose message contains msgContains the double
// recorded, under the lock; an empty msgContains counts every WARN.
func (l *guardLogger) warnCount(msgContains string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.logs {
		if e.level == "warn" && strings.Contains(e.msg, msgContains) {
			n++
		}
	}
	return n
}

func (e *guardLogEvent) Msg(msg string) {
	e.logger.mu.Lock()
	defer e.logger.mu.Unlock()
	e.logger.entries = append(e.logger.entries, fmt.Sprintf("%s:%s", e.level, msg))
	e.logger.logs = append(e.logger.logs, guardLogEntry{level: e.level, msg: msg, fields: e.fields})
}

func (e *guardLogEvent) Msgf(format string, args ...any) { e.Msg(fmt.Sprintf(format, args...)) }

// Err records the field name only. testLogEvent keeps the error VALUE for the debug-detail
// assertions (#1182); those live in server_test.go and are not measured here.
func (e *guardLogEvent) Err(err error) logger.LogEvent {
	if err == nil {
		// Mirrors testLogEvent.Err: zerolog emits nothing for a nil error, so neither
		// double records a field the real sink never writes.
		return e
	}
	return e.field("error")
}

// field records a field NAME, the whole of what this double keeps: every typed setter
// funnels here so the append that costs the allocation lives in one place.
func (e *guardLogEvent) field(key string) logger.LogEvent {
	e.fields = append(e.fields, key)
	return e
}

func (e *guardLogEvent) Str(key, _ string) logger.LogEvent { return e.field(key) }

func (e *guardLogEvent) Int(key string, _ int) logger.LogEvent { return e.field(key) }

func (e *guardLogEvent) Int64(key string, _ int64) logger.LogEvent { return e.field(key) }

func (e *guardLogEvent) Uint64(key string, _ uint64) logger.LogEvent { return e.field(key) }

func (e *guardLogEvent) Dur(key string, _ time.Duration) logger.LogEvent { return e.field(key) }

func (e *guardLogEvent) Interface(key string, _ any) logger.LogEvent { return e.field(key) }

func (e *guardLogEvent) Bytes(key string, _ []byte) logger.LogEvent { return e.field(key) }

func (e *guardLogEvent) Bool(key string, _ bool) logger.LogEvent { return e.field(key) }

func (e *guardLogEvent) Enabled() bool { return true }

// Tripwire guard (CONTEXT.md § Testing): re-pin the baseline on a toolchain shift, never
// widen the margin, which must stay below the ~+12 flat-adapter signal. Locks the
// allocation count of the FULL default middleware chain (registered echo-native via e.Use
// inside SetupMiddlewares); it fails if a future change routes the default chain through
// adaptMiddleware (the flat adapter), which would add one baton allocation per middleware
// layer — this config registers 12 unconditional layers, so 57 + ~12 = ~69 clears the
// ceiling of 64 — the ADR-026 invariant. Measured baseline: 57 allocs/op on go1.27.x, down
// from 62 because the guard now measures guardLogger above instead of the recording double:
// the five allocations that left were the #682 testLogEvent values map. About nine harness
// allocations remain at 57 (the per-call event struct, fields-slice growth and Msg
// bookkeeping), so this number is not all chain. ADR-026's amendment decomposes the growth
// from the original 53.
const (
	defaultMiddlewareChainBaselineAllocs = 57
	defaultMiddlewareChainAllocsMargin   = 7
	defaultMiddlewareChainMaxAllocs      = defaultMiddlewareChainBaselineAllocs + defaultMiddlewareChainAllocsMargin
)

// TestDefaultMiddlewareChainAllocsStable drives a request through the full default chain of a
// server built via New (SetupMiddlewares) and asserts allocs/op stays at or below the locked
// baseline, enforcing that the default path remains echo-native and baton-free.
func TestDefaultMiddlewareChainAllocsStable(t *testing.T) {
	if racedetect.Enabled {
		t.Skip("testing.AllocsPerRun is unreliable under -race; alloc baseline enforced in the non-race matrix")
	}
	log := &guardLogger{}
	srv := New(newTestConfig("", "", ""), log)
	hr := NewHandlerRegistry(srv.cfg)
	GET(hr, srv.ModuleGroup(), "/chain-bench", func(_ EmptyRequest, _ HandlerContext) (helloResp, IAPIError) {
		return helloResp{Message: "ok"}, nil
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/chain-bench", http.NoBody)
	// Warm one-time lazy state before measuring.
	srv.echo.ServeHTTP(httptest.NewRecorder(), req)
	warmupEntries := log.entryCount()
	require.Positive(t, warmupEntries, "the warm-up request never reached the logger middleware")

	got := testing.AllocsPerRun(100, func() {
		rec := httptest.NewRecorder()
		srv.echo.ServeHTTP(rec, req)
	})
	// Compare against the warm-up count, not zero: the warm-up alone would satisfy a
	// Positive check even if every measured request bypassed the logger middleware, and
	// then the baseline would not cover the logging path it claims to measure.
	require.Greater(t, log.entryCount(), warmupEntries,
		"the measured requests never reached the logger middleware, so the baseline would not cover the logging path")
	t.Logf("default middleware chain allocs/op = %.1f (ceiling %d)", got, defaultMiddlewareChainMaxAllocs)
	assert.LessOrEqual(t, got, float64(defaultMiddlewareChainMaxAllocs),
		"default middleware chain allocs/op regressed — a flat-adapter on the default path would add one baton alloc per middleware (ADR-026)")
}

const (
	observedHeaderTenant = "acme"
	observedPathTenant   = "/t/globex/orders"
	// observedOpenLimit keeps the pre-guard and the limiter on but out of reach of any
	// row's request count, so every row except ip_preguard_denied passes both.
	observedOpenLimit = 1_000_000
	// observedRuns is AllocsPerRun's measured run count; it adds one warm-up call of its own.
	observedRuns = 100

	warnTenantRejected = "[server.tenant] request rejected"
	warnFCCDuplicate   = "[server.forwardedclientcert] duplicated header"
	warnFCCLeaf        = "[server.forwardedclientcert] leaf certificate not decoded"
	warnIPPreGuard     = "[server.ip_preguard] request rejected"
	warnAccessLog4xx   = "with status 4xx" // the request logger's WARN for a 4xx
)

// newObservedServer builds a server through New the way an observed deployment configures
// it: OTel tracing on, the IP pre-guard and the limiter on, composite tenant resolution
// (header, then path segment 2 under /t) and forwarded-client-cert identity. denyIP drops
// the pre-guard threshold to 1/s (burst 2) so a client is denied once the burst is spent;
// the caller freezes the clock (see TestObservedRequestAllocsStable) so no token ever refills.
func newObservedServer(t *testing.T, log *guardLogger, denyIP bool) *Server {
	t.Helper()
	preGuard := observedOpenLimit
	if denyIP {
		preGuard = 1
	}
	cfg, err := config.LoadFromMap(map[string]any{
		"observability.enabled":              true,
		"app.name":                           testServiceName,
		"app.env":                            "development",
		"app.rate.limit":                     observedOpenLimit,
		"app.rate.ippreguard.enabled":        true,
		"app.rate.ippreguard.threshold":      preGuard,
		"multitenant.enabled":                true,
		"multitenant.resolver.type":          config.ResolverTypeComposite,
		"multitenant.resolver.order":         []string{config.ResolverTypeHeader, config.ResolverTypePath},
		"multitenant.resolver.path.prefix":   "/t",
		"multitenant.resolver.path.segment":  2,
		"server.forwardedclientcert.enabled": true,
	})
	require.NoError(t, err)
	srv := New(cfg, log)
	require.NoError(t, srv.setupErr)

	hr := NewHandlerRegistry(srv.cfg)
	ok := func(_ EmptyRequest, _ HandlerContext) (helloResp, IAPIError) { return helloResp{Message: "ok"}, nil }
	boom := func(_ EmptyRequest, _ HandlerContext) (helloResp, IAPIError) { panic("observed alloc guard") }
	GET(hr, srv.ModuleGroup(), "/orders", ok)
	GET(hr, srv.ModuleGroup(), "/files/:id/*", ok)
	GET(hr, srv.ModuleGroup(), "/t/:tenant/orders", ok)
	GET(hr, srv.ModuleGroup(), "/boom", boom)
	// A group with middleware gets Echo's RouteNotFound catch-all for its prefix.
	admin := srv.ModuleGroup().Group("/admin", func(_ HandlerContext, next func() error) error { return next() })
	GET(hr, admin, "/status", ok)
	return srv
}

// observedRequest builds one measured request: tenant, when set, goes in X-Tenant-ID, and
// headers adds raw, possibly repeated, values on top.
func observedRequest(method, target, tenant string, headers map[string][]string) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), method, target, http.NoBody)
	if tenant != "" {
		req.Header.Set(HeaderXTenantID, tenant)
	}
	for name, values := range headers {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	return req
}

// observedRow is one request shape TestObservedRequestAllocsStable pins.
type observedRow struct {
	name       string
	req        *http.Request
	denyIP     bool
	wantStatus int
	warn       string // substring of the only WARN sink allowed to fire, once per request
	baseline   int
}

// measureObservedRow warms, measures and checks one row.
func measureObservedRow(t *testing.T, exporter *tracetest.InMemoryExporter, tc observedRow) {
	log := &guardLogger{}
	srv := newObservedServer(t, log, tc.denyIP)
	// Warm one-time lazy state (and, for the denied row, spend the burst) before
	// measuring; the last warm-up proves the row exercises the shape it names.
	var rec *httptest.ResponseRecorder
	for range 3 {
		rec = httptest.NewRecorder()
		srv.echo.ServeHTTP(rec, tc.req)
	}
	require.Equal(t, tc.wantStatus, rec.Code, "the row does not exercise the shape it names")
	exporter.Reset()
	allWarns, sinkWarns := log.warnCount(""), log.warnCount(tc.warn)

	got := testing.AllocsPerRun(observedRuns, func() {
		srv.echo.ServeHTTP(httptest.NewRecorder(), tc.req)
	})

	require.NotEmpty(t, exporter.GetSpans(), "the measured requests produced no span, so tracing is not covered")
	wantWarns := 0
	if tc.warn != "" {
		wantWarns = observedRuns + 1
	}
	require.Equal(t, wantWarns, log.warnCount("")-allWarns, "the measured requests emitted an unpinned number of WARNs")
	require.Equal(t, wantWarns, log.warnCount(tc.warn)-sinkWarns, "the WARNs did not come from the sink this row pins")
	t.Logf("observed request %s allocs/op = %.1f (pinned %d)", tc.name, got, tc.baseline)
	assert.InDelta(t, float64(tc.baseline), got, 0, "observed request allocs/op moved off the pinned baseline (ADR-026)")
}

// Tripwire guard (CONTEXT.md § Testing), ADR-026: each row pins allocs/op exactly. A
// toolchain bump re-pins (never widens); a #2212 link that moves a row re-pins that row in
// the same commit and justifies the delta in its body. Pinned on go1.27.3 darwin/arm64;
// CI (linux, `make test-alloc`) enforces, so if CI differs re-pin from the CI log. The span
// exporter's per-span copy and guardLogger's bookkeeping are in every number.
//
// TestObservedRequestAllocsStable drives each request shape through the chain New builds
// with tracing, the IP pre-guard, the limiter, composite tenant resolution and
// forwarded-client-cert identity on, and pins its allocs/op and the WARNs it emits.
func TestObservedRequestAllocsStable(t *testing.T) {
	if racedetect.Enabled {
		t.Skip("testing.AllocsPerRun is unreliable under -race; alloc baseline enforced in the non-race matrix")
	}
	exporter := installTestTracerProvider(t)

	cases := []observedRow{
		{
			name: "plain_matched_route", baseline: 113, wantStatus: http.StatusOK,
			req: observedRequest(http.MethodGet, "/orders", observedHeaderTenant, nil),
		},
		{
			name: "path_param_and_wildcard_tail", baseline: 113, wantStatus: http.StatusOK,
			req: observedRequest(http.MethodGet, "/files/42/a/b/c.txt", observedHeaderTenant, nil),
		},
		{
			name: "method_not_allowed_on_registered_route", baseline: 125, wantStatus: http.StatusMethodNotAllowed,
			warn: warnAccessLog4xx,
			req:  observedRequest(http.MethodPost, "/orders", observedHeaderTenant, nil),
		},
		// 404 with no route template, then 404 through the /admin group's "/admin/*" catch-all.
		{
			name: "unmatched_route", baseline: 122, wantStatus: http.StatusNotFound,
			warn: warnAccessLog4xx,
			req:  observedRequest(http.MethodGet, "/nowhere", observedHeaderTenant, nil),
		},
		{
			name: "group_catch_all", baseline: 124, wantStatus: http.StatusNotFound,
			warn: warnAccessLog4xx,
			req:  observedRequest(http.MethodGet, "/admin/nowhere", observedHeaderTenant, nil),
		},
		// Recover turns the panic into a 500, which the request logger reports at ERROR.
		{
			name: "handler_panic_recovered", baseline: 145, wantStatus: http.StatusInternalServerError,
			req: observedRequest(http.MethodGet, "/boom", observedHeaderTenant, nil),
		},
		// The header resolver runs first, so it wins even where the path carries a tenant.
		{
			name: "header_won_tenant", baseline: 113, wantStatus: http.StatusOK,
			req: observedRequest(http.MethodGet, observedPathTenant, observedHeaderTenant, nil),
		},
		// No header: the composite falls through the header resolver to the path segment.
		{
			name: "path_won_tenant", baseline: 114, wantStatus: http.StatusOK,
			req: observedRequest(http.MethodGet, observedPathTenant, "", nil),
		},
		// Neither resolver finds a tenant: rejected before route matching.
		{
			name: "tenant_rejected", baseline: 93, wantStatus: http.StatusBadRequest,
			warn: warnTenantRejected,
			req:  observedRequest(http.MethodGet, "/orders", "", nil),
		},
		// Both forwarded-client-cert WARNs fire on a 200 (require is off).
		{
			name: "fcc_duplicate_header_warn", baseline: 131, wantStatus: http.StatusOK,
			warn: warnFCCDuplicate,
			req: observedRequest(http.MethodGet, "/orders", observedHeaderTenant, map[string][]string{
				headerClientCertSubject: {"CN=one", "CN=two"},
			}),
		},
		{
			name: "fcc_leaf_parse_warn", baseline: 134, wantStatus: http.StatusOK,
			warn: warnFCCLeaf,
			req: observedRequest(http.MethodGet, "/orders", observedHeaderTenant, map[string][]string{
				headerClientCertSubject: {"CN=client"},
				headerClientCertLeaf:    {"not-a-pem"},
			}),
		},
		// Rate 1/s, burst 2 under a frozen clock: the warm-ups spend the burst and no token
		// ever refills, so every measured request is denied and WARNs.
		{
			name: "ip_preguard_denied", baseline: 80, denyIP: true, wantStatus: http.StatusTooManyRequests,
			warn: warnIPPreGuard,
			req:  observedRequest(http.MethodGet, "/orders", observedHeaderTenant, nil),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := func(t *testing.T) { measureObservedRow(t, exporter, tc) }
			// A denyIP row runs inside a synctest bubble: the pre-guard's memory store reads
			// time.Now, which a bubble advances only when every goroutine in it is durably
			// blocked, and ServeHTTP never blocks, so the clock stands still for the whole row
			// and the spent bucket cannot refill however slow the runner is. Every measured
			// request is denied by construction, not by timing.
			if tc.denyIP {
				synctest.Test(t, run)
			} else {
				run(t)
			}
		})
	}
}
