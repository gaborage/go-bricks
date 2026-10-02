package mocks

import (
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/mock"
)

// MockMessagingClient provides a testify-based mock implementation of the messaging.Client interface.
// Its SimulateMessage* helpers feed the channels returned by (*MockAMQPClient).ConsumeFromQueue,
// which embeds this type.
//
// Example usage:
//
//	mockClient := mocks.NewMockMessagingClient()
//	mockClient.On("Publish", mock.Anything, "user.created", mock.Anything).Return(nil)
//	mockClient.On("IsReady").Return(true)
//
//	// Simulate incoming messages
//	mockClient.SimulateMessage("test.queue", []byte(`{"event": "test"}`))
type MockMessagingClient struct {
	mock.Mock

	// Message simulation
	// read back via (*MockAMQPClient).ConsumeFromQueue
	messageChannels map[string]chan amqp.Delivery
	mu              sync.RWMutex
	isReady         bool
	closed          bool
}

// NewMockMessagingClient creates a new mock messaging client
func NewMockMessagingClient() *MockMessagingClient {
	return &MockMessagingClient{
		messageChannels: make(map[string]chan amqp.Delivery),
		isReady:         true,
		closed:          false,
	}
}

// Close implements messaging.Client
func (m *MockMessagingClient) Close() error {
	m.mu.Lock()
	m.closed = true
	for _, ch := range m.messageChannels {
		close(ch)
	}
	m.messageChannels = make(map[string]chan amqp.Delivery)
	m.mu.Unlock()

	arguments := m.MethodCalled("Close")
	return arguments.Error(0)
}

// IsReady implements messaging.Client
func (m *MockMessagingClient) IsReady() bool {
	m.mu.RLock()
	closed := m.closed
	m.mu.RUnlock()

	if closed {
		return false
	}

	for _, ec := range m.ExpectedCalls {
		if ec.Method == "IsReady" {
			return m.MethodCalled("IsReady").Bool(0)
		}
	}

	m.mu.RLock()
	ready := m.isReady
	m.mu.RUnlock()

	return ready
}

// Helper methods for testing scenarios

// SimulateMessage sends a simulated message to the specified destination
func (m *MockMessagingClient) SimulateMessage(destination string, body []byte) {
	m.SimulateMessageWithHeaders(destination, body, nil)
}

// SimulateMessageWithHeaders sends a simulated message with headers to the specified destination
func (m *MockMessagingClient) SimulateMessageWithHeaders(destination string, body []byte, headers map[string]any) {
	delivery := amqp.Delivery{
		Body:    body,
		Headers: amqp.Table(headers),
	}

	// Fast path: destination exists - send under RLock to avoid racing with Close()
	m.mu.RLock()

	ch, exists := m.messageChannels[destination]
	if exists && !m.closed {
		select {
		case ch <- delivery:
		default:
			// Channel is full, ignore
		}
		m.mu.RUnlock()
		return
	}
	m.mu.RUnlock()

	// Slow path: create channel and send under write lock.
	m.mu.Lock()
	if m.messageChannels == nil {
		m.messageChannels = make(map[string]chan amqp.Delivery)
	}
	ch, exists = m.messageChannels[destination]
	if !exists {
		ch = make(chan amqp.Delivery, 100)
		m.messageChannels[destination] = ch
	}
	if !m.closed {
		select {
		case ch <- delivery:
		default:
			// Channel is full, ignore
		}
	}
	m.mu.Unlock()
}

// SetReady sets the ready state of the mock client
func (m *MockMessagingClient) SetReady(ready bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.isReady = ready
}

// ExpectIsReady sets up an IsReady expectation
func (m *MockMessagingClient) ExpectIsReady(ready bool) *mock.Call {
	return m.On("IsReady").Return(ready)
}

// ExpectClose sets up a close expectation
func (m *MockMessagingClient) ExpectClose(err error) *mock.Call {
	return m.On("Close").Return(err)
}
