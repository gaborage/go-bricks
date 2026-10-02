package mocks

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/messaging"
)

// TestMockAMQPClientCarriesNoBytePublishDoor pins ADR-096 on the consumer-visible
// double: the mock satisfies messaging.AMQPClient and, like it, exposes no byte
// publish method a test could route a plaintext frame through.
func TestMockAMQPClientCarriesNoBytePublishDoor(t *testing.T) {
	var client messaging.AMQPClient = NewMockAMQPClient()

	typ := reflect.TypeOf(client)
	for _, name := range []string{"Publish", "PublishToExchange", "PublishBytes"} {
		_, found := typ.MethodByName(name)
		assert.Falsef(t, found, "MockAMQPClient must not expose %s", name)
	}
}

// TestMockAMQPClientIsRefusedByTheTypedPublisher pins what a module's test sees
// when it hands the mock to a real messaging.Publisher[T]: the typed error, not a
// captured frame — the capture lives in messaging/testing.
func TestMockAMQPClientIsRefusedByTheTypedPublisher(t *testing.T) {
	decls := messaging.NewDeclarations()
	pub := messaging.DeclareTypedPublisher[struct{ ID int }](decls, &messaging.PublisherOptions{
		Exchange: "orders.events", RoutingKey: "order.created", EventType: "OrderCreated",
	})

	err := pub.Publish(t.Context(), NewMockAMQPClient(), struct{ ID int }{ID: 1})

	assert.ErrorIs(t, err, messaging.ErrPublishDoorUnavailable)
}

// TestMockAMQPClientExpectConsumeFromQueueAny pins both returns of the any-options
// expectation: a nil error hands back the queue's channel, which SimulateMessage
// feeds; an error hands back that error and no channel.
func TestMockAMQPClientExpectConsumeFromQueueAny(t *testing.T) {
	t.Run("nil_error_returns_the_queue_channel", func(t *testing.T) {
		client := NewMockAMQPClient()
		client.ExpectConsumeFromQueueAny(nil)

		deliveries, err := client.ConsumeFromQueue(t.Context(), messaging.ConsumeOptions{Queue: "q"})
		require.NoError(t, err)
		require.NotNil(t, deliveries)

		client.SimulateMessage("q", []byte(`{"event":"test"}`))
		require.Len(t, deliveries, 1)
		assert.JSONEq(t, `{"event":"test"}`, string((<-deliveries).Body))
	})

	t.Run("error_returns_no_channel", func(t *testing.T) {
		consumeErr := errors.New("consume failed")
		client := NewMockAMQPClient()
		client.ExpectConsumeFromQueueAny(consumeErr)

		deliveries, err := client.ConsumeFromQueue(t.Context(), messaging.ConsumeOptions{Queue: "q"})
		require.ErrorIs(t, err, consumeErr)
		assert.Nil(t, deliveries)
	})
}
