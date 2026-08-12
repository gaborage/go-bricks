package messaging

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
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

func (m *mockRegistry) Exchanges() map[string]*ExchangeDeclaration {
	args := m.Called()
	return args.Get(0).(map[string]*ExchangeDeclaration)
}

func (m *mockRegistry) Queues() map[string]*QueueDeclaration {
	args := m.Called()
	return args.Get(0).(map[string]*QueueDeclaration)
}

func (m *mockRegistry) Bindings() []*BindingDeclaration {
	args := m.Called()
	return args.Get(0).([]*BindingDeclaration)
}

func (m *mockRegistry) Publishers() []*PublisherDeclaration {
	args := m.Called()
	return args.Get(0).([]*PublisherDeclaration)
}

func (m *mockRegistry) Consumers() []*ConsumerDeclaration {
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
		assert.NotNil(t, decls.Consumers())

		assert.Empty(t, decls.Exchanges)
		assert.Empty(t, decls.Queues)
		assert.Empty(t, decls.Bindings)
		assert.Empty(t, decls.Publishers)
		assert.Empty(t, decls.Consumers())
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

		consumers := decls.Consumers()
		assert.Len(t, consumers, 1)
		registered := consumers[0]
		assert.Equal(t, consumer.Queue, registered.Queue)
		assert.Equal(t, consumer.EventType, registered.EventType)
		assert.Equal(t, handler, registered.Handler)
	})

	t.Run("handles nil consumer", func(t *testing.T) {
		decls := NewDeclarations()

		decls.RegisterConsumer(nil)

		assert.Empty(t, decls.Consumers())
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
		consumers := decls.Consumers()
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

		assert.Len(t, decls.Consumers(), 2)
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

		assert.Len(t, decls.Consumers(), 2)
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

		assert.Len(t, decls.Consumers(), 2)
	})

	t.Run("Consumers returns consumers in registration order", func(t *testing.T) {
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

		consumers := decls.Consumers()
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
		assert.Equal(t, len(decls.Consumers()), len(clone.Consumers()))

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
		assert.Equal(t, handler, clone.Consumers()[0].Handler)
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
		assert.Empty(t, clone.Consumers())
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

	consumers := decls.Consumers()
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
	consumers := clone.Consumers()

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

// TestDeclarationsIsEmpty covers the IsEmpty fast-path. The function delegates
// to Stats(), so we only need to verify the empty and non-empty branches.
func TestDeclarationsIsEmpty(t *testing.T) {
	t.Run("empty declarations", func(t *testing.T) {
		d := NewDeclarations()
		assert.True(t, d.IsEmpty())
	})

	t.Run("becomes non-empty after a single registration", func(t *testing.T) {
		d := NewDeclarations()
		d.RegisterQueue(&QueueDeclaration{Name: testQueue})
		assert.False(t, d.IsEmpty())
	})
}

// TestDeclarationsHashCoversAllArgTypes exercises Hash() with a publisher whose
// Args map contains every supported value type. This drives writeMapArgs through
// each typed branch (string/int/int64/bool/float64/default) and exercises
// writeInt64 / writeFloat64.
func TestDeclarationsHashCoversAllArgTypes(t *testing.T) {
	d := NewDeclarations()
	d.RegisterExchange(&ExchangeDeclaration{Name: testExchange, Type: exchangeTypeTopic, Durable: true})
	d.RegisterQueue(&QueueDeclaration{
		Name:    testQueue,
		Durable: true,
		Args: map[string]any{
			"string_arg":  "value",
			"int_arg":     int(42),
			"int64_arg":   int64(1024),
			"bool_arg":    true,
			"float64_arg": 1.5,
			"other_arg":   []string{"non-primitive falls to fmt.Fprintf branch"},
		},
	})
	d.RegisterBinding(&BindingDeclaration{Queue: testQueue, Exchange: testExchange, RoutingKey: testRoutingKey})
	d.RegisterPublisher(&PublisherDeclaration{
		Exchange:   testExchange,
		RoutingKey: testRoutingKey,
		EventType:  eventTestEvent,
	})

	first := d.Hash()
	assert.NotZero(t, first, "Hash with multi-typed args must be deterministic and non-zero")

	// Second call must return the same value — confirms map-iteration order is
	// neutralized by the sorted-keys logic inside writeMapArgs.
	second := d.Hash()
	assert.Equal(t, first, second, "Hash must be deterministic across calls regardless of map iteration order")
}

// --- Queue re-declaration merge semantics ---

const (
	mergeQueue  = "orders.events.queue"
	mergeQueueB = "payments.events.queue"
	mergeQueueC = "shipping.events.queue"

	// Chosen so sorted Args iteration order is a-key < b-key < z-key.
	argKeyA = "a-key"
	argKeyB = "b-key"
	argKeyZ = "z-key"
)

// TestRegisterQueueMergePreservesDLQArgs pins the regression: before the merge,
// whichever of the two declarations ran last replaced the other outright, so one
// ordering silently reverted dead-lettering to "drop on handler error".
func TestRegisterQueueMergePreservesDLQArgs(t *testing.T) {
	t.Run("dlq_first_then_plain", func(t *testing.T) {
		d := NewDeclarations()
		d.DeclareQueueWithDLQ(mergeQueue, nil)
		d.DeclareQueue(mergeQueue)

		require.NoError(t, d.Validate())
		assert.Len(t, d.Queues, 2, "primary queue plus its parking queue")
		assert.Equal(t, mergeQueue+".dlx", d.Queues[mergeQueue].Args[dlxArgKey])
	})

	t.Run("plain_first_then_dlq", func(t *testing.T) {
		d := NewDeclarations()
		d.DeclareQueue(mergeQueue)
		d.DeclareQueueWithDLQ(mergeQueue, nil)

		require.NoError(t, d.Validate())
		assert.Equal(t, mergeQueue+".dlx", d.Queues[mergeQueue].Args[dlxArgKey])
	})

	// The cross-module shape: one module declares the queue with its own broker
	// args, another adds dead-lettering. Neither call site can see the other, and
	// before the merge the later one wiped the earlier one's args wholesale.
	t.Run("custom_args_survive_a_later_dlq_declaration", func(t *testing.T) {
		d := NewDeclarations()
		d.RegisterQueue(&QueueDeclaration{
			Name:    mergeQueue,
			Durable: true,
			Args:    map[string]any{mapKeyTTL: ttlValue3600},
		})
		d.DeclareQueueWithDLQ(mergeQueue, nil)

		require.NoError(t, d.Validate())
		stored := d.Queues[mergeQueue]
		assert.Equal(t, ttlValue3600, stored.Args[mapKeyTTL], "incumbent's own args must survive")
		assert.Equal(t, mergeQueue+".dlx", stored.Args[dlxArgKey])
	})
}

func TestRegisterQueueIdenticalIsIdempotent(t *testing.T) {
	d := NewDeclarations()
	q := &QueueDeclaration{Name: testQueue, Durable: true, Args: map[string]any{mapKeyTTL: ttlValue3600}}

	d.RegisterQueue(q)
	d.RegisterQueue(q)

	require.NoError(t, d.Validate())
	assert.Len(t, d.Queues, 1)
	assert.Equal(t, ttlValue3600, d.Queues[testQueue].Args[mapKeyTTL])
}

func TestRegisterQueueConflictingArgsRecorded(t *testing.T) {
	t.Run("single_contested_key", func(t *testing.T) {
		d := NewDeclarations()
		d.RegisterQueue(&QueueDeclaration{Name: mergeQueue, Durable: true, Args: map[string]any{dlxArgKey: "orders.dlx"}})
		d.RegisterQueue(&QueueDeclaration{Name: mergeQueue, Durable: true, Args: map[string]any{dlxArgKey: "other.dlx"}})

		err := d.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), mergeQueue)
		assert.Contains(t, err.Error(), `Args["`+dlxArgKey+`"] kept "orders.dlx" vs rejected "other.dlx"`)
	})

	// Two keys disagree at once — the only shape that can distinguish sorted
	// iteration from bare map order, which would report either key at random.
	t.Run("lexicographically_first_contested_key_is_reported", func(t *testing.T) {
		d := NewDeclarations()
		d.RegisterQueue(&QueueDeclaration{Name: mergeQueue, Args: map[string]any{
			argKeyA: "a-incumbent",
			argKeyZ: "z-incumbent",
		}})
		d.RegisterQueue(&QueueDeclaration{Name: mergeQueue, Args: map[string]any{
			argKeyA: "a-rejected",
			argKeyZ: "z-rejected",
		}})

		err := d.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), `Args["`+argKeyA+`"] kept "a-incumbent" vs rejected "a-rejected"`)
		assert.NotContains(t, err.Error(), argKeyZ)
		assert.NotContains(t, err.Error(), "z-incumbent")
	})
}

func TestRegisterQueueConflictingFlagsRecorded(t *testing.T) {
	// want is asserted verbatim: the kept/rejected labels are what tell an
	// operator which of the two call sites is in effect, so their order in the
	// message is part of the contract (and is reproduced in wiki/messaging.md).
	tests := []struct {
		name  string
		first *QueueDeclaration
		next  *QueueDeclaration
		want  string
	}{
		{
			name:  "durable_mismatch",
			first: &QueueDeclaration{Name: testQueue, Durable: true},
			next:  &QueueDeclaration{Name: testQueue, Durable: false},
			want:  `Durable kept "true" vs rejected "false"`,
		},
		{
			name:  "autodelete_mismatch",
			first: &QueueDeclaration{Name: testQueue, AutoDelete: false},
			next:  &QueueDeclaration{Name: testQueue, AutoDelete: true},
			want:  `AutoDelete kept "false" vs rejected "true"`,
		},
		{
			name:  "exclusive_mismatch",
			first: &QueueDeclaration{Name: testQueue, Exclusive: false},
			next:  &QueueDeclaration{Name: testQueue, Exclusive: true},
			want:  `Exclusive kept "false" vs rejected "true"`,
		},
		{
			name:  "nowait_mismatch",
			first: &QueueDeclaration{Name: testQueue, NoWait: false},
			next:  &QueueDeclaration{Name: testQueue, NoWait: true},
			want:  `NoWait kept "false" vs rejected "true"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDeclarations()
			d.RegisterQueue(tt.first)
			d.RegisterQueue(tt.next)

			err := d.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), testQueue)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// TestRegisterQueueDeduplicatesIdenticalConflicts keeps the aggregate error an
// enumeration of distinct problems: repeating one disagreement must not inflate
// the count, which would also make the count depend on declaration order.
func TestRegisterQueueDeduplicatesIdenticalConflicts(t *testing.T) {
	d := NewDeclarations()
	d.RegisterQueue(&QueueDeclaration{Name: mergeQueue, Durable: true})
	for range 3 {
		d.RegisterQueue(&QueueDeclaration{Name: mergeQueue, Durable: false})
	}

	require.Len(t, d.queueConflicts, 1)

	err := d.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("(%d conflict(s))", len(d.queueConflicts)))
	assert.Equal(t, 1, strings.Count(err.Error(), `Durable kept "true" vs rejected "false"`),
		"one detail line per distinct conflict")
}

// TestRegisterQueueConflictKeepsIncumbent proves first-wins: a rejected
// re-declaration must not land even partially.
func TestRegisterQueueConflictKeepsIncumbent(t *testing.T) {
	t.Run("flag_conflict", func(t *testing.T) {
		d := NewDeclarations()
		d.RegisterQueue(&QueueDeclaration{
			Name:    mergeQueue,
			Durable: true,
			Args:    map[string]any{dlxArgKey: "orders.dlx"},
		})
		d.RegisterQueue(&QueueDeclaration{
			Name:    mergeQueue,
			Durable: false,
			Args:    map[string]any{dlxArgKey: "other.dlx", mapKeyTTL: ttlValue7200},
		})

		stored := d.Queues[mergeQueue]
		assert.True(t, stored.Durable, "incumbent flag must survive")
		assert.Equal(t, "orders.dlx", stored.Args[dlxArgKey])
		assert.NotContains(t, stored.Args, mapKeyTTL, "no partial merge from a rejected declaration")
	})

	// The flags agree here, so the rejection comes from the Args scan itself.
	// That is the only shape in which a scan that merged as it went could leak
	// the rejected declaration's other keys: argKeyA sorts before the contested
	// argKeyB, so it is visited while the scan still believes the merge is on.
	t.Run("args_conflict_leaks_no_sibling_key", func(t *testing.T) {
		d := NewDeclarations()
		d.RegisterQueue(&QueueDeclaration{
			Name: mergeQueue,
			Args: map[string]any{argKeyB: "b-incumbent"},
		})
		d.RegisterQueue(&QueueDeclaration{
			Name: mergeQueue,
			Args: map[string]any{argKeyA: "a-rejected", argKeyB: "b-rejected"},
		})

		require.Error(t, d.Validate())
		stored := d.Queues[mergeQueue]
		assert.Equal(t, "b-incumbent", stored.Args[argKeyB], "the contested key keeps the incumbent value")
		assert.NotContains(t, stored.Args, argKeyA, "a key new to the incumbent must not land from a rejected declaration")
	})
}

func TestValidateAggregatesMultipleQueueConflicts(t *testing.T) {
	d := NewDeclarations()
	d.RegisterQueue(&QueueDeclaration{Name: mergeQueue, Durable: true})
	d.RegisterQueue(&QueueDeclaration{Name: mergeQueue, Durable: false})
	d.RegisterQueue(&QueueDeclaration{Name: mergeQueueB, Durable: true, Args: map[string]any{dlxArgKey: "payments.dlx"}})
	d.RegisterQueue(&QueueDeclaration{Name: mergeQueueB, Durable: true, Args: map[string]any{dlxArgKey: ""}})

	err := d.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflicting queue declarations (2 conflict(s))")
	assert.Contains(t, err.Error(), mergeQueue)
	assert.Contains(t, err.Error(), mergeQueueB)
}

// TestRegisterQueueMergeHashIsOrderIndependent guards Manager.EnsureConsumers,
// which compares declHash across its singleflight boundary: an order-dependent
// merge would make two orderings of one declaration set replay as different
// topologies.
func TestRegisterQueueMergeHashIsOrderIndependent(t *testing.T) {
	dlqFirst := NewDeclarations()
	dlqFirst.DeclareQueueWithDLQ(mergeQueue, nil)
	dlqFirst.DeclareQueue(mergeQueue)

	plainFirst := NewDeclarations()
	plainFirst.DeclareQueue(mergeQueue)
	plainFirst.DeclareQueueWithDLQ(mergeQueue, nil)

	assert.Equal(t, dlqFirst.Hash(), plainFirst.Hash())
}

func TestCloneCopiesQueueConflicts(t *testing.T) {
	const cloneOnlyQueue = "clone.only.queue"
	const sourceOnlyQueue = "source.only.queue"

	conflict := func(d *Declarations, name string) {
		d.RegisterQueue(&QueueDeclaration{Name: name, Durable: true})
		d.RegisterQueue(&QueueDeclaration{Name: name, Durable: false})
	}

	d := NewDeclarations()
	for _, name := range []string{mergeQueue, mergeQueueB, mergeQueueC} {
		conflict(d, name)
	}
	require.Error(t, d.Validate())
	// Spare capacity is what makes the rest of this test discriminating: without
	// it every later append reallocates, and a clone that merely aliased the
	// source's slice would behave identically to a real copy.
	require.Greater(t, cap(d.queueConflicts), len(d.queueConflicts))

	clone := d.Clone()
	require.Error(t, clone.Validate(), "a clone must not pass a validation its source failed")

	// Both sides now record one more conflict. A shared backing array would let
	// the source's append overwrite the clone's at the same index.
	conflict(clone, cloneOnlyQueue)
	conflict(d, sourceOnlyQueue)

	assert.Contains(t, clone.Validate().Error(), cloneOnlyQueue)
	assert.NotContains(t, clone.Validate().Error(), sourceOnlyQueue)
	assert.Contains(t, d.Validate().Error(), sourceOnlyQueue)
	assert.NotContains(t, d.Validate().Error(), cloneOnlyQueue)
}

// TestRegisterQueueUncomparableArgsValues pins reflect.DeepEqual over ==:
// broker args routinely hold slices and tables, and == on those dynamic types
// panics at runtime, turning a declaration-time diagnostic into a boot panic.
func TestRegisterQueueUncomparableArgsValues(t *testing.T) {
	t.Run("equal_uncomparable_values_merge", func(t *testing.T) {
		d := NewDeclarations()
		require.NotPanics(t, func() {
			d.RegisterQueue(&QueueDeclaration{Name: testQueue, Args: map[string]any{
				testKey: []string{"a"},
				testArg: amqp.Table{"nested": "v"},
			}})
			d.RegisterQueue(&QueueDeclaration{Name: testQueue, Args: map[string]any{
				testKey: []string{"a"},
				testArg: amqp.Table{"nested": "v"},
			}})
		})

		assert.NoError(t, d.Validate())
		assert.Equal(t, []string{"a"}, d.Queues[testQueue].Args[testKey])
	})

	t.Run("differing_uncomparable_values_conflict", func(t *testing.T) {
		d := NewDeclarations()
		require.NotPanics(t, func() {
			d.RegisterQueue(&QueueDeclaration{Name: testQueue, Args: map[string]any{testKey: []string{"a"}}})
			d.RegisterQueue(&QueueDeclaration{Name: testQueue, Args: map[string]any{testKey: []string{"b"}}})
		})

		err := d.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), `Args["`+testKey+`"]`)
		assert.Contains(t, err.Error(), "[a]")
		assert.Contains(t, err.Error(), "[b]")
	})
}

func streamConsumer(queue string, autoAck bool, args map[string]any) *ConsumerDeclaration {
	return &ConsumerDeclaration{
		Queue:     queue,
		Consumer:  testConsumer,
		EventType: testEventType,
		AutoAck:   autoAck,
		Args:      args,
	}
}

func TestValidateStreamQueueShape(t *testing.T) {
	streamArgs := func() map[string]any { return map[string]any{argQueueType: queueTypeStream} }

	tests := []struct {
		name    string
		queue   *QueueDeclaration
		wantErr string
	}{
		{
			name:  "durable_non_exclusive_non_autodelete_passes",
			queue: &QueueDeclaration{Name: testStreamQueue, Durable: true, Args: streamArgs()},
		},
		{
			name:    "non_durable_rejected",
			queue:   &QueueDeclaration{Name: testStreamQueue, Args: streamArgs()},
			wantErr: `stream queue "events.stream" must be durable`,
		},
		{
			name:    "exclusive_rejected",
			queue:   &QueueDeclaration{Name: testStreamQueue, Durable: true, Exclusive: true, Args: streamArgs()},
			wantErr: `stream queue "events.stream" must not be exclusive`,
		},
		{
			name:    "auto_delete_rejected",
			queue:   &QueueDeclaration{Name: testStreamQueue, Durable: true, AutoDelete: true, Args: streamArgs()},
			wantErr: `stream queue "events.stream" must not be auto-delete`,
		},
		{
			// Stream-ness gates the whole rule set: a classic queue keeps its
			// pre-existing freedom to be non-durable, exclusive and auto-delete.
			name:  "non_stream_queue_unaffected",
			queue: &QueueDeclaration{Name: testStreamQueue, Exclusive: true, AutoDelete: true},
		},
		{
			name:  "other_queue_type_unaffected",
			queue: &QueueDeclaration{Name: testStreamQueue, Exclusive: true, Args: map[string]any{argQueueType: "quorum"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decls := NewDeclarations()
			decls.RegisterQueue(tt.queue)

			err := decls.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidateStreamQueueShapeAggregatesEveryViolation(t *testing.T) {
	decls := NewDeclarations()
	decls.RegisterQueue(&QueueDeclaration{
		Name:       testStreamQueue,
		Exclusive:  true,
		AutoDelete: true,
		Args:       map[string]any{argQueueType: queueTypeStream},
	})

	err := decls.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `stream queue "events.stream" must be durable`)
	assert.Contains(t, err.Error(), `stream queue "events.stream" must not be exclusive`)
	assert.Contains(t, err.Error(), `stream queue "events.stream" must not be auto-delete`)
}

func TestValidateStreamConsumerRules(t *testing.T) {
	timestamp := time.Now()

	tests := []struct {
		name        string
		nonStream   bool
		autoAck     bool
		args        map[string]any
		wantErr     string
		wantNoError bool
	}{
		{name: "no_args_accepted", wantNoError: true},
		{name: "offset_first_accepted", args: map[string]any{argStreamOffset: streamOffsetFirst}, wantNoError: true},
		{name: "offset_last_accepted", args: map[string]any{argStreamOffset: streamOffsetLast}, wantNoError: true},
		{name: "offset_next_accepted", args: map[string]any{argStreamOffset: streamOffsetNext}, wantNoError: true},
		{name: "offset_int_zero_accepted", args: map[string]any{argStreamOffset: 0}, wantNoError: true},
		{name: "offset_int64_accepted", args: map[string]any{argStreamOffset: int64(42)}, wantNoError: true},
		{name: "offset_int64_zero_accepted", args: map[string]any{argStreamOffset: int64(0)}, wantNoError: true},
		{name: "offset_timestamp_accepted", args: map[string]any{argStreamOffset: timestamp}, wantNoError: true},
		{name: "offset_interval_days_accepted", args: map[string]any{argStreamOffset: "7D"}, wantNoError: true},
		{name: "offset_interval_minutes_accepted", args: map[string]any{argStreamOffset: "30m"}, wantNoError: true},
		{name: "other_consumer_arg_accepted", args: map[string]any{"x-priority": 5}, wantNoError: true},
		{
			name:    "auto_ack_rejected",
			autoAck: true,
			wantErr: `consumer "test-consumer" on stream queue "events.stream" must use manual ack (AutoAck=false)`,
		},
		{
			name:    "offset_garbage_string_rejected",
			args:    map[string]any{argStreamOffset: "banana"},
			wantErr: `has invalid x-stream-offset value banana (string)`,
		},
		{
			name:    "offset_bad_interval_unit_rejected",
			args:    map[string]any{argStreamOffset: "7X"},
			wantErr: `has invalid x-stream-offset value 7X (string)`,
		},
		{
			name:    "offset_negative_int_rejected",
			args:    map[string]any{argStreamOffset: -1},
			wantErr: `has invalid x-stream-offset value -1 (int)`,
		},
		{
			name:    "offset_negative_int64_rejected",
			args:    map[string]any{argStreamOffset: int64(-1)},
			wantErr: `has invalid x-stream-offset value -1 (int64)`,
		},
		{
			name:    "offset_float_rejected",
			args:    map[string]any{argStreamOffset: 3.14},
			wantErr: `has invalid x-stream-offset value 3.14 (float64)`,
		},
		{
			name:      "offset_on_non_stream_queue_rejected",
			nonStream: true,
			args:      map[string]any{argStreamOffset: streamOffsetFirst},
			wantErr:   `consumer "test-consumer" on queue "events.stream" sets x-stream-offset but the queue is not a stream queue`,
		},
		{
			name:        "auto_ack_on_non_stream_queue_allowed",
			nonStream:   true,
			autoAck:     true,
			wantNoError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decls := NewDeclarations()
			if tt.nonStream {
				decls.DeclareQueue(testStreamQueue)
			} else {
				decls.DeclareStreamQueue(testStreamQueue, nil)
			}
			decls.RegisterConsumer(streamConsumer(testStreamQueue, tt.autoAck, tt.args))

			err := decls.Validate()
			if tt.wantNoError {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestConsumerArgsAffectHash(t *testing.T) {
	build := func(args map[string]any) *Declarations {
		d := NewDeclarations()
		d.DeclareStreamQueue(testStreamQueue, nil)
		d.RegisterConsumer(streamConsumer(testStreamQueue, false, args))
		return d
	}

	first := build(map[string]any{argStreamOffset: streamOffsetFirst})
	last := build(map[string]any{argStreamOffset: streamOffsetLast})

	assert.NotEqual(t, first.Hash(), last.Hash(),
		"consumer Args are broker-visible, so they must invalidate the replay hash")
	assert.Equal(t, first.Hash(), build(map[string]any{argStreamOffset: streamOffsetFirst}).Hash())
	assert.NotEqual(t, first.Hash(), build(nil).Hash())
	assert.Equal(t, first.Hash(), first.Clone().Hash(),
		"a clone must hash identically to its source")
}

func TestRegisterConsumerDeepCopiesArgs(t *testing.T) {
	callerArgs := map[string]any{argStreamOffset: streamOffsetFirst}

	decls := NewDeclarations()
	decls.DeclareStreamQueue(testStreamQueue, nil)
	decls.RegisterConsumer(streamConsumer(testStreamQueue, false, callerArgs))

	clone := decls.Clone()

	// Mutating the caller's map after registration must not reach either store.
	callerArgs[argStreamOffset] = streamOffsetLast
	callerArgs["x-priority"] = 5

	stored := decls.Consumers()[0]
	assert.Equal(t, map[string]any{argStreamOffset: streamOffsetFirst}, stored.Args)

	cloned := clone.Consumers()[0]
	assert.Equal(t, map[string]any{argStreamOffset: streamOffsetFirst}, cloned.Args)

	// The clone owns its own map too.
	stored.Args[argStreamOffset] = streamOffsetNext
	assert.Equal(t, streamOffsetFirst, cloned.Args[argStreamOffset])
}

func TestRegisterConsumerWithoutArgsGetsEmptyMap(t *testing.T) {
	decls := NewDeclarations()
	decls.DeclareQueue(testQueue)
	decls.RegisterConsumer(streamConsumer(testQueue, false, nil))

	stored := decls.Consumers()[0]
	require.NotNil(t, stored.Args)
	assert.Empty(t, stored.Args)

	cloned := decls.Clone().Consumers()[0]
	require.NotNil(t, cloned.Args)
	assert.Empty(t, cloned.Args)
}
