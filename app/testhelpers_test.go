package app

import (
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/server"
)

// minimalAppConfig is the smallest config NewWithConfig accepts: no Messaging and no
// Database block at all.
func minimalAppConfig(basePath string) *config.Config {
	return &config.Config{
		App: config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
		Server: config.ServerConfig{
			Port:    8080,
			Timeout: defaultTestConfig().Server.Timeout,
			Path:    config.PathConfig{Base: basePath},
		},
		Multitenant: config.MultitenantConfig{Enabled: false},
		Log:         config.LogConfig{Level: "error"},
	}
}

// newConfiguredApp builds through the public constructor, so Options travel their real path.
func newConfiguredApp(t *testing.T, cfg *config.Config, opts *Options) *App {
	t.Helper()
	a, _, err := NewWithConfig(cfg, opts)
	require.NoError(t, err)
	t.Cleanup(func() {
		if a.messagingManager != nil {
			a.messagingManager.StopCleanup()
		}
		if a.dbManager != nil {
			a.dbManager.StopCleanup()
		}
	})
	return a
}

// routeTableModule registers one raw route and one typed route that names its own module.
type routeTableModule struct{ name string }

type routeTableRequest struct{}

func (m *routeTableModule) Name() string             { return m.name }
func (m *routeTableModule) Init(_ *ModuleDeps) error { return nil }
func (m *routeTableModule) Shutdown() error          { return nil }
func (m *routeTableModule) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	r.Add(http.MethodGet, "/"+m.name, func(c server.HandlerContext) error { return c.String(http.StatusOK, "") })
	server.POST(hr, r, "/"+m.name, func(routeTableRequest, server.HandlerContext) (string, server.IAPIError) {
		return "", nil
	}, server.WithModule("billing"))
}

func handlerIDs(routes []server.RouteDescriptor) []string {
	ids := make([]string, len(routes))
	for i := range routes {
		ids[i] = routes[i].HandlerID
	}
	return ids
}

// configLoadMu serializes os.Chdir + config.Load() so callers of
// loadConfigFromYAML are safe under t.Parallel() if anyone adds it later.
var configLoadMu sync.Mutex

// loadConfigFromYAML writes the given YAML to a temp config.yaml and loads
// it via config.Load(). It restores the working directory before returning, so
// callers are not exposed to the temporary chdir.
func loadConfigFromYAML(t *testing.T, yaml string) *config.Config {
	t.Helper()

	configLoadMu.Lock()
	defer configLoadMu.Unlock()

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o600))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	defer func() { _ = os.Chdir(origDir) }()

	cfg, err := config.Load()
	require.NoError(t, err)
	return cfg
}

// minimumValidConfig contains the keys config.Load() requires to pass
// validation (app + server + database). Tests append their own sections.
const minimumValidConfig = `
app:
  name: test-app
  version: 1.0.0
  env: development
server:
  host: localhost
  port: 8080
database:
  type: postgresql
  host: localhost
  port: 5432
  database: testdb
  username: testuser
  password: testpass
log:
  level: info
`
