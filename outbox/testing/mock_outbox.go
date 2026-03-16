// Package testing provides test utilities for the transactional outbox pattern.
//
// MockOutbox implements app.OutboxPublisher for unit testing, allowing
// application developers to verify event publishing without a real database.
//
// Usage:
//
//	mockOutbox := outboxtest.NewMockOutbox()
//	svc := NewOrderService(db, mockOutbox)
//	svc.CreateOrder(ctx, order)
//	outboxtest.AssertEventPublished(t, mockOutbox, "order.created")
package testing

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gaborage/go-bricks/app"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// PublishedEvent records a single Publish() call for later assertion.
type PublishedEvent struct {
	EventID string
	Event   *app.OutboxEvent
}

// MockOutbox implements app.OutboxPublisher for unit testing.
// It records all published events and supports configurable failures.
//
// Thread-safe: all operations use sync.Mutex for concurrent test scenarios.
type MockOutbox struct {
	mu     sync.Mutex
	events []PublishedEvent

	// Configurable behavior
	publishErr error
	idCounter  atomic.Int64
}

// NewMockOutbox creates a new MockOutbox instance with no configured failures.
func NewMockOutbox() *MockOutbox {
	return &MockOutbox{}
}

// WithError configures the mock to return an error on every Publish() call.
// This is useful for testing error handling when the outbox is unavailable.
func (m *MockOutbox) WithError(err error) *MockOutbox {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publishErr = err
	return m
}

// Publish implements app.OutboxPublisher.
// Records the event and returns a deterministic event ID ("mock-event-1", "mock-event-2", etc.).
func (m *MockOutbox) Publish(_ context.Context, _ dbtypes.Tx, event *app.OutboxEvent) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.publishErr != nil {
		return "", m.publishErr
	}

	id := fmt.Sprintf("mock-event-%d", m.idCounter.Add(1))
	m.events = append(m.events, PublishedEvent{
		EventID: id,
		Event:   event,
	})

	return id, nil
}

// Events returns a copy of all published events.
func (m *MockOutbox) Events() []PublishedEvent {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]PublishedEvent, len(m.events))
	copy(result, m.events)
	return result
}

// EventsByType returns all published events matching the given event type.
func (m *MockOutbox) EventsByType(eventType string) []PublishedEvent {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []PublishedEvent
	for _, e := range m.events {
		if e.Event != nil && e.Event.EventType == eventType {
			result = append(result, e)
		}
	}
	return result
}

// Reset clears all recorded events and resets the ID counter.
func (m *MockOutbox) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = nil
	m.idCounter.Store(0)
}
