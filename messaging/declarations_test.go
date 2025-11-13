package messaging

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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
		assert.NotNil(t, decls.GetConsumers())

		assert.Empty(t, decls.Exchanges)
		assert.Empty(t, decls.Queues)
		assert.Empty(t, decls.Bindings)
		assert.Empty(t, decls.Publishers)
		assert.Empty(t, decls.GetConsumers())
	})
}

func TestDeclarationsRegisterExchange(t *testing.T) {
	t.Run("registers exchange with deep copy", func(t *testing.T) {
		decls := NewDeclarations()
		exchange := &ExchangeDeclaration{
			Name:       testExchange,
			Type:       exchangeTypeTopic,
			Durable:    true,
			AutoDelete: false,
			Internal:   false,
			NoWait:     false,
			Args:       map[string]any{testKey: testValue},
		}

		decls.RegisterExchange(exchange)

		assert.Len(t, decls.Exchanges, 1)
		registered := decls.Exchanges[testExchange]
		assert.NotNil(t, registered)
		assert.Equal(t, exchange.Name, registered.Name)
		assert.Equal(t, exchange.Type, registered.Type)
		assert.Equal(t, exchange.Durable, registered.Durable)
		assert.Equal(t, testValue, registered.Args[testKey])

		// Verify it's a deep copy by modifying original
		exchange.Name = modifiedValue
		exchange.Args[testKey] = modifiedValue
		assert.Equal(t, testExchange, registered.Name)
		assert.Equal(t, testValue, registered.Args[testKey])
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
			Type: exchangeTypeDirect,
			Args: nil,
		}

		decls.RegisterExchange(exchange)

		registered := decls.Exchanges[testExchange]
		assert.NotNil(t, registered.Args)
		assert.Empty(t, registered.Args)
	})

	t.Run("overwrites existing exchange", func(t *testing.T) {
		decls := NewDeclarations()
		exchange1 := &ExchangeDeclaration{Name: testName, Type: exchangeTypeDirect}
		exchange2 := &ExchangeDeclaration{Name: testName, Type: exchangeTypeTopic}

		decls.RegisterExchange(exchange1)
		decls.RegisterExchange(exchange2)

		assert.Len(t, decls.Exchanges, 1)
		assert.Equal(t, exchangeTypeTopic, decls.Exchanges[testName].Type)
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
			Args:       map[string]any{mapKeyTTL: ttlValue3600},
		}

		decls.RegisterQueue(queue)

		assert.Len(t, decls.Queues, 1)
		registered := decls.Queues[testQueue]
		assert.NotNil(t, registered)
		assert.Equal(t, queue.Name, registered.Name)
		assert.Equal(t, queue.Durable, registered.Durable)
		assert.Equal(t, ttlValue3600, registered.Args[mapKeyTTL])

		// Verify deep copy
		queue.Name = modifiedValue
		queue.Args[mapKeyTTL] = ttlValue7200
		assert.Equal(t, testQueue, registered.Name)
		assert.Equal(t, ttlValue3600, registered.Args[mapKeyTTL])
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
			RoutingKey: testRoutingKey,
			NoWait:     false,
			Args:       map[string]any{mapKeyBinding: testArg},
		}

		decls.RegisterBinding(binding)

		assert.Len(t, decls.Bindings, 1)
		registered := decls.Bindings[0]
		assert.Equal(t, binding.Queue, registered.Queue)
		assert.Equal(t, binding.Exchange, registered.Exchange)
		assert.Equal(t, binding.RoutingKey, registered.RoutingKey)
		assert.Equal(t, testArg, registered.Args[mapKeyBinding])

		// Verify deep copy
		binding.Queue = modifiedValue
		binding.Args[mapKeyBinding] = modifiedValue
		assert.Equal(t, testQueue, registered.Queue)
		assert.Equal(t, testArg, registered.Args[mapKeyBinding])
	})

	t.Run("handles nil binding", func(t *testing.T) {
		decls := NewDeclarations()

		decls.RegisterBinding(nil)

		assert.Empty(t, decls.Bindings)
	})

	t.Run("appends multiple bindings", func(t *testing.T) {
		decls := NewDeclarations()
		binding1 := &BindingDeclaration{Queue: testQueue1, Exchange: testExchange1}
		binding2 := &BindingDeclaration{Queue: testQueue2, Exchange: testExchange2}

		decls.RegisterBinding(binding1)
		decls.RegisterBinding(binding2)

		assert.Len(t, decls.Bindings, 2)
		assert.Equal(t, testQueue1, decls.Bindings[0].Queue)
		assert.Equal(t, testQueue2, decls.Bindings[1].Queue)
	})
}

func TestDeclarationsRegisterPublisher(t *testing.T) {
	t.Run("registers publisher with deep copy", func(t *testing.T) {
		decls := NewDeclarations()
		publisher := &PublisherDeclaration{
			Exchange:    testExchange,
			RoutingKey:  testRoutingKey,
			EventType:   testEventType,
			Description: descUserCreation,
			Mandatory:   true,
			Immediate:   false,
			Headers:     map[string]any{mapKeyVersion: version10},
		}

		decls.RegisterPublisher(publisher)

		assert.Len(t, decls.Publishers, 1)
		registered := decls.Publishers[0]
		assert.Equal(t, publisher.Exchange, registered.Exchange)
		assert.Equal(t, publisher.EventType, registered.EventType)
		assert.Equal(t, version10, registered.Headers[mapKeyVersion])

		// Verify deep copy
		publisher.EventType = modifiedValue
		publisher.Headers[mapKeyVersion] = version20
		assert.Equal(t, testEventType, registered.EventType)
		assert.Equal(t, version10, registered.Headers[mapKeyVersion])
	})

	t.Run("handles nil publisher", func(t *testing.T) {
		decls := NewDeclarations()

		decls.RegisterPublisher(nil)

		assert.Empty(t, decls.Publishers)
	})

	t.Run("handles nil headers", func(t *testing.T) {
		decls := NewDeclarations()
		publisher := &PublisherDeclaration{Exchange: testName, Headers: nil}

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
			Consumer:    testConsumer,
			AutoAck:     false,
			Exclusive:   true,
			NoLocal:     false,
			NoWait:      false,
			EventType:   testEventType,
			Description: descProcessUser,
			Handler:     handler,
		}

		decls.RegisterConsumer(consumer)

		consumers := decls.GetConsumers()
		assert.Len(t, consumers, 1)
		registered := consumers[0]
		assert.Equal(t, consumer.Queue, registered.Queue)
		assert.Equal(t, consumer.EventType, registered.EventType)
		assert.Equal(t, handler, registered.Handler)
	})

	t.Run("handles nil consumer", func(t *testing.T) {
		decls := NewDeclarations()

		decls.RegisterConsumer(nil)

		assert.Empty(t, decls.GetConsumers())
	})

	t.Run("panics on duplicate consumer registration", func(t *testing.T) {
		decls := NewDeclarations()
		handler := &mockHandler{}
		consumer := &ConsumerDeclaration{
			Queue:     testQueue,
			Consumer:  testConsumer,
			EventType: testEventType,
			Handler:   handler,
		}

		// First registration succeeds
		decls.RegisterConsumer(consumer)

		// Verify registered
		consumers := decls.GetConsumers()
		assert.Len(t, consumers, 1)
		assert.Equal(t, testQueue, consumers[0].Queue)

		// Second registration with same queue+consumer+event_type panics
		assert.Panics(t, func() {
			decls.RegisterConsumer(consumer)
		})
	})

	t.Run("allows different queue with same consumer tag", func(t *testing.T) {
		decls := NewDeclarations()

		// Different queues with same consumer tag and event type are allowed
		assert.NotPanics(t, func() {
			decls.RegisterConsumer(&ConsumerDeclaration{
				Queue:     testQueue1,
				Consumer:  genericConsumer,
				EventType: testEventType,
			})
			decls.RegisterConsumer(&ConsumerDeclaration{
				Queue:     testQueue2,
				Consumer:  genericConsumer,
				EventType: testEventType,
			})
		})

		assert.Len(t, decls.GetConsumers(), 2)
	})

	t.Run("allows same queue with different consumer tag", func(t *testing.T) {
		decls := NewDeclarations()

		// Same queue with different consumer tags are allowed
		assert.NotPanics(t, func() {
			decls.RegisterConsumer(&ConsumerDeclaration{
				Queue:     testQueue,
				Consumer:  testConsumer1,
				EventType: testEventType,
			})
			decls.RegisterConsumer(&ConsumerDeclaration{
				Queue:     testQueue,
				Consumer:  testConsumer2,
				EventType: testEventType,
			})
		})

		assert.Len(t, decls.GetConsumers(), 2)
	})

	t.Run("allows same queue+consumer with different event type", func(t *testing.T) {
		decls := NewDeclarations()

		// Same queue+consumer with different event types are allowed
		assert.NotPanics(t, func() {
			decls.RegisterConsumer(&ConsumerDeclaration{
				Queue:     testQueue,
				Consumer:  genericConsumer,
				EventType: testEvent1,
			})
			decls.RegisterConsumer(&ConsumerDeclaration{
				Queue:     testQueue,
				Consumer:  genericConsumer,
				EventType: testEvent2,
			})
		})

		assert.Len(t, decls.GetConsumers(), 2)
	})

	t.Run("GetConsumers returns consumers in registration order", func(t *testing.T) {
		decls := NewDeclarations()

		decls.RegisterConsumer(&ConsumerDeclaration{
			Queue:     testQueue1,
			Consumer:  testConsumer1,
			EventType: testEvent1,
		})
		decls.RegisterConsumer(&ConsumerDeclaration{
			Queue:     testQueue2,
			Consumer:  testConsumer2,
			EventType: testEvent2,
		})
		decls.RegisterConsumer(&ConsumerDeclaration{
			Queue:     testQueue3,
			Consumer:  testConsumer3,
			EventType: testEvent3,
		})

		consumers := decls.GetConsumers()
		assert.Len(t, consumers, 3)
		assert.Equal(t, testQueue1, consumers[0].Queue)
		assert.Equal(t, testQueue2, consumers[1].Queue)
		assert.Equal(t, testQueue3, consumers[2].Queue)
	})
}

func TestDeclarationsValidate(t *testing.T) {
	t.Run("valid declarations", func(t *testing.T) {
		decls := NewDeclarations()
		decls.RegisterExchange(&ExchangeDeclaration{Name: testExchange, Type: exchangeTypeTopic})
		decls.RegisterQueue(&QueueDeclaration{Name: testQueue})
		decls.RegisterBinding(&BindingDeclaration{Queue: testQueue, Exchange: testExchange})
		decls.RegisterPublisher(&PublisherDeclaration{Exchange: testExchange})
		decls.RegisterConsumer(&ConsumerDeclaration{Queue: testQueue})

		err := decls.Validate()

		assert.NoError(t, err)
	})

	t.Run("binding references non-existent queue", func(t *testing.T) {
		decls := NewDeclarations()
		decls.RegisterExchange(&ExchangeDeclaration{Name: testExchange, Type: exchangeTypeTopic})
		decls.RegisterBinding(&BindingDeclaration{Queue: missingQueue, Exchange: testExchange})

		err := decls.Validate()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "binding references non-existent queue: missing-queue")
	})

	t.Run("binding references non-existent exchange", func(t *testing.T) {
		decls := NewDeclarations()
		decls.RegisterQueue(&QueueDeclaration{Name: testQueue})
		decls.RegisterBinding(&BindingDeclaration{Queue: testQueue, Exchange: missingExchange})

		err := decls.Validate()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "binding references non-existent exchange: missing-exchange")
	})

	t.Run("consumer references non-existent queue", func(t *testing.T) {
		decls := NewDeclarations()
		decls.RegisterConsumer(&ConsumerDeclaration{Queue: missingQueue})

		err := decls.Validate()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "consumer references non-existent queue: missing-queue")
	})

	t.Run("publisher references non-existent exchange", func(t *testing.T) {
		decls := NewDeclarations()
		decls.RegisterPublisher(&PublisherDeclaration{Exchange: missingExchange})

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
		exchange := &ExchangeDeclaration{Name: testExchange, Type: exchangeTypeTopic}
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
		decls.RegisterExchange(&ExchangeDeclaration{Name: shortExchange1, Type: exchangeTypeTopic})
		decls.RegisterExchange(&ExchangeDeclaration{Name: shortExchange2, Type: exchangeTypeDirect})
		decls.RegisterQueue(&QueueDeclaration{Name: shortQueue1})
		decls.RegisterBinding(&BindingDeclaration{Queue: shortQueue1, Exchange: shortExchange1})
		decls.RegisterBinding(&BindingDeclaration{Queue: shortQueue1, Exchange: shortExchange2})
		decls.RegisterBinding(&BindingDeclaration{Queue: shortQueue1, Exchange: shortExchange1, RoutingKey: "different"})
		decls.RegisterPublisher(&PublisherDeclaration{Exchange: shortExchange1})
		decls.RegisterConsumer(&ConsumerDeclaration{Queue: shortQueue1})

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
			Type: exchangeTypeTopic,
			Args: map[string]any{testKey: testValue},
		})
		decls.RegisterQueue(&QueueDeclaration{
			Name: testQueue,
			Args: map[string]any{mapKeyTTL: ttlValue3600},
		})
		decls.RegisterBinding(&BindingDeclaration{
			Queue:    testQueue,
			Exchange: testExchange,
			Args:     map[string]any{mapKeyBinding: testArg},
		})
		decls.RegisterPublisher(&PublisherDeclaration{
			Exchange: testExchange,
			Headers:  map[string]any{mapKeyVersion: version10},
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
		assert.Equal(t, len(decls.GetConsumers()), len(clone.GetConsumers()))

		// Verify deep copy of exchanges
		originalExchange := decls.Exchanges[testExchange]
		clonedExchange := clone.Exchanges[testExchange]
		assert.Equal(t, originalExchange.Name, clonedExchange.Name)
		assert.Equal(t, originalExchange.Args[testKey], clonedExchange.Args[testKey])

		// Modify original and verify clone is unaffected
		originalExchange.Name = modifiedValue
		originalExchange.Args[testKey] = modifiedValue
		assert.Equal(t, testExchange, clonedExchange.Name)
		assert.Equal(t, testValue, clonedExchange.Args[testKey])

		// Verify handlers are shared (shallow copy)
		assert.Equal(t, handler, clone.GetConsumers()[0].Handler)
	})

	t.Run("handles nil args in clone", func(t *testing.T) {
		decls := NewDeclarations()
		decls.RegisterExchange(&ExchangeDeclaration{Name: testName, Args: nil})
		decls.RegisterQueue(&QueueDeclaration{Name: testName, Args: nil})
		decls.RegisterBinding(&BindingDeclaration{Queue: testName, Exchange: testName, Args: nil})
		decls.RegisterPublisher(&PublisherDeclaration{Exchange: testName, Headers: nil})

		clone := decls.Clone()

		assert.NotNil(t, clone.Exchanges[testName].Args)
		assert.NotNil(t, clone.Queues[testName].Args)
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
		assert.Empty(t, clone.GetConsumers())
	})
}

// TestRegisterConsumerPreservesWorkerPoolConfig verifies that Workers and PrefetchCount
// fields are preserved during consumer registration (regression test for v0.17.0 bug).
func TestRegisterConsumerPreservesWorkerPoolConfig(t *testing.T) {
	decls := NewDeclarations()

	consumer := &ConsumerDeclaration{
		Queue:         testQueue,
		Consumer:      testConsumer,
		EventType:     testEventType,
		Handler:       &mockHandler{},
		Workers:       48,
		PrefetchCount: 480,
	}

	decls.RegisterConsumer(consumer)

	consumers := decls.GetConsumers()
	assert.Len(t, consumers, 1)
	assert.Equal(t, 48, consumers[0].Workers, "Workers field should be preserved during registration")
	assert.Equal(t, 480, consumers[0].PrefetchCount, "PrefetchCount field should be preserved during registration")
}

// TestClonePreservesWorkerPoolConfig verifies that Workers and PrefetchCount
// fields are preserved during declaration cloning (regression test for v0.17.0 bug).
func TestClonePreservesWorkerPoolConfig(t *testing.T) {
	original := NewDeclarations()
	original.RegisterConsumer(&ConsumerDeclaration{
		Queue:         testQueue,
		Consumer:      testConsumer,
		EventType:     testEventType,
		Handler:       &mockHandler{},
		Workers:       48,
		PrefetchCount: 480,
	})

	clone := original.Clone()
	consumers := clone.GetConsumers()

	assert.Len(t, consumers, 1)
	assert.Equal(t, 48, consumers[0].Workers, "Workers field should be preserved during cloning")
	assert.Equal(t, 480, consumers[0].PrefetchCount, "PrefetchCount field should be preserved during cloning")
}

// TestReplayToRegistryPreservesWorkerPoolConfig verifies that Workers and PrefetchCount
// are propagated correctly through the entire declaration pipeline.
func TestReplayToRegistryPreservesWorkerPoolConfig(t *testing.T) {
	decls := NewDeclarations()
	decls.RegisterConsumer(&ConsumerDeclaration{
		Queue:         testQueue,
		Consumer:      testConsumer,
		EventType:     testEventType,
		Handler:       &mockHandler{},
		Workers:       48,
		PrefetchCount: 480,
	})

	registry := &mockRegistry{}
	registry.On("RegisterConsumer", mock.Anything).Return()

	err := decls.ReplayToRegistry(registry)
	assert.NoError(t, err)

	assert.Len(t, registry.consumers, 1)
	assert.Equal(t, 48, registry.consumers[0].Workers, "Workers should propagate through ReplayToRegistry")
	assert.Equal(t, 480, registry.consumers[0].PrefetchCount, "PrefetchCount should propagate through ReplayToRegistry")
}
