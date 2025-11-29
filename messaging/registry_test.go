package messaging

import (
	"context"
	"errors"
	"fmt"
	"runtime"
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

	// Controllable readiness for deterministic testing
	makeReady func()

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

// controllableMockAMQPClient provides deterministic readiness control for testing
type controllableMockAMQPClient struct {
	*simpleMockAMQPClient
	readySignal chan struct{}
	signalSent  bool
	signalMu    sync.Mutex
}

// newControllableMockClient creates a mock client with controllable readiness via channel
func newControllableMockClient() (client *controllableMockAMQPClient, readySignal chan struct{}) {
	base := &simpleMockAMQPClient{isReady: false}
	readySignal = make(chan struct{})

	client = &controllableMockAMQPClient{
		simpleMockAMQPClient: base,
		readySignal:          readySignal,
		signalSent:           false,
	}

	// Provide method to make client ready
	client.makeReady = func() {
		client.signalMu.Lock()
		defer client.signalMu.Unlock()
		if !client.signalSent {
			close(readySignal)
			client.signalSent = true
		}
	}

	return
}

// IsReady overrides the base implementation with channel-based signaling
func (c *controllableMockAMQPClient) IsReady() bool {
	// Fast path if already ready
	c.mu.RLock()
	closed := c.closed
	ready := c.isReady
	c.mu.RUnlock()
	if closed {
		return false
	}
	if ready {
		return true
	}

	// Wait for ready signal (non-blocking check)
	select {
	case <-c.readySignal:
		c.mu.Lock()
		c.isReady = true
		c.mu.Unlock()
		return true
	default:
		return false
	}
}

func TestNewRegistrySimple(t *testing.T) {
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
	assert.NotNil(t, registry.consumerIndex)
	assert.NotNil(t, registry.consumerOrder)
	assert.False(t, registry.declared)
	assert.False(t, registry.consumersActive)
}

func TestRegistryDeclareInfrastructureSuccessSimple(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: true}
	logger := &stubLogger{}
	registry := NewRegistry(client, logger)

	// Register infrastructure
	registry.RegisterExchange(&ExchangeDeclaration{
		Name: testExchangeName,
		Type: "topic",
	})
	registry.RegisterQueue(&QueueDeclaration{
		Name:    testQueueName,
		Durable: true,
	})
	registry.RegisterBinding(&BindingDeclaration{
		Queue:      testQueueName,
		Exchange:   testExchangeName,
		RoutingKey: testKeyValue,
	})

	ctx := context.Background()
	err := registry.DeclareInfrastructure(ctx)

	assert.NoError(t, err)
	assert.True(t, registry.declared)
	assert.Contains(t, client.declaredExchanges, testExchangeName)
	assert.Contains(t, client.declaredQueues, testQueueName)
	assert.Contains(t, client.bindings, "test-queue:test-exchange:test.key")
}

func TestRegistryDeclareInfrastructureClientNotReadyTimeoutSimple(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: false}
	registry := NewRegistry(client, &stubLogger{})

	// Use a longer timeout to avoid flakes, but still test the timeout behavior
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := registry.DeclareInfrastructure(ctx)

	assert.Error(t, err)
	// Could be either timeout or context cancelled depending on timing
	assert.True(t,
		strings.Contains(err.Error(), "timeout waiting for AMQP client") ||
			strings.Contains(err.Error(), "context cancelled while waiting for AMQP client"),
		"Expected timeout or context cancellation error, got: %s", err.Error())
}

func TestRegistryDeclareInfrastructureExchangeDeclarationErrorSimple(t *testing.T) {
	client := &simpleMockAMQPClient{
		isReady:            true,
		declareExchangeErr: errors.New("exchange declaration failed"),
	}
	registry := NewRegistry(client, &stubLogger{})

	registry.RegisterExchange(&ExchangeDeclaration{
		Name: testExchangeName,
		Type: "topic",
	})

	err := registry.DeclareInfrastructure(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to declare exchange test-exchange")
}

func TestRegistryStartConsumersSuccessSimple(t *testing.T) {
	deliveries := make(chan amqp.Delivery)
	close(deliveries) // Close immediately to avoid starting consumer goroutines

	client := &simpleMockAMQPClient{
		isReady:      true,
		deliveryChan: deliveries,
	}
	registry := NewRegistry(client, &stubLogger{})

	handler := &testHandler{}
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
	})

	err := registry.StartConsumers(context.Background())

	assert.NoError(t, err)
	assert.True(t, registry.consumersActive)
	assert.NotNil(t, registry.cancelConsumers)

	// Clean up
	registry.StopConsumers()
}

func TestRegistryStartConsumersClientNotReadySimple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{isReady: false}, &stubLogger{})

	err := registry.StartConsumers(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "AMQP client is not ready")
}

func TestRegistryStopConsumersSuccessSimple(t *testing.T) {
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

func TestRegistryValidatePublisherSimple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	registry.RegisterPublisher(&PublisherDeclaration{
		Exchange:   testExchangeName,
		RoutingKey: testKeyValue,
	})

	// Valid publisher
	assert.True(t, registry.ValidatePublisher(testExchangeName, testKeyValue))

	// Invalid publisher
	assert.False(t, registry.ValidatePublisher("unknown-exchange", testKeyValue))
	assert.False(t, registry.ValidatePublisher(testExchangeName, "unknown.key"))
}

func TestRegistryValidateConsumerSimple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue: testQueueName,
	})

	// Valid consumer
	assert.True(t, registry.ValidateConsumer(testQueueName))

	// Invalid consumer
	assert.False(t, registry.ValidateConsumer("unknown-queue"))
}

func TestRegistryPublishersSimple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	// Initially empty
	publishers := registry.Publishers()
	assert.Empty(t, publishers)

	// Add publishers
	pub1 := &PublisherDeclaration{
		Exchange:   testExchange1Name,
		RoutingKey: "test.key.1",
		EventType:  "test-event-1",
	}
	pub2 := &PublisherDeclaration{
		Exchange:   testExchange2Name,
		RoutingKey: "test.key.2",
		EventType:  "test-event-2",
	}

	registry.RegisterPublisher(pub1)
	registry.RegisterPublisher(pub2)

	publishers = registry.Publishers()
	assert.Len(t, publishers, 2)

	// Verify data integrity (returned slice should be a copy)
	publishers[0] = nil
	assert.Len(t, registry.Publishers(), 2) // Original should be unchanged
}

func TestRegistryConsumersSimple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	// Initially empty
	consumers := registry.Consumers()
	assert.Empty(t, consumers)

	// Add consumers
	cons1 := &ConsumerDeclaration{
		Queue:     testQueue1Name,
		EventType: "test-event-1",
	}
	cons2 := &ConsumerDeclaration{
		Queue:     testQueue2Name,
		EventType: "test-event-2",
	}

	registry.RegisterConsumer(cons1)
	registry.RegisterConsumer(cons2)

	consumers = registry.Consumers()
	assert.Len(t, consumers, 2)

	// Verify data integrity (returned slice should be a copy)
	consumers[0] = nil
	assert.Len(t, registry.Consumers(), 2) // Original should be unchanged
}

func TestRegistryRegisterAfterDeclaredSimple(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: true}
	logger := &stubLogger{}
	registry := NewRegistry(client, logger)

	// Declare infrastructure first
	err := registry.DeclareInfrastructure(context.Background())
	assert.NoError(t, err)

	// Now try to register new components (should log warnings but not fail)
	registry.RegisterExchange(&ExchangeDeclaration{
		Name: lateExchangeName,
		Type: "topic",
	})

	registry.RegisterQueue(&QueueDeclaration{
		Name:    lateQueueName,
		Durable: true,
	})

	registry.RegisterBinding(&BindingDeclaration{
		Queue:      lateQueueName,
		Exchange:   lateExchangeName,
		RoutingKey: "late.key",
	})

	// Verify these were not actually registered
	assert.NotContains(t, client.declaredExchanges, lateExchangeName)
	assert.NotContains(t, client.declaredQueues, lateQueueName)
}

func TestRegistryDeclareInfrastructureAlreadyDeclaredSimple(t *testing.T) {
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

func TestRegistryDeclareInfrastructureNilClientSimple(t *testing.T) {
	registry := NewRegistry(nil, &stubLogger{})

	err := registry.DeclareInfrastructure(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "AMQP client is not available")
}

func TestRegistryStartConsumersConsumeErrorSimple(t *testing.T) {
	client := &simpleMockAMQPClient{
		isReady:    true,
		consumeErr: errors.New("consume error"),
	}
	registry := NewRegistry(client, &stubLogger{})

	handler := &testHandler{}
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
	})

	err := registry.StartConsumers(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start consumer for queue test-queue")
	assert.False(t, registry.consumersActive)
}

func TestRegistryStartConsumersNoHandlersSimple(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: true}
	registry := NewRegistry(client, &stubLogger{})

	// Register consumer without handler (documentation only)
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   nil, // No handler
	})

	err := registry.StartConsumers(context.Background())

	assert.NoError(t, err)
	assert.True(t, registry.consumersActive) // Should still be marked active
	assert.NotNil(t, registry.cancelConsumers)

	// Clean up
	registry.StopConsumers()
}

func TestRegistryStopConsumersNotActiveSimple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	// StopConsumers when not active should be no-op
	registry.StopConsumers()

	assert.False(t, registry.consumersActive)
	assert.Nil(t, registry.cancelConsumers)
}

// ===== Getter Methods Tests =====

func TestRegistryExchanges(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	// Initially empty
	exchanges := registry.Exchanges()
	assert.Empty(t, exchanges)

	// Add exchanges
	ex1 := &ExchangeDeclaration{
		Name:    testExchange1Name,
		Type:    "topic",
		Durable: true,
	}
	ex2 := &ExchangeDeclaration{
		Name:       testExchange2Name,
		Type:       "direct",
		AutoDelete: true,
	}

	registry.RegisterExchange(ex1)
	registry.RegisterExchange(ex2)

	exchanges = registry.Exchanges()
	assert.Len(t, exchanges, 2)
	assert.Equal(t, ex1, exchanges[testExchange1Name])
	assert.Equal(t, ex2, exchanges[testExchange2Name])

	// Verify data integrity (returned map should be a copy)
	exchanges[testExchange1Name] = nil
	originalExchanges := registry.Exchanges()
	assert.Len(t, originalExchanges, 2)
	assert.NotNil(t, originalExchanges[testExchange1Name])
}

func TestRegistryQueues(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	// Initially empty
	queues := registry.Queues()
	assert.Empty(t, queues)

	// Add queues
	q1 := &QueueDeclaration{
		Name:    testQueue1Name,
		Durable: true,
	}
	q2 := &QueueDeclaration{
		Name:       testQueue2Name,
		AutoDelete: true,
		Exclusive:  true,
	}

	registry.RegisterQueue(q1)
	registry.RegisterQueue(q2)

	queues = registry.Queues()
	assert.Len(t, queues, 2)
	assert.Equal(t, q1, queues[testQueue1Name])
	assert.Equal(t, q2, queues[testQueue2Name])

	// Verify data integrity (returned map should be a copy)
	queues[testQueue1Name] = nil
	originalQueues := registry.Queues()
	assert.Len(t, originalQueues, 2)
	assert.NotNil(t, originalQueues[testQueue1Name])
}

func TestRegistryBindings(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	// Initially empty
	bindings := registry.Bindings()
	assert.Empty(t, bindings)

	// Add bindings
	b1 := &BindingDeclaration{
		Queue:      testQueue1Name,
		Exchange:   testExchange1Name,
		RoutingKey: "test.key.1",
	}
	b2 := &BindingDeclaration{
		Queue:      testQueue2Name,
		Exchange:   testExchange2Name,
		RoutingKey: "test.key.2",
		NoWait:     true,
	}

	registry.RegisterBinding(b1)
	registry.RegisterBinding(b2)

	bindings = registry.Bindings()
	assert.Len(t, bindings, 2)
	assert.Equal(t, b1, bindings[0])
	assert.Equal(t, b2, bindings[1])

	// Verify data integrity (returned slice should be a copy)
	bindings[0] = nil
	originalBindings := registry.Bindings()
	assert.Len(t, originalBindings, 2)
	assert.NotNil(t, originalBindings[0])
}

// ===== amqpDeliveryAccessor Tests =====

func TestAmqpDeliveryAccessorGet(t *testing.T) {
	tests := []struct {
		name     string
		headers  amqp.Table
		key      string
		expected any
	}{
		{
			name:     "nil headers",
			headers:  nil,
			key:      testKeyName,
			expected: nil,
		},
		{
			name:     "empty headers",
			headers:  amqp.Table{},
			key:      testKeyName,
			expected: nil,
		},
		{
			name: "existing key",
			headers: amqp.Table{
				testKeyName: testValueContent,
			},
			key:      testKeyName,
			expected: testValueContent,
		},
		{
			name: "non-existing key",
			headers: amqp.Table{
				"other-key": "other-value",
			},
			key:      testKeyName,
			expected: nil,
		},
		{
			name: "multiple headers",
			headers: amqp.Table{
				"key1": "value1",
				"key2": 42,
				"key3": true,
			},
			key:      "key2",
			expected: 42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accessor := &amqpDeliveryAccessor{headers: tt.headers}
			result := accessor.Get(tt.key)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAmqpDeliveryAccessorSet(t *testing.T) {
	// Create accessor with some initial headers
	headers := amqp.Table{
		"existing": "value",
	}
	accessor := &amqpDeliveryAccessor{headers: headers}

	// Verify initial state
	assert.Equal(t, "value", accessor.Get("existing"))

	// Call Set - should be a no-op
	accessor.Set(newKeyName, "new-value")
	accessor.Set("existing", "modified-value")

	// Verify headers remain unchanged
	assert.Equal(t, "value", accessor.Get("existing"))
	assert.Nil(t, accessor.Get(newKeyName))

	// Verify the original headers map wasn't modified
	assert.Equal(t, "value", headers["existing"])
	assert.NotContains(t, headers, newKeyName)
}

// ===== Enhanced DeclareInfrastructure Tests =====

func TestRegistryDeclareInfrastructureQueueDeclarationError(t *testing.T) {
	client := &simpleMockAMQPClient{
		isReady:         true,
		declareQueueErr: errors.New("queue declaration failed"),
	}
	registry := NewRegistry(client, &stubLogger{})

	registry.RegisterQueue(&QueueDeclaration{
		Name:    testQueueName,
		Durable: true,
	})

	err := registry.DeclareInfrastructure(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to declare queue test-queue")
}

func TestRegistryDeclareInfrastructureBindingError(t *testing.T) {
	client := &simpleMockAMQPClient{
		isReady:      true,
		bindQueueErr: errors.New("binding failed"),
	}
	registry := NewRegistry(client, &stubLogger{})

	registry.RegisterBinding(&BindingDeclaration{
		Queue:      testQueueName,
		Exchange:   testExchangeName,
		RoutingKey: testKeyValue,
	})

	err := registry.DeclareInfrastructure(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to bind queue test-queue to exchange test-exchange")
}

func TestRegistryDeclareInfrastructureContextCancellation(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: false}
	registry := NewRegistry(client, &stubLogger{})

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the context immediately
	cancel()

	err := registry.DeclareInfrastructure(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context cancelled while waiting for AMQP client")
}

func TestRegistryDeclareInfrastructureClientBecomesReady(t *testing.T) {
	client, readySignal := newControllableMockClient()
	registry := NewRegistry(client, &stubLogger{})

	registry.RegisterExchange(&ExchangeDeclaration{
		Name: testExchangeName,
		Type: "topic",
	})

	// Start declaration in a goroutine
	done := make(chan error, 1)
	go func() {
		done <- registry.DeclareInfrastructure(context.Background())
	}()

	// Wait for the goroutine to start, then signal readiness deterministically
	go func() {
		// Small delay to ensure the DeclareInfrastructure call is waiting
		time.Sleep(10 * time.Millisecond)
		client.makeReady()
	}()

	// Should complete successfully after readiness signal
	select {
	case err := <-done:
		assert.NoError(t, err)
		assert.True(t, registry.declared)
	case <-time.After(1 * time.Second):
		t.Fatal("DeclareInfrastructure did not complete within timeout")
	}

	// Ensure the ready signal was used
	select {
	case <-readySignal:
		// Expected - signal should be closed
	default:
		t.Error("Ready signal was not closed")
	}
}

// ===== Message Handling Tests =====

func TestRegistryHandleMessagesContextCancellation(t *testing.T) {
	deliveries := make(chan amqp.Delivery)
	client := &simpleMockAMQPClient{
		isReady:      true,
		deliveryChan: deliveries,
	}
	registry := NewRegistry(client, &stubLogger{})

	handler := &testHandler{}
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
	}

	// Start the message handler
	ctx, cancel := context.WithCancel(context.Background())

	// Use synchronization channels instead of sleep
	handlerDone := make(chan struct{})
	handlerStarted := make(chan struct{})
	go func() {
		close(handlerStarted) // Signal handler is about to start
		registry.handleMessages(ctx, consumer, deliveries)
		close(handlerDone)
	}()

	// Wait for handler to start, then cancel context immediately
	<-handlerStarted
	cancel()

	// Handler should stop
	select {
	case <-handlerDone:
		// Expected
	case <-time.After(1 * time.Second):
		t.Fatal("Handler did not stop after context cancellation")
	}
}

func TestRegistryHandleMessagesChannelClosure(t *testing.T) {
	deliveries := make(chan amqp.Delivery)
	client := &simpleMockAMQPClient{
		isReady:      true,
		deliveryChan: deliveries,
	}
	registry := NewRegistry(client, &stubLogger{})

	handler := &testHandler{}
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
	}

	// Start the message handler
	ctx := context.Background()

	// Use synchronization channels instead of sleep
	handlerDone := make(chan struct{})
	handlerStarted := make(chan struct{})
	go func() {
		close(handlerStarted) // Signal handler is about to start
		registry.handleMessages(ctx, consumer, deliveries)
		close(handlerDone)
	}()

	// Wait for handler to start, then close delivery channel immediately
	<-handlerStarted
	close(deliveries)

	// Handler should stop
	select {
	case <-handlerDone:
		// Expected
	case <-time.After(1 * time.Second):
		t.Fatal("Handler did not stop after channel closure")
	}
}

func TestRegistryHandleMessagesWithDelivery(t *testing.T) {
	deliveries := make(chan amqp.Delivery, 1)
	client := &simpleMockAMQPClient{
		isReady:      true,
		deliveryChan: deliveries,
	}
	registry := NewRegistry(client, &stubLogger{})

	// Create a handler that signals when message is processed
	handler := &countingTestHandler{}
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
		AutoAck:   false,
	}

	// Create a mock delivery
	delivery := amqp.Delivery{
		MessageId:    testMessageID,
		RoutingKey:   testRoutingKey,
		Exchange:     testExchangeName,
		DeliveryTag:  123,
		Body:         []byte(testMessageBody),
		Headers:      amqp.Table{},
		Acknowledger: &mockAcknowledger{},
	}

	// Start the message handler with test context
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Use synchronization channels
	handlerDone := make(chan struct{})
	handlerStarted := make(chan struct{})
	go func() {
		defer close(handlerDone)
		close(handlerStarted) // Signal handler is about to start
		registry.handleMessages(ctx, consumer, deliveries)
	}()

	// Wait for handler to start, then send the delivery
	<-handlerStarted
	deliveries <- delivery

	// Wait for handler to process the message by checking call count
	for range 100 { // Max 100ms wait with 1ms intervals
		if handler.CallCount() >= 1 {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}

	// Verify handler was called
	assert.Equal(t, 1, handler.CallCount())

	// Close channel to stop handler
	close(deliveries)

	// Wait for handler to finish
	select {
	case <-handlerDone:
		// Expected
	case <-time.After(1 * time.Second):
		t.Fatal("Handler did not stop after channel closure")
	}
}

// countingTestHandler extends testHandler with call counting
type countingTestHandler struct {
	testHandler
	callCount int
	mu        sync.Mutex
}

func (h *countingTestHandler) Handle(ctx context.Context, delivery *amqp.Delivery) error {
	h.mu.Lock()
	h.callCount++
	h.mu.Unlock()
	return h.testHandler.Handle(ctx, delivery)
}

func (h *countingTestHandler) CallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.callCount
}

// mockAcknowledger for testing message acknowledgment
type mockAcknowledger struct {
	ackCalled    bool
	nackCalled   bool
	ackErr       error
	nackErr      error
	nackMultiple bool
	nackRequeue  bool
}

func (m *mockAcknowledger) Ack(_ uint64, _ bool) error {
	m.ackCalled = true
	return m.ackErr
}

func (m *mockAcknowledger) Nack(_ uint64, multiple, requeue bool) error {
	m.nackCalled = true
	m.nackMultiple = multiple
	m.nackRequeue = requeue
	return m.nackErr
}

func (m *mockAcknowledger) Reject(_ uint64, _ bool) error {
	return nil
}

// ===== processMessage Tests =====

func TestRegistryProcessMessageSuccess(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &countingTestHandler{}
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
		AutoAck:   false,
	}

	acker := &mockAcknowledger{}
	delivery := &amqp.Delivery{
		MessageId:    testMessageID,
		RoutingKey:   testRoutingKey,
		Exchange:     testExchangeName,
		DeliveryTag:  123,
		Body:         []byte(testMessageBody),
		Headers:      amqp.Table{"test-header": testValueContent},
		Acknowledger: acker,
	}

	log := &stubLogger{}
	ctx := context.Background()

	registry.processMessage(ctx, consumer, delivery, log)

	// Verify handler was called
	assert.Equal(t, 1, handler.CallCount())

	// Verify message was acknowledged
	assert.True(t, acker.ackCalled)
	assert.False(t, acker.nackCalled)
}

func TestRegistryProcessMessageHandlerError(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &countingTestHandler{
		testHandler: testHandler{retErr: errors.New("handler error")},
	}
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
		AutoAck:   false,
	}

	acker := &mockAcknowledger{}
	delivery := &amqp.Delivery{
		MessageId:    testMessageID,
		RoutingKey:   testRoutingKey,
		Exchange:     testExchangeName,
		DeliveryTag:  123,
		Body:         []byte(testMessageBody),
		Headers:      amqp.Table{},
		Acknowledger: acker,
	}

	log := &stubLogger{}
	ctx := context.Background()

	registry.processMessage(ctx, consumer, delivery, log)

	// Verify handler was called
	assert.Equal(t, 1, handler.CallCount())

	// Verify message was negatively acknowledged WITHOUT requeue (prevents infinite retry loops)
	assert.False(t, acker.ackCalled)
	assert.True(t, acker.nackCalled)
	assert.False(t, acker.nackMultiple, "Should nack single message only")
	assert.False(t, acker.nackRequeue, "Should NOT requeue failed messages (prevents infinite loops)")
}

func TestRegistryProcessMessageAutoAck(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &countingTestHandler{}
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
		AutoAck:   true, // AutoAck enabled
	}

	acker := &mockAcknowledger{}
	delivery := &amqp.Delivery{
		MessageId:    testMessageID,
		RoutingKey:   testRoutingKey,
		Exchange:     testExchangeName,
		DeliveryTag:  123,
		Body:         []byte(testMessageBody),
		Headers:      amqp.Table{},
		Acknowledger: acker,
	}

	log := &stubLogger{}
	ctx := context.Background()

	registry.processMessage(ctx, consumer, delivery, log)

	// Verify handler was called
	assert.Equal(t, 1, handler.CallCount())

	// With AutoAck, no manual ack/nack should happen
	assert.False(t, acker.ackCalled)
	assert.False(t, acker.nackCalled)
}

func TestRegistryProcessMessageAckError(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &countingTestHandler{}
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
		AutoAck:   false,
	}

	acker := &mockAcknowledger{
		ackErr: errors.New("ack failed"),
	}
	delivery := &amqp.Delivery{
		MessageId:    testMessageID,
		RoutingKey:   testRoutingKey,
		Exchange:     testExchangeName,
		DeliveryTag:  123,
		Body:         []byte(testMessageBody),
		Headers:      amqp.Table{},
		Acknowledger: acker,
	}

	log := &stubLogger{}
	ctx := context.Background()

	registry.processMessage(ctx, consumer, delivery, log)

	// Verify handler was called
	assert.Equal(t, 1, handler.CallCount())

	// Verify ack was attempted (even though it failed)
	assert.True(t, acker.ackCalled)
	assert.False(t, acker.nackCalled)
}

func TestRegistryProcessMessageNackError(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &countingTestHandler{
		testHandler: testHandler{retErr: errors.New("handler error")},
	}
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
		AutoAck:   false,
	}

	acker := &mockAcknowledger{
		nackErr: errors.New("nack failed"),
	}
	delivery := &amqp.Delivery{
		MessageId:    testMessageID,
		RoutingKey:   testRoutingKey,
		Exchange:     testExchangeName,
		DeliveryTag:  123,
		Body:         []byte(testMessageBody),
		Headers:      amqp.Table{},
		Acknowledger: acker,
	}

	log := &stubLogger{}
	ctx := context.Background()

	registry.processMessage(ctx, consumer, delivery, log)

	// Verify handler was called
	assert.Equal(t, 1, handler.CallCount())

	// Verify nack was attempted (even though it failed)
	assert.False(t, acker.ackCalled)
	assert.True(t, acker.nackCalled)
}

// ===== Panic Recovery Tests =====

// panicTestHandler is a MessageHandler that panics with a configured message
type panicTestHandler struct {
	panicMsg  string
	callCount int
	mu        sync.Mutex
}

func (h *panicTestHandler) Handle(_ context.Context, _ *amqp.Delivery) error {
	h.mu.Lock()
	h.callCount++
	h.mu.Unlock()
	panic(h.panicMsg)
}

func (h *panicTestHandler) EventType() string { return "panic-test" }

func (h *panicTestHandler) CallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.callCount
}

func TestRegistryProcessMessageHandlerPanic(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &panicTestHandler{panicMsg: "nil pointer dereference"}
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
		AutoAck:   false,
	}

	acker := &mockAcknowledger{}
	delivery := &amqp.Delivery{
		MessageId:    testMessageID,
		RoutingKey:   testRoutingKey,
		Exchange:     testExchangeName,
		DeliveryTag:  123,
		Body:         []byte(testMessageBody),
		Headers:      amqp.Table{},
		Acknowledger: acker,
	}

	log := &stubLogger{}
	ctx := context.Background()

	// This should NOT panic - panic should be recovered
	require.NotPanics(t, func() {
		registry.processMessage(ctx, consumer, delivery, log)
	})

	// Verify handler was called
	assert.Equal(t, 1, handler.CallCount())

	// Verify message was negatively acknowledged WITHOUT requeue (same as errors)
	assert.False(t, acker.ackCalled)
	assert.True(t, acker.nackCalled)
	assert.False(t, acker.nackMultiple, "Should nack single message only")
	assert.False(t, acker.nackRequeue, "Should NOT requeue panicked messages (prevents infinite loops)")
}

func TestRegistryProcessMessageHandlerPanicNack(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &panicTestHandler{panicMsg: "test panic"}
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
		AutoAck:   false,
	}

	acker := &mockAcknowledger{}
	delivery := &amqp.Delivery{
		MessageId:    testMessageID,
		RoutingKey:   testRoutingKey,
		Exchange:     testExchangeName,
		DeliveryTag:  123,
		Body:         []byte(testMessageBody),
		Headers:      amqp.Table{},
		Acknowledger: acker,
	}

	log := &stubLogger{}
	ctx := context.Background()

	registry.processMessage(ctx, consumer, delivery, log)

	// Verify panic resulted in nack without requeue (consistent with error handling)
	assert.False(t, acker.ackCalled, "Should not ack panicked message")
	assert.True(t, acker.nackCalled, "Should nack panicked message")
	assert.False(t, acker.nackRequeue, "Should NOT requeue panicked messages")
}

func TestRegistryProcessMessageHandlerPanicLogging(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &panicTestHandler{panicMsg: "critical error"}
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
		AutoAck:   false,
	}

	acker := &mockAcknowledger{}
	delivery := &amqp.Delivery{
		MessageId:     testMessageID,
		CorrelationId: "test-correlation-123",
		RoutingKey:    testRoutingKey,
		Exchange:      testExchangeName,
		DeliveryTag:   123,
		ConsumerTag:   "test-consumer-tag",
		Body:          []byte(testMessageBody),
		Headers:       amqp.Table{},
		Acknowledger:  acker,
	}

	log := &stubLogger{}
	ctx := context.Background()

	registry.processMessage(ctx, consumer, delivery, log)

	// Verify panic was logged with appropriate message
	entries := log.getEntries()
	require.NotEmpty(t, entries, "Expected at least one log entry")

	// Check for panic recovery log message
	foundPanicLog := false
	for _, entry := range entries {
		if strings.Contains(entry, "Panic recovered in message handler") {
			foundPanicLog = true
			break
		}
	}
	assert.True(t, foundPanicLog, "Expected panic recovery log message")
}

func TestRegistryHandleMessagesContinuesAfterPanic(t *testing.T) {
	deliveries := make(chan amqp.Delivery, 3)
	mockClient := &simpleMockAMQPClient{
		deliveryChan: deliveries,
		isReady:      true,
	}
	registry := NewRegistry(mockClient, &stubLogger{})

	// Handler panics on all messages
	panicHandler := &panicTestHandler{panicMsg: "first message panic"}

	panicConsumer := &ConsumerDeclaration{
		Queue:     "panic-queue",
		EventType: "panic-event",
		Handler:   panicHandler,
		AutoAck:   false,
	}

	// Create deliveries
	acker1 := &mockAcknowledger{}
	acker2 := &mockAcknowledger{}
	acker3 := &mockAcknowledger{}

	delivery1 := amqp.Delivery{
		MessageId:    "msg-1",
		DeliveryTag:  1,
		Headers:      amqp.Table{},
		Acknowledger: acker1,
	}
	delivery2 := amqp.Delivery{
		MessageId:    "msg-2",
		DeliveryTag:  2,
		Headers:      amqp.Table{},
		Acknowledger: acker2,
	}
	delivery3 := amqp.Delivery{
		MessageId:    "msg-3",
		DeliveryTag:  3,
		Headers:      amqp.Table{},
		Acknowledger: acker3,
	}

	// Send messages
	deliveries <- delivery1 // Will panic
	deliveries <- delivery2 // Should still be processed
	deliveries <- delivery3 // Should still be processed
	close(deliveries)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Process messages from panic consumer
	registry.handleMessages(ctx, panicConsumer, deliveries)

	// Verify all messages were processed despite first panic
	assert.Equal(t, 3, panicHandler.CallCount(), "All messages should be processed despite panics")
	assert.True(t, acker1.nackCalled, "First message (panicked) should be nacked")
	assert.True(t, acker2.nackCalled, "Second message (panicked) should be nacked")
	assert.True(t, acker3.nackCalled, "Third message (panicked) should be nacked")
}

func TestRegistryMultipleConsumersPanicIsolation(t *testing.T) {
	deliveries1 := make(chan amqp.Delivery, 1)
	deliveries2 := make(chan amqp.Delivery, 1)

	mockClient := &simpleMockAMQPClient{isReady: true}
	registry := NewRegistry(mockClient, &stubLogger{})

	// Consumer 1 panics, Consumer 2 succeeds
	panicHandler := &panicTestHandler{panicMsg: "consumer 1 panic"}
	successHandler := &countingTestHandler{}

	consumer1 := &ConsumerDeclaration{
		Queue:     "panic-queue",
		EventType: "panic-event",
		Handler:   panicHandler,
		AutoAck:   false,
	}

	consumer2 := &ConsumerDeclaration{
		Queue:     "success-queue",
		EventType: "success-event",
		Handler:   successHandler,
		AutoAck:   false,
	}

	// Create deliveries
	acker1 := &mockAcknowledger{}
	acker2 := &mockAcknowledger{}

	delivery1 := amqp.Delivery{
		MessageId:    "panic-msg",
		DeliveryTag:  1,
		Headers:      amqp.Table{},
		Acknowledger: acker1,
	}
	delivery2 := amqp.Delivery{
		MessageId:    "success-msg",
		DeliveryTag:  2,
		Headers:      amqp.Table{},
		Acknowledger: acker2,
	}

	deliveries1 <- delivery1
	deliveries2 <- delivery2
	close(deliveries1)
	close(deliveries2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start both consumers
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		registry.handleMessages(ctx, consumer1, deliveries1)
	}()

	go func() {
		defer wg.Done()
		registry.handleMessages(ctx, consumer2, deliveries2)
	}()

	wg.Wait()

	// Verify consumer 1 panicked and nacked
	assert.Equal(t, 1, panicHandler.CallCount())
	assert.True(t, acker1.nackCalled, "Panicked message should be nacked")
	assert.False(t, acker1.nackRequeue, "Panicked message should NOT be requeued")

	// Verify consumer 2 succeeded and acked (unaffected by consumer 1's panic)
	assert.Equal(t, 1, successHandler.CallCount())
	assert.True(t, acker2.ackCalled, "Success message should be acked")
	assert.False(t, acker2.nackCalled, "Success message should NOT be nacked")
}

func TestRegistryProcessMessageHandlerPanicWithAutoAck(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &panicTestHandler{panicMsg: "panic with autoack"}
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
		AutoAck:   true, // AutoAck enabled
	}

	acker := &mockAcknowledger{}
	delivery := &amqp.Delivery{
		MessageId:    testMessageID,
		RoutingKey:   testRoutingKey,
		Exchange:     testExchangeName,
		DeliveryTag:  123,
		Body:         []byte(testMessageBody),
		Headers:      amqp.Table{},
		Acknowledger: acker,
	}

	log := &stubLogger{}
	ctx := context.Background()

	// Should not panic
	require.NotPanics(t, func() {
		registry.processMessage(ctx, consumer, delivery, log)
	})

	// With AutoAck, no manual ack/nack should happen (even on panic)
	assert.False(t, acker.ackCalled, "AutoAck mode should not manually ack")
	assert.False(t, acker.nackCalled, "AutoAck mode should not manually nack")
}

// ===== Concurrent Operations Tests =====

func TestRegistryStartConsumersWithMultipleConsumers(t *testing.T) {
	deliveries1 := make(chan amqp.Delivery)
	deliveries2 := make(chan amqp.Delivery)

	client := &multipleMockAMQPClient{
		simpleMockAMQPClient: simpleMockAMQPClient{isReady: true},
		queues: map[string]chan amqp.Delivery{
			"queue-1": deliveries1,
			"queue-2": deliveries2,
		},
	}
	registry := NewRegistry(client, &stubLogger{})

	handler1 := &testHandler{}
	handler2 := &testHandler{}

	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     "queue-1",
		EventType: "event-1",
		Handler:   handler1,
	})
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     "queue-2",
		EventType: "event-2",
		Handler:   handler2,
	})

	err := registry.StartConsumers(context.Background())
	assert.NoError(t, err)
	assert.True(t, registry.consumersActive)

	// Clean up
	close(deliveries1)
	close(deliveries2)
	registry.StopConsumers()
}

func TestRegistryStartConsumersWithPartialFailure(t *testing.T) {
	client := &multipleMockAMQPClient{
		simpleMockAMQPClient: simpleMockAMQPClient{isReady: true},
		consumeErrors: map[string]error{
			"failing-queue": errors.New("consume failed"),
		},
	}
	registry := NewRegistry(client, &stubLogger{})

	handler1 := &testHandler{}
	handler2 := &testHandler{}

	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     "working-queue",
		EventType: "event-1",
		Handler:   handler1,
	})
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     "failing-queue",
		EventType: "event-2",
		Handler:   handler2,
	})

	err := registry.StartConsumers(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start consumer for queue failing-queue")
	assert.False(t, registry.consumersActive)
}

// multipleMockAMQPClient extends simpleMockAMQPClient for testing multiple consumers
type multipleMockAMQPClient struct {
	simpleMockAMQPClient
	queues        map[string]chan amqp.Delivery
	consumeErrors map[string]error
}

func (m *multipleMockAMQPClient) ConsumeFromQueue(_ context.Context, opts ConsumeOptions) (<-chan amqp.Delivery, error) {
	if m.consumeErrors != nil {
		if err, exists := m.consumeErrors[opts.Queue]; exists {
			return nil, err
		}
	}

	if m.queues != nil {
		if ch, exists := m.queues[opts.Queue]; exists {
			return ch, nil
		}
	}

	// Default behavior
	return m.simpleMockAMQPClient.ConsumeFromQueue(context.Background(), opts)
}

// ===== Edge Cases and Boundary Conditions =====

func TestRegistryStartConsumersAlreadyStarted(t *testing.T) {
	deliveries := make(chan amqp.Delivery)
	close(deliveries) // Close to avoid goroutine leak

	client := &simpleMockAMQPClient{
		isReady:      true,
		deliveryChan: deliveries,
	}
	registry := NewRegistry(client, &stubLogger{})

	handler := &testHandler{}
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
	})

	// Start consumers first time
	err := registry.StartConsumers(context.Background())
	assert.NoError(t, err)
	assert.True(t, registry.consumersActive)

	// Start consumers second time - should be no-op
	err = registry.StartConsumers(context.Background())
	assert.NoError(t, err)
	assert.True(t, registry.consumersActive)

	// Clean up
	registry.StopConsumers()
}

func TestRegistryStartConsumersNilClient(t *testing.T) {
	registry := NewRegistry(nil, &stubLogger{})

	err := registry.StartConsumers(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "AMQP client is not ready")
}

func TestRegistryStartConsumersOnlyDocumentationConsumers(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: true}
	registry := NewRegistry(client, &stubLogger{})

	// Register consumer without handler (documentation only)
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     "doc-queue",
		EventType: "doc-event",
		Handler:   nil, // No handler
	})

	err := registry.StartConsumers(context.Background())
	assert.NoError(t, err)
	assert.True(t, registry.consumersActive)

	// Clean up
	registry.StopConsumers()
}

func TestRegistryRegisterPublisherNeverBlocked(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{isReady: true}, &stubLogger{})

	// Publishers can be registered even after declaration (unlike exchanges/queues/bindings)
	err := registry.DeclareInfrastructure(context.Background())
	assert.NoError(t, err)

	// This should work fine
	registry.RegisterPublisher(&PublisherDeclaration{
		Exchange:   lateExchangeName,
		RoutingKey: "late.key",
		EventType:  "late-event",
	})

	publishers := registry.Publishers()
	assert.Len(t, publishers, 1)
}

func TestRegistryRegisterConsumerNeverBlocked(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{isReady: true}, &stubLogger{})

	// Consumers can be registered even after declaration (unlike exchanges/queues/bindings)
	err := registry.DeclareInfrastructure(context.Background())
	assert.NoError(t, err)

	// This should work fine
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     lateQueueName,
		EventType: "late-event",
	})

	consumers := registry.Consumers()
	assert.Len(t, consumers, 1)
}

// ===== Consumer Concurrency Tests (v0.17+) =====

func TestAutoScaleWorkersDefault(t *testing.T) {
	decls := NewDeclarations()

	consumer := decls.DeclareConsumer(&ConsumerOptions{
		Queue:     testQueue,
		Consumer:  testConsumer,
		EventType: testEventType,
		// Workers not set - should auto-scale
	}, nil)

	expectedWorkers := runtime.NumCPU() * 4
	assert.Equal(t, expectedWorkers, consumer.Workers, "Workers should auto-scale to NumCPU*4")

	expectedPrefetch := min(expectedWorkers*10, 500)
	assert.Equal(t, expectedPrefetch, consumer.PrefetchCount, "PrefetchCount should be Workers*10 capped at 500")
}

func TestExplicitWorkersOverride(t *testing.T) {
	decls := NewDeclarations()

	consumer := decls.DeclareConsumer(&ConsumerOptions{
		Queue:         testQueue,
		Consumer:      testConsumer,
		EventType:     testEventType,
		Workers:       10, // Explicit override
		PrefetchCount: 50, // Explicit override
	}, nil)

	assert.Equal(t, 10, consumer.Workers, "Explicit Workers should not be overridden")
	assert.Equal(t, 50, consumer.PrefetchCount, "Explicit PrefetchCount should not be overridden")
}

func TestSequentialProcessing(t *testing.T) {
	decls := NewDeclarations()

	consumer := decls.DeclareConsumer(&ConsumerOptions{
		Queue:     "sequential-queue",
		Consumer:  "sequential-consumer",
		EventType: "sequential-event",
		Workers:   1, // Explicit sequential processing
	}, nil)

	assert.Equal(t, 1, consumer.Workers, "Sequential processing should use 1 worker")
}

func TestWorkerPoolConcurrentProcessing(t *testing.T) {
	// This is a unit test that verifies the worker pool spawns correctly
	// Integration test with actual concurrent message processing would go in integration tests

	deliveries := make(chan amqp.Delivery, 10)
	mockClient := &simpleMockAMQPClient{
		deliveryChan: deliveries,
		isReady:      true,
	}
	registry := NewRegistry(mockClient, &stubLogger{})

	handler := &countingTestHandler{}
	consumer := &ConsumerDeclaration{
		Queue:         "concurrent-queue",
		Consumer:      "concurrent-consumer",
		EventType:     "concurrent-event",
		Handler:       handler,
		Workers:       4, // 4 concurrent workers
		PrefetchCount: 40,
		AutoAck:       false,
	}

	// Send 8 messages
	for i := 0; i < 8; i++ {
		acker := &mockAcknowledger{}
		deliveries <- amqp.Delivery{
			MessageId:    fmt.Sprintf(testMessageIDFmt, i),
			DeliveryTag:  uint64(i + 1),
			Headers:      amqp.Table{},
			Acknowledger: acker,
		}
	}
	close(deliveries)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// This will process all messages and return when deliveries channel closes
	registry.handleMessages(ctx, consumer, deliveries)

	// Verify all 8 messages were processed
	assert.Equal(t, 8, handler.CallCount(), "All messages should be processed")
}

func TestPrefetchAutoScaling(t *testing.T) {
	tests := []struct {
		name             string
		workers          int
		expectedPrefetch int
	}{
		{"Small worker pool", 5, 50},                  // 5*10 = 50
		{"Medium worker pool", 20, 200},               // 20*10 = 200
		{"Large worker pool (capped)", 60, 500},       // 60*10 = 600, but capped at 500
		{"Very large worker pool (capped)", 100, 500}, // 100*10 = 1000, but capped at 500
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decls := NewDeclarations()

			consumer := decls.DeclareConsumer(&ConsumerOptions{
				Queue:     testQueue,
				Consumer:  testConsumer,
				EventType: testEventType,
				Workers:   tt.workers,
				// PrefetchCount not set - should auto-scale
			}, nil)

			assert.Equal(t, tt.expectedPrefetch, consumer.PrefetchCount,
				"PrefetchCount should be Workers*10 capped at 500")
		})
	}
}

func TestPrefetchCapping(t *testing.T) {
	decls := NewDeclarations()

	consumer := decls.DeclareConsumer(&ConsumerOptions{
		Queue:         testQueue,
		Consumer:      testConsumer,
		EventType:     testEventType,
		Workers:       10,
		PrefetchCount: 1500, // Exceeds cap of 1000
	}, nil)

	assert.Equal(t, 1000, consumer.PrefetchCount, "PrefetchCount should be capped at 1000")
}

func TestWorkerPoolGracefulShutdown(t *testing.T) {
	deliveries := make(chan amqp.Delivery, 5)
	mockClient := &simpleMockAMQPClient{
		deliveryChan: deliveries,
		isReady:      true,
	}
	registry := NewRegistry(mockClient, &stubLogger{})

	handler := &countingTestHandler{}
	consumer := &ConsumerDeclaration{
		Queue:         "shutdown-queue",
		Consumer:      "shutdown-consumer",
		EventType:     "shutdown-event",
		Handler:       handler,
		Workers:       3,
		PrefetchCount: 30,
		AutoAck:       false,
	}

	// Send 3 messages
	for i := 0; i < 3; i++ {
		acker := &mockAcknowledger{}
		deliveries <- amqp.Delivery{
			MessageId:    fmt.Sprintf(testMessageIDFmt, i),
			DeliveryTag:  uint64(i + 1),
			Headers:      amqp.Table{},
			Acknowledger: acker,
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Start processing in background
	done := make(chan struct{})
	go func() {
		registry.handleMessages(ctx, consumer, deliveries)
		close(done)
	}()

	// Give workers time to start
	time.Sleep(50 * time.Millisecond)

	// Cancel context to trigger shutdown
	cancel()

	// Wait for graceful shutdown (should complete within 1 second)
	select {
	case <-done:
		// Success - workers stopped gracefully
	case <-time.After(1 * time.Second):
		t.Fatal("Workers did not stop gracefully within timeout")
	}

	// Verify all 3 messages were processed before shutdown
	assert.Equal(t, 3, handler.CallCount(), "All messages should be processed before shutdown")
}

func TestWorkerResourceCaps(t *testing.T) {
	tests := []struct {
		name             string
		inputWorkers     int
		inputPrefetch    int
		expectedWorkers  int
		expectedPrefetch int
	}{
		{"Workers capped at 200", 250, 0, 200, 500},          // Workers capped, prefetch auto-scaled (capped at 500)
		{"PrefetchCount capped at 1000", 50, 1200, 50, 1000}, // Workers OK, prefetch explicitly capped
		{"Both within limits", 50, 300, 50, 300},             // No capping needed
		{"Workers at cap", 200, 0, 200, 500},                 // Workers at cap, prefetch auto-scaled (capped at 500)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decls := NewDeclarations()

			consumer := decls.DeclareConsumer(&ConsumerOptions{
				Queue:         testQueue,
				Consumer:      testConsumer,
				EventType:     testEventType,
				Workers:       tt.inputWorkers,
				PrefetchCount: tt.inputPrefetch,
			}, nil)

			assert.Equal(t, tt.expectedWorkers, consumer.Workers, "Workers should be capped at 200")
			assert.Equal(t, tt.expectedPrefetch, consumer.PrefetchCount, "PrefetchCount should be capped at 1000")
		})
	}
}

// BenchmarkSequentialVsConcurrent compares sequential vs concurrent message processing
func BenchmarkSequentialVsConcurrent(b *testing.B) {
	// Simulate slow handler (10ms processing time)
	slowHandler := &testHandler{retErr: nil}

	b.Run("Sequential (Workers=1)", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			deliveries := make(chan amqp.Delivery, 100)
			mockClient := &simpleMockAMQPClient{
				deliveryChan: deliveries,
				isReady:      true,
			}
			registry := NewRegistry(mockClient, &stubLogger{})

			consumer := &ConsumerDeclaration{
				Queue:         testBenchQueue,
				Consumer:      testBenchConsumer,
				EventType:     testBenchEvent,
				Handler:       slowHandler,
				Workers:       1, // Sequential
				PrefetchCount: 10,
				AutoAck:       true,
			}

			// Send 10 messages
			for j := 0; j < 10; j++ {
				deliveries <- amqp.Delivery{
					MessageId:   fmt.Sprintf(testMessageIDFmt, j),
					DeliveryTag: uint64(j + 1),
					Headers:     amqp.Table{},
				}
			}
			close(deliveries)

			ctx := context.Background()
			registry.handleMessages(ctx, consumer, deliveries)
		}
	})

	b.Run("Concurrent (Workers=4)", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			deliveries := make(chan amqp.Delivery, 100)
			mockClient := &simpleMockAMQPClient{
				deliveryChan: deliveries,
				isReady:      true,
			}
			registry := NewRegistry(mockClient, &stubLogger{})

			consumer := &ConsumerDeclaration{
				Queue:         testBenchQueue,
				Consumer:      testBenchConsumer,
				EventType:     testBenchEvent,
				Handler:       slowHandler,
				Workers:       4, // Concurrent
				PrefetchCount: 40,
				AutoAck:       true,
			}

			// Send 10 messages
			for j := 0; j < 10; j++ {
				deliveries <- amqp.Delivery{
					MessageId:   fmt.Sprintf(testMessageIDFmt, j),
					DeliveryTag: uint64(j + 1),
					Headers:     amqp.Table{},
				}
			}
			close(deliveries)

			ctx := context.Background()
			registry.handleMessages(ctx, consumer, deliveries)
		}
	})

	b.Run("HighConcurrent (Workers=8)", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			deliveries := make(chan amqp.Delivery, 100)
			mockClient := &simpleMockAMQPClient{
				deliveryChan: deliveries,
				isReady:      true,
			}
			registry := NewRegistry(mockClient, &stubLogger{})

			consumer := &ConsumerDeclaration{
				Queue:         testBenchQueue,
				Consumer:      testBenchConsumer,
				EventType:     testBenchEvent,
				Handler:       slowHandler,
				Workers:       8, // High concurrency
				PrefetchCount: 80,
				AutoAck:       true,
			}

			// Send 10 messages
			for j := 0; j < 10; j++ {
				deliveries <- amqp.Delivery{
					MessageId:   fmt.Sprintf(testMessageIDFmt, j),
					DeliveryTag: uint64(j + 1),
					Headers:     amqp.Table{},
				}
			}
			close(deliveries)

			ctx := context.Background()
			registry.handleMessages(ctx, consumer, deliveries)
		}
	})
}
