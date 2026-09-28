package streams

import (
	"context"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/ha"
)

// superviseInterval is how often the supervisor polls every handle's status: the
// client has no close callback.
const superviseInterval = 5 * time.Second

// The supervisor's log lines.
const (
	msgConsumerLost = "Stream consumer closed unexpectedly and will not reconnect - its stream was likely deleted; " +
		"it stays down until the service restarts"
	msgPublisherLost = "Stream publisher closed unexpectedly and will not reconnect - its stream was likely deleted; " +
		"it stays down until the service restarts"
	msgSupervisorAbandoned = "Stream supervisor did not stop within the shutdown budget; abandoning it"
	msgLostFlushSkipped    = "Skipped the shutdown offset flush of a lost stream consumer - another replica may have " +
		"re-created its stream under the same name; handled messages will replay"
)

// startSupervisorLocked watches the handles Start opened, under the consumers'
// context so that stopLocked's cancel stops it.
func (m *Manager) startSupervisorLocked(ctx context.Context) {
	if len(m.consumers) == 0 && len(m.publishers) == 0 {
		return
	}
	done := make(chan struct{})
	m.supervisorDone = done
	go m.supervise(ctx, done)
}

func (m *Manager) detachSupervisorLocked() <-chan struct{} {
	done := m.supervisorDone
	m.supervisorDone = nil
	return done
}

// awaitSupervisor waits for the supervisor to exit until ctx, the stop phase's
// one flush budget, is done.
func (m *Manager) awaitSupervisor(ctx context.Context, done <-chan struct{}) {
	if done == nil {
		return
	}
	select {
	case <-done:
		return
	default:
	}

	select {
	case <-done:
	case <-ctx.Done():
		m.log.Warn().Dur("flush_budget", m.flushBudget).Msg(msgSupervisorAbandoned)
	}
}

func (m *Manager) supervise(ctx context.Context, done chan<- struct{}) {
	defer close(done)

	ticker := time.NewTicker(m.superviseEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.superviseOnce(ctx)
		}
	}
}

// superviseOnce reports what closed since the last pass. An orderly stop is never
// reported: stopLocked empties the handle lists under m.mu before it releases the
// lock. The ctx check keeps a supervisor whose stop stopped waiting for it off the
// handles of a later Start.
func (m *Manager) superviseOnce(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ctx.Err() != nil {
		return
	}
	m.reportClosedLocked()
}

// reportClosedLocked reports each handle found closed for the first time and
// marks a lost consumer so the shutdown flush skips it.
func (m *Manager) reportClosedLocked() {
	for _, rc := range m.consumers {
		if rc.lost || rc.handle.GetStatus() != ha.StatusClosed {
			continue
		}
		rc.lost = true
		m.reportLostConsumer(rc)
	}
	for _, p := range m.publishers {
		if p.status() != ha.StatusClosed {
			continue
		}
		if m.markPublisherLostLocked(p) {
			m.reportLostPublisher(p)
		}
	}
}

// markPublisherLostLocked records p as accounted for and reports whether it was
// not already.
func (m *Manager) markPublisherLostLocked(p *Publisher) bool {
	if m.lostPublishers[p] {
		return false
	}
	if m.lostPublishers == nil {
		m.lostPublishers = map[*Publisher]bool{}
	}
	m.lostPublishers[p] = true
	return true
}

func (m *Manager) reportLostConsumer(rc *runningConsumer) {
	m.log.Error().
		Str(logFieldStream, rc.stream).
		Str(logFieldConsumer, rc.name).
		Bool(logFieldPartitioned, rc.decl.Super).
		Msg(msgConsumerLost)
}

func (m *Manager) reportLostPublisher(p *Publisher) {
	m.log.Error().
		Str(logFieldStream, p.stream).
		Bool(logFieldPartitioned, p.super).
		Msg(msgPublisherLost)
}
