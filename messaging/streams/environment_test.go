package streams

import (
	"testing"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/ha"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReconnectBackOffPinsTheReattachTiming pins the literal values rather than
// comparing against ha.NewBackOffConfiguration(): matching the client's default
// would re-couple the timing ADR-123 documents to whatever the next client
// release ships.
func TestReconnectBackOffPinsTheReattachTiming(t *testing.T) {
	got := reconnectBackOff()

	assert.Equal(t, &ha.BackOffConfiguration{MinInterval: 3, MaxInterval: 8, EnableRandom: true}, got)
	require.NoError(t, got.Validate(), "every reliable constructor refuses an invalid backoff")
	assert.NotSame(t, got, reconnectBackOff(), "the client keeps the pointer, so each handle needs its own")
}
