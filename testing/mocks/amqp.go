package mocks

import (
	"context"
	"maps"
	"slices"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/mock"

	"github.com/gaborage/go-bricks/messaging"
)

// PublishedFrame is one frame handed to MockAMQPClient.PublishToExchange, kept so
// a test can assert the destination a messaging.Publisher[T] chose rather than one
// the test itself re-spelled.
type PublishedFrame struct {
	Options messaging.PublishOptions
	Data    []byte
}

// clone returns a frame sharing no mutable state with f: the headers map is copied
// one level (declared headers are scalars — the depth the framework copies at too)
// and the payload is cloned. Both the capture and every read pass through it, so
// neither a caller reusing its buffer nor a test writing into a returned frame can
// reach a recorded one.
func (f PublishedFrame) clone() PublishedFrame {
	if f.Options.Headers != nil {
		headers := make(map[string]any, len(f.Options.Headers))
		maps.Copy(headers, f.Options.Headers)
		f.Options.Headers = headers
	}
	f.Data = slices.Clone(f.Data)

	return f
}

// MockAMQPClient provides a testify-based mock implementation of the messaging.AMQPClient interface.
// It extends MockMessagingClient with AMQP-specific operations for advanced testing scenarios.
//
// Example usage:
//
//	mockAMQP := mocks.NewMockAMQPClient()
//	mockAMQP.On("DeclareQueue", mock.Anything, mock.Anything).Return(nil)
//	mockAMQP.On("PublishToExchange", mock.Anything, mock.MatchedBy(func(opts messaging.PublishOptions) bool {
//		return opts.Exchange == "test.exchange"
//	}), mock.Anything).Return(nil)
type MockAMQPClient struct {
	*MockMessagingClient

	// AMQP infrastructure tracking for testing
	declaredQueues    map[string]bool
	declaredExchanges map[string]bool
	bindings          map[string]bool

	// Publish capture; guarded by the embedded MockMessagingClient's mutex.
	published []PublishedFrame
}

// NewMockAMQPClient creates a new mock AMQP client
func NewMockAMQPClient() *MockAMQPClient {
	return &MockAMQPClient{
		MockMessagingClient: NewMockMessagingClient(),
		declaredQueues:      make(map[string]bool),
		declaredExchanges:   make(map[string]bool),
		bindings:            make(map[string]bool),
	}
}

var _ messaging.AMQPClient = (*MockAMQPClient)(nil)

// PublishToExchange implements messaging.AMQPClient. The frame is captured before
// the expectation is consulted, so a call the mock answers with an error is recorded
// too — what was attempted is what a publish test asserts on.
func (m *MockAMQPClient) PublishToExchange(ctx context.Context, options messaging.PublishOptions, data []byte) error {
	m.capture(options, data)

	arguments := m.Called(ctx, options, data)
	return arguments.Error(0)
}

// capture records the frame (see PublishedFrame.clone for what is copied).
func (m *MockAMQPClient) capture(options messaging.PublishOptions, data []byte) {
	frame := PublishedFrame{Options: options, Data: data}.clone()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = append(m.published, frame)
}

// PublishedFrames returns every frame PublishToExchange received, oldest first, as
// a snapshot the caller owns outright.
func (m *MockAMQPClient) PublishedFrames() []PublishedFrame {
	m.mu.RLock()
	defer m.mu.RUnlock()

	frames := make([]PublishedFrame, 0, len(m.published))
	for _, frame := range m.published {
		frames = append(frames, frame.clone())
	}

	return frames
}

// LastPublishedFrame returns the most recent frame and whether there was one.
func (m *MockAMQPClient) LastPublishedFrame() (frame PublishedFrame, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.published) == 0 {
		return PublishedFrame{}, false
	}

	return m.published[len(m.published)-1].clone(), true
}

// ClearPublishedFrames drops the capture (for test cleanup); infrastructure
// tracking is left alone, ClearInfrastructure owns that.
func (m *MockAMQPClient) ClearPublishedFrames() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = nil
}

// ConsumeFromQueue implements messaging.AMQPClient
func (m *MockAMQPClient) ConsumeFromQueue(ctx context.Context, options messaging.ConsumeOptions) (<-chan amqp.Delivery, error) {
	arguments := m.Called(ctx, options)

	m.mu.Lock()
	if _, exists := m.messageChannels[options.Queue]; !exists {
		m.messageChannels[options.Queue] = make(chan amqp.Delivery, 100)
	}
	ch := m.messageChannels[options.Queue]
	m.mu.Unlock()

	if err := arguments.Error(1); err != nil {
		return nil, err
	}
	return ch, nil
}

// DeclareQueue implements messaging.AMQPClient
func (m *MockAMQPClient) DeclareQueue(ctx context.Context, queue *messaging.QueueDeclaration) error {
	arguments := m.Called(ctx, queue)
	err := arguments.Error(0)

	if err == nil && queue != nil {
		m.mu.Lock()
		m.declaredQueues[queue.Name] = true
		m.mu.Unlock()
	}

	return err
}

// DeclareExchange implements messaging.AMQPClient
func (m *MockAMQPClient) DeclareExchange(ctx context.Context, exchange *messaging.ExchangeDeclaration) error {
	arguments := m.Called(ctx, exchange)
	err := arguments.Error(0)

	if err == nil && exchange != nil {
		m.mu.Lock()
		m.declaredExchanges[exchange.Name] = true
		m.mu.Unlock()
	}

	return err
}

// BindQueue implements messaging.AMQPClient
func (m *MockAMQPClient) BindQueue(ctx context.Context, binding *messaging.BindingDeclaration) error {
	arguments := m.Called(ctx, binding)
	err := arguments.Error(0)

	if err == nil && binding != nil {
		bindingKey := binding.Queue + ":" + binding.Exchange + ":" + binding.RoutingKey
		m.mu.Lock()
		m.bindings[bindingKey] = true
		m.mu.Unlock()
	}

	return err
}

// Helper methods for testing scenarios

// IsQueueDeclared returns true if the queue was declared
func (m *MockAMQPClient) IsQueueDeclared(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.declaredQueues[name]
}

// IsExchangeDeclared returns true if the exchange was declared
func (m *MockAMQPClient) IsExchangeDeclared(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.declaredExchanges[name]
}

// IsQueueBound returns true if the queue is bound to the exchange with the routing key
func (m *MockAMQPClient) IsQueueBound(queue, exchange, routingKey string) bool {
	bindingKey := queue + ":" + exchange + ":" + routingKey
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.bindings[bindingKey]
}

// ClearInfrastructure clears all declared infrastructure (for test cleanup)
func (m *MockAMQPClient) ClearInfrastructure() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.declaredQueues = make(map[string]bool)
	m.declaredExchanges = make(map[string]bool)
	m.bindings = make(map[string]bool)
}

// ExpectPublishToExchange sets up a publish to exchange expectation
func (m *MockAMQPClient) ExpectPublishToExchange(options messaging.PublishOptions, data []byte, err error) *mock.Call {
	return m.On("PublishToExchange", mock.Anything, options, data).Return(err)
}

// ExpectPublishToExchangeAny sets up a publish to exchange expectation for any options and data
func (m *MockAMQPClient) ExpectPublishToExchangeAny(err error) *mock.Call {
	return m.On("PublishToExchange", mock.Anything, mock.Anything, mock.Anything).Return(err)
}

// ExpectConsumeFromQueue sets up a consume from queue expectation
func (m *MockAMQPClient) ExpectConsumeFromQueue(options messaging.ConsumeOptions, err error) *mock.Call {
	return m.On("ConsumeFromQueue", mock.Anything, options).Return(nil, err)
}

// declOrAny returns the testify matcher for a declaration parameter: a nil
// declaration falls back to mock.Anything (match any declaration), otherwise
// the declaration is matched by value via testify's reflect.DeepEqual — an
// equivalent struct matches, it need not be the same pointer.
func declOrAny[T any](decl *T) any {
	if decl == nil {
		return mock.Anything
	}
	return decl
}

// ExpectDeclareQueue sets up a declare queue expectation; a nil declaration matches any (see declOrAny).
func (m *MockAMQPClient) ExpectDeclareQueue(queue *messaging.QueueDeclaration, err error) *mock.Call {
	return m.On("DeclareQueue", mock.Anything, declOrAny(queue)).Return(err)
}

// ExpectDeclareExchange sets up a declare exchange expectation; a nil declaration matches any (see declOrAny).
func (m *MockAMQPClient) ExpectDeclareExchange(exchange *messaging.ExchangeDeclaration, err error) *mock.Call {
	return m.On("DeclareExchange", mock.Anything, declOrAny(exchange)).Return(err)
}

// ExpectBindQueue sets up a bind queue expectation; a nil declaration matches any (see declOrAny).
func (m *MockAMQPClient) ExpectBindQueue(binding *messaging.BindingDeclaration, err error) *mock.Call {
	return m.On("BindQueue", mock.Anything, declOrAny(binding)).Return(err)
}

// ExpectDeclareExchangeAny sets up a declare exchange expectation for any parameters
func (m *MockAMQPClient) ExpectDeclareExchangeAny(err error) *mock.Call {
	return m.On("DeclareExchange", mock.Anything, mock.Anything).Return(err)
}

// ExpectDeclareQueueAny sets up a declare queue expectation for any parameters
func (m *MockAMQPClient) ExpectDeclareQueueAny(err error) *mock.Call {
	return m.On("DeclareQueue", mock.Anything, mock.Anything).Return(err)
}

// ExpectBindQueueAny sets up a bind queue expectation for any parameters
func (m *MockAMQPClient) ExpectBindQueueAny(err error) *mock.Call {
	return m.On("BindQueue", mock.Anything, mock.Anything).Return(err)
}
