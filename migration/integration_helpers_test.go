//go:build integration

package migration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/testing/containers"
)

// flywayOrSkip returns the absolute path to the host's flyway binary, or
// t.Skips when Flyway isn't on PATH. Mirrors the Docker-availability skip
// pattern in testing/containers/postgresql.go so local runs without Flyway
// installed degrade to a skip rather than a hard failure. CI's framework-
// integration-test job installs the binary so this branch is exercised.
//
// Called lazily by flywayConfig() / migrator() so PG-only role-separation
// tests can use the same integrationEnv without requiring Flyway.
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

// integrationEnv bundles a per-test PG database plus the on-disk Flyway
// scaffolding (config file + migrations dir). One env can host multiple
// tenant databases — call createTenantDB to provision additional ones.
type integrationEnv struct {
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

// newIntegrationEnv provisions a fresh database on the package's shared PG
// container (pkgPG, ADR-020) + scaffolds a per-test Flyway config and
// migrations directory. The Flyway-or-skip check is deferred until
// flywayConfig() / migrator() is called so PG-only role-separation tests
// don't require Flyway on PATH.
//
// Roles are cluster-scoped, so dropping the test's database does not reclaim
// them; sweepNewRoles drops every role created while the test ran. That
// sweep makes this package's integration tests serial by contract: none may
// call t.Parallel (so -parallel is a no-op here), because one test's sweep
// would drop the roles of a test running alongside it. A test that changes
// cluster-wide settings (ALTER SYSTEM) uses newPrivateIntegrationEnv instead.
func newIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()

	pg := pkgPG.Get(t)
	// Registered before NewDatabase so cleanups (last in, first out) run the
	// sweep after the DROP DATABASE, when the test's roles own nothing.
	sweepNewRoles(t, pg.ConnectionString())
	return newEnvFor(t, pg.NewDatabase(t))
}

// newPrivateIntegrationEnv starts a PG container for this test alone, for a
// test whose cluster-wide changes (ALTER SYSTEM) no sweep restores and would
// otherwise leak into every later test on pkgPG.
func newPrivateIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()

	parent, cancelParent := testCtx(t)
	t.Cleanup(cancelParent)
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	t.Cleanup(cancel)

	pg := containers.MustStartPostgreSQLContainer(ctx, t, nil).WithCleanup(t)
	return newEnvFor(t, pg.NewDatabase(t))
}

// sweepNewRoles snapshots pg_roles now and registers a cleanup that drops,
// in one statement, every role created since, so the next test on the shared
// container starts with the bootstrap role set. adminDSN must address the
// container's own database: the test's database is gone when the sweep runs.
// A failed sweep fails the test rather than leaking roles into the next one.
func sweepNewRoles(t *testing.T, adminDSN string) {
	t.Helper()
	db, err := sql.Open("pgx", adminDSN)
	require.NoError(t, err, "open admin connection for the role sweep")
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := testCtx(t)
	defer cancel()
	before, err := roleNames(ctx, db)
	require.NoError(t, err, "snapshot pg_roles")

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		after, err := roleNames(ctx, db)
		if err != nil {
			t.Errorf("list pg_roles for the role sweep: %v", err)
			return
		}
		var created []string
		for name := range after {
			if _, ok := before[name]; !ok {
				created = append(created, quotePGIdent(name))
			}
		}
		if len(created) == 0 {
			return
		}
		slices.Sort(created)
		roles := strings.Join(created, ", ")
		// SECURITY: Manual SQL review completed - DROP ROLE takes no bind
		// parameters, so the names are interpolated. Each comes from the
		// pg_roles catalog, not from caller input, and quotePGIdent wraps it as
		// a PostgreSQL delimited identifier with embedded quotes doubled, which
		// also keeps mixed-case names such as "Public" intact.
		if _, err := db.ExecContext(ctx, "DROP ROLE "+roles); err != nil { // NOSONAR: catalog role names, PG-identifier-quoted; DROP ROLE takes no bind parameters
			t.Errorf("drop roles created during the test (%s): %v", roles, err)
		}
	})
}

// roleNames returns the name of every role in the cluster.
func roleNames(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT rolname FROM pg_roles`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	names := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names[name] = struct{}{}
	}
	return names, rows.Err()
}

// newEnvFor scaffolds a per-test Flyway config and migrations directory
// targeting db, whose credentials become the env's admin user.
func newEnvFor(t *testing.T, db *containers.PostgreSQLDatabase) *integrationEnv {
	t.Helper()

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
		db.Host, db.Port,
	)
	require.NoError(t, os.WriteFile(configPath, []byte(conf), 0o600))

	return &integrationEnv{
		configPath:    configPath,
		migrationsDir: migrationsDir,
		host:          db.Host,
		port:          db.Port,
		adminUser:     db.Username,
		adminPassword: db.Password,
		defaultDB:     db.Database,
		logger:        logger.New("disabled", true),
	}
}

// createTenantDB creates an additional PG database on the running container
// for tests that exercise per-tenant isolation, dropped when the test ends
// because callers pass fixed, cluster-scoped names. Returns a
// *config.DatabaseConfig pointing at the new database with the container
// admin credentials.
func (e *integrationEnv) createTenantDB(t *testing.T, dbName string) *config.DatabaseConfig {
	t.Helper()
	db := e.adminDB(t)

	ctx, cancel := testCtx(t)
	defer cancel()
	// %q here is Go string-literal quoting, not PostgreSQL identifier
	// quoting — coincidentally compatible for the ASCII test tenant names
	// callers pass. Do not extend to caller-controlled input.
	if _, err := db.ExecContext(ctx,
		fmt.Sprintf(`CREATE DATABASE %q`, dbName)); err != nil {
		t.Fatalf("create database %q: %v", dbName, err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), time.Minute)
		defer dropCancel()
		// SECURITY: Manual SQL review completed - same interpolated identifier
		// as the CREATE above: dbName is a fixed [a-z_] test constant, over
		// which Go's %q and PostgreSQL delimited-identifier quoting coincide; %q
		// is Go quoting, so do not extend this site to external input. FORCE
		// evicts a connection the test left open so it cannot block the drop.
		if _, err := db.ExecContext(dropCtx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, dbName)); err != nil { // NOSONAR: fixed test identifier; DROP DATABASE takes no bind parameters
			t.Errorf("drop database %q: %v", dbName, err)
		}
	})

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
// fields (e.g. Timeout, Audit) for their scenario. Lazily resolves the
// Flyway binary on PATH and t.Skips when absent so PG-only tests reusing
// integrationEnv don't require Flyway.
func (e *integrationEnv) flywayConfig(t *testing.T) *Config {
	t.Helper()
	if e.flywayPath == "" {
		e.flywayPath = flywayOrSkip(t)
	}
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

// adminDB returns a *sql.DB authenticated as the container admin user against
// the env's default database. Used by role-provisioning integration tests to
// create roles and schemas before connecting as the freshly-provisioned roles.
// The connection is closed automatically when the test ends.
func (e *integrationEnv) adminDB(t *testing.T) *sql.DB {
	t.Helper()
	return e.openAsRole(t, e.adminUser, e.adminPassword)
}

// adminConn returns a framework database.Interface authenticated as the
// container admin user against the env's default database. Used by the
// transaction-scoped provisioning tests, which need database.WithTx and the
// database.Tx that ProvisionPGRolesTx consumes. Closed when the test ends.
func (e *integrationEnv) adminConn(t *testing.T) database.Interface {
	t.Helper()
	conn, err := database.NewConnection(e.dbConfigFor(e.defaultDB), e.logger)
	require.NoError(t, err, "open framework connection as admin")
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// roleDSN returns the DSN that authenticates as role with password against the
// env's default database.
func (e *integrationEnv) roleDSN(role, password string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		role, password, e.host, e.port, e.defaultDB)
}

// openAsRole returns a *sql.DB authenticated as the given PostgreSQL role
// using the env's default database. Used by role-separation tests to verify
// that DDL is rejected for runtime roles and that DML is permitted for
// migrator-owned tables via ALTER DEFAULT PRIVILEGES.
func (e *integrationEnv) openAsRole(t *testing.T, role, password string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", e.roleDSN(role, password))
	require.NoError(t, err, "open connection as role %q", role)
	t.Cleanup(func() { _ = db.Close() })

	// Eagerly verify the credential — sql.Open is lazy and a bad password
	// would otherwise surface inside the test body, masking the credential
	// problem as a feature failure.
	ctx, cancel := testCtx(t)
	defer cancel()
	require.NoError(t, db.PingContext(ctx), "ping as role %q", role)
	return db
}
