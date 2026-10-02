package fixtures

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/messaging"
)

func TestNewMessageSimulatorDeliversThroughConsumeFromQueue(t *testing.T) {
	msg := []byte(`{"event":"user.created"}`)
	client := NewMessageSimulator(msg)

	deliveries, err := client.ConsumeFromQueue(t.Context(), messaging.ConsumeOptions{Queue: TestQueueName})
	require.NoError(t, err)

	select {
	case d := <-deliveries:
		require.Equal(t, msg, d.Body)
	case <-time.After(time.Second):
		t.Fatal("simulated message was not delivered")
	}
}
