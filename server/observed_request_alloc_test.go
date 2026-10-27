package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/racedetect"
)

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
// the pre-guard threshold to 1/s (burst 2) so a client is denied once the burst is spent.
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

	cases := []struct {
		name       string
		req        *http.Request
		denyIP     bool
		wantStatus int
		warn       string // substring of the only WARN sink allowed to fire
		warns      int    // WARNs that sink emits per request
		baseline   int
	}{
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
			warn: warnAccessLog4xx, warns: 1,
			req: observedRequest(http.MethodPost, "/orders", observedHeaderTenant, nil),
		},
		// 404 with no route template, then 404 through the /admin group's "/admin/*" catch-all.
		{
			name: "unmatched_route", baseline: 122, wantStatus: http.StatusNotFound,
			warn: warnAccessLog4xx, warns: 1,
			req: observedRequest(http.MethodGet, "/nowhere", observedHeaderTenant, nil),
		},
		{
			name: "group_catch_all", baseline: 124, wantStatus: http.StatusNotFound,
			warn: warnAccessLog4xx, warns: 1,
			req: observedRequest(http.MethodGet, "/admin/nowhere", observedHeaderTenant, nil),
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
			warn: warnTenantRejected, warns: 1,
			req: observedRequest(http.MethodGet, "/orders", "", nil),
		},
		// Both forwarded-client-cert WARNs fire on a 200 (require is off).
		{
			name: "fcc_duplicate_header_warn", baseline: 131, wantStatus: http.StatusOK,
			warn: warnFCCDuplicate, warns: 1,
			req: observedRequest(http.MethodGet, "/orders", observedHeaderTenant, map[string][]string{
				headerClientCertSubject: {"CN=one", "CN=two"},
			}),
		},
		{
			name: "fcc_leaf_parse_warn", baseline: 134, wantStatus: http.StatusOK,
			warn: warnFCCLeaf, warns: 1,
			req: observedRequest(http.MethodGet, "/orders", observedHeaderTenant, map[string][]string{
				headerClientCertSubject: {"CN=client"},
				headerClientCertLeaf:    {"not-a-pem"},
			}),
		},
		// Rate 1/s, burst 2: the warm-ups spend the burst, and the measured run ends long
		// before a token refills, so every measured request is denied and WARNs.
		{
			name: "ip_preguard_denied", baseline: 80, denyIP: true, wantStatus: http.StatusTooManyRequests,
			warn: warnIPPreGuard, warns: 1,
			req: observedRequest(http.MethodGet, "/orders", observedHeaderTenant, nil),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			wantWarns := tc.warns * (observedRuns + 1)
			require.Equal(t, wantWarns, log.warnCount("")-allWarns, "the measured requests emitted an unpinned number of WARNs")
			require.Equal(t, wantWarns, log.warnCount(tc.warn)-sinkWarns, "the WARNs did not come from the sink this row pins")
			t.Logf("observed request %s allocs/op = %.1f (pinned %d)", tc.name, got, tc.baseline)
			assert.InDelta(t, float64(tc.baseline), got, 0, "observed request allocs/op moved off the pinned baseline (ADR-026)")
		})
	}
}
