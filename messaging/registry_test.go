package messaging

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	semconv "go.opentelemetry.io/otel/semconv/v1.32.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gaborage/go-bricks/internal/testutil"
	gobrickslogger "github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/tracking"
	"github.com/gaborage/go-bricks/multitenant"
	obtest "github.com/gaborage/go-bricks/observability/testing"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
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
	verifiedExchanges []string
	bindings          []string

	// Track args received by each declare/bind call, keyed by name
	// (queue/exchange name, or "queue:exchange:routingKey" for bindings).
	queueArgs    map[string]map[string]any
	exchangeArgs map[string]map[string]any
	bindingArgs  map[string]map[string]any

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

func (m *simpleMockAMQPClient) publishBytes(_ context.Context, _ publishOptions, _ []byte) error {
	return nil
}

func (m *simpleMockAMQPClient) Consume(_ context.Context, _ string) (<-chan amqp.Delivery, error) {
	return m.deliveryChan, m.consumeErr
}

func (m *simpleMockAMQPClient) ConsumeFromQueue(_ context.Context, _ ConsumeOptions) (<-chan amqp.Delivery, error) {
	return m.deliveryChan, m.consumeErr
}

func (m *simpleMockAMQPClient) DeclareQueue(_ context.Context, queue *QueueDeclaration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.declareQueueErr != nil {
		return m.declareQueueErr
	}
	m.declaredQueues = append(m.declaredQueues, queue.Name)
	if m.queueArgs == nil {
		m.queueArgs = make(map[string]map[string]any)
	}
	m.queueArgs[queue.Name] = queue.Args
	return nil
}

func (m *simpleMockAMQPClient) DeclareExchange(_ context.Context, exchange *ExchangeDeclaration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.declareExchangeErr != nil {
		return m.declareExchangeErr
	}
	// The real client branches on Passive; so does the fake, or a test could not
	// tell a verified external exchange from one this service created.
	if exchange.Passive {
		m.verifiedExchanges = append(m.verifiedExchanges, exchange.Name)
		return nil
	}
	m.declaredExchanges = append(m.declaredExchanges, exchange.Name)
	if m.exchangeArgs == nil {
		m.exchangeArgs = make(map[string]map[string]any)
	}
	m.exchangeArgs[exchange.Name] = exchange.Args
	return nil
}

func (m *simpleMockAMQPClient) BindQueue(_ context.Context, binding *BindingDeclaration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bindQueueErr != nil {
		return m.bindQueueErr
	}
	bindingKey := binding.Queue + ":" + binding.Exchange + ":" + binding.RoutingKey
	m.bindings = append(m.bindings, bindingKey)
	if m.bindingArgs == nil {
		m.bindingArgs = make(map[string]map[string]any)
	}
	m.bindingArgs[bindingKey] = binding.Args
	return nil
}

// queueArgsFor, exchangeArgsFor, and bindingArgsFor return the args captured
// for a declare/bind call, for test assertions. Binding keys use the
// "queue:exchange:routingKey" form.
func (m *simpleMockAMQPClient) queueArgsFor(queueName string) map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.queueArgs[queueName]
}

func (m *simpleMockAMQPClient) exchangeArgsFor(exchangeName string) map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.exchangeArgs[exchangeName]
}

func (m *simpleMockAMQPClient) bindingArgsFor(bindingKey string) map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.bindingArgs[bindingKey]
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

	return client, readySignal
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
		Type: ExchangeTypeTopic,
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

	require.NoError(t, err)
	assert.True(t, registry.declared)
	assert.Contains(t, client.declaredExchanges, testExchangeName)
	assert.Contains(t, client.declaredQueues, testQueueName)
	assert.Contains(t, client.bindings, "test-queue:test-exchange:test.key")
}

// TestRegistryDeclareInfrastructurePassesArgs guards the replay path in
// DeclareInfrastructure: it must forward each declaration's Args to the broker
// exactly as registered, at all three call sites (exchange, queue, binding).
// Exact-map assertions mean this fails if any of the three registry call sites
// ever drops Args again. Args are set on the declarations BEFORE registering —
// the pattern documented for users (see wiki/messaging.md).
func TestRegistryDeclareInfrastructurePassesArgs(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: true}
	registry := NewRegistry(client, &stubLogger{})

	ex := NewTopicExchange("args.exchange")
	ex.Args["alternate-exchange"] = "orders.alt"
	registry.RegisterExchange(ex)

	q := NewQueue("args.queue")
	q.Args["x-dead-letter-exchange"] = "orders.dlx"
	registry.RegisterQueue(q)

	b := NewBinding("args.queue", "args.exchange", "orders.*")
	b.Args["x-match"] = "all"
	registry.RegisterBinding(b)

	err := registry.DeclareInfrastructure(t.Context())

	require.NoError(t, err)
	assert.Equal(t, map[string]any{"alternate-exchange": "orders.alt"}, client.exchangeArgsFor("args.exchange"))
	assert.Equal(t, map[string]any{"x-dead-letter-exchange": "orders.dlx"}, client.queueArgsFor("args.queue"))
	assert.Equal(t, map[string]any{"x-match": "all"}, client.bindingArgsFor("args.queue:args.exchange:orders.*"))
}

func TestRegistryDeclareInfrastructureClientNotReadyTimeoutSimple(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: false}
	registry := NewRegistry(client, &stubLogger{})

	// Use a longer timeout to avoid flakes, but still test the timeout behavior
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := registry.DeclareInfrastructure(ctx)

	require.Error(t, err)
	// Could be either timeout or context canceled depending on timing
	assert.True(t,
		strings.Contains(err.Error(), "timeout waiting for AMQP client") ||
			strings.Contains(err.Error(), "context canceled while waiting for AMQP client"),
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
		Type: ExchangeTypeTopic,
	})

	err := registry.DeclareInfrastructure(t.Context())

	require.Error(t, err)
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

	require.NoError(t, err)
	assert.True(t, registry.consumersActive)
	assert.NotNil(t, registry.cancelConsumers)

	// Clean up
	registry.StopConsumers()
}

func TestRegistryStartConsumersClientNotReadySimple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{isReady: false}, &stubLogger{})

	err := registry.StartConsumers(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "AMQP client is not ready")
}

func TestRegistryStopConsumersSuccessSimple(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{isReady: true}, &stubLogger{})

	// Simulate active consumers
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Ensure cleanup even if StopConsumers doesn't call it
	registry.consumersActive = true
	registry.cancelConsumers = cancel

	registry.StopConsumers()

	assert.False(t, registry.consumersActive)
	assert.Nil(t, registry.cancelConsumers)

	// Verify context was canceled
	select {
	case <-ctx.Done():
		// Expected - context should be canceled
	default:
		t.Fatal("Expected context to be canceled")
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
	err := registry.DeclareInfrastructure(t.Context())
	require.NoError(t, err)

	// Now try to register new components (should log warnings but not fail)
	registry.RegisterExchange(&ExchangeDeclaration{
		Name: lateExchangeName,
		Type: ExchangeTypeTopic,
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

// TestRegistryDeclareInfrastructureVerifiesAnExternalExchange pins the startup
// pass: an external exchange reaches the client as a passive declaration and is
// never created by this service (ADR-119).
func TestRegistryDeclareInfrastructureVerifiesAnExternalExchange(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: true}
	registry := NewRegistry(client, &stubLogger{})
	registry.RegisterExchange(NewExternalExchange(testExternalExchange))
	registry.RegisterExchange(&ExchangeDeclaration{Name: testExchangeName, Type: ExchangeTypeTopic, Durable: true})

	require.NoError(t, registry.DeclareInfrastructure(context.Background()))

	assert.Equal(t, []string{testExternalExchange}, client.verifiedExchanges)
	assert.Equal(t, []string{testExchangeName}, client.declaredExchanges)
}

func TestRegistryDeclareInfrastructureAlreadyDeclaredSimple(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: true}
	registry := NewRegistry(client, &stubLogger{})

	// First declaration
	err := registry.DeclareInfrastructure(t.Context())
	require.NoError(t, err)
	assert.True(t, registry.declared)

	// Second declaration should be no-op
	err = registry.DeclareInfrastructure(context.Background())
	assert.NoError(t, err)
}

func TestRegistryDeclareInfrastructureNilClientSimple(t *testing.T) {
	registry := NewRegistry(nil, &stubLogger{})

	err := registry.DeclareInfrastructure(t.Context())

	require.Error(t, err)
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

	require.Error(t, err)
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

	require.NoError(t, err)
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
		Type:    ExchangeTypeTopic,
		Durable: true,
	}
	ex2 := &ExchangeDeclaration{
		Name:       testExchange2Name,
		Type:       ExchangeTypeDirect,
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

	err := registry.DeclareInfrastructure(t.Context())

	require.Error(t, err)
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

	err := registry.DeclareInfrastructure(t.Context())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to bind queue test-queue to exchange test-exchange")
}

func TestRegistryDeclareInfrastructureContextCancellation(t *testing.T) {
	client := &simpleMockAMQPClient{isReady: false}
	registry := NewRegistry(client, &stubLogger{})

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the context immediately
	cancel()

	err := registry.DeclareInfrastructure(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled while waiting for AMQP client")
}

func TestRegistryDeclareInfrastructureClientBecomesReady(t *testing.T) {
	client, readySignal := newControllableMockClient()
	registry := NewRegistry(client, &stubLogger{})

	registry.RegisterExchange(&ExchangeDeclaration{
		Name: testExchangeName,
		Type: ExchangeTypeTopic,
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
		require.NoError(t, err)
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
	handlerDone := make(chan bool, 1)
	handlerStarted := make(chan struct{})
	go func() {
		close(handlerStarted) // Signal handler is about to start
		handlerDone <- registry.handleMessages(ctx, consumer, deliveries, nil)
	}()

	// Wait for handler to start, then cancel context immediately
	<-handlerStarted
	cancel()

	// Handler should stop and report "no re-subscribe" (shutdown, not a flap).
	select {
	case reconnect := <-handlerDone:
		assert.False(t, reconnect, "context cancellation must not signal re-subscribe")
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
	handlerDone := make(chan bool, 1)
	handlerStarted := make(chan struct{})
	go func() {
		close(handlerStarted) // Signal handler is about to start
		handlerDone <- registry.handleMessages(ctx, consumer, deliveries, nil)
	}()

	// Wait for handler to start, then close delivery channel immediately
	<-handlerStarted
	close(deliveries)

	// Handler should stop and report "re-subscribe" (the broker closed the
	// delivery channel; this is a flap, not a shutdown).
	select {
	case reconnect := <-handlerDone:
		assert.True(t, reconnect, "delivery-channel close must signal re-subscribe")
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
		registry.handleMessages(ctx, consumer, deliveries, nil)
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
	ackCount     int
	nackCount    int
	ackErr       error
	nackErr      error
	nackMultiple bool
	nackRequeue  bool
	mu           sync.Mutex
}

func (m *mockAcknowledger) Ack(_ uint64, _ bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ackCalled = true
	m.ackCount++
	return m.ackErr
}

func (m *mockAcknowledger) Nack(_ uint64, multiple, requeue bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nackCalled = true
	m.nackCount++
	m.nackMultiple = multiple
	m.nackRequeue = requeue
	return m.nackErr
}

func (m *mockAcknowledger) Reject(_ uint64, _ bool) error {
	return nil
}

// AckCalled is a thread-safe read of ackCalled, for tests that poll from a
// goroutine other than the one calling Ack (e.g. require.Eventually against
// an asynchronous worker).
func (m *mockAcknowledger) AckCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ackCalled
}

// panicEventLogger fails every post-handler log call, so the first outcome log
// inside the pipeline panics deterministically.
type panicEventLogger struct{ stubLogger }

func (l *panicEventLogger) Info() gobrickslogger.LogEvent { panic("log sink gone") }

func (l *panicEventLogger) Error() gobrickslogger.LogEvent { panic("log sink gone") }

func (l *panicEventLogger) Warn() gobrickslogger.LogEvent                     { panic("log sink gone") }
func (l *panicEventLogger) WithContext(_ any) gobrickslogger.Logger           { return l }
func (l *panicEventLogger) WithFields(_ map[string]any) gobrickslogger.Logger { return l }

// TestRegistryProcessMessagePanicInTheTailNacksWithoutRequeue pins the lane-level
// recovery: a panic past the handler — here from outcome logging — must not
// escape processMessage, and the manual-ack delivery is nacked without requeue.
// The panic-in-the-tail guarantee moved to the pipeline with ADR-069; see
// TestRunSettlesEvenWhenTheDeliveryTailPanics. What stays here is the lane's own
// half: that its Settle closure nacks without requeue on a Panicked result.
func TestRegistryProcessMessagePanicInTheTailNacksWithoutRequeue(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})
	consumer := &ConsumerDeclaration{
		Queue: testQueueName, EventType: testEventType,
		Handler: &countingTestHandler{}, AutoAck: false,
	}
	acker := &mockAcknowledger{}
	delivery := &amqp.Delivery{
		DeliveryTag: 123, Body: []byte(testMessageBody), Acknowledger: acker,
	}

	require.NotPanics(t, func() {
		registry.processMessage(context.Background(), consumer, delivery, &panicEventLogger{})
	})

	assert.Equal(t, 1, acker.nackCount, "the delivery is settled exactly once")
	assert.Equal(t, 0, acker.ackCount, "a delivery whose tail panicked is never acked")
	assert.False(t, acker.nackRequeue, "the nack must not requeue")
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

// tenantRecordingHandler records the tenant its context carried, so a test can
// assert what the delivery seeded rather than what the stamp said.
type tenantRecordingHandler struct {
	mu     sync.Mutex
	calls  int
	tenant string
	err    error
}

func (h *tenantRecordingHandler) Handle(ctx context.Context, _ *amqp.Delivery) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	h.tenant, _ = multitenant.GetTenant(ctx)
	return h.err
}

func (h *tenantRecordingHandler) EventType() string { return testEventType }

func (h *tenantRecordingHandler) seen() (calls int, tenant string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls, h.tenant
}

// TestRegistryProcessMessageTenantStamp is the classic lane's pass-through case:
// the lane hands the pipeline its two tenancy fields and settles what comes back.
// The stamp rules themselves — precedence, the error text, fail-closed, the
// TenantOptional carve-out — belong to the pipeline both lanes share and are
// tabled in messaging/internal/delivery's own tests.
func TestRegistryProcessMessageTenantStamp(t *testing.T) {
	const acme = "acme"

	t.Run("stamped_delivery_seeds_the_handler", func(t *testing.T) {
		registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})
		registry.tenantStamps = true
		handler := &tenantRecordingHandler{}
		acker := &mockAcknowledger{}

		registry.processMessage(context.Background(),
			&ConsumerDeclaration{Queue: testQueueName, EventType: testEventType, Handler: handler},
			&amqp.Delivery{
				MessageId:    testMessageID,
				Body:         []byte(testMessageBody),
				Headers:      amqp.Table{TenantStampHeader: acme},
				Acknowledger: acker,
			}, &stubLogger{})

		calls, tenant := handler.seen()
		assert.Equal(t, 1, calls)
		assert.Equal(t, acme, tenant)
		assert.True(t, acker.AckCalled())
	})

	// The lane's own contribution: a refusal from the pipeline settles as this
	// lane settles any failure — nacked, never requeued, so a bad stamp cannot
	// loop forever.
	t.Run("unusable_stamp_nacks_without_requeue", func(t *testing.T) {
		registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})
		registry.tenantStamps = true
		handler := &tenantRecordingHandler{}
		acker := &mockAcknowledger{}

		registry.processMessage(context.Background(),
			&ConsumerDeclaration{Queue: testQueueName, EventType: testEventType, Handler: handler},
			&amqp.Delivery{
				MessageId:    testMessageID,
				Body:         []byte(testMessageBody),
				Headers:      amqp.Table{},
				Acknowledger: acker,
			}, &stubLogger{})

		calls, _ := handler.seen()
		assert.Zero(t, calls)

		acker.mu.Lock()
		defer acker.mu.Unlock()
		assert.True(t, acker.nackCalled)
		assert.False(t, acker.nackRequeue)
	})

	// TenantOptional reaches the pipeline from the consumer declaration, not from
	// the registry — this is the wiring, not the rule.
	t.Run("tenant_optional_reaches_the_pipeline", func(t *testing.T) {
		registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})
		registry.tenantStamps = true
		handler := &tenantRecordingHandler{}
		acker := &mockAcknowledger{}

		registry.processMessage(context.Background(),
			&ConsumerDeclaration{
				Queue: testQueueName, EventType: testEventType,
				Handler: handler, TenantOptional: true,
			},
			&amqp.Delivery{
				MessageId:    testMessageID,
				Body:         []byte(testMessageBody),
				Headers:      amqp.Table{},
				Acknowledger: acker,
			}, &stubLogger{})

		calls, tenant := handler.seen()
		assert.Equal(t, 1, calls)
		assert.Empty(t, tenant)
		assert.True(t, acker.AckCalled())
	})
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
	registry.handleMessages(ctx, panicConsumer, deliveries, nil)

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
		registry.handleMessages(ctx, consumer1, deliveries1, nil)
	}()

	go func() {
		defer wg.Done()
		registry.handleMessages(ctx, consumer2, deliveries2, nil)
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
	require.NoError(t, err)
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
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start consumer for queue failing-queue")
	assert.False(t, registry.consumersActive)
}

// multipleMockAMQPClient extends simpleMockAMQPClient for testing multiple consumers
type multipleMockAMQPClient struct {
	simpleMockAMQPClient
	queues        map[string]chan amqp.Delivery
	consumeErrors map[string]error
}

func (m *multipleMockAMQPClient) ConsumeFromQueue(ctx context.Context, opts ConsumeOptions) (<-chan amqp.Delivery, error) {
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

	// Default behavior - propagate context for proper cancellation/deadline handling
	return m.simpleMockAMQPClient.ConsumeFromQueue(ctx, opts)
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
	require.NoError(t, err)
	assert.True(t, registry.consumersActive)

	// Start consumers second time - should be no-op
	err = registry.StartConsumers(context.Background())
	require.NoError(t, err)
	assert.True(t, registry.consumersActive)

	// Clean up
	registry.StopConsumers()
}

func TestRegistryStartConsumersNilClient(t *testing.T) {
	registry := NewRegistry(nil, &stubLogger{})

	err := registry.StartConsumers(context.Background())
	require.Error(t, err)
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
	require.NoError(t, err)
	assert.True(t, registry.consumersActive)

	// Clean up
	registry.StopConsumers()
}

func TestRegistryRegisterPublisherNeverBlocked(t *testing.T) {
	registry := NewRegistry(&simpleMockAMQPClient{isReady: true}, &stubLogger{})

	// Publishers can be registered even after declaration (unlike exchanges/queues/bindings)
	err := registry.DeclareInfrastructure(t.Context())
	require.NoError(t, err)

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
	err := registry.DeclareInfrastructure(t.Context())
	require.NoError(t, err)

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
	registry.handleMessages(ctx, consumer, deliveries, nil)

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
		registry.handleMessages(ctx, consumer, deliveries, nil)
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
			registry.handleMessages(ctx, consumer, deliveries, nil)
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
			registry.handleMessages(ctx, consumer, deliveries, nil)
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
			registry.handleMessages(ctx, consumer, deliveries, nil)
		}
	})
}

// ===== Consumer Re-subscribe After Reconnect Tests =====

// consumeResult is one scripted return value for resubscribingMockClient.
type consumeResult struct {
	ch  <-chan amqp.Delivery
	err error
}

// resubscribingMockClient hands out scripted ConsumeFromQueue results on
// successive calls, so a test can simulate an AMQP flap by closing the active
// delivery channel and assert the registry re-subscribes onto the next one.
// Once the script is exhausted it returns exhaustedErr (keeping the supervisor
// in the retry/backoff loop) or, if that is nil, a never-closing open channel.
type resubscribingMockClient struct {
	*simpleMockAMQPClient
	callMu       sync.Mutex
	results      []consumeResult
	calls        int
	optsSeen     []ConsumeOptions
	exhaustedErr error
	// failing simulates a broker outage the test controls at runtime: while set,
	// every ConsumeFromQueue fails without returning a scripted result — the call is
	// still counted, so it advances past one — and a test can observe an unrecovered
	// consumer for as long as it needs to. Guarded by callMu.
	failing bool
}

var _ AMQPClient = (*resubscribingMockClient)(nil)

func (m *resubscribingMockClient) ConsumeFromQueue(_ context.Context, opts ConsumeOptions) (<-chan amqp.Delivery, error) {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	idx := m.calls
	m.calls++
	m.optsSeen = append(m.optsSeen, opts)
	if m.failing {
		return nil, errNotConnected
	}
	if idx < len(m.results) {
		return m.results[idx].ch, m.results[idx].err
	}
	if m.exhaustedErr != nil {
		return nil, m.exhaustedErr // keep the supervisor in the retry/backoff loop
	}
	return make(chan amqp.Delivery), nil // park: open channel that never closes
}

// setFailing opens or closes a broker outage the supervisor cannot script its way out of.
func (m *resubscribingMockClient) setFailing(failing bool) {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	m.failing = failing
}

func (m *resubscribingMockClient) consumeCallCount() int {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	return m.calls
}

// consumeOptionsAt returns the ConsumeOptions of the i-th ConsumeFromQueue call.
func (m *resubscribingMockClient) consumeOptionsAt(i int) ConsumeOptions {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	return m.optsSeen[i]
}

// TestRegistryConsumerResubscribesAfterDeliveryChannelCloses is the regression
// test for the consumer-not-resubscribing-after-reconnect bug: when the broker
// closes the active delivery channel (a connection/channel flap), the registry
// must acquire a fresh subscription instead of leaving the queue with zero
// consumers until a process restart.
func TestRegistryConsumerResubscribesAfterDeliveryChannelCloses(t *testing.T) {
	ch1 := make(chan amqp.Delivery)
	ch2 := make(chan amqp.Delivery, 1) // buffered so the post-reconnect send never blocks
	client := &resubscribingMockClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		results:              []consumeResult{{ch: ch1}, {ch: ch2}},
	}
	registry := NewRegistry(client, &stubLogger{})
	registry.resubscribeDelay = 5 * time.Millisecond // fast re-subscribe for the test

	handler := &countingTestHandler{}
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Workers:   1,
		Handler:   handler,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, registry.StartConsumers(ctx))

	// Simulate an AMQP flap: the broker closes the active delivery channel.
	close(ch1)

	// The consumer must re-subscribe (acquire ch2) without a process restart.
	require.Eventually(t, func() bool {
		return client.consumeCallCount() >= 2
	}, time.Second, 2*time.Millisecond, "consumer did not re-subscribe after delivery channel close")

	// Prove the new subscription is live: a delivery on ch2 is processed.
	deliverAndAwaitAck(t, ch2, handler)

	registry.StopConsumers()
}

// TestRegistryConsumerResubscribeRetriesUntilClientReady verifies the
// re-subscribe loop backs off and retries while the AMQP client is still
// re-establishing its connection (ConsumeFromQueue returns errNotConnected),
// then succeeds once the client is ready again.
func TestRegistryConsumerResubscribeRetriesUntilClientReady(t *testing.T) {
	ch1 := make(chan amqp.Delivery)
	ch2 := make(chan amqp.Delivery, 1)
	client := &resubscribingMockClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		results: []consumeResult{
			{ch: ch1},              // initial subscription
			{err: errNotConnected}, // client still reconnecting
			{err: errNotConnected}, // still reconnecting
			{ch: ch2},              // client ready again
		},
	}
	log := newRecordingLogger()
	registry := NewRegistry(client, log)
	registry.resubscribeDelay = 5 * time.Millisecond

	handler := &countingTestHandler{}
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Workers:   1,
		Handler:   handler,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, registry.StartConsumers(ctx))

	close(ch1) // flap

	// Must keep retrying through the two errNotConnected results to the success.
	require.Eventually(t, func() bool {
		return client.consumeCallCount() >= 4
	}, 2*time.Second, 2*time.Millisecond, "consumer did not retry re-subscribe until client ready")

	deliverAndAwaitAck(t, ch2, handler)

	registry.StopConsumers()

	// errNotConnected is not an *amqp.Error, so its failure lines carry no broker reply.
	var failures int
	for _, ln := range log.Lines() {
		if ln.Msg == "Consumer re-subscribe attempt failed, will retry" {
			failures++
			assert.Empty(t, ln.Values("amqp_reply_code"))
			assert.Empty(t, ln.Values("amqp_reply_text"))
		}
	}
	assert.Equal(t, 2, failures)
}

// TestRegistryConsumerSupervisorStopsOnContextCancel verifies the consumer
// supervisor exits cleanly when StopConsumers cancels the context mid-backoff,
// and does not keep re-subscribing afterwards (no zombie goroutine).
func TestRegistryConsumerSupervisorStopsOnContextCancel(t *testing.T) {
	ch1 := make(chan amqp.Delivery)
	client := &resubscribingMockClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		results:              []consumeResult{{ch: ch1}},
		// After the first session every re-subscribe attempt fails, so the
		// supervisor stays in the retry/backoff loop (rather than parking on a
		// success channel) and we can cancel while it is actively retrying.
		exhaustedErr: errNotConnected,
	}
	registry := NewRegistry(client, &stubLogger{})
	registry.resubscribeDelay = 5 * time.Millisecond

	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Workers:   1,
		Handler:   &countingTestHandler{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, registry.StartConsumers(ctx))

	// Flap, then wait until the supervisor is actively retrying (it has called
	// ConsumeFromQueue again and is failing into backoff) before canceling, so
	// this exercises cancellation from inside the retry/backoff path.
	close(ch1)
	require.Eventually(t, func() bool {
		return client.consumeCallCount() >= 2
	}, time.Second, 2*time.Millisecond, "supervisor did not enter the re-subscribe retry loop")

	registry.StopConsumers() // cancel while the supervisor is retrying

	// Cancellation interrupts resubscribe's backoff select immediately (it
	// selects on ctx.Done alongside the backoff timer), so the supervisor exits
	// after at most one already-dispatched ConsumeFromQueue rather than waiting
	// out a backoff. Let that in-flight call settle, then require the count to
	// stay frozen across a window much larger than the re-subscribe backoff: a
	// still-alive supervisor would keep incrementing it. This avoids a
	// re-polling check that could land inside a single jittered backoff gap.
	time.Sleep(10 * registry.resubscribeDelay)
	settled := client.consumeCallCount()
	time.Sleep(20 * registry.resubscribeDelay)
	assert.Equal(t, settled, client.consumeCallCount(),
		"supervisor kept re-subscribing after StopConsumers")
}

// TestRegistryConsumerResubscribeEscalatesToWarnFromFifthFailure verifies a
// permanently failing re-subscribe becomes visible: the first four consecutive
// failures stay at Debug, the fifth logs at WARN, and every failure carries the
// broker's reply code and text.
func TestRegistryConsumerResubscribeEscalatesToWarnFromFifthFailure(t *testing.T) {
	const (
		failedMsg       = "Consumer re-subscribe attempt failed, will retry"
		resubscribedMsg = "Consumer re-subscribed after delivery channel closed"
	)
	ch1 := make(chan amqp.Delivery)
	notFound := &amqp.Error{Code: amqp.NotFound, Reason: "NOT_FOUND - no queue 'test-queue'", Server: true}
	client := &resubscribingMockClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		results: []consumeResult{
			{ch: ch1},
			{err: notFound},
			{err: notFound},
			{err: notFound},
			{err: notFound},
			{err: notFound},
			{ch: make(chan amqp.Delivery)},
		},
	}
	log := newRecordingLogger()
	registry := NewRegistry(client, log)
	registry.resubscribeDelay = time.Millisecond

	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Workers:   1,
		Handler:   &countingTestHandler{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, registry.StartConsumers(ctx))

	close(ch1)
	require.Eventually(t, func() bool {
		return slices.ContainsFunc(log.Lines(), func(ln recordedLine) bool { return ln.Msg == resubscribedMsg })
	}, 5*time.Second, 2*time.Millisecond, "consumer did not re-subscribe after the failure streak")
	registry.StopConsumers()

	var levels []string
	for _, ln := range log.Lines() {
		if ln.Msg != failedMsg {
			continue
		}
		levels = append(levels, ln.Level)
		assert.Equal(t, []string{strconv.Itoa(notFound.Code)}, ln.Values("amqp_reply_code"))
		assert.Equal(t, []string{notFound.Reason}, ln.Values("amqp_reply_text"))
	}
	debug, warn := gobrickslogger.LevelDebug, gobrickslogger.LevelWarn
	assert.Equal(t, []string{debug, debug, debug, debug, warn}, levels)

	success := log.Line(t, resubscribedMsg)
	assert.Equal(t, gobrickslogger.LevelInfo, success.Level)
	assert.Equal(t, []string{"6"}, success.Values("attempt"))
}

// ===== Per-Consumer Subscription State Tests =====

// TestRegistryConsumerStatesTrackSubscriptionAndResubscribe pins the per-consumer
// snapshot across a flap: zero-valued before startup, subscribed once the first
// subscription is established, and counted plus timestamped after the supervisor
// re-subscribes onto a fresh delivery channel.
func TestRegistryConsumerStatesTrackSubscriptionAndResubscribe(t *testing.T) {
	ch1 := make(chan amqp.Delivery)
	ch2 := make(chan amqp.Delivery, 1) // buffered so the post-reconnect send never blocks
	client := &resubscribingMockClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		results:              []consumeResult{{ch: ch1}, {ch: ch2}},
	}
	registry := NewRegistry(client, &stubLogger{})
	registry.resubscribeDelay = 5 * time.Millisecond

	handler := &countingTestHandler{}
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Workers:   1,
		Handler:   handler,
	})

	before := registry.ConsumerStates()
	require.Len(t, before, 1)
	assert.Equal(t, testQueueName, before[0].Queue)
	assert.False(t, before[0].Subscribed, "a declared consumer is unsubscribed until StartConsumers")
	assert.Zero(t, before[0].Resubscribes)
	assert.True(t, before[0].LastResubscribeAt.IsZero())
	assert.Zero(t, before[0].FailStreak)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, registry.StartConsumers(ctx))

	started := registry.ConsumerStates()
	require.Len(t, started, 1)
	assert.True(t, started[0].Subscribed, "the first subscription is established synchronously")
	assert.Zero(t, started[0].Resubscribes, "the first subscription is not a re-subscribe")
	assert.True(t, started[0].LastResubscribeAt.IsZero())

	// Simulate an AMQP flap: the broker closes the active delivery channel. The
	// ack proves the supervisor is live on ch2, so the state write has landed.
	close(ch1)
	deliverAndAwaitAck(t, ch2, handler)

	after := registry.ConsumerStates()
	require.Len(t, after, 1)
	assert.True(t, after[0].Subscribed)
	assert.Equal(t, uint64(1), after[0].Resubscribes)
	assert.False(t, after[0].LastResubscribeAt.IsZero(), "a successful re-subscribe stamps the time")
	assert.Zero(t, after[0].FailStreak)

	registry.StopConsumers()
}

// TestConsumerStateGivenUp pins the predicate: a consumer counts as abandoned only
// while it is unsubscribed AND its consecutive failures have reached the same
// threshold that escalates the re-subscribe log.
func TestConsumerStateGivenUp(t *testing.T) {
	tests := []struct {
		name  string
		state ConsumerState
		want  bool
	}{
		{"never_started", ConsumerState{}, false},
		{"unsubscribed_below_threshold", ConsumerState{FailStreak: consumerResubscribeWarnFromAttempt - 1}, false},
		{"unsubscribed_at_threshold", ConsumerState{FailStreak: consumerResubscribeWarnFromAttempt}, true},
		{"unsubscribed_past_threshold", ConsumerState{FailStreak: consumerResubscribeWarnFromAttempt + 1}, true},
		{"subscribed_with_stale_streak", ConsumerState{Subscribed: true, FailStreak: consumerResubscribeWarnFromAttempt + 1}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.state.GivenUp())
		})
	}
}

// TestRegistryConsumerStatesCountFailedResubscribeAttempts drives a consumer
// through a broker outage: each failed re-subscribe attempt lengthens the streak
// until the snapshot reports GivenUp, and a success clears it.
func TestRegistryConsumerStatesCountFailedResubscribeAttempts(t *testing.T) {
	client, ch1 := newOutageClient()
	registry, _ := startStateRegistry(t, client, &stubLogger{})

	client.setFailing(true) // the outage lasts until the test lifts it
	close(ch1)              // flap into an outage the supervisor cannot recover from

	require.Eventually(t, func() bool {
		state := registry.ConsumerStates()[0]
		return !state.Subscribed && state.FailStreak >= consumerResubscribeWarnFromAttempt
	}, 5*time.Second, 2*time.Millisecond, "failure streak did not reach the threshold")
	assert.True(t, registry.ConsumerStates()[0].GivenUp())

	client.setFailing(false) // broker back

	require.Eventually(t, func() bool {
		return registry.ConsumerStates()[0].Subscribed
	}, 5*time.Second, 2*time.Millisecond, "consumer did not re-subscribe once the outage lifted")
	recovered := registry.ConsumerStates()[0]
	assert.Zero(t, recovered.FailStreak, "a successful re-subscribe resets the streak")
	assert.False(t, recovered.GivenUp())
	assert.Equal(t, uint64(1), recovered.Resubscribes)

	registry.StopConsumers()

	stopped := registry.ConsumerStates()[0]
	assert.False(t, stopped.Subscribed)
	assert.Equal(t, uint64(1), stopped.Resubscribes, "cumulative counters survive a stop")
	assert.False(t, stopped.LastResubscribeAt.IsZero(), "the last re-subscribe time survives a stop")
}

// TestRegistryConsumerStatesMaskGivenUpAfterStopConsumers pins that shutdown is
// not an outage: a stopped registry has no supervisor left to re-subscribe, so
// its consumers report unsubscribed with no streak rather than abandoned.
func TestRegistryConsumerStatesMaskGivenUpAfterStopConsumers(t *testing.T) {
	client, ch1 := newOutageClient()
	registry, _ := startStateRegistry(t, client, &stubLogger{})

	client.setFailing(true)
	close(ch1)
	require.Eventually(t, func() bool {
		return registry.ConsumerStates()[0].GivenUp()
	}, 5*time.Second, 2*time.Millisecond, "failure streak did not reach the threshold")

	registry.StopConsumers()

	stopped := registry.ConsumerStates()[0]
	assert.False(t, stopped.Subscribed)
	assert.Zero(t, stopped.FailStreak)
	assert.False(t, stopped.GivenUp(), "a stopped registry has no consumer that gave up")
}

// TestRegistrySetResubscribeDelayIgnoresANonPositiveDelay pins the guard's boundary: the
// manager passes ManagerOptions.ConsumerResubscribeDelay straight through, and zero is what an
// app that never sets the option sends, so a guard that accepted it would leave every consumer
// re-subscribing with no backoff floor at all.
func TestRegistrySetResubscribeDelayIgnoresANonPositiveDelay(t *testing.T) {
	tests := []struct {
		name     string
		delay    time.Duration
		expected time.Duration
	}{
		{name: "zero_keeps_the_default", delay: 0, expected: defaultConsumerResubscribeDelay},
		{name: "negative_keeps_the_default", delay: -time.Second, expected: defaultConsumerResubscribeDelay},
		{name: "positive_overrides", delay: time.Millisecond, expected: time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewRegistry(&simpleMockAMQPClient{isReady: true}, gobrickslogger.New("error", false))

			registry.setResubscribeDelay(tt.delay)

			assert.Equal(t, tt.expected, registry.resubscribeDelay)
		})
	}
}

// TestRegistryAnyGivenUpMasksAStoppedRegistry pins the predicate the readiness probe reads
// against the snapshot it mirrors: it must answer the same as ConsumerStates under the same
// shutdown mask, so a stopped registry — whose supervisors are canceled, not failing — never
// reads as an outage.
func TestRegistryAnyGivenUpMasksAStoppedRegistry(t *testing.T) {
	log := gobrickslogger.New("error", false)
	client, deliveries := newOutageClient()
	registry, cancel := startStateRegistry(t, client, log)
	defer cancel()

	require.False(t, registry.anyGivenUp(), "a subscribed consumer has not given up")

	client.setFailing(true)
	close(deliveries)
	require.Eventually(t, registry.anyGivenUp, 5*time.Second, 2*time.Millisecond,
		"the consumer never reached the give-up threshold")

	states := registry.ConsumerStates()
	require.Len(t, states, 1)
	assert.True(t, states[0].GivenUp(), "the predicate and the snapshot must agree")

	registry.StopConsumers()

	assert.False(t, registry.anyGivenUp(), "a stopped registry has no consumer that gave up")
	states = registry.ConsumerStates()
	require.Len(t, states, 1)
	assert.False(t, states[0].GivenUp(), "and the snapshot agrees there too")
}

// newOutageClient returns a client scripted with one subscription and the channel
// that subscription feeds, so a test can close it and then hold the outage open with
// setFailing for as long as it needs to observe an unrecovered consumer.
func newOutageClient() (client *resubscribingMockClient, deliveries chan amqp.Delivery) {
	deliveries = make(chan amqp.Delivery)
	return &resubscribingMockClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		results:              []consumeResult{{ch: deliveries}},
	}, deliveries
}

// startStateRegistry starts a registry with one handler-backed consumer on client,
// paced for fast re-subscribes, and returns it with the cancel of the context its
// consumers run under — cleanup calls that cancel too, so a caller takes it only to
// end the session early. The caller stops the consumers.
func startStateRegistry(t *testing.T, client AMQPClient, log gobrickslogger.Logger) (*Registry, context.CancelFunc) {
	t.Helper()
	registry := NewRegistry(client, log)
	registry.resubscribeDelay = time.Millisecond
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Workers:   1,
		Handler:   &countingTestHandler{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, registry.StartConsumers(ctx))
	return registry, cancel
}

// TestRegistryConsumerStatesStartEachRunWithAFreshSession pins the session
// boundary: a restarted consumer writes to fresh state, so neither the previous
// run's failure streak nor a supervisor still unwinding from it can be read as the
// new run's, while the cumulative counters carry across.
func TestRegistryConsumerStatesStartEachRunWithAFreshSession(t *testing.T) {
	ch1 := make(chan amqp.Delivery)
	ch2 := make(chan amqp.Delivery)
	client := &resubscribingMockClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		results:              []consumeResult{{ch: ch1}, {ch: ch2}},
	}
	registry, _ := startStateRegistry(t, client, &stubLogger{})

	// Recover once, so the cumulative counter has something to carry across.
	close(ch1)
	require.Eventually(t, func() bool {
		return registry.ConsumerStates()[0].Resubscribes == 1
	}, 5*time.Second, 2*time.Millisecond, "consumer did not re-subscribe")

	// Then an outage it cannot recover from, until the streak reaches the threshold.
	client.setFailing(true)
	close(ch2)
	require.Eventually(t, func() bool {
		return registry.ConsumerStates()[0].GivenUp()
	}, 5*time.Second, 2*time.Millisecond, "failure streak did not reach the threshold")

	registry.StopConsumers()
	client.setFailing(false)
	require.NoError(t, registry.StartConsumers(context.Background()))
	defer registry.StopConsumers()

	restarted := registry.ConsumerStates()[0]
	assert.True(t, restarted.Subscribed)
	assert.Zero(t, restarted.FailStreak, "a restarted consumer does not inherit the previous session's streak")
	assert.False(t, restarted.GivenUp())
	// A lower bound, not an equality: the previous run's supervisor is not joined, so
	// it may land one more success before the carry-over reads its counters. A
	// non-carrying implementation reports 0 and still fails here.
	assert.GreaterOrEqual(t, restarted.Resubscribes, uint64(1), "the cumulative counters carry across a restart")
}

// pausingConsumeClient serves one subscription, fails every re-subscribe after it,
// and blocks the attempt at pauseAt so a test can read the supervisor's state while
// it is frozen at an exact attempt count. Call 0 is the initial subscription, so
// pauseAt is an attempt number and must be at least 1.
type pausingConsumeClient struct {
	*simpleMockAMQPClient
	first   chan amqp.Delivery
	pauseAt int
	paused  chan struct{}
	release chan struct{}

	mu    sync.Mutex
	calls int
}

var _ AMQPClient = (*pausingConsumeClient)(nil)

func newPausingConsumeClient(pauseAt int) *pausingConsumeClient {
	return &pausingConsumeClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		first:                make(chan amqp.Delivery),
		pauseAt:              pauseAt,
		paused:               make(chan struct{}),
		release:              make(chan struct{}),
	}
}

func (m *pausingConsumeClient) ConsumeFromQueue(_ context.Context, _ ConsumeOptions) (<-chan amqp.Delivery, error) {
	m.mu.Lock()
	idx := m.calls
	m.calls++
	m.mu.Unlock()

	switch idx {
	case 0:
		return m.first, nil
	case m.pauseAt:
		close(m.paused)
		<-m.release
		return nil, errNotConnected
	default:
		return nil, errNotConnected
	}
}

// TestRegistryConsumerStatesCountTheAttemptTheLogEscalatesOn pins the streak against
// the re-subscribe loop's own attempt counter: the two must be one number, or the
// WARN escalation and the readiness verdict would fire an attempt apart. The client
// freezes inside the attempt after the escalating one, so both are read at rest.
func TestRegistryConsumerStatesCountTheAttemptTheLogEscalatesOn(t *testing.T) {
	// Call 0 is the initial subscription, so attempt N is call N. Freezing on the
	// attempt after the threshold leaves exactly that many failures behind.
	client := newPausingConsumeClient(consumerResubscribeWarnFromAttempt + 1)
	log := newRecordingLogger()
	registry, _ := startStateRegistry(t, client, log)

	close(client.first)
	select {
	case <-client.paused: // every attempt up to the threshold has now failed
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor never reached the paused attempt")
	}
	defer close(client.release)

	state := registry.ConsumerStates()[0]
	var warned []string
	for _, line := range log.Lines() {
		if line.Msg == "Consumer re-subscribe attempt failed, will retry" && line.Level == gobrickslogger.LevelWarn {
			warned = append(warned, line.Values("attempt")...)
		}
	}
	require.Len(t, warned, 1, "exactly the threshold attempt should have escalated to WARN")
	assert.Equal(t, strconv.Itoa(state.FailStreak), warned[0],
		"the streak and the attempt the log escalates on are one number")
	assert.Equal(t, consumerResubscribeWarnFromAttempt, state.FailStreak)
	assert.True(t, state.GivenUp())
}

// TestRegistryConsumerStatesClearSubscribedWhenAHealthySessionIsCanceled pins the
// half of the session end that only a healthy session can show: with no outage in
// progress nothing has marked the consumer unsubscribed along the way, so the
// supervisor's exit is the sole writer of that flag.
func TestRegistryConsumerStatesClearSubscribedWhenAHealthySessionIsCanceled(t *testing.T) {
	client, _ := newOutageClient() // the delivery channel stays open: no outage
	registry, cancel := startStateRegistry(t, client, &stubLogger{})
	defer cancel()

	require.True(t, registry.ConsumerStates()[0].Subscribed)

	cancel() // no StopConsumers: the mask stays off

	require.Eventually(t, func() bool {
		return !registry.ConsumerStates()[0].Subscribed
	}, 5*time.Second, 2*time.Millisecond, "a canceled supervisor left the consumer reading subscribed")
}

// TestRegistryConsumerStatesEndTheSessionWhenTheContextIsCanceled pins the other way
// a consumer stops: a caller that cancels the context it handed StartConsumers,
// instead of calling StopConsumers, leaves consumersActive true, so the snapshot is
// unmasked and the flags themselves have to be honest. Neither may outlive the
// supervisor — subscribed with nothing consuming, or given up with nothing retrying.
func TestRegistryConsumerStatesEndTheSessionWhenTheContextIsCanceled(t *testing.T) {
	client, ch1 := newOutageClient()
	registry, cancel := startStateRegistry(t, client, &stubLogger{})
	defer cancel()

	require.True(t, registry.ConsumerStates()[0].Subscribed)

	// Cancel an outage in progress, so both flags are set when the supervisor goes.
	client.setFailing(true)
	close(ch1)
	require.Eventually(t, func() bool {
		return registry.ConsumerStates()[0].GivenUp()
	}, 5*time.Second, 2*time.Millisecond, "failure streak did not reach the threshold")

	cancel() // no StopConsumers: the mask stays off

	require.Eventually(t, func() bool {
		state := registry.ConsumerStates()[0]
		return !state.Subscribed && !state.GivenUp()
	}, 5*time.Second, 2*time.Millisecond, "a canceled supervisor left its flags behind")
	assert.Zero(t, registry.ConsumerStates()[0].FailStreak)
}

// TestRegistryConsumerHistoryOutlivesItsSession pins the split between what belongs to a
// consumer and what belongs to one run of it. StopConsumers does not wait for its
// supervisors and AMQPClientImpl.ConsumeFromQueue ignores the context, so a replaced
// session can still land a success: it must reach the cumulative record, and it must not
// touch the live flags of the session that replaced it.
func TestRegistryConsumerHistoryOutlivesItsSession(t *testing.T) {
	client := &resubscribingMockClient{simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true}}
	registry := NewRegistry(client, &stubLogger{})
	declaration := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Workers:   1,
		Handler:   &countingTestHandler{},
	}
	registry.RegisterConsumer(declaration)

	// One goroutine throughout, so consumerStateFor's "callers hold r.mu" contract is
	// trivially met without starting real supervisors.
	first := registry.consumerStateFor(declaration)
	first.markResubscribed(time.Now())
	second := registry.consumerStateFor(declaration)
	second.markSubscribed()
	registry.consumersActive = true

	// The replaced session's supervisor, still unwinding, lands one more of everything.
	first.markResubscribed(time.Now())
	first.setFailStreak(consumerResubscribeWarnFromAttempt)
	first.markUnsubscribed()

	state := registry.ConsumerStates()[0]
	assert.Equal(t, uint64(2), state.Resubscribes, "the record counts every session's successes")
	assert.False(t, state.LastResubscribeAt.IsZero())
	assert.True(t, state.Subscribed, "a replaced session cannot clear the live session's flags")
	assert.Zero(t, state.FailStreak, "nor lengthen its streak")
}

// TestRegistryConsumerStatesCarryTheWholeConsumerIdentity pins that a row names the
// consumer, not merely its queue: RegisterConsumer's identity is queue + consumer tag +
// event type, so two consumers legitimately sharing a queue must be told apart.
func TestRegistryConsumerStatesCarryTheWholeConsumerIdentity(t *testing.T) {
	const (
		ordersTag    = "orders-worker"
		auditTag     = "audit-worker"
		ordersEvent  = "order.created"
		auditedEvent = "order.audited"
	)
	client := &resubscribingMockClient{simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true}}
	registry := NewRegistry(client, &stubLogger{})
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue: testQueueName, Consumer: ordersTag, EventType: ordersEvent,
		Workers: 1, Handler: &countingTestHandler{},
	})
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue: testQueueName, Consumer: auditTag, EventType: auditedEvent,
		Workers: 1, Handler: &countingTestHandler{},
	})

	states := registry.ConsumerStates()

	require.Len(t, states, 2, "two consumers on one queue are two rows")
	assert.Equal(t, []string{testQueueName, ordersTag, ordersEvent},
		[]string{states[0].Queue, states[0].Consumer, states[0].EventType})
	assert.Equal(t, []string{testQueueName, auditTag, auditedEvent},
		[]string{states[1].Queue, states[1].Consumer, states[1].EventType})
	assert.Empty(t, states[0].Key, "a registry does not know the key it was leased under")
}

// TestRegistryConsumerStatesCoverEveryDeclaredConsumerInOrder pins the snapshot's
// shape: one entry per declared consumer, in declaration order, including the
// documentation-only consumer that has no handler and therefore never subscribes.
func TestRegistryConsumerStatesCoverEveryDeclaredConsumerInOrder(t *testing.T) {
	client := &resubscribingMockClient{simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true}}
	registry := NewRegistry(client, &stubLogger{})

	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Workers:   1,
		Handler:   &countingTestHandler{},
	})
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueue1Name,
		EventType: testEventType,
		Workers:   1,
	})
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueue2Name,
		EventType: testEventType,
		Workers:   1,
		Handler:   &countingTestHandler{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, registry.StartConsumers(ctx))
	defer registry.StopConsumers()

	states := registry.ConsumerStates()
	require.Len(t, states, 3)
	assert.Equal(t, []string{testQueueName, testQueue1Name, testQueue2Name},
		[]string{states[0].Queue, states[1].Queue, states[2].Queue})
	assert.True(t, states[0].Subscribed)
	assert.False(t, states[1].Subscribed, "a consumer without a handler never subscribes")
	assert.False(t, states[1].GivenUp(), "a documentation-only consumer never reads as abandoned")
	assert.True(t, states[2].Subscribed)
}

// ===== Topology Redeclare After Reconnect Tests =====

var testBindingKey = fakeBindingKey(&BindingDeclaration{Queue: testQueueName, Exchange: testExchangeName, RoutingKey: "orders.#"})

const (
	redeclaredMsg       = "Messaging topology redeclared on new channel"
	redeclareFailedMsg  = "Messaging topology redeclare failed, the next channel retries"
	redeclareSkippedMsg = "Messaging declaration rejected with PRECONDITION_FAILED, skipped until restart: fix the server-side definition and restart the process"
)

// reconnectingMockClient fakes a broker behind a reconnecting client. It keeps
// the declared queues, fails a consume on a missing queue with 404, fails the
// declares scripted in declareErrs, and exposes a channel generation. A broker
// error (*amqp.Error) closes the channel like RabbitMQ does: a failed consume
// rotates the generation at once, a failed declare leaves the channel dead until
// the next consume attempt observes it and the client reinitializes. Any other
// declare error leaves the channel open.
type reconnectingMockClient struct {
	*simpleMockAMQPClient
	callMu        sync.Mutex
	generation    uint64
	notReady      bool
	channelClosed bool
	notReadyCalls int
	restartOn     string // declare key that first restarts the broker: new generation, queues wiped
	queues        map[string]bool
	declareErrs   map[string][]error
	consumeErrs   []error
	declares      map[string][]string // key -> generation of each declare attempt, in order
	subscriptions []chan amqp.Delivery
	// chanReady mirrors the real client's new-channel broadcast. Only newChannel,
	// signalChannelReady and Close close it, so a test that rotates the
	// generation through locked() alone leaves an observer asleep and exercises
	// the consumer driver on its own.
	chanReady    chan struct{}
	clientClosed bool
	// parkOn is the declares key whose declare parks inside the fake, so a test
	// can hold a redeclare pass open across a StopConsumers; nil parkGate leaves
	// every declare unparked.
	parkOn   string
	parkGate *testutil.BlockedCreate
}

var (
	_ channelGenerationer  = (*reconnectingMockClient)(nil)
	_ channelReadyNotifier = (*reconnectingMockClient)(nil)
)

func newReconnectingMockClient() *reconnectingMockClient {
	return &reconnectingMockClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		generation:           1,
		queues:               map[string]bool{},
		declareErrs:          map[string][]error{},
		declares:             map[string][]string{},
	}
}

func (m *reconnectingMockClient) locked(fn func()) {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	fn()
}

func (m *reconnectingMockClient) channelGeneration() (generation uint64, ready bool) {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	return m.generation, !m.notReady
}

func (m *reconnectingMockClient) channelReadyNotify() (ready <-chan struct{}, open bool) {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	if m.clientClosed {
		return nil, false
	}
	if m.chanReady == nil {
		m.chanReady = make(chan struct{})
	}
	return m.chanReady, true
}

// newChannel is a reconnect that ended ready: a fresh generation, announced.
func (m *reconnectingMockClient) newChannel() {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	m.generation++
	m.wakeObservers()
}

// signalChannelReady announces readiness on the generation already running, so a
// test can prove a wake the topology does not need declares nothing.
func (m *reconnectingMockClient) signalChannelReady() {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	m.wakeObservers()
}

// Close ends every observer before closing the base client, the way the real
// client's Close does.
func (m *reconnectingMockClient) Close() error {
	m.callMu.Lock()
	m.clientClosed = true
	m.wakeObservers()
	m.callMu.Unlock()
	return m.simpleMockAMQPClient.Close()
}

// wakeObservers closes the current broadcast and drops it; callers hold callMu.
func (m *reconnectingMockClient) wakeObservers() {
	if m.chanReady != nil {
		close(m.chanReady)
		m.chanReady = nil
	}
}

// parkIfArmed blocks a declare of key until the test releases it. The gate is
// read under callMu but waited on outside it, so a parked declare holds no lock.
func (m *reconnectingMockClient) parkIfArmed(key string) {
	m.callMu.Lock()
	gate, armed := m.parkGate, m.parkOn == key
	m.callMu.Unlock()
	if gate != nil && armed {
		gate.Arrive()
	}
}

func (m *reconnectingMockClient) declare(ctx context.Context, key string, onSuccess func()) error {
	m.parkIfArmed(key)
	// The real client refuses a declare on a canceled context before it touches
	// the channel (AMQPClientImpl.DeclareExchange), so a halted pass stops here
	// instead of finishing on the broker.
	if err := ctx.Err(); err != nil {
		return err
	}
	m.callMu.Lock()
	defer m.callMu.Unlock()
	if m.notReady {
		return errNotConnected
	}
	if key == m.restartOn {
		m.restartOn = ""
		m.generation++
		m.queues = map[string]bool{}
	}
	m.declares[key] = append(m.declares[key], strconv.FormatUint(m.generation, 10))
	if errs := m.declareErrs[key]; len(errs) > 0 {
		m.declareErrs[key] = errs[1:]
		var amqpErr *amqp.Error
		if errors.As(errs[0], &amqpErr) {
			m.channelClosed = true
		}
		return errs[0]
	}
	if onSuccess != nil {
		onSuccess()
	}
	return nil
}

func (m *reconnectingMockClient) DeclareExchange(ctx context.Context, exchange *ExchangeDeclaration) error {
	return m.declare(ctx, "exchange:"+exchange.Name, nil)
}

func (m *reconnectingMockClient) DeclareQueue(ctx context.Context, queue *QueueDeclaration) error {
	return m.declare(ctx, "queue:"+queue.Name, func() { m.queues[queue.Name] = true })
}

func (m *reconnectingMockClient) BindQueue(ctx context.Context, binding *BindingDeclaration) error {
	return m.declare(ctx, fakeBindingKey(binding), nil)
}

// fakeBindingKey is the fake broker's declares key of a binding: every field of
// the broker's binding identity, names quoted, so bindings stay apart in the fake
// even where the registry's own keys would collide.
func fakeBindingKey(b *BindingDeclaration) string {
	return fmt.Sprintf("binding:%q|%q|%q|%v", b.Queue, b.Exchange, b.RoutingKey, b.Args)
}

func (m *reconnectingMockClient) ConsumeFromQueue(_ context.Context, opts ConsumeOptions) (<-chan amqp.Delivery, error) {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	switch {
	case m.notReady:
		m.notReadyCalls++
		return nil, errNotConnected
	case m.channelClosed:
		m.channelClosed = false
		m.generation++
		return nil, amqp.ErrClosed
	case len(m.consumeErrs) > 0:
		err := m.consumeErrs[0]
		m.consumeErrs = m.consumeErrs[1:]
		return nil, err
	case !m.queues[opts.Queue]:
		m.generation++
		return nil, &amqp.Error{Code: amqp.NotFound, Reason: "NOT_FOUND - no queue '" + opts.Queue + "'", Server: true}
	}
	ch := make(chan amqp.Delivery, 1)
	m.subscriptions = append(m.subscriptions, ch)
	return ch, nil
}

// declaresOf returns the generation of every declare attempt for key, in order.
func (m *reconnectingMockClient) declaresOf(key string) []string {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	return slices.Clone(m.declares[key])
}

// subscription returns the i-th successful subscription, or nil before it exists.
func (m *reconnectingMockClient) subscription(i int) chan amqp.Delivery {
	m.callMu.Lock()
	defer m.callMu.Unlock()
	if i < len(m.subscriptions) {
		return m.subscriptions[i]
	}
	return nil
}

// startRedeclareRegistry declares one exchange, queue and binding, then any
// extra bindings, plus a consumer on the queue, then starts consuming on the
// client's first channel.
func startRedeclareRegistry(ctx context.Context, t *testing.T, client AMQPClient, log gobrickslogger.Logger, handler MessageHandler, bindings ...*BindingDeclaration) *Registry {
	t.Helper()
	return startRedeclareRegistryOn(ctx, t, client, log, handler,
		&ExchangeDeclaration{Name: testExchangeName, Type: ExchangeTypeTopic, Durable: true}, bindings...)
}

// startRedeclareRegistryOn is startRedeclareRegistry over a caller-chosen
// exchange, so the same consumer-driven fixture serves a locally declared one
// and an EXTERNAL one (ADR-119), whose pass carries a passive step.
func startRedeclareRegistryOn(ctx context.Context, t *testing.T, client AMQPClient, log gobrickslogger.Logger, handler MessageHandler, exchange *ExchangeDeclaration, bindings ...*BindingDeclaration) *Registry {
	t.Helper()
	registry := NewRegistry(client, log)
	registry.resubscribeDelay = time.Millisecond
	registry.RegisterExchange(exchange)
	registry.RegisterQueue(&QueueDeclaration{Name: testQueueName, Durable: true})
	registry.RegisterBinding(&BindingDeclaration{Queue: testQueueName, Exchange: exchange.Name, RoutingKey: "orders.#"})
	for _, binding := range bindings {
		registry.RegisterBinding(binding)
	}
	registry.RegisterConsumer(&ConsumerDeclaration{Queue: testQueueName, EventType: testEventType, Workers: 1, Handler: handler})

	require.NoError(t, registry.DeclareInfrastructure(ctx))
	require.NoError(t, registry.StartConsumers(ctx))
	return registry
}

// awaitSubscription waits until the client has handed out its i-th subscription.
func awaitSubscription(t *testing.T, client *reconnectingMockClient, i int) chan amqp.Delivery {
	t.Helper()
	require.Eventually(t, func() bool {
		return client.subscription(i) != nil
	}, 5*time.Second, time.Millisecond, "consumer did not subscribe a %d-th time", i+1)
	return client.subscription(i)
}

// deliverAndAwaitAck proves a subscription is live: the delivery reaches the
// handler and processMessage's tail acks it before the test moves on.
func deliverAndAwaitAck(t *testing.T, ch chan amqp.Delivery, handler *countingTestHandler) {
	t.Helper()
	acker := &mockAcknowledger{}
	ch <- amqp.Delivery{MessageId: testMessageID, Body: []byte(testMessageBody), Headers: amqp.Table{}, Acknowledger: acker}
	require.Eventually(t, acker.AckCalled, 5*time.Second, time.Millisecond, "delivery was not acked")
	assert.Positive(t, handler.CallCount())
}

// TestRegistryRedeclaresLostTopologyBeforeResubscribing is the acceptance test
// for a broker that lost its topology across a reconnect: once the client is
// ready on a new channel the registry declares the queue again, and the
// consumer receives the next message.
func TestRegistryRedeclaresLostTopologyBeforeResubscribing(t *testing.T) {
	client := newReconnectingMockClient()
	handler := &countingTestHandler{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := startRedeclareRegistry(ctx, t, client, &stubLogger{}, handler)
	defer registry.StopConsumers()
	first := awaitSubscription(t, client, 0)

	client.locked(func() {
		delete(client.queues, testQueueName)
		client.notReady = true
		client.generation++
	})
	close(first)
	require.Eventually(t, func() bool {
		var calls int
		client.locked(func() { calls = client.notReadyCalls })
		return calls >= 2
	}, 5*time.Second, time.Millisecond, "consumer did not retry while the client was reconnecting")
	client.locked(func() { client.notReady = false })

	deliverAndAwaitAck(t, awaitSubscription(t, client, 1), handler)
	assert.Equal(t, []string{"1", "2"}, client.declaresOf("queue:"+testQueueName))
}

// TestRegistryRedeclareRepeatsThePassiveStep pins that an external exchange is
// re-verified on each new channel generation, exactly as a local declaration is
// re-declared: the owner may have deleted it while this service was away.
func TestRegistryRedeclareRepeatsThePassiveStep(t *testing.T) {
	client := newReconnectingMockClient()
	handler := &countingTestHandler{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := startRedeclareRegistryOn(ctx, t, client, &stubLogger{}, handler, NewExternalExchange(testExternalExchange))
	defer registry.StopConsumers()
	first := awaitSubscription(t, client, 0)

	client.locked(func() { client.generation++ })
	close(first)

	deliverAndAwaitAck(t, awaitSubscription(t, client, 1), handler)
	assert.Equal(t, []string{"1", "2"}, client.declaresOf("exchange:"+testExternalExchange))
}

// TestRegistryRedeclarePassiveConflictSkipsLikeAnyStep pins that ADR-119 carves
// NO exemption into ADR-113's skip set. A real broker cannot answer 406 to a
// passive declare — its passive path is lookup-or-404 — so the criterion "an
// external exchange never enters the skip set" is satisfied by the protocol, not
// by code. Were a broker ever to answer 406, exempting the step would be worse
// than skipping it: replayTopology ends a pass at the first failure and
// exchanges run before bindings, so a never-skipped step would block every later
// pass. Skipping degrades instead — the pass reaches the queue and the binding,
// whose own 404 still surfaces a genuinely absent exchange.
func TestRegistryRedeclarePassiveConflictSkipsLikeAnyStep(t *testing.T) {
	client := newReconnectingMockClient()
	handler := &countingTestHandler{}
	log := newRecordingLogger()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := startRedeclareRegistryOn(ctx, t, client, log, handler, NewExternalExchange(testExternalExchange))
	defer registry.StopConsumers()
	first := awaitSubscription(t, client, 0)

	mismatch := &amqp.Error{Code: amqp.PreconditionFailed, Reason: "PRECONDITION_FAILED - inequivalent arg 'type'", Server: true}
	client.locked(func() {
		client.generation++
		client.declareErrs["exchange:"+testExternalExchange] = []error{mismatch}
	})
	close(first)

	deliverAndAwaitAck(t, awaitSubscription(t, client, 1), handler)

	bindingKey := fakeBindingKey(&BindingDeclaration{Queue: testQueueName, Exchange: testExternalExchange, RoutingKey: "orders.#"})
	assert.Equal(t, []string{"1", "2"}, client.declaresOf("exchange:"+testExternalExchange),
		"the refused step is skipped by every later pass, like any other 406")
	assert.Equal(t, []string{"1", "3"}, client.declaresOf(bindingKey),
		"the pass must reach the binding on the next generation instead of stalling on the skipped step")
	skipped := log.Line(t, redeclareSkippedMsg)
	assert.Equal(t, []string{"406"}, skipped.Values("amqp_reply_code"))
	assert.Equal(t, []string{"exchange:" + testExternalExchange}, skipped.Values("declaration"))
}

// TestRegistryRedeclaresOncePerChannelGeneration verifies a healthy reconnect
// costs one declare pass per new channel: the backoff attempts that follow
// within the same generation declare nothing.
func TestRegistryRedeclaresOncePerChannelGeneration(t *testing.T) {
	client := newReconnectingMockClient()
	handler := &countingTestHandler{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := startRedeclareRegistry(ctx, t, client, &stubLogger{}, handler)
	defer registry.StopConsumers()
	first := awaitSubscription(t, client, 0)

	client.locked(func() {
		client.generation++
		client.consumeErrs = []error{errNotConnected, errNotConnected, errNotConnected}
	})
	close(first)

	deliverAndAwaitAck(t, awaitSubscription(t, client, 1), handler)
	for _, key := range []string{"exchange:" + testExchangeName, "queue:" + testQueueName, testBindingKey} {
		assert.Equal(t, []string{"1", "2"}, client.declaresOf(key), key)
	}
}

// TestRegistryRedeclareDoesNotRetryFailedPassOnSameChannel verifies a pass that fails
// without losing the channel is not re-run by the backoff attempts on that generation.
func TestRegistryRedeclareDoesNotRetryFailedPassOnSameChannel(t *testing.T) {
	client := newReconnectingMockClient()
	handler := &countingTestHandler{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := startRedeclareRegistry(ctx, t, client, &stubLogger{}, handler)
	defer registry.StopConsumers()
	first := awaitSubscription(t, client, 0)

	client.locked(func() {
		client.generation++
		client.declareErrs["exchange:"+testExchangeName] = []error{errors.New("invalid args table")}
		client.consumeErrs = []error{errNotConnected, errNotConnected}
	})
	close(first)

	deliverAndAwaitAck(t, awaitSubscription(t, client, 1), handler)
	assert.Equal(t, []string{"1", "2"}, client.declaresOf("exchange:"+testExchangeName))
	assert.Equal(t, []string{"1"}, client.declaresOf("queue:"+testQueueName))
	assert.Equal(t, []string{"1"}, client.declaresOf(testBindingKey))
}

// TestRegistryRedeclaresOncePerGenerationAcrossConsumers verifies consumers
// re-subscribing onto the same new channel share one pass.
func TestRegistryRedeclaresOncePerGenerationAcrossConsumers(t *testing.T) {
	const otherQueue = "test-queue-2"
	client := newReconnectingMockClient()
	handler := &countingTestHandler{}
	registry := NewRegistry(client, &stubLogger{})
	registry.resubscribeDelay = time.Millisecond
	for _, queue := range []string{testQueueName, otherQueue} {
		registry.RegisterQueue(&QueueDeclaration{Name: queue, Durable: true})
		registry.RegisterConsumer(&ConsumerDeclaration{Queue: queue, EventType: testEventType, Workers: 1, Handler: handler})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer registry.StopConsumers()
	require.NoError(t, registry.DeclareInfrastructure(ctx))
	require.NoError(t, registry.StartConsumers(ctx))
	first, second := awaitSubscription(t, client, 0), awaitSubscription(t, client, 1)

	client.locked(func() { client.generation++ })
	close(first)
	close(second)

	awaitSubscription(t, client, 3)
	assert.Equal(t, []string{"1", "2"}, client.declaresOf("queue:"+testQueueName))
	assert.Equal(t, []string{"1", "2"}, client.declaresOf("queue:"+otherQueue))
}

// TestRegistryRedeclaresBindingsToAnUndeclaredExchange verifies a pass whose
// bindings outnumber its exchanges and queues — a queue bound twice to a broker
// built-in exchange the registry never declares.
func TestRegistryRedeclaresBindingsToAnUndeclaredExchange(t *testing.T) {
	const builtin = "amq.topic"
	client := newReconnectingMockClient()
	registry := NewRegistry(client, &stubLogger{})
	registry.resubscribeDelay = time.Millisecond
	registry.RegisterQueue(&QueueDeclaration{Name: testQueueName, Durable: true})
	for _, routingKey := range []string{"orders.created", "orders.cancelled"} {
		registry.RegisterBinding(&BindingDeclaration{Queue: testQueueName, Exchange: builtin, RoutingKey: routingKey})
	}
	registry.RegisterConsumer(&ConsumerDeclaration{Queue: testQueueName, EventType: testEventType, Workers: 1, Handler: &countingTestHandler{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer registry.StopConsumers()
	require.NoError(t, registry.DeclareInfrastructure(ctx))
	require.NoError(t, registry.StartConsumers(ctx))
	first := awaitSubscription(t, client, 0)

	client.locked(func() { client.generation++ })
	close(first)

	awaitSubscription(t, client, 1)
	assert.Equal(t, []string{"1", "2"}, client.declaresOf(fakeBindingKey(&BindingDeclaration{Queue: testQueueName, Exchange: builtin, RoutingKey: "orders.cancelled"})))
}

// TestRegistryRedeclaresAgainWhenTheChannelIsReplacedMidPass verifies a pass cut
// across two channels by a broker restart is repeated on the new channel before
// the consumer subscribes, so topology the restart wiped is declared again.
func TestRegistryRedeclaresAgainWhenTheChannelIsReplacedMidPass(t *testing.T) {
	client := newReconnectingMockClient()
	handler := &countingTestHandler{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := startRedeclareRegistry(ctx, t, client, &stubLogger{}, handler)
	defer registry.StopConsumers()
	first := awaitSubscription(t, client, 0)

	client.locked(func() {
		client.generation++
		client.restartOn = "queue:" + testQueueName
	})
	close(first)

	deliverAndAwaitAck(t, awaitSubscription(t, client, 1), handler)
	assert.Equal(t, []string{"1", "2", "3"}, client.declaresOf("exchange:"+testExchangeName))
}

// TestRegistryResubscribeOnSameChannelDoesNotRedeclare verifies a delivery
// channel closed without a new channel (a broker basic.cancel) re-subscribes
// without a pass, because DeclareInfrastructure recorded its generation.
func TestRegistryResubscribeOnSameChannelDoesNotRedeclare(t *testing.T) {
	client := newReconnectingMockClient()
	handler := &countingTestHandler{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := startRedeclareRegistry(ctx, t, client, &stubLogger{}, handler)
	defer registry.StopConsumers()
	close(awaitSubscription(t, client, 0))

	deliverAndAwaitAck(t, awaitSubscription(t, client, 1), handler)
	assert.Equal(t, []string{"1"}, client.declaresOf("queue:"+testQueueName))
}

// TestRegistryResubscribeWithoutChannelGenerationDoesNotRedeclare verifies a
// client that does not expose channelGeneration keeps the behavior from before
// the redeclare pass: re-subscribing declares nothing.
func TestRegistryResubscribeWithoutChannelGenerationDoesNotRedeclare(t *testing.T) {
	ch1 := make(chan amqp.Delivery)
	client := &resubscribingMockClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		results:              []consumeResult{{ch: ch1}},
	}
	_, exposesGeneration := AMQPClient(client).(channelGenerationer)
	require.False(t, exposesGeneration)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := startRedeclareRegistry(ctx, t, client, &stubLogger{}, &countingTestHandler{})
	defer registry.StopConsumers()

	close(ch1)
	require.Eventually(t, func() bool {
		return client.consumeCallCount() >= 2
	}, 5*time.Second, time.Millisecond, "consumer did not re-subscribe")
	client.mu.RLock()
	defer client.mu.RUnlock()
	assert.Equal(t, []string{testQueueName}, client.declaredQueues)
}

// TestRegistryRedeclareSkipsDeclarationRejectedWithPreconditionFailed pins the
// 406 rule: a declaration the broker refuses because a surviving entity has
// different arguments ends that pass, logs one WARN, and is skipped by every
// later pass in the process, so the consumer attaches to the surviving queue.
func TestRegistryRedeclareSkipsDeclarationRejectedWithPreconditionFailed(t *testing.T) {
	client := newReconnectingMockClient()
	handler := &countingTestHandler{}
	log := newRecordingLogger()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := startRedeclareRegistry(ctx, t, client, log, handler)
	defer registry.StopConsumers()
	first := awaitSubscription(t, client, 0)

	mismatch := &amqp.Error{Code: amqp.PreconditionFailed, Reason: "PRECONDITION_FAILED - inequivalent arg 'x-queue-type'", Server: true}
	client.locked(func() {
		client.generation++
		client.declareErrs["queue:"+testQueueName] = []error{mismatch, mismatch}
	})
	close(first)
	second := awaitSubscription(t, client, 1)

	client.locked(func() { client.generation++ })
	close(second)
	deliverAndAwaitAck(t, awaitSubscription(t, client, 2), handler)

	assert.Equal(t, []string{"1", "2"}, client.declaresOf("queue:"+testQueueName))
	assert.Equal(t, []string{"1", "3", "4"}, client.declaresOf(testBindingKey))
	skipped := log.Line(t, redeclareSkippedMsg)
	assert.Equal(t, gobrickslogger.LevelWarn, skipped.Level)
	assert.Equal(t, []string{"406"}, skipped.Values("amqp_reply_code"))
	assert.Equal(t, []string{mismatch.Reason}, skipped.Values("amqp_reply_text"))
	assert.Equal(t, []string{"queue:" + testQueueName}, skipped.Values("declaration"))
}

// TestRegistryRedeclareSkipsOnlyTheRejectedBinding verifies the 406 skip set
// keeps apart two bindings of one queue whose names join to the same text or
// which differ only in Args: the rejected binding is skipped, named in the WARN
// by its registration index, and its sibling is still declared on the next
// channel.
func TestRegistryRedeclareSkipsOnlyTheRejectedBinding(t *testing.T) {
	tests := []struct {
		name              string
		rejected, sibling *BindingDeclaration
		declaration       string
	}{
		{
			name:        "delimiter_in_a_name",
			rejected:    &BindingDeclaration{Queue: testQueueName, Exchange: "orders", RoutingKey: "created|eu"},
			sibling:     &BindingDeclaration{Queue: testQueueName, Exchange: "orders|created", RoutingKey: "eu"},
			declaration: "binding[1]:" + testQueueName + "|orders|created|eu",
		},
		{
			name:        "same_names_different_args",
			rejected:    &BindingDeclaration{Queue: testQueueName, Exchange: "amq.headers", Args: map[string]any{"x-match": "all", "region": "eu"}},
			sibling:     &BindingDeclaration{Queue: testQueueName, Exchange: "amq.headers", Args: map[string]any{"x-match": "any", "region": "eu"}},
			declaration: "binding[1]:" + testQueueName + "|amq.headers|",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newReconnectingMockClient()
			log := newRecordingLogger()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			registry := startRedeclareRegistry(ctx, t, client, log, &countingTestHandler{}, tt.rejected, tt.sibling)
			defer registry.StopConsumers()
			first := awaitSubscription(t, client, 0)

			mismatch := &amqp.Error{Code: amqp.PreconditionFailed, Reason: "PRECONDITION_FAILED - inequivalent binding", Server: true}
			client.locked(func() {
				client.generation++
				client.declareErrs[fakeBindingKey(tt.rejected)] = []error{mismatch, mismatch}
			})
			close(first)

			awaitSubscription(t, client, 1)
			assert.Equal(t, []string{"1", "2"}, client.declaresOf(fakeBindingKey(tt.rejected)))
			assert.Equal(t, []string{"1", "3"}, client.declaresOf(fakeBindingKey(tt.sibling)))
			assert.Equal(t, []string{tt.declaration}, log.Line(t, redeclareSkippedMsg).Values("declaration"))
		})
	}
}

// TestRegistryRedeclareRetriesFailedDeclarationOnNextChannel verifies a
// redeclare failure other than PRECONDITION_FAILED logs a WARN carrying the
// broker reply, keeps the re-subscribe loop going, and is declared again on the
// next channel.
func TestRegistryRedeclareRetriesFailedDeclarationOnNextChannel(t *testing.T) {
	client := newReconnectingMockClient()
	handler := &countingTestHandler{}
	log := newRecordingLogger()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := startRedeclareRegistry(ctx, t, client, log, handler)
	defer registry.StopConsumers()
	first := awaitSubscription(t, client, 0)

	client.locked(func() {
		client.generation++
		client.declareErrs["exchange:"+testExchangeName] = []error{amqp.ErrClosed}
	})
	close(first)

	deliverAndAwaitAck(t, awaitSubscription(t, client, 1), handler)
	assert.Equal(t, []string{"1", "2", "3"}, client.declaresOf("exchange:"+testExchangeName))
	failed := log.Line(t, redeclareFailedMsg)
	assert.Equal(t, gobrickslogger.LevelWarn, failed.Level)
	assert.Equal(t, []string{"504"}, failed.Values("amqp_reply_code"))
	assert.Equal(t, []string{"exchange:" + testExchangeName}, failed.Values("declaration"))
	assert.Equal(t, []string{"3"}, log.Line(t, redeclaredMsg).Values("channel_generation"))
}

// newPublisherOnlyRegistry declares one exchange a typed publisher targets, and
// no consumer at all — the shape whose only redeclare driver is the client's
// new-channel announcement.
func newPublisherOnlyRegistry(t *testing.T, client AMQPClient) *Registry {
	t.Helper()
	registry := NewRegistry(client, &stubLogger{})
	registry.RegisterExchange(&ExchangeDeclaration{Name: testExchangeName, Type: ExchangeTypeTopic, Durable: true})
	registry.RegisterPublisher(&PublisherDeclaration{Exchange: testExchangeName, RoutingKey: testKeyValue, EventType: testEventType})
	require.NoError(t, registry.DeclareInfrastructure(context.Background()))
	require.Empty(t, registry.Consumers())
	return registry
}

// awaitDeclares waits for key to have been declared as many times as want, then
// pins which generations those declares ran on.
func awaitDeclares(t *testing.T, client *reconnectingMockClient, key string, want ...string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return len(client.declaresOf(key)) >= len(want)
	}, 5*time.Second, time.Millisecond, "declaration %s was not replayed %d times", key, len(want))
	assert.Equal(t, want, client.declaresOf(key), key)
}

// TestRegistryRedeclaresForPublisherOnlyRegistry is the acceptance test for a
// registry that declared but consumes nothing: the client's new-channel
// announcement drives the pass, so a publisher-only service recovers topology the
// broker lost instead of publishing into a missing exchange forever.
func TestRegistryRedeclaresForPublisherOnlyRegistry(t *testing.T) {
	client := newReconnectingMockClient()
	registry := newPublisherOnlyRegistry(t, client)
	defer registry.StopConsumers()

	client.newChannel()
	awaitDeclares(t, client, "exchange:"+testExchangeName, "1", "2")

	// A wake on a generation already declared adds nothing, so the pass that
	// follows the NEXT rotation is the third declare and not the fourth.
	client.signalChannelReady()
	client.newChannel()
	awaitDeclares(t, client, "exchange:"+testExchangeName, "1", "2", "3")
}

// TestRegistryRedeclaresOncePerGenerationWhenTheObserverWinsTheChannel pins the
// guard from the observer's side: the consumer's own pre-subscribe pass finds the
// generation recorded and declares nothing.
func TestRegistryRedeclaresOncePerGenerationWhenTheObserverWinsTheChannel(t *testing.T) {
	client := newReconnectingMockClient()
	handler := &countingTestHandler{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := startRedeclareRegistry(ctx, t, client, &stubLogger{}, handler)
	defer registry.StopConsumers()
	first := awaitSubscription(t, client, 0)

	client.newChannel()
	awaitDeclares(t, client, "queue:"+testQueueName, "1", "2")

	close(first)
	deliverAndAwaitAck(t, awaitSubscription(t, client, 1), handler)
	assert.Equal(t, []string{"1", "2"}, client.declaresOf("queue:"+testQueueName))
	assert.Equal(t, []string{"1", "2"}, client.declaresOf("exchange:"+testExchangeName))
}

// TestRegistryRedeclaresOncePerGenerationWhenTheConsumerWinsTheChannel pins the
// guard from the other side: an announcement of a generation the re-subscribe
// already declared costs no second pass.
func TestRegistryRedeclaresOncePerGenerationWhenTheConsumerWinsTheChannel(t *testing.T) {
	client := newReconnectingMockClient()
	handler := &countingTestHandler{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := startRedeclareRegistry(ctx, t, client, &stubLogger{}, handler)
	defer registry.StopConsumers()
	first := awaitSubscription(t, client, 0)

	client.locked(func() { client.generation++ })
	close(first)
	deliverAndAwaitAck(t, awaitSubscription(t, client, 1), handler)
	awaitDeclares(t, client, "queue:"+testQueueName, "1", "2")

	client.signalChannelReady()
	client.newChannel()
	awaitDeclares(t, client, "queue:"+testQueueName, "1", "2", "3")
}

// awaitObserverExit waits for the redeclare observer goroutine behind done to return.
func awaitObserverExit(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("redeclare observer did not exit")
	}
}

// TestRegistryRedeclareObserverStopsWhenTheClientCloses covers the teardown path
// that never reaches StopConsumers: the manager closes a client on its
// start-consumers rollback and drops the registry, so the observer has to take
// the client's own end as its signal.
func TestRegistryRedeclareObserverStopsWhenTheClientCloses(t *testing.T) {
	client := newReconnectingMockClient()
	registry := newPublisherOnlyRegistry(t, client)
	defer registry.StopConsumers()

	require.NoError(t, client.Close())

	awaitObserverExit(t, registry.redeclareObserverDone)
}

// TestRegistryStartsNoRedeclareObserverWithoutTheAnnouncement verifies a client
// that does not announce its channels is never observed: an external AMQPClient
// gets no goroutine, the same way it gets no redeclare pass.
func TestRegistryStartsNoRedeclareObserverWithoutTheAnnouncement(t *testing.T) {
	client := &resubscribingMockClient{simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true}}
	_, announces := AMQPClient(client).(channelReadyNotifier)
	require.False(t, announces)

	registry := newPublisherOnlyRegistry(t, client)
	defer registry.StopConsumers()

	assert.Nil(t, registry.redeclareObserverDone)
}

// TestRegistryRedeclareObserverStopsOnStopConsumers pins the other half: a
// publisher-only registry never activates consumers, so the observer must stop
// ahead of the active-consumers guard rather than outlive the shutdown.
func TestRegistryRedeclareObserverStopsOnStopConsumers(t *testing.T) {
	client := newReconnectingMockClient()
	registry := newPublisherOnlyRegistry(t, client)

	registry.StopConsumers()

	awaitObserverExit(t, registry.redeclareObserverDone)
}

// TestRegistryStopsARedeclarePassAlreadyInFlight pins that halting the registry
// reaches a pass already inside replayTopology, not only the next one: the
// observer runs on a context StopConsumers cancels, so the declarations after
// the one it was parked in are never issued onto a connection shutdown is about
// to close.
func TestRegistryStopsARedeclarePassAlreadyInFlight(t *testing.T) {
	client := newReconnectingMockClient()
	registry := NewRegistry(client, &stubLogger{})
	registry.RegisterExchange(&ExchangeDeclaration{Name: testExchangeName, Type: ExchangeTypeTopic, Durable: true})
	registry.RegisterQueue(&QueueDeclaration{Name: testQueueName})
	require.NoError(t, registry.DeclareInfrastructure(context.Background()))

	gate := testutil.NewBlockedCreate(t)
	client.locked(func() { client.parkOn, client.parkGate = "exchange:"+testExchangeName, gate })
	client.newChannel()
	<-gate.Started

	registry.StopConsumers()
	gate.Release()
	awaitObserverExit(t, registry.redeclareObserverDone)

	assert.Equal(t, []string{"1"}, client.declaresOf("queue:"+testQueueName),
		"the halted pass declared nothing past the declaration it was parked in")
}

// ===== Consume metrics + receive span tests (plan 099) =====

// sleepingCountingHandler wraps countingTestHandler with a fixed sleep before
// returning, so processMessage observes a non-zero processingTime.
// tracking.RecordConsume skips duration <= 0, and a zero-cost handler
// can produce a zero delta on a coarse clock, which would make duration
// assertions flake.
type sleepingCountingHandler struct {
	countingTestHandler
	sleepFor time.Duration
}

func (h *sleepingCountingHandler) Handle(ctx context.Context, delivery *amqp.Delivery) error {
	time.Sleep(h.sleepFor)
	return h.countingTestHandler.Handle(ctx, delivery)
}

// setupConsumeMetrics installs a test MeterProvider for the AMQP tracking
// package and returns it plus a cleanup that restores the previous provider.
// The tracking instruments are singletons bound at first use, so the reset must
// bracket the test on both sides or state leaks into sibling tests.
func setupConsumeMetrics(t *testing.T) (mp *obtest.TestMeterProvider, cleanup func()) {
	t.Helper()
	prev := otel.GetMeterProvider()
	mp = obtest.NewTestMeterProvider()
	otel.SetMeterProvider(mp)
	tracking.ResetMeterForTesting()
	return mp, func() {
		// no Shutdown: the first-installed provider is otel's permanent delegate (internal/global/state.go sync.Once, #1093)
		otel.SetMeterProvider(prev)
		tracking.ResetMeterForTesting()
	}
}

func TestRegistryProcessMessageRecordsConsumeMetricsOnSuccess(t *testing.T) {
	mp, cleanup := setupConsumeMetrics(t)
	defer cleanup()

	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &sleepingCountingHandler{sleepFor: time.Millisecond}
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

	registry.processMessage(context.Background(), consumer, delivery, &stubLogger{})

	rm := mp.Collect(t)

	obtest.AssertMetricValue(t, rm, "messaging.client.consumed.messages", int64(1))

	durationMetric := obtest.FindMetric(rm, "messaging.client.operation.duration")
	require.NotNil(t, durationMetric)
	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, histData.DataPoints, 1)

	dp := histData.DataPoints[0]
	assertAttribute(t, dp.Attributes.ToSlice(), "messaging.operation.name", "receive")

	_, hasErrType := dp.Attributes.Value(attribute.Key("error.type"))
	assert.False(t, hasErrType, "success path must not stamp error.type")
}

func TestRegistryProcessMessageRecordsConsumeMetricsOnError(t *testing.T) {
	mp, cleanup := setupConsumeMetrics(t)
	defer cleanup()

	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &sleepingCountingHandler{
		countingTestHandler: countingTestHandler{
			testHandler: testHandler{retErr: errors.New("handler error")},
		},
		sleepFor: time.Millisecond,
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

	registry.processMessage(context.Background(), consumer, delivery, &stubLogger{})

	rm := mp.Collect(t)

	// The counter is stamped at completion now (ADR-068), so a failed delivery
	// is still counted once and carries the error type that failed it.
	consumed := obtest.FindMetric(rm, "messaging.client.consumed.messages")
	require.NotNil(t, consumed)
	sumData, ok := consumed.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sumData.DataPoints, 1)
	assert.Equal(t, int64(1), sumData.DataPoints[0].Value)
	assertAttribute(t, sumData.DataPoints[0].Attributes.ToSlice(), "error.type", "*errors.errorString")

	durationMetric := obtest.FindMetric(rm, "messaging.client.operation.duration")
	require.NotNil(t, durationMetric)
	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, histData.DataPoints, 1)

	dp := histData.DataPoints[0]
	assertAttribute(t, dp.Attributes.ToSlice(), "error.type", "*errors.errorString")
}

func TestRegistryProcessMessageCountsExactlyOncePerDelivery(t *testing.T) {
	mp, cleanup := setupConsumeMetrics(t)
	defer cleanup()

	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &sleepingCountingHandler{sleepFor: time.Millisecond}
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

	registry.processMessage(context.Background(), consumer, delivery, &stubLogger{})
	registry.processMessage(context.Background(), consumer, delivery, &stubLogger{})

	rm := mp.Collect(t)

	// Two deliveries, two counts — recorded at completion, once each.
	obtest.AssertMetricValue(t, rm, "messaging.client.consumed.messages", int64(2))

	durationMetric := obtest.FindMetric(rm, "messaging.client.operation.duration")
	require.NotNil(t, durationMetric)
	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, histData.DataPoints, 1)
	assert.Equal(t, uint64(2), histData.DataPoints[0].Count)
}

func TestRegistryProcessMessageStartsReceiveSpan(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

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
		Headers:      amqp.Table{},
		Acknowledger: acker,
	}

	registry.processMessage(context.Background(), consumer, delivery, &stubLogger{})

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, testQueueName+" receive", span.Name)
	assert.Equal(t, trace.SpanKindConsumer, span.SpanKind)
	assertAttribute(t, span.Attributes, "messaging.operation.name", "receive")
}

func TestRegistryProcessMessagePanicMarksSpanError(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &panicTestHandler{panicMsg: "boom"}
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

	require.NotPanics(t, func() {
		registry.processMessage(context.Background(), consumer, delivery, &stubLogger{})
	})

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status.Code)
}

// ===== Per-delivery correlation_id log-shape Tests =====

// recordedLine is one emitted log line: the message plus every field write in
// emission order. Duplicate keys are preserved — zerolog does not de-duplicate —
// so Values can pin that correlation_id is stamped exactly once.
type recordedLine struct {
	Level string
	Msg   string
	Pairs [][2]string
}

// Values returns every value written under key, in order.
func (l recordedLine) Values(key string) []string {
	var out []string
	for _, p := range l.Pairs {
		if p[0] == key {
			out = append(out, p[1])
		}
	}
	return out
}

// recordingLogger captures field-level log shape. stubLogger cannot: every one
// of its event setters discards its arguments.
type recordingLogger struct {
	mu     *sync.Mutex
	lines  *[]recordedLine
	fields [][2]string // context-level fields carried by this logger
	// debugDisabled, when set, makes every event handed out by Debug() report
	// Enabled() == false. Zero value = enabled, matching every pre-existing test.
	debugDisabled bool
	// lastDebug is the most recent event Debug() handed out, so a disabled-path
	// test can assert no fields were built on it: the Step 1 guard skips the
	// whole setter chain (including Msg) when disabled, so no line is ever
	// recorded to assert against instead.
	lastDebug *recordingEvent
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{mu: &sync.Mutex{}, lines: &[]recordedLine{}}
}

func (l *recordingLogger) Lines() []recordedLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]recordedLine, len(*l.lines))
	copy(out, *l.lines)
	return out
}

// Line returns the single line with the given message, failing the test if the
// count is not exactly one.
func (l *recordingLogger) Line(t *testing.T, msg string) recordedLine {
	t.Helper()
	var hits []recordedLine
	for _, ln := range l.Lines() {
		if ln.Msg == msg {
			hits = append(hits, ln)
		}
	}
	require.Len(t, hits, 1, "expected exactly one %q line", msg)
	return hits[0]
}

func (l *recordingLogger) WithContext(_ any) gobrickslogger.Logger { return l }

func (l *recordingLogger) WithFields(f map[string]any) gobrickslogger.Logger {
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys) // map order is random; sort so the shape is deterministic
	merged := make([][2]string, 0, len(l.fields)+len(keys))
	merged = append(merged, l.fields...)
	for _, k := range keys {
		merged = append(merged, [2]string{k, fmt.Sprint(f[k])})
	}
	return &recordingLogger{mu: l.mu, lines: l.lines, fields: merged, debugDisabled: l.debugDisabled}
}

func (l *recordingLogger) Info() gobrickslogger.LogEvent { return l.event(gobrickslogger.LevelInfo) }

func (l *recordingLogger) Error() gobrickslogger.LogEvent { return l.event(gobrickslogger.LevelError) }

func (l *recordingLogger) Warn() gobrickslogger.LogEvent { return l.event(gobrickslogger.LevelWarn) }

func (l *recordingLogger) Fatal() gobrickslogger.LogEvent { return l.event(gobrickslogger.LevelFatal) }

// Debug additionally tracks the event it hands out in lastDebug (see field doc).
func (l *recordingLogger) Debug() gobrickslogger.LogEvent {
	e := l.event(gobrickslogger.LevelDebug)
	l.lastDebug = e
	return e
}

func (l *recordingLogger) event(level string) *recordingEvent {
	pairs := make([][2]string, len(l.fields), len(l.fields)+8)
	copy(pairs, l.fields)
	enabled := level != gobrickslogger.LevelDebug || !l.debugDisabled
	return &recordingEvent{l: l, level: level, pairs: pairs, enabled: enabled}
}

type recordingEvent struct {
	l       *recordingLogger
	level   string
	pairs   [][2]string
	enabled bool
}

func (e *recordingEvent) add(k string, v any) gobrickslogger.LogEvent {
	e.pairs = append(e.pairs, [2]string{k, fmt.Sprint(v)})
	return e
}

func (e *recordingEvent) Str(k, v string) gobrickslogger.LogEvent { return e.add(k, v) }

func (e *recordingEvent) Int(k string, v int) gobrickslogger.LogEvent { return e.add(k, v) }

func (e *recordingEvent) Int64(k string, v int64) gobrickslogger.LogEvent { return e.add(k, v) }

func (e *recordingEvent) Uint64(k string, v uint64) gobrickslogger.LogEvent { return e.add(k, v) }

func (e *recordingEvent) Dur(k string, v time.Duration) gobrickslogger.LogEvent { return e.add(k, v) }

func (e *recordingEvent) Interface(k string, v any) gobrickslogger.LogEvent { return e.add(k, v) }

func (e *recordingEvent) Bytes(k string, v []byte) gobrickslogger.LogEvent {
	return e.add(k, string(v))
}
func (e *recordingEvent) Bool(k string, v bool) gobrickslogger.LogEvent { return e.add(k, v) }
func (e *recordingEvent) Enabled() bool                                 { return e.enabled }

func (e *recordingEvent) Err(err error) gobrickslogger.LogEvent {
	if err != nil {
		return e.add("error", err.Error())
	}
	return e
}

func (e *recordingEvent) Msg(msg string) {
	e.l.mu.Lock()
	defer e.l.mu.Unlock()
	*e.l.lines = append(*e.l.lines, recordedLine{Level: e.level, Msg: msg, Pairs: e.pairs})
}

func (e *recordingEvent) Msgf(format string, args ...any) { e.Msg(fmt.Sprintf(format, args...)) }

var _ gobrickslogger.Logger = (*recordingLogger)(nil)

func TestRegistryProcessMessageStampsCorrelationIDOnSuccessLines(t *testing.T) {
	const wantTraceID = "req-119"
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
		Headers:      amqp.Table{gobrickstrace.HeaderXRequestID: wantTraceID},
		Acknowledger: acker,
	}

	rec := newRecordingLogger()
	registry.processMessage(context.Background(), consumer, delivery, rec)

	debugLine := rec.Line(t, "Processing message")
	require.NotEmpty(t, debugLine.Pairs)
	assert.Equal(t, [2]string{"correlation_id", wantTraceID}, debugLine.Pairs[0])
	assert.Equal(t, []string{"message_id", "routing_key", "exchange", "delivery_tag", "body_size"}, pairKeys(debugLine.Pairs[1:]))

	infoLine := rec.Line(t, "Message processed successfully")
	require.NotEmpty(t, infoLine.Pairs)
	assert.Equal(t, [2]string{"correlation_id", wantTraceID}, infoLine.Pairs[0])
	assert.Equal(t, []string{"processing_time", "message_id"}, pairKeys(infoLine.Pairs[1:]),
		"the shared delivery spine leads the line, the lane's own fields follow")
}

// pairKeys extracts just the keys, in order, from a field-pair slice.
func pairKeys(pairs [][2]string) []string {
	keys := make([]string, len(pairs))
	for i, p := range pairs {
		keys[i] = p[0]
	}
	return keys
}

func TestRegistryProcessMessageCorrelationIDIsStableAcrossLines(t *testing.T) {
	tests := []struct {
		name        string
		handler     MessageHandler
		acker       *mockAcknowledger
		wantMsgs    []string
		expectPanic bool
	}{
		{
			name:     "success_with_ack_failure",
			handler:  &countingTestHandler{},
			acker:    &mockAcknowledger{ackErr: errors.New("ack failed")},
			wantMsgs: []string{"Processing message", "Message processed successfully", "Failed to ack message"},
		},
		{
			name:     "handler_error_with_nack_failure",
			handler:  &countingTestHandler{testHandler: testHandler{retErr: errors.New("handler error")}},
			acker:    &mockAcknowledger{nackErr: errors.New("nack failed")},
			wantMsgs: []string{"Processing message", "Message processing failed - discarding without requeue", "Failed to nack message"},
		},
		{
			name:        "handler_panic_with_nack_failure",
			handler:     &panicTestHandler{panicMsg: "boom"},
			acker:       &mockAcknowledger{nackErr: errors.New("nack failed")},
			wantMsgs:    []string{"Processing message", "Panic recovered in message handler - discarding without requeue", "Failed to nack message"},
			expectPanic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})
			consumer := &ConsumerDeclaration{
				Queue:     testQueueName,
				EventType: testEventType,
				Handler:   tt.handler,
				AutoAck:   false,
			}
			delivery := &amqp.Delivery{
				MessageId:    testMessageID,
				RoutingKey:   testRoutingKey,
				Exchange:     testExchangeName,
				DeliveryTag:  123,
				Body:         []byte(testMessageBody),
				Headers:      amqp.Table{},
				Acknowledger: tt.acker,
			}

			rec := newRecordingLogger()
			run := func() {
				registry.processMessage(context.Background(), consumer, delivery, rec)
			}
			if tt.expectPanic {
				require.NotPanics(t, run)
			} else {
				run()
			}

			lines := rec.Lines()
			require.GreaterOrEqual(t, len(lines), 3, "expected at least three lines")

			// correlation_id means the framework trace ID on every line, and
			// exactly once: the AMQP message's own CorrelationId is stamped
			// under amqp_correlation_id. Requiring a single value is what keeps
			// a second stamp from creeping back and masking the traceID-
			// propagation bug this test exists to catch (Current-state fact 3).
			var sharedID string
			for _, msg := range tt.wantMsgs {
				line := rec.Line(t, msg)
				values := line.Values("correlation_id")
				require.Len(t, values, 1, "line %q must carry exactly one correlation_id", msg)
				id := values[0]
				require.NotEmpty(t, id, "line %q has empty correlation_id", msg)
				if sharedID == "" {
					sharedID = id
				} else {
					assert.Equal(t, sharedID, id, "line %q correlation_id diverged", msg)
				}
			}
		})
	}
}

func TestRegistryProcessMessageFailureLineSeparatesTheTraceAndAMQPCorrelationIDs(t *testing.T) {
	const wantTraceID = "req-119"
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &countingTestHandler{testHandler: testHandler{retErr: errors.New("handler error")}}
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
		AutoAck:   false,
	}

	acker := &mockAcknowledger{}
	delivery := &amqp.Delivery{
		MessageId:     testMessageID,
		CorrelationId: "amqp-corr-1",
		RoutingKey:    testRoutingKey,
		Exchange:      testExchangeName,
		DeliveryTag:   123,
		Body:          []byte(testMessageBody),
		Headers:       amqp.Table{gobrickstrace.HeaderXRequestID: wantTraceID},
		Acknowledger:  acker,
	}

	rec := newRecordingLogger()
	registry.processMessage(context.Background(), consumer, delivery, rec)

	line := rec.Line(t, "Message processing failed - discarding without requeue")
	assert.Equal(t, []string{wantTraceID}, line.Values("correlation_id"))
	assert.Equal(t, []string{"amqp-corr-1"}, line.Values("amqp_correlation_id"))
}

func TestRegistryProcessMessagePanicLineSeparatesTheTraceAndAMQPCorrelationIDs(t *testing.T) {
	const wantTraceID = "req-119"
	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})

	handler := &panicTestHandler{panicMsg: "boom"}
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   handler,
		AutoAck:   false,
	}

	acker := &mockAcknowledger{}
	delivery := &amqp.Delivery{
		MessageId:     testMessageID,
		CorrelationId: "amqp-corr-1",
		RoutingKey:    testRoutingKey,
		Exchange:      testExchangeName,
		DeliveryTag:   123,
		Body:          []byte(testMessageBody),
		Headers:       amqp.Table{gobrickstrace.HeaderXRequestID: wantTraceID},
		Acknowledger:  acker,
	}

	rec := newRecordingLogger()
	require.NotPanics(t, func() {
		registry.processMessage(context.Background(), consumer, delivery, rec)
	})

	line := rec.Line(t, "Panic recovered in message handler - discarding without requeue")
	assert.Equal(t, []string{wantTraceID}, line.Values("correlation_id"))
	assert.Equal(t, []string{"amqp-corr-1"}, line.Values("amqp_correlation_id"))
}

// The per-delivery derived logger is invisible to stubLogger (its WithFields
// returns the receiver), so this drives a real *ZeroLogger. Level "error" keeps
// the success path silent while still paying the full WithContext/WithFields
// cost, which is exactly what is being measured.
func TestRegistryProcessMessagePerDeliveryLoggerAllocs(t *testing.T) {
	measure := func(t *testing.T, tenantStamps bool, headers amqp.Table) float64 {
		t.Helper()
		registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})
		registry.setTenantStamps(tenantStamps)
		log := gobrickslogger.New("error", false)
		consumer := &ConsumerDeclaration{
			Queue:     testQueueName,
			EventType: testEventType,
			Handler:   &countingTestHandler{},
		}
		delivery := &amqp.Delivery{
			MessageId:    testMessageID,
			RoutingKey:   testRoutingKey,
			Exchange:     testExchangeName,
			DeliveryTag:  123,
			Body:         []byte(testMessageBody),
			Headers:      headers,
			Acknowledger: &mockAcknowledger{},
		}
		ctx := context.Background()

		return testing.AllocsPerRun(200, func() {
			registry.processMessage(ctx, consumer, delivery, log)
		})
	}

	stampsOff := measure(t, false, amqp.Table{gobrickstrace.HeaderXRequestID: "req-119"})
	stampsOn := measure(t, true, amqp.Table{
		gobrickstrace.HeaderXRequestID: "req-119",
		TenantStampHeader:              "acme",
	})
	t.Logf("allocs/op: stamps off = %.1f, on = %.1f", stampsOff, stampsOn)

	// Ceiling fixed at 42.0 (advisor resolution 2026-08-09): measured BEFORE = 47.0,
	// AFTER = 38.0 allocs/op — fails the old per-delivery WithFields layer, passes the
	// new per-event stamps with headroom. 38.0 predates PR2a's tracking collapse, which
	// had already dropped this tree's baseline to 34.0 before the delivery pipeline
	// (ADR-068) landed at 29.0 allocs/op (25–27 once it shared its span options and
	// cached its tracer — the exact figure is order-dependent: earlier tests warm the
	// meter/tracer globals), and to 23.0 once the lane stopped boxing its header carrier
	// and building the metric destination through fmt.
	assert.Less(t, stampsOff, 42.0, "the per-delivery WithFields layer is back")

	// The stamp's own cost is asserted as a DELTA, not an absolute: both figures move
	// together by a couple of allocations depending on which tests warmed the meter and
	// tracer globals first, so an absolute ceiling tight enough to be meaningful here
	// would be flaky. Reading the carrier allocates nothing; the whole delta is
	// multitenant.SetTenant's valueCtx and its boxed string, which every tenant-aware
	// delivery pays by design.
	assert.LessOrEqual(t, stampsOn-stampsOff, 3.0,
		"the tenant stamp read grew an allocation layer beyond SetTenant's context value")
}

// TestRegistryProcessMessageLogsDebugFieldsWhenEnabled proves the Step 1 guard
// is transparent when the debug event is enabled: the exact field set and
// values the live chain sets must still reach the "Processing message" line,
// in order.
func TestRegistryProcessMessageLogsDebugFieldsWhenEnabled(t *testing.T) {
	const wantTraceID = "req-118-enabled"
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
		Headers:      amqp.Table{gobrickstrace.HeaderXRequestID: wantTraceID},
		Acknowledger: acker,
	}

	rec := newRecordingLogger()
	registry.processMessage(context.Background(), consumer, delivery, rec)

	debugLine := rec.Line(t, "Processing message")
	assert.Equal(t, [][2]string{
		{"correlation_id", wantTraceID},
		{"message_id", testMessageID},
		{"routing_key", testRoutingKey},
		{"exchange", testExchangeName},
		{"delivery_tag", "123"},
		{"body_size", strconv.Itoa(len(testMessageBody))},
	}, debugLine.Pairs)
}

// TestRegistryProcessMessageSkipsDebugFieldBuildWhenDisabled proves the Step 1
// guard suppresses field building on the disabled path: no fields are set on
// the debug event and no "Processing message" line is emitted, while the INFO
// success line is unaffected (the guard did not swallow the rest of the
// function).
func TestRegistryProcessMessageSkipsDebugFieldBuildWhenDisabled(t *testing.T) {
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
		Headers:      amqp.Table{},
		Acknowledger: acker,
	}

	rec := newRecordingLogger()
	rec.debugDisabled = true
	registry.processMessage(context.Background(), consumer, delivery, rec)

	require.NotNil(t, rec.lastDebug, "Debug() was never called")
	assert.Empty(t, rec.lastDebug.pairs, "guard deleted: debug fields were built for a dropped event")
	for _, ln := range rec.Lines() {
		assert.NotEqual(t, "Processing message", ln.Msg, "guard deleted: Msg was called on a disabled debug event")
	}

	infoLine := rec.Line(t, "Message processed successfully")
	assert.NotEmpty(t, infoLine.Pairs)
}

// ===== Delivery-pipeline lane adapter tests (ADR-068) =====

func TestConsumeSpanExtrasOmitTheFieldsTheDeliveryDidNotCarry(t *testing.T) {
	assert.Empty(t, consumeSpanExtras(deliveryIdentity{}),
		"a delivery with no exchange, routing key, message id or correlation id adds no span attribute")
}

func TestRegistryProcessMessageSpanCarriesEveryDeliveryAttribute(t *testing.T) {
	exporter, cleanup := setupTestTracing(t)
	defer cleanup()

	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   &countingTestHandler{},
		AutoAck:   false,
	}
	delivery := &amqp.Delivery{
		MessageId:     testMessageID,
		CorrelationId: "amqp-corr-1",
		RoutingKey:    testRoutingKey,
		Exchange:      testExchangeName,
		DeliveryTag:   123,
		Body:          []byte(testMessageBody),
		Headers:       amqp.Table{},
		Acknowledger:  &mockAcknowledger{},
	}

	registry.processMessage(context.Background(), consumer, delivery, &stubLogger{})

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, testQueueName+" receive", span.Name)
	assert.Equal(t, trace.SpanKindConsumer, span.SpanKind)
	assertAttribute(t, span.Attributes, string(semconv.MessagingSystemKey), "rabbitmq")
	assertAttribute(t, span.Attributes, string(semconv.MessagingOperationNameKey), "receive")
	assertAttribute(t, span.Attributes, string(semconv.MessagingDestinationNameKey), testQueueName)
	assertAttribute(t, span.Attributes, string(semconv.MessagingMessageBodySizeKey), int64(len(testMessageBody)))
	assertAttribute(t, span.Attributes, "messaging.rabbitmq.exchange", testExchangeName)
	assertAttribute(t, span.Attributes, "messaging.rabbitmq.destination.routing_key", testRoutingKey)
	assertAttribute(t, span.Attributes, string(semconv.MessagingMessageIDKey), testMessageID)
	assertAttribute(t, span.Attributes, string(semconv.MessagingMessageConversationIDKey), "amqp-corr-1")
}

// ===== Stream Queue Consumption Tests =====

func TestConsumeOptionsForwardsArgs(t *testing.T) {
	tests := []struct {
		name     string
		resume   *streamResume
		wantArgs map[string]any
	}{
		{name: "nil_resume_forwards_declared_args", wantArgs: map[string]any{argStreamOffset: streamOffsetFirst}},
		{
			name:     "unseen_resume_forwards_declared_args",
			resume:   &streamResume{},
			wantArgs: map[string]any{argStreamOffset: streamOffsetFirst},
		},
		{
			name:     "seen_resume_overrides_offset_to_one_past_last",
			resume:   &streamResume{last: 11, seen: true},
			wantArgs: map[string]any{argStreamOffset: int64(12)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewRegistry(&simpleMockAMQPClient{isReady: true}, &stubLogger{})

			declared := map[string]any{argStreamOffset: streamOffsetFirst}
			opts := registry.consumeOptionsFor(&ConsumerDeclaration{
				Queue:         testStreamQueue,
				Consumer:      testConsumer,
				PrefetchCount: 10,
				Args:          declared,
			}, tt.resume)

			assert.Equal(t, tt.wantArgs, opts.Args)
			assert.Equal(t, testStreamQueue, opts.Queue)
			assert.Equal(t, map[string]any{argStreamOffset: streamOffsetFirst}, declared,
				"the override must land on a copy, never the declaration's map")
		})
	}
}

func TestStreamOffsetFromHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers amqp.Table
		want    int64
		wantOK  bool
	}{
		{name: "int64", headers: amqp.Table{argStreamOffset: int64(9223372036854775807)}, want: 9223372036854775807, wantOK: true},
		{name: "int32", headers: amqp.Table{argStreamOffset: int32(2147483647)}, want: 2147483647, wantOK: true},
		{name: "int16", headers: amqp.Table{argStreamOffset: int16(32767)}, want: 32767, wantOK: true},
		{name: "int8", headers: amqp.Table{argStreamOffset: int8(127)}, want: 127, wantOK: true},
		{name: "int", headers: amqp.Table{argStreamOffset: 4242}, want: 4242, wantOK: true},
		{name: "absent", headers: amqp.Table{}, want: 0, wantOK: false},
		{name: "nil_headers", headers: nil, want: 0, wantOK: false},
		{name: "string_value", headers: amqp.Table{argStreamOffset: "12"}, want: 0, wantOK: false},
		{name: "float_value", headers: amqp.Table{argStreamOffset: 12.5}, want: 0, wantOK: false},
		{name: "other_header_only", headers: amqp.Table{"x-priority": int64(5)}, want: 0, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := streamOffsetFromHeaders(tt.headers)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// newStreamResumeRegistry wires a registry whose single consumer runs against a
// scripted client, so a test can flap the delivery channel and inspect the
// ConsumeOptions of the re-subscribe.
func newStreamResumeRegistry(t *testing.T, streamQueue bool, declaredArgs map[string]any) (
	registry *Registry, client *resubscribingMockClient, ch1 chan amqp.Delivery,
) {
	t.Helper()

	ch1 = make(chan amqp.Delivery, 1)
	ch2 := make(chan amqp.Delivery)
	client = &resubscribingMockClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		results:              []consumeResult{{ch: ch1}, {ch: ch2}},
	}
	registry = NewRegistry(client, &stubLogger{})
	registry.resubscribeDelay = 5 * time.Millisecond

	queue := &QueueDeclaration{Name: testStreamQueue, Durable: true, Args: map[string]any{}}
	if streamQueue {
		queue.Args[argQueueType] = queueTypeStream
	}
	registry.RegisterQueue(queue)
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testStreamQueue,
		Consumer:  testConsumer,
		EventType: testEventType,
		Workers:   1,
		Handler:   &countingTestHandler{},
		Args:      declaredArgs,
	})

	return registry, client, ch1
}

// waitForResubscribe blocks until the consumer has issued a second
// ConsumeFromQueue call.
func waitForResubscribe(t *testing.T, client *resubscribingMockClient) {
	t.Helper()

	require.Eventually(t, func() bool {
		return client.consumeCallCount() >= 2
	}, time.Second, 2*time.Millisecond, "consumer did not re-subscribe after delivery channel close")
}

// deliverAndFlap sends one delivery carrying offset, waits for it to be acked
// (so the feed loop has recorded it), then closes the channel to force a
// re-subscribe and waits for the second ConsumeFromQueue call.
func deliverAndFlap(t *testing.T, client *resubscribingMockClient, ch chan amqp.Delivery, offset int64) {
	t.Helper()

	acker := &mockAcknowledger{}
	ch <- amqp.Delivery{
		MessageId:    testMessageID,
		Body:         []byte(testMessageBody),
		Headers:      amqp.Table{argStreamOffset: offset},
		Acknowledger: acker,
	}
	require.Eventually(t, acker.AckCalled, time.Second, 2*time.Millisecond,
		"delivery was not acked")

	close(ch)
	waitForResubscribe(t, client)
}

// TestSuperviseConsumerStreamResume pins the flap-resume contract: a stream
// consumer re-subscribes one past the last offset it handed to the worker pool
// instead of re-reading the stream from its declared start position.
func TestSuperviseConsumerStreamResume(t *testing.T) {
	offset := func(v int64) *int64 { return &v }

	tests := []struct {
		name          string
		streamQueue   bool
		declaredArgs  map[string]any
		deliverOffset *int64 // nil = flap before any delivery arrives
		wantResubArgs map[string]any
	}{
		{
			name:          "resumes_one_past_last_delivered_offset",
			streamQueue:   true,
			declaredArgs:  map[string]any{argStreamOffset: streamOffsetFirst},
			deliverOffset: offset(11),
			wantResubArgs: map[string]any{argStreamOffset: int64(12)},
		},
		{
			name:          "override_preserves_other_declared_args",
			streamQueue:   true,
			declaredArgs:  map[string]any{argStreamOffset: streamOffsetFirst, "x-priority": 5},
			deliverOffset: offset(7),
			wantResubArgs: map[string]any{argStreamOffset: int64(8), "x-priority": 5},
		},
		{
			// Stream-ness is read from the declared queue table, not the
			// delivery, so a publisher-forged x-stream-offset header on a
			// classic queue must not alter the re-subscribe.
			name:          "non_stream_queue_ignores_forged_offset_header",
			streamQueue:   false,
			declaredArgs:  map[string]any{"x-priority": 5},
			deliverOffset: offset(99),
			wantResubArgs: map[string]any{"x-priority": 5},
		},
		{
			name:          "flap_before_first_delivery_keeps_declared_offset",
			streamQueue:   true,
			declaredArgs:  map[string]any{argStreamOffset: streamOffsetFirst},
			wantResubArgs: map[string]any{argStreamOffset: streamOffsetFirst},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, client, ch1 := newStreamResumeRegistry(t, tt.streamQueue, tt.declaredArgs)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			require.NoError(t, registry.StartConsumers(ctx))

			if tt.deliverOffset != nil {
				deliverAndFlap(t, client, ch1, *tt.deliverOffset)
			} else {
				close(ch1)
				waitForResubscribe(t, client)
			}

			assert.Equal(t, tt.declaredArgs, client.consumeOptionsAt(0).Args,
				"the initial subscribe must use the declared Args")
			assert.Equal(t, tt.wantResubArgs, client.consumeOptionsAt(1).Args)

			// The declaration's Args map is shared registry state: any override
			// must have been applied to a copy.
			assert.Equal(t, tt.declaredArgs, registry.Consumers()[0].Args)

			registry.StopConsumers()
		})
	}
}

// TestConsumeOptionsWidensIntStreamOffset pins the wire contract for
// x-stream-offset: amqp091 encodes a Go int as a 32-bit AMQP field ('I') and an
// int64 as 64-bit ('l'), so a declared offset above math.MaxInt32 must be
// widened before it reaches the broker or it truncates silently — 1<<32 would
// arrive as 0 and replay the entire stream.
func TestConsumeOptionsWidensIntStreamOffset(t *testing.T) {
	timestamp := time.Now()
	const pastMaxInt32 = int(1) << 32

	tests := []struct {
		name     string
		declared map[string]any
		wantArgs map[string]any
	}{
		{
			name:     "int_above_max_int32_widened_to_int64",
			declared: map[string]any{argStreamOffset: pastMaxInt32},
			wantArgs: map[string]any{argStreamOffset: int64(4294967296)},
		},
		{
			name:     "int_at_max_int32_widened_to_int64",
			declared: map[string]any{argStreamOffset: math.MaxInt32},
			wantArgs: map[string]any{argStreamOffset: int64(2147483647)},
		},
		{
			name:     "small_int_widened_without_mangling",
			declared: map[string]any{argStreamOffset: 7},
			wantArgs: map[string]any{argStreamOffset: int64(7)},
		},
		{
			name:     "zero_int_widened_without_mangling",
			declared: map[string]any{argStreamOffset: 0},
			wantArgs: map[string]any{argStreamOffset: int64(0)},
		},
		{
			name:     "int64_passes_through_untouched",
			declared: map[string]any{argStreamOffset: int64(4294967296)},
			wantArgs: map[string]any{argStreamOffset: int64(4294967296)},
		},
		{
			name:     "named_position_untouched",
			declared: map[string]any{argStreamOffset: streamOffsetFirst},
			wantArgs: map[string]any{argStreamOffset: streamOffsetFirst},
		},
		{
			name:     "interval_string_untouched",
			declared: map[string]any{argStreamOffset: "7D"},
			wantArgs: map[string]any{argStreamOffset: "7D"},
		},
		{
			name:     "timestamp_untouched",
			declared: map[string]any{argStreamOffset: timestamp},
			wantArgs: map[string]any{argStreamOffset: timestamp},
		},
		{
			// Only the offset is widened: unrelated int args keep their type, so
			// the fix's blast radius stays at the one key that needs it.
			name:     "other_args_survive_the_widening_copy",
			declared: map[string]any{argStreamOffset: pastMaxInt32, "x-priority": 5},
			wantArgs: map[string]any{argStreamOffset: int64(4294967296), "x-priority": 5},
		},
		{
			name:     "no_offset_arg_untouched",
			declared: map[string]any{"x-priority": 5},
			wantArgs: map[string]any{"x-priority": 5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewRegistry(&simpleMockAMQPClient{isReady: true}, &stubLogger{})
			snapshot := maps.Clone(tt.declared)

			opts := registry.consumeOptionsFor(&ConsumerDeclaration{
				Queue:    testStreamQueue,
				Consumer: testConsumer,
				Args:     tt.declared,
			}, nil)

			// assert.Equal compares dynamic types, so an un-widened int fails here.
			assert.Equal(t, tt.wantArgs, opts.Args)
			assert.Equal(t, snapshot, tt.declared,
				"the widening must land on a copy, never the declaration's map")
		})
	}
}

// TestStartConsumerWidensDeclaredIntOffset proves the widening survives the
// whole initial-subscribe path, not just consumeOptionsFor's return value.
func TestStartConsumerWidensDeclaredIntOffset(t *testing.T) {
	declared := map[string]any{argStreamOffset: int(1) << 32}
	registry, client, _ := newStreamResumeRegistry(t, true, declared)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, registry.StartConsumers(ctx))

	assert.Equal(t, map[string]any{argStreamOffset: int64(4294967296)},
		client.consumeOptionsAt(0).Args,
		"the client must receive the offset as int64 so amqp091 encodes it 64-bit")
	assert.Equal(t, map[string]any{argStreamOffset: int(1) << 32}, declared,
		"the declaration's map must be untouched")

	registry.StopConsumers()
}

func TestRegistryProcessMessageRecordsSettlementOutcome(t *testing.T) {
	tests := []struct {
		name        string
		handler     MessageHandler
		acker       *mockAcknowledger
		wantOutcome string
	}{
		{
			name:        "ack_success",
			handler:     &countingTestHandler{},
			acker:       &mockAcknowledger{},
			wantOutcome: tracking.OutcomeAcked,
		},
		{
			name:        "nack_success",
			handler:     &countingTestHandler{testHandler: testHandler{retErr: errors.New("handler error")}},
			acker:       &mockAcknowledger{},
			wantOutcome: tracking.OutcomeNacked,
		},
		{
			name:        "ack_failure",
			handler:     &countingTestHandler{},
			acker:       &mockAcknowledger{ackErr: errors.New("ack failed")},
			wantOutcome: tracking.OutcomeFailed,
		},
		{
			name:        "nack_failure",
			handler:     &countingTestHandler{testHandler: testHandler{retErr: errors.New("handler error")}},
			acker:       &mockAcknowledger{nackErr: errors.New("nack failed")},
			wantOutcome: tracking.OutcomeFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mp, cleanup := setupConsumeMetrics(t)
			defer cleanup()

			registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})
			consumer := &ConsumerDeclaration{
				Queue:     testQueueName,
				EventType: testEventType,
				Handler:   tt.handler,
				AutoAck:   false,
			}
			delivery := &amqp.Delivery{
				MessageId:    testMessageID,
				RoutingKey:   testRoutingKey,
				Exchange:     testExchangeName,
				DeliveryTag:  123,
				Body:         []byte(testMessageBody),
				Headers:      amqp.Table{},
				Acknowledger: tt.acker,
			}

			registry.processMessage(context.Background(), consumer, delivery, &stubLogger{})

			assertExactlyLaneAndOutcome(t, settlementAttrs(t, mp.Collect(t)), tracking.LaneClassic, tt.wantOutcome)
		})
	}
}

func TestRegistryProcessMessageAutoAckRecordsNoSettlement(t *testing.T) {
	mp, cleanup := setupConsumeMetrics(t)
	defer cleanup()

	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})
	consumer := &ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Handler:   &countingTestHandler{},
		AutoAck:   true,
	}
	delivery := &amqp.Delivery{
		DeliveryTag:  123,
		Body:         []byte(testMessageBody),
		Acknowledger: &mockAcknowledger{},
	}

	registry.processMessage(context.Background(), consumer, delivery, &stubLogger{})

	assert.Nil(t, obtest.FindMetric(mp.Collect(t), "messaging.settlement.total"),
		"AutoAck is not a settle call, so it must not increment the settlement counter")
}

func settlementAttrs(t *testing.T, rm metricdata.ResourceMetrics) []attribute.KeyValue {
	t.Helper()
	m := obtest.FindMetric(rm, "messaging.settlement.total")
	require.NotNil(t, m)
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sum.DataPoints, 1)
	assert.Equal(t, int64(1), sum.DataPoints[0].Value)
	return sum.DataPoints[0].Attributes.ToSlice()
}

func assertExactlyLaneAndOutcome(t *testing.T, attrs []attribute.KeyValue, lane, outcome string) {
	t.Helper()
	got := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		got[string(attr.Key)] = attr.Value.AsString()
	}
	assert.Equal(t, map[string]string{"lane": lane, "outcome": outcome}, got)
}

// staticRedeclareSource is a redeclare driver whose channel generation a test
// sets by hand. channelGenerationer is the whole of what a source is asked for,
// so this type carries no declare surface at all and a pass can only ever
// declare through the registry's own client.
type staticRedeclareSource struct{ generation uint64 }

func (s *staticRedeclareSource) channelGeneration() (generation uint64, ready bool) {
	return s.generation, true
}

// tokenFor gives a hand-driven source the ledger identity NewRegistry builds for
// the registry's own client.
func tokenFor(source channelGenerationer) *redeclareToken {
	return &redeclareToken{source: source}
}

// TestRegistryRedeclareGuardIsPerSource pins which pair the once-per-generation
// guard is keyed by: sources number their channels independently, so two
// sources reporting the same generation are two different channels and each
// earns its pass, while one source on a generation it already ran earns none.
func TestRegistryRedeclareGuardIsPerSource(t *testing.T) {
	client := newReconnectingMockClient()
	registry := newPublisherOnlyRegistry(t, client)
	defer registry.StopConsumers()
	key := "exchange:" + testExchangeName
	require.Equal(t, []string{"1"}, client.declaresOf(key), "DeclareInfrastructure declares once")

	ctx := context.Background()
	publisher, peer := tokenFor(&staticRedeclareSource{generation: 2}), tokenFor(&staticRedeclareSource{generation: 2})
	registry.redeclareTopologyFrom(ctx, publisher)
	registry.redeclareTopologyFrom(ctx, peer)
	assert.Equal(t, []string{"1", "1", "1"}, client.declaresOf(key), "two sources on the same generation are two channels")

	registry.redeclareTopologyFrom(ctx, publisher)
	assert.Equal(t, []string{"1", "1", "1"}, client.declaresOf(key), "one source twice on one generation is one pass")
}

// TestRegistryRefusesARedeclarePassOnceStopped pins the stop signal a source
// outside the registry cannot see: stopping the registry's own observer is not
// enough, so the pass itself refuses once StopConsumers has run.
func TestRegistryRefusesARedeclarePassOnceStopped(t *testing.T) {
	client := newReconnectingMockClient()
	registry := newPublisherOnlyRegistry(t, client)

	registry.StopConsumers()
	registry.redeclareTopologyFrom(context.Background(), tokenFor(&staticRedeclareSource{generation: 2}))

	assert.Equal(t, []string{"1"}, client.declaresOf("exchange:"+testExchangeName))
}

// TestRegistryRedeclaresOnTheFirstGenerationASourceIsSeenAt pins that a source's
// FIRST sighting declares rather than being adopted at whatever generation it
// happens to report. Adopting would leave the route missing on exactly the
// channel a first publish goes out on, and generation 1 is the case an adopting
// implementation would look most correct on: it is the number a freshly built
// client reports before it has ever rotated.
func TestRegistryRedeclaresOnTheFirstGenerationASourceIsSeenAt(t *testing.T) {
	client := newReconnectingMockClient()
	registry := newPublisherOnlyRegistry(t, client)
	defer registry.StopConsumers()
	key := "exchange:" + testExchangeName
	require.Equal(t, []string{"1"}, client.declaresOf(key), "DeclareInfrastructure declares once")

	registry.redeclareTopologyFrom(context.Background(), tokenFor(&staticRedeclareSource{generation: 1}))

	assert.Equal(t, []string{"1", "1"}, client.declaresOf(key),
		"an unseen source's generation 1 is a channel this registry has never declared on")
}

// TestRegistryRunsNoRedeclarePassBeforeDeclareInfrastructure pins the latch:
// DeclareInfrastructure owns the first declare, so a sighting that lands before
// it must declare nothing — and record nothing, or the generation it adopted
// would never earn its pass afterwards.
func TestRegistryRunsNoRedeclarePassBeforeDeclareInfrastructure(t *testing.T) {
	client := newReconnectingMockClient()
	registry := NewRegistry(client, &stubLogger{})
	registry.RegisterExchange(&ExchangeDeclaration{Name: testExchangeName, Type: ExchangeTypeTopic, Durable: true})
	registry.RegisterPublisher(&PublisherDeclaration{Exchange: testExchangeName, RoutingKey: testKeyValue, EventType: testEventType})
	defer registry.StopConsumers()
	key := "exchange:" + testExchangeName

	registry.redeclareTopologyFrom(context.Background(), tokenFor(&staticRedeclareSource{generation: 2}))

	assert.Empty(t, client.declaresOf(key), "an unlatched registry declares nothing")
	require.NoError(t, registry.DeclareInfrastructure(context.Background()))
	assert.Equal(t, []string{"1"}, client.declaresOf(key),
		"the registry did hold topology to replay, so the empty above was the latch")
}

// uncomparableClientWrapper is the client shape wiki/messaging.md blesses for a
// custom MessagingClientFactory — a struct EMBEDDING the framework's own client,
// which promotes both redeclare seams — handed over as a value. Its map field
// makes that value uncomparable, and an interface-keyed ledger cannot hash it.
type uncomparableClientWrapper struct {
	*reconnectingMockClient
	tags map[string]string
}

var _ redeclareSource = uncomparableClientWrapper{}

// TestRegistryRedeclaresThroughAnUncomparableClientWrapper pins that no ledger
// entry is ever keyed by the client value: the wrapper above would panic an
// interface-keyed map on the seed write and again on every pass, taking down the
// observer goroutine — and one frame up in the manager, skipping the client
// rollback, so each rebuilt client leaked its reconnect loop.
func TestRegistryRedeclaresThroughAnUncomparableClientWrapper(t *testing.T) {
	client := newReconnectingMockClient()
	wrapper := uncomparableClientWrapper{reconnectingMockClient: client, tags: map[string]string{"lane": "wrapped"}}
	registry := newPublisherOnlyRegistry(t, wrapper)
	defer registry.StopConsumers()

	client.newChannel()

	awaitDeclares(t, client, "exchange:"+testExchangeName, "1", "2")
}

// TestRegistryRepairsTopologyAfterAStopStartCycle pins that StopConsumers halts
// repair for the stop and not for the registry's life: StartConsumers re-arms it,
// so a restarted consumer's re-subscribe still re-declares, the way it did before
// the pass gained a halt gate.
func TestRegistryRepairsTopologyAfterAStopStartCycle(t *testing.T) {
	client := newReconnectingMockClient()
	handler := &countingTestHandler{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := startRedeclareRegistry(ctx, t, client, &stubLogger{}, handler)
	defer registry.StopConsumers()
	awaitSubscription(t, client, 0)

	registry.StopConsumers()
	require.NoError(t, registry.StartConsumers(ctx))
	restarted := awaitSubscription(t, client, 1)

	// Rotate through locked() alone: the stop ended the observer for good, so the
	// re-subscribe below is the only driver left to prove.
	client.locked(func() { client.generation++ })
	close(restarted)

	deliverAndAwaitAck(t, awaitSubscription(t, client, 2), handler)
	assert.Equal(t, []string{"1", "2"}, client.declaresOf("queue:"+testQueueName))
}

// TestObserveChannelReadyTakesTheBroadcastBeforeThePass pins the ordering that
// keeps a rotation landing mid-pass from being lost: the observer is already
// holding the current broadcast when it runs the pass, so a channel replaced
// meanwhile closes a broadcast it is about to select on. Running the pass first
// instead would rotate against no broadcast at all — the previous one is closed
// and dropped, the next not yet taken — and the wake would wait for a further
// rotation that may never come.
func TestObserveChannelReadyTakesTheBroadcastBeforeThePass(t *testing.T) {
	client := newReconnectingMockClient()
	stop := make(chan struct{})
	secondPass := make(chan struct{})
	observerDone := make(chan struct{})
	passes := 0 // touched only by the observer goroutine

	go func() {
		defer close(observerDone)
		observeChannelReady(client, stop, func() {
			passes++
			switch passes {
			case 1:
				client.newChannel() // the rotation lands while this pass runs
			case 2:
				close(secondPass)
			}
		})
	}()

	select {
	case <-secondPass:
	case <-time.After(5 * time.Second):
		t.Error("a rotation that landed during the pass woke nobody")
	}
	close(stop)
	<-observerDone
}

// TestRegistryStartsOneRedeclareObserverAcrossRepeatedDeclares pins the second
// job of DeclareInfrastructure's already-declared guard: it is what makes
// startRedeclaring run once. A second trip through it would start a second
// observer and overwrite cancelRedeclare, leaving the first goroutine with
// nothing left that can ever stop it.
func TestRegistryStartsOneRedeclareObserverAcrossRepeatedDeclares(t *testing.T) {
	client := newReconnectingMockClient()
	registry := newPublisherOnlyRegistry(t, client)
	first := registry.redeclareObserverDone
	require.NotNil(t, first)

	require.NoError(t, registry.DeclareInfrastructure(context.Background()))

	assert.Equal(t, first, registry.redeclareObserverDone, "a second DeclareInfrastructure started a second observer")
	registry.StopConsumers()
	awaitObserverExit(t, first)
}

// gatedRedeclareSource parks a pass inside channelGeneration — after it has taken
// redeclareMu and released mu, and before the replay that needs mu again — so a
// test can hold redeclareMu across a concurrent DeclareInfrastructure. Arrive
// blocks on EVERY call, so the loop's second reading returns only because
// Release already ran: moving Release after the pass completes would deadlock.
type gatedRedeclareSource struct {
	generation uint64
	gate       *testutil.BlockedCreate
}

func (s *gatedRedeclareSource) channelGeneration() (generation uint64, ready bool) {
	s.gate.Arrive()
	return s.generation, true
}

// TestDeclareInfrastructureTakesRedeclareMuBeforeMu pins the documented lock
// order. POSITIVE CONTROL: swapping the two Lock calls in DeclareInfrastructure
// deadlocks this test — the parked pass holds redeclareMu and then needs mu for
// its replay, while DeclareInfrastructure would be holding mu and waiting on
// redeclareMu, and both waits below time out. The one bounded wait is the sleep:
// a goroutine blocking on a mutex emits no signal to observe, and without it the
// released pass races DeclareInfrastructure to mu and the inverted order can
// finish before it deadlocks.
func TestDeclareInfrastructureTakesRedeclareMuBeforeMu(t *testing.T) {
	client := newReconnectingMockClient()
	registry := newPublisherOnlyRegistry(t, client)

	gate := testutil.NewBlockedCreate(t)
	passDone := make(chan struct{})
	go func() {
		defer close(passDone)
		registry.redeclareTopologyFrom(context.Background(), tokenFor(&gatedRedeclareSource{generation: 2, gate: gate}))
	}()
	<-gate.Started

	declareDone := make(chan error, 1)
	go func() { declareDone <- registry.DeclareInfrastructure(context.Background()) }()
	time.Sleep(50 * time.Millisecond)

	gate.Release()

	select {
	case <-passDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the parked pass never reached mu: DeclareInfrastructure holds it while waiting for redeclareMu")
	}
	select {
	case err := <-declareDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("DeclareInfrastructure never returned")
	}
	registry.StopConsumers()
}
