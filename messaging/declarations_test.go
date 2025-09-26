package messaging

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

const (
	modifiedValue = "modified"
)

// Mock message handler for testing
type mockHandler struct {
	mock.Mock
}

func (m *mockHandler) Handle(ctx context.Context, delivery *amqp.Delivery) error {
	args := m.Called(ctx, delivery)
	return args.Error(0)
}

func (m *mockHandler) EventType() string {
	args := m.Called()
	return args.String(0)
}

// Mock registry for testing ReplayToRegistry
type mockRegistry struct {
	mock.Mock
	exchanges  []*ExchangeDeclaration
	queues     []*QueueDeclaration
	bindings   []*BindingDeclaration
	publishers []*PublisherDeclaration
	consumers  []*ConsumerDeclaration
}

func (m *mockRegistry) RegisterExchange(e *ExchangeDeclaration) {
	m.Called(e)
	m.exchanges = append(m.exchanges, e)
}

func (m *mockRegistry) RegisterQueue(q *QueueDeclaration) {
	m.Called(q)
	m.queues = append(m.queues, q)
}

func (m *mockRegistry) RegisterBinding(b *BindingDeclaration) {
	m.Called(b)
	m.bindings = append(m.bindings, b)
}

func (m *mockRegistry) RegisterPublisher(p *PublisherDeclaration) {
	m.Called(p)
	m.publishers = append(m.publishers, p)
}

func (m *mockRegistry) RegisterConsumer(c *ConsumerDeclaration) {
	m.Called(c)
	m.consumers = append(m.consumers, c)
}

// Additional methods required by RegistryInterface
func (m *mockRegistry) DeclareInfrastructure(ctx context.Context) error {
	args := m.MethodCalled("DeclareInfrastructure", ctx)
	return args.Error(0)
}

func (m *mockRegistry) StartConsumers(ctx context.Context) error {
	args := m.MethodCalled("StartConsumers", ctx)
	return args.Error(0)
}

func (m *mockRegistry) StopConsumers() {
	m.Called()
}

func (m *mockRegistry) GetExchanges() map[string]*ExchangeDeclaration {
	args := m.Called()
	return args.Get(0).(map[string]*ExchangeDeclaration)
}

func (m *mockRegistry) GetQueues() map[string]*QueueDeclaration {
	args := m.Called()
	return args.Get(0).(map[string]*QueueDeclaration)
}

func (m *mockRegistry) GetBindings() []*BindingDeclaration {
	args := m.Called()
	return args.Get(0).([]*BindingDeclaration)
}

func (m *mockRegistry) GetPublishers() []*PublisherDeclaration {
	args := m.Called()
	return args.Get(0).([]*PublisherDeclaration)
}

func (m *mockRegistry) GetConsumers() []*ConsumerDeclaration {
	args := m.Called()
	return args.Get(0).([]*ConsumerDeclaration)
}

func (m *mockRegistry) ValidateConsumer(consumerTag string) bool {
	args := m.Called(consumerTag)
	return args.Bool(0)
}

func (m *mockRegistry) ValidatePublisher(exchange, routingKey string) bool {
	args := m.Called(exchange, routingKey)
	return args.Bool(0)
}

func TestNewDeclarations(t *testing.T) {
	t.Run("creates empty declarations", func(t *testing.T) {
		decls := NewDeclarations()

		assert.NotNil(t, decls)
		assert.NotNil(t, decls.Exchanges)
		assert.NotNil(t, decls.Queues)
		assert.NotNil(t, decls.Bindings)
		assert.NotNil(t, decls.Publishers)
		assert.NotNil(t, decls.Consumers)

		assert.Empty(t, decls.Exchanges)
		assert.Empty(t, decls.Queues)
		assert.Empty(t, decls.Bindings)
		assert.Empty(t, decls.Publishers)
		assert.Empty(t, decls.Consumers)
	})
}

func TestDeclarationsRegisterExchange(t *testing.T) {
	t.Run("registers exchange with deep copy", func(t *testing.T) {
		decls := NewDeclarations()
		exchange := &ExchangeDeclaration{
			Name:       testExchange,
			Type:       "topic",
			Durable:    true,
			AutoDelete: false,
			Internal:   false,
			NoWait:     false,
			Args:       map[string]any{"key": "value"},
		}

		decls.RegisterExchange(exchange)

		assert.Len(t, decls.Exchanges, 1)
		registered := decls.Exchanges[testExchange]
		assert.NotNil(t, registered)
		assert.Equal(t, exchange.Name, registered.Name)
		assert.Equal(t, exchange.Type, registered.Type)
		assert.Equal(t, exchange.Durable, registered.Durable)
		assert.Equal(t, "value", registered.Args["key"])

		// Verify it's a deep copy by modifying original
		exchange.Name = modifiedValue
		exchange.Args["key"] = modifiedValue
		assert.Equal(t, testExchange, registered.Name)
		assert.Equal(t, "value", registered.Args["key"])
	})

	t.Run("handles nil exchange", func(t *testing.T) {
		decls := NewDeclarations()

		decls.RegisterExchange(nil)

		assert.Empty(t, decls.Exchanges)
	})

	t.Run("handles nil args", func(t *testing.T) {
		decls := NewDeclarations()
		exchange := &ExchangeDeclaration{
			Name: testExchange,
			Type: "direct",
			Args: nil,
		}

		decls.RegisterExchange(exchange)

		registered := decls.Exchanges[testExchange]
		assert.NotNil(t, registered.Args)
		assert.Empty(t, registered.Args)
	})

	t.Run("overwrites existing exchange", func(t *testing.T) {
		decls := NewDeclarations()
		exchange1 := &ExchangeDeclaration{Name: "test", Type: "direct"}
		exchange2 := &ExchangeDeclaration{Name: "test", Type: "topic"}

		decls.RegisterExchange(exchange1)
		decls.RegisterExchange(exchange2)

		assert.Len(t, decls.Exchanges, 1)
		assert.Equal(t, "topic", decls.Exchanges["test"].Type)
	})
}

func TestDeclarationsRegisterQueue(t *testing.T) {
	t.Run("registers queue with deep copy", func(t *testing.T) {
		decls := NewDeclarations()
		queue := &QueueDeclaration{
			Name:       testQueue,
			Durable:    true,
			AutoDelete: false,
			Exclusive:  false,
			NoWait:     false,
			Args:       map[string]any{"ttl": 3600},
		}

		decls.RegisterQueue(queue)

		assert.Len(t, decls.Queues, 1)
		registered := decls.Queues[testQueue]
		assert.NotNil(t, registered)
		assert.Equal(t, queue.Name, registered.Name)
		assert.Equal(t, queue.Durable, registered.Durable)
		assert.Equal(t, 3600, registered.Args["ttl"])

		// Verify deep copy
		queue.Name = modifiedValue
		queue.Args["ttl"] = 7200
		assert.Equal(t, testQueue, registered.Name)
		assert.Equal(t, 3600, registered.Args["ttl"])
	})

	t.Run("handles nil queue", func(t *testing.T) {
		decls := NewDeclarations()

		decls.RegisterQueue(nil)

		assert.Empty(t, decls.Queues)
	})

	t.Run("handles nil args", func(t *testing.T) {
		decls := NewDeclarations()
		queue := &QueueDeclaration{Name: testQueue, Args: nil}

		decls.RegisterQueue(queue)

		registered := decls.Queues[testQueue]
		assert.NotNil(t, registered.Args)
		assert.Empty(t, registered.Args)
	})
}

func TestDeclarationsRegisterBinding(t *testing.T) {
	t.Run("registers binding with deep copy", func(t *testing.T) {
		decls := NewDeclarations()
		binding := &BindingDeclaration{
			Queue:      testQueue,
			Exchange:   testExchange,
			RoutingKey: "test.key",
			NoWait:     false,
			Args:       map[string]any{"binding": "arg"},
		}

		decls.RegisterBinding(binding)

		assert.Len(t, decls.Bindings, 1)
		registered := decls.Bindings[0]
		assert.Equal(t, binding.Queue, registered.Queue)
		assert.Equal(t, binding.Exchange, registered.Exchange)
		assert.Equal(t, binding.RoutingKey, registered.RoutingKey)
		assert.Equal(t, "arg", registered.Args["binding"])

		// Verify deep copy
		binding.Queue = modifiedValue
		binding.Args["binding"] = modifiedValue
		assert.Equal(t, testQueue, registered.Queue)
		assert.Equal(t, "arg", registered.Args["binding"])
	})

	t.Run("handles nil binding", func(t *testing.T) {
		decls := NewDeclarations()

		decls.RegisterBinding(nil)

		assert.Empty(t, decls.Bindings)
	})

	t.Run("appends multiple bindings", func(t *testing.T) {
		decls := NewDeclarations()
		binding1 := &BindingDeclaration{Queue: "queue1", Exchange: "exchange1"}
		binding2 := &BindingDeclaration{Queue: "queue2", Exchange: "exchange2"}

		decls.RegisterBinding(binding1)
		decls.RegisterBinding(binding2)

		assert.Len(t, decls.Bindings, 2)
		assert.Equal(t, "queue1", decls.Bindings[0].Queue)
		assert.Equal(t, "queue2", decls.Bindings[1].Queue)
	})
}

func TestDeclarationsRegisterPublisher(t *testing.T) {
	t.Run("registers publisher with deep copy", func(t *testing.T) {
		decls := NewDeclarations()
		publisher := &PublisherDeclaration{
			Exchange:    testExchange,
			RoutingKey:  "test.key",
			EventType:   testEventType,
			Description: "User creation event",
			Mandatory:   true,
			Immediate:   false,
			Headers:     map[string]any{"version": "1.0"},
		}

		decls.RegisterPublisher(publisher)

		assert.Len(t, decls.Publishers, 1)
		registered := decls.Publishers[0]
		assert.Equal(t, publisher.Exchange, registered.Exchange)
		assert.Equal(t, publisher.EventType, registered.EventType)
		assert.Equal(t, "1.0", registered.Headers["version"])

		// Verify deep copy
		publisher.EventType = modifiedValue
		publisher.Headers["version"] = "2.0"
		assert.Equal(t, testEventType, registered.EventType)
		assert.Equal(t, "1.0", registered.Headers["version"])
	})

	t.Run("handles nil publisher", func(t *testing.T) {
		decls := NewDeclarations()

		decls.RegisterPublisher(nil)

		assert.Empty(t, decls.Publishers)
	})

	t.Run("handles nil headers", func(t *testing.T) {
		decls := NewDeclarations()
		publisher := &PublisherDeclaration{Exchange: "test", Headers: nil}

		decls.RegisterPublisher(publisher)

		registered := decls.Publishers[0]
		assert.NotNil(t, registered.Headers)
		assert.Empty(t, registered.Headers)
	})
}

func TestDeclarationsRegisterConsumer(t *testing.T) {
	t.Run("registers consumer", func(t *testing.T) {
		decls := NewDeclarations()
		handler := &mockHandler{}
		consumer := &ConsumerDeclaration{
			Queue:       testQueue,
			Consumer:    "test-consumer",
			AutoAck:     false,
			Exclusive:   true,
			NoLocal:     false,
			NoWait:      false,
			EventType:   testEventType,
			Description: "Process user creation",
			Handler:     handler,
		}

		decls.RegisterConsumer(consumer)

		assert.Len(t, decls.Consumers, 1)
		registered := decls.Consumers[0]
		assert.Equal(t, consumer.Queue, registered.Queue)
		assert.Equal(t, consumer.EventType, registered.EventType)
		assert.Equal(t, handler, registered.Handler)
	})

	t.Run("handles nil consumer", func(t *testing.T) {
		decls := NewDeclarations()

		decls.RegisterConsumer(nil)

		assert.Empty(t, decls.Consumers)
	})
}

func TestDeclarationsValidate(t *testing.T) {
	t.Run("valid declarations", func(t *testing.T) {
		decls := NewDeclarations()
		decls.RegisterExchange(&ExchangeDeclaration{Name: testExchange, Type: "topic"})
		decls.RegisterQueue(&QueueDeclaration{Name: testQueue})
		decls.RegisterBinding(&BindingDeclaration{Queue: testQueue, Exchange: testExchange})
		decls.RegisterPublisher(&PublisherDeclaration{Exchange: testExchange})
		decls.RegisterConsumer(&ConsumerDeclaration{Queue: testQueue})

		err := decls.Validate()

		assert.NoError(t, err)
	})

	t.Run("binding references non-existent queue", func(t *testing.T) {
		decls := NewDeclarations()
		decls.RegisterExchange(&ExchangeDeclaration{Name: testExchange, Type: "topic"})
		decls.RegisterBinding(&BindingDeclaration{Queue: "missing-queue", Exchange: testExchange})

		err := decls.Validate()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "binding references non-existent queue: missing-queue")
	})

	t.Run("binding references non-existent exchange", func(t *testing.T) {
		decls := NewDeclarations()
		decls.RegisterQueue(&QueueDeclaration{Name: testQueue})
		decls.RegisterBinding(&BindingDeclaration{Queue: testQueue, Exchange: "missing-exchange"})

		err := decls.Validate()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "binding references non-existent exchange: missing-exchange")
	})

	t.Run("consumer references non-existent queue", func(t *testing.T) {
		decls := NewDeclarations()
		decls.RegisterConsumer(&ConsumerDeclaration{Queue: "missing-queue"})

		err := decls.Validate()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "consumer references non-existent queue: missing-queue")
	})

	t.Run("publisher references non-existent exchange", func(t *testing.T) {
		decls := NewDeclarations()
		decls.RegisterPublisher(&PublisherDeclaration{Exchange: "missing-exchange"})

		err := decls.Validate()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "publisher references non-existent exchange: missing-exchange")
	})

	t.Run("empty declarations are valid", func(t *testing.T) {
		decls := NewDeclarations()

		err := decls.Validate()

		assert.NoError(t, err)
	})
}

func TestDeclarationsReplayToRegistry(t *testing.T) {
	t.Run("replays all declarations in correct order", func(t *testing.T) {
		decls := NewDeclarations()
		mockReg := &mockRegistry{}

		// Set up declarations
		exchange := &ExchangeDeclaration{Name: testExchange, Type: "topic"}
		queue := &QueueDeclaration{Name: testQueue}
		binding := &BindingDeclaration{Queue: testQueue, Exchange: testExchange}
		publisher := &PublisherDeclaration{Exchange: testExchange}
		consumer := &ConsumerDeclaration{Queue: testQueue}

		decls.RegisterExchange(exchange)
		decls.RegisterQueue(queue)
		decls.RegisterBinding(binding)
		decls.RegisterPublisher(publisher)
		decls.RegisterConsumer(consumer)

		// Set up expectations
		mockReg.On("RegisterExchange", mock.AnythingOfType("*messaging.ExchangeDeclaration"))
		mockReg.On("RegisterQueue", mock.AnythingOfType("*messaging.QueueDeclaration"))
		mockReg.On("RegisterBinding", mock.AnythingOfType("*messaging.BindingDeclaration"))
		mockReg.On("RegisterPublisher", mock.AnythingOfType("*messaging.PublisherDeclaration"))
		mockReg.On("RegisterConsumer", mock.AnythingOfType("*messaging.ConsumerDeclaration"))

		err := decls.ReplayToRegistry(mockReg)

		assert.NoError(t, err)
		mockReg.AssertExpectations(t)

		// Verify order: exchanges first, then queues, then bindings, then publishers/consumers
		assert.Len(t, mockReg.exchanges, 1)
		assert.Len(t, mockReg.queues, 1)
		assert.Len(t, mockReg.bindings, 1)
		assert.Len(t, mockReg.publishers, 1)
		assert.Len(t, mockReg.consumers, 1)
	})

	t.Run("handles nil registry", func(t *testing.T) {
		decls := NewDeclarations()

		err := decls.ReplayToRegistry(nil)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "registry is nil")
	})

	t.Run("handles empty declarations", func(t *testing.T) {
		decls := NewDeclarations()
		mockReg := &mockRegistry{}

		err := decls.ReplayToRegistry(mockReg)

		assert.NoError(t, err)
		assert.Empty(t, mockReg.exchanges)
		assert.Empty(t, mockReg.queues)
		assert.Empty(t, mockReg.bindings)
		assert.Empty(t, mockReg.publishers)
		assert.Empty(t, mockReg.consumers)
	})
}

func TestDeclarationsStats(t *testing.T) {
	t.Run("returns correct counts", func(t *testing.T) {
		decls := NewDeclarations()
		decls.RegisterExchange(&ExchangeDeclaration{Name: "ex1", Type: "topic"})
		decls.RegisterExchange(&ExchangeDeclaration{Name: "ex2", Type: "direct"})
		decls.RegisterQueue(&QueueDeclaration{Name: "q1"})
		decls.RegisterBinding(&BindingDeclaration{Queue: "q1", Exchange: "ex1"})
		decls.RegisterBinding(&BindingDeclaration{Queue: "q1", Exchange: "ex2"})
		decls.RegisterBinding(&BindingDeclaration{Queue: "q1", Exchange: "ex1", RoutingKey: "different"})
		decls.RegisterPublisher(&PublisherDeclaration{Exchange: "ex1"})
		decls.RegisterConsumer(&ConsumerDeclaration{Queue: "q1"})

		stats := decls.Stats()

		assert.Equal(t, 2, stats.Exchanges)
		assert.Equal(t, 1, stats.Queues)
		assert.Equal(t, 3, stats.Bindings)
		assert.Equal(t, 1, stats.Publishers)
		assert.Equal(t, 1, stats.Consumers)
	})

	t.Run("empty declarations return zero counts", func(t *testing.T) {
		decls := NewDeclarations()

		stats := decls.Stats()

		assert.Equal(t, 0, stats.Exchanges)
		assert.Equal(t, 0, stats.Queues)
		assert.Equal(t, 0, stats.Bindings)
		assert.Equal(t, 0, stats.Publishers)
		assert.Equal(t, 0, stats.Consumers)
	})
}

func TestDeclarationsClone(t *testing.T) {
	t.Run("creates deep copy of all declarations", func(t *testing.T) {
		decls := NewDeclarations()
		handler := &mockHandler{}

		decls.RegisterExchange(&ExchangeDeclaration{
			Name: testExchange,
			Type: "topic",
			Args: map[string]any{"key": "value"},
		})
		decls.RegisterQueue(&QueueDeclaration{
			Name: testQueue,
			Args: map[string]any{"ttl": 3600},
		})
		decls.RegisterBinding(&BindingDeclaration{
			Queue:    testQueue,
			Exchange: testExchange,
			Args:     map[string]any{"binding": "arg"},
		})
		decls.RegisterPublisher(&PublisherDeclaration{
			Exchange: testExchange,
			Headers:  map[string]any{"version": "1.0"},
		})
		decls.RegisterConsumer(&ConsumerDeclaration{
			Queue:   testQueue,
			Handler: handler,
		})

		clone := decls.Clone()

		// Verify all counts match
		assert.Equal(t, len(decls.Exchanges), len(clone.Exchanges))
		assert.Equal(t, len(decls.Queues), len(clone.Queues))
		assert.Equal(t, len(decls.Bindings), len(clone.Bindings))
		assert.Equal(t, len(decls.Publishers), len(clone.Publishers))
		assert.Equal(t, len(decls.Consumers), len(clone.Consumers))

		// Verify deep copy of exchanges
		originalExchange := decls.Exchanges[testExchange]
		clonedExchange := clone.Exchanges[testExchange]
		assert.Equal(t, originalExchange.Name, clonedExchange.Name)
		assert.Equal(t, originalExchange.Args["key"], clonedExchange.Args["key"])

		// Modify original and verify clone is unaffected
		originalExchange.Name = modifiedValue
		originalExchange.Args["key"] = modifiedValue
		assert.Equal(t, testExchange, clonedExchange.Name)
		assert.Equal(t, "value", clonedExchange.Args["key"])

		// Verify handlers are shared (shallow copy)
		assert.Equal(t, handler, clone.Consumers[0].Handler)
	})

	t.Run("handles nil args in clone", func(t *testing.T) {
		decls := NewDeclarations()
		decls.RegisterExchange(&ExchangeDeclaration{Name: "test", Args: nil})
		decls.RegisterQueue(&QueueDeclaration{Name: "test", Args: nil})
		decls.RegisterBinding(&BindingDeclaration{Queue: "test", Exchange: "test", Args: nil})
		decls.RegisterPublisher(&PublisherDeclaration{Exchange: "test", Headers: nil})

		clone := decls.Clone()

		assert.NotNil(t, clone.Exchanges["test"].Args)
		assert.NotNil(t, clone.Queues["test"].Args)
		assert.NotNil(t, clone.Bindings[0].Args)
		assert.NotNil(t, clone.Publishers[0].Headers)
	})

	t.Run("clone of empty declarations", func(t *testing.T) {
		decls := NewDeclarations()

		clone := decls.Clone()

		assert.NotNil(t, clone)
		assert.Empty(t, clone.Exchanges)
		assert.Empty(t, clone.Queues)
		assert.Empty(t, clone.Bindings)
		assert.Empty(t, clone.Publishers)
		assert.Empty(t, clone.Consumers)
	})
}
