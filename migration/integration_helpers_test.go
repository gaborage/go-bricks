//go:build integration

package migration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/testing/containers"
)

// flywayOrSkip returns the absolute path to the host's flyway binary, or
// t.Skips when Flyway isn't on PATH. Mirrors the Docker-availability skip
// pattern in testing/containers/postgresql.go so local runs without Flyway
// installed degrade to a skip rather than a hard failure. CI's framework-
// integration-test job installs the binary so this branch is exercised.
func flywayOrSkip(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("flyway")
	if err != nil {
		t.Skip("flyway CLI not available on PATH; install Flyway 10+ locally or run via CI")
	}
	return path
}

// testCtx returns a context that honors the test's deadline (if any), so DB
// operations issued from helpers cannot outlive the test's allowed runtime.
// Falls back to a plain cancel-only context when the test has no deadline.
func testCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	if dl, ok := t.Deadline(); ok {
		return context.WithDeadline(context.Background(), dl)
	}
	return context.WithCancel(context.Background())
}

// integrationEnv bundles a per-test PG container plus the on-disk Flyway
// scaffolding (config file + migrations dir). One env can host multiple
// tenant databases — call createTenantDB to provision additional ones.
type integrationEnv struct {
	container     *containers.PostgreSQLContainer
	flywayPath    string
	configPath    string
	migrationsDir string
	host          string
	port          int
	adminUser     string
	adminPassword string
	defaultDB     string
	logger        logger.Logger
}

// newIntegrationEnv starts a PG testcontainer + scaffolds a per-test Flyway
// config and migrations directory. flywayOrSkip is checked first so tests
// fail-fast on hosts without Flyway rather than spending the testcontainer
// startup time only to skip afterward.
func newIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()
	flywayPath := flywayOrSkip(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	cfg := containers.DefaultPostgreSQLConfig()
	pg := containers.MustStartPostgreSQLContainer(ctx, t, cfg).WithCleanup(t)

	host, err := pg.Host(ctx)
	require.NoError(t, err, "pg container host")
	port, err := pg.MappedPort(ctx)
	require.NoError(t, err, "pg container port")

	dir := t.TempDir()
	migrationsDir := filepath.Join(dir, "migrations")
	require.NoError(t, os.MkdirAll(migrationsDir, 0o755))

	// Flyway 10 ${env.X} substitution flows the per-tenant DB_NAME (set by
	// FlywayMigrator.buildEnvironmentVariables) through to the JDBC URL, so
	// one shared conf can serve multi-tenant runs without rewriting per call.
	configPath := filepath.Join(dir, "flyway.conf")
	conf := fmt.Sprintf(
		"flyway.url=jdbc:postgresql://%s:%d/${env.DB_NAME}\n"+
			"flyway.user=${env.DB_USER}\n"+
			"flyway.password=${env.DB_PASSWORD}\n"+
			"flyway.cleanDisabled=false\n",
		host, port,
	)
	require.NoError(t, os.WriteFile(configPath, []byte(conf), 0o600))

	return &integrationEnv{
		container:     pg,
		flywayPath:    flywayPath,
		configPath:    configPath,
		migrationsDir: migrationsDir,
		host:          host,
		port:          port,
		adminUser:     cfg.Username,
		adminPassword: cfg.Password,
		defaultDB:     cfg.Database,
		logger:        logger.New("disabled", true),
	}
}

// createTenantDB creates an additional PG database on the running container
// for tests that exercise per-tenant isolation. Returns a *config.DatabaseConfig
// pointing at the new database with the container admin credentials.
func (e *integrationEnv) createTenantDB(t *testing.T, dbName string) *config.DatabaseConfig {
	t.Helper()
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		e.adminUser, e.adminPassword, e.host, e.port, e.defaultDB)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err, "open admin connection")
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := testCtx(t)
	defer cancel()
	// %q here is Go string-literal quoting, not PostgreSQL identifier
	// quoting — coincidentally compatible for the ASCII test tenant names
	// callers pass. Do not extend to caller-controlled input.
	if _, err = db.ExecContext(ctx,
		fmt.Sprintf(`CREATE DATABASE %q`, dbName)); err != nil {
		t.Fatalf("create database %q: %v", dbName, err)
	}

	return e.dbConfigFor(dbName)
}

// dbConfigFor returns a *config.DatabaseConfig pointing at the named PG
// database on the running container. The database must already exist
// (either createTenantDB or the container's default).
func (e *integrationEnv) dbConfigFor(dbName string) *config.DatabaseConfig {
	return &config.DatabaseConfig{
		Type:     config.PostgreSQL,
		Host:     e.host,
		Port:     e.port,
		Username: e.adminUser,
		Password: e.adminPassword,
		Database: dbName,
	}
}

// flywayConfig returns a *Config wired with the env's binary + conf +
// migrations directory. Tests typically copy this and override individual
// fields (e.g. Timeout, Audit) for their scenario.
func (e *integrationEnv) flywayConfig() *Config {
	return &Config{
		FlywayPath:    e.flywayPath,
		ConfigPath:    e.configPath,
		MigrationPath: e.migrationsDir,
		Timeout:       2 * time.Minute,
		Environment:   "test",
	}
}

// writeMigration writes a versioned migration script into the env's
// migrations directory, returning the file path so callers can later tamper
// with it (e.g. to exercise checksum-mismatch detection).
func (e *integrationEnv) writeMigration(t *testing.T, version int, description, body string) string {
	t.Helper()
	name := fmt.Sprintf("V%d__%s.sql", version, description)
	path := filepath.Join(e.migrationsDir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// migrator returns a *FlywayMigrator wired against the env's PG container.
// The migrator's own *config.Config supplies only the App.Env value; tests
// pass per-call *DatabaseConfig values via MigrateFor for tenant targeting.
func (e *integrationEnv) migrator() *FlywayMigrator {
	cfg := &config.Config{
		Database: *e.dbConfigFor(e.defaultDB),
		App:      config.AppConfig{Env: "test"},
	}
	return NewFlywayMigrator(cfg, e.logger)
}

// schemaHistoryEntries opens an admin connection to the named database and
// returns the version + description rows from flyway_schema_history in
// application order. Used to verify that advisory-lock-serialized runs
// produced exactly one row per applied version (no duplicates).
func (e *integrationEnv) schemaHistoryEntries(t *testing.T, dbName string) []schemaHistoryRow {
	t.Helper()
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		e.adminUser, e.adminPassword, e.host, e.port, dbName)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := testCtx(t)
	defer cancel()
	rows, err := db.QueryContext(ctx,
		`SELECT version, description, success FROM flyway_schema_history WHERE version IS NOT NULL ORDER BY installed_rank`)
	require.NoError(t, err, "query schema history")
	defer func() { _ = rows.Close() }()

	var out []schemaHistoryRow
	for rows.Next() {
		var r schemaHistoryRow
		require.NoError(t, rows.Scan(&r.Version, &r.Description, &r.Success))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

type schemaHistoryRow struct {
	Version     string
	Description string
	Success     bool
}
