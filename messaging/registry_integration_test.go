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
	"github.com/gaborage/go-bricks/testing/containers"
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

	// Registered before the registry: t.Cleanup is LIFO, so consumers stop
	// before these deletes run and cannot re-declare the queue behind them.
	//
	// The two deletes run on SEPARATE channels. A test that removed the queue
	// itself leaves QueueDelete a 404, which is a channel-level exception: on one
	// shared channel that close would take the exchange delete with it and leak
	// the exchange on a broker every test in the package shares.
	adminCh := dialChannel(t, brokerURL)
	exchangeCh := dialChannel(t, brokerURL)
	t.Cleanup(func() {
		_, _ = adminCh.QueueDelete(queue, false, false, false)
		_ = exchangeCh.ExchangeDelete(exchange, false, false)
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
	ownerCh := dialChannel(t, brokerURL)
	require.NoError(t, ownerCh.ExchangeDeclare(exchange, ExchangeTypeTopic, true, false, false, false, nil))
	t.Cleanup(func() {
		_, _ = ownerCh.QueueDelete(queue, false, false, false)
		_ = ownerCh.ExchangeDelete(exchange, false, false)
	})

	// The non-owner may create its own queue and nothing else: no configure
	// permission reaches the exchange name.
	nonOwnerURL := nonOwnerURLFor(ctx, t, broker, queue)

	client := NewAMQPClient(nonOwnerURL, log, WithReinitDelay(50*time.Millisecond))
	t.Cleanup(func() { _ = client.Close() })
	require.Eventually(t, client.IsReady, 10*time.Second, 100*time.Millisecond, clientReadyMsg)

	handler := &countingTestHandler{}
	registry := NewRegistry(client, log)
	registry.resubscribeDelay = 50 * time.Millisecond
	registry.RegisterExchange(NewExternalExchange(exchange))
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
	err := dialChannel(t, nonOwnerURL).ExchangeDeclare(exchange, ExchangeTypeTopic, true, false, false, false, nil)
	require.Error(t, err)
	var amqpErr *amqp.Error
	require.ErrorAs(t, err, &amqpErr)
	assert.Equal(t, amqp.AccessRefused, amqpErr.Code)
}

// TestRegistryDeclareInfrastructureFailsOnAnAbsentExternalExchange pins what a
// missing external exchange costs at startup, against a real broker: the pass
// ends carrying the broker's own 404 naming the exchange, reachable with
// errors.As.
//
// It deliberately asserts NOTHING about the ADR-113 skip set. That set is
// written only by replayTopology, which the startup path never reaches, so an
// emptiness check here could not fail. The skip behavior is pinned where it can
// fail, in TestRegistryRedeclarePassiveConflictSkipsLikeAnyStep.
func TestRegistryDeclareInfrastructureFailsOnAnAbsentExternalExchange(t *testing.T) {
	broker := pkgBroker.Get(t)
	log := logger.New("disabled", true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	absent, queue := uniqueName(t, "absent_exchange"), uniqueName(t, "nonowner_queue")
	client := NewAMQPClient(nonOwnerURLFor(ctx, t, broker, queue), log, WithReinitDelay(50*time.Millisecond))
	t.Cleanup(func() { _ = client.Close() })
	require.Eventually(t, client.IsReady, 10*time.Second, 100*time.Millisecond, clientReadyMsg)

	registry := NewRegistry(client, log)
	registry.RegisterExchange(NewExternalExchange(absent))

	err := registry.DeclareInfrastructure(ctx)

	require.Error(t, err)
	var amqpErr *amqp.Error
	require.ErrorAs(t, err, &amqpErr)
	assert.Equal(t, amqp.NotFound, amqpErr.Code)
	assert.Contains(t, err.Error(), absent)
}

// dialChannel opens a control connection and channel, closing the connection
// when the test ends.
func dialChannel(t *testing.T, url string) *amqp.Channel {
	t.Helper()
	conn, err := amqp.Dial(url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	channel, err := conn.Channel()
	require.NoError(t, err)
	return channel
}

// nonOwnerURLFor returns the URL of a broker user that may create the named
// queue and nothing else — no configure permission reaches any exchange.
func nonOwnerURLFor(ctx context.Context, t *testing.T, broker *containers.RabbitMQContainer, queue string) string {
	t.Helper()
	url, err := broker.AddUser(ctx, uniqueName(t, "nonowner_user"), "nonowner-pw", "^"+regexp.QuoteMeta(queue)+"$", ".*", ".*")
	require.NoError(t, err)
	return url
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
