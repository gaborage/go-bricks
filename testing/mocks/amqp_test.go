package mocks

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/messaging"
)

const (
	captureExchange   = "orders.events"
	captureRoutingKey = "order.created"
)

func publishOptionsWithHeaders() messaging.PublishOptions {
	return messaging.PublishOptions{
		Exchange:   captureExchange,
		RoutingKey: captureRoutingKey,
		Headers:    map[string]any{"event_type": "OrderCreated"},
	}
}

func TestMockAMQPClientCapturesPublishedFrames(t *testing.T) {
	client := NewMockAMQPClient()
	client.ExpectPublishToExchangeAny(nil)

	require.NoError(t, client.PublishToExchange(t.Context(), publishOptionsWithHeaders(), []byte(`{"order_id":1}`)))
	require.NoError(t, client.PublishToExchange(t.Context(),
		messaging.PublishOptions{Exchange: captureExchange, RoutingKey: "order.shipped"}, []byte(`{"order_id":2}`)))

	frames := client.PublishedFrames()
	require.Len(t, frames, 2, "frames are kept oldest first")
	assert.Equal(t, captureRoutingKey, frames[0].Options.RoutingKey)
	assert.Equal(t, "OrderCreated", frames[0].Options.Headers["event_type"])
	assert.Equal(t, `{"order_id":1}`, string(frames[0].Data))
	assert.Equal(t, "order.shipped", frames[1].Options.RoutingKey)
	assert.Nil(t, frames[1].Options.Headers, "a nil headers map stays nil")

	last, ok := client.LastPublishedFrame()
	require.True(t, ok)
	assert.Equal(t, "order.shipped", last.Options.RoutingKey)
}

func TestMockAMQPClientCaptureCopiesCallerState(t *testing.T) {
	client := NewMockAMQPClient()
	client.ExpectPublishToExchangeAny(nil)

	options := publishOptionsWithHeaders()
	data := []byte(`{"order_id":1}`)
	require.NoError(t, client.PublishToExchange(t.Context(), options, data))

	// What a stamping layer downstream — or a caller reusing its buffer — does next.
	options.Headers["x-tenant-id"] = "acme"
	data[2] = 'X'

	frame := client.PublishedFrames()[0]
	assert.NotContains(t, frame.Options.Headers, "x-tenant-id", "the recorded headers are a copy")
	assert.Equal(t, `{"order_id":1}`, string(frame.Data), "the recorded payload is a copy")

	// And the snapshot the caller holds is its own: writing to it changes nothing.
	frame.Options.Headers["event_type"] = "mutated"
	frame.Data[2] = 'Z'
	stored := client.PublishedFrames()[0]
	assert.Equal(t, "OrderCreated", stored.Options.Headers["event_type"])
	assert.Equal(t, `{"order_id":1}`, string(stored.Data))

	// LastPublishedFrame hands out its own copy too.
	last, ok := client.LastPublishedFrame()
	require.True(t, ok)
	last.Options.Headers["event_type"] = "mutated"
	last.Data[2] = 'Z'
	assert.Equal(t, "OrderCreated", client.PublishedFrames()[0].Options.Headers["event_type"])
	assert.Equal(t, `{"order_id":1}`, string(client.PublishedFrames()[0].Data))
}

func TestMockAMQPClientCapturesFailedPublish(t *testing.T) {
	publishErr := errors.New("broker refused")
	client := NewMockAMQPClient()
	client.ExpectPublishToExchangeAny(publishErr)

	require.ErrorIs(t, client.PublishToExchange(t.Context(), publishOptionsWithHeaders(), []byte(`{}`)), publishErr)

	assert.Len(t, client.PublishedFrames(), 1, "an attempt the mock failed is still recorded")
}

func TestMockAMQPClientPublishCaptureStartsAndClearsEmpty(t *testing.T) {
	client := NewMockAMQPClient()

	assert.Empty(t, client.PublishedFrames())
	_, ok := client.LastPublishedFrame()
	assert.False(t, ok, "no frame before the first publish")

	client.On("PublishToExchange", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	require.NoError(t, client.PublishToExchange(t.Context(), publishOptionsWithHeaders(), []byte(`{}`)))
	require.Len(t, client.PublishedFrames(), 1)

	client.ClearPublishedFrames()
	assert.Empty(t, client.PublishedFrames())
	_, ok = client.LastPublishedFrame()
	assert.False(t, ok, "the capture is empty again after a clear")
}

// TestMockAMQPClientCaptureExercisedByTypedPublisher is the consumer-facing shape
// the capture exists for: a messaging.Publisher[T] handle publishing through the
// mock, asserted on the destination the DECLARATION chose.
func TestMockAMQPClientCaptureExercisedByTypedPublisher(t *testing.T) {
	type orderCreated struct {
		OrderID int64 `json:"order_id"`
	}

	client := NewMockAMQPClient()
	client.ExpectPublishToExchangeAny(nil)

	pub := messaging.DeclareTypedPublisher[orderCreated](messaging.NewDeclarations(), &messaging.PublisherOptions{
		Exchange:   captureExchange,
		RoutingKey: captureRoutingKey,
		EventType:  "OrderCreated",
		Headers:    map[string]any{"event_type": "OrderCreated"},
	})

	require.NoError(t, pub.Publish(t.Context(), client, orderCreated{OrderID: 42}))

	frame, ok := client.LastPublishedFrame()
	require.True(t, ok)
	assert.Equal(t, captureExchange, frame.Options.Exchange)
	assert.Equal(t, captureRoutingKey, frame.Options.RoutingKey)
	assert.Equal(t, "OrderCreated", frame.Options.Headers["event_type"])
	assert.JSONEq(t, `{"order_id":42}`, string(frame.Data))
}
