package app

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/testutil"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/server"
)

// TestDependenciesTakeTheBuiltInStoreAsTenantChecker pins that the bootstrap hands the server
// the resource source's existence check: the built-in store, which implements HasTenant.
func TestDependenciesTakeTheBuiltInStoreAsTenantChecker(t *testing.T) {
	cfg := defaultTestConfig()
	bundle, err := newAppBootstrap(cfg, logger.New("error", false), nil).dependencies(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { (&Builder{bundle: bundle}).closeBundleManagers() })

	assert.IsType(t, &config.TenantStore{}, bundle.tenantChecker)
}

// TestResolveServerAsksTheResourceSourceAboutPathTenants pins ADR-170's one authority: the
// server resolveServer builds admits a path tenant the checker it was handed holds, and refuses
// one only a consumer-supplied ServerOptions.TenantChecker holds, which the checker replaces.
func TestResolveServerAsksTheResourceSourceAboutPathTenants(t *testing.T) {
	server.DefaultRouteRegistry.Clear()
	t.Cleanup(server.DefaultRouteRegistry.Clear)
	cfg := &config.Config{App: config.AppConfig{Name: "path-tenant", Env: "test", Version: "1.0.0"}}
	cfg.Server.Host = localhostIPV4
	cfg.Server.Timeout.Read = 5 * time.Second
	cfg.Server.Timeout.Write = 5 * time.Second
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = config.ResolverTypePath
	cfg.Multitenant.Resolver.Path.Segment = 1
	log := logger.New("disabled", true)
	opts := &Options{ServerOptions: &server.Options{TenantChecker: testutil.TenantSet{"consumer-set": {}}}}

	runner := resolveServer(cfg, log, opts, testutil.TenantSet{"acme": {}})
	srv, ok := runner.(*server.Server)
	require.True(t, ok)
	srv.ModuleGroup().Add(http.MethodGet, "/:tenant/orders", func(c server.HandlerContext) error {
		return c.String(http.StatusOK, "")
	})

	testutil.AssertGETStatuses(t, testutil.StartListener(t, srv), map[string]int{
		"/acme/orders":         http.StatusOK,
		"/consumer-set/orders": http.StatusBadRequest,
	})
}
