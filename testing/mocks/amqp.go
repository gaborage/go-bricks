package mocks

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/mock"

	"github.com/gaborage/go-bricks/messaging"
)

// MockAMQPClient provides a testify-based mock implementation of the messaging.AMQPClient interface.
// It extends MockMessagingClient with AMQP-specific operations for advanced testing scenarios.
//
// Example usage:
//
//	mockAMQP := &mocks.MockAMQPClient{}
//	mockAMQP.On("DeclareQueue", "test.queue", true, false, false, false).Return(nil)
//	mockAMQP.On("PublishToExchange", mock.Anything, mock.MatchedBy(func(opts messaging.PublishOptions) bool {
//		return opts.Exchange == "test.exchange"
//	}), mock.Anything).Return(nil)
type MockAMQPClient struct {
	*MockMessagingClient

	// AMQP infrastructure tracking for testing
	declaredQueues    map[string]bool
	declaredExchanges map[string]bool
	bindings          map[string]bool
}

// NewMockAMQPClient creates a new mock AMQP client
func NewMockAMQPClient() *MockAMQPClient {
	return &MockAMQPClient{
		MockMessagingClient: NewMockMessagingClient(),
		declaredQueues:      make(map[string]bool),
		declaredExchanges:   make(map[string]bool),
		bindings:            make(map[string]bool),
	}
}

var _ messaging.AMQPClient = (*MockAMQPClient)(nil)

// PublishToExchange implements messaging.AMQPClient
func (m *MockAMQPClient) PublishToExchange(ctx context.Context, options messaging.PublishOptions, data []byte) error {
	arguments := m.Called(ctx, options, data)
	return arguments.Error(0)
}

// ConsumeFromQueue implements messaging.AMQPClient
func (m *MockAMQPClient) ConsumeFromQueue(ctx context.Context, options messaging.ConsumeOptions) (<-chan amqp.Delivery, error) {
	arguments := m.Called(ctx, options)

	// Create a channel for this queue if it doesn't exist
	m.mu.Lock()
	if _, exists := m.messageChannels[options.Queue]; !exists {
		m.messageChannels[options.Queue] = make(chan amqp.Delivery, 100)
	}
	ch := m.messageChannels[options.Queue]
	m.mu.Unlock()

	if err := arguments.Error(1); err != nil {
		return nil, err
	}
	return ch, nil
}

// DeclareQueue implements messaging.AMQPClient
func (m *MockAMQPClient) DeclareQueue(name string, durable, autoDelete, exclusive, noWait bool) error {
	arguments := m.Called(name, durable, autoDelete, exclusive, noWait)
	err := arguments.Error(0)

	// Only update state if the operation succeeded
	if err == nil {
		m.mu.Lock()
		m.declaredQueues[name] = true
		m.mu.Unlock()
	}

	return err
}

// DeclareExchange implements messaging.AMQPClient
func (m *MockAMQPClient) DeclareExchange(name, kind string, durable, autoDelete, internal, noWait bool) error {
	arguments := m.Called(name, kind, durable, autoDelete, internal, noWait)
	err := arguments.Error(0)

	// Only update state if the operation succeeded
	if err == nil {
		m.mu.Lock()
		m.declaredExchanges[name] = true
		m.mu.Unlock()
	}

	return err
}

// BindQueue implements messaging.AMQPClient
func (m *MockAMQPClient) BindQueue(queue, exchange, routingKey string, noWait bool) error {
	arguments := m.Called(queue, exchange, routingKey, noWait)
	err := arguments.Error(0)

	// Only update state if the operation succeeded
	if err == nil {
		bindingKey := queue + ":" + exchange + ":" + routingKey
		m.mu.Lock()
		m.bindings[bindingKey] = true
		m.mu.Unlock()
	}

	return err
}

// Helper methods for testing scenarios

// IsQueueDeclared returns true if the queue was declared
func (m *MockAMQPClient) IsQueueDeclared(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.declaredQueues[name]
}

// IsExchangeDeclared returns true if the exchange was declared
func (m *MockAMQPClient) IsExchangeDeclared(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.declaredExchanges[name]
}

// IsQueueBound returns true if the queue is bound to the exchange with the routing key
func (m *MockAMQPClient) IsQueueBound(queue, exchange, routingKey string) bool {
	bindingKey := queue + ":" + exchange + ":" + routingKey
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.bindings[bindingKey]
}

// ClearInfrastructure clears all declared infrastructure (for test cleanup)
func (m *MockAMQPClient) ClearInfrastructure() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.declaredQueues = make(map[string]bool)
	m.declaredExchanges = make(map[string]bool)
	m.bindings = make(map[string]bool)
}

// ExpectPublishToExchange sets up a publish to exchange expectation
func (m *MockAMQPClient) ExpectPublishToExchange(options messaging.PublishOptions, data []byte, err error) *mock.Call {
	return m.On("PublishToExchange", mock.Anything, options, data).Return(err)
}

// ExpectPublishToExchangeAny sets up a publish to exchange expectation for any options and data
func (m *MockAMQPClient) ExpectPublishToExchangeAny(err error) *mock.Call {
	return m.On("PublishToExchange", mock.Anything, mock.Anything, mock.Anything).Return(err)
}

// ExpectConsumeFromQueue sets up a consume from queue expectation
func (m *MockAMQPClient) ExpectConsumeFromQueue(options messaging.ConsumeOptions, err error) *mock.Call {
	return m.On("ConsumeFromQueue", mock.Anything, options).Return(nil, err)
}

// ExpectDeclareQueue sets up a declare queue expectation
func (m *MockAMQPClient) ExpectDeclareQueue(name string, durable, autoDelete, exclusive, noWait bool, err error) *mock.Call {
	return m.On("DeclareQueue", name, durable, autoDelete, exclusive, noWait).Return(err)
}

// ExpectDeclareExchange sets up a declare exchange expectation
func (m *MockAMQPClient) ExpectDeclareExchange(name, kind string, durable, autoDelete, internal, noWait bool, err error) *mock.Call {
	return m.On("DeclareExchange", name, kind, durable, autoDelete, internal, noWait).Return(err)
}

// ExpectBindQueue sets up a bind queue expectation
func (m *MockAMQPClient) ExpectBindQueue(queue, exchange, routingKey string, noWait bool, err error) *mock.Call {
	return m.On("BindQueue", queue, exchange, routingKey, noWait).Return(err)
}

// ExpectDeclareExchangeAny sets up a declare exchange expectation for any parameters
func (m *MockAMQPClient) ExpectDeclareExchangeAny(err error) *mock.Call {
	return m.On("DeclareExchange", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(err)
}

// ExpectDeclareQueueAny sets up a declare queue expectation for any parameters
func (m *MockAMQPClient) ExpectDeclareQueueAny(err error) *mock.Call {
	return m.On("DeclareQueue", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(err)
}

// ExpectBindQueueAny sets up a bind queue expectation for any parameters
func (m *MockAMQPClient) ExpectBindQueueAny(err error) *mock.Call {
	return m.On("BindQueue", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(err)
}
