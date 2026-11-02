package app

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/server"
)

const (
	wantTenantFamilyHead = "tenant-owned route family with global middleware: "
	wantTenantFamilyTail = "global middleware runs before a family resolves its tenant, so it would see no tenant on the family's routes — " +
		"attach each gate per group instead: in the family's Authenticate (before the tenant) or Use (after it), and on every other group that needs it"
	emptyGlobalMWWarn = "implements GlobalMiddleware but installs none beside a tenant-owned route family"
)

func passThroughMW(_ server.HandlerContext, next func() error) error { return next() }

func mwModule(name string, mws ...server.MiddlewareFunc) *globalMWModule {
	return &globalMWModule{name: name, mws: mws}
}

// familyStubServer is a real server whose tenant-owned families a test records by hand; only
// TenantFamilyPrefixes is stubbed, so the nil filter, count and RouteConflicts are the real ones.
type familyStubServer struct {
	*server.Server
	families []string
}

func (s *familyStubServer) TenantFamilyPrefixes() []string { return slices.Clone(s.families) }

// familyModule builds its tenant-owned families, and registers a raw GET per entry of routes,
// while its routes register.
type familyModule struct {
	name     string
	prefixes []string
	routes   []string
	srv      *familyStubServer
}

func orgsFamily() familyModule { return familyModule{name: "orgs", prefixes: []string{"/api/v1/orgs"}} }

func (m *familyModule) Name() string             { return m.name }
func (m *familyModule) Init(_ *ModuleDeps) error { return nil }
func (m *familyModule) Shutdown() error          { return nil }
func (m *familyModule) RegisterRoutes(_ *server.HandlerRegistry, r server.RouteRegistrar) {
	m.srv.families = append(m.srv.families, m.prefixes...)
	for _, path := range m.routes {
		r.Add(http.MethodGet, path, func(c server.HandlerContext) error { return c.String(http.StatusOK, "") })
	}
}

// familyScenario is one startup: middleware modules, then family modules, beside families
// built before the module loop (seeded) and middleware registered directly on the server.
type familyScenario struct {
	mwModules []*globalMWModule
	families  []familyModule
	seeded    []string
	direct    []server.MiddlewareFunc
}

type familyRun struct {
	srv       *familyStubServer
	log       *capturingLogger
	hookCalls int
	err       error
}

// runFamilyScenario runs prepareRuntime over sc, logging through a capturingLogger.
func runFamilyScenario(t *testing.T, sc *familyScenario) *familyRun {
	t.Helper()
	server.DefaultRouteRegistry.Clear()
	t.Cleanup(server.DefaultRouteRegistry.Clear)
	cfg := minimalAppConfig("")
	run := &familyRun{log: &capturingLogger{}}
	run.srv = &familyStubServer{Server: server.New(cfg, logger.New("error", false)), families: slices.Clone(sc.seeded)}
	a := newConfiguredApp(t, cfg, &Options{Server: run.srv, PostRegisterRoutes: func([]server.RouteDescriptor) error {
		run.hookCalls++
		return nil
	}})
	a.logger = run.log
	for _, m := range sc.mwModules {
		require.NoError(t, a.RegisterModule(m))
	}
	for _, m := range sc.families {
		m.srv = run.srv
		require.NoError(t, a.RegisterModule(&m))
	}
	run.srv.RegisterGlobalMiddleware(sc.direct...)
	run.err = a.prepareRuntime(context.Background())
	return run
}

// TestPrepareRuntimeRefusesGlobalMiddlewareBesideTenantFamily pins the startup refusal
// (ADR-164) and its attribution: each family's owning module, the modules whose global
// middleware was installed, and how many were registered directly on the server.
func TestPrepareRuntimeRefusesGlobalMiddlewareBesideTenantFamily(t *testing.T) {
	auditAuthz := func() []*globalMWModule {
		return []*globalMWModule{mwModule("audit", passThroughMW), mwModule("authz", passThroughMW)}
	}
	tests := []struct {
		name string
		sc   familyScenario
		want string
	}{
		{
			name: "one_family_module_middleware",
			sc:   familyScenario{mwModules: auditAuthz(), families: []familyModule{orgsFamily()}},
			want: `family /api/v1/orgs (module "orgs"); global middleware from module(s) "audit", "authz"; `,
		},
		{
			name: "two_families",
			sc: familyScenario{mwModules: auditAuthz(), families: []familyModule{
				orgsFamily(), {name: "partner", prefixes: []string{"/partner/v2"}},
			}},
			want: `families /api/v1/orgs (module "orgs"), /partner/v2 (module "partner"); global middleware from module(s) "audit", "authz"; `,
		},
		{
			name: "family_with_no_module",
			sc:   familyScenario{mwModules: []*globalMWModule{mwModule("audit", passThroughMW)}, seeded: []string{"/x"}},
			want: `family /x (no module); global middleware from module(s) "audit"; `,
		},
		{
			// audit's nil entry is never installed and its other two are, so exactly 1 is direct.
			name: "module_and_direct_middleware",
			sc: familyScenario{
				mwModules: []*globalMWModule{mwModule("audit", []server.MiddlewareFunc{passThroughMW, nil, passThroughMW}...)},
				families:  []familyModule{orgsFamily()},
				direct:    []server.MiddlewareFunc{passThroughMW},
			},
			want: `family /api/v1/orgs (module "orgs"); global middleware from module(s) "audit" and 1 registered directly on the server; `,
		},
		{
			name: "direct_middleware_only",
			sc:   familyScenario{families: []familyModule{orgsFamily()}, direct: []server.MiddlewareFunc{passThroughMW, passThroughMW}},
			want: `family /api/v1/orgs (module "orgs"); global middleware: 2 registered directly on the server; `,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := runFamilyScenario(t, &tt.sc)

			assert.Zero(t, run.hookCalls, "the hook never sees a refused route table")
			require.ErrorIs(t, run.err, server.ErrTenantFamilyWithGlobalMiddleware)
			assert.Equal(t, wantTenantFamilyHead+tt.want+wantTenantFamilyTail, run.err.Error())
		})
	}

	t.Run("route_conflict_reported_first", func(t *testing.T) {
		dup := familyModule{name: "orgs", prefixes: []string{"/api/v1/orgs"}, routes: []string{"/dup", "/dup"}}
		run := runFamilyScenario(t, &familyScenario{mwModules: auditAuthz(), families: []familyModule{dup}})

		require.ErrorIs(t, run.err, server.ErrDuplicateRoute)
		require.NotErrorIs(t, run.err, server.ErrTenantFamilyWithGlobalMiddleware, "a route conflict is reported first")
		assert.Zero(t, run.hookCalls, "the hook never sees a refused route table")
	})
}

// TestTenantFamilyRefusalCountsOnlyInstalledMiddleware is the negative control: beside a
// family, global middleware that installs nothing (an empty slice, nil entries) refuses nothing.
func TestTenantFamilyRefusalCountsOnlyInstalledMiddleware(t *testing.T) {
	tests := []struct {
		name string
		sc   familyScenario
	}{
		{name: "module_returns_empty_slice", sc: familyScenario{mwModules: []*globalMWModule{mwModule("audit", []server.MiddlewareFunc{}...)}}},
		{name: "module_returns_only_nil", sc: familyScenario{mwModules: []*globalMWModule{mwModule("audit", []server.MiddlewareFunc{nil, nil}...)}}},
		{name: "direct_register_only_nil", sc: familyScenario{direct: []server.MiddlewareFunc{nil}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.sc.families = []familyModule{orgsFamily()}
			run := runFamilyScenario(t, &tt.sc)

			require.NoError(t, run.err)
			assert.Zero(t, run.srv.GlobalMiddlewareCount())
		})
	}
}

// TestPrepareRuntimeWarnsOnEmptyGlobalMiddlewareBesideTenantFamily pins one WARN per
// GlobalMiddleware implementer that installs none, only beside a family, and that boot goes on.
func TestPrepareRuntimeWarnsOnEmptyGlobalMiddlewareBesideTenantFamily(t *testing.T) {
	tests := []struct {
		name     string
		families []familyModule
		want     []string
	}{
		{name: "beside_a_family", families: []familyModule{orgsFamily()}, want: []string{`"audit"`, `"authz"`}},
		{name: "no_family"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := runFamilyScenario(t, &familyScenario{
				mwModules: []*globalMWModule{mwModule("audit", []server.MiddlewareFunc{}...), mwModule("authz", nil)},
				families:  tt.families,
			})

			require.NoError(t, run.err)
			warns := run.log.warnsContaining(emptyGlobalMWWarn)
			require.Len(t, warns, len(tt.want))
			for i, name := range tt.want {
				assert.Contains(t, warns[i], name)
			}
		})
	}
}
