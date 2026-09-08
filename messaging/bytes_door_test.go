package messaging

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFrameworkClientsImplementTheByteDoor pins the other half: the framework's
// own client and the stamping wrapper DO satisfy the unexported door the typed
// handle and the relay publish through.
func TestFrameworkClientsImplementTheByteDoor(t *testing.T) {
	var _ bytePublisher = (*AMQPClientImpl)(nil)
	var _ bytePublisher = (*stampingPublisher)(nil)

	_, ok := any(&stampingPublisher{}).(bytePublisher)
	assert.True(t, ok)
}

// TestPublishThroughDoorRefusesAClientWithoutOne pins the typed error a
// consumer-built or mocked client produces.
func TestPublishThroughDoorRefusesAClientWithoutOne(t *testing.T) {
	err := publishThroughDoor(t.Context(), struct{ AMQPClient }{}, publishOptions{RoutingKey: "q"}, []byte("x"))
	require.ErrorIs(t, err, ErrPublishDoorUnavailable)
	assert.NotContains(t, err.Error(), "q", "the error names the client type, never the destination")
}

// TestPublishThroughDoorCarriesNoPropsWhenTheCallerKnowsNothing pins the
// carrier's nil arm end to end: the door forwards a zero-value option struct
// untouched, and a nil Props publishes rather than panicking (ADR-105 — the
// properties travel as one pointer, so every read of it is nil-guarded).
func TestPublishThroughDoorCarriesNoPropsWhenTheCallerKnowsNothing(t *testing.T) {
	client := &capturingPublishClient{}

	require.NoError(t, publishThroughDoor(t.Context(), client, publishOptions{RoutingKey: "q"}, []byte("x")))

	frames := client.captured()
	require.Len(t, frames, 1)
	assert.Nil(t, frames[0].options.props, "no door claimed anything about the message")

	pub := (&AMQPClientImpl{}).preparePublishing(t.Context(), frames[0].options, []byte("x"))
	assert.Equal(t, contentTypeOctetStream, pub.ContentType)
	assert.Empty(t, pub.Type)
	assert.NotEmpty(t, pub.MessageId)
	assert.Equal(t, amqp.Persistent, pub.DeliveryMode)
}
