package app

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/testutil"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/server"
)

// TestDebugRoutesSkipPathTenant pins ADR-169 with the real debug group on a real listener:
// under a path resolver that reads segment 1, GET /_debug/goroutines would resolve "_debug",
// which fails the tenant grammar (400). The seat skips the debug prefix, so the endpoint
// answers 200 behind its own IP allowlist, while a route outside the prefix still resolves.
func TestDebugRoutesSkipPathTenant(t *testing.T) {
	server.DefaultRouteRegistry.Clear()
	t.Cleanup(server.DefaultRouteRegistry.Clear)
	cfg := &config.Config{App: config.AppConfig{Name: "debug-root", Env: "test", Version: "1.0.0"}}
	cfg.Server.Host = localhostIPV4
	cfg.Server.Timeout.Read = 5 * time.Second
	cfg.Server.Timeout.Write = 5 * time.Second
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = config.ResolverTypePath
	cfg.Multitenant.Resolver.Path.Segment = 1
	cfg.Debug.Enabled = true
	cfg.Debug.PathPrefix = debugPath
	cfg.Debug.AllowedIPs = []string{localhostIPV4}
	cfg.Debug.Endpoints.Goroutines = true
	log := logger.New("disabled", true)
	srv := server.NewWithOptions(cfg, log, server.Options{TenantChecker: testutil.TenantSet{}}) // needed to start (ADR-170)

	require.NoError(t, NewDebugHandlers(nil, &cfg.Debug, log).RegisterDebugEndpoints(srv.RootGroup()))
	srv.RootGroup().Add(http.MethodGet, "/_other/goroutines", func(c server.HandlerContext) error {
		return c.String(http.StatusOK, "")
	})
	require.Empty(t, srv.RouteConflicts(), "the debug group's own routes are admitted under its prefix")

	testutil.AssertGETStatuses(t, testutil.StartListener(t, srv), map[string]int{
		debugPath + "/goroutines": http.StatusOK,
		"/_other/goroutines":      http.StatusBadRequest,
	})
}
