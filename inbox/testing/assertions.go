package testing

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

// AssertProcessed asserts that ProcessOnce was called for the given event id.
func AssertProcessed(t *testing.T, mock *MockInbox, eventID string) {
	t.Helper()
	assert.Contains(t, mock.ProcessedIDs(), eventID,
		"expected event id %q to be processed, but it was not", eventID)
}

// AssertNotProcessed asserts that ProcessOnce was never called for the given event id.
func AssertNotProcessed(t *testing.T, mock *MockInbox, eventID string) {
	t.Helper()
	assert.NotContains(t, mock.ProcessedIDs(), eventID,
		"expected event id %q to NOT be processed, but it was", eventID)
}

// AssertProcessCount asserts the total number of ProcessOnce calls.
func AssertProcessCount(t *testing.T, mock *MockInbox, expected int) {
	t.Helper()
	assert.Len(t, mock.ProcessedIDs(), expected,
		"expected %d ProcessOnce calls", expected)
}

// AssertHandlerRan asserts that the handler (fn) actually executed for the given
// event id (i.e. it was a first-time, non-error processing).
func AssertHandlerRan(t *testing.T, mock *MockInbox, eventID string) {
	t.Helper()
	assert.True(t, slices.Contains(mock.RanIDs(), eventID),
		"expected handler to run for event id %q, but it did not", eventID)
}
