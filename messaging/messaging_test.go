package messaging

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
)

// MockClient implements the Client interface for testing
type MockClient struct {
	isReady bool
	closed  bool
}

func (m *MockClient) Publish(_ context.Context, _ string, _ []byte) error {
	if !m.isReady {
		return errNotConnected
	}
	if m.closed {
		return errAlreadyClosed
	}
	return nil
}

func (m *MockClient) Consume(_ context.Context, _ string) (<-chan amqp.Delivery, error) {
	if !m.isReady {
		return nil, errNotConnected
	}
	if m.closed {
		return nil, errAlreadyClosed
	}

	ch := make(chan amqp.Delivery)
	close(ch) // Close immediately for testing
	return ch, nil
}

func (m *MockClient) Close() error {
	if m.closed {
		return errAlreadyClosed
	}
	m.closed = true
	m.isReady = false
	return nil
}

func (m *MockClient) IsReady() bool {
	return m.isReady && !m.closed
}

// MockAMQPClient implements the AMQPClient interface for testing
type MockAMQPClient struct {
	*MockClient
	queues    map[string]bool
	exchanges map[string]bool
	bindings  map[string]bool
}

func NewMockAMQPClient() *MockAMQPClient {
	return &MockAMQPClient{
		MockClient: &MockClient{isReady: true, closed: false},
		queues:     make(map[string]bool),
		exchanges:  make(map[string]bool),
		bindings:   make(map[string]bool),
	}
}

func (m *MockAMQPClient) PublishToExchange(_ context.Context, _ PublishOptions, _ []byte) error {
	if !m.isReady {
		return errNotConnected
	}
	if m.closed {
		return errAlreadyClosed
	}
	return nil
}

func (m *MockAMQPClient) ConsumeFromQueue(_ context.Context, _ ConsumeOptions) (<-chan amqp.Delivery, error) {
	if !m.isReady {
		return nil, errNotConnected
	}
	if m.closed {
		return nil, errAlreadyClosed
	}

	ch := make(chan amqp.Delivery)
	close(ch) // Close immediately for testing
	return ch, nil
}

func (m *MockAMQPClient) DeclareQueue(name string, _, _, _, _ bool) error {
	if !m.isReady {
		return errNotConnected
	}
	if m.closed {
		return errAlreadyClosed
	}
	m.queues[name] = true
	return nil
}

func (m *MockAMQPClient) DeclareExchange(name, _ string, _, _, _, _ bool) error {
	if !m.isReady {
		return errNotConnected
	}
	if m.closed {
		return errAlreadyClosed
	}
	m.exchanges[name] = true
	return nil
}

func (m *MockAMQPClient) BindQueue(queue, exchange, routingKey string, _ bool) error {
	if !m.isReady {
		return errNotConnected
	}
	if m.closed {
		return errAlreadyClosed
	}
	bindingKey := queue + ":" + exchange + ":" + routingKey
	m.bindings[bindingKey] = true
	return nil
}

// MockMessageHandler implements the MessageHandler interface for testing
type MockMessageHandler struct {
	eventType string
	handled   []amqp.Delivery
	shouldErr bool
}

func (m *MockMessageHandler) Handle(_ context.Context, delivery *amqp.Delivery) error {
	m.handled = append(m.handled, *delivery)
	if m.shouldErr {
		return assert.AnError
	}
	return nil
}

func (m *MockMessageHandler) EventType() string {
	return m.eventType
}

func TestPublishOptions(t *testing.T) {
	options := PublishOptions{
		Exchange:   "test-exchange",
		RoutingKey: "test.route",
		Headers:    map[string]interface{}{"test": "value"},
		Mandatory:  true,
		Immediate:  false,
	}

	assert.Equal(t, "test-exchange", options.Exchange)
	assert.Equal(t, "test.route", options.RoutingKey)
	assert.Equal(t, "value", options.Headers["test"])
	assert.True(t, options.Mandatory)
	assert.False(t, options.Immediate)
}

func TestConsumeOptions(t *testing.T) {
	options := ConsumeOptions{
		Queue:     "test-queue",
		Consumer:  "test-consumer",
		AutoAck:   true,
		Exclusive: false,
		NoLocal:   false,
		NoWait:    true,
	}

	assert.Equal(t, "test-queue", options.Queue)
	assert.Equal(t, "test-consumer", options.Consumer)
	assert.True(t, options.AutoAck)
	assert.False(t, options.Exclusive)
	assert.False(t, options.NoLocal)
	assert.True(t, options.NoWait)
}

func TestMockClientPublish(t *testing.T) {
	tests := []struct {
		name        string
		isReady     bool
		closed      bool
		expectError bool
	}{
		{
			name:        "successful_publish",
			isReady:     true,
			closed:      false,
			expectError: false,
		},
		{
			name:        "not_ready",
			isReady:     false,
			closed:      false,
			expectError: true,
		},
		{
			name:        "client_closed",
			isReady:     true,
			closed:      true,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &MockClient{
				isReady: tt.isReady,
				closed:  tt.closed,
			}

			ctx := context.Background()
			err := client.Publish(ctx, "test-destination", []byte("test message"))

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMockClientConsume(t *testing.T) {
	tests := []struct {
		name        string
		isReady     bool
		closed      bool
		expectError bool
	}{
		{
			name:        "successful_consume",
			isReady:     true,
			closed:      false,
			expectError: false,
		},
		{
			name:        "not_ready",
			isReady:     false,
			closed:      false,
			expectError: true,
		},
		{
			name:        "client_closed",
			isReady:     true,
			closed:      true,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &MockClient{
				isReady: tt.isReady,
				closed:  tt.closed,
			}

			ctx := context.Background()
			ch, err := client.Consume(ctx, "test-destination")

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, ch)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, ch)
			}
		})
	}
}

func TestMockClientClose(t *testing.T) {
	client := &MockClient{
		isReady: true,
		closed:  false,
	}

	// First close should succeed
	err := client.Close()
	assert.NoError(t, err)
	assert.False(t, client.IsReady())

	// Second close should return error
	err = client.Close()
	assert.Error(t, err)
	assert.Equal(t, errAlreadyClosed, err)
}

func TestMockClientIsReady(t *testing.T) {
	client := &MockClient{
		isReady: true,
		closed:  false,
	}

	assert.True(t, client.IsReady())

	client.isReady = false
	assert.False(t, client.IsReady())

	client.isReady = true
	client.closed = true
	assert.False(t, client.IsReady())
}

func TestMockAMQPClientPublishToExchange(t *testing.T) {
	client := NewMockAMQPClient()

	ctx := context.Background()
	options := PublishOptions{
		Exchange:   "test-exchange",
		RoutingKey: "test.route",
	}

	err := client.PublishToExchange(ctx, options, []byte("test message"))
	assert.NoError(t, err)
}

func TestMockAMQPClientConsumeFromQueue(t *testing.T) {
	client := NewMockAMQPClient()

	ctx := context.Background()
	options := ConsumeOptions{
		Queue:    "test-queue",
		Consumer: "test-consumer",
	}

	ch, err := client.ConsumeFromQueue(ctx, options)
	assert.NoError(t, err)
	assert.NotNil(t, ch)
}

func TestMockAMQPClientDeclareQueue(t *testing.T) {
	client := NewMockAMQPClient()

	err := client.DeclareQueue("test-queue", true, false, false, false)
	assert.NoError(t, err)
	assert.True(t, client.queues["test-queue"])
}

func TestMockAMQPClientDeclareExchange(t *testing.T) {
	client := NewMockAMQPClient()

	err := client.DeclareExchange("test-exchange", "topic", true, false, false, false)
	assert.NoError(t, err)
	assert.True(t, client.exchanges["test-exchange"])
}

func TestMockAMQPClientBindQueue(t *testing.T) {
	client := NewMockAMQPClient()

	err := client.BindQueue("test-queue", "test-exchange", "test.route", false)
	assert.NoError(t, err)

	bindingKey := "test-queue:test-exchange:test.route"
	assert.True(t, client.bindings[bindingKey])
}

func TestMockAMQPClientNotReady(t *testing.T) {
	client := NewMockAMQPClient()
	client.isReady = false

	ctx := context.Background()

	// Test PublishToExchange
	err := client.PublishToExchange(ctx, PublishOptions{}, []byte("test"))
	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)

	// Test ConsumeFromQueue
	ch, err := client.ConsumeFromQueue(ctx, ConsumeOptions{})
	assert.Error(t, err)
	assert.Nil(t, ch)
	assert.Equal(t, errNotConnected, err)

	// Test DeclareQueue
	err = client.DeclareQueue("test", true, false, false, false)
	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)

	// Test DeclareExchange
	err = client.DeclareExchange("test", "topic", true, false, false, false)
	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)

	// Test BindQueue
	err = client.BindQueue("queue", "exchange", "route", false)
	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)
}

func TestMockAMQPClientClosed(t *testing.T) {
	client := NewMockAMQPClient()
	client.closed = true

	ctx := context.Background()

	// Test PublishToExchange
	err := client.PublishToExchange(ctx, PublishOptions{}, []byte("test"))
	assert.Error(t, err)
	assert.Equal(t, errAlreadyClosed, err)

	// Test ConsumeFromQueue
	ch, err := client.ConsumeFromQueue(ctx, ConsumeOptions{})
	assert.Error(t, err)
	assert.Nil(t, ch)
	assert.Equal(t, errAlreadyClosed, err)
}

func TestMockMessageHandlerHandle(t *testing.T) {
	handler := &MockMessageHandler{
		eventType: "test.event",
		shouldErr: false,
	}

	delivery := amqp.Delivery{
		Body: []byte("test message"),
	}

	ctx := context.Background()
	err := handler.Handle(ctx, &delivery)
	assert.NoError(t, err)
	assert.Len(t, handler.handled, 1)
	assert.Equal(t, "test message", string(handler.handled[0].Body))
}

func TestMockMessageHandlerHandleError(t *testing.T) {
	handler := &MockMessageHandler{
		eventType: "test.event",
		shouldErr: true,
	}

	delivery := amqp.Delivery{
		Body: []byte("test message"),
	}

	ctx := context.Background()
	err := handler.Handle(ctx, &delivery)
	assert.Error(t, err)
	assert.Len(t, handler.handled, 1)
}

func TestMockMessageHandlerEventType(t *testing.T) {
	handler := &MockMessageHandler{
		eventType: "test.event.type",
	}

	assert.Equal(t, "test.event.type", handler.EventType())
}

func TestInterfaceCompliance(t *testing.T) {
	// Test that our mocks implement the interfaces correctly
	var client Client = &MockClient{}
	assert.NotNil(t, client)

	var amqpClient AMQPClient = NewMockAMQPClient()
	assert.NotNil(t, amqpClient)

	var handler MessageHandler = &MockMessageHandler{}
	assert.NotNil(t, handler)

	// Test that AMQPClient extends Client
	var clientFromAMQP Client = NewMockAMQPClient()
	assert.NotNil(t, clientFromAMQP)
}

func TestErrorConstants(t *testing.T) {
	// Test that error constants are properly defined
	assert.Equal(t, "not connected to AMQP broker", errNotConnected.Error())
	assert.Equal(t, "AMQP client already closed", errAlreadyClosed.Error())
	assert.Equal(t, "AMQP client is shutting down", errShutdown.Error())
}
