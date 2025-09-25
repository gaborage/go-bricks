package multitenant

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

const (
	testEventType = "test.created"
	testConsumer  = "test-consumer"
)

func TestNewMessagingDeclarations(t *testing.T) {
	declarations := NewMessagingDeclarations()

	assert.NotNil(t, declarations)
	assert.NotNil(t, declarations.Exchanges)
	assert.NotNil(t, declarations.Queues)
	assert.NotNil(t, declarations.Bindings)
	assert.NotNil(t, declarations.Publishers)
	assert.NotNil(t, declarations.Consumers)

	assert.Empty(t, declarations.Exchanges)
	assert.Empty(t, declarations.Queues)
	assert.Empty(t, declarations.Bindings)
	assert.Empty(t, declarations.Publishers)
	assert.Empty(t, declarations.Consumers)

	assert.True(t, declarations.IsEmpty())
}

func TestMessagingDeclarationsCaptureFromRegistry(t *testing.T) {
	log := logger.New("debug", true)
	mockClient := NewRecordingAMQPClient()
	registry := messaging.NewRegistry(mockClient, log)

	// Register some test infrastructure
	exchange := &messaging.ExchangeDeclaration{
		Name:    testExchange,
		Type:    "topic",
		Durable: true,
		Args:    map[string]any{"x-message-ttl": 3600},
	}
	registry.RegisterExchange(exchange)

	queue := &messaging.QueueDeclaration{
		Name:    testQueue,
		Durable: true,
		Args:    map[string]any{"x-max-length": 1000},
	}
	registry.RegisterQueue(queue)

	binding := &messaging.BindingDeclaration{
		Queue:      testQueue,
		Exchange:   testExchange,
		RoutingKey: "test.event.*",
		Args:       map[string]any{"x-match": "all"},
	}
	registry.RegisterBinding(binding)

	publisher := &messaging.PublisherDeclaration{
		Exchange:    testExchange,
		RoutingKey:  "test.event.created",
		EventType:   testEventType,
		Description: "Test event publisher",
		Headers:     map[string]any{"version": "v1"},
	}
	registry.RegisterPublisher(publisher)

	consumer := &messaging.ConsumerDeclaration{
		Queue:       testQueue,
		Consumer:    testConsumer,
		EventType:   testEventType,
		Description: "Test event consumer",
		Handler:     &mockHandler{},
	}
	registry.RegisterConsumer(consumer)

	// Capture declarations
	declarations := NewMessagingDeclarations()
	declarations.CaptureFromRegistry(registry)

	// Verify capture
	assert.False(t, declarations.IsEmpty())

	// Check exchanges
	assert.Len(t, declarations.Exchanges, 1)
	capturedExchange := declarations.Exchanges[testExchange]
	require.NotNil(t, capturedExchange)
	assert.Equal(t, testExchange, capturedExchange.Name)
	assert.Equal(t, "topic", capturedExchange.Type)
	assert.True(t, capturedExchange.Durable)
	assert.Equal(t, 3600, capturedExchange.Args["x-message-ttl"])

	// Check queues
	assert.Len(t, declarations.Queues, 1)
	capturedQueue := declarations.Queues[testQueue]
	require.NotNil(t, capturedQueue)
	assert.Equal(t, testQueue, capturedQueue.Name)
	assert.True(t, capturedQueue.Durable)
	assert.Equal(t, 1000, capturedQueue.Args["x-max-length"])

	// Check bindings
	assert.Len(t, declarations.Bindings, 1)
	capturedBinding := declarations.Bindings[0]
	assert.Equal(t, testQueue, capturedBinding.Queue)
	assert.Equal(t, testExchange, capturedBinding.Exchange)
	assert.Equal(t, "test.event.*", capturedBinding.RoutingKey)
	assert.Equal(t, "all", capturedBinding.Args["x-match"])

	// Check publishers
	assert.Len(t, declarations.Publishers, 1)
	capturedPublisher := declarations.Publishers[0]
	assert.Equal(t, testExchange, capturedPublisher.Exchange)
	assert.Equal(t, "test.event.created", capturedPublisher.RoutingKey)
	assert.Equal(t, testEventType, capturedPublisher.EventType)
	assert.Equal(t, "v1", capturedPublisher.Headers["version"])

	// Check consumers
	assert.Len(t, declarations.Consumers, 1)
	capturedConsumer := declarations.Consumers[0]
	assert.Equal(t, testQueue, capturedConsumer.Queue)
	assert.Equal(t, testConsumer, capturedConsumer.Consumer)
	assert.Equal(t, testEventType, capturedConsumer.EventType)
	assert.Equal(t, consumer.Handler, capturedConsumer.Handler) // Same reference

	// Verify deep copying - modify original and ensure captured is unchanged
	exchange.Name = "modified.exchange"
	assert.Equal(t, testExchange, capturedExchange.Name) // Should be unchanged
}

func TestMessagingDeclarationsReplayToRegistry(t *testing.T) {
	// Create declarations with test data
	declarations := NewMessagingDeclarations()

	declarations.Exchanges[testExchange] = &messaging.ExchangeDeclaration{
		Name:    testExchange,
		Type:    "topic",
		Durable: true,
	}

	declarations.Queues[testQueue] = &messaging.QueueDeclaration{
		Name:    testQueue,
		Durable: true,
	}

	declarations.Bindings = append(declarations.Bindings, &messaging.BindingDeclaration{
		Queue:      testQueue,
		Exchange:   testExchange,
		RoutingKey: "test.*",
	})

	declarations.Publishers = append(declarations.Publishers, &messaging.PublisherDeclaration{
		Exchange:   testExchange,
		RoutingKey: testEventType,
		EventType:  testEventType,
	})

	declarations.Consumers = append(declarations.Consumers, &messaging.ConsumerDeclaration{
		Queue:     testQueue,
		Consumer:  testConsumer,
		EventType: testEventType,
		Handler:   &mockHandler{},
	})

	// Create new registry and replay
	log := logger.New("debug", true)
	mockClient := NewRecordingAMQPClient()
	targetRegistry := messaging.NewRegistry(mockClient, log)

	err := declarations.ReplayToRegistry(targetRegistry)
	require.NoError(t, err)

	// Verify replay
	replayedExchanges := targetRegistry.GetExchanges()
	assert.Len(t, replayedExchanges, 1)
	assert.Contains(t, replayedExchanges, testExchange)

	replayedQueues := targetRegistry.GetQueues()
	assert.Len(t, replayedQueues, 1)
	assert.Contains(t, replayedQueues, testQueue)

	replayedBindings := targetRegistry.GetBindings()
	assert.Len(t, replayedBindings, 1)
	assert.Equal(t, testQueue, replayedBindings[0].Queue)

	replayedPublishers := targetRegistry.GetPublishers()
	assert.Len(t, replayedPublishers, 1)
	assert.Equal(t, testExchange, replayedPublishers[0].Exchange)

	replayedConsumers := targetRegistry.GetConsumers()
	assert.Len(t, replayedConsumers, 1)
	assert.Equal(t, testQueue, replayedConsumers[0].Queue)
}

func TestMessagingDeclarationsValidate(t *testing.T) {
	t.Run("valid_declarations", func(t *testing.T) {
		declarations := NewMessagingDeclarations()

		// Add valid infrastructure
		declarations.Exchanges[testExchange] = &messaging.ExchangeDeclaration{
			Name: testExchange,
			Type: "topic",
		}

		declarations.Queues[testQueue] = &messaging.QueueDeclaration{
			Name: testQueue,
		}

		declarations.Bindings = append(declarations.Bindings, &messaging.BindingDeclaration{
			Queue:    testQueue,
			Exchange: testExchange,
		})

		declarations.Publishers = append(declarations.Publishers, &messaging.PublisherDeclaration{
			Exchange: testExchange,
		})

		declarations.Consumers = append(declarations.Consumers, &messaging.ConsumerDeclaration{
			Queue: testQueue,
		})

		err := declarations.Validate()
		assert.NoError(t, err)
	})

	t.Run("binding_references_undefined_queue", func(t *testing.T) {
		declarations := NewMessagingDeclarations()

		declarations.Exchanges[testExchange] = &messaging.ExchangeDeclaration{
			Name: testExchange,
		}

		// Binding references undefined queue
		declarations.Bindings = append(declarations.Bindings, &messaging.BindingDeclaration{
			Queue:    "undefined.queue",
			Exchange: testExchange,
		})

		err := declarations.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "binding references undefined queue: undefined.queue")
	})

	t.Run("binding_references_undefined_exchange", func(t *testing.T) {
		declarations := NewMessagingDeclarations()

		declarations.Queues[testQueue] = &messaging.QueueDeclaration{
			Name: testQueue,
		}

		// Binding references undefined exchange
		declarations.Bindings = append(declarations.Bindings, &messaging.BindingDeclaration{
			Queue:    testQueue,
			Exchange: "undefined.exchange",
		})

		err := declarations.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "binding references undefined exchange: undefined.exchange")
	})

	t.Run("consumer_references_undefined_queue", func(t *testing.T) {
		declarations := NewMessagingDeclarations()

		// Consumer references undefined queue
		declarations.Consumers = append(declarations.Consumers, &messaging.ConsumerDeclaration{
			Queue: "undefined.queue",
		})

		err := declarations.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "consumer references undefined queue: undefined.queue")
	})

	t.Run("publisher_references_undefined_exchange", func(t *testing.T) {
		declarations := NewMessagingDeclarations()

		// Publisher references undefined exchange
		declarations.Publishers = append(declarations.Publishers, &messaging.PublisherDeclaration{
			Exchange: "undefined.exchange",
		})

		err := declarations.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "publisher references undefined exchange: undefined.exchange")
	})
}

func TestMessagingDeclarationsStats(t *testing.T) {
	declarations := NewMessagingDeclarations()

	// Empty declarations
	stats := declarations.Stats()
	assert.Equal(t, 0, stats.Exchanges)
	assert.Equal(t, 0, stats.Queues)
	assert.Equal(t, 0, stats.Bindings)
	assert.Equal(t, 0, stats.Publishers)
	assert.Equal(t, 0, stats.Consumers)
	assert.True(t, declarations.IsEmpty())

	// Add some declarations
	declarations.Exchanges["ex1"] = &messaging.ExchangeDeclaration{Name: "ex1"}
	declarations.Exchanges["ex2"] = &messaging.ExchangeDeclaration{Name: "ex2"}
	declarations.Queues["q1"] = &messaging.QueueDeclaration{Name: "q1"}
	declarations.Bindings = append(declarations.Bindings, &messaging.BindingDeclaration{})
	declarations.Publishers = append(declarations.Publishers, &messaging.PublisherDeclaration{})
	declarations.Publishers = append(declarations.Publishers, &messaging.PublisherDeclaration{})
	declarations.Consumers = append(declarations.Consumers, &messaging.ConsumerDeclaration{})

	stats = declarations.Stats()
	assert.Equal(t, 2, stats.Exchanges)
	assert.Equal(t, 1, stats.Queues)
	assert.Equal(t, 1, stats.Bindings)
	assert.Equal(t, 2, stats.Publishers)
	assert.Equal(t, 1, stats.Consumers)
	assert.False(t, declarations.IsEmpty())

	// Test string representation
	statsStr := stats.String()
	assert.Contains(t, statsStr, "Exchanges: 2")
	assert.Contains(t, statsStr, "Queues: 1")
	assert.Contains(t, statsStr, "Bindings: 1")
	assert.Contains(t, statsStr, "Publishers: 2")
	assert.Contains(t, statsStr, "Consumers: 1")
}

func TestMessagingDeclarationsBackwardCompatibility(t *testing.T) {
	// Test that the Declarations alias works
	declarations := NewMessagingDeclarations()
	assert.NotNil(t, declarations)

	// Test that the old Replay method still works
	log := logger.New("debug", true)
	mockClient := NewRecordingAMQPClient()
	registry := messaging.NewRegistry(mockClient, log)

	// Should not panic
	declarations.Replay(context.TODO(), registry)
}

// Mock handler for testing
type mockHandler struct{}

func (h *mockHandler) Handle(_ context.Context, _ *amqp.Delivery) error {
	return nil
}

func (h *mockHandler) EventType() string {
	return "test.event"
}
