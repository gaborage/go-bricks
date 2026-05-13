//go:build integration

package commands

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/testing/containers"
)

// flywayOrSkip mirrors the migration package's helper of the same name —
// duplicated rather than exported to keep the testing/containers import
// surface narrow on the cross-module boundary.
func flywayOrSkip(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("flyway")
	if err != nil {
		t.Skip("flyway CLI not available on PATH; install Flyway 10+ locally or run via CI")
	}
	return path
}

// cliEnv bundles the PG container + scaffolded YAML/migrations dirs the
// CLI integration tests need. The YAML config drives both tenant listing
// and credential resolution (--source-config + --credentials-from=config-file).
type cliEnv struct {
	flywayPath    string
	host          string
	port          int
	adminUser     string
	adminPassword string
	defaultDB     string
	configPath    string
	flywayConfig  string
	migrationsDir string
	tenantIDs     []string
}

// newCLIEnv stands up a PG container with N tenant databases (one per
// tenantID), writes a multitenant YAML pointing at them, and scaffolds a
// flyway.conf + migrations dir under t.TempDir().
func newCLIEnv(t *testing.T, tenantIDs ...string) *cliEnv {
	t.Helper()
	flywayPath := flywayOrSkip(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	containerCfg := containers.DefaultPostgreSQLConfig()
	pg := containers.MustStartPostgreSQLContainer(ctx, t, containerCfg).WithCleanup(t)

	host, err := pg.Host(ctx)
	require.NoError(t, err)
	port, err := pg.MappedPort(ctx)
	require.NoError(t, err)

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		containerCfg.Username, containerCfg.Password, host, port, containerCfg.Database)
	admin, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })

	for _, id := range tenantIDs {
		if _, err = admin.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %q`, id)); err != nil {
			t.Fatalf("create database %q: %v", id, err)
		}
	}

	dir := t.TempDir()
	migrationsDir := filepath.Join(dir, "migrations")
	require.NoError(t, os.MkdirAll(migrationsDir, 0o755))

	// flyway.conf uses ${env.DB_*} substitution so a single conf file
	// serves all tenants — the framework's buildEnvironmentVariables wires
	// the per-tenant DB_NAME into the subprocess env.
	flywayConf := filepath.Join(dir, "flyway.conf")
	conf := "flyway.url=jdbc:postgresql://${env.DB_HOST}:${env.DB_PORT}/${env.DB_NAME}\n" +
		"flyway.user=${env.DB_USER}\n" +
		"flyway.password=${env.DB_PASSWORD}\n" +
		"flyway.cleanDisabled=false\n"
	require.NoError(t, os.WriteFile(flywayConf, []byte(conf), 0o600))

	// multitenant YAML — one entry per tenant ID pointing at the
	// just-created PG database. --credentials-from=config-file pulls
	// credentials from this same file.
	var sb strings.Builder
	sb.WriteString("multitenant:\n  enabled: true\n  tenants:\n")
	for _, id := range tenantIDs {
		fmt.Fprintf(&sb, "    %s:\n", id)
		sb.WriteString("      database:\n")
		fmt.Fprintf(&sb, "        type: %s\n        host: %q\n        port: %d\n", config.PostgreSQL, host, port)
		fmt.Fprintf(&sb, "        username: %q\n        password: %q\n        database: %q\n",
			containerCfg.Username, containerCfg.Password, id)
	}
	configPath := filepath.Join(dir, "tenants.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(sb.String()), 0o600))

	return &cliEnv{
		flywayPath:    flywayPath,
		host:          host,
		port:          port,
		adminUser:     containerCfg.Username,
		adminPassword: containerCfg.Password,
		defaultDB:     containerCfg.Database,
		configPath:    configPath,
		flywayConfig:  flywayConf,
		migrationsDir: migrationsDir,
		tenantIDs:     tenantIDs,
	}
}

// writeMigration adds a versioned SQL file to the migrations dir.
func (e *cliEnv) writeMigration(t *testing.T, version int, description, body string) {
	t.Helper()
	path := filepath.Join(e.migrationsDir, fmt.Sprintf("V%d__%s.sql", version, description))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

// execIn runs arbitrary SQL against the named tenant DB. Used to
// pre-poison a tenant with conflicting state to exercise the
// "one-of-three-fails" path.
func (e *cliEnv) execIn(t *testing.T, dbName, query string) {
	t.Helper()
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		e.adminUser, e.adminPassword, e.host, e.port, dbName)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(context.Background(), query)
	require.NoError(t, err)
}

// runMigrate invokes the migrate cobra command with the env's flags +
// --source-config / --credentials-from=config-file / --json. Returns the
// raw JSON stream + Execute()'s error so callers can assert exit semantics.
func (e *cliEnv) runMigrate(t *testing.T, extraArgs ...string) (string, error) {
	t.Helper()
	cmd := NewMigrateCommand()
	args := append([]string{
		"--source-config", e.configPath,
		"--credentials-from", "config-file",
		"--flyway-path", e.flywayPath,
		"--flyway-config", e.flywayConfig,
		"--migrations-dir", e.migrationsDir,
		"--json",
	}, extraArgs...)
	cmd.SetArgs(args)
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return stdout.String(), err
}

// parseTenantEvents pulls all newline-delimited tenant_complete events out
// of the CLI's JSON stream, keyed by tenant ID. Skips the summary event
// and any non-JSON lines cobra emits on error (Usage: banner, Error: prefix).
func parseTenantEvents(t *testing.T, stream string) map[string]map[string]any {
	t.Helper()
	events := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(stream), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev["event"] == "tenant_complete" {
			events[ev["tenant_id"].(string)] = ev
		}
	}
	return events
}

// TestCLIMigrateEndToEndMultiTenantWithFailure exercises go-bricks-migrate
// against a 3-tenant batch where one tenant has been pre-poisoned with a
// conflicting table. With --continue-on-error, the healthy tenants
// succeed, the poisoned tenant fails, exit code is non-zero, and the JSON
// stream carries the structured per-tenant Result fields.
func TestCLIMigrateEndToEndMultiTenantWithFailure(t *testing.T) {
	env := newCLIEnv(t, "tenant_a", "tenant_b", "tenant_c")

	env.writeMigration(t, 1, "create_users",
		"CREATE TABLE users (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL);")
	env.writeMigration(t, 2, "create_orders",
		"CREATE TABLE orders (id BIGSERIAL PRIMARY KEY, user_id BIGINT NOT NULL);")

	// Poison tenant_b: a conflicting table exists, so V1's CREATE TABLE
	// will fail when Flyway runs against this tenant.
	env.execIn(t, "tenant_b", "CREATE TABLE users (existing_column INT);")

	stream, err := env.runMigrate(t, "--continue-on-error")
	require.Error(t, err, "one tenant fails so the process must exit non-zero")
	assert.Contains(t, err.Error(), "one or more tenants failed")

	events := parseTenantEvents(t, stream)
	require.Contains(t, events, "tenant_a")
	require.Contains(t, events, "tenant_b")
	require.Contains(t, events, "tenant_c")

	assert.Equal(t, "ok", events["tenant_a"]["status"])
	assert.Equal(t, "fail", events["tenant_b"]["status"])
	assert.Equal(t, "ok", events["tenant_c"]["status"])

	// Successful tenants surface the structured Result fields in the JSON
	// stream — tenant_a / tenant_c each applied versions 1+2.
	for _, id := range []string{"tenant_a", "tenant_c"} {
		applied, ok := events[id]["applied_versions"].([]any)
		require.Truef(t, ok, "%s should expose applied_versions when status=ok", id)
		assert.Equal(t, []any{"1", "2"}, applied, "tenant %s applied versions", id)
		assert.Equal(t, "2", events[id]["ending_version"], "tenant %s ending_version", id)
	}
}

// TestCLIMigrateEndToEndIdempotentRerun verifies that a second migrate
// invocation against already-migrated tenants is a no-op: exit zero, no
// applied_versions in the stream (omit-when-empty), ending_version still
// reflects the schema terminus.
func TestCLIMigrateEndToEndIdempotentRerun(t *testing.T) {
	env := newCLIEnv(t, "tenant_a", "tenant_b")

	env.writeMigration(t, 1, "create_users",
		"CREATE TABLE users (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL);")

	// First run: both tenants apply V1.
	stream1, err := env.runMigrate(t)
	require.NoError(t, err)
	events1 := parseTenantEvents(t, stream1)
	require.Len(t, events1, 2)
	for id, ev := range events1 {
		assert.Equalf(t, "ok", ev["status"], "first run tenant %s status", id)
		applied, ok := ev["applied_versions"].([]any)
		require.Truef(t, ok, "first run tenant %s applied_versions present", id)
		assert.Equal(t, []any{"1"}, applied)
	}

	// Second run: same migrations, no work to do.
	stream2, err := env.runMigrate(t)
	require.NoError(t, err, "idempotent rerun must exit zero")
	events2 := parseTenantEvents(t, stream2)
	require.Len(t, events2, 2)
	for id, ev := range events2 {
		assert.Equalf(t, "ok", ev["status"], "rerun tenant %s status", id)
		// applied_versions is omitted when Result.AppliedVersions is empty
		// (omit-when-empty convention in the CLI hook).
		_, present := ev["applied_versions"]
		assert.Falsef(t, present, "rerun tenant %s must omit applied_versions on no-op", id)
		assert.Equal(t, "1", ev["ending_version"], "rerun tenant %s ending_version mirrors starting_version", id)
	}
}
