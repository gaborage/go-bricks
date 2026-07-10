// Package testing provides test utilities for the consumer-side inbox.
//
// MockInbox implements app.InboxProcessor for unit testing, letting application
// developers verify exactly-once handler logic without a real database.
//
// Usage:
//
//	mockInbox := inboxtest.NewMockInbox()
//	handler := NewHandler(mockInbox)
//	handler.Handle(ctx, delivery)
//	inboxtest.AssertProcessed(t, mockInbox, "evt-1")
package testing

import (
	"context"
	"sync"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// MockInbox implements app.InboxProcessor for unit testing.
// It records every ProcessOnce call, auto-deduplicates repeated event ids (the
// second call with the same id skips fn, like the real inbox), and supports a
// configurable error and pre-marked already-processed ids.
//
// Thread-safe: all operations use sync.Mutex for concurrent test scenarios.
type MockInbox struct {
	mu         sync.Mutex
	processed  []string        // every eventID passed to ProcessOnce
	ranFor     []string        // eventIDs whose fn was actually executed
	seen       map[string]bool // ids already processed (fn skipped on repeat)
	processErr error
}

// NewMockInbox creates a new MockInbox with no configured failures.
func NewMockInbox() *MockInbox {
	return &MockInbox{seen: map[string]bool{}}
}

// WithError configures the mock to return err from every ProcessOnce call
// (without running fn). Useful for testing handler error paths.
func (m *MockInbox) WithError(err error) *MockInbox {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.processErr = err
	return m
}

// MarkAlreadyProcessed pre-marks eventID as processed, so the next ProcessOnce
// for that id treats it as a duplicate and skips fn.
func (m *MockInbox) MarkAlreadyProcessed(eventID string) *MockInbox {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen[eventID] = true
	return m
}

// ProcessOnce implements app.InboxProcessor. It records the call and runs fn
// (with a nil tx) exactly once per eventID, unless an error is configured or the
// id was already processed.
func (m *MockInbox) ProcessOnce(ctx context.Context, eventID string, fn func(ctx context.Context, tx dbtypes.Tx) error) error {
	m.mu.Lock()
	if m.processErr != nil {
		err := m.processErr
		m.mu.Unlock()
		return err
	}
	m.processed = append(m.processed, eventID)
	dup := m.seen[eventID]
	m.seen[eventID] = true
	m.mu.Unlock()

	if dup {
		return nil
	}
	if fn == nil {
		return nil
	}
	if err := fn(ctx, nil); err != nil {
		return err
	}
	m.mu.Lock()
	m.ranFor = append(m.ranFor, eventID)
	m.mu.Unlock()
	return nil
}

// ProcessedIDs returns a copy of every eventID passed to ProcessOnce, except
// calls short-circuited by a configured WithError error (those are not
// recorded).
func (m *MockInbox) ProcessedIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.processed))
	copy(out, m.processed)
	return out
}

// RanIDs returns a copy of the eventIDs whose fn was actually executed
// (excludes duplicates and error short-circuits).
func (m *MockInbox) RanIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.ranFor))
	copy(out, m.ranFor)
	return out
}

// Reset clears all recorded state.
func (m *MockInbox) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.processed = nil
	m.ranFor = nil
	m.seen = map[string]bool{}
	m.processErr = nil
}
