package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

const (
	schedulerPkg = "github.com/gaborage/go-bricks/scheduler"
	appPkg       = "github.com/gaborage/go-bricks/app"
	consumerPkg  = "example.com/svc/orders"
)

func frameworkRootsConfig(resolverType string) *config.Config {
	cfg := &config.Config{}
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = resolverType
	cfg.Multitenant.Resolver.Path.Segment = 1
	cfg.Multitenant.Resolver.Domain = "example.com"
	cfg.Multitenant.Resolver.Order = []string{config.ResolverTypeHeader, config.ResolverTypeSubdomain}
	return cfg
}

func TestFrameworkRoots(t *testing.T) {
	path := &multitenant.PathResolver{Segment: 1}
	header := &multitenant.HeaderResolver{HeaderName: HeaderXTenantID}
	tests := []struct {
		name     string
		edit     func(cfg *config.Config)
		resolver multitenant.TenantResolver
		want     []string
	}{
		{name: "multitenant_disabled", edit: func(cfg *config.Config) { cfg.Multitenant.Enabled = false }, resolver: path},
		{name: "header_resolver_reads_no_path", resolver: header},
		{name: "nil_resolver", resolver: nil},
		{name: "path_resolver_scheduler_root", resolver: path, want: []string{"/_sys"}},
		{name: "base_path_prefixes_scheduler_root", edit: func(cfg *config.Config) { cfg.Server.Path.Base = "api/" }, resolver: path, want: []string{"/api/_sys"}},
		{name: "slash_base_path_is_none", edit: func(cfg *config.Config) { cfg.Server.Path.Base = "/" }, resolver: path, want: []string{"/_sys"}},
		{name: "debug_disabled_adds_nothing", edit: func(cfg *config.Config) { cfg.Debug.PathPrefix = "/_debug" }, resolver: path, want: []string{"/_sys"}},
		{
			name: "debug_prefix_at_engine_root", resolver: path, want: []string{"/api/_sys", "/_debug"},
			edit: func(cfg *config.Config) {
				cfg.Server.Path.Base = "/api"
				cfg.Debug.Enabled = true
				cfg.Debug.PathPrefix = "_debug/"
			},
		},
		{
			name: "debug_prefix_below_base_is_a_root", resolver: path, want: []string{"/api/_sys", "/api/v1/_debug"},
			edit: func(cfg *config.Config) {
				cfg.Server.Path.Base = "/api"
				cfg.Debug.Enabled = true
				cfg.Debug.PathPrefix = "/api/v1/_debug"
			},
		},
		{
			name: "debug_prefix_beside_base_word", resolver: path, want: []string{"/api/v1/_sys", "/api/v"},
			edit: func(cfg *config.Config) {
				cfg.Server.Path.Base = "/api/v1"
				cfg.Debug.Enabled = true
				cfg.Debug.PathPrefix = "/api/v"
			},
		},
		{
			name: "composite_with_path_member", resolver: &multitenant.CompositeResolver{Resolvers: []multitenant.TenantResolver{header, path}},
			want: []string{"/_sys"},
		},
		{
			name: "default_debug_prefix_under_root_base", resolver: path, want: []string{"/_sys", "/_sys"},
			edit: func(cfg *config.Config) { cfg.Debug.Enabled = true; cfg.Debug.PathPrefix = "/_sys" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := frameworkRootsConfig(config.ResolverTypePath)
			if tt.edit != nil {
				tt.edit(cfg)
			}
			roots, err := frameworkRoots(cfg, tt.resolver)
			require.NoError(t, err)
			assert.Equal(t, tt.want, roots)
		})
	}
}

// TestFrameworkRootsRefuseDebugPrefixCoveringModuleRoutes pins the debug-root refusal: under a
// path-reading resolver with debug.enabled, a debug.pathprefix at the engine root or at or
// above server.path.base would reserve every module route, so it is an error naming both keys.
// The same prefix under a resolver that reads no path, or with debug off, is not judged.
func TestFrameworkRootsRefuseDebugPrefixCoveringModuleRoutes(t *testing.T) {
	path := &multitenant.PathResolver{Segment: 1}
	tests := []struct {
		name, base, prefix string
	}{
		{name: "empty_prefix", base: "/api", prefix: ""},
		{name: "slash_prefix", base: "/api", prefix: "/"},
		{name: "slash_prefix_root_base", base: "", prefix: "/"},
		{name: "prefix_equals_base", base: "/api/v1", prefix: "/api/v1/"},
		{name: "prefix_above_base", base: "/api/v1", prefix: "/api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := frameworkRootsConfig(config.ResolverTypePath)
			cfg.Server.Path.Base = tt.base
			cfg.Debug.Enabled = true
			cfg.Debug.PathPrefix = tt.prefix
			roots, err := frameworkRoots(cfg, path)
			require.EqualError(t, err, `server: debug.pathprefix "`+tt.prefix+`" is at or above server.path.base "`+tt.base+
				`": under a tenant resolver that reads the path the debug prefix is a framework route root, so it would `+
				`reserve every module route; set debug.pathprefix outside server.path.base (ADR-168)`)
			assert.Equal(t, []string{joinBasePath(normalizeBasePath(tt.base), "/_sys")}, roots, "the scheduler root alone")

			_, err = frameworkRoots(cfg, &multitenant.HeaderResolver{HeaderName: HeaderXTenantID})
			require.NoError(t, err, "a resolver that reads no path reserves nothing")
			cfg.Debug.Enabled = false
			_, err = frameworkRoots(cfg, path)
			require.NoError(t, err, "a disabled debug group reserves nothing")
		})
	}
}

// TestStartRefusesDebugPrefixAboveBase drives the refusal through New and Start: the error
// names both keys, and Start returns it before binding.
func TestStartRefusesDebugPrefixAboveBase(t *testing.T) {
	cfg := frameworkRootsConfig(config.ResolverTypePath)
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Path.Base = "/api/v1"
	cfg.Debug.Enabled = true
	cfg.Debug.PathPrefix = "/api"
	srv := New(cfg, logger.New("disabled", true))
	require.Error(t, srv.setupErr, "New records the refusal")
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) }) // stops a Start that wrongly binds
	err := srv.Start()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `debug.pathprefix "/api" is at or above server.path.base "/api/v1"`)
}

// TestServerFrameworkRootsFollowTheBuiltResolver pins that New hands admission the roots of
// the seat it installed, from the resolver the configured type builds.
func TestServerFrameworkRootsFollowTheBuiltResolver(t *testing.T) {
	tests := []struct {
		name         string
		resolverType string
		want         []string
	}{
		{name: "path", resolverType: config.ResolverTypePath, want: []string{"/_sys"}},
		{name: "header", resolverType: config.ResolverTypeHeader},
		{name: "subdomain", resolverType: config.ResolverTypeSubdomain},
		{name: "composite_without_path", resolverType: config.ResolverTypeComposite},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(frameworkRootsConfig(tt.resolverType), logger.New("disabled", true))
			assert.Equal(t, tt.want, srv.admission.frameworkRoots)
		})
	}
}

func TestRouteAdmissionRefusesNonFrameworkRegistrationUnderFrameworkRoot(t *testing.T) {
	DefaultRouteRegistry.Clear()
	t.Cleanup(DefaultRouteRegistry.Clear)
	tests := []struct {
		name    string
		path    string
		pkg     string
		refused bool
	}{
		{name: "scheduler_route", path: "/api/_sys/job", pkg: schedulerPkg},
		{name: "debug_route", path: "/_debug/gc", pkg: appPkg},
		{name: "probe_route", path: "/api/_sys/health", pkg: serverPackagePath},
		{name: "server_package_route_is_not_a_probe", path: "/api/_sys/adapted", pkg: serverPackagePath, refused: true},
		{name: "consumer_route_under_scheduler_root", path: "/api/_sys/mine", pkg: consumerPkg, refused: true},
		{name: "consumer_route_at_root_itself", path: "/_debug", pkg: consumerPkg, refused: true},
		{name: "consumer_route_beside_root", path: "/api/_system", pkg: consumerPkg},
		{name: "framework_package_prefix_is_not_framework", path: "/api/_sys/x", pkg: schedulerPkg + "x", refused: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newRouteAdmission()
			a.frameworkRoots = []string{"/api/_sys", "/_debug"}
			a.probePaths = []string{"/api/_sys/health", "/ready"}
			reg := RouteRegistrant{HandlerName: "h", Package: tt.pkg}
			added := false
			a.admit(admittedRoute{Method: http.MethodGet, Path: tt.path, Descriptor: modellessDescriptor(http.MethodGet, tt.path, reg)}, func() { added = true })
			assert.Equal(t, !tt.refused, added)
			if !tt.refused {
				assert.Empty(t, a.snapshot())
				return
			}
			conflicts := a.snapshot()
			require.Len(t, conflicts, 1)
			c := conflicts[0]
			assert.Equal(t, RouteConflictReasonFrameworkRoot, c.Reason)
			assert.Equal(t, reg, c.Duplicate)
			assert.Equal(t, tt.path, c.Path)
			assert.NotEmpty(t, c.FirstPath)
		})
	}
}

func TestRouteAdmissionRefusesConsumerGroupUnderFrameworkRoot(t *testing.T) {
	a := newRouteAdmission()
	a.frameworkRoots = []string{"/_sys"}
	a.admitGroup("/_sys", schedulerPkg)
	a.admitGroup("/_sys/extra", consumerPkg)
	a.admitGroup("/orders", consumerPkg)

	conflicts := a.snapshot()
	require.Len(t, conflicts, 1)
	assert.Equal(t, RouteConflict{
		Path: "/_sys/extra", FirstPath: "/_sys", Duplicate: RouteRegistrant{Package: consumerPkg},
		Reason: RouteConflictReasonFrameworkRoot,
	}, conflicts[0])
	assert.Equal(t, "group /_sys/extra — under a framework route root at /_sys, refused: middleware ("+consumerPkg+")", conflicts[0].line())
	require.Len(t, a.registered, 2, "a refused group is not recorded for a family's trespass scan")
}

func TestRouteAdmissionFrameworkRootAfterStartPanics(t *testing.T) {
	a := newRouteAdmission()
	a.frameworkRoots = []string{"/_sys"}
	a.seal()
	reg := RouteRegistrant{HandlerName: "late", Package: consumerPkg}
	assert.PanicsWithError(t,
		"server: registration after Start: route registration refused (1 conflict(s))\n"+
			"GET /_sys/late — under a framework route root at /_sys, refused: late ("+consumerPkg+")",
		func() {
			a.admit(admittedRoute{Method: http.MethodGet, Path: "/_sys/late", Descriptor: modellessDescriptor(http.MethodGet, "/_sys/late", reg)}, func() {})
		})
	assert.Panics(t, func() { a.admitGroup("/_sys/late", consumerPkg) })
}
