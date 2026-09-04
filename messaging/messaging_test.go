package messaging

import (
	"context"
	"reflect"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockClient implements the Client interface for testing
type MockClient struct {
	isReady bool
	closed  bool
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

func (m *MockAMQPClient) publishBytes(_ context.Context, _ publishOptions, _ []byte) error {
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

func (m *MockAMQPClient) DeclareQueue(_ context.Context, queue *QueueDeclaration) error {
	if !m.isReady {
		return errNotConnected
	}
	if m.closed {
		return errAlreadyClosed
	}
	m.queues[queue.Name] = true
	return nil
}

func (m *MockAMQPClient) DeclareExchange(_ context.Context, exchange *ExchangeDeclaration) error {
	if !m.isReady {
		return errNotConnected
	}
	if m.closed {
		return errAlreadyClosed
	}
	m.exchanges[exchange.Name] = true
	return nil
}

func (m *MockAMQPClient) BindQueue(_ context.Context, binding *BindingDeclaration) error {
	if !m.isReady {
		return errNotConnected
	}
	if m.closed {
		return errAlreadyClosed
	}
	bindingKey := binding.Queue + ":" + binding.Exchange + ":" + binding.RoutingKey
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
	options := publishOptions{
		Exchange:   testExchange,
		RoutingKey: testRoute,
		Headers:    map[string]any{"test": "value"},
		Mandatory:  true,
		Immediate:  false,
	}

	assert.Equal(t, testExchange, options.Exchange)
	assert.Equal(t, testRoute, options.RoutingKey)
	assert.Equal(t, "value", options.Headers["test"])
	assert.True(t, options.Mandatory)
	assert.False(t, options.Immediate)
}

func TestConsumeOptions(t *testing.T) {
	options := ConsumeOptions{
		Queue:     testQueue,
		Consumer:  testConsumer,
		AutoAck:   true,
		Exclusive: false,
		NoLocal:   false,
		NoWait:    true,
	}

	assert.Equal(t, testQueue, options.Queue)
	assert.Equal(t, testConsumer, options.Consumer)
	assert.True(t, options.AutoAck)
	assert.False(t, options.Exclusive)
	assert.False(t, options.NoLocal)
	assert.True(t, options.NoWait)
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

func TestMockAMQPClientPublishBytes(t *testing.T) {
	client := NewMockAMQPClient()

	ctx := context.Background()
	options := publishOptions{
		Exchange:   testExchange,
		RoutingKey: testRoute,
	}

	err := client.publishBytes(ctx, options, []byte(testMessage))
	assert.NoError(t, err)
}

func TestMockAMQPClientConsumeFromQueue(t *testing.T) {
	client := NewMockAMQPClient()

	ctx := context.Background()
	options := ConsumeOptions{
		Queue:    testQueue,
		Consumer: testConsumer,
	}

	ch, err := client.ConsumeFromQueue(ctx, options)
	assert.NoError(t, err)
	assert.NotNil(t, ch)
}

func TestMockAMQPClientDeclareQueue(t *testing.T) {
	client := NewMockAMQPClient()

	err := client.DeclareQueue(context.Background(), &QueueDeclaration{Name: testQueue, Durable: true})
	assert.NoError(t, err)
	assert.True(t, client.queues[testQueue])
}

func TestMockAMQPClientDeclareExchange(t *testing.T) {
	client := NewMockAMQPClient()

	err := client.DeclareExchange(context.Background(), &ExchangeDeclaration{Name: testExchange, Type: "topic", Durable: true})
	assert.NoError(t, err)
	assert.True(t, client.exchanges[testExchange])
}

func TestMockAMQPClientBindQueue(t *testing.T) {
	client := NewMockAMQPClient()

	err := client.BindQueue(context.Background(), &BindingDeclaration{Queue: testQueue, Exchange: testExchange, RoutingKey: testRoute})
	assert.NoError(t, err)

	bindingKey := "test-queue:test-exchange:test.route"
	assert.True(t, client.bindings[bindingKey])
}

func TestMockAMQPClientNotReady(t *testing.T) {
	client := NewMockAMQPClient()
	client.isReady = false

	ctx := context.Background()

	// Test publishBytes
	err := client.publishBytes(ctx, publishOptions{}, []byte("test"))
	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)

	// Test ConsumeFromQueue
	ch, err := client.ConsumeFromQueue(ctx, ConsumeOptions{})
	assert.Error(t, err)
	assert.Nil(t, ch)
	assert.Equal(t, errNotConnected, err)

	// Test DeclareQueue
	err = client.DeclareQueue(context.Background(), &QueueDeclaration{Name: "test", Durable: true})
	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)

	// Test DeclareExchange
	err = client.DeclareExchange(context.Background(), &ExchangeDeclaration{Name: "test", Type: "topic", Durable: true})
	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)

	// Test BindQueue
	err = client.BindQueue(context.Background(), &BindingDeclaration{Queue: "queue", Exchange: "exchange", RoutingKey: "route"})
	assert.Error(t, err)
	assert.Equal(t, errNotConnected, err)
}

func TestMockAMQPClientClosed(t *testing.T) {
	client := NewMockAMQPClient()
	client.closed = true

	ctx := context.Background()

	// Test publishBytes
	err := client.publishBytes(ctx, publishOptions{}, []byte("test"))
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
		Body: []byte(testMessage),
	}

	ctx := context.Background()
	err := handler.Handle(ctx, &delivery)
	assert.NoError(t, err)
	assert.Len(t, handler.handled, 1)
	assert.Equal(t, testMessage, string(handler.handled[0].Body))
}

func TestMockMessageHandlerHandleError(t *testing.T) {
	handler := &MockMessageHandler{
		eventType: "test.event",
		shouldErr: true,
	}

	delivery := amqp.Delivery{
		Body: []byte(testMessage),
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

// TestModuleFacingTypesCarryNoBytePublishDoor is ADR-096's acceptance check: the
// types a module can hold — Client, AMQPClient and the framework's own client and
// wrapper as seen THROUGH those interfaces — expose neither Publish nor
// PublishToExchange, so no exported path hands bytes to the broker. The
// unexported bytePublisher is reachable only from inside this package.
func TestModuleFacingTypesCarryNoBytePublishDoor(t *testing.T) {
	for name, typ := range map[string]reflect.Type{
		"Client":     reflect.TypeOf((*Client)(nil)).Elem(),
		"AMQPClient": reflect.TypeOf((*AMQPClient)(nil)).Elem(),
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, reflect.Interface, typ.Kind())
			assertNoByteDoor(t, typ)
		})
	}

	// The concrete types too: a module that type-asserts its client to the
	// framework implementation must find no exported byte door either.
	for name, typ := range map[string]reflect.Type{
		"AMQPClientImpl":    reflect.TypeOf((*AMQPClientImpl)(nil)),
		"stampingPublisher": reflect.TypeOf((*stampingPublisher)(nil)),
	} {
		t.Run(name, func(t *testing.T) {
			assertNoByteDoor(t, typ)
		})
	}
}

// assertNoByteDoor checks the EXPORTED method set (reflect lists only exported
// methods for a non-interface type, and every method for an interface).
func assertNoByteDoor(t *testing.T, typ reflect.Type) {
	t.Helper()
	for _, name := range []string{"Publish", "PublishToExchange", "PublishBytes"} {
		_, found := typ.MethodByName(name)
		assert.Falsef(t, found, "%s must not expose %s", typ, name)
	}
}
