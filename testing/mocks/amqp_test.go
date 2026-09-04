package mocks

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

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
