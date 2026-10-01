package messaging

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManagerCloseJoinsConsumersBeforeClosingClients pins the manager side of the join:
// Close closes a consumer client only once its handlers have finished, and it joins
// outside consMu, so a handler reading ConsumerStates while it winds down does not hold
// Close for the whole stop budget.
func TestManagerCloseJoinsConsumersBeforeClosingClients(t *testing.T) {
	client := &resubscribingMockClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		results:              []consumeResult{{ch: deliveriesFor(1)}},
	}
	log := newRecordingLogger()
	handler := newGatedHandler()
	registry := startStopRegistry(t, client, log, handler)

	entryClient := &stubAMQPClient{}
	m := &Manager{
		logger:    log,
		consumers: map[string]*consumerEntry{"": {client: entryClient, registry: registry}},
	}
	handler.after = func() { m.ConsumerStates() }
	var finishedAtClientClose atomic.Int64
	finishedAtClientClose.Store(-1)
	entryClient.closeHook = func() { finishedAtClientClose.Store(handler.finished.Load()) }

	select {
	case <-handler.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never entered")
	}

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		assert.NoError(t, m.Close())
	}()
	awaitStopBegun(t, log, closed)
	released := time.Now()
	close(handler.release)
	<-closed

	assert.Less(t, time.Since(released), stopReturnsPromptlyLimit, "Close waited out the stop budget")
	assert.Equal(t, int64(1), finishedAtClientClose.Load(), "the consumer client closed under a running handler")
	assert.Zero(t, registry.runningSupervisors.Load())
}

// TestManagerCloseDoesNotWaitAgainForAnAbandonedStop pins the second join: once the
// shutdown stop has given up on a stuck handler, Close must not spend another budget on
// the same supervisors — it warns with the count and closes the clients.
func TestManagerCloseDoesNotWaitAgainForAnAbandonedStop(t *testing.T) {
	client := &resubscribingMockClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		results:              []consumeResult{{ch: deliveriesFor(1)}},
	}
	log := newRecordingLogger()
	handler := newGatedHandler()
	registry := startStopRegistry(t, client, log, handler)
	registry.stopBudget = shortStopBudgetForTests
	t.Cleanup(func() {
		close(handler.release)
		require.Eventually(t, func() bool { return registry.runningSupervisors.Load() == 0 },
			5*time.Second, time.Millisecond, "supervisor did not exit once released")
	})

	entryClient := &stubAMQPClient{}
	m := &Manager{
		logger:    log,
		consumers: map[string]*consumerEntry{"": {client: entryClient, registry: registry}},
	}

	select {
	case <-handler.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never entered")
	}

	registry.StopConsumers(context.Background()) // the manager's own stop would spend the full 5s budget
	log.Line(t, msgSupervisorsAbandoned)

	start := time.Now()
	require.NoError(t, m.Close())
	assert.Less(t, time.Since(start), stopReturnsPromptlyLimit, "Close waited a second budget on an abandoned stop")
	assert.False(t, entryClient.IsReady(), "Close did not close the consumer client")
	line := log.Line(t, msgSupervisorsStillAbandoned)
	assert.Equal(t, []string{"1"}, line.Values("running_supervisors"))
}
