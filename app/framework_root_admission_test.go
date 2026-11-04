package app_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/server"
)

// thisPackage is where frameworkRootModule's handlers live: an external test package, so the
// handler package is not github.com/gaborage/go-bricks/app.
const thisPackage = "github.com/gaborage/go-bricks/app_test"

type frameworkRootReq struct{}

type frameworkRootResp struct{}

// embeddingRegistrar is a consumer registrar that embeds the framework's: server.GET through it
// takes the typed fallback and reaches the real registrar through Add.
type embeddingRegistrar struct{ server.RouteRegistrar }

// frameworkRootModule registers under <base>/_sys through every door: the GET helper, a direct
// RegisterHandler call, the typed and raw doors of an embedding registrar, and one route outside
// the root.
type frameworkRootModule struct{}

func (frameworkRootModule) Name() string                    { return "framework-root-trespasser" }
func (frameworkRootModule) Init(*app.ModuleDeps) error      { return nil }
func (frameworkRootModule) Shutdown() error                 { return nil }
func (frameworkRootModule) raw(server.HandlerContext) error { return nil }

func (frameworkRootModule) typed(frameworkRootReq, server.HandlerContext) (frameworkRootResp, server.IAPIError) {
	return frameworkRootResp{}, nil
}

func (m frameworkRootModule) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.GET(hr, r, "/_sys/helper", m.typed)
	server.RegisterHandler(hr, r, http.MethodGet, "/_sys/direct", m.typed)
	embedded := embeddingRegistrar{r}
	server.GET(hr, embedded, "/_sys/embedded", m.typed)
	embedded.Add(http.MethodGet, "/_sys/raw", m.raw)
	server.GET(hr, r, "/orders", m.typed)
}

// TestModuleRoutesUnderFrameworkRootsRefused pins ADR-168's attribution through ModuleRegistry:
// a route is judged by its handler function's package, so a module route under <base>/_sys is
// refused whichever door registered it. A caller frame would name app for the direct
// RegisterHandler call (frame 3 is ModuleRegistry) and server for the embedding registrar's
// typed fallback, and admit both.
func TestModuleRoutesUnderFrameworkRootsRefused(t *testing.T) {
	server.DefaultRouteRegistry.Clear()
	t.Cleanup(server.DefaultRouteRegistry.Clear)
	cfg := &config.Config{App: config.AppConfig{Name: "framework-root-admission", Env: "test", Version: "1.0.0"}}
	cfg.Server.Path.Base = "/api"
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Resolver.Type = config.ResolverTypePath
	cfg.Multitenant.Resolver.Path.Segment = 2
	log := logger.New("disabled", true)
	srv := server.New(cfg, log)

	registry := app.NewModuleRegistry(&app.ModuleDeps{Logger: log, Config: cfg})
	require.NoError(t, registry.Register(frameworkRootModule{}))
	registry.RegisterRoutes(srv.ModuleGroup())

	want := map[string]string{
		"/api/_sys/helper":   thisPackage,
		"/api/_sys/direct":   thisPackage,
		"/api/_sys/embedded": "github.com/gaborage/go-bricks/server", // the typed fallback's adapter
		"/api/_sys/raw":      thisPackage,
	}
	got := map[string]string{}
	for _, c := range srv.RouteConflicts() {
		assert.Equal(t, server.RouteConflictReasonFrameworkRoot, c.Reason, c.Path)
		assert.Equal(t, "/api/_sys", c.FirstPath, c.Path)
		got[c.Path] = c.Duplicate.Package
	}
	assert.Equal(t, want, got, "every door refused, /api/orders admitted")
	require.ErrorIs(t, srv.Start(), server.ErrDuplicateRoute)
}
