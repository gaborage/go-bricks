package scheduler

import (
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gaborage/go-bricks/logger"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

const (
	maxFailingKeysListed = 10
	maxEdgesPerWindow    = 2

	edgeNew  = "new"
	edgeGrew = "grew"

	jobStateClean     = "clean"
	jobStateFailing   = "failing"
	jobStateRecovered = "recovered"
)

// runOutcome is one completed run as its window sees it.
type runOutcome struct {
	ok            []int64
	failing       []int64
	keys          map[string]bool
	err           error
	traceparent   string
	correlationID string
}

// windowTally is what one window has counted so far.
type windowTally struct {
	runs        int64
	failingRuns int64
	slowRuns    int64
	skipped     int64
	ok          []int64
	failing     []int64
	keys        map[string]bool
	last        *runOutcome

	edges           int
	edgesSuppressed int64
}

// windowState is one Windowed job's summary window: [k·width, (k+1)·width) on the
// UTC epoch. The first event past the boundary, a scheduled trigger or a completion,
// closes it.
type windowState struct {
	jobID string
	spec  *windowedSpec
	width time.Duration
	late  atomic.Int64

	mu       sync.Mutex
	sealed   bool
	open     bool
	index    int64
	inflight bool
	tally    windowTally

	// failingSet: each scheduled run adds its keys, and a window's close resets
	// it to its last completed run's keys.
	failingSet    map[string]bool
	failingAtOpen bool
}

// windowSnapshot is a closed window, ready to be written.
type windowSnapshot struct {
	window   *windowState
	start    time.Time
	partial  bool
	inflight bool
	late     int64
	tally    windowTally
	state    string
}

// edgeEvent is a scheduled run that moved the job's failing set: clean to failing
// (new), or a key the set did not hold (grew).
type edgeEvent struct {
	window *windowState
	reason string
	out    *runOutcome
}

func newWindowState(jobID string, spec *windowedSpec, width time.Duration) *windowState {
	w := &windowState{jobID: jobID, spec: spec, width: width, failingSet: make(map[string]bool)}
	w.reset()
	return w
}

func (w *windowState) reset() {
	n := len(w.spec.counters)
	w.tally = windowTally{ok: make([]int64, n), failing: make([]int64, n), keys: make(map[string]bool)}
}

// roll opens the window now falls in, and returns the window it closes, if any.
func (w *windowState) roll(now time.Time) *windowSnapshot {
	index := now.UnixNano() / int64(w.width)
	if w.open && index <= w.index {
		return nil
	}
	var closed *windowSnapshot
	if w.open {
		closed = w.close(false)
	}
	w.open, w.index = true, index
	w.failingAtOpen = len(w.failingSet) > 0
	w.reset()
	return closed
}

// close settles the failing set to the window's last completed run, if any, and
// captures the window; an empty one writes no line.
func (w *windowState) close(partial bool) *windowSnapshot {
	if w.tally.last != nil {
		w.failingSet = maps.Clone(w.tally.last.keys)
	}
	if w.tally.runs == 0 && w.tally.skipped == 0 && !w.inflight {
		return nil
	}
	state := jobStateClean
	if len(w.failingSet) > 0 {
		state = jobStateFailing
	} else if w.failingAtOpen {
		state = jobStateRecovered
	}
	return &windowSnapshot{
		window:   w,
		start:    time.Unix(0, w.index*int64(w.width)).UTC(),
		partial:  partial,
		inflight: w.inflight,
		late:     w.late.Swap(0),
		tally:    w.tally,
		state:    state,
	}
}

// trigger records a scheduled trigger: it rolls the window, then takes the run lock.
func (w *windowState) trigger(now time.Time, tryLock func() bool) (acquired bool, closed *windowSnapshot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sealed {
		return tryLock(), nil
	}
	closed = w.roll(now)
	if !tryLock() {
		w.tally.skipped++
		return false, closed
	}
	w.inflight = true
	return true, closed
}

// complete counts a scheduled run in the window it completes in, and returns the
// edge it emits, if any. A completion that closes the previous window counts there
// as in flight. After the seal it reports sealed instead, and the run writes its
// own action line.
func (w *windowState) complete(now time.Time, out *runOutcome) (closed *windowSnapshot, edge *edgeEvent, sealed bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sealed {
		return nil, nil, true
	}
	closed = w.roll(now)
	w.inflight = false
	t := &w.tally
	t.runs++
	if len(out.keys) > 0 {
		t.failingRuns++
	}
	if out.keys[keyRunSlow] {
		t.slowRuns++
	}
	for i := range out.ok {
		t.ok[i] += out.ok[i]
		t.failing[i] += out.failing[i]
	}
	for key := range out.keys {
		t.keys[key] = true
	}
	t.last = out
	if reason := w.moveFailingSet(out.keys); reason != "" {
		if t.edges < maxEdgesPerWindow {
			t.edges++
			edge = &edgeEvent{window: w, reason: reason, out: out}
		} else {
			t.edgesSuppressed++
		}
	}
	return closed, edge, false
}

// moveFailingSet adds a run's keys to the failing set, and names the edge they
// make: new when the set was empty, grew when it held keys but not all of these.
func (w *windowState) moveFailingSet(keys map[string]bool) string {
	added := false
	for key := range keys {
		if !w.failingSet[key] {
			added = true
		}
	}
	wasFailing := len(w.failingSet) > 0
	maps.Copy(w.failingSet, keys)
	if !added {
		return ""
	}
	if wasFailing {
		return edgeGrew
	}
	return edgeNew
}

// seal closes the open window as partial and ends window accounting for the job.
func (w *windowState) seal(now time.Time) []*windowSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sealed || !w.open {
		w.sealed = true
		return nil
	}
	w.sealed = true
	var out []*windowSnapshot
	if closed := w.roll(now); closed != nil {
		out = append(out, closed)
	}
	if partial := w.close(true); partial != nil {
		out = append(out, partial)
	}
	return out
}

// logWindow writes a closed window's line through the module logger: its keys are
// fixed by the job's declaration, so no runtime string ever becomes a field name.
func (m *Module) logWindow(s *windowSnapshot) {
	if s == nil {
		return
	}
	// The logger is consumer-supplied: a panic here would unwind through window
	// accounting the caller has not finished (the run lock, the run's own settle).
	defer func() { _ = recover() }()
	t := s.tally
	level := logLevelInfo
	if t.failingRuns > 0 || (t.runs == 0 && t.skipped > 0) {
		level = logLevelWarn
	}
	listed, more := listFailingKeys(t.keys)
	inflight := int64(0)
	if s.inflight {
		inflight = 1
	}
	event := createJobLogEvent(m.logger, level).
		Str("log.type", "action").
		Str(jobIDAttr, s.window.jobID).
		Str("window.start", s.start.Format(time.RFC3339Nano)).
		Str("window.end", s.start.Add(s.window.width).Format(time.RFC3339Nano)).
		Bool("window.partial", s.partial).
		Int64("runs", t.runs).
		Int64("runs.failing", t.failingRuns).
		Int64("runs.slow", t.slowRuns).
		Int64("runs.skipped", t.skipped).
		Int64("runs.inflight", inflight).
		Str("job.state", s.state).
		Interface("failing.keys", listed).
		Int("failing.keys.more", more).
		Int64("edges.suppressed", t.edgesSuppressed).
		Int64("recorder.late", s.late)
	event = counterFields(event, s.window.spec, t.ok, t.failing)
	var traceparent, correlationID string
	if t.last != nil {
		event = event.Err(t.last.err)
		traceparent, correlationID = t.last.traceparent, t.last.correlationID
	} else {
		correlationID = gobrickstrace.EnsureTraceID(m.shutdownCtx)
	}
	event.
		Str("traceparent", traceparent).
		Str(logger.FieldCorrelationID, correlationID).
		Msgf("Job '%s' window summary", s.window.jobID)
}

// logEdge writes an edge line through the module logger, carrying the run's counts
// and identity: ERROR when the run errored or panicked, otherwise WARN.
func (m *Module) logEdge(e *edgeEvent) {
	if e == nil {
		return
	}
	defer func() { _ = recover() }()
	level := logLevelWarn
	if e.out.keys[keyRunError] || e.out.keys[keyRunPanic] {
		level = logLevelError
	}
	listed, more := listFailingKeys(e.out.keys)
	event := createJobLogEvent(m.logger, level).
		Str("log.type", "action").
		Str(jobIDAttr, e.window.jobID).
		Str("edge.reason", e.reason).
		Interface("failing.keys", listed).
		Int("failing.keys.more", more)
	counterFields(event, e.window.spec, e.out.ok, e.out.failing).
		Err(e.out.err).
		Str("traceparent", e.out.traceparent).
		Str(logger.FieldCorrelationID, e.out.correlationID).
		Msgf("Job '%s' failing edge: %s", e.window.jobID, e.reason)
}

// listFailingKeys orders keys reserved first, and returns at most
// maxFailingKeysListed of them plus the count of the rest.
func listFailingKeys(set map[string]bool) (listed []string, more int) {
	keys := slices.AppendSeq(make([]string, 0, len(set)), maps.Keys(set))
	slices.SortFunc(keys, compareFailingKeys)
	listed = keys[:min(len(keys), maxFailingKeysListed)]
	return listed, len(keys) - len(listed)
}

// counterFields adds <name>.ok and <name>.failing for every declared Counter.
func counterFields(event logger.LogEvent, spec *windowedSpec, ok, failing []int64) logger.LogEvent {
	for i, c := range spec.counters {
		event = event.Int64(c.name+".ok", ok[i]).Int64(c.name+".failing", failing[i])
	}
	return event
}
