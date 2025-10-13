//go:build integration

package containers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PostgreSQLContainerConfig holds configuration for PostgreSQL test container
type PostgreSQLContainerConfig struct {
	// ImageTag specifies the PostgreSQL version (default: "17-alpine")
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
// The returned configuration sets ImageTag to "17-alpine", Username to "testuser", Password to "testpass",
// Database to "testdb", and StartupTimeout to 60 seconds.
func DefaultPostgreSQLConfig() *PostgreSQLContainerConfig {
	return &PostgreSQLContainerConfig{
		ImageTag:       "17-alpine",
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
}

// StartPostgreSQLContainer starts a PostgreSQL testcontainer using the provided configuration.
// If cfg is nil, DefaultPostgreSQLConfig is used. If Docker is not available the test is
// skipped with a clear message. On success it returns a PostgreSQLContainer wrapping the
// running container and its connection string; on failure it returns a non-nil error.
func StartPostgreSQLContainer(ctx context.Context, t *testing.T, cfg *PostgreSQLContainerConfig) (*PostgreSQLContainer, error) {
	t.Helper()

	if cfg == nil {
		cfg = DefaultPostgreSQLConfig()
	}

	// Check if Docker is available - skip test if not
	if !isDockerAvailable(ctx) {
		t.Skip("Docker is not available - skipping integration test. Install Docker Desktop or ensure Docker daemon is running.")
		return nil, nil // Never reached due to Skip, but satisfies return
	}

	// Create PostgreSQL container with configuration
	pgContainer, err := postgres.Run(ctx,
		fmt.Sprintf("postgres:%s", cfg.ImageTag),
		postgres.WithDatabase(cfg.Database),
		postgres.WithUsername(cfg.Username),
		postgres.WithPassword(cfg.Password),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2). // Postgres restarts after initial setup
				WithStartupTimeout(cfg.StartupTimeout),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start PostgreSQL container: %w", err)
	}

	// Get connection string
	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		// Clean up container on error
		_ = pgContainer.Terminate(ctx)
		return nil, fmt.Errorf("failed to get PostgreSQL connection string: %w", err)
	}

	t.Logf("PostgreSQL container started successfully at %s", maskConnectionString(connStr))

	return &PostgreSQLContainer{
		container: pgContainer,
		connStr:   connStr,
	}, nil
}

// ConnectionString returns the PostgreSQL connection string
func (p *PostgreSQLContainer) ConnectionString() string {
	return p.connStr
}

// Terminate stops and removes the PostgreSQL container
func (p *PostgreSQLContainer) Terminate(ctx context.Context) error {
	if p.container == nil {
		return nil
	}
	return p.container.Terminate(ctx)
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
	return mappedPort.Int(), nil
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
