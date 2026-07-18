//go:build integration

package messaging

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/testing/containers"
)

const (
	clientReadyMsg = "Client should connect and become ready within 10s"
)

// uniqueName generates a unique resource name for tests to prevent cross-test pollution
func uniqueName(t *testing.T, prefix string) string {
	t.Helper()
	// Use test name and unix nano timestamp for uniqueness
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

// setupTestBroker starts a RabbitMQ testcontainer and returns the broker URL
func setupTestBroker(t *testing.T) string {
	t.Helper()

	// Create context with timeout to prevent indefinite hangs
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)

	// Register cleanup to cancel context
	t.Cleanup(func() {
		cancel()
	})

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

	// Wait for client to become ready with extended timeout for slow CI environments
	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 10*time.Second, 200*time.Millisecond, clientReadyMsg)
}

func TestAMQPClientPublishConsumeSimple(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	// Wait for client to become ready with extended timeout for slow CI environments
	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 10*time.Second, 200*time.Millisecond, clientReadyMsg)

	ctx := context.Background()
	queueName := uniqueName(t, "test-simple-queue")

	// Declare queue
	err := client.DeclareQueue(queueName, false, true, false, false, nil)
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

	// Wait for client to become ready with extended timeout for slow CI environments
	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 10*time.Second, 200*time.Millisecond, clientReadyMsg)

	tests := []struct {
		name       string
		queueName  string
		durable    bool
		autoDelete bool
		exclusive  bool
		noWait     bool
	}{
		{"simple", uniqueName(t, "q-simple"), false, true, false, false},
		{"durable", uniqueName(t, "q-durable"), true, false, false, false},
		{"exclusive", uniqueName(t, "q-exclusive"), false, true, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := client.DeclareQueue(tt.queueName, tt.durable, tt.autoDelete, tt.exclusive, tt.noWait, nil)
			assert.NoError(t, err)
		})
	}
}

func TestAMQPClientDeclareExchange(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	// Wait for client to become ready with extended timeout for slow CI environments
	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 10*time.Second, 200*time.Millisecond, clientReadyMsg)

	tests := []struct {
		name         string
		exchangeName string
		kind         string
	}{
		{"direct", uniqueName(t, "ex-direct"), "direct"},
		{"fanout", uniqueName(t, "ex-fanout"), "fanout"},
		{"topic", uniqueName(t, "ex-topic"), "topic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := client.DeclareExchange(tt.exchangeName, tt.kind, false, true, false, false, nil)
			assert.NoError(t, err)
		})
	}
}

func TestAMQPClientBindQueue(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	// Wait for client to become ready with extended timeout for slow CI environments
	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 10*time.Second, 200*time.Millisecond, clientReadyMsg)

	exchangeName := uniqueName(t, "bind-test-exchange")
	queueName := uniqueName(t, "bind-test-queue")

	// Declare exchange and queue
	err := client.DeclareExchange(exchangeName, "direct", false, true, false, false, nil)
	require.NoError(t, err)

	err = client.DeclareQueue(queueName, false, true, false, false, nil)
	require.NoError(t, err)

	// Bind queue to exchange
	err = client.BindQueue(queueName, exchangeName, "test-key", false, nil)
	assert.NoError(t, err)
}

// TestAMQPClientDeclareQueueArgsDeadLetter pins the report's exact loss
// scenario end-to-end: a queue declared with x-dead-letter-exchange parks a
// message that is nacked without requeue (the same signal the registry sends
// on handler error/panic) instead of dropping it forever.
//
// Topology is load-bearing:
//   - the DLX is a fanout exchange. Dead-lettered messages are republished to
//     the DLX with their ORIGINAL routing key — which is the work-queue name,
//     because client.Publish targets the default exchange (see
//     AMQPClientImpl.Publish). A fanout DLX ignores routing keys, so the
//     binding can't mismatch (a direct DLX bound with routing key = the
//     work-queue name would also work; fanout is the foolproof choice).
//   - the DLQ is bound to the DLX with an empty binding key (irrelevant under
//     fanout).
//   - the work queue carries x-dead-letter-exchange pointing at the DLX.
//
// If this test times out, re-check the topology against the bullets above
// (a routing-key/binding mismatch produces the identical timeout symptom)
// before concluding the broker-side dead-lettering assumption is wrong.
func TestAMQPClientDeclareQueueArgsDeadLetter(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	require.Eventually(t, client.IsReady, 10*time.Second, 200*time.Millisecond, clientReadyMsg)

	ctx := context.Background()
	dlxName := uniqueName(t, "dlx-fanout")
	dlqName := uniqueName(t, "dlq")
	workQueueName := uniqueName(t, "work-queue")

	// Declare the DLX as a fanout exchange.
	require.NoError(t, client.DeclareExchange(dlxName, "fanout", true, false, false, false, nil))

	// Declare the DLQ and bind it to the DLX (binding key irrelevant under fanout).
	require.NoError(t, client.DeclareQueue(dlqName, true, false, false, false, nil))
	require.NoError(t, client.BindQueue(dlqName, dlxName, "", false, nil))

	// Declare the work queue with x-dead-letter-exchange pointing at the DLX.
	require.NoError(t, client.DeclareQueue(workQueueName, true, false, false, false, map[string]any{
		"x-dead-letter-exchange": dlxName,
	}))

	// Consume from the work queue, then nack WITHOUT requeue — the same
	// signal the framework's registry sends on handler error/panic.
	workDeliveries, err := client.Consume(ctx, workQueueName)
	require.NoError(t, err)

	testMsg := []byte("dead-lettered message")
	require.NoError(t, client.Publish(ctx, workQueueName, testMsg))

	select {
	case delivery := <-workDeliveries:
		assert.Equal(t, testMsg, delivery.Body)
		require.NoError(t, delivery.Nack(false, false))
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for message on work queue")
	}

	// The nacked-without-requeue message must be PARKED in the DLQ, not dropped.
	dlqDeliveries, err := client.Consume(ctx, dlqName)
	require.NoError(t, err)

	select {
	case delivery := <-dlqDeliveries:
		assert.Equal(t, testMsg, delivery.Body, "dead-lettered message must retain its original body")
		_ = delivery.Ack(false)
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for dead-lettered message in DLQ — Args did not reach the broker or topology is wrong")
	}
}

// TestAMQPClientDeclareQueueArgsQuorum pins the "cannot attach to
// ops-provisioned queues" half of the finding: args participate in RabbitMQ's
// declare-equivalence check, so declaring a quorum queue and then redeclaring
// the SAME name with the SAME args must both succeed (equivalence holds).
func TestAMQPClientDeclareQueueArgsQuorum(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	require.Eventually(t, client.IsReady, 10*time.Second, 200*time.Millisecond, clientReadyMsg)

	queueName := uniqueName(t, "quorum-queue")
	args := map[string]any{"x-queue-type": "quorum"}

	// Quorum queues require durable=true, exclusive=false, autoDelete=false.
	require.NoError(t, client.DeclareQueue(queueName, true, false, false, false, args))

	// Redeclaring the same name with the same args must be equivalent — no 406.
	require.NoError(t, client.DeclareQueue(queueName, true, false, false, false, args))
}

// =============================================================================
// Publishing Tests
// =============================================================================

func TestAMQPClientPublishToExchange(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	client := NewAMQPClient(brokerURL, log)
	defer client.Close()

	// Wait for client to become ready with extended timeout for slow CI environments
	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 10*time.Second, 200*time.Millisecond, clientReadyMsg)

	ctx := context.Background()
	exchangeName := uniqueName(t, "pub-test-exchange")
	queueName := uniqueName(t, "pub-test-queue")
	routingKey := "test-route"

	// Setup exchange, queue, and binding
	err := client.DeclareExchange(exchangeName, "direct", false, true, false, false, nil)
	require.NoError(t, err)

	err = client.DeclareQueue(queueName, false, true, false, false, nil)
	require.NoError(t, err)

	err = client.BindQueue(queueName, exchangeName, routingKey, false, nil)
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

	// Wait for client to become ready with extended timeout for slow CI environments
	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 10*time.Second, 200*time.Millisecond, clientReadyMsg)

	ctx := context.Background()
	queueName := uniqueName(t, "confirms-test-queue")

	err := client.DeclareQueue(queueName, false, true, false, false, nil)
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

	// Wait for client to become ready with extended timeout for slow CI environments
	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 10*time.Second, 200*time.Millisecond, clientReadyMsg)

	ctx := context.Background()
	queueName := uniqueName(t, "consume-opts-queue")

	err := client.DeclareQueue(queueName, false, true, false, false, nil)
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

	// Wait for client to become ready with extended timeout for slow CI environments
	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 10*time.Second, 200*time.Millisecond, clientReadyMsg)

	ctx := context.Background()
	queueName := uniqueName(t, "manual-ack-queue")

	err := client.DeclareQueue(queueName, false, true, false, false, nil)
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

	// Wait for client to become ready with extended timeout for slow CI environments
	require.Eventually(t, func() bool {
		return client.IsReady()
	}, 10*time.Second, 200*time.Millisecond, clientReadyMsg)

	// Close client (tests Close function at 94.1% coverage)
	err := client.Close()
	assert.NoError(t, err, "Close should succeed")

	// Client should no longer be ready
	assert.False(t, client.IsReady(), "Client should not be ready after close")

	// Closing again should return error
	err = client.Close()
	assert.Error(t, err, "Second close should return error")
}

// TestAMQPClientPublishImmediatelyOnColdStart is the secondary (real-broker)
// confirmation for issue #655: publish IMMEDIATELY on a freshly constructed
// client, WITHOUT the require.Eventually(IsReady) wait every other test in
// this file uses first. The default 5s messaging.reconnect.readytimeout
// pre-flight inside PublishToExchange must absorb the connect+channel-init
// window against a real broker — on unpatched code this regresses to an
// instant ErrNotConnected.
func TestAMQPClientPublishImmediatelyOnColdStart(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)

	// Topology declaration is unaffected by #655 (only PublishToExchange gets a
	// pre-flight wait), so set it up with a client that has already reached
	// readiness. The fix is exercised by a SEPARATE, freshly constructed client
	// below.
	setup := NewAMQPClient(brokerURL, log)
	defer setup.Close()
	require.Eventually(t, setup.IsReady, 10*time.Second, 200*time.Millisecond, clientReadyMsg)

	ctx := context.Background()
	exchangeName := uniqueName(t, "cold-start-exchange")
	queueName := uniqueName(t, "cold-start-queue")
	routingKey := "cold-start-route"

	require.NoError(t, setup.DeclareExchange(exchangeName, "direct", false, true, false, false, nil))
	require.NoError(t, setup.DeclareQueue(queueName, false, true, false, false, nil))
	require.NoError(t, setup.BindQueue(queueName, exchangeName, routingKey, false, nil))
	deliveries, err := setup.Consume(ctx, queueName)
	require.NoError(t, err)

	cold := NewAMQPClient(brokerURL, log)
	defer cold.Close()

	testMsg := []byte("cold start message")
	err = cold.PublishToExchange(ctx, PublishOptions{
		Exchange:   exchangeName,
		RoutingKey: routingKey,
	}, testMsg)
	require.NoError(t, err, "publish immediately after client construction must succeed via the readytimeout pre-flight wait")

	select {
	case delivery := <-deliveries:
		assert.Equal(t, testMsg, delivery.Body)
		_ = delivery.Ack(false)
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for cold-start published message")
	}
}
