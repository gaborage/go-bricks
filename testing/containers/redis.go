//go:build integration

package containers

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// redisPort is the container-side port every Redis wait strategy and MappedPort lookup
// addresses.
const redisPort = "6379/tcp"

// redisCLI is the in-container client every bootstrap and readiness exec runs.
const redisCLI = "redis-cli"

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

// Cluster bootstrap knobs. StartupTimeout bounds the cluster-ready wait as well
// as the container start — each leg separately, not the two together, so a
// cluster fixture's worst case is twice the configured value and must still fit
// the caller's own budget (the Shared deadline in integration_main_test.go).
const (
	redisClusterReadyState    = "cluster_state:ok"
	redisClusterBootstrapPoll = 250 * time.Millisecond
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
	// Cluster starts the node with --cluster-enabled yes and gives it every one
	// of the 16384 slots, so one server answers for the whole keyspace over the
	// cluster protocol — the shape Amazon ElastiCache Serverless presents behind
	// its single endpoint. Mutually exclusive with TLS: both replace the
	// container command, and the combination is not a fixture anything needs.
	Cluster bool
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
	// Cluster and TLS are mutually exclusive (startRedisContainerInternal refuses
	// the pair); a switch says so as a shape rather than leaving it to the order
	// two ifs happen to be written in.
	switch {
	case cfg.Cluster:
		return append(opts, testcontainers.WithCmdArgs("--cluster-enabled", "yes"))
	case cfg.TLS == nil:
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

	if cfg.Cluster && cfg.TLS != nil {
		return nil, fmt.Errorf("redis container: cluster and TLS both replace the container command, so they cannot be combined")
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

	c, err := newRedisContainer(ctx, redisContainer)
	if err != nil {
		return nil, err
	}
	if !cfg.Cluster {
		return c, nil
	}
	if err := bootstrapRedisCluster(ctx, c, cfg.StartupTimeout); err != nil {
		terminateOnFailure(ctx, redisContainer)
		return nil, err
	}
	return c, nil
}

// bootstrapRedisCluster turns a freshly started cluster-enabled node into a
// one-node cluster that owns every slot. The node starts owning none and reports
// cluster_state:fail until the claim lands and the cluster cron has run, so the
// claim is followed by a wait rather than assumed.
//
// The announce pair is what makes it reachable: the slot map a cluster client
// follows carries each node's OWN address, which inside Docker is a container IP
// and port no host-side client can dial. Announcing the mapped host address
// makes the node advertise where the test actually reaches it, so the client's
// first redirect lands rather than hangs.
func bootstrapRedisCluster(ctx context.Context, c *RedisContainer, timeout time.Duration) error {
	announceIP, err := resolveAnnounceIP(ctx, c.host)
	if err != nil {
		return err
	}

	for _, args := range [][]string{
		{redisCLI, "config", "set", "cluster-announce-ip", announceIP},
		{redisCLI, "config", "set", "cluster-announce-port", strconv.Itoa(c.port)},
		{redisCLI, "cluster", "addslotsrange", "0", "16383"},
	} {
		if err := execInRedisContainer(ctx, c, args); err != nil {
			return err
		}
	}

	return waitForRedisClusterReady(ctx, c, timeout)
}

// resolveAnnounceIP turns the host side of the mapped address into something
// cluster-announce-ip will take. Host() is not always an IP literal — a TCP
// Docker endpoint or TESTCONTAINERS_HOST_OVERRIDE hands back a name — and some
// Redis builds reject a hostname there outright, which would fail the bootstrap
// at its first CONFIG SET. IPv4 only: the announced address is what a host-side
// client dials back, and the fixture publishes a v4 mapping — so an IPv6 literal
// is refused here rather than announced into a slot map no redirect could reach.
func resolveAnnounceIP(ctx context.Context, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		ip4 := ip.To4()
		if ip4 == nil {
			return "", fmt.Errorf("redis container: %q is an IPv6 literal, and cluster-announce-ip needs the IPv4 address the fixture maps", host)
		}
		return ip4.String(), nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return "", fmt.Errorf("redis container: resolving %q for cluster-announce-ip: %w", host, err)
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("redis container: %q has no IPv4 address to announce as cluster-announce-ip", host)
	}
	return ips[0].String(), nil
}

// execInRedisContainer runs one redis-cli command inside the container, treating
// a non-zero exit as an error. Multiplexed strips the Docker stream framing, so
// the message quotes what redis-cli printed rather than the wire bytes.
func execInRedisContainer(ctx context.Context, c *RedisContainer, args []string) error {
	code, reader, err := c.container.Exec(ctx, args, tcexec.Multiplexed())
	if err != nil {
		return fmt.Errorf("redis container: exec %v: %w", args, err)
	}
	var out bytes.Buffer
	if _, copyErr := out.ReadFrom(reader); copyErr != nil {
		return fmt.Errorf("redis container: reading output of %v: %w", args, copyErr)
	}
	if code != 0 {
		return fmt.Errorf("redis container: %v exited %d: %s", args, code, strings.TrimSpace(out.String()))
	}
	return nil
}

// waitForRedisClusterReady waits until the node reports the whole slot space as
// served. The state flips on the cluster cron, whose timing is the server's
// business, so this polls rather than sleeps — through the library's own exec
// strategy, the same post-start readiness seam enableStreamPlugin uses, rather
// than a hand-rolled deadline loop. The strategy reports only that it timed out,
// so the last CLUSTER INFO it saw is carried out alongside it.
func waitForRedisClusterReady(ctx context.Context, c *RedisContainer, timeout time.Duration) error {
	// WithStartupTimeout stores a POINTER, so a zero is honored rather than
	// falling back to the library default: the wait context would expire before
	// the first poll and report a timeout no amount of waiting could have
	// avoided. Only a nil cfg gets DefaultRedisConfig, so a caller that builds
	// RedisContainerConfig by hand reaches here with zero.
	timeout = cmp.Or(timeout, DefaultRedisConfig().StartupTimeout)

	var last string
	strategy := wait.ForExec([]string{redisCLI, "cluster", "info"}).
		WithResponseMatcher(func(body io.Reader) bool {
			out, err := io.ReadAll(body)
			if err != nil {
				return false
			}
			last = strings.TrimSpace(string(out))
			return strings.Contains(last, redisClusterReadyState)
		}).
		WithStartupTimeout(timeout).
		WithPollInterval(redisClusterBootstrapPoll)

	if err := strategy.WaitUntilReady(ctx, c.container); err != nil {
		return fmt.Errorf("redis container: cluster did not reach %s within %s (%w); last CLUSTER INFO: %s",
			redisClusterReadyState, timeout, err, last)
	}
	return nil
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
