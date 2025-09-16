package messaging

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ===== Registry Infrastructure Management Tests =====

// mockAMQPClient implements AMQPClient for testing registry functionality
type simpleMockAMQPClient struct {
	isReady            bool
	closed             bool
	declareQueueErr    error
	declareExchangeErr error
	bindQueueErr       error
	consumeErr         error
	deliveryChan       chan amqp.Delivery

	// Track calls for verification
	declaredQueues    []string
	declaredExchanges []string
	bindings          []string

	mu sync.RWMutex
}

// Compile-time interface conformance check
var _ AMQPClient = (*simpleMockAMQPClient)(nil)

func (m *simpleMockAMQPClient) IsReady() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isReady && !m.closed
}

func (m *simpleMockAMQPClient) Publish(_ context.Context, _ string, _ []byte) error {
	return nil
}

func (m *simpleMockAMQPClient) PublishToExchange(_ context.Context, _ PublishOptions, _ []byte) error {
	return nil
}

func (m *simpleMockAMQPClient) Consume(_ context.Context, _ string) (<-chan amqp.Delivery, error) {
	return m.deliveryChan, m.consumeErr
}

func (m *simpleMockAMQPClient) ConsumeFromQueue(_ context.Context, _ ConsumeOptions) (<-chan amqp.Delivery, error) {
	return m.deliveryChan, m.consumeErr
}

func (m *simpleMockAMQPClient) DeclareQueue(name string, _, _, _, _ bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.declareQueueErr != nil {
		return m.declareQueueErr
	}
	m.declaredQueues = append(m.declaredQueues, name)
	return nil
}

func (m *simpleMockAMQPClient) DeclareExchange(name, _ string, _, _, _, _ bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.declareExchangeErr != nil {
		return m.declareExchangeErr
	}
	m.declaredExchanges = append(m.declaredExchanges, name)
	return nil
}

func (m *simpleMockAMQPClient) BindQueue(queue, exchange, routingKey string, _ bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bindQueueErr != nil {
		return m.bindQueueErr
	}
	m.bindings = append(m.bindings, queue+":"+exchange+":"+routingKey)
	return nil
}

func (m *simpleMockAMQPClient) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *simpleMockAMQPClient) SetReady(ready bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.isReady = ready
}

func TestNewRegistry_Simple(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: true}
	logger := &stubLogger{}

	registry := NewRegistry(client, logger)

	require.NotNil(t, registry)
	assert.Equal(t, client, registry.client)
	assert.Equal(t, logger, registry.logger)
	assert.NotNil(t, registry.exchanges)
	assert.NotNil(t, registry.queues)
	assert.NotNil(t, registry.bindings)
	assert.NotNil(t, registry.publishers)
	assert.NotNil(t, registry.consumers)
	assert.False(t, registry.declared)
	assert.False(t, registry.consumersActive)
}

func TestRegistry_DeclareInfrastructure_Success_Simple(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: true}
	logger := &stubLogger{}
	registry := NewRegistry(client, logger)

	// Register infrastructure
	registry.RegisterExchange(&ExchangeDeclaration{
		Name: "test-exchange",
		Type: "topic",
	})
	registry.RegisterQueue(&QueueDeclaration{
		Name:    "test-queue",
		Durable: true,
	})
	registry.RegisterBinding(&BindingDeclaration{
		Queue:      "test-queue",
		Exchange:   "test-exchange",
		RoutingKey: "test.key",
	})

	ctx := context.Background()
	err := registry.DeclareInfrastructure(ctx)

	assert.NoError(t, err)
	assert.True(t, registry.declared)
	assert.Contains(t, client.declaredExchanges, "test-exchange")
	assert.Contains(t, client.declaredQueues, "test-queue")
	assert.Contains(t, client.bindings, "test-queue:test-exchange:test.key")
}

func TestRegistry_DeclareInfrastructure_ClientNotReady_Timeout_Simple(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: false}
	registry := NewRegistry(client, &stubLogger{})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := registry.DeclareInfrastructure(ctx)

	assert.Error(t, err)
	// Could be either timeout or context cancelled depending on timing
	assert.True(t,
		strings.Contains(err.Error(), "timeout waiting for AMQP client") ||
			strings.Contains(err.Error(), "context cancelled while waiting for AMQP client"),
		"Expected timeout or context cancellation error, got: %s", err.Error())
}

func TestRegistry_DeclareInfrastructure_ExchangeDeclarationError_Simple(t *testing.T) {
	client := &simpleMockAMQPClient{
		isReady:            true,
		declareExchangeErr: errors.New("exchange declaration failed"),
	}
	registry := NewRegistry(client, &stubLogger{})

	registry.RegisterExchange(&ExchangeDeclaration{
		Name: "test-exchange",
		Type: "topic",
	})

	err := registry.DeclareInfrastructure(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to declare exchange test-exchange")
}

func TestRegistry_StartConsumers_Success_Simple(t *testing.T) {
	deliveries := make(chan amqp.Delivery)
	close(deliveries) // Close immediately to avoid starting consumer goroutines

	client := &simpleMockAMQPClient{
		isReady:      true,
		deliveryChan: deliveries,
	}
	registry := NewRegistry(client, &stubLogger{})

	handler := &testHandler{}
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     "test-queue",
		EventType: "test-event",
		Handler:   handler,
	})

	err := registry.StartConsumers(context.Background())

	assert.NoError(t, err)
	assert.True(t, registry.consumersActive)
	assert.NotNil(t, registry.cancelConsumers)

	// Clean up
	registry.StopConsumers()
}

func TestRegistry_StartConsumers_ClientNotReady_Simple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{isReady: false}, &stubLogger{})

	err := registry.StartConsumers(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "AMQP client is not ready")
}

func TestRegistry_StopConsumers_Success_Simple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{isReady: true}, &stubLogger{})

	// Simulate active consumers
	ctx, cancel := context.WithCancel(context.Background())
	registry.consumersActive = true
	registry.cancelConsumers = cancel

	registry.StopConsumers()

	assert.False(t, registry.consumersActive)
	assert.Nil(t, registry.cancelConsumers)

	// Verify context was cancelled
	select {
	case <-ctx.Done():
		// Expected - context should be cancelled
	default:
		t.Fatal("Expected context to be cancelled")
	}
}

func TestRegistry_ValidatePublisher_Simple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	registry.RegisterPublisher(&PublisherDeclaration{
		Exchange:   "test-exchange",
		RoutingKey: "test.key",
	})

	// Valid publisher
	assert.True(t, registry.ValidatePublisher("test-exchange", "test.key"))

	// Invalid publisher
	assert.False(t, registry.ValidatePublisher("unknown-exchange", "test.key"))
	assert.False(t, registry.ValidatePublisher("test-exchange", "unknown.key"))
}

func TestRegistry_ValidateConsumer_Simple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue: "test-queue",
	})

	// Valid consumer
	assert.True(t, registry.ValidateConsumer("test-queue"))

	// Invalid consumer
	assert.False(t, registry.ValidateConsumer("unknown-queue"))
}

func TestRegistry_GetPublishers_Simple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	// Initially empty
	publishers := registry.GetPublishers()
	assert.Empty(t, publishers)

	// Add publishers
	pub1 := &PublisherDeclaration{
		Exchange:   "test-exchange-1",
		RoutingKey: "test.key.1",
		EventType:  "test-event-1",
	}
	pub2 := &PublisherDeclaration{
		Exchange:   "test-exchange-2",
		RoutingKey: "test.key.2",
		EventType:  "test-event-2",
	}

	registry.RegisterPublisher(pub1)
	registry.RegisterPublisher(pub2)

	publishers = registry.GetPublishers()
	assert.Len(t, publishers, 2)

	// Verify data integrity (returned slice should be a copy)
	publishers[0] = nil
	assert.Len(t, registry.GetPublishers(), 2) // Original should be unchanged
}

func TestRegistry_GetConsumers_Simple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	// Initially empty
	consumers := registry.GetConsumers()
	assert.Empty(t, consumers)

	// Add consumers
	cons1 := &ConsumerDeclaration{
		Queue:     "test-queue-1",
		EventType: "test-event-1",
	}
	cons2 := &ConsumerDeclaration{
		Queue:     "test-queue-2",
		EventType: "test-event-2",
	}

	registry.RegisterConsumer(cons1)
	registry.RegisterConsumer(cons2)

	consumers = registry.GetConsumers()
	assert.Len(t, consumers, 2)

	// Verify data integrity (returned slice should be a copy)
	consumers[0] = nil
	assert.Len(t, registry.GetConsumers(), 2) // Original should be unchanged
}

func TestRegistry_RegisterAfterDeclared_Simple(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: true}
	logger := &stubLogger{}
	registry := NewRegistry(client, logger)

	// Declare infrastructure first
	err := registry.DeclareInfrastructure(context.Background())
	assert.NoError(t, err)

	// Now try to register new components (should log warnings but not fail)
	registry.RegisterExchange(&ExchangeDeclaration{
		Name: "late-exchange",
		Type: "topic",
	})

	registry.RegisterQueue(&QueueDeclaration{
		Name:    "late-queue",
		Durable: true,
	})

	registry.RegisterBinding(&BindingDeclaration{
		Queue:      "late-queue",
		Exchange:   "late-exchange",
		RoutingKey: "late.key",
	})

	// Verify these were not actually registered
	assert.NotContains(t, client.declaredExchanges, "late-exchange")
	assert.NotContains(t, client.declaredQueues, "late-queue")
}

func TestRegistry_DeclareInfrastructure_AlreadyDeclared_Simple(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: true}
	registry := NewRegistry(client, &stubLogger{})

	// First declaration
	err := registry.DeclareInfrastructure(context.Background())
	assert.NoError(t, err)
	assert.True(t, registry.declared)

	// Second declaration should be no-op
	err = registry.DeclareInfrastructure(context.Background())
	assert.NoError(t, err)
}

func TestRegistry_DeclareInfrastructure_NilClient_Simple(t *testing.T) {
	registry := NewRegistry(nil, &stubLogger{})

	err := registry.DeclareInfrastructure(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "AMQP client is not available")
}

func TestRegistry_StartConsumers_ConsumeError_Simple(t *testing.T) {
	client := &simpleMockAMQPClient{
		isReady:    true,
		consumeErr: errors.New("consume error"),
	}
	registry := NewRegistry(client, &stubLogger{})

	handler := &testHandler{}
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     "test-queue",
		EventType: "test-event",
		Handler:   handler,
	})

	err := registry.StartConsumers(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start consumer for queue test-queue")
	assert.False(t, registry.consumersActive)
}

func TestRegistry_StartConsumers_NoHandlers_Simple(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: true}
	registry := NewRegistry(client, &stubLogger{})

	// Register consumer without handler (documentation only)
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     "test-queue",
		EventType: "test-event",
		Handler:   nil, // No handler
	})

	err := registry.StartConsumers(context.Background())

	assert.NoError(t, err)
	assert.True(t, registry.consumersActive) // Should still be marked active
	assert.NotNil(t, registry.cancelConsumers)

	// Clean up
	registry.StopConsumers()
}

func TestRegistry_StopConsumers_NotActive_Simple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	// StopConsumers when not active should be no-op
	registry.StopConsumers()

	assert.False(t, registry.consumersActive)
	assert.Nil(t, registry.cancelConsumers)
}
