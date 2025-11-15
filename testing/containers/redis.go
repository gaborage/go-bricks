//go:build integration

package containers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// RedisContainerConfig holds configuration for Redis test container
type RedisContainerConfig struct {
	// ImageTag specifies the Redis version (default: "7-alpine")
	ImageTag string
	// StartupTimeout for container initialization (default: 60 seconds)
	StartupTimeout time.Duration
}

// DefaultRedisConfig returns a RedisContainerConfig populated with sensible defaults.
//
// The returned configuration sets ImageTag to "7-alpine" and StartupTimeout to 60 seconds.
func DefaultRedisConfig() *RedisContainerConfig {
	return &RedisContainerConfig{
		ImageTag:       "7-alpine",
		StartupTimeout: 60 * time.Second,
	}
}

// RedisContainer wraps testcontainers Redis container with helper methods
type RedisContainer struct {
	container *redis.RedisContainer
	host      string
	port      int
}

// StartRedisContainer starts a Redis testcontainer using the provided configuration.
// If cfg is nil, DefaultRedisConfig is used. If Docker is not available the test is
// skipped with a clear message. On success it returns a RedisContainer wrapping the
// running container and its connection details; on failure it returns a non-nil error.
func StartRedisContainer(ctx context.Context, t *testing.T, cfg *RedisContainerConfig) (*RedisContainer, error) {
	t.Helper()

	if cfg == nil {
		cfg = DefaultRedisConfig()
	}

	// Check if Docker is available - skip test if not
	if !isDockerAvailable(ctx) {
		t.Skip("Docker is not available - skipping integration test. Install Docker Desktop or ensure Docker daemon is running.")
		return nil, nil // Never reached due to Skip, but satisfies return
	}

	// Create Redis container with configuration
	redisContainer, err := redis.Run(ctx,
		fmt.Sprintf("redis:%s", cfg.ImageTag),
		testcontainers.WithWaitStrategy(
			wait.ForLog("Ready to accept connections").
				WithStartupTimeout(cfg.StartupTimeout),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start Redis container: %w", err)
	}

	// Get host and port
	host, err := redisContainer.Host(ctx)
	if err != nil {
		_ = redisContainer.Terminate(ctx)
		return nil, fmt.Errorf("failed to get Redis host: %w", err)
	}

	mappedPort, err := redisContainer.MappedPort(ctx, "6379/tcp")
	if err != nil {
		_ = redisContainer.Terminate(ctx)
		return nil, fmt.Errorf("failed to get Redis port: %w", err)
	}

	port := mappedPort.Int()

	t.Logf("Redis container started successfully at %s:%d", host, port)

	return &RedisContainer{
		container: redisContainer,
		host:      host,
		port:      port,
	}, nil
}

// Host returns the container host
func (r *RedisContainer) Host() string {
	return r.host
}

// Port returns the mapped Redis port (6379)
func (r *RedisContainer) Port() int {
	return r.port
}

// Terminate stops and removes the Redis container
func (r *RedisContainer) Terminate(ctx context.Context) error {
	if r.container == nil {
		return nil
	}
	return r.container.Terminate(ctx)
}

// MustStartRedisContainer starts a Redis test container and fails the test if startup fails.
//
// It is a convenience wrapper around StartRedisContainer that calls t.Fatalf on any error and
// returns the started *RedisContainer when successful.
func MustStartRedisContainer(ctx context.Context, t *testing.T, cfg *RedisContainerConfig) *RedisContainer {
	t.Helper()

	container, err := StartRedisContainer(ctx, t, cfg)
	if err != nil {
		t.Fatalf("Failed to start Redis container: %v", err)
	}

	return container
}

// WithCleanup registers a cleanup function to terminate the container when the test finishes
func (r *RedisContainer) WithCleanup(t *testing.T) *RedisContainer {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if err := r.Terminate(ctx); err != nil {
			t.Logf("Warning: failed to terminate Redis container: %v", err)
		}
	})
	return r
}
