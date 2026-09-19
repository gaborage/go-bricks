//go:build integration

package messaging

import (
	"context"
	"regexp"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
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

// TestRegistryConsumesFromAnExternalExchangeWithoutConfigurePermission is the
// broker-backed acceptance test for ADR-119. The non-owner runs as a user with
// configure permission on its OWN queue and none on the exchange, which is the
// deployment the external door exists for: it must verify the exchange, bind to
// it and consume, while an active declare of the same name is refused. It also
// pins that the reference costs the owner nothing — a later declare of the real
// shape still succeeds, because the non-owner never created a shape to race.
func TestRegistryConsumesFromAnExternalExchangeWithoutConfigurePermission(t *testing.T) {
	broker := pkgBroker.Get(t)
	brokerURL := broker.BrokerURL()
	log := logger.New("disabled", true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exchange, queue := uniqueName(t, "external_exchange"), uniqueName(t, "nonowner_queue")

	// The owner declares the exchange out of band, as another service would.
	owner, err := amqp.Dial(brokerURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = owner.Close() })
	ownerCh, err := owner.Channel()
	require.NoError(t, err)
	require.NoError(t, ownerCh.ExchangeDeclare(exchange, ExchangeTypeTopic, true, false, false, false, nil))
	t.Cleanup(func() {
		_, _ = ownerCh.QueueDelete(queue, false, false, false)
		_ = ownerCh.ExchangeDelete(exchange, false, false)
	})

	// The non-owner may create its own queue and nothing else: no configure
	// permission reaches the exchange name.
	user := uniqueName(t, "nonowner_user")
	nonOwnerURL, err := broker.AddUser(ctx, user, "nonowner-pw", "^"+regexp.QuoteMeta(queue)+"$", ".*", ".*")
	require.NoError(t, err)

	client := NewAMQPClient(nonOwnerURL, log, WithReinitDelay(50*time.Millisecond))
	t.Cleanup(func() { _ = client.Close() })
	require.Eventually(t, client.IsReady, 10*time.Second, 100*time.Millisecond, clientReadyMsg)

	handler := &countingTestHandler{}
	registry := NewRegistry(client, log)
	registry.resubscribeDelay = 50 * time.Millisecond
	registry.RegisterExchange(&ExchangeDeclaration{Name: exchange, Passive: true})
	registry.RegisterQueue(&QueueDeclaration{Name: queue})
	registry.RegisterBinding(&BindingDeclaration{Queue: queue, Exchange: exchange, RoutingKey: "orders.#"})
	registry.RegisterConsumer(&ConsumerDeclaration{Queue: queue, EventType: testEventType, Workers: 1, Handler: handler})

	defer registry.StopConsumers()
	require.NoError(t, registry.DeclareInfrastructure(ctx), "a passive declare must not need configure permission")
	require.NoError(t, registry.StartConsumers(ctx))

	require.Eventually(t, func() bool {
		_ = ownerCh.PublishWithContext(ctx, exchange, "orders.created", false, false,
			amqp.Publishing{MessageId: testMessageID, Body: []byte(testMessageBody)})
		return handler.CallCount() > 0
	}, 20*time.Second, 200*time.Millisecond, "the non-owner did not receive a message through the external exchange")

	// The owner's later declare of the real shape still succeeds: the non-owner
	// never sent a shape for the broker to compare against.
	require.NoError(t, ownerCh.ExchangeDeclare(exchange, ExchangeTypeTopic, true, false, false, false, nil))

	// Control, on its own connection so the registry's channel is undisturbed:
	// the same user cannot ACTIVELY declare that exchange. Without this the
	// passive success above could be a mis-provisioned permission set.
	refused, err := amqp.Dial(nonOwnerURL)
	require.NoError(t, err)
	defer refused.Close()
	refusedCh, err := refused.Channel()
	require.NoError(t, err)
	err = refusedCh.ExchangeDeclare(exchange, ExchangeTypeTopic, true, false, false, false, nil)
	require.Error(t, err)
	var amqpErr *amqp.Error
	require.ErrorAs(t, err, &amqpErr)
	assert.Equal(t, amqp.AccessRefused, amqpErr.Code)
}
