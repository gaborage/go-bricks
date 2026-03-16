package testing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// AssertEventPublished asserts that at least one event with the given type was published.
func AssertEventPublished(t *testing.T, mock *MockOutbox, eventType string) {
	t.Helper()
	events := mock.EventsByType(eventType)
	assert.NotEmpty(t, events, "expected event type %q to be published, but no events found", eventType)
}

// AssertEventNotPublished asserts that no events with the given type were published.
func AssertEventNotPublished(t *testing.T, mock *MockOutbox, eventType string) {
	t.Helper()
	events := mock.EventsByType(eventType)
	assert.Empty(t, events, "expected event type %q to NOT be published, but found %d events", eventType, len(events))
}

// AssertEventCount asserts that exactly the expected number of events were published (all types).
func AssertEventCount(t *testing.T, mock *MockOutbox, expected int) {
	t.Helper()
	events := mock.Events()
	assert.Len(t, events, expected, "expected %d total events, got %d", expected, len(events))
}

// AssertEventTypeCount asserts that exactly the expected number of events with the given type were published.
func AssertEventTypeCount(t *testing.T, mock *MockOutbox, eventType string, expected int) {
	t.Helper()
	events := mock.EventsByType(eventType)
	assert.Len(t, events, expected, "expected %d events of type %q, got %d", expected, eventType, len(events))
}

// AssertEventWithAggregate asserts that an event with the given type and aggregate ID was published.
func AssertEventWithAggregate(t *testing.T, mock *MockOutbox, eventType, aggregateID string) {
	t.Helper()
	events := mock.EventsByType(eventType)
	for _, e := range events {
		if e.Event.AggregateID == aggregateID {
			return
		}
	}
	assert.Failf(t, "event not found",
		"expected event type %q with aggregate ID %q, but none found", eventType, aggregateID)
}

// AssertEventWithExchange asserts that an event with the given type and exchange was published.
func AssertEventWithExchange(t *testing.T, mock *MockOutbox, eventType, exchange string) {
	t.Helper()
	events := mock.EventsByType(eventType)
	for _, e := range events {
		if e.Event.Exchange == exchange {
			return
		}
	}
	assert.Failf(t, "event not found",
		"expected event type %q with exchange %q, but none found", eventType, exchange)
}

// AssertNoEvents asserts that no events were published.
func AssertNoEvents(t *testing.T, mock *MockOutbox) {
	t.Helper()
	AssertEventCount(t, mock, 0)
}
