package testing

import (
	"context"
	"fmt"
	"testing"

	"github.com/gaborage/go-bricks/app"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockOutboxPublishRecordsEvent(t *testing.T) {
	mock := NewMockOutbox()

	event := &app.OutboxEvent{
		EventType:   "order.created",
		AggregateID: "order-123",
		Exchange:    "order.events",
	}

	eventID, err := mock.Publish(context.Background(), nil, event)

	require.NoError(t, err)
	assert.Equal(t, "mock-event-1", eventID)
	assert.Len(t, mock.Events(), 1)
	assert.Equal(t, "order.created", mock.Events()[0].Event.EventType)
}

func TestMockOutboxPublishMultipleEvents(t *testing.T) {
	mock := NewMockOutbox()

	for i := range 3 {
		event := &app.OutboxEvent{
			EventType:   fmt.Sprintf("event.%d", i),
			AggregateID: fmt.Sprintf("agg-%d", i),
		}
		_, err := mock.Publish(context.Background(), nil, event)
		require.NoError(t, err)
	}

	assert.Len(t, mock.Events(), 3)
}

func TestMockOutboxPublishWithError(t *testing.T) {
	mock := NewMockOutbox().WithError(fmt.Errorf("outbox unavailable"))

	event := &app.OutboxEvent{
		EventType:   "order.created",
		AggregateID: "order-123",
	}

	_, err := mock.Publish(context.Background(), nil, event)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "outbox unavailable")
	assert.Empty(t, mock.Events())
}

func TestMockOutboxEventsByType(t *testing.T) {
	mock := NewMockOutbox()

	events := []app.OutboxEvent{
		{EventType: "order.created", AggregateID: "1"},
		{EventType: "order.cancelled", AggregateID: "2"},
		{EventType: "order.created", AggregateID: "3"},
	}

	for i := range events {
		_, err := mock.Publish(context.Background(), nil, &events[i])
		require.NoError(t, err)
	}

	created := mock.EventsByType("order.created")
	assert.Len(t, created, 2)

	cancelled := mock.EventsByType("order.cancelled")
	assert.Len(t, cancelled, 1)

	unknown := mock.EventsByType("unknown")
	assert.Empty(t, unknown)
}

func TestMockOutboxReset(t *testing.T) {
	mock := NewMockOutbox()

	event := &app.OutboxEvent{EventType: "test", AggregateID: "1"}
	_, err := mock.Publish(context.Background(), nil, event)
	require.NoError(t, err)

	mock.Reset()

	assert.Empty(t, mock.Events())

	// ID counter should also reset
	eventID, err := mock.Publish(context.Background(), nil, event)
	require.NoError(t, err)
	assert.Equal(t, "mock-event-1", eventID)
}

func TestAssertEventPublishedSuccess(t *testing.T) {
	mock := NewMockOutbox()
	event := &app.OutboxEvent{EventType: "order.created", AggregateID: "1"}
	_, _ = mock.Publish(context.Background(), nil, event)

	// Should not fail
	AssertEventPublished(t, mock, "order.created")
}

func TestAssertEventNotPublishedSuccess(t *testing.T) {
	mock := NewMockOutbox()
	event := &app.OutboxEvent{EventType: "order.created", AggregateID: "1"}
	_, _ = mock.Publish(context.Background(), nil, event)

	// Should not fail — "order.cancelled" was never published
	AssertEventNotPublished(t, mock, "order.cancelled")
}

func TestAssertEventCountSuccess(t *testing.T) {
	mock := NewMockOutbox()
	for i := range 3 {
		e := &app.OutboxEvent{EventType: "test", AggregateID: fmt.Sprintf("%d", i)}
		_, _ = mock.Publish(context.Background(), nil, e)
	}

	AssertEventCount(t, mock, 3)
}

func TestAssertEventWithAggregateSuccess(t *testing.T) {
	mock := NewMockOutbox()
	event := &app.OutboxEvent{EventType: "order.created", AggregateID: "order-42"}
	_, _ = mock.Publish(context.Background(), nil, event)

	AssertEventWithAggregate(t, mock, "order.created", "order-42")
}

func TestAssertNoEventsSuccess(t *testing.T) {
	mock := NewMockOutbox()
	AssertNoEvents(t, mock)
}

func TestAssertEventWithExchangeSuccess(t *testing.T) {
	mock := NewMockOutbox()
	event := &app.OutboxEvent{
		EventType:   "order.created",
		AggregateID: "1",
		Exchange:    "order.events",
	}
	_, _ = mock.Publish(context.Background(), nil, event)

	AssertEventWithExchange(t, mock, "order.created", "order.events")
}
