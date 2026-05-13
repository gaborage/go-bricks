//go:build integration

package containers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	go_ora "github.com/sijms/go-ora/v2"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// OracleContainerConfig holds configuration for Oracle test container
type OracleContainerConfig struct {
	// ImageTag specifies the Oracle version (default: "23-slim")
	ImageTag string
	// Password for SYSTEM, SYS, and APP users (default: "testpass")
	Password string
	// Database name (default: "FREE" for Oracle Free)
	Database string
	// AppUser is the application user to create (default: "testuser")
	AppUser string
	// StartupTimeout for container initialization (default: 120 seconds, Oracle takes longer)
	StartupTimeout time.Duration
}

// DefaultOracleConfig returns an OracleContainerConfig populated with sensible defaults.
//
// The returned configuration sets ImageTag to "23-slim", Password to "testpass",
// Database to "FREEPDB1" (Oracle Free default PDB), AppUser to "testuser", and StartupTimeout to 120 seconds.
func DefaultOracleConfig() *OracleContainerConfig {
	return &OracleContainerConfig{
		ImageTag:       "23-slim",
		Password:       "testpass",
		Database:       "FREEPDB1", // Oracle Free default PDB name
		AppUser:        "testuser",
		StartupTimeout: 120 * time.Second,
	}
}

// OracleContainer wraps testcontainers Oracle container with helper methods
type OracleContainer struct {
	container testcontainers.Container
	connStr   string
	host      string
	port      int
	database  string
	username  string
	password  string

	adminOnce sync.Once
	adminDB   *sql.DB
	adminErr  error
}

// StartOracleContainer starts an Oracle testcontainer using the provided configuration.
// If cfg is nil, DefaultOracleConfig is used. If Docker is not available the test is
// skipped with a clear message. On success it returns an OracleContainer wrapping the
// running container and its connection details; on failure it returns a non-nil error.
//
// Uses gvenzl/oracle-free image (community-maintained, widely adopted).
func StartOracleContainer(ctx context.Context, t *testing.T, cfg *OracleContainerConfig) (*OracleContainer, error) {
	t.Helper()

	if cfg == nil {
		cfg = DefaultOracleConfig()
	}

	// Check if Docker is available - skip test if not
	if !isDockerAvailable(ctx) {
		t.Skip("Docker is not available - skipping integration test. Install Docker Desktop or ensure Docker daemon is running.")
		return nil, nil // Never reached due to Skip, but satisfies return
	}

	cc, err := startOracleContainerInternal(ctx, cfg)
	if err != nil {
		return nil, err
	}

	t.Logf("Oracle container started successfully at %s:%d (service: %s, user: %s)",
		cc.host, cc.port, cc.database, cc.username)

	return cc, nil
}

// StartOracleContainerForTestMain starts an Oracle test container without
// requiring a *testing.T. Intended for package-level TestMain usage where
// container provisioning must happen before m.Run() and *T is unavailable.
//
// Returns (container, true, nil) on success.
// Returns (nil, false, nil) when Docker is unavailable — caller should log a
// message and os.Exit(0) so non-integration runs are unaffected.
// Returns (nil, true, err) when Docker is available but startup failed.
//
// Callers are responsible for invoking Terminate after m.Run().
func StartOracleContainerForTestMain(ctx context.Context, cfg *OracleContainerConfig) (container *OracleContainer, dockerAvailable bool, err error) {
	if !isDockerAvailable(ctx) {
		return nil, false, nil
	}
	cc, err := startOracleContainerInternal(ctx, cfg)
	if err != nil {
		return nil, true, err
	}
	return cc, true, nil
}

// startOracleContainerInternal does the actual testcontainer setup without
// any *testing.T interaction. Both StartOracleContainer (which adds *T-bound
// Skip/Logf) and StartOracleContainerForTestMain wrap it.
func startOracleContainerInternal(ctx context.Context, cfg *OracleContainerConfig) (*OracleContainer, error) {
	if cfg == nil {
		cfg = DefaultOracleConfig()
	}

	env := map[string]string{
		"ORACLE_PASSWORD":   cfg.Password,
		"APP_USER":          cfg.AppUser,
		"APP_USER_PASSWORD": cfg.Password,
	}

	// Composite wait strategy: log message (fast early signal) + port listening
	// (network verification) avoids a race where the log appears before Oracle
	// is ready to accept connections.
	req := testcontainers.ContainerRequest{
		Image:        fmt.Sprintf("gvenzl/oracle-free:%s", cfg.ImageTag),
		ExposedPorts: []string{"1521/tcp"},
		Env:          env,
		WaitingFor: wait.ForAll(
			wait.ForLog("DATABASE IS READY TO USE!"),
			wait.ForListeningPort("1521/tcp"),
		).WithStartupTimeout(cfg.StartupTimeout),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start Oracle container: %w", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("failed to get Oracle container host: %w", err)
	}

	mappedPort, err := container.MappedPort(ctx, "1521")
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("failed to get Oracle container port: %w", err)
	}

	port := int(mappedPort.Num())

	connStr := go_ora.BuildUrl(host, port, cfg.Database, cfg.AppUser, cfg.Password, nil)

	return &OracleContainer{
		container: container,
		connStr:   connStr,
		host:      host,
		port:      port,
		database:  cfg.Database,
		username:  cfg.AppUser,
		password:  cfg.Password,
	}, nil
}

// ConnectionString returns the Oracle connection string (oracle:// format)
func (o *OracleContainer) ConnectionString() string {
	return o.connStr
}

// Host returns the container host
func (o *OracleContainer) Host() string {
	return o.host
}

// Port returns the mapped Oracle port (1521)
func (o *OracleContainer) Port() int {
	return o.port
}

// Database returns the database/service name
func (o *OracleContainer) Database() string {
	return o.database
}

// Username returns the application username
func (o *OracleContainer) Username() string {
	return o.username
}

// Password returns the password
func (o *OracleContainer) Password() string {
	return o.password
}

// Terminate stops and removes the Oracle container. Also closes the cached
// admin connection pool used by NewSchema (see admin()).
func (o *OracleContainer) Terminate(ctx context.Context) error {
	if o.adminDB != nil {
		_ = o.adminDB.Close()
	}
	if o.container == nil {
		return nil
	}
	return o.container.Terminate(ctx)
}

// admin returns a lazily-initialized *sql.DB connected as SYSTEM to the
// container's PDB. The handle is reused across all NewSchema / DROP USER calls
// for the lifetime of the container — go-ora's sql.Open is cheap but the auth
// handshake is not, and provisioning 30+ test schemas would otherwise pay that
// handshake cost twice per test (once for create, once for cleanup).
func (o *OracleContainer) admin() (*sql.DB, error) {
	o.adminOnce.Do(func() {
		o.adminDB, o.adminErr = sql.Open("oracle",
			go_ora.BuildUrl(o.host, o.port, o.database, "system", o.password, nil))
	})
	return o.adminDB, o.adminErr
}

// MustStartOracleContainer starts an Oracle test container and fails the test if startup fails.
//
// It is a convenience wrapper around StartOracleContainer that calls t.Fatalf on any error and
// returns the started *OracleContainer when successful.
func MustStartOracleContainer(ctx context.Context, t *testing.T, cfg *OracleContainerConfig) *OracleContainer {
	t.Helper()

	container, err := StartOracleContainer(ctx, t, cfg)
	if err != nil {
		t.Fatalf("Failed to start Oracle container: %v", err)
	}

	return container
}

// WithCleanup registers a cleanup function to terminate the container when the test finishes
func (o *OracleContainer) WithCleanup(t *testing.T) *OracleContainer {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if err := o.Terminate(ctx); err != nil {
			t.Logf("Warning: failed to terminate Oracle container: %v", err)
		}
	})
	return o
}

// Static SQL for NewSchema — both statements are PL/SQL anonymous blocks that
// take the schema name (and password) as bind variables and use
// DBMS_ASSERT.SIMPLE_SQL_NAME to validate the identifier server-side. This
// keeps the Go-side SQL string literal-constant (no fmt.Sprintf) and adds an
// extra runtime check against malformed identifiers even though the caller-
// side input alphabet is already crypto/rand-constrained.
const (
	dropUserSQL = `
		BEGIN
			EXECUTE IMMEDIATE 'DROP USER ' || DBMS_ASSERT.SIMPLE_SQL_NAME(:1) || ' CASCADE';
		END;
	`

	provisionSchemaSQL = `
		DECLARE
			v_schema VARCHAR2(128) := DBMS_ASSERT.SIMPLE_SQL_NAME(:1);
			v_password VARCHAR2(128) := :2;
		BEGIN
			EXECUTE IMMEDIATE 'CREATE USER ' || v_schema ||
				' IDENTIFIED BY "' || REPLACE(v_password, '"', '""') || '"';
			EXECUTE IMMEDIATE 'GRANT CONNECT, RESOURCE, CREATE TYPE, CREATE PROCEDURE, CREATE SEQUENCE TO ' || v_schema;
			EXECUTE IMMEDIATE 'GRANT UNLIMITED TABLESPACE TO ' || v_schema;
		END;
	`
)

// OracleSchema represents an isolated Oracle user/schema provisioned on a
// shared OracleContainer. Callers build their own *sql.DB or framework
// *Connection from these credentials with whichever DatabaseConfig they need
// (pool sizes, timezone, keep-alive, etc.). DROP USER ... CASCADE registered
// in NewSchema is the cleanup contract — tests must not rely on dropping
// their own objects by name.
type OracleSchema struct {
	Host     string
	Port     int
	Database string
	Username string // schema/user name (uppercase as Oracle stores it)
	Password string
}

// ConnectionString returns the oracle:// DSN bound to this schema.
func (s *OracleSchema) ConnectionString() string {
	return go_ora.BuildUrl(s.Host, s.Port, s.Database, s.Username, s.Password, nil)
}

// NewSchema provisions a fresh randomly-named Oracle user/schema on the
// container and returns its credentials plus a per-test context.
// The schema name follows the form GBTEST_<8-char random hex>; the generated
// password is ephemeral and never logged. A t.Cleanup registered here drops
// the schema with CASCADE when the test finishes, reclaiming every object
// the test created (tables, sequences, UDTs, procedures) provided those
// objects live inside this schema.
//
// Grants applied: CONNECT, RESOURCE, CREATE TYPE, CREATE PROCEDURE,
// CREATE SEQUENCE, plus UNLIMITED TABLESPACE so tests can create tables
// without an ORA-01950 quota error on Oracle 12c+ (RESOURCE no longer carries
// tablespace quota by default).
func (o *OracleContainer) NewSchema(t *testing.T) (*OracleSchema, context.Context) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)

	schemaName := randomSchemaName(t)
	schemaPassword := randomPassword(t)

	adminDB, err := o.admin()
	if err != nil {
		t.Fatalf("open admin connection to Oracle: %v", err)
	}

	// Register DROP USER ... CASCADE immediately so that a failure mid-grant
	// doesn't leak a half-provisioned schema across the test binary's lifetime.
	// Oracle commits DDL implicitly, so a CREATE USER that succeeds before a
	// failing GRANT still leaves the user behind without this cleanup.
	//
	// SECURITY: identifier passed as a bind variable and validated by
	// DBMS_ASSERT.SIMPLE_SQL_NAME inside the PL/SQL block. The Go-side SQL is
	// static (no fmt.Sprintf), and DBMS_ASSERT raises if the identifier isn't
	// a valid unquoted Oracle name — defense in depth on top of the
	// crypto/rand-constrained GBTEST_<hex> alphabet.
	t.Cleanup(func() {
		dropCtx, cancelDrop := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelDrop()
		if _, err := adminDB.ExecContext(dropCtx, dropUserSQL, schemaName); err != nil {
			t.Logf("drop test schema %s: %v", schemaName, err)
		}
	})

	// Batch CREATE USER + GRANTs into a single PL/SQL anonymous block to cut
	// three sequential roundtrips down to one. The block raises on any failure,
	// so a partial-provision scenario is still caught by the cleanup above.
	//
	// SECURITY: schema identifier passed as bind + DBMS_ASSERT.SIMPLE_SQL_NAME
	// validation; password passed as bind and quoted inside the dynamic SQL
	// with single-quote doubling to defang any embedded quote (the random
	// generator emits hex only, but doubling is the canonical safe pattern).
	// Go-side SQL is static.
	if _, err := adminDB.ExecContext(ctx, provisionSchemaSQL, schemaName, schemaPassword); err != nil {
		t.Fatalf("provision test schema %s: %v", schemaName, err)
	}

	return &OracleSchema{
		Host:     o.host,
		Port:     o.port,
		Database: o.database,
		Username: schemaName,
		Password: schemaPassword,
	}, ctx
}

// randomSchemaName returns GBTEST_<8 hex chars uppercase>. 15 chars total,
// well under Oracle's 30-byte identifier limit. Uppercase to match Oracle's
// own folding for unquoted identifiers — go-ora's URL parser does not
// re-fold the username, so the wire form must already be uppercase or
// authentication against the stored uppercase username will fail with
// ORA-01017. Alphanumeric-only so no escaping concerns in SQL.
func randomSchemaName(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("read random schema name: %v", err)
	}
	return "GBTEST_" + strings.ToUpper(hex.EncodeToString(b))
}

// randomPassword returns 24 hex chars (12 random bytes). Hex-only so the value
// is safe inside an Oracle IDENTIFIED BY "..." double-quoted literal — no
// quote, backslash, or wildcard characters to escape. 24 chars stays well
// under Oracle's default 30-byte profile password limit (Oracle accepts
// longer passwords on CREATE without error but silently truncates, causing
// subsequent auth attempts to fail with ORA-01017 — see issue #404).
func randomPassword(t *testing.T) string {
	t.Helper()
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("read random schema password: %v", err)
	}
	return hex.EncodeToString(b)
}
