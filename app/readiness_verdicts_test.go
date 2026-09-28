package app

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

const (
	warnUnhealthyMsg = "Readiness component unhealthy"
	infoRecoveredMsg = "Readiness component recovered"
)

// testClock is the injected clock the WARN interval is judged against. A real-clock test would
// be a loose time bound and a false green: it could never prove the line was suppressed for a
// minute rather than for the microseconds the test itself took.
type testClock struct{ at time.Time }

func (c *testClock) now() time.Time          { return c.at }
func (c *testClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// newTestVerdictStore builds a store over a recording logger and a fake clock.
func newTestVerdictStore() (*verdictStore, *recLogger, *testClock) {
	rec := &recLogger{}
	clock := &testClock{at: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	return newVerdictStore(rec, clock.now), rec, clock
}

func unhealthy(name string, critical bool, err error) *HealthStatus {
	return &HealthStatus{Name: name, Status: unhealthyStatus, Critical: critical, Err: err}
}

func healthy(name string, critical bool) *HealthStatus {
	return &HealthStatus{Name: name, Status: healthyStatus, Critical: critical}
}

// TestVerdictStoreWarnsOnTheTransitionThenOnceAMinute pins the rate floor at both ends: the
// line fires the moment a non-critical kind goes unhealthy, stays quiet for the rest of the
// minute however often it is judged, and fires again exactly at the interval.
func TestVerdictStoreWarnsOnTheTransitionThenOnceAMinute(t *testing.T) {
	store, rec, clock := newTestVerdictStore()
	probeErr := errors.New("dial tcp 10.0.0.5:6379: connection refused")

	store.record(unhealthy(componentCache, false, probeErr), clock.at)
	require.Len(t, rec.linesWith(warnUnhealthyMsg), 1, "the transition into unhealthy is reported")

	warn := rec.linesWith(warnUnhealthyMsg)[0]
	assert.Equal(t, "warn", warn.level)
	assert.Equal(t, componentCache, warn.str["component"])
	assert.False(t, warn.bools["critical"])
	assert.Equal(t, probeErr.Error(), warn.err, "the log carries the full probe error")

	clock.advance(readinessWarnInterval - time.Second)
	store.record(unhealthy(componentCache, false, probeErr), clock.at)
	store.record(unhealthy(componentCache, false, probeErr), clock.at)
	assert.Len(t, rec.linesWith(warnUnhealthyMsg), 1, "no second line inside the minute")

	clock.advance(time.Second)
	store.record(unhealthy(componentCache, false, probeErr), clock.at)
	assert.Len(t, rec.linesWith(warnUnhealthyMsg), 2, "the line repeats once the interval elapses")
}

// TestVerdictStoreReportsRecoveryOnce pins the end of the incident: one INFO on the way back to
// healthy, nothing while it stays healthy, and a re-armed WARN if it fails again inside the
// same minute — a new outage is a transition, not a repeat.
func TestVerdictStoreReportsRecoveryOnce(t *testing.T) {
	store, rec, clock := newTestVerdictStore()
	probeErr := errors.New("connection refused")

	store.record(unhealthy(componentCache, false, probeErr), clock.at)
	store.record(healthy(componentCache, false), clock.at)
	store.record(healthy(componentCache, false), clock.at)

	recovered := rec.linesWith(infoRecoveredMsg)
	require.Len(t, recovered, 1, "recovery is reported once, not on every healthy judgment")
	assert.Equal(t, "info", recovered[0].level)
	assert.Equal(t, componentCache, recovered[0].str["component"])

	store.record(unhealthy(componentCache, false, probeErr), clock.at)
	assert.Len(t, rec.linesWith(warnUnhealthyMsg), 2,
		"failing again is a fresh transition, whatever the interval says")
}

// TestVerdictStoreSaysNothingForACriticalKind is the whole of the critical guard. A critical
// kind's outage is already an ERROR from readyCheck, and these lines exist only for the
// non-critical outages /ready answers 200 through — so a critical kind must produce neither the
// WARN nor the recovery INFO, however long it stays down.
func TestVerdictStoreSaysNothingForACriticalKind(t *testing.T) {
	store, rec, clock := newTestVerdictStore()
	probeErr := errors.New("connection refused")

	store.record(unhealthy(componentDatabase, true, probeErr), clock.at)
	clock.advance(2 * readinessWarnInterval)
	store.record(unhealthy(componentDatabase, true, probeErr), clock.at)
	store.record(healthy(componentDatabase, true), clock.at)

	assert.Empty(t, rec.linesWith(warnUnhealthyMsg), "a critical kind keeps readyCheck's ERROR line alone")
	assert.Empty(t, rec.linesWith(infoRecoveredMsg), "and reports no recovery of its own")
}

// TestVerdictStoreKeepsTheLastVerdictPerKind pins what the gauge reads: one entry per judged
// kind, carrying that kind's latest status and its critical setting. The order is the map's and
// pinned nowhere — the SDK's own datapoint slice does not preserve observation order anyway.
func TestVerdictStoreKeepsTheLastVerdictPerKind(t *testing.T) {
	store, _, clock := newTestVerdictStore()

	store.record(unhealthy(componentCache, false, errors.New("connection refused")), clock.at)
	store.record(healthy(componentDatabase, true), clock.at)
	store.record(healthy(componentCache, false), clock.at)

	assert.ElementsMatch(t, []verdictReading{
		{kind: componentCache, status: healthyStatus, critical: false},
		{kind: componentDatabase, status: healthyStatus, critical: true},
	}, store.readings())
}

// TestVerdictStoreToleratesNoStore pins advisory item 16: every hand-built App in these tests
// carries no store, so both methods must answer on a nil receiver rather than panic.
func TestVerdictStoreToleratesNoStore(t *testing.T) {
	var store *verdictStore

	assert.NotPanics(t, func() { store.record(healthy(componentCache, false), time.Time{}) })
	assert.Nil(t, store.readings())
}

// gatedVerdictLogger is the recorder with one rendezvous inside record's write path: the first
// WARN parks in Warn(), where its commit has already returned, until the test opens the gate.
type gatedVerdictLogger struct {
	*recLogger
	once    sync.Once
	entered chan struct{} // closed as that WARN reaches the gate
	gate    chan struct{} // closed by the test to let it write
}

func (l *gatedVerdictLogger) Warn() logger.LogEvent {
	l.once.Do(func() { close(l.entered) })
	<-l.gate
	return l.recLogger.Warn()
}

// TestVerdictStoreOrdersItsTransitionLinesAcrossJudgments pins the emission order against the
// commits behind it. full() bypasses /ready's singleflight, so one judgment can commit a
// non-critical kind unhealthy while a second commits its recovery, and with the writes
// unserialized the INFO lands before the WARN it followed — a log in which the incident ends
// before it began. The gate holds the first write open, so an unserialized emitter inverts the
// pair whenever the recovery is scheduled inside the grace; a serialized one keeps the second
// judgment outside record entirely, so the wait below bounds the test's duration and never
// decides its verdict.
func TestVerdictStoreOrdersItsTransitionLinesAcrossJudgments(t *testing.T) {
	rec := &recLogger{}
	clock := &testClock{at: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	gated := &gatedVerdictLogger{recLogger: rec, entered: make(chan struct{}), gate: make(chan struct{})}
	store := newVerdictStore(gated, clock.now)

	outage := make(chan struct{})
	go func() {
		defer close(outage)
		store.record(unhealthy(componentCache, false, errors.New("connection refused")), clock.at)
	}()
	select {
	case <-gated.entered:
	case <-time.After(time.Second):
		t.Fatal("the unhealthy WARN never reached the gate")
	}

	recovery := make(chan struct{})
	go func() {
		defer close(recovery)
		store.record(healthy(componentCache, false), clock.at)
	}()
	select {
	case <-recovery: // unserialized: the recovery committed and wrote its line already
	case <-time.After(200 * time.Millisecond): // serialized: it is parked outside record
	}
	close(gated.gate)
	<-outage
	<-recovery

	lines := rec.linesWith("Readiness component ")
	require.Len(t, lines, 2, "one transition each: into unhealthy, then back out")
	assert.Equal(t, warnUnhealthyMsg, lines[0].msg, "the outage is reported before the recovery that followed it")
	assert.Equal(t, infoRecoveredMsg, lines[1].msg)
}

// TestVerdictStoreDropsAStaleCommit pins the ordering serialized commits alone do not buy.
// full() bypasses /ready's singleflight, so a slow debug judgment can probe a kind, spend the
// rest of its budget on the kinds behind it, and commit after a later /ready flight already
// committed a fresher verdict for that same kind. Storing it anyway would flip the gauge back to
// 1 for a kind that is still down and report a recovery that never happened. Driving it through
// readinessReport.record pins the plumbing too: a record stamped at commit time rather than
// carrying the probe's startedAt would let the stale reading win.
func TestVerdictStoreDropsAStaleCommit(t *testing.T) {
	store, rec, clock := newTestVerdictStore()
	probedAt := clock.at

	// The debug judgment observed the kind healthy first; the /ready flight observed it
	// unhealthy 400ms later and commits first.
	fresh := readinessReport{{
		status:    *unhealthy(componentMessaging, false, errors.New("connection refused")),
		startedAt: probedAt.Add(400 * time.Millisecond),
	}}
	stale := readinessReport{{status: *healthy(componentMessaging, false), startedAt: probedAt}}

	fresh.record(store)
	require.Len(t, rec.linesWith(warnUnhealthyMsg), 1,
		"a kind's first verdict has nothing to compare against and stores")

	stale.record(store)

	assert.Equal(t, []verdictReading{{kind: componentMessaging, status: unhealthyStatus}}, store.readings(),
		"the older reading does not overwrite the fresher one")
	assert.Empty(t, rec.linesWith(infoRecoveredMsg), "a dropped commit reports no recovery")
	assert.Len(t, rec.linesWith(warnUnhealthyMsg), 1, "and no line of its own")

	mp := registerTestGauges(t, gaugeSources{verdicts: store})
	points := gaugeDataPoints(t, mp.Collect(t), metricReadinessStatus)
	require.Len(t, points, 1)
	assert.Equal(t, int64(0), points[0].Value, "the gauge still reports the kind down")
}

// TestConcurrentJudgmentsRecordSafely drives the two judgment entry points against one store the
// way the endpoints do: /ready shares its judgment through a singleflight, but
// /_sys/health-debug's full() bypasses it, so a health-debug request and a /ready request judge
// — and record — at the same time, over the same kinds. CI runs this under -race.
func TestConcurrentJudgmentsRecordSafely(t *testing.T) {
	store, _, _ := newTestVerdictStore()
	judge := judgeOf(
		describe(componentDatabase, true, nil, nil, nil),
		describe(componentCache, false, errors.New("connection refused"), nil, nil),
	)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for range 25 {
			judge.full(t.Context()).record(store)
		}
	}()
	go func() {
		defer wg.Done()
		for range 25 {
			report, _, _ := judge.gate(t.Context())
			report.record(store)
		}
	}()
	go func() {
		defer wg.Done()
		for range 25 {
			store.readings()
		}
	}()
	wg.Wait()

	assert.ElementsMatch(t, []verdictReading{
		{kind: componentCache, status: unhealthyStatus, critical: false},
		{kind: componentDatabase, status: healthyStatus, critical: true},
	}, store.readings())
}
