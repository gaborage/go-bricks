package server_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/testutil"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/server"
)

const thisTestPackage = "github.com/gaborage/go-bricks/server_test"

type frameworkRootReq struct{}

type frameworkRootResp struct{}

func frameworkRootServer(resolverType string) *server.Server {
	cfg := &config.Config{App: config.AppConfig{Name: "framework-roots", Env: "test", Version: "1.0.0"}}
	cfg.Server.Path.Base = "/api"
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = resolverType
	cfg.Multitenant.Resolver.Path.Segment = 2
	cfg.Debug.Enabled = true
	cfg.Debug.PathPrefix = "/_debug"
	// A path-reading server needs a check to start (ADR-170); this one holds no tenant.
	return server.NewWithOptions(cfg, logger.New("disabled", true), server.Options{TenantChecker: testutil.TenantSet{}})
}

// TestStartRefusesConsumerRoutesUnderFrameworkRoots pins ADR-168's refusal through server.New:
// under a path resolver a consumer route, middleware-bearing group or tenant-owned family under
// <base>/_sys or the debug prefix is a RouteConflictReasonFrameworkRoot conflict, and Start
// returns it.
func TestStartRefusesConsumerRoutesUnderFrameworkRoots(t *testing.T) {
	server.DefaultRouteRegistry.Clear()
	t.Cleanup(server.DefaultRouteRegistry.Clear)
	noop := func(c server.HandlerContext) error { return c.String(http.StatusOK, "") }
	mw := func(_ server.HandlerContext, next func() error) error { return next() }
	forged := func(d *server.RouteDescriptor) { d.Package = "github.com/gaborage/go-bricks/scheduler" }
	typed := func(frameworkRootReq, server.HandlerContext) (frameworkRootResp, server.IAPIError) {
		return frameworkRootResp{}, nil
	}

	srv := frameworkRootServer(config.ResolverTypePath)
	hr := server.NewHandlerRegistry(&config.Config{})
	srv.ModuleGroup().Add(http.MethodGet, "/_sys/mine", noop)
	server.GET(hr, srv.ModuleGroup(), "/_sys/typed", typed, forged)
	srv.ModuleGroup().Group("/_sys/extra", mw)
	srv.RootGroup().Add(http.MethodPost, "/_debug/flush", noop)
	server.TenantOwnedGroup(srv.ModuleGroup(), "/_sys/family", server.TenantPolicy{
		Tenant: func(server.HandlerContext) (string, error) { return "acme", nil },
	})
	srv.ModuleGroup().Add(http.MethodGet, "/_system", noop) // beside the root, admitted
	srv.RootGroup().Add(http.MethodGet, "/_sys/job", noop)  // the unprefixed spelling, admitted

	conflicts := srv.RouteConflicts()
	want := []struct{ method, path, root string }{
		{http.MethodGet, "/api/_sys/mine", "/api/_sys"},
		{http.MethodGet, "/api/_sys/typed", "/api/_sys"},
		{"", "/api/_sys/extra", "/api/_sys"},
		{http.MethodPost, "/_debug/flush", "/_debug"},
		{"", "/api/_sys/family", "/api/_sys"}, // a tenant-owned family, with no routes
	}
	require.Len(t, conflicts, len(want))
	for i, w := range want {
		assert.Equal(t, server.RouteConflictReasonFrameworkRoot, conflicts[i].Reason)
		assert.Equal(t, w.method, conflicts[i].Method)
		assert.Equal(t, w.path, conflicts[i].Path)
		assert.Equal(t, w.root, conflicts[i].FirstPath)
		assert.Equal(t, thisTestPackage, conflicts[i].Duplicate.Package, "a RouteOption cannot forge the registrant's package")
	}

	err := srv.Start()
	require.Error(t, err)
	require.ErrorIs(t, err, server.ErrDuplicateRoute)
	var dup *server.DuplicateRouteError
	require.ErrorAs(t, err, &dup)
	assert.Len(t, dup.Conflicts, len(want))
	assert.Contains(t, err.Error(), "GET /api/_sys/mine — under a framework route root at /api/_sys, refused: ")
}

// TestHeaderResolverReservesNoFrameworkRoot pins the gate: a resolver that reads no path
// reserves nothing, so the same registrations are admitted.
func TestHeaderResolverReservesNoFrameworkRoot(t *testing.T) {
	server.DefaultRouteRegistry.Clear()
	t.Cleanup(server.DefaultRouteRegistry.Clear)
	srv := frameworkRootServer(config.ResolverTypeHeader)
	noop := func(c server.HandlerContext) error { return c.String(http.StatusOK, "") }
	srv.ModuleGroup().Add(http.MethodGet, "/_sys/mine", noop)
	srv.RootGroup().Add(http.MethodPost, "/_debug/flush", noop)
	assert.Empty(t, srv.RouteConflicts())
}

// TestRouteOptionReadsHandlerPackage pins that RegisterHandler sets RouteDescriptor.Package
// before the options run, so an option reads the handler's package, and stamps it again after.
func TestRouteOptionReadsHandlerPackage(t *testing.T) {
	server.DefaultRouteRegistry.Clear()
	t.Cleanup(server.DefaultRouteRegistry.Clear)
	srv := frameworkRootServer(config.ResolverTypeHeader)
	hr := server.NewHandlerRegistry(&config.Config{})
	var seen string
	read := func(d *server.RouteDescriptor) { seen = d.Package }
	typed := func(frameworkRootReq, server.HandlerContext) (frameworkRootResp, server.IAPIError) {
		return frameworkRootResp{}, nil
	}
	server.GET(hr, srv.ModuleGroup(), "/orders", typed, read)
	assert.Equal(t, thisTestPackage, seen)
}
