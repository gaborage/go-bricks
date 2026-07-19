package messaging

import (
	"context"
	"errors"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingAMQPClient implements AMQPClient and records every call so the
// tenantAwarePublisher wrapper's delegation can be verified.
type recordingAMQPClient struct {
	publishCalls         []PublishOptions
	publishErr           error
	consumeCalls         []string
	consumeErr           error
	consumeFromQueue     []ConsumeOptions
	consumeFromQueueErr  error
	declareQueueCalls    []string
	declareQueueArgs     []map[string]any
	declareQueueErr      error
	declareExchangeCalls []string
	declareExchangeArgs  []map[string]any
	declareExchangeErr   error
	bindQueueCalls       [][3]string
	bindQueueArgs        []map[string]any
	bindQueueErr         error
	closeErr             error
	closed               bool
	ready                bool
}

func (r *recordingAMQPClient) Publish(ctx context.Context, destination string, data []byte) error {
	return r.PublishToExchange(ctx, PublishOptions{Exchange: "", RoutingKey: destination}, data)
}

func (r *recordingAMQPClient) PublishToExchange(_ context.Context, options PublishOptions, _ []byte) error {
	r.publishCalls = append(r.publishCalls, options)
	return r.publishErr
}

func (r *recordingAMQPClient) Consume(_ context.Context, destination string) (<-chan amqp.Delivery, error) {
	r.consumeCalls = append(r.consumeCalls, destination)
	if r.consumeErr != nil {
		return nil, r.consumeErr
	}
	ch := make(chan amqp.Delivery)
	close(ch)
	return ch, nil
}

func (r *recordingAMQPClient) ConsumeFromQueue(_ context.Context, options ConsumeOptions) (<-chan amqp.Delivery, error) {
	r.consumeFromQueue = append(r.consumeFromQueue, options)
	if r.consumeFromQueueErr != nil {
		return nil, r.consumeFromQueueErr
	}
	ch := make(chan amqp.Delivery)
	close(ch)
	return ch, nil
}

func (r *recordingAMQPClient) DeclareQueue(_ context.Context, queue *QueueDeclaration) error {
	r.declareQueueCalls = append(r.declareQueueCalls, queue.Name)
	r.declareQueueArgs = append(r.declareQueueArgs, queue.Args)
	return r.declareQueueErr
}

func (r *recordingAMQPClient) DeclareExchange(_ context.Context, exchange *ExchangeDeclaration) error {
	r.declareExchangeCalls = append(r.declareExchangeCalls, exchange.Name)
	r.declareExchangeArgs = append(r.declareExchangeArgs, exchange.Args)
	return r.declareExchangeErr
}

func (r *recordingAMQPClient) BindQueue(_ context.Context, binding *BindingDeclaration) error {
	r.bindQueueCalls = append(r.bindQueueCalls, [3]string{binding.Queue, binding.Exchange, binding.RoutingKey})
	r.bindQueueArgs = append(r.bindQueueArgs, binding.Args)
	return r.bindQueueErr
}

func (r *recordingAMQPClient) Close() error {
	r.closed = true
	return r.closeErr
}

func (r *recordingAMQPClient) IsReady() bool {
	return r.ready
}

func TestNewTenantAwarePublisherEmptyKeyReturnsBase(t *testing.T) {
	base := &recordingAMQPClient{}
	got := newTenantAwarePublisher(base, "")
	assert.Same(t, AMQPClient(base), got, "empty key should bypass wrapping")
}

func TestNewTenantAwarePublisherNonEmptyKeyWraps(t *testing.T) {
	base := &recordingAMQPClient{}
	got := newTenantAwarePublisher(base, tenant1ID)
	wrapper, ok := got.(*tenantAwarePublisher)
	require.True(t, ok, "non-empty key should return *tenantAwarePublisher")
	assert.Equal(t, tenant1ID, wrapper.key)
	assert.Same(t, AMQPClient(base), wrapper.base)
}

func TestTenantAwarePublisherPublishInjectsTenantHeader(t *testing.T) {
	base := &recordingAMQPClient{}
	pub := newTenantAwarePublisher(base, tenant1ID)

	require.NoError(t, pub.Publish(context.Background(), testRoutingKey, []byte("body")))
	require.Len(t, base.publishCalls, 1)
	got := base.publishCalls[0]
	assert.Equal(t, "", got.Exchange, "Publish targets the default exchange")
	assert.Equal(t, testRoutingKey, got.RoutingKey)
	assert.Equal(t, tenant1ID, got.Headers[tenantHeader])
}

func TestTenantAwarePublisherPublishToExchangeAddsHeaderWhenNil(t *testing.T) {
	base := &recordingAMQPClient{}
	pub := newTenantAwarePublisher(base, tenant1ID)

	opts := PublishOptions{Exchange: testExchange, RoutingKey: testRoutingKey}
	require.Nil(t, opts.Headers)
	require.NoError(t, pub.PublishToExchange(context.Background(), opts, []byte("body")))

	require.Len(t, base.publishCalls, 1)
	assert.Equal(t, tenant1ID, base.publishCalls[0].Headers[tenantHeader])
	assert.Nil(t, opts.Headers, "caller's Headers map must not be mutated")
}

func TestTenantAwarePublisherPublishToExchangeClonesExistingHeaders(t *testing.T) {
	base := &recordingAMQPClient{}
	pub := newTenantAwarePublisher(base, tenant1ID)

	original := map[string]any{testKey: testValue}
	opts := PublishOptions{Exchange: testExchange, RoutingKey: testRoutingKey, Headers: original}
	require.NoError(t, pub.PublishToExchange(context.Background(), opts, []byte("body")))

	require.Len(t, base.publishCalls, 1)
	got := base.publishCalls[0]
	assert.Equal(t, tenant1ID, got.Headers[tenantHeader])
	assert.Equal(t, testValue, got.Headers[testKey])

	_, hadTenantInOriginal := original[tenantHeader]
	assert.False(t, hadTenantInOriginal, "the caller's headers map must not gain a tenant entry (must be cloned)")
}

func TestTenantAwarePublisherPublishToExchangePropagatesError(t *testing.T) {
	wantErr := errors.New("publish failed")
	base := &recordingAMQPClient{publishErr: wantErr}
	pub := newTenantAwarePublisher(base, tenant1ID)

	err := pub.PublishToExchange(context.Background(), PublishOptions{Exchange: testExchange, RoutingKey: testRoutingKey}, []byte("body"))
	assert.ErrorIs(t, err, wantErr)
}

func TestTenantAwarePublisherConsumeDelegates(t *testing.T) {
	base := &recordingAMQPClient{}
	pub := newTenantAwarePublisher(base, tenant1ID)

	ch, err := pub.Consume(context.Background(), testQueue)
	require.NoError(t, err)
	require.NotNil(t, ch)
	assert.Equal(t, []string{testQueue}, base.consumeCalls)
}

func TestTenantAwarePublisherConsumeFromQueueDelegates(t *testing.T) {
	base := &recordingAMQPClient{}
	pub := newTenantAwarePublisher(base, tenant1ID)

	opts := ConsumeOptions{Queue: testQueue, Consumer: testConsumer}
	ch, err := pub.ConsumeFromQueue(context.Background(), opts)
	require.NoError(t, err)
	require.NotNil(t, ch)
	require.Len(t, base.consumeFromQueue, 1)
	assert.Equal(t, opts, base.consumeFromQueue[0])
}

func TestTenantAwarePublisherDeclareQueueDelegates(t *testing.T) {
	base := &recordingAMQPClient{}
	pub := newTenantAwarePublisher(base, tenant1ID)

	queueArgs := map[string]any{"x-dead-letter-exchange": "orders.dlx"}
	require.NoError(t, pub.DeclareQueue(t.Context(), &QueueDeclaration{Name: testQueue, Durable: true, Args: queueArgs}))
	assert.Equal(t, []string{testQueue}, base.declareQueueCalls)
	assert.Equal(t, []map[string]any{{"x-dead-letter-exchange": "orders.dlx"}}, base.declareQueueArgs, "args must delegate unchanged")
}

func TestTenantAwarePublisherDeclareExchangeDelegates(t *testing.T) {
	base := &recordingAMQPClient{}
	pub := newTenantAwarePublisher(base, tenant1ID)

	exchangeArgs := map[string]any{"alternate-exchange": "orders.alt"}
	require.NoError(t, pub.DeclareExchange(t.Context(), &ExchangeDeclaration{Name: testExchange, Type: exchangeTypeTopic, Durable: true, Args: exchangeArgs}))
	assert.Equal(t, []string{testExchange}, base.declareExchangeCalls)
	assert.Equal(t, []map[string]any{{"alternate-exchange": "orders.alt"}}, base.declareExchangeArgs, "args must delegate unchanged")
}

func TestTenantAwarePublisherBindQueueDelegates(t *testing.T) {
	base := &recordingAMQPClient{}
	pub := newTenantAwarePublisher(base, tenant1ID)

	bindingArgs := map[string]any{"x-match": "all"}
	require.NoError(t, pub.BindQueue(t.Context(), &BindingDeclaration{Queue: testQueue, Exchange: testExchange, RoutingKey: testRoutingKey, Args: bindingArgs}))
	require.Len(t, base.bindQueueCalls, 1)
	assert.Equal(t, [3]string{testQueue, testExchange, testRoutingKey}, base.bindQueueCalls[0])
	assert.Equal(t, []map[string]any{{"x-match": "all"}}, base.bindQueueArgs, "args must delegate unchanged")
}

func TestTenantAwarePublisherCloseDelegates(t *testing.T) {
	base := &recordingAMQPClient{}
	pub := newTenantAwarePublisher(base, tenant1ID)

	require.NoError(t, pub.Close())
	assert.True(t, base.closed)
}

func TestTenantAwarePublisherCloseError(t *testing.T) {
	wantErr := errors.New("close failed")
	base := &recordingAMQPClient{closeErr: wantErr}
	pub := newTenantAwarePublisher(base, tenant1ID)

	err := pub.Close()
	assert.ErrorIs(t, err, wantErr)
}

func TestTenantAwarePublisherIsReadyDelegates(t *testing.T) {
	base := &recordingAMQPClient{ready: true}
	pub := newTenantAwarePublisher(base, tenant1ID)
	assert.True(t, pub.IsReady())

	base.ready = false
	assert.False(t, pub.IsReady())
}
