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
	declareQueueErr      error
	declareExchangeCalls []string
	declareExchangeErr   error
	bindQueueCalls       [][3]string
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

func (r *recordingAMQPClient) DeclareQueue(name string, _, _, _, _ bool) error {
	r.declareQueueCalls = append(r.declareQueueCalls, name)
	return r.declareQueueErr
}

func (r *recordingAMQPClient) DeclareExchange(name, _ string, _, _, _, _ bool) error {
	r.declareExchangeCalls = append(r.declareExchangeCalls, name)
	return r.declareExchangeErr
}

func (r *recordingAMQPClient) BindQueue(queue, exchange, routingKey string, _ bool) error {
	r.bindQueueCalls = append(r.bindQueueCalls, [3]string{queue, exchange, routingKey})
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

	require.NoError(t, pub.DeclareQueue(testQueue, true, false, false, false))
	assert.Equal(t, []string{testQueue}, base.declareQueueCalls)
}

func TestTenantAwarePublisherDeclareExchangeDelegates(t *testing.T) {
	base := &recordingAMQPClient{}
	pub := newTenantAwarePublisher(base, tenant1ID)

	require.NoError(t, pub.DeclareExchange(testExchange, exchangeTypeTopic, true, false, false, false))
	assert.Equal(t, []string{testExchange}, base.declareExchangeCalls)
}

func TestTenantAwarePublisherBindQueueDelegates(t *testing.T) {
	base := &recordingAMQPClient{}
	pub := newTenantAwarePublisher(base, tenant1ID)

	require.NoError(t, pub.BindQueue(testQueue, testExchange, testRoutingKey, false))
	require.Len(t, base.bindQueueCalls, 1)
	assert.Equal(t, [3]string{testQueue, testExchange, testRoutingKey}, base.bindQueueCalls[0])
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
