package messaging

import (
	"testing"

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
