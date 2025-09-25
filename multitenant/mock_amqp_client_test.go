package multitenant

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/messaging"
)

const (
	testExchange   = "test.exchange"
	testQueue      = "test.queue"
	testRoutingKey = "test.routing.key"
)

func TestNewRecordingAMQPClient(t *testing.T) {
	client := NewRecordingAMQPClient()

	assert.NotNil(t, client)
	assert.True(t, client.IsReady())
	assert.False(t, client.closed)

	// Verify it implements the interfaces
	var _ messaging.AMQPClient = client
	var _ messaging.Client = client
}

func TestRecordingAMQPClientIsReady(t *testing.T) {
	client := NewRecordingAMQPClient()

	// Should be ready initially
	assert.True(t, client.IsReady())

	// Should not be ready after close
	err := client.Close()
	assert.NoError(t, err)
	assert.False(t, client.IsReady())
}

func TestRecordingAMQPClientDeclareExchange(t *testing.T) {
	client := NewRecordingAMQPClient()

	// Should succeed and record the call
	err := client.DeclareExchange(testExchange, "topic", true, false, false, false)
	assert.NoError(t, err)

	// Verify the call was recorded
	calls := client.GetExchangeCalls()
	require.Len(t, calls, 1)

	call := calls[0]
	assert.Equal(t, testExchange, call.Name)
	assert.Equal(t, "topic", call.Kind)
	assert.True(t, call.Durable)
	assert.False(t, call.AutoDelete)
	assert.False(t, call.Internal)
	assert.False(t, call.NoWait)

	// Verify call counts
	counts := client.GetCallCounts()
	assert.Equal(t, 1, counts.DeclareExchange)
	assert.Equal(t, 0, counts.DeclareQueue)
	assert.Equal(t, 0, counts.BindQueue)
}

func TestRecordingAMQPClientDeclareQueue(t *testing.T) {
	client := NewRecordingAMQPClient()

	// Should succeed and record the call
	err := client.DeclareQueue(testQueue, true, false, false, false)
	assert.NoError(t, err)

	// Verify the call was recorded
	calls := client.GetQueueCalls()
	require.Len(t, calls, 1)

	call := calls[0]
	assert.Equal(t, testQueue, call.Name)
	assert.True(t, call.Durable)
	assert.False(t, call.AutoDelete)
	assert.False(t, call.Exclusive)
	assert.False(t, call.NoWait)

	// Verify call counts
	counts := client.GetCallCounts()
	assert.Equal(t, 0, counts.DeclareExchange)
	assert.Equal(t, 1, counts.DeclareQueue)
	assert.Equal(t, 0, counts.BindQueue)
}

func TestRecordingAMQPClientBindQueue(t *testing.T) {
	client := NewRecordingAMQPClient()

	// Should succeed and record the call
	err := client.BindQueue(testQueue, testExchange, testRoutingKey, false)
	assert.NoError(t, err)

	// Verify the call was recorded
	calls := client.GetBindingCalls()
	require.Len(t, calls, 1)

	call := calls[0]
	assert.Equal(t, testQueue, call.Queue)
	assert.Equal(t, testExchange, call.Exchange)
	assert.Equal(t, testRoutingKey, call.RoutingKey)
	assert.False(t, call.NoWait)

	// Verify call counts
	counts := client.GetCallCounts()
	assert.Equal(t, 0, counts.DeclareExchange)
	assert.Equal(t, 0, counts.DeclareQueue)
	assert.Equal(t, 1, counts.BindQueue)
}

func TestRecordingAMQPClientPublishToExchange(t *testing.T) {
	client := NewRecordingAMQPClient()
	ctx := context.Background()

	options := messaging.PublishOptions{
		Exchange:   testExchange,
		RoutingKey: testRoutingKey,
		Mandatory:  true,
	}
	data := []byte("test message")

	// Should succeed and record the call
	err := client.PublishToExchange(ctx, options, data)
	assert.NoError(t, err)

	// Verify call counts
	counts := client.GetCallCounts()
	assert.Equal(t, 1, counts.Publish)
}

func TestRecordingAMQPClientConsumeFromQueue(t *testing.T) {
	client := NewRecordingAMQPClient()
	ctx := context.Background()

	options := messaging.ConsumeOptions{
		Queue:    testQueue,
		Consumer: "test-consumer",
		AutoAck:  true,
	}

	// Should succeed and return closed channel
	ch, err := client.ConsumeFromQueue(ctx, options)
	assert.NoError(t, err)
	assert.NotNil(t, ch)

	// Channel should be closed (no messages during recording)
	_, ok := <-ch
	assert.False(t, ok, "Channel should be closed")

	// Verify call counts
	counts := client.GetCallCounts()
	assert.Equal(t, 1, counts.Consume)
}

func TestRecordingAMQPClientBasicPublish(t *testing.T) {
	client := NewRecordingAMQPClient()
	ctx := context.Background()

	data := []byte("test message")

	// Should succeed and record the call
	err := client.Publish(ctx, "test.destination", data)
	assert.NoError(t, err)

	// Verify call counts
	counts := client.GetCallCounts()
	assert.Equal(t, 1, counts.Publish)
}

func TestRecordingAMQPClientBasicConsume(t *testing.T) {
	client := NewRecordingAMQPClient()
	ctx := context.Background()

	// Should succeed and return closed channel
	ch, err := client.Consume(ctx, "test.destination")
	assert.NoError(t, err)
	assert.NotNil(t, ch)

	// Channel should be closed (no messages during recording)
	_, ok := <-ch
	assert.False(t, ok, "Channel should be closed")

	// Verify call counts
	counts := client.GetCallCounts()
	assert.Equal(t, 1, counts.Consume)
}

func TestRecordingAMQPClientMultipleOperations(t *testing.T) {
	client := NewRecordingAMQPClient()
	ctx := context.Background()

	// Perform multiple operations
	assert.NoError(t, client.DeclareExchange("ex1", "direct", true, false, false, false))
	assert.NoError(t, client.DeclareExchange("ex2", "topic", false, true, false, false))
	assert.NoError(t, client.DeclareQueue("q1", true, false, false, false))
	assert.NoError(t, client.BindQueue("q1", "ex1", "key1", false))
	assert.NoError(t, client.BindQueue("q1", "ex2", "key2", false))

	options := messaging.PublishOptions{Exchange: "ex1", RoutingKey: "key1"}
	assert.NoError(t, client.PublishToExchange(ctx, options, []byte("msg1")))
	assert.NoError(t, client.PublishToExchange(ctx, options, []byte("msg2")))

	// Verify all calls were recorded
	counts := client.GetCallCounts()
	assert.Equal(t, 2, counts.DeclareExchange)
	assert.Equal(t, 1, counts.DeclareQueue)
	assert.Equal(t, 2, counts.BindQueue)
	assert.Equal(t, 2, counts.Publish)
	assert.Equal(t, 0, counts.Consume)

	// Verify specific calls
	exchangeCalls := client.GetExchangeCalls()
	assert.Len(t, exchangeCalls, 2)
	assert.Equal(t, "ex1", exchangeCalls[0].Name)
	assert.Equal(t, "ex2", exchangeCalls[1].Name)

	queueCalls := client.GetQueueCalls()
	assert.Len(t, queueCalls, 1)
	assert.Equal(t, "q1", queueCalls[0].Name)

	bindingCalls := client.GetBindingCalls()
	assert.Len(t, bindingCalls, 2)
	assert.Equal(t, "key1", bindingCalls[0].RoutingKey)
	assert.Equal(t, "key2", bindingCalls[1].RoutingKey)
}

func TestRecordingAMQPClientReset(t *testing.T) {
	client := NewRecordingAMQPClient()

	// Perform some operations
	assert.NoError(t, client.DeclareExchange("test", "direct", true, false, false, false))
	assert.NoError(t, client.DeclareQueue("test", true, false, false, false))

	// Verify operations were recorded
	counts := client.GetCallCounts()
	assert.Equal(t, 1, counts.DeclareExchange)
	assert.Equal(t, 1, counts.DeclareQueue)

	// Reset and verify clean slate
	client.Reset()

	counts = client.GetCallCounts()
	assert.Equal(t, 0, counts.DeclareExchange)
	assert.Equal(t, 0, counts.DeclareQueue)
	assert.Equal(t, 0, counts.BindQueue)
	assert.Equal(t, 0, counts.Publish)
	assert.Equal(t, 0, counts.Consume)

	// Verify call slices are empty
	assert.Empty(t, client.GetExchangeCalls())
	assert.Empty(t, client.GetQueueCalls())
	assert.Empty(t, client.GetBindingCalls())
}

func TestRecordingAMQPClientConcurrentAccess(t *testing.T) {
	client := NewRecordingAMQPClient()
	ctx := context.Background()

	// Test concurrent access to ensure thread safety
	const numGoroutines = 10
	const numOperations = 100

	done := make(chan bool, numGoroutines)

	// Start multiple goroutines performing operations
	for i := range numGoroutines {
		go func(_ int) {
			for j := range numOperations {
				switch j % 4 {
				case 0:
					client.DeclareExchange("ex", "direct", true, false, false, false)
				case 1:
					client.DeclareQueue("q", true, false, false, false)
				case 2:
					client.BindQueue("q", "ex", "key", false)
				case 3:
					client.PublishToExchange(ctx, messaging.PublishOptions{}, []byte("msg"))
				}
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify no race conditions occurred and operations were recorded
	counts := client.GetCallCounts()
	expectedCount := numGoroutines * numOperations / 4 // Each operation type is 1/4 of total
	assert.Equal(t, expectedCount, counts.DeclareExchange)
	assert.Equal(t, expectedCount, counts.DeclareQueue)
	assert.Equal(t, expectedCount, counts.BindQueue)
	assert.Equal(t, expectedCount, counts.Publish)
}
