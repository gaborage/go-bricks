//go:build integration

package containers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx stdlib driver for the admin *sql.DB NewDatabase uses
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// maxDatabaseNameLen is PostgreSQL's identifier length limit (NAMEDATALEN-1).
const maxDatabaseNameLen = 63

// errAdminAfterTerminate is what admin() returns once Terminate has closed the
// pool, so a NewDatabase running after the suite tore the container down fails
// with a named cause instead of "sql: database is closed".
var errAdminAfterTerminate = errors.New("postgresql container: admin pool used after Terminate")

// PostgreSQLContainerConfig holds configuration for PostgreSQL test container
type PostgreSQLContainerConfig struct {
	// ImageTag specifies the PostgreSQL version (default: the pin in DefaultPostgreSQLConfig)
	ImageTag string
	// Username for PostgreSQL authentication (default: "testuser")
	Username string
	// Password for PostgreSQL authentication (default: "testpass")
	Password string
	// Database name to create (default: "testdb")
	Database string
	// StartupTimeout for container initialization (default: 60 seconds)
	StartupTimeout time.Duration
}

// DefaultPostgreSQLConfig returns a PostgreSQLContainerConfig populated with sensible defaults.
//
// The returned configuration sets Username to "testuser", Password to "testpass",
// Database to "testdb", and StartupTimeout to 60 seconds.
func DefaultPostgreSQLConfig() *PostgreSQLContainerConfig {
	return &PostgreSQLContainerConfig{
		// renovate: datasource=docker depName=postgres
		ImageTag:       "18.6-alpine",
		Username:       "testuser",
		Password:       "testpass",
		Database:       "testdb",
		StartupTimeout: 60 * time.Second,
	}
}

// PostgreSQLContainer wraps testcontainers PostgreSQL container with helper methods
type PostgreSQLContainer struct {
	container *postgres.PostgresContainer
	connStr   string
	host      string
	port      int
	username  string
	password  string

	// adminMu guards the three fields below: admin() opens lazily on a test
	// goroutine while Terminate closes on TestMain's, so both go through it.
	adminMu     sync.Mutex
	adminDB     *sql.DB
	adminErr    error
	adminClosed bool
}

// StartPostgreSQLContainer starts a PostgreSQL testcontainer using the provided configuration.
// If cfg is nil, DefaultPostgreSQLConfig is used. If Docker is not available the test is
// skipped with a clear message. On success it returns a PostgreSQLContainer wrapping the
// running container and its connection string; on failure it returns a non-nil error.
func StartPostgreSQLContainer(ctx context.Context, t *testing.T, cfg *PostgreSQLContainerConfig) (*PostgreSQLContainer, error) {
	t.Helper()

	if !isDockerAvailable(ctx) {
		t.Skip(DockerUnavailableSkipMessage)
		return nil, nil // Never reached due to Skip, but satisfies return
	}

	cc, err := startPostgreSQLContainerInternal(ctx, cfg)
	if err != nil {
		return nil, err
	}

	t.Logf("PostgreSQL container started successfully at %s", maskConnectionString(cc.connStr))

	return cc, nil
}

// StartPostgreSQLContainerForTestMain starts a PostgreSQL test container without
// requiring a *testing.T. Intended for package-level TestMain usage where
// container provisioning must happen before m.Run() and *T is unavailable.
//
// Returns (container, true, nil) on success.
// Returns (nil, false, nil) when Docker is unavailable — what that means is the
// caller's decision: a package whose tests are all integration tests may log and
// os.Exit(0), while a package that also holds unit tests hands the tuple to
// containers.Shared, which skips only the requesting test.
// Returns (nil, true, err) when Docker is available but startup failed.
//
// Callers are responsible for invoking Terminate after m.Run().
func StartPostgreSQLContainerForTestMain(ctx context.Context, cfg *PostgreSQLContainerConfig) (container *PostgreSQLContainer, dockerAvailable bool, err error) {
	if !isDockerAvailable(ctx) {
		return nil, false, nil
	}
	cc, err := startPostgreSQLContainerInternal(ctx, cfg)
	if err != nil {
		return nil, true, err
	}
	return cc, true, nil
}

// startPostgreSQLContainerInternal does the actual testcontainer setup without
// any *testing.T interaction. Both StartPostgreSQLContainer (which adds *T-bound
// Skip/Logf) and StartPostgreSQLContainerForTestMain wrap it.
func startPostgreSQLContainerInternal(ctx context.Context, cfg *PostgreSQLContainerConfig) (*PostgreSQLContainer, error) {
	if cfg == nil {
		cfg = DefaultPostgreSQLConfig()
	}

	// Use composite wait strategy: log message (fast early signal) + port listening (network verification)
	// This prevents race conditions where the log appears but PostgreSQL isn't ready to accept connections
	pgContainer, err := postgres.Run(ctx,
		fmt.Sprintf("postgres:%s", cfg.ImageTag),
		postgres.WithDatabase(cfg.Database),
		postgres.WithUsername(cfg.Username),
		postgres.WithPassword(cfg.Password),
		testcontainers.WithWaitStrategy(
			wait.ForAll(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2), // Postgres restarts after initial setup
				wait.ForListeningPort("5432/tcp"),
			).WithStartupTimeout(cfg.StartupTimeout),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start PostgreSQL container: %w", err)
	}

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		return nil, fmt.Errorf("failed to get PostgreSQL connection string: %w", err)
	}

	host, err := pgContainer.Host(ctx)
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		return nil, fmt.Errorf("failed to get PostgreSQL host: %w", err)
	}

	mappedPort, err := pgContainer.MappedPort(ctx, "5432/tcp")
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		return nil, fmt.Errorf("failed to get PostgreSQL port: %w", err)
	}

	return &PostgreSQLContainer{
		container: pgContainer,
		connStr:   connStr,
		host:      host,
		port:      int(mappedPort.Num()),
		username:  cfg.Username,
		password:  cfg.Password,
	}, nil
}

// ConnectionString returns the PostgreSQL connection string
func (p *PostgreSQLContainer) ConnectionString() string {
	return p.connStr
}

// Terminate stops and removes the PostgreSQL container. Also closes the cached
// admin connection pool used by NewDatabase (see admin()).
func (p *PostgreSQLContainer) Terminate(ctx context.Context) error {
	p.closeAdmin()
	if p.container == nil {
		return nil
	}
	return p.container.Terminate(ctx)
}

// closeAdmin closes and forgets the cached admin pool, then marks it terminated.
// Holding adminMu across both means a NewDatabase racing Terminate either gets
// the live handle or errAdminAfterTerminate, never one closed under it.
func (p *PostgreSQLContainer) closeAdmin() {
	p.adminMu.Lock()
	defer p.adminMu.Unlock()

	if p.adminDB != nil {
		_ = p.adminDB.Close()
		p.adminDB = nil
	}
	p.adminClosed = true
}

// admin returns a lazily-initialized *sql.DB connected to the container's default
// database. The handle is reused across every NewDatabase CREATE / DROP for the
// lifetime of the container, so provisioning 20+ test databases pays the connection
// handshake once instead of twice per test. A failed open is cached like a
// successful one, so a broken driver is not re-dialed once per test.
func (p *PostgreSQLContainer) admin() (*sql.DB, error) {
	p.adminMu.Lock()
	defer p.adminMu.Unlock()

	if p.adminClosed {
		return nil, errAdminAfterTerminate
	}
	if p.adminDB == nil && p.adminErr == nil {
		p.adminDB, p.adminErr = sql.Open("pgx", p.connStr)
	}
	return p.adminDB, p.adminErr
}

// Host returns the container host
func (p *PostgreSQLContainer) Host(ctx context.Context) (string, error) {
	if p.container == nil {
		return "", fmt.Errorf("container not initialized")
	}
	return p.container.Host(ctx)
}

// MappedPort returns the mapped port for PostgreSQL
func (p *PostgreSQLContainer) MappedPort(ctx context.Context) (int, error) {
	if p.container == nil {
		return 0, fmt.Errorf("container not initialized")
	}
	mappedPort, err := p.container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return 0, err
	}
	return int(mappedPort.Num()), nil
}

// MustStartPostgreSQLContainer starts a PostgreSQL test container and fails the test if startup fails.
//
// It is a convenience wrapper around StartPostgreSQLContainer that calls t.Fatalf on any error and
// returns the started *PostgreSQLContainer when successful.
func MustStartPostgreSQLContainer(ctx context.Context, t *testing.T, cfg *PostgreSQLContainerConfig) *PostgreSQLContainer {
	t.Helper()

	container, err := StartPostgreSQLContainer(ctx, t, cfg)
	if err != nil {
		t.Fatalf("Failed to start PostgreSQL container: %v", err)
	}

	return container
}

// WithCleanup registers a cleanup function to terminate the container when the test finishes
func (p *PostgreSQLContainer) WithCleanup(t *testing.T) *PostgreSQLContainer {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if err := p.Terminate(ctx); err != nil {
			t.Logf("Warning: failed to terminate PostgreSQL container: %v", err)
		}
	})
	return p
}

// PostgreSQLDatabase represents an isolated database provisioned on a shared
// PostgreSQLContainer. Callers build their own *sql.DB or framework *Connection
// from these credentials with whichever DatabaseConfig they need (pool sizes,
// timezone, keep-alive, TLS mode). DROP DATABASE registered in NewDatabase is the
// cleanup contract — tests must not rely on dropping their own objects by name.
type PostgreSQLDatabase struct {
	Host     string
	Port     int
	Database string
	Username string
	Password string
}

// ConnectionString returns the postgres:// DSN bound to this database.
func (d *PostgreSQLDatabase) ConnectionString() string {
	// url.UserPassword escapes credentials containing @ : or /, and JoinHostPort
	// brackets an IPv6 literal, which some Docker setups return from Host().
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(d.Username, d.Password),
		Host:     net.JoinHostPort(d.Host, strconv.Itoa(d.Port)),
		Path:     "/" + d.Database,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

// NewDatabase provisions a fresh randomly-named database on the container and
// returns its credentials. The name follows the form <sanitized test name>_<8
// random chars>; a t.Cleanup registered here drops it WITH (FORCE), reclaiming
// every object the test created provided those objects live inside this database.
//
// Tests that CREATE TABLE with fixed names therefore stay isolated from each other
// exactly as they were when every test booted its own server.
func (p *PostgreSQLContainer) NewDatabase(t *testing.T) *PostgreSQLDatabase {
	t.Helper()

	admin, err := p.admin()
	if err != nil {
		t.Fatalf("open admin connection to PostgreSQL: %v", err)
	}

	name := randomDatabaseName(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// SECURITY: Manual SQL review completed - CREATE DATABASE takes no
	// placeholders, so the identifier is interpolated. name comes from
	// randomDatabaseName, which emits only [a-z0-9_]: the lowercased t.Name()
	// mapped onto that alphabet plus 8 lowercased crypto/rand.Text() chars
	// ([a-z2-7]). Over that alphabet Go's %q quoting and PostgreSQL
	// delimited-identifier quoting coincide — but %q is Go string-literal
	// quoting, not PG identifier quoting, so do not extend this site to
	// caller-controlled input.
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		t.Fatalf("create test database %s: %v", name, err)
	}

	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer dropCancel()

		// SECURITY: Manual SQL review completed - same interpolated identifier as
		// the CREATE above: randomDatabaseName emits [a-z0-9_] only (test name
		// plus 8 lowercased crypto/rand.Text() chars, [a-z2-7]), an alphabet over
		// which Go's %q and PostgreSQL delimited-identifier quoting coincide. %q
		// is Go quoting, not PG quoting — do not extend to caller-controlled
		// input. FORCE evicts connections the test left open so the drop cannot
		// be blocked by a leaked pool member.
		if _, dropErr := admin.ExecContext(dropCtx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, name)); dropErr != nil {
			t.Logf("Warning: failed to drop test database %s: %v", name, dropErr)
		}
	})

	return &PostgreSQLDatabase{
		Host:     p.host,
		Port:     p.port,
		Database: name,
		Username: p.username,
		Password: p.password,
	}
}

// randomDatabaseName derives a unique PostgreSQL-safe database name from the
// calling test: lowercase [a-z0-9_] plus a random suffix, capped at 63 bytes.
// That suffix is 8 crypto/rand.Text() base32 chars — 40 bits — and it is the
// whole collision budget both per-test isolation and NewDatabase's WITH (FORCE)
// drop rest on, since two tests landing on one name would drop each other's
// database mid-run rather than merely share it.
func randomDatabaseName(t *testing.T) string {
	t.Helper()

	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, strings.ToLower(t.Name()))

	suffix := "_" + strings.ToLower(rand.Text()[:8])
	if limit := maxDatabaseNameLen - len(suffix); len(sanitized) > limit {
		sanitized = sanitized[:limit]
	}

	return sanitized + suffix
}

// maskConnectionString removes sensitive information from connection string for safe logging.
// It attempts to mask passwords in postgres:// URLs. Returns a generic masked placeholder
// if parsing fails to avoid leaking credentials.
func maskConnectionString(connStr string) string {
	// Simple masking for postgres://user:password@host:port/database format
	// Example: postgres://testuser:****@localhost:54321/testdb?sslmode=disable
	masked := connStr

	// Find password segment (between : and @)
	for i := 0; i < len(masked); i++ {
		if masked[i] == ':' && i+1 < len(masked) {
			// Find the @ symbol after the colon
			for j := i + 1; j < len(masked); j++ {
				if masked[j] == '@' {
					// Mask the password segment
					masked = masked[:i+1] + "****" + masked[j:]
					return masked
				}
			}
		}
	}

	// If parsing fails, return generic message
	return "postgres://****:****@<host>:<port>/<database>"
}
