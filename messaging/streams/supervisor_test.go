package streams

import (
	"context"
	"testing"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/ha"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// supervisorWait bounds a wait on the supervisor goroutine, so a regression fails
// the test instead of hanging the suite. Nothing asserts on elapsed time.
const supervisorWait = 5 * time.Second

// supervisedManager builds a manager whose supervisor never ticks on its own, so a
// test drives every pass itself. The count threshold keeps a delivery pending in
// the offset book rather than committed.
func supervisedManager() (*Manager, *recordingLogger) {
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, OffsetStoreCount: 1000, Logger: log})
	m.superviseEvery = time.Hour
	return m, log
}

// twoStreamDecls declares a consumer and a publisher on each of two plain
// streams: the first one is the stream a test loses, the second one must not
// notice.
func twoStreamDecls() *Declarations {
	decls := NewDeclarations()
	for _, name := range []string{testStream, secondTestStream} {
		decls.DeclareStream(name, nil)
		decls.DeclareConsumer(&ConsumerOptions{Stream: name, Name: testConsumerName, Handler: noopHandler})
		decls.DeclarePublisher(&PublisherOptions{Stream: name})
	}
	return decls
}

func TestSupervisorReportsAnUnexpectedlyClosedHandleOnce(t *testing.T) {
	m, log := supervisedManager()
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, twoStreamDecls())
	t.Cleanup(m.StopConsumers)

	fake.consumer(testStream).events.setStatus(ha.StatusClosed)
	fake.producer(testStream).setStatus(ha.StatusClosed)
	for range 3 {
		m.superviseOnce(context.Background())
	}

	assert.ElementsMatch(t, []string{msgConsumerLost, msgPublisherLost}, log.messagesAt("error"),
		"one ERROR per handle that closed, however many passes see it closed")
	assert.Equal(t, testStream, log.fieldAt("error", msgConsumerLost, logFieldStream))
	assert.Equal(t, testConsumerName, log.fieldAt("error", msgConsumerLost, logFieldConsumer))
	assert.Equal(t, testStream, log.fieldAt("error", msgPublisherLost, logFieldStream))
}

func TestSupervisorKeepsALostConsumersLivePartitionsCommitting(t *testing.T) {
	log := &recordingLogger{}
	m := NewManager(ManagerOptions{URI: unreachableTestURI, OffsetStoreCount: 1, Logger: log})
	m.superviseEvery = time.Hour
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, superConsumerDecls())
	t.Cleanup(m.StopConsumers)
	consumer := fake.consumer(testSuperStream)

	consumer.events.setStatus(ha.StatusClosed)
	m.superviseOnce(context.Background())
	consumer.deliver(testPartition1, 9, amqpMessage("still delivered"))

	require.Equal(t, []string{msgConsumerLost}, log.messagesAt("error"))
	assert.Equal(t, 1, countOf(fake.recorded(), callConsumerStore+":"+testConsumerName+"/"+testPartition1+"=9"),
		"a partition the client still delivers on keeps committing")
}

func TestSupervisorLeavesALostStreamDown(t *testing.T) {
	m, _ := supervisedManager()
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, twoStreamDecls())
	t.Cleanup(m.StopConsumers)
	calls := len(fake.recorded())

	fake.consumer(testStream).events.setStatus(ha.StatusClosed)
	fake.producer(testStream).setStatus(ha.StatusClosed)
	m.superviseOnce(context.Background())
	m.superviseOnce(context.Background())

	assert.Empty(t, fake.recorded()[calls:], "nothing is re-declared or reattached")
	assert.False(t, m.Ready(), "the streams component stays unhealthy")
}

func TestSupervisorKeepsTheComponentUnhealthyOnceAHandleIsLost(t *testing.T) {
	tests := []struct {
		name      string
		setStatus func(*fakeEnvironment, int)
	}{
		{name: "consumer", setStatus: func(f *fakeEnvironment, s int) { f.consumer(testSuperStream).events.setStatus(s) }},
		{name: "publisher", setStatus: func(f *fakeEnvironment, s int) { f.producer(testSuperStream).setStatus(s) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := supervisedManager()
			fake := newFakeEnvironment()
			decls := superConsumerDecls()
			decls.DeclareSuperStreamPublisher(&SuperStreamPublisherOptions{SuperStream: testSuperStream})
			startOnFake(t, m, fake, decls)
			t.Cleanup(m.StopConsumers)
			require.True(t, m.Ready())

			tt.setStatus(fake, ha.StatusClosed)
			m.superviseOnce(context.Background())
			// The client reports a super-stream handle open again once another
			// partition reconnects.
			tt.setStatus(fake, ha.StatusOpen)

			assert.False(t, m.Ready(), "a lost handle keeps the streams component unhealthy")
		})
	}
}

func TestSupervisorReportsAPublisherLostAgainAfterARestart(t *testing.T) {
	m, log := supervisedManager()
	fake := newFakeEnvironment()
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclarePublisher(&PublisherOptions{Stream: testStream})
	startOnFake(t, m, fake, decls)
	fake.producer(testStream).setStatus(ha.StatusClosed)
	m.superviseOnce(context.Background())
	require.NoError(t, m.Close())

	startOnFake(t, m, fake, decls)
	t.Cleanup(func() { _ = m.Close() })
	fake.producer(testStream).setStatus(ha.StatusClosed)
	m.superviseOnce(context.Background())

	assert.Equal(t, []string{msgPublisherLost, msgPublisherLost}, log.messagesAt("error"),
		"the same publisher, lost again after a restart, is reported again")
}

func TestSupervisorPassAfterItsRunEndedReportsNothing(t *testing.T) {
	m, log := supervisedManager()
	fake := newFakeEnvironment()
	startOnFake(t, m, fake, oneConsumerDecls())
	t.Cleanup(m.StopConsumers)
	ended, cancel := context.WithCancel(context.Background())
	cancel()

	fake.consumer(testStream).events.setStatus(ha.StatusClosed)
	m.superviseOnce(ended)

	assert.Empty(t, log.messagesAt("error"),
		"a supervisor whose run ended leaves the handles of a later Start alone")
}

func TestNewManagerPollsEveryFiveSeconds(t *testing.T) {
	m := NewManager(ManagerOptions{URI: unreachableTestURI, Logger: &recordingLogger{}})

	assert.Equal(t, 5*time.Second, m.superviseEvery)
}

// awaitClosed fails the test if ch does not close within supervisorWait.
func awaitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(supervisorWait):
		t.Fatal(what)
	}
}

// secondSuperStream is the super stream a lost-super-stream test keeps healthy.
const secondSuperStream = "shipments-partitioned"

func TestSupervisorNeverFlushesALostSuperStreamPosition(t *testing.T) {
	m, log := supervisedManager()
	fake := newFakeEnvironment()
	decls := superConsumerDecls()
	decls.DeclareSuperStream(secondSuperStream, testPartitions, nil)
	decls.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
		SuperStream: secondSuperStream, Name: secondConsumerName, Handler: noopHandler,
	})
	startOnFake(t, m, fake, decls)
	lost, kept := fake.consumer(testSuperStream), fake.consumer(secondSuperStream)
	lost.deliver(testPartition0, 41, amqpMessage("before"))
	kept.deliver(secondSuperStream+"-0", 7, amqpMessage("kept"))

	lost.events.setStatus(ha.StatusClosed)
	m.superviseOnce(context.Background())
	m.StopConsumers()

	recorded := fake.recorded()
	assert.Zero(t, countOf(recorded, callStoreOffset+":"+testConsumerName+"/"+testPartition0+"=41"),
		"a lost consumer's pending position is never flushed")
	assert.Equal(t, 1, countOf(recorded, callStoreOffset+":"+secondConsumerName+"/"+secondSuperStream+"-0=7"),
		"the shutdown flush still commits a healthy super stream's position")
	assert.Equal(t, []string{testSuperStream}, log.warnStreams(msgLostFlushSkipped),
		"the skipped flush is reported, naming only the lost consumer's stream")
}

func TestSupervisorExitsOnShutdown(t *testing.T) {
	tests := []struct {
		name string
		stop func(*Manager)
	}{
		{name: "stop_consumers", stop: func(m *Manager) { m.StopConsumers() }},
		{name: "close", stop: func(m *Manager) { _ = m.Close() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, log := supervisedManager()
			m.superviseEvery = time.Millisecond
			m.flushBudget = 100 * time.Millisecond
			fake := newFakeEnvironment()
			startOnFake(t, m, fake, oneConsumerDecls())
			consumer := fake.consumer(testStream)
			// The client marks a handle closed when it is closed on purpose too.
			consumer.events.onClose = func() { consumer.events.setStatus(ha.StatusClosed) }
			m.mu.Lock()
			done := m.supervisorDone
			m.mu.Unlock()
			require.NotNil(t, done)
			reads := consumer.events.statusReadCount()
			require.Eventually(t, func() bool { return consumer.events.statusReadCount() >= reads+2 },
				supervisorWait, time.Millisecond, "the supervisor is not running passes")

			tt.stop(m)

			select {
			case <-done:
			default:
				t.Fatal("the supervisor goroutine outlived the shutdown")
			}
			m.mu.Lock()
			assert.Nil(t, m.supervisorDone, "a stop forgets the supervisor it stopped")
			m.mu.Unlock()
			assert.NotContains(t, log.warnMessages(), msgSupervisorAbandoned)
			assert.Empty(t, log.messagesAt("error"), "an orderly shutdown is never reported")
		})
	}
}

func TestAwaitSupervisorGivesUpWithTheStopPhase(t *testing.T) {
	m, log := supervisedManager()
	m.flushBudget = time.Hour
	stopCtx, cancel := context.WithCancel(context.Background())
	cancel()

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		m.awaitSupervisor(stopCtx, make(chan struct{}))
	}()

	awaitClosed(t, returned, "the wait took a budget of its own instead of the stop phase's")
	assert.Contains(t, log.warnMessages(), msgSupervisorAbandoned)
}

func TestAwaitSupervisorReportsNoAbandonOnceItExited(t *testing.T) {
	m, log := supervisedManager()
	stopCtx, cancel := context.WithCancel(context.Background())
	cancel()
	exited := make(chan struct{})
	close(exited)

	for range 100 {
		m.awaitSupervisor(stopCtx, exited)
	}

	assert.NotContains(t, log.warnMessages(), msgSupervisorAbandoned,
		"a supervisor that already exited is not abandoned, however spent the budget")
}

func TestSupervisorIsNotStartedWithNothingToWatch(t *testing.T) {
	m, _ := supervisedManager()
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	startOnFake(t, m, newFakeEnvironment(), decls)
	t.Cleanup(m.StopConsumers)

	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Nil(t, m.supervisorDone)
}
