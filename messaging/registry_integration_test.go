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

// TestRegistryRedeclaresDeletedQueueAfterReconnect is the broker-backed
// acceptance test for the reconnect redeclare pass: the consumed queue is
// deleted behind the client and the client's channel is torn down; once the
// client is back on a new channel the registry declares the queue and its
// binding again, and delivery resumes.
func TestRegistryRedeclaresDeletedQueueAfterReconnect(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)
	client := NewAMQPClient(brokerURL, log, WithReinitDelay(50*time.Millisecond))
	t.Cleanup(func() { _ = client.Close() })
	require.Eventually(t, client.IsReady, 10*time.Second, 100*time.Millisecond, clientReadyMsg)

	exchange, queue := uniqueName(t, "redeclare_exchange"), uniqueName(t, "redeclare_queue")

	// Registered before the registry: t.Cleanup is LIFO, so consumers stop
	// before these deletes run and cannot re-declare the queue behind them.
	admin, err := amqp.Dial(brokerURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	adminCh, err := admin.Channel()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = adminCh.QueueDelete(queue, false, false, false)
		_ = adminCh.ExchangeDelete(exchange, false, false)
	})

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

	_, err = adminCh.QueueDelete(queue, false, false, false)
	require.NoError(t, err)
	client.m.RLock()
	channel := client.channel
	client.m.RUnlock()
	// The broker may already have closed it: consuming the deleted queue is a
	// channel-level 404. Either way the client reinitializes onto a new channel.
	_ = channel.Close()

	require.Eventually(t, func() bool {
		current, ready := client.channelGeneration()
		return ready && current > generation
	}, 10*time.Second, 50*time.Millisecond, "client did not reconnect onto a new channel")

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
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)
	client := NewAMQPClient(brokerURL, log,
		WithReinitDelay(50*time.Millisecond),
		WithPublishTimeout(2*time.Second))
	t.Cleanup(func() { _ = client.Close() })
	require.Eventually(t, client.IsReady, 10*time.Second, 100*time.Millisecond, clientReadyMsg)

	exchange, queue := uniqueName(t, "publisher_exchange"), uniqueName(t, "publisher_queue")

	admin, err := amqp.Dial(brokerURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	adminCh, err := admin.Channel()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = adminCh.QueueDelete(queue, false, false, false)
		_ = adminCh.ExchangeDelete(exchange, false, false)
	})

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
	client.m.RLock()
	channel := client.channel
	client.m.RUnlock()
	_ = channel.Close()

	require.Eventually(t, func() bool {
		current, ready := client.channelGeneration()
		return ready && current > generation
	}, 10*time.Second, 50*time.Millisecond, "client did not reconnect onto a new channel")

	require.Eventually(t, func() bool {
		if err := client.publishBytes(ctx, publishOptions{Exchange: exchange, RoutingKey: "orders.created"}, []byte(testMessageBody)); err != nil {
			return false
		}
		msg, ok, err := adminCh.Get(queue, true)
		return err == nil && ok && string(msg.Body) == testMessageBody
	}, 20*time.Second, 200*time.Millisecond, "the publish did not reach the queue after the exchange was redeclared")
}
