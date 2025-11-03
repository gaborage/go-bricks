package messaging

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Test constructor functions

func TestNewTopicExchange(t *testing.T) {
	t.Run("creates exchange with production defaults", func(t *testing.T) {
		exchange := NewTopicExchange("test.exchange")

		assert.Equal(t, "test.exchange", exchange.Name)
		assert.Equal(t, exchangeTypeTopic, exchange.Type)
		assert.True(t, exchange.Durable)
		assert.False(t, exchange.AutoDelete)
		assert.False(t, exchange.Internal)
		assert.False(t, exchange.NoWait)
		assert.NotNil(t, exchange.Args)
		assert.Empty(t, exchange.Args)
	})

	t.Run("handles empty name", func(t *testing.T) {
		exchange := NewTopicExchange("")

		assert.Equal(t, "", exchange.Name)
		assert.Equal(t, exchangeTypeTopic, exchange.Type)
		assert.True(t, exchange.Durable)
	})

	t.Run("creates independent instances", func(t *testing.T) {
		ex1 := NewTopicExchange("exchange1")
		ex2 := NewTopicExchange("exchange2")

		ex1.Args[testKey] = testValue1
		ex2.Args[testKey] = testValue2

		assert.Equal(t, testValue1, ex1.Args[testKey])
		assert.Equal(t, testValue2, ex2.Args[testKey])
	})
}

func TestNewQueue(t *testing.T) {
	t.Run("creates queue with production defaults", func(t *testing.T) {
		queue := NewQueue("test.queue")

		assert.Equal(t, "test.queue", queue.Name)
		assert.True(t, queue.Durable)
		assert.False(t, queue.AutoDelete)
		assert.False(t, queue.Exclusive)
		assert.False(t, queue.NoWait)
		assert.NotNil(t, queue.Args)
		assert.Empty(t, queue.Args)
	})

	t.Run("handles empty name", func(t *testing.T) {
		queue := NewQueue("")

		assert.Equal(t, "", queue.Name)
		assert.True(t, queue.Durable)
	})

	t.Run("creates independent instances", func(t *testing.T) {
		q1 := NewQueue("queue1")
		q2 := NewQueue("queue2")

		q1.Args[mapKeyTTL] = ttlValue3600
		q2.Args[mapKeyTTL] = ttlValue7200

		assert.Equal(t, ttlValue3600, q1.Args[mapKeyTTL])
		assert.Equal(t, ttlValue7200, q2.Args[mapKeyTTL])
	})
}

func TestNewBinding(t *testing.T) {
	t.Run("creates binding with correct fields", func(t *testing.T) {
		binding := NewBinding("my.queue", "my.exchange", "routing.key.*")

		assert.Equal(t, "my.queue", binding.Queue)
		assert.Equal(t, "my.exchange", binding.Exchange)
		assert.Equal(t, "routing.key.*", binding.RoutingKey)
		assert.False(t, binding.NoWait)
		assert.NotNil(t, binding.Args)
		assert.Empty(t, binding.Args)
	})

	t.Run("handles empty routing key", func(t *testing.T) {
		binding := NewBinding("queue", "exchange", "")

		assert.Equal(t, "", binding.RoutingKey)
	})

	t.Run("creates independent instances", func(t *testing.T) {
		b1 := NewBinding("q1", "ex1", "key1")
		b2 := NewBinding("q2", "ex2", "key2")

		b1.Args["arg1"] = testValue1
		b2.Args["arg2"] = testValue2

		assert.Equal(t, testValue1, b1.Args["arg1"])
		assert.Nil(t, b1.Args["arg2"])
		assert.Equal(t, testValue2, b2.Args["arg2"])
		assert.Nil(t, b2.Args["arg1"])
	})
}

func TestNewPublisher(t *testing.T) {
	t.Run("creates publisher with all options", func(t *testing.T) {
		opts := &PublisherOptions{
			Exchange:    "test.exchange",
			RoutingKey:  "test.key",
			EventType:   eventTestEvent,
			Description: "Test event publisher",
			Headers:     map[string]any{mapKeyVersion: version10, "source": "test"},
			Mandatory:   true,
			Immediate:   true,
		}

		publisher := NewPublisher(opts)

		assert.Equal(t, "test.exchange", publisher.Exchange)
		assert.Equal(t, "test.key", publisher.RoutingKey)
		assert.Equal(t, eventTestEvent, publisher.EventType)
		assert.Equal(t, "Test event publisher", publisher.Description)
		assert.True(t, publisher.Mandatory)
		assert.True(t, publisher.Immediate)
		assert.Equal(t, version10, publisher.Headers[mapKeyVersion])
		assert.Equal(t, "test", publisher.Headers["source"])
	})

	t.Run("creates publisher with minimal options", func(t *testing.T) {
		opts := &PublisherOptions{
			Exchange:   "minimal.exchange",
			RoutingKey: "minimal.key",
		}

		publisher := NewPublisher(opts)

		assert.Equal(t, "minimal.exchange", publisher.Exchange)
		assert.Equal(t, "minimal.key", publisher.RoutingKey)
		assert.Equal(t, "", publisher.EventType)
		assert.Equal(t, "", publisher.Description)
		assert.False(t, publisher.Mandatory)
		assert.False(t, publisher.Immediate)
		assert.NotNil(t, publisher.Headers)
		assert.Empty(t, publisher.Headers)
	})

	t.Run("creates empty headers map when nil", func(t *testing.T) {
		opts := &PublisherOptions{
			Exchange:   "test.exchange",
			RoutingKey: "test.key",
			Headers:    nil,
		}

		publisher := NewPublisher(opts)

		assert.NotNil(t, publisher.Headers)
		assert.Empty(t, publisher.Headers)
	})

	t.Run("preserves provided headers map", func(t *testing.T) {
		headers := map[string]any{testKey: testValue}
		opts := &PublisherOptions{
			Exchange:   "test.exchange",
			RoutingKey: "test.key",
			Headers:    headers,
		}

		publisher := NewPublisher(opts)

		assert.Equal(t, headers, publisher.Headers)
		assert.Equal(t, testValue, publisher.Headers[testKey])
	})
}

func TestNewConsumer(t *testing.T) {
	mockHandler := &mockHandler{}

	t.Run("creates consumer with all options", func(t *testing.T) {
		opts := &ConsumerOptions{
			Queue:       "test.queue",
			Consumer:    testConsumer,
			EventType:   eventTestEvent,
			Description: "Test event consumer",
			Handler:     mockHandler,
			AutoAck:     true,
			Exclusive:   true,
			NoLocal:     true,
		}

		consumer := NewConsumer(opts)

		assert.Equal(t, "test.queue", consumer.Queue)
		assert.Equal(t, testConsumer, consumer.Consumer)
		assert.Equal(t, eventTestEvent, consumer.EventType)
		assert.Equal(t, "Test event consumer", consumer.Description)
		assert.Equal(t, mockHandler, consumer.Handler)
		assert.True(t, consumer.AutoAck)
		assert.True(t, consumer.Exclusive)
		assert.True(t, consumer.NoLocal)
		assert.False(t, consumer.NoWait) // Always false per spec
	})

	t.Run("creates consumer with minimal options", func(t *testing.T) {
		opts := &ConsumerOptions{
			Queue:    "minimal.queue",
			Consumer: testConsumer,
		}

		consumer := NewConsumer(opts)

		assert.Equal(t, "minimal.queue", consumer.Queue)
		assert.Equal(t, testConsumer, consumer.Consumer)
		assert.Equal(t, "", consumer.EventType)
		assert.Equal(t, "", consumer.Description)
		assert.Nil(t, consumer.Handler)
		assert.False(t, consumer.AutoAck)
		assert.False(t, consumer.Exclusive)
		assert.False(t, consumer.NoLocal)
		assert.False(t, consumer.NoWait)
	})

	t.Run("handles nil handler", func(t *testing.T) {
		opts := &ConsumerOptions{
			Queue:    "test.queue",
			Consumer: testConsumer,
			Handler:  nil,
		}

		consumer := NewConsumer(opts)

		assert.Nil(t, consumer.Handler)
	})
}

// Test Declarations convenience methods

func TestDeclarationsTopicExchange(t *testing.T) {
	t.Run("creates and registers exchange", func(t *testing.T) {
		decls := NewDeclarations()

		exchange := decls.DeclareTopicExchange("test.exchange")

		assert.NotNil(t, exchange)
		assert.Equal(t, "test.exchange", exchange.Name)
		assert.Equal(t, exchangeTypeTopic, exchange.Type)
		assert.True(t, exchange.Durable)

		// Verify it's registered
		assert.Len(t, decls.Exchanges, 1)
		registered := decls.Exchanges["test.exchange"]
		assert.NotNil(t, registered)
		assert.Equal(t, "test.exchange", registered.Name)
	})

	t.Run("overwrites existing exchange with same name", func(t *testing.T) {
		decls := NewDeclarations()

		ex1 := decls.DeclareTopicExchange("test.exchange")
		// Modify registered copy directly
		decls.Exchanges["test.exchange"].Args["first"] = "value1"

		// Declare again with same name - this creates a new exchange and overwrites
		ex2 := decls.DeclareTopicExchange("test.exchange")

		assert.Len(t, decls.Exchanges, 1)
		registered := decls.Exchanges["test.exchange"]
		// Second declaration overwrites, so "first" is lost
		assert.Nil(t, registered.Args["first"])
		// New exchange has empty args by default
		assert.Empty(t, registered.Args)
		assert.NotNil(t, ex1)
		assert.NotNil(t, ex2)
	})

	t.Run("allows multiple different exchanges", func(t *testing.T) {
		decls := NewDeclarations()

		ex1 := decls.DeclareTopicExchange("exchange1")
		ex2 := decls.DeclareTopicExchange("exchange2")

		assert.NotNil(t, ex1)
		assert.NotNil(t, ex2)
		assert.Len(t, decls.Exchanges, 2)
	})
}

func TestDeclarationsQueue(t *testing.T) {
	t.Run("creates and registers queue", func(t *testing.T) {
		decls := NewDeclarations()

		queue := decls.DeclareQueue("test.queue")

		assert.NotNil(t, queue)
		assert.Equal(t, "test.queue", queue.Name)
		assert.True(t, queue.Durable)

		// Verify it's registered
		assert.Len(t, decls.Queues, 1)
		registered := decls.Queues["test.queue"]
		assert.NotNil(t, registered)
		assert.Equal(t, "test.queue", registered.Name)
	})

	t.Run("overwrites existing queue with same name", func(t *testing.T) {
		decls := NewDeclarations()

		q1 := decls.DeclareQueue("test.queue")
		// Modify registered copy directly
		decls.Queues["test.queue"].Args["first"] = "value1"

		// Declare again with same name - this creates a new queue and overwrites
		q2 := decls.DeclareQueue("test.queue")

		assert.Len(t, decls.Queues, 1)
		registered := decls.Queues["test.queue"]
		// Second declaration overwrites, so "first" is lost
		assert.Nil(t, registered.Args["first"])
		// New queue has empty args by default
		assert.Empty(t, registered.Args)
		assert.NotNil(t, q1)
		assert.NotNil(t, q2)
	})

	t.Run("allows multiple different queues", func(t *testing.T) {
		decls := NewDeclarations()

		q1 := decls.DeclareQueue("queue1")
		q2 := decls.DeclareQueue("queue2")

		assert.NotNil(t, q1)
		assert.NotNil(t, q2)
		assert.Len(t, decls.Queues, 2)
	})
}

func TestDeclarationsBinding(t *testing.T) {
	t.Run("creates and registers binding", func(t *testing.T) {
		decls := NewDeclarations()

		binding := decls.DeclareBinding("test.queue", "test.exchange", "test.key")

		assert.NotNil(t, binding)
		assert.Equal(t, "test.queue", binding.Queue)
		assert.Equal(t, "test.exchange", binding.Exchange)
		assert.Equal(t, "test.key", binding.RoutingKey)

		// Verify it's registered
		assert.Len(t, decls.Bindings, 1)
		assert.Equal(t, "test.queue", decls.Bindings[0].Queue)
	})

	t.Run("allows multiple bindings", func(t *testing.T) {
		decls := NewDeclarations()

		b1 := decls.DeclareBinding("queue1", "exchange1", "key1")
		b2 := decls.DeclareBinding("queue2", "exchange2", "key2")

		assert.NotNil(t, b1)
		assert.NotNil(t, b2)
		assert.Len(t, decls.Bindings, 2)
	})

	t.Run("allows same queue bound to multiple exchanges", func(t *testing.T) {
		decls := NewDeclarations()

		b1 := decls.DeclareBinding("queue1", "exchange1", "key1")
		b2 := decls.DeclareBinding("queue1", "exchange2", "key2")

		assert.NotNil(t, b1)
		assert.NotNil(t, b2)
		assert.Len(t, decls.Bindings, 2)
	})
}

func TestDeclarationsPublisher(t *testing.T) {
	t.Run("creates and registers publisher without auto-declare", func(t *testing.T) {
		decls := NewDeclarations()

		opts := &PublisherOptions{
			Exchange:    "test.exchange",
			RoutingKey:  "test.key",
			EventType:   "TestEvent",
			Description: "Test publisher",
		}

		publisher := decls.DeclarePublisher(opts, nil)

		assert.NotNil(t, publisher)
		assert.Equal(t, "test.exchange", publisher.Exchange)
		assert.Equal(t, "TestEvent", publisher.EventType)

		// Verify publisher registered
		assert.Len(t, decls.Publishers, 1)
		assert.Equal(t, "test.exchange", decls.Publishers[0].Exchange)

		// Verify exchange NOT auto-registered
		assert.Len(t, decls.Exchanges, 0)
	})

	t.Run("auto-registers exchange when provided", func(t *testing.T) {
		decls := NewDeclarations()

		exchange := NewTopicExchange("auto.exchange")
		opts := &PublisherOptions{
			Exchange:   "auto.exchange",
			RoutingKey: "auto.key",
			EventType:  "AutoEvent",
		}

		publisher := decls.DeclarePublisher(opts, exchange)

		assert.NotNil(t, publisher)

		// Verify both publisher and exchange registered
		assert.Len(t, decls.Publishers, 1)
		assert.Len(t, decls.Exchanges, 1)
		assert.Equal(t, "auto.exchange", decls.Exchanges["auto.exchange"].Name)
	})

	t.Run("does not re-register already registered exchange", func(t *testing.T) {
		decls := NewDeclarations()

		// Pre-register exchange with custom args (modify registered copy)
		decls.DeclareTopicExchange("existing.exchange")
		decls.Exchanges["existing.exchange"].Args["custom"] = "value"

		// Try to auto-register same exchange - should skip because already exists
		newExchange := NewTopicExchange("existing.exchange")
		opts := &PublisherOptions{
			Exchange:   "existing.exchange",
			RoutingKey: "test.key",
		}

		publisher := decls.DeclarePublisher(opts, newExchange)

		assert.NotNil(t, publisher)
		assert.Len(t, decls.Exchanges, 1)

		// Original exchange should be preserved (not overwritten)
		registered := decls.Exchanges["existing.exchange"]
		assert.Equal(t, "value", registered.Args["custom"])
	})

	t.Run("allows multiple publishers", func(t *testing.T) {
		decls := NewDeclarations()

		opts1 := &PublisherOptions{Exchange: "ex1", RoutingKey: "key1"}
		opts2 := &PublisherOptions{Exchange: "ex2", RoutingKey: "key2"}

		p1 := decls.DeclarePublisher(opts1, nil)
		p2 := decls.DeclarePublisher(opts2, nil)

		assert.NotNil(t, p1)
		assert.NotNil(t, p2)
		assert.Len(t, decls.Publishers, 2)
	})
}

func TestDeclarationsConsumer(t *testing.T) {
	mockHandler := &mockHandler{}

	t.Run("creates and registers consumer without auto-declare", func(t *testing.T) {
		decls := NewDeclarations()

		opts := &ConsumerOptions{
			Queue:       "test.queue",
			Consumer:    testConsumer,
			EventType:   eventTestEvent,
			Description: "Test consumer",
			Handler:     mockHandler,
		}

		consumer := decls.DeclareConsumer(opts, nil)

		assert.NotNil(t, consumer)
		assert.Equal(t, "test.queue", consumer.Queue)
		assert.Equal(t, eventTestEvent, consumer.EventType)

		// Verify consumer registered
		consumers := decls.GetConsumers()
		assert.Len(t, consumers, 1)
		assert.Equal(t, "test.queue", consumers[0].Queue)

		// Verify queue NOT auto-registered
		assert.Len(t, decls.Queues, 0)
	})

	t.Run("auto-registers queue when provided", func(t *testing.T) {
		decls := NewDeclarations()

		queue := NewQueue("auto.queue")
		opts := &ConsumerOptions{
			Queue:     "auto.queue",
			Consumer:  "auto-consumer",
			EventType: "AutoEvent",
			Handler:   mockHandler,
		}

		consumer := decls.DeclareConsumer(opts, queue)

		assert.NotNil(t, consumer)

		// Verify both consumer and queue registered
		assert.Len(t, decls.GetConsumers(), 1)
		assert.Len(t, decls.Queues, 1)
		assert.Equal(t, "auto.queue", decls.Queues["auto.queue"].Name)
	})

	t.Run("does not re-register already registered queue", func(t *testing.T) {
		decls := NewDeclarations()

		// Pre-register queue with custom args (modify registered copy)
		decls.DeclareQueue("existing.queue")
		decls.Queues["existing.queue"].Args["custom"] = "value"

		// Try to auto-register same queue - should skip because already exists
		newQueue := NewQueue("existing.queue")
		opts := &ConsumerOptions{
			Queue:    "existing.queue",
			Consumer: testConsumer,
			Handler:  mockHandler,
		}

		consumer := decls.DeclareConsumer(opts, newQueue)

		assert.NotNil(t, consumer)
		assert.Len(t, decls.Queues, 1)

		// Original queue should be preserved (not overwritten)
		registered := decls.Queues["existing.queue"]
		assert.Equal(t, "value", registered.Args["custom"])
	})

	t.Run("allows multiple consumers", func(t *testing.T) {
		decls := NewDeclarations()

		opts1 := &ConsumerOptions{Queue: "q1", Consumer: "c1", Handler: mockHandler}
		opts2 := &ConsumerOptions{Queue: "q2", Consumer: "c2", Handler: mockHandler}

		c1 := decls.DeclareConsumer(opts1, nil)
		c2 := decls.DeclareConsumer(opts2, nil)

		assert.NotNil(t, c1)
		assert.NotNil(t, c2)
		assert.Len(t, decls.GetConsumers(), 2)
	})
}

// Integration tests

func TestHelpersIntegrationWorkflow(t *testing.T) {
	t.Run("complete workflow using helpers", func(t *testing.T) {
		decls := NewDeclarations()

		// Declare infrastructure
		exchange := decls.DeclareTopicExchange("orders.events")
		queue := decls.DeclareQueue("orders.processing")
		binding := decls.DeclareBinding(queue.Name, exchange.Name, "order.*")

		// Declare publisher (exchange already registered, pass nil)
		publisher := decls.DeclarePublisher(&PublisherOptions{
			Exchange:    exchange.Name,
			RoutingKey:  "order.created",
			EventType:   "OrderCreated",
			Description: "New order events",
			Headers:     map[string]any{mapKeyVersion: version10},
		}, nil)

		// Declare consumer (queue already registered, pass nil)
		consumer := decls.DeclareConsumer(&ConsumerOptions{
			Queue:       queue.Name,
			Consumer:    testConsumer,
			EventType:   "OrderCreated",
			Description: "Process new orders",
			Handler:     &mockHandler{},
		}, nil)

		// Verify all registered
		assert.NotNil(t, exchange)
		assert.NotNil(t, queue)
		assert.NotNil(t, binding)
		assert.NotNil(t, publisher)
		assert.NotNil(t, consumer)

		// Verify counts
		stats := decls.Stats()
		assert.Equal(t, 1, stats.Exchanges)
		assert.Equal(t, 1, stats.Queues)
		assert.Equal(t, 1, stats.Bindings)
		assert.Equal(t, 1, stats.Publishers)
		assert.Equal(t, 1, stats.Consumers)

		// Verify validation passes
		err := decls.Validate()
		assert.NoError(t, err)
	})

	t.Run("workflow with auto-declare", func(t *testing.T) {
		decls := NewDeclarations()

		// Create exchange and queue but don't register yet
		exchange := NewTopicExchange("notifications.events")
		queue := NewQueue("notifications.delivery")

		// Auto-register via publisher/consumer
		decls.DeclarePublisher(&PublisherOptions{
			Exchange:   exchange.Name,
			RoutingKey: "notification.sent",
		}, exchange)

		decls.DeclareConsumer(&ConsumerOptions{
			Queue:    queue.Name,
			Consumer: testConsumer,
		}, queue)

		// Binding still needs explicit declaration
		decls.DeclareBinding(queue.Name, exchange.Name, "notification.*")

		// Verify all registered
		assert.Len(t, decls.Exchanges, 1)
		assert.Len(t, decls.Queues, 1)
		assert.Len(t, decls.Bindings, 1)
		assert.Len(t, decls.Publishers, 1)
		assert.Len(t, decls.GetConsumers(), 1)

		err := decls.Validate()
		assert.NoError(t, err)
	})

	t.Run("helpers-created declarations can be replayed", func(t *testing.T) {
		decls := NewDeclarations()
		mockReg := &mockRegistry{}

		// Create declarations using helpers
		decls.DeclareTopicExchange("test.exchange")
		decls.DeclareQueue("test.queue")
		decls.DeclareBinding("test.queue", "test.exchange", "test.*")
		decls.DeclarePublisher(&PublisherOptions{
			Exchange:   "test.exchange",
			RoutingKey: "test.created",
		}, nil)
		decls.DeclareConsumer(&ConsumerOptions{
			Queue:    "test.queue",
			Consumer: testConsumer,
		}, nil)

		// Set up mock expectations
		mockReg.On("RegisterExchange", mock.AnythingOfType("*messaging.ExchangeDeclaration"))
		mockReg.On("RegisterQueue", mock.AnythingOfType("*messaging.QueueDeclaration"))
		mockReg.On("RegisterBinding", mock.AnythingOfType("*messaging.BindingDeclaration"))
		mockReg.On("RegisterPublisher", mock.AnythingOfType("*messaging.PublisherDeclaration"))
		mockReg.On("RegisterConsumer", mock.AnythingOfType("*messaging.ConsumerDeclaration"))

		// Replay to registry
		err := decls.ReplayToRegistry(mockReg)

		assert.NoError(t, err)
		mockReg.AssertExpectations(t)
	})

	t.Run("helpers-created declarations can be cloned", func(t *testing.T) {
		decls := NewDeclarations()

		// Create declarations using helpers and modify registered copies
		decls.DeclareTopicExchange("clone.exchange")
		decls.Exchanges["clone.exchange"].Args["custom"] = "original"

		decls.DeclareQueue("clone.queue")
		decls.Queues["clone.queue"].Args[mapKeyTTL] = ttlValue3600

		// Clone
		clone := decls.Clone()

		// Verify clone has same structure
		assert.Equal(t, len(decls.Exchanges), len(clone.Exchanges))
		assert.Equal(t, len(decls.Queues), len(clone.Queues))

		// Verify deep copy
		clonedExchange := clone.Exchanges["clone.exchange"]
		assert.Equal(t, "original", clonedExchange.Args["custom"])

		// Modify original registered copy
		decls.Exchanges["clone.exchange"].Args["custom"] = modifiedValue

		// Clone should be unaffected
		assert.Equal(t, "original", clonedExchange.Args["custom"])
	})
}
