//go:build integration

package containers

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// streamPortSpec is the native stream-protocol port the rabbitmq_stream plugin binds.
	streamPortSpec = "5552/tcp"

	streamPluginReadyTimeout = 30 * time.Second
	streamPluginPollInterval = 250 * time.Millisecond
)

// RabbitMQContainerConfig holds configuration for RabbitMQ test container
type RabbitMQContainerConfig struct {
	// ImageTag specifies the RabbitMQ version (default: the pin in DefaultRabbitMQConfig)
	ImageTag string
	// Username for RabbitMQ authentication (default: "guest")
	Username string
	// Password for RabbitMQ authentication (default: "guest")
	Password string
	// StartupTimeout for container initialization (default: 60 seconds)
	StartupTimeout time.Duration
	// EnableStreamPlugin publishes port 5552 and enables the rabbitmq_stream
	// plugin after boot, for tests that speak the native stream protocol.
	EnableStreamPlugin bool
}

// DefaultRabbitMQConfig returns a RabbitMQContainerConfig populated with sensible defaults.
//
// The returned configuration sets Username to "guest", Password to "guest", and StartupTimeout
// to 60 seconds. The container uses the default RabbitMQ virtual host "/".
func DefaultRabbitMQConfig() *RabbitMQContainerConfig {
	return &RabbitMQContainerConfig{
		// renovate: datasource=docker depName=rabbitmq
		ImageTag:       "4.2.9-management-alpine",
		Username:       "guest",
		Password:       "guest",
		StartupTimeout: 60 * time.Second,
	}
}

// RabbitMQContainer wraps testcontainers RabbitMQ container with helper methods
type RabbitMQContainer struct {
	container  *rabbitmq.RabbitMQContainer
	brokerURL  string
	host       string
	port       int
	streamPort int
	username   string
	password   string
}

// StartRabbitMQContainer starts a RabbitMQ testcontainer using the provided configuration.
// If cfg is nil, DefaultRabbitMQConfig is used. If Docker is not available the test is
// skipped with a clear message. On success it returns a RabbitMQContainer wrapping the
// running container and its AMQP URL; on failure it returns a non-nil error.
func StartRabbitMQContainer(ctx context.Context, t *testing.T, cfg *RabbitMQContainerConfig) (*RabbitMQContainer, error) {
	t.Helper()

	if cfg == nil {
		cfg = DefaultRabbitMQConfig()
	}

	if !isDockerAvailable(ctx) {
		t.Skip("Docker is not available - skipping integration test. Install Docker Desktop or ensure Docker daemon is running.")
		return nil, nil // Never reached due to Skip, but satisfies return
	}

	// Use composite wait strategy: log message (fast early signal) + port listening (network verification)
	// This prevents race conditions where the log appears but RabbitMQ isn't ready to accept connections
	opts := []testcontainers.ContainerCustomizer{
		rabbitmq.WithAdminUsername(cfg.Username),
		rabbitmq.WithAdminPassword(cfg.Password),
		testcontainers.WithWaitStrategy(
			wait.ForAll(
				wait.ForLog("Server startup complete"),
				wait.ForListeningPort("5672/tcp"),
			).WithStartupTimeout(cfg.StartupTimeout),
		),
	}
	if cfg.EnableStreamPlugin {
		// The port must be published at creation time; the plugin that binds it is
		// enabled after boot, so it cannot join the wait strategy above.
		opts = append(opts, testcontainers.WithExposedPorts(streamPortSpec))
	}

	rmqContainer, err := rabbitmq.Run(ctx, fmt.Sprintf("rabbitmq:%s", cfg.ImageTag), opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to start RabbitMQ container: %w", err)
	}

	amqpURL, err := rmqContainer.AmqpURL(ctx)
	if err != nil {
		_ = rmqContainer.Terminate(ctx)
		return nil, fmt.Errorf("failed to get RabbitMQ AMQP URL: %w", err)
	}

	// Get host and port for manual connection building if needed
	host, err := rmqContainer.Host(ctx)
	if err != nil {
		_ = rmqContainer.Terminate(ctx)
		return nil, fmt.Errorf("failed to get RabbitMQ host: %w", err)
	}

	mappedPort, err := rmqContainer.MappedPort(ctx, "5672/tcp")
	if err != nil {
		_ = rmqContainer.Terminate(ctx)
		return nil, fmt.Errorf("failed to get RabbitMQ port: %w", err)
	}

	port := int(mappedPort.Num())

	t.Logf("RabbitMQ container started successfully at %s:%d", host, port)

	container := &RabbitMQContainer{
		container: rmqContainer,
		brokerURL: amqpURL,
		host:      host,
		port:      port,
		username:  cfg.Username,
		password:  cfg.Password,
	}

	if cfg.EnableStreamPlugin {
		streamPort, err := enableStreamPlugin(ctx, rmqContainer)
		if err != nil {
			_ = rmqContainer.Terminate(ctx)
			return nil, err
		}
		container.streamPort = streamPort
		t.Logf("RabbitMQ stream protocol available at %s:%d", host, streamPort)
	}

	return container, nil
}

// enableStreamPlugin turns on rabbitmq_stream in a running container and waits for
// its listener to accept connections on the mapped port. The container's own wait
// strategy ran before the plugin existed, so readiness is polled here.
func enableStreamPlugin(ctx context.Context, c *rabbitmq.RabbitMQContainer) (port int, err error) {
	code, reader, err := c.Exec(ctx, []string{"rabbitmq-plugins", "enable", "rabbitmq_stream"})
	if err != nil {
		return 0, fmt.Errorf("failed to enable rabbitmq_stream plugin: %w", err)
	}
	if code != 0 {
		output, readErr := io.ReadAll(reader)
		if readErr != nil {
			return 0, fmt.Errorf("rabbitmq-plugins enable rabbitmq_stream exited %d; reading its output failed: %w", code, readErr)
		}
		return 0, fmt.Errorf("rabbitmq-plugins enable rabbitmq_stream exited %d: %s", code, output)
	}

	strategy := wait.ForListeningPort(streamPortSpec).
		WithStartupTimeout(streamPluginReadyTimeout).
		WithPollInterval(streamPluginPollInterval)
	if err := strategy.WaitUntilReady(ctx, c); err != nil {
		return 0, fmt.Errorf("rabbitmq_stream listener did not become ready: %w", err)
	}

	mapped, err := c.MappedPort(ctx, streamPortSpec)
	if err != nil {
		return 0, fmt.Errorf("failed to get RabbitMQ stream port: %w", err)
	}
	return int(mapped.Num()), nil
}

// BrokerURL returns the AMQP connection URL for the running container.
func (r *RabbitMQContainer) BrokerURL() string {
	return r.brokerURL
}

// Host returns the container host
func (r *RabbitMQContainer) Host() string {
	return r.host
}

// Port returns the host-side port Docker mapped to the container's 5672.
func (r *RabbitMQContainer) Port() int {
	return r.port
}

// StreamPort returns the host-side port Docker mapped to the container's 5552.
// Zero when the container was started without EnableStreamPlugin.
func (r *RabbitMQContainer) StreamPort() int {
	return r.streamPort
}

// StreamURI returns the native stream-protocol URI for the default vhost.
// Clients must also pin an address resolver to Host/StreamPort: the broker
// advertises its container-internal address, which the host cannot dial.
func (r *RabbitMQContainer) StreamURI() string {
	// url.UserPassword escapes credentials containing @ : or /, and JoinHostPort
	// brackets an IPv6 literal, which some Docker setups return from Host(). The
	// vhost is appended already-encoded: %2f is how the default "/" vhost is spelled.
	u := url.URL{
		Scheme: "rabbitmq-stream",
		User:   url.UserPassword(r.username, r.password),
		Host:   net.JoinHostPort(r.host, strconv.Itoa(r.streamPort)),
	}
	return u.String() + "/%2f"
}

// Terminate stops and removes the RabbitMQ container
func (r *RabbitMQContainer) Terminate(ctx context.Context) error {
	if r.container == nil {
		return nil
	}
	return r.container.Terminate(ctx)
}

// MustStartRabbitMQContainer starts a RabbitMQ test container and fails the test if startup fails.
//
// It is a convenience wrapper around StartRabbitMQContainer that calls t.Fatalf on any error and
// returns the started *RabbitMQContainer when successful.
func MustStartRabbitMQContainer(ctx context.Context, t *testing.T, cfg *RabbitMQContainerConfig) *RabbitMQContainer {
	t.Helper()

	container, err := StartRabbitMQContainer(ctx, t, cfg)
	if err != nil {
		t.Fatalf("Failed to start RabbitMQ container: %v", err)
	}

	return container
}

// WithCleanup registers a cleanup function to terminate the container when the test finishes
func (r *RabbitMQContainer) WithCleanup(t *testing.T) *RabbitMQContainer {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if err := r.Terminate(ctx); err != nil {
			t.Logf("Warning: failed to terminate RabbitMQ container: %v", err)
		}
	})
	return r
}
