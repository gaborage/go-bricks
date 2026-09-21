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

// testRecoveredBody distinguishes the publish that must land after the topology
// came back from the one that warmed the connection up before it was destroyed.
const testRecoveredBody = "published after the exchange came back"

// drainQueue empties queue so a later Get can only return a message published
// after this call.
func drainQueue(t *testing.T, ch *amqp.Channel, queue string) {
	t.Helper()
	for {
		_, ok, err := ch.Get(queue, true)
		require.NoError(t, err)
		if !ok {
			return
		}
	}
}

// TestManagerRedeclaresDeletedExchangeThroughAPooledPublisher is the
// production-wiring acceptance test for #1761: a publisher-only service, wired
// exactly as the app wires it (EnsureConsumers declares the topology, publishes
// go through Manager.Publisher), loses its exchange to an operator under a LIVE
// connection. The client that eats the broker's 404 is the pooled publisher, not
// the registry's own, so nothing else can drive the repair — and the repair has
// to run through the registry's connection.
func TestManagerRedeclaresDeletedExchangeThroughAPooledPublisher(t *testing.T) {
	brokerURL := setupTestBroker(t)
	log := logger.New("disabled", true)
	ctx := context.Background()

	exchange, queue := uniqueName(t, "pooled_publisher_exchange"), uniqueName(t, "pooled_publisher_queue")

	// Registered before the manager: t.Cleanup is LIFO, so the manager closes
	// before these deletes run and cannot re-declare behind them.
	admin, err := amqp.Dial(brokerURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	adminCh, err := admin.Channel()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = adminCh.QueueDelete(queue, false, false, false)
		_ = adminCh.ExchangeDelete(exchange, false, false)
	})

	m := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{"": brokerURL}},
		log,
		ManagerOptions{
			ConnectionTimeout:  2 * time.Second,
			MaxPublishAttempts: 2,
			PublishTimeout:     3 * time.Second,
			ReinitDelay:        50 * time.Millisecond,
			ResendDelay:        50 * time.Millisecond,
		},
		nil,
	)
	t.Cleanup(func() { _ = m.Close() })

	decls := NewDeclarations()
	decls.RegisterExchange(&ExchangeDeclaration{Name: exchange, Type: ExchangeTypeTopic, Durable: true})
	decls.RegisterQueue(&QueueDeclaration{Name: queue, Durable: true})
	decls.RegisterBinding(&BindingDeclaration{Queue: queue, Exchange: exchange, RoutingKey: "orders.#"})
	decls.RegisterPublisher(&PublisherDeclaration{Exchange: exchange, RoutingKey: "orders.created", EventType: testEventType})
	require.Empty(t, decls.Consumers(), "this service consumes nothing: the registry's own client never rotates")
	require.NoError(t, m.EnsureConsumers(ctx, "", decls))

	publisher, release, err := m.Publisher(ctx, "")
	require.NoError(t, err)
	defer release()
	door, ok := publisher.(bytePublisher)
	require.True(t, ok, "the pooled publisher must expose the framework's byte door")

	publish := func(body string) error {
		return door.publishBytes(ctx, publishOptions{Exchange: exchange, RoutingKey: "orders.created"}, []byte(body))
	}
	require.Eventually(t, func() bool {
		return publish(testMessageBody) == nil
	}, 20*time.Second, 200*time.Millisecond, "the pooled publisher never reached the broker")
	drainQueue(t, adminCh, queue)

	// Out of band, under a live connection: deleting the exchange takes its
	// bindings with it, so nothing short of a redeclare pass routes a publish to
	// the queue again.
	require.NoError(t, adminCh.ExchangeDelete(exchange, false, false))

	require.Eventually(t, func() bool {
		if err := publish(testRecoveredBody); err != nil {
			return false
		}
		msg, ok, err := adminCh.Get(queue, true)
		return err == nil && ok && string(msg.Body) == testRecoveredBody
	}, 40*time.Second, 250*time.Millisecond, "the publish never reached the queue after the exchange was deleted")
}
