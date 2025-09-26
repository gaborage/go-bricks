package mocks

import (
	"context"
	"maps"

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
	if m.hasExpectation("RegisterExchange") {
		m.Called(declaration)
	}
}

// RegisterQueue implements messaging.RegistryInterface
func (m *MockRegistry) RegisterQueue(declaration *messaging.QueueDeclaration) {
	m.queues[declaration.Name] = declaration
	if m.hasExpectation("RegisterQueue") {
		m.Called(declaration)
	}
}

// RegisterBinding implements messaging.RegistryInterface
func (m *MockRegistry) RegisterBinding(declaration *messaging.BindingDeclaration) {
	m.bindings = append(m.bindings, declaration)
	if m.hasExpectation("RegisterBinding") {
		m.Called(declaration)
	}
}

// RegisterPublisher implements messaging.RegistryInterface
func (m *MockRegistry) RegisterPublisher(declaration *messaging.PublisherDeclaration) {
	m.publishers = append(m.publishers, declaration)
	if m.hasExpectation("RegisterPublisher") {
		m.Called(declaration)
	}
}

// RegisterConsumer implements messaging.RegistryInterface
func (m *MockRegistry) RegisterConsumer(declaration *messaging.ConsumerDeclaration) {
	m.consumers = append(m.consumers, declaration)
	if m.hasExpectation("RegisterConsumer") {
		m.Called(declaration)
	}
}

// DeclareInfrastructure implements messaging.RegistryInterface
func (m *MockRegistry) DeclareInfrastructure(ctx context.Context) error {
	if m.hasExpectation("DeclareInfrastructure") {
		arguments := m.Called(ctx)
		return arguments.Error(0)
	}
	return nil
}

// StartConsumers implements messaging.RegistryInterface
func (m *MockRegistry) StartConsumers(ctx context.Context) error {
	if m.hasExpectation("StartConsumers") {
		arguments := m.Called(ctx)
		return arguments.Error(0)
	}
	return nil
}

// StopConsumers implements messaging.RegistryInterface
func (m *MockRegistry) StopConsumers() {
	if m.hasExpectation("StopConsumers") {
		m.Called()
	}
}

// GetExchanges implements messaging.RegistryInterface
func (m *MockRegistry) GetExchanges() map[string]*messaging.ExchangeDeclaration {
	// Check if expectation exists for this method
	if m.hasExpectation("GetExchanges") {
		arguments := m.Called()
		return arguments.Get(0).(map[string]*messaging.ExchangeDeclaration)
	}
	// Return copy of internal storage
	result := make(map[string]*messaging.ExchangeDeclaration, len(m.exchanges))
	maps.Copy(result, m.exchanges)
	return result
}

// GetQueues implements messaging.RegistryInterface
func (m *MockRegistry) GetQueues() map[string]*messaging.QueueDeclaration {
	// Check if expectation exists for this method
	if m.hasExpectation("GetQueues") {
		arguments := m.Called()
		return arguments.Get(0).(map[string]*messaging.QueueDeclaration)
	}
	// Return copy of internal storage
	result := make(map[string]*messaging.QueueDeclaration, len(m.queues))
	maps.Copy(result, m.queues)
	return result
}

// GetBindings implements messaging.RegistryInterface
func (m *MockRegistry) GetBindings() []*messaging.BindingDeclaration {
	// Check if expectation exists for this method
	if m.hasExpectation("GetBindings") {
		arguments := m.Called()
		return arguments.Get(0).([]*messaging.BindingDeclaration)
	}
	// Return copy of internal storage
	result := make([]*messaging.BindingDeclaration, len(m.bindings))
	copy(result, m.bindings)
	return result
}

// GetPublishers implements messaging.RegistryInterface
func (m *MockRegistry) GetPublishers() []*messaging.PublisherDeclaration {
	// Check if expectation exists for this method
	if m.hasExpectation("GetPublishers") {
		arguments := m.Called()
		return arguments.Get(0).([]*messaging.PublisherDeclaration)
	}
	// Return copy of internal storage
	result := make([]*messaging.PublisherDeclaration, len(m.publishers))
	copy(result, m.publishers)
	return result
}

// GetConsumers implements messaging.RegistryInterface
func (m *MockRegistry) GetConsumers() []*messaging.ConsumerDeclaration {
	// Check if expectation exists for this method
	if m.hasExpectation("GetConsumers") {
		arguments := m.Called()
		return arguments.Get(0).([]*messaging.ConsumerDeclaration)
	}
	// Return copy of internal storage
	result := make([]*messaging.ConsumerDeclaration, len(m.consumers))
	copy(result, m.consumers)
	return result
}

// ValidatePublisher implements messaging.RegistryInterface
func (m *MockRegistry) ValidatePublisher(exchange, routingKey string) bool {
	if m.hasExpectation("ValidatePublisher") {
		arguments := m.Called(exchange, routingKey)
		return arguments.Bool(0)
	}

	for _, p := range m.publishers {
		if p.Exchange == exchange && p.RoutingKey == routingKey {
			return true
		}
	}
	return false
}

// ValidateConsumer implements messaging.RegistryInterface
func (m *MockRegistry) ValidateConsumer(queue string) bool {
	if m.hasExpectation("ValidateConsumer") {
		arguments := m.Called(queue)
		return arguments.Bool(0)
	}

	for _, c := range m.consumers {
		if c.Queue == queue {
			return true
		}
	}
	return false
}

// hasExpectation checks if there's an expectation set for the given method name
func (m *MockRegistry) hasExpectation(methodName string) bool {
	for _, call := range m.ExpectedCalls {
		if call.Method == methodName {
			return true
		}
	}
	return false
}

// Helper methods for testing scenarios

// ExpectRegistrations sets up expectations for infrastructure registration
func (m *MockRegistry) ExpectRegistrations() {
	m.On("RegisterExchange", mock.Anything).Return().Maybe()
	m.On("RegisterQueue", mock.Anything).Return().Maybe()
	m.On("RegisterBinding", mock.Anything).Return().Maybe()
	m.On("RegisterPublisher", mock.Anything).Return().Maybe()
	m.On("RegisterConsumer", mock.Anything).Return().Maybe()
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
