//go:build integration

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/testing/containers"
)

// setupTestBroker starts a RabbitMQ testcontainer and returns the broker URL
func setupTestBroker(t *testing.T) string {
	t.Helper()

	ctx := context.Background()
	rmqContainer := containers.MustStartRabbitMQContainer(ctx, t, nil).WithCleanup(t)
	return rmqContainer.BrokerURL()
}

// =============================================================================
// Connection Tests
// =============================================================================

func TestAMQPClientConnection(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 5*time.Second, 100*time.Millisecond, "Client should become ready")
}

func TestAMQPClientPublishConsumeSimple(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 5*time.Second, 100*time.Millisecond, "Client should become ready")

	ctx := context.Background()
	queueName := "test-simple-queue"

	// Declare queue
	err := client.DeclareQueue(queueName, false, true, false, false)
	require.NoError(t, err)

	// Start consumer
	deliveries, err := client.Consume(ctx, queueName)
	require.NoError(t, err)

	// Publish message
	testMsg := []byte("hello world")
	err = client.Publish(ctx, queueName, testMsg)
	require.NoError(t, err)

	// Consume message
	select {
	case delivery := <-deliveries:
		assert.Equal(t, testMsg, delivery.Body)
		_ = delivery.Ack(false)
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for message")
	}
}

// =============================================================================
// Queue and Exchange Declaration Tests
// =============================================================================

func TestAMQPClientDeclareQueueVariants(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 5*time.Second, 100*time.Millisecond)

	tests := []struct {
		name       string
		queueName  string
		durable    bool
		autoDelete bool
		exclusive  bool
		noWait     bool
	}{
		{"simple", "q-simple", false, true, false, false},
		{"durable", "q-durable", true, false, false, false},
		{"exclusive", "q-exclusive", false, true, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := client.DeclareQueue(tt.queueName, tt.durable, tt.autoDelete, tt.exclusive, tt.noWait)
			assert.NoError(t, err)
		})
	}
}

func TestAMQPClientDeclareExchange(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 5*time.Second, 100*time.Millisecond)

	tests := []struct {
		name         string
		exchangeName string
		kind         string
	}{
		{"direct", "ex-direct", "direct"},
		{"fanout", "ex-fanout", "fanout"},
		{"topic", "ex-topic", "topic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := client.DeclareExchange(tt.exchangeName, tt.kind, false, true, false, false)
			assert.NoError(t, err)
		})
	}
}

func TestAMQPClientBindQueue(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 5*time.Second, 100*time.Millisecond)

	exchangeName := "bind-test-exchange"
	queueName := "bind-test-queue"

	// Declare exchange and queue
	err := client.DeclareExchange(exchangeName, "direct", false, true, false, false)
	require.NoError(t, err)

	err = client.DeclareQueue(queueName, false, true, false, false)
	require.NoError(t, err)

	// Bind queue to exchange
	err = client.BindQueue(queueName, exchangeName, "test-key", false)
	assert.NoError(t, err)
}

// =============================================================================
// Publishing Tests
// =============================================================================

func TestAMQPClientPublishToExchange(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 5*time.Second, 100*time.Millisecond)

	ctx := context.Background()
	exchangeName := "pub-test-exchange"
	queueName := "pub-test-queue"
	routingKey := "test-route"

	// Setup exchange, queue, and binding
	err := client.DeclareExchange(exchangeName, "direct", false, true, false, false)
	require.NoError(t, err)

	err = client.DeclareQueue(queueName, false, true, false, false)
	require.NoError(t, err)

	err = client.BindQueue(queueName, exchangeName, routingKey, false)
	require.NoError(t, err)

	// Start consumer
	deliveries, err := client.Consume(ctx, queueName)
	require.NoError(t, err)

	// Publish to exchange
	testMsg := []byte("exchange message")
	err = client.PublishToExchange(ctx, PublishOptions{
		Exchange:   exchangeName,
		RoutingKey: routingKey,
	}, testMsg)
	require.NoError(t, err)

	// Verify message received
	select {
	case delivery := <-deliveries:
		assert.Equal(t, testMsg, delivery.Body)
		_ = delivery.Ack(false)
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for exchange message")
	}
}

func TestAMQPClientPublisherConfirms(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 5*time.Second, 100*time.Millisecond)

	ctx := context.Background()
	queueName := "confirms-test-queue"

	err := client.DeclareQueue(queueName, false, true, false, false)
	require.NoError(t, err)

	// Publish multiple messages (tests publisher confirms in init function)
	for i := 0; i < 10; i++ {
		err := client.Publish(ctx, queueName, []byte("msg"))
		require.NoError(t, err, "All publishes should succeed with confirms")
	}
}

// =============================================================================
// Consumer Tests
// =============================================================================

func TestAMQPClientConsumeWithOptions(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 5*time.Second, 100*time.Millisecond)

	ctx := context.Background()
	queueName := "consume-opts-queue"

	err := client.DeclareQueue(queueName, false, true, false, false)
	require.NoError(t, err)

	// Consume with auto-ack
	deliveries, err := client.ConsumeFromQueue(ctx, ConsumeOptions{
		Queue:   queueName,
		AutoAck: true,
	})
	require.NoError(t, err)

	// Publish and consume
	err = client.Publish(ctx, queueName, []byte("autoack test"))
	require.NoError(t, err)

	select {
	case delivery := <-deliveries:
		assert.Equal(t, []byte("autoack test"), delivery.Body)
		// No manual ack needed (auto-ack enabled)
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for autoack message")
	}
}

func TestAMQPClientConsumeManualAck(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 5*time.Second, 100*time.Millisecond)

	ctx := context.Background()
	queueName := "manual-ack-queue"

	err := client.DeclareQueue(queueName, false, true, false, false)
	require.NoError(t, err)

	// Consume without auto-ack (manual ack)
	deliveries, err := client.ConsumeFromQueue(ctx, ConsumeOptions{
		Queue:   queueName,
		AutoAck: false,
	})
	require.NoError(t, err)

	// Publish message
	err = client.Publish(ctx, queueName, []byte("manual ack test"))
	require.NoError(t, err)

	// Consume and manually ack
	select {
	case delivery := <-deliveries:
		assert.Equal(t, []byte("manual ack test"), delivery.Body)
		err = delivery.Ack(false)
		assert.NoError(t, err, "Manual ack should succeed")
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for manual ack message")
	}
}

// =============================================================================
// Close Tests
// =============================================================================

func TestAMQPClientClose(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)

	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 5*time.Second, 100*time.Millisecond)

	// Close client (tests Close function at 94.1% coverage)
	err := client.Close()
	assert.NoError(t, err, "Close should succeed")

	// Client should no longer be ready
	assert.False(t, client.IsReady(), "Client should not be ready after close")

	// Closing again should return error
	err = client.Close()
	assert.Error(t, err, "Second close should return error")
}
