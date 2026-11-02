package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
)

// familySeatTenant is the tenant every request through the root chain carries: the root
// tenant seat still resolves family routes.
const familySeatTenant = "seat-tenant"

// newTenantFamilyConfig is newProbeTestConfig with multi-tenant mode on, resolving the tenant
// from X-Tenant-ID.
func newTenantFamilyConfig(basePath string) *config.Config {
	cfg := newProbeTestConfig(basePath)
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = config.ResolverTypeHeader
	cfg.Multitenant.Resolver.Header = HeaderXTenantID
	return cfg
}

// fixedTenantPolicy is a TenantPolicy whose Tenant always resolves id.
func fixedTenantPolicy(id string) TenantPolicy {
	return TenantPolicy{Tenant: func(HandlerContext) (string, error) { return id, nil }}
}

// newTenantFamilyServer builds a Server from newTenantFamilyConfig, clearing
// DefaultRouteRegistry before and after the test.
func newTenantFamilyServer(t *testing.T, basePath string) *Server {
	t.Helper()
	return newTenantFamilyServerWithConfig(t, newTenantFamilyConfig(basePath))
}

// newTenantFamilyServerWithConfig is newTenantFamilyServer with the config supplied by the caller.
func newTenantFamilyServerWithConfig(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	DefaultRouteRegistry.Clear()
	t.Cleanup(DefaultRouteRegistry.Clear)
	return New(cfg, &testLogger{})
}

// serveFamilyRequest sends GET path through srv's engine carrying the seat tenant.
func serveFamilyRequest(srv *Server, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	req.Header.Set(HeaderXTenantID, familySeatTenant)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)
	return rec
}

// answerWith is a Handler answering 200 with body.
func answerWith(body string) Handler {
	return func(c HandlerContext) error { return c.String(http.StatusOK, body) }
}

func TestTenantOwnedGroupIgnoresParentMiddleware(t *testing.T) {
	tests := []struct {
		name string
		// build returns the parent group and the family built under it.
		build func(srv *Server, parentMW MiddlewareFunc) (parent, family RouteRegistrar)
	}{
		{
			name: "parent_middleware_before_family",
			build: func(srv *Server, parentMW MiddlewareFunc) (parent, family RouteRegistrar) {
				parent = srv.ModuleGroup().Group("/p", parentMW)
				return parent, tenantOwnedGroup(parent, "/fam", fixedTenantPolicy("family-tenant"))
			},
		},
		{
			name: "parent_use_after_family",
			build: func(srv *Server, parentMW MiddlewareFunc) (parent, family RouteRegistrar) {
				parent = srv.ModuleGroup().Group("/p")
				family = tenantOwnedGroup(parent, "/fam", fixedTenantPolicy("family-tenant"))
				parent.Use(parentMW)
				return parent, family
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTenantFamilyServer(t, "")
			parentRan := false
			parentMW := func(_ HandlerContext, next func() error) error {
				parentRan = true
				return next()
			}
			parent, family := tt.build(srv, parentMW)
			family.Add(http.MethodGet, "/orders", answerWith("family"))
			parent.Add(http.MethodGet, "/own", answerWith("parent"))

			rec := serveFamilyRequest(srv, "/p/fam/orders")
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "family", rec.Body.String())
			assert.False(t, parentRan, "a parent group's middleware must never run on a tenant-owned family route")

			rec = serveFamilyRequest(srv, "/p/own")
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.True(t, parentRan, "control: the parent middleware runs on the parent's own route")
		})
	}
}

func TestTenantOwnedGroupPanicsAtCallSite(t *testing.T) {
	policy := fixedTenantPolicy("family-tenant")
	// bare is a group no Server built: it carries no admission.
	bare := func(*Server) RouteRegistrar {
		return newRouteGroup(echo.New().Group(""), "", newTenantFamilyConfig(""))
	}
	moduleGroup := (*Server).ModuleGroup
	rootGroup := (*Server).RootGroup
	ordersGroup := func(srv *Server) RouteRegistrar { return srv.ModuleGroup().Group("/orders") }
	// inFamily is a plain Group inside the tenant-owned family /fam.
	inFamily := func(srv *Server) RouteRegistrar {
		return tenantOwnedGroup(srv.ModuleGroup(), "/fam", policy).Group("/sub")
	}
	// besideFamily is the module group of a Server already holding the tenant-owned family /partner/v2.
	besideFamily := func(srv *Server) RouteRegistrar {
		tenantOwnedGroup(srv.ModuleGroup(), "/partner/v2", policy)
		return srv.ModuleGroup()
	}
	tests := []struct {
		name      string
		basePath  string
		parent    func(srv *Server) RouteRegistrar // the registrar the refused call builds under
		prefix    string
		nilTenant bool     // the refused call passes TenantPolicy{} instead of policy
		want      []string // substrings of the panic message, the full prefix first
	}{
		{name: "non_framework_registrar", parent: bare, prefix: "/fam", want: []string{`"/fam"`}},
		{name: "nil_tenant", parent: moduleGroup, prefix: "/fam", nilTenant: true, want: []string{`"/fam"`}},
		{name: "nested_in_family", parent: inFamily, prefix: "/inner", want: []string{`"/fam/sub/inner"`, `"/fam"`}},
		{name: "empty_prefix", parent: ordersGroup, prefix: "", want: []string{`"/orders"`}},
		{name: "root_prefix", parent: ordersGroup, prefix: "/", want: []string{`"/orders"`}},
		{name: "param_segment", parent: moduleGroup, prefix: "/orgs/:org", want: []string{`"/orgs/:org"`}},
		{name: "wildcard_segment", parent: moduleGroup, prefix: "/files/*", want: []string{`"/files/*"`}},
		{name: "covers_health_path", basePath: "/api", parent: rootGroup, prefix: "/api", want: []string{`"/api"`, `"/api/health"`}},
		{name: "covers_ready_path", parent: rootGroup, prefix: "/ready", want: []string{`"/ready"`}},
		{name: "equals_other_family", parent: besideFamily, prefix: "/partner/v2", want: []string{`"/partner/v2"`}},
		{name: "covers_other_family", parent: besideFamily, prefix: "/partner", want: []string{`"/partner"`, `"/partner/v2"`}},
		{name: "under_other_family", parent: besideFamily, prefix: "/partner/v2/x", want: []string{`"/partner/v2/x"`, `"/partner/v2"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTenantFamilyServer(t, tt.basePath)
			parent := tt.parent(srv)
			p := policy
			if tt.nilTenant {
				p = TenantPolicy{}
			}
			before := srv.admission.familyPrefixes()
			// Each substring re-runs the refused call; the record must stay unchanged throughout.
			for _, want := range tt.want {
				assertRegistrationPanics(t, want, func() { tenantOwnedGroup(parent, tt.prefix, p) })
			}
			assert.Equal(t, before, srv.admission.familyPrefixes(), "a refused call records no family")
		})
	}
}

func TestTenantOwnedGroupRefusesWhenMultitenantDisabled(t *testing.T) {
	srv := newTenantFamilyServerWithConfig(t, newProbeTestConfig("/api"))
	build := func() { tenantOwnedGroup(srv.ModuleGroup(), "/fam", fixedTenantPolicy("family-tenant")) }

	assertRegistrationPanics(t, strconv.Quote("/api/fam"), build)
	assertRegistrationPanics(t, "multitenant.enabled", build)
	assert.Empty(t, srv.admission.familyPrefixes())
}

func TestTenantOwnedGroupAllowsSegmentSiblingPrefix(t *testing.T) {
	srv := newTenantFamilyServer(t, "")
	prefixes := []string{"/partner/v2", "/partner/v20"}
	tenants := []string{"partner-a", "partner-b"}
	for i, prefix := range prefixes {
		tenantOwnedGroup(srv.ModuleGroup(), prefix, fixedTenantPolicy(tenants[i])).
			Add(http.MethodGet, "/orders", answerWith(prefix))
	}

	for _, prefix := range prefixes {
		rec := serveFamilyRequest(srv, prefix+"/orders")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, prefix, rec.Body.String())
	}
	assert.Equal(t, prefixes, srv.admission.familyPrefixes())
}
