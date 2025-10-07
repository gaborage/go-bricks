//go:build integration

package containers

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"github.com/testcontainers/testcontainers-go/wait"
)

// MongoDBContainerConfig holds configuration for MongoDB test container
type MongoDBContainerConfig struct {
	// ImageTag specifies the MongoDB version (default: "8.0")
	ImageTag string
	// Username for MongoDB authentication (default: "testuser")
	Username string
	// Password for MongoDB authentication (default: "testpass")
	Password string
	// Database name to create (default: "testdb")
	Database string
	// StartupTimeout for container initialization (default: 60 seconds)
	StartupTimeout time.Duration
}

// DefaultMongoDBConfig returns a MongoDBContainerConfig populated with sensible defaults.
//
// The returned configuration sets ImageTag to "8.0", Username to "testuser", Password to "testpass",
// Database to "testdb", and StartupTimeout to 60 seconds.
func DefaultMongoDBConfig() *MongoDBContainerConfig {
	return &MongoDBContainerConfig{
		ImageTag:       "8.0",
		Username:       "testuser",
		Password:       "testpass",
		Database:       "testdb",
		StartupTimeout: 60 * time.Second,
	}
}

// MongoDBContainer wraps testcontainers MongoDB container with helper methods
type MongoDBContainer struct {
	container *mongodb.MongoDBContainer
	connStr   string
}

// StartMongoDBContainer starts a MongoDB testcontainer using the provided configuration.
// If cfg is nil, DefaultMongoDBConfig is used. If Docker is not available the test is
// skipped with a clear message. On success it returns a MongoDBContainer wrapping the
// running container and its connection string; on failure it returns a non-nil error.
func StartMongoDBContainer(ctx context.Context, t *testing.T, cfg *MongoDBContainerConfig) (*MongoDBContainer, error) {
	t.Helper()

	if cfg == nil {
		cfg = DefaultMongoDBConfig()
	}

	// Check if Docker is available - skip test if not
	if !isDockerAvailable(ctx) {
		t.Skip("Docker is not available - skipping integration test. Install Docker Desktop or ensure Docker daemon is running.")
		return nil, nil // Never reached due to Skip, but satisfies return
	}

	// Create MongoDB container with configuration
	mongoContainer, err := mongodb.Run(ctx,
		fmt.Sprintf("mongo:%s", cfg.ImageTag),
		mongodb.WithUsername(cfg.Username),
		mongodb.WithPassword(cfg.Password),
		testcontainers.WithWaitStrategy(
			wait.ForLog("Waiting for connections").
				WithStartupTimeout(cfg.StartupTimeout),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start MongoDB container: %w", err)
	}

	// Get connection string
	connStr, err := mongoContainer.ConnectionString(ctx)
	if err != nil {
		// Clean up container on error
		_ = mongoContainer.Terminate(ctx)
		return nil, fmt.Errorf("failed to get MongoDB connection string: %w", err)
	}

	t.Logf("MongoDB container started successfully at %s", redactConnectionString(connStr))

	return &MongoDBContainer{
		container: mongoContainer,
		connStr:   connStr,
	}, nil
}

// ConnectionString returns the MongoDB connection string
func (m *MongoDBContainer) ConnectionString() string {
	return m.connStr
}

// Terminate stops and removes the MongoDB container
func (m *MongoDBContainer) Terminate(ctx context.Context) error {
	if m.container == nil {
		return nil
	}
	return m.container.Terminate(ctx)
}

// Host returns the container host
func (m *MongoDBContainer) Host(ctx context.Context) (string, error) {
	if m.container == nil {
		return "", fmt.Errorf("container not initialized")
	}
	return m.container.Host(ctx)
}

// MappedPort returns the mapped port for MongoDB
func (m *MongoDBContainer) MappedPort(ctx context.Context) (int, error) {
	if m.container == nil {
		return 0, fmt.Errorf("container not initialized")
	}
	mappedPort, err := m.container.MappedPort(ctx, "27017/tcp")
	if err != nil {
		return 0, err
	}
	return mappedPort.Int(), nil
}

// redactConnectionString removes password from MongoDB connection string for safe logging.
// Supports both mongodb:// and mongodb+srv:// schemes.
// Returns a generic masked placeholder if parsing fails to avoid leaking credentials.
func redactConnectionString(connStr string) string {
	u, err := url.Parse(connStr)
	if err != nil {
		// If parsing fails, return a generic message without credentials
		return "mongodb://****:****@<host>:<port>"
	}

	// If there's user info, redact the password
	if u.User != nil {
		username := u.User.Username()
		if username != "" {
			// Replace password with asterisks
			u.User = url.UserPassword(username, "****")
		}
	}

	return u.String()
}

// isDockerAvailable reports whether a Docker daemon is reachable via the testcontainers Docker provider.
// It returns true if the provider can obtain the daemon host, false otherwise.
func isDockerAvailable(ctx context.Context) bool {
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return false
	}
	defer provider.Close()

	// Try to get Docker info - if this fails, Docker is not available
	_, err = provider.DaemonHost(ctx)
	return err == nil
}

// MustStartMongoDBContainer starts a MongoDB test container and fails the test if startup fails.
//
// It is a convenience wrapper around StartMongoDBContainer that calls t.Fatalf on any error and
// returns the started *MongoDBContainer when successful.
func MustStartMongoDBContainer(ctx context.Context, t *testing.T, cfg *MongoDBContainerConfig) *MongoDBContainer {
	t.Helper()

	container, err := StartMongoDBContainer(ctx, t, cfg)
	if err != nil {
		t.Fatalf("Failed to start MongoDB container: %v", err)
	}

	return container
}

// WithCleanup registers a cleanup function to terminate the container when the test finishes
func (m *MongoDBContainer) WithCleanup(t *testing.T) *MongoDBContainer {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if err := m.Terminate(ctx); err != nil {
			t.Logf("Warning: failed to terminate MongoDB container: %v", err)
		}
	})
	return m
}
