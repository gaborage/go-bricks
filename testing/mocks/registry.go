package mocks

import (
	"context"

	"github.com/stretchr/testify/mock"

	"github.com/gaborage/go-bricks/messaging"
)

// MockRegistry provides a testify-based mock implementation of the messaging.RegistryInterface.
// It allows for testing messaging infrastructure management and consumer lifecycle.
//
// Example usage:
//
//	mockRegistry := &mocks.MockRegistry{}
//	mockRegistry.On("RegisterExchange", mock.MatchedBy(func(decl *messaging.ExchangeDeclaration) bool {
//		return decl.Name == "test.exchange"
//	})).Return()
//	mockRegistry.On("DeclareInfrastructure", mock.Anything).Return(nil)
type MockRegistry struct {
	mock.Mock

	// In-memory storage for tracking registrations
	exchanges  map[string]*messaging.ExchangeDeclaration
	queues     map[string]*messaging.QueueDeclaration
	bindings   []*messaging.BindingDeclaration
	publishers []*messaging.PublisherDeclaration
	consumers  []*messaging.ConsumerDeclaration
}

// NewMockRegistry creates a new mock registry
func NewMockRegistry() *MockRegistry {
	return &MockRegistry{
		exchanges:  make(map[string]*messaging.ExchangeDeclaration),
		queues:     make(map[string]*messaging.QueueDeclaration),
		bindings:   make([]*messaging.BindingDeclaration, 0),
		publishers: make([]*messaging.PublisherDeclaration, 0),
		consumers:  make([]*messaging.ConsumerDeclaration, 0),
	}
}

// RegisterExchange implements messaging.RegistryInterface
func (m *MockRegistry) RegisterExchange(declaration *messaging.ExchangeDeclaration) {
	m.exchanges[declaration.Name] = declaration
	m.Called(declaration)
}

// RegisterQueue implements messaging.RegistryInterface
func (m *MockRegistry) RegisterQueue(declaration *messaging.QueueDeclaration) {
	m.queues[declaration.Name] = declaration
	m.Called(declaration)
}

// RegisterBinding implements messaging.RegistryInterface
func (m *MockRegistry) RegisterBinding(declaration *messaging.BindingDeclaration) {
	m.bindings = append(m.bindings, declaration)
	m.Called(declaration)
}

// RegisterPublisher implements messaging.RegistryInterface
func (m *MockRegistry) RegisterPublisher(declaration *messaging.PublisherDeclaration) {
	m.publishers = append(m.publishers, declaration)
	m.Called(declaration)
}

// RegisterConsumer implements messaging.RegistryInterface
func (m *MockRegistry) RegisterConsumer(declaration *messaging.ConsumerDeclaration) {
	m.consumers = append(m.consumers, declaration)
	m.Called(declaration)
}

// noop is a helper to avoid code duplication in DeclareInfrastructure and StartConsumers
func (m *MockRegistry) noop(ctx context.Context) error {
	arguments := m.Called(ctx)
	return arguments.Error(0)
}

// DeclareInfrastructure implements messaging.RegistryInterface
func (m *MockRegistry) DeclareInfrastructure(ctx context.Context) error {
	return m.noop(ctx)
}

// StartConsumers implements messaging.RegistryInterface
func (m *MockRegistry) StartConsumers(ctx context.Context) error {
	return m.noop(ctx)
}

// StopConsumers implements messaging.RegistryInterface
func (m *MockRegistry) StopConsumers() {
	m.Called()
}

// GetExchanges implements messaging.RegistryInterface
func (m *MockRegistry) GetExchanges() map[string]*messaging.ExchangeDeclaration {
	arguments := m.Called()
	if len(arguments) > 0 {
		return arguments.Get(0).(map[string]*messaging.ExchangeDeclaration)
	}
	// Return copy of internal storage
	result := make(map[string]*messaging.ExchangeDeclaration, len(m.exchanges))
	for name, decl := range m.exchanges {
		result[name] = decl
	}
	return result
}

// GetQueues implements messaging.RegistryInterface
func (m *MockRegistry) GetQueues() map[string]*messaging.QueueDeclaration {
	arguments := m.Called()
	if len(arguments) > 0 {
		return arguments.Get(0).(map[string]*messaging.QueueDeclaration)
	}
	// Return copy of internal storage
	result := make(map[string]*messaging.QueueDeclaration, len(m.queues))
	for name, decl := range m.queues {
		result[name] = decl
	}
	return result
}

// GetBindings implements messaging.RegistryInterface
func (m *MockRegistry) GetBindings() []*messaging.BindingDeclaration {
	arguments := m.Called()
	if len(arguments) > 0 {
		return arguments.Get(0).([]*messaging.BindingDeclaration)
	}
	// Return copy of internal storage
	result := make([]*messaging.BindingDeclaration, len(m.bindings))
	copy(result, m.bindings)
	return result
}

// GetPublishers implements messaging.RegistryInterface
func (m *MockRegistry) GetPublishers() []*messaging.PublisherDeclaration {
	arguments := m.Called()
	if len(arguments) > 0 {
		return arguments.Get(0).([]*messaging.PublisherDeclaration)
	}
	// Return copy of internal storage
	result := make([]*messaging.PublisherDeclaration, len(m.publishers))
	copy(result, m.publishers)
	return result
}

// GetConsumers implements messaging.RegistryInterface
func (m *MockRegistry) GetConsumers() []*messaging.ConsumerDeclaration {
	arguments := m.Called()
	if len(arguments) > 0 {
		return arguments.Get(0).([]*messaging.ConsumerDeclaration)
	}
	// Return copy of internal storage
	result := make([]*messaging.ConsumerDeclaration, len(m.consumers))
	copy(result, m.consumers)
	return result
}

// ValidatePublisher implements messaging.RegistryInterface
func (m *MockRegistry) ValidatePublisher(exchange, routingKey string) bool {
	arguments := m.Called(exchange, routingKey)
	return arguments.Bool(0)
}

// ValidateConsumer implements messaging.RegistryInterface
func (m *MockRegistry) ValidateConsumer(queue string) bool {
	arguments := m.Called(queue)
	return arguments.Bool(0)
}

// Helper methods for testing scenarios

// ExpectRegistrations sets up expectations for infrastructure registration
func (m *MockRegistry) ExpectRegistrations() {
	m.On("RegisterExchange", mock.Anything).Return()
	m.On("RegisterQueue", mock.Anything).Return()
	m.On("RegisterBinding", mock.Anything).Return()
	m.On("RegisterPublisher", mock.Anything).Return()
	m.On("RegisterConsumer", mock.Anything).Return()
}

// ExpectDeclareInfrastructure sets up a declare infrastructure expectation
func (m *MockRegistry) ExpectDeclareInfrastructure(err error) *mock.Call {
	return m.On("DeclareInfrastructure", mock.Anything).Return(err)
}

// ExpectStartConsumers sets up a start consumers expectation
func (m *MockRegistry) ExpectStartConsumers(err error) *mock.Call {
	return m.On("StartConsumers", mock.Anything).Return(err)
}

// ExpectStopConsumers sets up a stop consumers expectation
func (m *MockRegistry) ExpectStopConsumers() *mock.Call {
	return m.On("StopConsumers").Return()
}

// ExpectValidatePublisher sets up a validate publisher expectation
func (m *MockRegistry) ExpectValidatePublisher(exchange, routingKey string, valid bool) *mock.Call {
	return m.On("ValidatePublisher", exchange, routingKey).Return(valid)
}

// ExpectValidateConsumer sets up a validate consumer expectation
func (m *MockRegistry) ExpectValidateConsumer(queue string, valid bool) *mock.Call {
	return m.On("ValidateConsumer", queue).Return(valid)
}

// ClearRegistrations clears all internal registrations (for test cleanup)
func (m *MockRegistry) ClearRegistrations() {
	m.exchanges = make(map[string]*messaging.ExchangeDeclaration)
	m.queues = make(map[string]*messaging.QueueDeclaration)
	m.bindings = make([]*messaging.BindingDeclaration, 0)
	m.publishers = make([]*messaging.PublisherDeclaration, 0)
	m.consumers = make([]*messaging.ConsumerDeclaration, 0)
}
