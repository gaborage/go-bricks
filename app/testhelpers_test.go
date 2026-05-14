package app

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

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
