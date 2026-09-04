package testing

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/messaging"
)

type orderCreated struct {
	OrderID int64 `json:"order_id"`
}

// publishTwice is what a module does with the seam: it holds an EventPublisher
// and never sees which implementation is behind it.
func publishTwice(ctx context.Context, pub messaging.EventPublisher[orderCreated]) error {
	if err := pub.Publish(ctx, nil, orderCreated{OrderID: 1}); err != nil {
		return err
	}
	return pub.Publish(ctx, nil, orderCreated{OrderID: 2})
}

func TestCapturePublisherRecordsEventsInOrder(t *testing.T) {
	capture := NewCapturePublisher[orderCreated]()

	require.NoError(t, publishTwice(t.Context(), capture))

	assert.Equal(t, []orderCreated{{OrderID: 1}, {OrderID: 2}}, capture.Events())
	last, ok := capture.Last()
	require.True(t, ok)
	assert.Equal(t, int64(2), last.OrderID)
}

func TestCapturePublisherStartsEmpty(t *testing.T) {
	capture := NewCapturePublisher[orderCreated]()
	assert.Empty(t, capture.Events())
	_, ok := capture.Last()
	assert.False(t, ok)
}

// TestCapturePublisherFailStillRecordsTheAttempt pins that a failing publish is
// observable: the module's attempt is what a test asserts, not only its success.
func TestCapturePublisherFailStillRecordsTheAttempt(t *testing.T) {
	capture := NewCapturePublisher[orderCreated]()
	boom := errors.New("broker down")
	capture.Fail(boom)

	err := publishTwice(t.Context(), capture)

	require.ErrorIs(t, err, boom)
	assert.Len(t, capture.Events(), 1, "the second publish never ran because the first failed")

	capture.Fail(nil)
	require.NoError(t, capture.Publish(t.Context(), nil, orderCreated{OrderID: 3}))
	assert.Len(t, capture.Events(), 2)
}

func TestCapturePublisherResetKeepsTheConfiguredError(t *testing.T) {
	capture := NewCapturePublisher[orderCreated]()
	boom := errors.New("still failing")
	capture.Fail(boom)
	require.ErrorIs(t, capture.Publish(t.Context(), nil, orderCreated{}), boom)

	capture.Reset()

	assert.Empty(t, capture.Events())
	assert.ErrorIs(t, capture.Publish(t.Context(), nil, orderCreated{}), boom)
}

// TestCapturePublisherEventsIsACopy pins that a caller writing into the returned
// slice cannot reach the recording.
func TestCapturePublisherEventsIsACopy(t *testing.T) {
	capture := NewCapturePublisher[orderCreated]()
	require.NoError(t, capture.Publish(t.Context(), nil, orderCreated{OrderID: 7}))

	events := capture.Events()
	events[0].OrderID = 99

	assert.Equal(t, int64(7), capture.Events()[0].OrderID)
}

func TestCapturePublisherIsSafeForConcurrentPublishes(t *testing.T) {
	capture := NewCapturePublisher[orderCreated]()
	const n = 64
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = capture.Publish(context.Background(), nil, orderCreated{OrderID: int64(i)})
		}()
	}
	wg.Wait()

	assert.Len(t, capture.Events(), n)
}
