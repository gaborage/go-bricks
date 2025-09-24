package mocks

import (
	"context"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/mock"
)

// MockMessagingClient provides a testify-based mock implementation of the messaging.Client interface.
// It includes message simulation capabilities for testing message flows.
//
// Example usage:
//
//	mockClient := &mocks.MockMessagingClient{}
//	mockClient.On("Publish", mock.Anything, "user.created", mock.Anything).Return(nil)
//	mockClient.On("IsReady").Return(true)
//
//	// Simulate incoming messages
//	mockClient.SimulateMessage("test.queue", []byte(`{"event": "test"}`))
type MockMessagingClient struct {
	mock.Mock

	// Message simulation
	messageChannels map[string]chan amqp.Delivery
	mu              sync.RWMutex
	isReady         bool
	closed          bool
}

// NewMockMessagingClient creates a new mock messaging client with message simulation capabilities
func NewMockMessagingClient() *MockMessagingClient {
	return &MockMessagingClient{
		messageChannels: make(map[string]chan amqp.Delivery),
		isReady:         true,
		closed:          false,
	}
}

// Publish implements messaging.Client
func (m *MockMessagingClient) Publish(ctx context.Context, destination string, data []byte) error {
	arguments := m.MethodCalled("Publish", ctx, destination, data)
	return arguments.Error(0)
}

// Consume implements messaging.Client
func (m *MockMessagingClient) Consume(ctx context.Context, destination string) (<-chan amqp.Delivery, error) {
	arguments := m.MethodCalled("Consume", ctx, destination)

	// Create a channel for this destination if it doesn't exist
	m.mu.Lock()
	if _, exists := m.messageChannels[destination]; !exists {
		m.messageChannels[destination] = make(chan amqp.Delivery, 100)
	}
	ch := m.messageChannels[destination]
	m.mu.Unlock()

	if err := arguments.Error(1); err != nil {
		return nil, err
	}
	return ch, nil
}

// Close implements messaging.Client
func (m *MockMessagingClient) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.closed = true
	// Close all message channels
	for _, ch := range m.messageChannels {
		close(ch)
	}
	m.messageChannels = make(map[string]chan amqp.Delivery)

	arguments := m.MethodCalled("Close")
	return arguments.Error(0)
}

// IsReady implements messaging.Client
func (m *MockMessagingClient) IsReady() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Always call MethodCalled to honor expectations, even when closed
	arguments := m.MethodCalled("IsReady")

	// If closed, return false regardless of expectations
	if m.closed {
		return false
	}

	// Use expectation result if provided
	if len(arguments) > 0 {
		return arguments.Bool(0)
	}

	// Default to internal ready state
	return m.isReady
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
		Headers: headers,
	}

	// Fast path: destination exists - send under RLock to avoid racing with Close()
	m.mu.RLock()

	ch, exists := m.messageChannels[destination]
	if exists && !m.closed {
		// Send the message in a non-blocking way
		select {
		case ch <- delivery:
			// Message sent successfully
		default:
			// Channel is full, ignore
		}
		m.mu.RUnlock()
		return
	}
	m.mu.RUnlock()

	// Slow path: create channel and send under write lock.
	m.mu.Lock()
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

// ExpectPublish sets up a publish expectation
func (m *MockMessagingClient) ExpectPublish(destination string, data []byte, err error) *mock.Call {
	return m.On("Publish", mock.Anything, destination, data).Return(err)
}

// ExpectPublishAny sets up a publish expectation for any destination and data
func (m *MockMessagingClient) ExpectPublishAny(err error) *mock.Call {
	return m.On("Publish", mock.Anything, mock.Anything, mock.Anything).Return(err)
}

// ExpectConsume sets up a consume expectation
func (m *MockMessagingClient) ExpectConsume(destination string, err error) *mock.Call {
	return m.On("Consume", mock.Anything, destination).Return(nil, err)
}

// ExpectIsReady sets up an IsReady expectation
func (m *MockMessagingClient) ExpectIsReady(ready bool) *mock.Call {
	return m.On("IsReady").Return(ready)
}

// ExpectClose sets up a close expectation
func (m *MockMessagingClient) ExpectClose(err error) *mock.Call {
	return m.On("Close").Return(err)
}
