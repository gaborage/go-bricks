package messaging

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	msgStoppingAllConsumers      = "Stopping all consumers"
	msgAllConsumersStopped       = "All consumers stopped"
	msgSupervisorsAbandoned      = "Consumer supervisors still running after the stop budget; abandoning them"
	msgSupervisorsStillAbandoned = "Consumer supervisors still running from an abandoned stop; not waiting again"
	stopReleaseGrace             = 50 * time.Millisecond
	shortStopBudgetForTests      = 20 * time.Millisecond
	stopReturnsPromptlyLimit     = time.Second
)

// gatedHandler blocks every Handle until release is closed, counting the calls that
// entered and the calls that finished.
type gatedHandler struct {
	entered  chan struct{}
	release  chan struct{}
	calls    atomic.Int64
	finished atomic.Int64
}

func newGatedHandler() *gatedHandler {
	return &gatedHandler{entered: make(chan struct{}, 1), release: make(chan struct{})}
}

func (h *gatedHandler) Handle(_ context.Context, _ *amqp.Delivery) error {
	h.calls.Add(1)
	select {
	case h.entered <- struct{}{}:
	default:
	}
	<-h.release
	h.finished.Add(1)
	return nil
}

func (h *gatedHandler) EventType() string { return testEventType }

// gatedResubscribeClient serves the initial subscription, then blocks the first
// re-subscribe until release is closed and ignores the context while it does, as
// AMQPClientImpl.ConsumeFromQueue does.
type gatedResubscribeClient struct {
	*simpleMockAMQPClient
	first   chan amqp.Delivery
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

var _ AMQPClient = (*gatedResubscribeClient)(nil)

func newGatedResubscribeClient() *gatedResubscribeClient {
	return &gatedResubscribeClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		first:                make(chan amqp.Delivery),
		entered:              make(chan struct{}),
		release:              make(chan struct{}),
	}
}

func (c *gatedResubscribeClient) ConsumeFromQueue(_ context.Context, _ ConsumeOptions) (<-chan amqp.Delivery, error) {
	if c.calls.Add(1) == 1 {
		return c.first, nil
	}
	close(c.entered)
	<-c.release
	return make(chan amqp.Delivery), nil
}

func startStopRegistry(t *testing.T, client AMQPClient, log *recordingLogger, handler MessageHandler) *Registry {
	t.Helper()
	registry := NewRegistry(client, log)
	registry.resubscribeDelay = time.Millisecond
	registry.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueueName,
		EventType: testEventType,
		Workers:   1,
		Handler:   handler,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, registry.StartConsumers(ctx))
	return registry
}

// stopAsync runs StopConsumers on its own goroutine, calls atReturn the moment it
// returns, and closes the returned channel after that.
func stopAsync(registry *Registry, atReturn func()) <-chan struct{} {
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		registry.StopConsumers()
		atReturn()
	}()
	return stopped
}

// awaitStopBegun waits until StopConsumers has canceled the consumers, then gives an
// unjoined stop the grace to return first, so a stop that does not wait is seen doing so.
func awaitStopBegun(t *testing.T, log *recordingLogger, stopped <-chan struct{}) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, line := range log.Lines() {
			if line.Msg == msgStoppingAllConsumers {
				return true
			}
		}
		return false
	}, 5*time.Second, time.Millisecond, "StopConsumers never began")
	select {
	case <-stopped:
	case <-time.After(stopReleaseGrace):
	}
}

func deliveriesFor(n int) chan amqp.Delivery {
	ch := make(chan amqp.Delivery, n)
	for range n {
		ch <- amqp.Delivery{MessageId: testMessageID, Body: []byte(testMessageBody), Headers: amqp.Table{}, Acknowledger: &mockAcknowledger{}}
	}
	return ch
}

// TestRegistryStopConsumersJoinsSupervisors pins the join: once StopConsumers returns,
// the handler it interrupted has finished and no later delivery reaches a handler, even
// with deliveries already buffered for the worker pool.
func TestRegistryStopConsumersJoinsSupervisors(t *testing.T) {
	deliveries := deliveriesFor(5)
	client := &resubscribingMockClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		results:              []consumeResult{{ch: deliveries}},
	}
	log := newRecordingLogger()
	handler := newGatedHandler()
	registry := startStopRegistry(t, client, log, handler)

	select {
	case <-handler.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never entered")
	}

	var callsAtReturn, finishedAtReturn int64
	stopped := stopAsync(registry, func() {
		callsAtReturn = handler.calls.Load()
		finishedAtReturn = handler.finished.Load()
	})
	awaitStopBegun(t, log, stopped)
	close(handler.release)
	<-stopped

	assert.Positive(t, finishedAtReturn, "StopConsumers returned before the in-flight handler finished")
	assert.Equal(t, callsAtReturn, finishedAtReturn, "a handler was still running when StopConsumers returned")
	assert.Zero(t, registry.runningSupervisors.Load())
	log.Line(t, msgAllConsumersStopped)

	time.Sleep(stopReleaseGrace)
	assert.Equal(t, callsAtReturn, handler.calls.Load(), "a handler ran after StopConsumers returned")
}

// TestRegistryStopConsumersAbandonsAStuckHandlerAfterTheBudget pins the bound: a handler
// that ignores its canceled context cannot hold the stop past its budget, and the stop
// says how many supervisors it left behind.
func TestRegistryStopConsumersAbandonsAStuckHandlerAfterTheBudget(t *testing.T) {
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

	select {
	case <-handler.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never entered")
	}

	start := time.Now()
	registry.StopConsumers()
	assert.Less(t, time.Since(start), stopReturnsPromptlyLimit, "a stuck handler held StopConsumers past its budget")

	line := log.Line(t, msgSupervisorsAbandoned)
	assert.Equal(t, []string{"1"}, line.Values("running_supervisors"))
	assert.Equal(t, []string{shortStopBudgetForTests.String()}, line.Values("stop_budget"))
	for _, l := range log.Lines() {
		assert.NotEqual(t, msgAllConsumersStopped, l.Msg, "a stop that gave up reported the consumers stopped")
	}
}

// TestRegistryStopConsumersJoinsAnInFlightResubscribe pins the re-subscribe half of the
// join: a re-subscribe the stop interrupted has either landed before StopConsumers
// returns, or been abandoned past the budget — never landed after it.
func TestRegistryStopConsumersJoinsAnInFlightResubscribe(t *testing.T) {
	tests := []struct {
		name            string
		budget          time.Duration
		release         bool
		wantResubscribe uint64
		wantAbandoned   bool
	}{
		{name: "completed_within_budget", budget: consumerStopBudget, release: true, wantResubscribe: 1},
		{name: "abandoned_past_budget", budget: shortStopBudgetForTests, release: false, wantResubscribe: 0, wantAbandoned: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newGatedResubscribeClient()
			log := newRecordingLogger()
			registry := startStopRegistry(t, client, log, &countingTestHandler{})
			registry.stopBudget = tt.budget
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(client.release) }) }
			t.Cleanup(func() {
				release()
				require.Eventually(t, func() bool { return registry.runningSupervisors.Load() == 0 },
					5*time.Second, time.Millisecond, "supervisor did not exit once released")
			})

			close(client.first)
			select {
			case <-client.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("re-subscribe never started")
			}

			var resubscribesAtReturn uint64
			var runningAtReturn int64
			stopped := stopAsync(registry, func() {
				resubscribesAtReturn = registry.ConsumerStates()[0].Resubscribes
				runningAtReturn = registry.runningSupervisors.Load()
			})
			awaitStopBegun(t, log, stopped)
			if tt.release {
				release()
			}
			<-stopped

			assert.Equal(t, tt.wantResubscribe, resubscribesAtReturn)
			var abandoned []string
			for _, line := range log.Lines() {
				if line.Msg == msgSupervisorsAbandoned {
					abandoned = append(abandoned, line.Values("running_supervisors")...)
				}
			}
			if tt.wantAbandoned {
				assert.Equal(t, []string{"1"}, abandoned)
				return
			}
			assert.Empty(t, abandoned)
			assert.Zero(t, runningAtReturn)
			time.Sleep(stopReleaseGrace)
			assert.Equal(t, tt.wantResubscribe, registry.ConsumerStates()[0].Resubscribes,
				"a re-subscribe landed after StopConsumers returned")
		})
	}
}

// TestRegistryStopConsumersTwiceReturnsPromptly pins idempotence: Close stops a registry
// StopConsumers already stopped, and the second call has nothing left to wait for.
func TestRegistryStopConsumersTwiceReturnsPromptly(t *testing.T) {
	client, _ := newOutageClient()
	log := newRecordingLogger()
	registry := startStopRegistry(t, client, log, &countingTestHandler{})

	registry.StopConsumers()
	start := time.Now()
	registry.StopConsumers()
	assert.Less(t, time.Since(start), stopReturnsPromptlyLimit)
	assert.Zero(t, registry.runningSupervisors.Load())
	for _, line := range log.Lines() {
		assert.NotEqual(t, msgSupervisorsAbandoned, line.Msg)
	}
}

// TestRegistryStopConsumersDoesNotWaitAgainAfterAbandoning pins the second stop of a
// group the first stop gave up on: it must not spend another budget on the same stuck
// supervisors, only warn with how many are still running.
func TestRegistryStopConsumersDoesNotWaitAgainAfterAbandoning(t *testing.T) {
	client := &resubscribingMockClient{
		simpleMockAMQPClient: &simpleMockAMQPClient{isReady: true},
		results:              []consumeResult{{ch: deliveriesFor(1)}},
	}
	log := newRecordingLogger()
	handler := newGatedHandler()
	registry := startStopRegistry(t, client, log, handler)
	t.Cleanup(func() {
		close(handler.release)
		require.Eventually(t, func() bool { return registry.runningSupervisors.Load() == 0 },
			5*time.Second, time.Millisecond, "supervisor did not exit once released")
	})

	select {
	case <-handler.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never entered")
	}

	registry.stopBudget = shortStopBudgetForTests
	registry.StopConsumers()
	log.Line(t, msgSupervisorsAbandoned)

	registry.stopBudget = consumerStopBudget
	start := time.Now()
	registry.StopConsumers()
	assert.Less(t, time.Since(start), stopReturnsPromptlyLimit, "a second stop waited again on abandoned supervisors")
	line := log.Line(t, msgSupervisorsStillAbandoned)
	assert.Equal(t, []string{"1"}, line.Values("running_supervisors"))
}
