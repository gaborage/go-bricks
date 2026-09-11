//go:build integration

package containers

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// redisPort is the container-side port every Redis wait strategy and MappedPort lookup
// addresses.
const redisPort = "6379/tcp"

// redisTerminateTimeout bounds the teardown of a container whose startup
// failed, independent of the caller's (likely expired) startup deadline.
const redisTerminateTimeout = 30 * time.Second

// Container-side paths the TLS material is copied to. They match the layout the
// upstream redis module uses, so a reader comparing the two sees one scheme.
const (
	redisTLSCAPath   = "/tls/ca.crt"
	redisTLSCertPath = "/tls/server.crt"
	redisTLSKeyPath  = "/tls/server.key"
)

// redisTLSFileMode makes the copied PEM world-readable: redis-server drops to
// an unprivileged user before opening them, so 0o600 owned by root would make
// the server fail to start rather than fail to verify.
const redisTLSFileMode = 0o644

// RedisTLSMaterial is the PEM material a TLS-only Redis container serves: the
// certificate and key it presents, and the CA it is signed by (which is also
// what a client must trust).
type RedisTLSMaterial struct {
	CA   []byte
	Cert []byte
	Key  []byte
}

// RedisContainerConfig holds configuration for Redis test container
type RedisContainerConfig struct {
	// ImageTag specifies the Redis version (default: the pin in DefaultRedisConfig)
	ImageTag string
	// StartupTimeout for container initialization (default: 60 seconds)
	StartupTimeout time.Duration
	// TLS, when non-nil, makes the server TLS-only: the plaintext listener is
	// disabled and TLS is bound to 6379 instead, so Host() and Port() address
	// the TLS listener exactly as they address the plaintext one — a caller only
	// has to turn TLS on client-side. Nil (the default) serves plaintext.
	TLS *RedisTLSMaterial
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

// redisOptions builds the container customizers, wait strategy included.
//
// Composite wait strategy: log message (fast early signal) + port listening (network
// verification) prevents a race where the log appears but Redis is not ready to accept
// connections.
//
// With cfg.TLS set, the PEM material is copied in and bound by command flags.
// WithCmdArgs (not WithCmd) is what the upstream module uses for its own TLS
// mode: the image's entrypoint prepends `redis-server` to any argument list
// starting with a dash, so passing bare flags replaces the default command
// without naming the binary twice. `--port 0` disables the plaintext listener
// while TLS binds the same 6379, so the listening-port wait still holds.
// Client certificates are not required (`--tls-auth-clients no`): the fixture
// exercises server verification, and demanding a client cert would make every
// negative test fail for the wrong reason.
func redisOptions(cfg *RedisContainerConfig) []testcontainers.ContainerCustomizer {
	opts := make([]testcontainers.ContainerCustomizer, 0, 3)
	opts = append(opts, waitOptionWithin(cfg.StartupTimeout,
		wait.ForLog("Ready to accept connections"),
		wait.ForListeningPort(redisPort),
	))
	if cfg.TLS == nil {
		return opts
	}

	files := make([]testcontainers.ContainerFile, 0, 3)
	for _, f := range []struct {
		path string
		pem  []byte
	}{
		{redisTLSCAPath, cfg.TLS.CA},
		{redisTLSCertPath, cfg.TLS.Cert},
		{redisTLSKeyPath, cfg.TLS.Key},
	} {
		files = append(files, testcontainers.ContainerFile{
			Reader:            bytes.NewReader(f.pem),
			ContainerFilePath: f.path,
			FileMode:          redisTLSFileMode,
		})
	}

	return append(opts,
		testcontainers.WithFiles(files...),
		testcontainers.WithCmdArgs(
			"--tls-port", strings.TrimSuffix(redisPort, "/tcp"),
			"--port", "0",
			"--tls-cert-file", redisTLSCertPath,
			"--tls-key-file", redisTLSKeyPath,
			"--tls-ca-cert-file", redisTLSCAPath,
			"--tls-auth-clients", "no",
		),
	)
}

// startRedisContainerInternal does the actual testcontainer setup without
// any *testing.T interaction. Both StartRedisContainer (which adds *T-bound
// Skip/Logf) and StartRedisContainerForTestMain wrap it.
func startRedisContainerInternal(ctx context.Context, cfg *RedisContainerConfig) (*RedisContainer, error) {
	if cfg == nil {
		cfg = DefaultRedisConfig()
	}

	// redis.Run can hand back a started container together with an error (a
	// wait-strategy timeout, say), so terminate it before returning — the same
	// contract newRedisContainer honors for its own later failures.
	redisContainer, err := redis.Run(ctx,
		fmt.Sprintf("redis:%s", cfg.ImageTag),
		redisOptions(cfg)...,
	)
	if err != nil {
		if redisContainer != nil {
			terminateOnFailure(ctx, redisContainer)
		}
		return nil, fmt.Errorf("failed to start Redis container: %w", err)
	}

	return newRedisContainer(ctx, redisContainer)
}

// terminateOnFailure tears down a container whose startup failed. The
// caller's ctx is usually the reason it failed (a startup deadline), so the
// teardown runs on a context that keeps its values but not its cancellation,
// bounded on its own. The result is ignored: the startup error is the one to
// report.
func terminateOnFailure(ctx context.Context, c *redis.RedisContainer) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), redisTerminateTimeout)
	defer cancel()
	_ = c.Terminate(cleanupCtx)
}

// newRedisContainer resolves the host-side address of a started container and
// wraps it. On any lookup failure it terminates the container it was handed —
// the caller never receives a half-built wrapper it would have to clean up.
func newRedisContainer(ctx context.Context, redisContainer *redis.RedisContainer) (*RedisContainer, error) {
	host, err := redisContainer.Host(ctx)
	if err != nil {
		terminateOnFailure(ctx, redisContainer)
		return nil, fmt.Errorf("failed to get Redis host: %w", err)
	}

	mappedPort, err := redisContainer.MappedPort(ctx, redisPort)
	if err != nil {
		terminateOnFailure(ctx, redisContainer)
		return nil, fmt.Errorf("failed to get Redis port: %w", err)
	}

	return &RedisContainer{
		container: redisContainer,
		host:      host,
		port:      int(mappedPort.Num()),
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
