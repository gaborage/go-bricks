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

// Test constants for commonly used string literals
const (
	testExchangeName  = "test-exchange"
	testQueueName     = "test-queue"
	testKeyValue      = "test.key"
	testEventType     = "test-event"
	testExchange1Name = "test-exchange-1"
	testExchange2Name = "test-exchange-2"
	testQueue1Name    = "test-queue-1"
	testQueue2Name    = "test-queue-2"
	lateExchangeName  = "late-exchange"
	lateQueueName     = "late-queue"
	testKeyName       = "test-key"
	testValueContent  = "test-value"
	newKeyName        = "new-key"
	testMessageID     = "test-message-id"
	testRoutingKey    = "test.routing.key"
	testMessageBody   = "test message body"
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
	assert.NotNil(t, registry.consumers)
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

func TestRegistryGetPublishersSimple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	// Initially empty
	publishers := registry.GetPublishers()
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

	publishers = registry.GetPublishers()
	assert.Len(t, publishers, 2)

	// Verify data integrity (returned slice should be a copy)
	publishers[0] = nil
	assert.Len(t, registry.GetPublishers(), 2) // Original should be unchanged
}

func TestRegistryGetConsumersSimple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	// Initially empty
	consumers := registry.GetConsumers()
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

	consumers = registry.GetConsumers()
	assert.Len(t, consumers, 2)

	// Verify data integrity (returned slice should be a copy)
	consumers[0] = nil
	assert.Len(t, registry.GetConsumers(), 2) // Original should be unchanged
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

func TestRegistryGetExchanges(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	// Initially empty
	exchanges := registry.GetExchanges()
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

	exchanges = registry.GetExchanges()
	assert.Len(t, exchanges, 2)
	assert.Equal(t, ex1, exchanges[testExchange1Name])
	assert.Equal(t, ex2, exchanges[testExchange2Name])

	// Verify data integrity (returned map should be a copy)
	exchanges[testExchange1Name] = nil
	originalExchanges := registry.GetExchanges()
	assert.Len(t, originalExchanges, 2)
	assert.NotNil(t, originalExchanges[testExchange1Name])
}

func TestRegistryGetQueues(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	// Initially empty
	queues := registry.GetQueues()
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

	queues = registry.GetQueues()
	assert.Len(t, queues, 2)
	assert.Equal(t, q1, queues[testQueue1Name])
	assert.Equal(t, q2, queues[testQueue2Name])

	// Verify data integrity (returned map should be a copy)
	queues[testQueue1Name] = nil
	originalQueues := registry.GetQueues()
	assert.Len(t, originalQueues, 2)
	assert.NotNil(t, originalQueues[testQueue1Name])
}

func TestRegistryGetBindings(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	// Initially empty
	bindings := registry.GetBindings()
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

	bindings = registry.GetBindings()
	assert.Len(t, bindings, 2)
	assert.Equal(t, b1, bindings[0])
	assert.Equal(t, b2, bindings[1])

	// Verify data integrity (returned slice should be a copy)
	bindings[0] = nil
	originalBindings := registry.GetBindings()
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
		if handler.GetCallCount() >= 1 {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}

	// Verify handler was called
	assert.Equal(t, 1, handler.GetCallCount())

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

func (h *countingTestHandler) GetCallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.callCount
}

// mockAcknowledger for testing message acknowledgment
type mockAcknowledger struct {
	ackCalled  bool
	nackCalled bool
	ackErr     error
	nackErr    error
}

func (m *mockAcknowledger) Ack(_ uint64, _ bool) error {
	m.ackCalled = true
	return m.ackErr
}

func (m *mockAcknowledger) Nack(_ uint64, _, _ bool) error {
	m.nackCalled = true
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
	assert.Equal(t, 1, handler.GetCallCount())

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
	assert.Equal(t, 1, handler.GetCallCount())

	// Verify message was negatively acknowledged (nacked)
	assert.False(t, acker.ackCalled)
	assert.True(t, acker.nackCalled)
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
	assert.Equal(t, 1, handler.GetCallCount())

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
	assert.Equal(t, 1, handler.GetCallCount())

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
	assert.Equal(t, 1, handler.GetCallCount())

	// Verify nack was attempted (even though it failed)
	assert.False(t, acker.ackCalled)
	assert.True(t, acker.nackCalled)
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

	publishers := registry.GetPublishers()
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

	consumers := registry.GetConsumers()
	assert.Len(t, consumers, 1)
}
