package testing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/migration/provisioning"
)

// AssertJobReachedState fails the test if the job named jobID is not at
// the expected state. Helpful for the typical assertion in consumer tests
// where the executor's terminal-state check is the contract under test.
func AssertJobReachedState(t *testing.T, store provisioning.StateStore, jobID string, want provisioning.State) {
	t.Helper()
	job, err := store.Get(context.Background(), jobID)
	require.NoError(t, err, "Get job %q", jobID)
	assert.Equal(t, want, job.State, "job %q state", jobID)
}

// AssertJobLastError fails the test if the job's LastError does not equal
// want. Useful when verifying that a failed transition recorded the
// expected diagnostic message.
func AssertJobLastError(t *testing.T, store provisioning.StateStore, jobID, want string) {
	t.Helper()
	job, err := store.Get(context.Background(), jobID)
	require.NoError(t, err, "Get job %q", jobID)
	assert.Equal(t, want, job.LastError, "job %q LastError", jobID)
}

// AssertTransitionPath verifies the MockStateStore observed the expected
// sequence of transitions for jobID, in order. Other jobs' transitions
// are ignored — pass jobID="" to assert across all jobs.
func AssertTransitionPath(t *testing.T, m *MockStateStore, jobID string, want []provisioning.State) {
	t.Helper()
	got := make([]provisioning.State, 0, len(want))
	for _, ev := range m.TransitionLog() {
		if jobID != "" && ev.JobID != jobID {
			continue
		}
		got = append(got, ev.To)
	}
	assert.Equal(t, want, got, "transition path for job %q", jobID)
}
