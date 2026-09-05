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
	// ImageTag specifies the Redis version (default: the pin in DefaultRedisConfig)
	ImageTag string
	// StartupTimeout for container initialization (default: 60 seconds)
	StartupTimeout time.Duration
}

// DefaultRedisConfig returns a RedisContainerConfig populated with sensible defaults.
//
// The returned configuration sets StartupTimeout to 60 seconds.
func DefaultRedisConfig() *RedisContainerConfig {
	return &RedisContainerConfig{
		// renovate: datasource=docker depName=redis
		ImageTag:       "8.10.1-alpine",
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

	if !isDockerAvailable(ctx) {
		t.Skip(DockerUnavailableSkipMessage)
		return nil, nil // Never reached due to Skip, but satisfies return
	}

	cc, err := startRedisContainerInternal(ctx, cfg)
	if err != nil {
		return nil, err
	}

	t.Logf("Redis container started successfully at %s:%d", cc.host, cc.port)

	return cc, nil
}

// StartRedisContainerForTestMain starts a Redis test container without
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
func StartRedisContainerForTestMain(ctx context.Context, cfg *RedisContainerConfig) (container *RedisContainer, dockerAvailable bool, err error) {
	if !isDockerAvailable(ctx) {
		return nil, false, nil
	}
	cc, err := startRedisContainerInternal(ctx, cfg)
	if err != nil {
		return nil, true, err
	}
	return cc, true, nil
}

// startRedisContainerInternal does the actual testcontainer setup without
// any *testing.T interaction. Both StartRedisContainer (which adds *T-bound
// Skip/Logf) and StartRedisContainerForTestMain wrap it.
func startRedisContainerInternal(ctx context.Context, cfg *RedisContainerConfig) (*RedisContainer, error) {
	if cfg == nil {
		cfg = DefaultRedisConfig()
	}

	// Use composite wait strategy: log message (fast early signal) + port listening (network verification)
	// This prevents race conditions where the log appears but Redis isn't ready to accept connections
	redisContainer, err := redis.Run(ctx,
		fmt.Sprintf("redis:%s", cfg.ImageTag),
		testcontainers.WithWaitStrategy(
			wait.ForAll(
				wait.ForLog("Ready to accept connections"),
				wait.ForListeningPort("6379/tcp"),
			).WithStartupTimeout(cfg.StartupTimeout),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start Redis container: %w", err)
	}

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

	port := int(mappedPort.Num())

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

// Port returns the host-side port Docker mapped to the container's 6379.
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

// WithCleanup registers a cleanup function to terminate the container when the test finishes.
// Uses a 30-second timeout to prevent hanging if Docker misbehaves during teardown.
func (r *RedisContainer) WithCleanup(t *testing.T) *RedisContainer {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := r.Terminate(ctx); err != nil {
			t.Logf("Warning: failed to terminate Redis container: %v", err)
		}
	})
	return r
}
