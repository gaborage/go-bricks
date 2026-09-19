//go:build integration

package messaging

import (
	"context"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

// newRedeclareFixture brings up a broker, a client that reinitializes fast
// enough for a test to watch a channel rotation, and a second connection whose
// admin channel deletes the test's topology on cleanup. The deletes are
// registered before the caller builds its registry, so t.Cleanup's LIFO order
// stops consumers first and nothing re-declares behind them.
func newRedeclareFixture(t *testing.T, log logger.Logger, exchange, queue string, opts ...ClientOption) (*AMQPClientImpl, *amqp.Channel) {
	t.Helper()
	brokerURL := setupTestBroker(t)
	client := NewAMQPClient(brokerURL, log, append([]ClientOption{WithReinitDelay(50 * time.Millisecond)}, opts...)...)
	t.Cleanup(func() { _ = client.Close() })
	require.Eventually(t, client.IsReady, 10*time.Second, 100*time.Millisecond, clientReadyMsg)

	admin, err := amqp.Dial(brokerURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	adminCh, err := admin.Channel()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = adminCh.QueueDelete(queue, false, false, false)
		_ = adminCh.ExchangeDelete(exchange, false, false)
	})
	return client, adminCh
}

// rotateClientChannel tears down the client's current channel and waits for it
// to come back past generation. The broker may already have closed it — using a
// deleted entity is a channel-level 404 — so the close result is ignored; either
// way the client reinitializes onto a new channel.
func rotateClientChannel(t *testing.T, client *AMQPClientImpl, generation uint64) {
	t.Helper()
	client.m.RLock()
	channel := client.channel
	client.m.RUnlock()
	_ = channel.Close()

	require.Eventually(t, func() bool {
		current, ready := client.channelGeneration()
		return ready && current > generation
	}, 10*time.Second, 50*time.Millisecond, "client did not reconnect onto a new channel")
}

// TestRegistryRedeclaresDeletedQueueAfterReconnect is the broker-backed
// acceptance test for the reconnect redeclare pass: the consumed queue is
// deleted behind the client and the client's channel is torn down; once the
// client is back on a new channel the registry declares the queue and its
// binding again, and delivery resumes.
func TestRegistryRedeclaresDeletedQueueAfterReconnect(t *testing.T) {
	log := logger.New("disabled", true)
	exchange, queue := uniqueName(t, "redeclare_exchange"), uniqueName(t, "redeclare_queue")
	client, adminCh := newRedeclareFixture(t, log, exchange, queue)

	handler := &countingTestHandler{}
	registry := NewRegistry(client, log)
	registry.resubscribeDelay = 50 * time.Millisecond
	registry.RegisterExchange(&ExchangeDeclaration{Name: exchange, Type: ExchangeTypeTopic})
	registry.RegisterQueue(&QueueDeclaration{Name: queue})
	registry.RegisterBinding(&BindingDeclaration{Queue: queue, Exchange: exchange, RoutingKey: "orders.#"})
	registry.RegisterConsumer(&ConsumerDeclaration{Queue: queue, EventType: testEventType, Workers: 1, Handler: handler})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer registry.StopConsumers()
	require.NoError(t, registry.DeclareInfrastructure(ctx))
	require.NoError(t, registry.StartConsumers(ctx))
	generation, _ := client.channelGeneration()

	_, err := adminCh.QueueDelete(queue, false, false, false)
	require.NoError(t, err)
	rotateClientChannel(t, client, generation)

	require.Eventually(t, func() bool {
		_ = adminCh.PublishWithContext(ctx, exchange, "orders.created", false, false,
			amqp.Publishing{MessageId: testMessageID, Body: []byte(testMessageBody)})
		return handler.CallCount() > 0
	}, 20*time.Second, 200*time.Millisecond, "delivery did not resume after the queue was redeclared")
}

// TestRegistryRedeclaresDeletedExchangeForPublisherOnlyService is the
// broker-backed acceptance test for the second driver: a service that declares
// and publishes but consumes nothing has no re-subscribe to carry a redeclare
// pass, so the deleted exchange comes back only because the client announced its
// new channel. Without that driver the publish 404s forever.
func TestRegistryRedeclaresDeletedExchangeForPublisherOnlyService(t *testing.T) {
	log := logger.New("disabled", true)
	exchange, queue := uniqueName(t, "publisher_exchange"), uniqueName(t, "publisher_queue")
	client, adminCh := newRedeclareFixture(t, log, exchange, queue, WithPublishTimeout(2*time.Second))

	registry := NewRegistry(client, log)
	registry.RegisterExchange(&ExchangeDeclaration{Name: exchange, Type: ExchangeTypeTopic})
	registry.RegisterQueue(&QueueDeclaration{Name: queue})
	registry.RegisterBinding(&BindingDeclaration{Queue: queue, Exchange: exchange, RoutingKey: "orders.#"})
	registry.RegisterPublisher(&PublisherDeclaration{Exchange: exchange, RoutingKey: "orders.created", EventType: testEventType})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer registry.StopConsumers()
	require.NoError(t, registry.DeclareInfrastructure(ctx))
	require.Empty(t, registry.Consumers())
	generation, _ := client.channelGeneration()

	// Deleting the exchange takes its bindings with it, so nothing short of a
	// redeclare pass can route a publish to the queue again.
	require.NoError(t, adminCh.ExchangeDelete(exchange, false, false))
	rotateClientChannel(t, client, generation)

	require.Eventually(t, func() bool {
		if err := client.publishBytes(ctx, publishOptions{Exchange: exchange, RoutingKey: "orders.created"}, []byte(testMessageBody)); err != nil {
			return false
		}
		msg, ok, err := adminCh.Get(queue, true)
		return err == nil && ok && string(msg.Body) == testMessageBody
	}, 20*time.Second, 200*time.Millisecond, "the publish did not reach the queue after the exchange was redeclared")
}
