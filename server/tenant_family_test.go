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
	DefaultRouteRegistry.Clear()
	t.Cleanup(DefaultRouteRegistry.Clear)
	return New(newTenantFamilyConfig(basePath), &testLogger{})
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
	q := strconv.Quote
	// existingFamily is a server already holding the tenant-owned family /partner/v2.
	existingFamily := func(t *testing.T) *Server {
		srv := newTenantFamilyServer(t, "")
		tenantOwnedGroup(srv.ModuleGroup(), "/partner/v2", policy)
		return srv
	}
	tests := []struct {
		name string
		// fixture returns the refused call and the Server backing it (nil when none does).
		fixture func(t *testing.T) (call func(), srv *Server)
		want    []string // substrings of the panic message, the full prefix first
	}{
		{
			name: "non_framework_registrar",
			fixture: func(*testing.T) (func(), *Server) {
				bare := newRouteGroup(echo.New().Group(""), "", newTenantFamilyConfig(""))
				return func() { tenantOwnedGroup(bare, "/fam", policy) }, nil
			},
			want: []string{q("/fam")},
		},
		{
			name: "nil_tenant",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "")
				return func() { tenantOwnedGroup(srv.ModuleGroup(), "/fam", TenantPolicy{}) }, srv
			},
			want: []string{q("/fam")},
		},
		{
			name: "nested_in_family",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "")
				sub := tenantOwnedGroup(srv.ModuleGroup(), "/fam", policy).Group("/sub")
				return func() { tenantOwnedGroup(sub, "/inner", policy) }, srv
			},
			want: []string{q("/fam/sub/inner"), q("/fam")},
		},
		{
			name: "empty_prefix",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "")
				parent := srv.ModuleGroup().Group("/orders")
				return func() { tenantOwnedGroup(parent, "", policy) }, srv
			},
			want: []string{q("/orders")},
		},
		{
			name: "root_prefix",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "")
				parent := srv.ModuleGroup().Group("/orders")
				return func() { tenantOwnedGroup(parent, "/", policy) }, srv
			},
			want: []string{q("/orders")},
		},
		{
			name: "param_segment",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "")
				return func() { tenantOwnedGroup(srv.ModuleGroup(), "/orgs/:org", policy) }, srv
			},
			want: []string{q("/orgs/:org")},
		},
		{
			name: "wildcard_segment",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "")
				return func() { tenantOwnedGroup(srv.ModuleGroup(), "/files/*", policy) }, srv
			},
			want: []string{q("/files/*")},
		},
		{
			name: "covers_health_path",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "/api")
				return func() { tenantOwnedGroup(srv.RootGroup(), "/api", policy) }, srv
			},
			want: []string{q("/api"), q("/api/health")},
		},
		{
			name: "covers_ready_path",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := newTenantFamilyServer(t, "")
				return func() { tenantOwnedGroup(srv.RootGroup(), "/ready", policy) }, srv
			},
			want: []string{q("/ready")},
		},
		{
			name: "equals_other_family",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := existingFamily(t)
				return func() { tenantOwnedGroup(srv.ModuleGroup(), "/partner/v2", policy) }, srv
			},
			want: []string{q("/partner/v2")},
		},
		{
			name: "covers_other_family",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := existingFamily(t)
				return func() { tenantOwnedGroup(srv.ModuleGroup(), "/partner", policy) }, srv
			},
			want: []string{q("/partner"), q("/partner/v2")},
		},
		{
			name: "under_other_family",
			fixture: func(t *testing.T) (func(), *Server) {
				srv := existingFamily(t)
				return func() { tenantOwnedGroup(srv.ModuleGroup(), "/partner/v2/x", policy) }, srv
			},
			want: []string{q("/partner/v2/x"), q("/partner/v2")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call, srv := tt.fixture(t)
			var admission *routeAdmission
			if srv != nil {
				admission = srv.admission
			}
			before := admission.familyPrefixes()
			// Each substring re-runs the refused call; the record must stay unchanged throughout.
			for _, want := range tt.want {
				assertRegistrationPanics(t, want, call)
			}
			assert.Equal(t, before, admission.familyPrefixes(), "a refused call records no family")
		})
	}
}

func TestTenantOwnedGroupRefusesWhenMultitenantDisabled(t *testing.T) {
	DefaultRouteRegistry.Clear()
	t.Cleanup(DefaultRouteRegistry.Clear)
	srv := New(newProbeTestConfig("/api"), &testLogger{})
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
