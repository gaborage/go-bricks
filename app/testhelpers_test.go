package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

// loadConfigFromYAML writes the given YAML to a temp config.yaml and loads
// it via config.Load(). It restores the original working directory on cleanup.
func loadConfigFromYAML(t *testing.T, yaml string) *config.Config {
	t.Helper()

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0o600))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})
	require.NoError(t, os.Chdir(tmpDir))

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
