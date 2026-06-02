//go:build integration

package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/testing/containers"
)

// quiesceCLIEnv stands up a PG container and a single-tenant config file naming
// it as the control-plane target. Unlike newCLIEnv it needs no Flyway, so the
// quiesce CLI path runs wherever Docker is available.
type quiesceCLIEnv struct {
	configPath string
	tenant     string
}

func newQuiesceCLIEnv(t *testing.T) *quiesceCLIEnv {
	t.Helper()
	parent, cancelParent := testCtx(t)
	t.Cleanup(cancelParent)
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	t.Cleanup(cancel)

	cfg := containers.DefaultPostgreSQLConfig()
	pg := containers.MustStartPostgreSQLContainer(ctx, t, cfg).WithCleanup(t)
	host, err := pg.Host(ctx)
	require.NoError(t, err)
	port, err := pg.MappedPort(ctx)
	require.NoError(t, err)

	const tenant = "control-plane"
	var sb bytes.Buffer
	sb.WriteString("multitenant:\n  enabled: true\n  tenants:\n")
	fmt.Fprintf(&sb, "    %s:\n      database:\n", tenant)
	fmt.Fprintf(&sb, "        type: %s\n        host: %q\n        port: %d\n", config.PostgreSQL, host, port)
	fmt.Fprintf(&sb, "        username: %q\n        password: %q\n        database: %q\n        tls:\n          mode: disable\n",
		cfg.Username, cfg.Password, cfg.Database)

	configPath := filepath.Join(t.TempDir(), "tenants.yaml")
	require.NoError(t, os.WriteFile(configPath, sb.Bytes(), 0o600))
	return &quiesceCLIEnv{configPath: configPath, tenant: tenant}
}

// run executes a fresh quiesce command tree with the env's source/credential
// flags plus the given subcommand args, returning combined output and the error.
func (e *quiesceCLIEnv) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewQuiesceCommand()
	full := append([]string{"--source-config", e.configPath, "--credentials-from", "config-file", "--tenant", e.tenant}, args...)
	root.SetArgs(full)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	ctx, cancel := testCtx(t)
	defer cancel()
	root.SetContext(ctx)
	err := root.Execute()
	return out.String(), err
}

func TestCLIQuiesceSetStatusClearEndToEnd(t *testing.T) {
	env := newQuiesceCLIEnv(t)

	out, err := env.run(t, "status")
	require.NoError(t, err, out)
	assert.Contains(t, out, "not quiesced")

	out, err = env.run(t, "set", "--applied-by", "deployer@ci", "--reason", "deploy-9", "--ttl", "1h")
	require.NoError(t, err, out)
	assert.Contains(t, out, "Quiesce flag set")

	out, err = env.run(t, "status", "--json")
	require.NoError(t, err, out)
	assert.Contains(t, out, `"active": true`)
	assert.Contains(t, out, `"setBy": "deployer@ci"`)

	out, err = env.run(t, "clear", "--applied-by", "ops-oncall")
	require.NoError(t, err, out)
	assert.Contains(t, out, "cleared")

	// After an explicit clear the row persists with cleared_at set, so status
	// reports "cleared" (distinct from "not quiesced", which means no row).
	out, err = env.run(t, "status", "--json")
	require.NoError(t, err, out)
	assert.Contains(t, out, `"active": false`)
	assert.Contains(t, out, `"cleared": true`)
}

func TestCLIQuiesceClearWhenInactiveSucceeds(t *testing.T) {
	env := newQuiesceCLIEnv(t)
	out, err := env.run(t, "clear", "--applied-by", "ops")
	require.NoError(t, err, out)
	assert.Contains(t, out, "No active quiesce flag")
}
