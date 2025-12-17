//go:build integration

package containers

import (
	"context"
	"fmt"
	"testing"
	"time"

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

	// Oracle container environment variables
	env := map[string]string{
		"ORACLE_PASSWORD": cfg.Password,
		"APP_USER":        cfg.AppUser,
		"APP_USER_PASSWORD": cfg.Password,
	}

	// Create container request
	// Use composite wait strategy: log message (fast early signal) + port listening (network verification)
	// This prevents race conditions where the log appears but Oracle isn't ready to accept connections
	req := testcontainers.ContainerRequest{
		Image:        fmt.Sprintf("gvenzl/oracle-free:%s", cfg.ImageTag),
		ExposedPorts: []string{"1521/tcp"},
		Env:          env,
		WaitingFor: wait.ForAll(
			wait.ForLog("DATABASE IS READY TO USE!"),
			wait.ForListeningPort("1521/tcp"),
		).WithStartupTimeout(cfg.StartupTimeout),
	}

	// Start container
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start Oracle container: %w", err)
	}

	// Get host and port
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

	port := mappedPort.Int()

	// Build connection string for go-ora driver
	// Format: oracle://user:password@host:port/service
	connStr := fmt.Sprintf("oracle://%s:%s@%s:%d/%s",
		cfg.AppUser, cfg.Password, host, port, cfg.Database)

	t.Logf("Oracle container started successfully at %s:%d (service: %s, user: %s)",
		host, port, cfg.Database, cfg.AppUser)

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

// Terminate stops and removes the Oracle container
func (o *OracleContainer) Terminate(ctx context.Context) error {
	if o.container == nil {
		return nil
	}
	return o.container.Terminate(ctx)
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
