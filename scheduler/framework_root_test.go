package scheduler

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

type frameworkRootProbeReq struct{}

type frameworkRootProbeResp struct{}

// TestSystemRoutesSkipPathTenant pins ADR-169 with the real scheduler module on a real
// listener: under a path resolver that reads segment 2, GET <base>/_sys/job would resolve
// "_sys", which fails the tenant grammar (400). The seat skips the scheduler root, so the job
// list answers 200, while a route outside the root still resolves and still gets the 400.
func TestSystemRoutesSkipPathTenant(t *testing.T) {
	server.DefaultRouteRegistry.Clear()
	t.Cleanup(server.DefaultRouteRegistry.Clear)
	cfg := &config.Config{App: config.AppConfig{Name: "scheduler-root", Env: "test", Version: "1.0.0"}}
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Timeout.Read = 5 * time.Second
	cfg.Server.Timeout.Write = 5 * time.Second
	cfg.Server.Path.Base = "/api"
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = config.ResolverTypePath
	cfg.Multitenant.Resolver.Path.Segment = 2
	log := logger.New("disabled", true)
	srv := server.NewWithOptions(cfg, log, server.Options{TenantChecker: testutil.TenantSet{}}) // needed to start (ADR-170)

	m := NewModule()
	m.config = cfg
	m.logger = log
	hr := server.NewHandlerRegistry(cfg)
	m.RegisterRoutes(hr, srv.ModuleGroup())
	server.GET(hr, srv.ModuleGroup(), "/_other/job", func(frameworkRootProbeReq, server.HandlerContext) (frameworkRootProbeResp, server.IAPIError) {
		return frameworkRootProbeResp{}, nil
	})
	require.Empty(t, srv.RouteConflicts(), "the scheduler's own routes are admitted under its root")

	testutil.AssertGETStatuses(t, testutil.StartListener(t, srv), map[string]int{
		"/api/_sys/job":   http.StatusOK,
		"/api/_other/job": http.StatusBadRequest,
	})
}
