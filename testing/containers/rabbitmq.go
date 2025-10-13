//go:build integration

package containers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	"github.com/testcontainers/testcontainers-go/wait"
)

// RabbitMQContainerConfig holds configuration for RabbitMQ test container
type RabbitMQContainerConfig struct {
	// ImageTag specifies the RabbitMQ version (default: "3.13-management-alpine")
	ImageTag string
	// Username for RabbitMQ authentication (default: "guest")
	Username string
	// Password for RabbitMQ authentication (default: "guest")
	Password string
	// StartupTimeout for container initialization (default: 60 seconds)
	StartupTimeout time.Duration
}

// DefaultRabbitMQConfig returns a RabbitMQContainerConfig populated with sensible defaults.
//
// The returned configuration sets ImageTag to "3.13-management-alpine", Username to "guest",
// Password to "guest", and StartupTimeout to 60 seconds. The container uses the default
// RabbitMQ virtual host "/".
func DefaultRabbitMQConfig() *RabbitMQContainerConfig {
	return &RabbitMQContainerConfig{
		ImageTag:       "3.13-management-alpine",
		Username:       "guest",
		Password:       "guest",
		StartupTimeout: 60 * time.Second,
	}
}

// RabbitMQContainer wraps testcontainers RabbitMQ container with helper methods
type RabbitMQContainer struct {
	container *rabbitmq.RabbitMQContainer
	brokerURL string
	host      string
	port      int
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

	// Check if Docker is available - skip test if not
	if !isDockerAvailable(ctx) {
		t.Skip("Docker is not available - skipping integration test. Install Docker Desktop or ensure Docker daemon is running.")
		return nil, nil // Never reached due to Skip, but satisfies return
	}

	// Create RabbitMQ container with configuration
	rmqContainer, err := rabbitmq.Run(ctx,
		fmt.Sprintf("rabbitmq:%s", cfg.ImageTag),
		rabbitmq.WithAdminUsername(cfg.Username),
		rabbitmq.WithAdminPassword(cfg.Password),
		testcontainers.WithWaitStrategy(
			wait.ForLog("Server startup complete").
				WithStartupTimeout(cfg.StartupTimeout),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to start RabbitMQ container: %w", err)
	}

	// Get AMQP URL
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

	port := mappedPort.Int()

	t.Logf("RabbitMQ container started successfully at %s:%d", host, port)

	return &RabbitMQContainer{
		container: rmqContainer,
		brokerURL: amqpURL,
		host:      host,
		port:      port,
	}, nil
}

// BrokerURL returns the AMQP connection URL (amqp://guest:guest@localhost:port/)
func (r *RabbitMQContainer) BrokerURL() string {
	return r.brokerURL
}

// Host returns the container host
func (r *RabbitMQContainer) Host() string {
	return r.host
}

// Port returns the mapped AMQP port (5672)
func (r *RabbitMQContainer) Port() int {
	return r.port
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
