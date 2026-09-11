//go:build integration

package containers

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

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

// StartRedisTLSContainer starts a TLS-only Redis testcontainer presenting
// certPEM/keyPEM and trusting caPEM, using the same image as the plaintext
// helper. If cfg is nil, DefaultRedisConfig is used. If Docker is not available
// the test is skipped with a clear message.
//
// The server's plaintext listener is disabled (`--port 0`) and TLS is bound to
// 6379 instead, so Host() and Port() address the TLS listener exactly as they
// address the plaintext one — a caller only has to turn TLS on client-side.
// Client certificates are not required (`--tls-auth-clients no`): this helper
// exists to exercise server verification, and demanding a client cert would
// make every negative test fail for the wrong reason.
func StartRedisTLSContainer(ctx context.Context, t *testing.T, cfg *RedisContainerConfig, caPEM, certPEM, keyPEM []byte) (*RedisContainer, error) {
	t.Helper()

	if cfg == nil {
		cfg = DefaultRedisConfig()
	}

	if !isDockerAvailable(ctx) {
		t.Skip(DockerUnavailableSkipMessage)
		return nil, nil // Never reached due to Skip, but satisfies return
	}

	redisContainer, err := redis.Run(ctx,
		fmt.Sprintf("redis:%s", cfg.ImageTag),
		redisTLSOptions(cfg, caPEM, certPEM, keyPEM)...,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start TLS Redis container: %w", err)
	}

	cc, err := newRedisContainer(ctx, redisContainer)
	if err != nil {
		return nil, err
	}

	t.Logf("TLS Redis container started successfully at %s:%d", cc.host, cc.port)

	return cc, nil
}

// MustStartRedisTLSContainer starts a TLS-only Redis test container and fails
// the test if startup fails. It mirrors MustStartRedisContainer.
func MustStartRedisTLSContainer(ctx context.Context, t *testing.T, cfg *RedisContainerConfig, caPEM, certPEM, keyPEM []byte) *RedisContainer {
	t.Helper()

	container, err := StartRedisTLSContainer(ctx, t, cfg, caPEM, certPEM, keyPEM)
	if err != nil {
		t.Fatalf("Failed to start TLS Redis container: %v", err)
	}

	return container
}

// redisTLSOptions builds the customizers that turn the stock image into a
// TLS-only server: the PEM material copied in, the command flags that bind it,
// and the wait strategy.
//
// WithCmdArgs (not WithCmd) is what the upstream module uses for its own TLS
// mode: the image's entrypoint prepends `redis-server` to any argument list
// starting with a dash, so passing bare flags replaces the default command
// without naming the binary twice.
//
// The wait strategy still watches for the readiness log and a listening 6379 —
// the TLS listener binds the same port number, so a TCP connect succeeds there
// even though nothing plaintext is served on it.
func redisTLSOptions(cfg *RedisContainerConfig, caPEM, certPEM, keyPEM []byte) []testcontainers.ContainerCustomizer {
	return []testcontainers.ContainerCustomizer{
		testcontainers.WithFiles(
			testcontainers.ContainerFile{
				Reader:            bytes.NewReader(caPEM),
				ContainerFilePath: redisTLSCAPath,
				FileMode:          redisTLSFileMode,
			},
			testcontainers.ContainerFile{
				Reader:            bytes.NewReader(certPEM),
				ContainerFilePath: redisTLSCertPath,
				FileMode:          redisTLSFileMode,
			},
			testcontainers.ContainerFile{
				Reader:            bytes.NewReader(keyPEM),
				ContainerFilePath: redisTLSKeyPath,
				FileMode:          redisTLSFileMode,
			},
		),
		testcontainers.WithCmdArgs(
			"--tls-port", strings.TrimSuffix(redisPort, "/tcp"),
			"--port", "0",
			"--tls-cert-file", redisTLSCertPath,
			"--tls-key-file", redisTLSKeyPath,
			"--tls-ca-cert-file", redisTLSCAPath,
			"--tls-auth-clients", "no",
		),
		waitOptionWithin(cfg.StartupTimeout,
			wait.ForLog("Ready to accept connections"),
			wait.ForListeningPort(redisPort),
		),
	}
}
