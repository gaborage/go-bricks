package app

import (
	"sync"
	"time"

	"github.com/gaborage/go-bricks/logger"
)

// readinessWarnInterval bounds how often a non-critical kind's unhealthy WARN repeats while
// it stays unhealthy. A fixed constant and not a config key: with observability disabled these
// lines are the only in-process signal that a degraded kind is still degraded (ADR-120), so
// there is nothing to tune down to.
const readinessWarnInterval = time.Minute

// verdictStore remembers, per kind, the status the most recent readiness judgment recorded for
// it. It is the readiness gauge's only source: judging never reads it back, so a last verdict
// is a view and never an input (ADR-120).
//
// Only a judged kind has an entry, and an entry is never cleared. A /ready judgment stops at
// the first failing critical kind (readinessJudge.gate), so the kinds behind it are not judged
// on that pass and keep the verdict their last judgment gave them — the freshest answer anyone
// has. One consequence is deliberate: a non-critical kind stuck unhealthy behind a failing
// critical kind stops being re-recorded, so its once-a-minute WARN goes quiet until a judgment
// reaches it again. The lines are judgment-driven, exactly like the gauge.
//
// readinessFailure's `readiness` pseudo-kind never reaches this store: the judge synthesizes it
// outside walk, so it is not a kind and never gets a series.
type verdictStore struct {
	// lineMu orders the transition lines, never the state: record holds it across its commit and
	// the write that follows, so two judgments report their transitions in the order they
	// committed them. The entries stay under mu alone.
	lineMu  sync.Mutex
	mu      sync.Mutex
	entries map[string]verdictEntry
	logger  logger.Logger
	// now is injected so the WARN interval is testable without a real-clock time bound.
	now func() time.Time
}

// verdictEntry is one kind's last verdict and the WARN bookkeeping behind it.
type verdictEntry struct {
	status   string
	critical bool
	// warnedAt is when this kind's unhealthy WARN last fired, and nothing else: only a fresh
	// WARN moves it, so it survives the recovery and every healthy judgment after it. Zero
	// means this kind has never warned — which is every critical kind, none of which reports.
	warnedAt time.Time
}

// verdictReading is one kind's last verdict as the gauge callback reads it.
type verdictReading struct {
	kind     string
	status   string
	critical bool
}

// readinessLine is the transition one record owes an operator: decided while the store's lock
// is held, emitted once it is released.
type readinessLine uint8

const (
	noLine readinessLine = iota
	unhealthyLine
	recoveredLine
)

// newVerdictStore builds the store the judgment entry points write and the gauge reads. Both
// halves of the framework hand it App's logger and the real clock; only tests pass a fake one.
func newVerdictStore(log logger.Logger, now func() time.Time) *verdictStore {
	return &verdictStore{entries: make(map[string]verdictEntry), logger: log, now: now}
}

// record stores what one judgment decided about one kind, and logs the transitions a
// non-critical kind's outage would otherwise leave unreported — a critical kind already carries
// readyCheck's ERROR line. A nil receiver records nothing, so a hand-built App without a store
// still judges.
//
// The line is emitted with commit's lock released, so the exporter's readings() never queues
// behind a log write. lineMu is held across the pair instead: a judgment that commits second
// cannot report its transition before the one that committed first.
func (s *verdictStore) record(result *HealthStatus) {
	if s == nil {
		return
	}

	s.lineMu.Lock()
	defer s.lineMu.Unlock()

	switch s.commit(result) {
	case unhealthyLine:
		s.logger.Warn().
			Err(result.Err).
			Str("component", result.Name).
			Bool("critical", result.Critical).
			Msg("Readiness component unhealthy")
	case recoveredLine:
		s.logger.Info().
			Str("component", result.Name).
			Bool("critical", result.Critical).
			Msg("Readiness component recovered")
	case noLine:
		// Nothing to report: the kind is critical, its status did not transition, or it is
		// still inside its WARN interval.
	}
}

// commit stores one kind's verdict and returns the line its transition owes. Deciding under the
// lock is what keeps the emitter single: full() bypasses /ready's singleflight
// (app/debug_health.go), so two judgments can be in flight at once, and the committed warnedAt
// is the arbiter — whichever commits second reads what the first wrote and stays quiet.
func (s *verdictStore) commit(result *HealthStatus) readinessLine {
	s.mu.Lock()
	defer s.mu.Unlock()

	previous := s.entries[result.Name]
	entry := verdictEntry{status: result.Status, critical: result.Critical}
	line := noLine
	if !result.Critical {
		line, entry.warnedAt = s.transition(result.Status, previous)
	}
	s.entries[result.Name] = entry
	return line
}

// transition decides the line one non-critical verdict owes and the warnedAt to commit beside
// it. Entry into unhealthy always warns, whatever the interval says — that is what the
// isFailing(previous.status) conjunct buys — and the line then repeats at
// readinessWarnInterval while the kind stays unhealthy. Every other verdict carries the
// previous warnedAt through untouched.
func (s *verdictStore) transition(status string, previous verdictEntry) (readinessLine, time.Time) {
	switch {
	case isFailing(status):
		now := s.now()
		if isFailing(previous.status) && now.Sub(previous.warnedAt) < readinessWarnInterval {
			return noLine, previous.warnedAt
		}
		return unhealthyLine, now
	case status == healthyStatus && isFailing(previous.status):
		return recoveredLine, previous.warnedAt
	default:
		return noLine, previous.warnedAt
	}
}

// readings is the gauge's view of every judged kind, in whatever order the map yields. Kind
// order would buy nothing: the SDK rebuilds a gauge's datapoint slice by ranging its own sync
// map of observations (sdk/metric internal/aggregate/lastvalue.go, copyAndClearDpts), so
// observation order does not survive into the collection either way. A nil receiver has no
// readings.
func (s *verdictStore) readings() []verdictReading {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	readings := make([]verdictReading, 0, len(s.entries))
	for kind, entry := range s.entries {
		readings = append(readings, verdictReading{kind: kind, status: entry.status, critical: entry.critical})
	}
	return readings
}
