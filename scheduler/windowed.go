package scheduler

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/gaborage/go-bricks/logger"
)

// Counter declares one domain outcome a Windowed job reports. Each declared Counter
// writes <Name>.ok and <Name>.failing on every window line.
type Counter struct {
	Name  string
	Attrs []AttrSet
}

// AttrSet declares one attribute key a Counter's calls carry, and every value it may take.
type AttrSet struct {
	Key    string
	Values []string
}

const (
	maxWindowedCounters = 32
	maxCounterSeries    = 2000

	keyRunError   = "run.error"
	keyRunSlow    = "run.slow"
	keyRunPanic   = "run.panic"
	keyUndeclared = "undeclared"
)

// reservedFailingKeys lead failing.keys in this order, so the scheduler's own
// failure reasons never fall into failing.keys.more.
var reservedFailingKeys = []string{keyRunError, keyRunPanic, keyRunSlow, keyUndeclared}

func failingKeyRank(key string) int {
	if i := slices.Index(reservedFailingKeys, key); i >= 0 {
		return i
	}
	return len(reservedFailingKeys)
}

func compareFailingKeys(a, b string) int {
	if ra, rb := failingKeyRank(a), failingKeyRank(b); ra != rb {
		return ra - rb
	}
	return strings.Compare(a, b)
}

var (
	counterNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,62}$`)
	attrKeyPattern     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	attrValuePattern   = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

	// reservedCounterWords keep a Counter's fields clear of the window line's own keys.
	reservedCounterWords = []string{"job", "run", "runs", "window", "failing", "edges", "log", "recorder", "undeclared"}

	errWindowedExecute = errors.New("scheduler: a Windowed job runs only when registered with FixedRate")
)

// Windowed wraps run as a FixedRate job whose runs the scheduler reports once per
// scheduler.summary.window instead of once per run. run reports its outcomes through
// the Recorder, against the declared counters. Registering it with any other schedule
// fails, and calling its Execute directly returns an error without calling run.
func Windowed(run func(JobContext, *Recorder) error, counters ...Counter) Executor {
	return &windowedJob{run: run, counters: counters}
}

type windowedJob struct {
	run      func(JobContext, *Recorder) error
	counters []Counter
}

func (j *windowedJob) Execute(JobContext) error { return errWindowedExecute }

func (j *windowedJob) windowed() *windowedJob { return j }

// windowedExecutor is how the registration doors recognize a Windowed job.
type windowedExecutor interface {
	windowed() *windowedJob
}

type counterSpec struct {
	name       string
	attrs      map[string]map[string]bool
	instrument metric.Int64Counter
}

// windowedSpec is a validated Windowed job.
type windowedSpec struct {
	run      func(JobContext, *Recorder) error
	counters []counterSpec
	index    map[string]int
	jobAttr  attribute.KeyValue
}

func counterField(i int, rest string) string { return fmt.Sprintf("counters[%d]%s", i, rest) }

func compileWindowed(j *windowedJob) (*windowedSpec, error) {
	if j.run == nil {
		return nil, &ValidationError{Field: "run", Message: "must not be nil"}
	}
	if len(j.counters) > maxWindowedCounters {
		return nil, &ValidationError{Field: "counters", Message: fmt.Sprintf("must hold at most %d Counters (got %d)", maxWindowedCounters, len(j.counters))}
	}
	spec := &windowedSpec{run: j.run, index: make(map[string]int, len(j.counters))}
	needles := logger.DefaultFilterConfig().SensitiveFields
	for i, c := range j.counters {
		if err := validateCounterName(i, c.Name, needles); err != nil {
			return nil, err
		}
		if _, dup := spec.index[c.Name]; dup {
			return nil, &ValidationError{Field: counterField(i, ".Name"), Message: fmt.Sprintf("%q is declared twice", c.Name)}
		}
		attrs, err := compileAttrSets(i, c.Attrs)
		if err != nil {
			return nil, err
		}
		spec.index[c.Name] = i
		spec.counters = append(spec.counters, counterSpec{name: c.Name, attrs: attrs})
	}
	return spec, nil
}

func validateCounterName(i int, name string, needles []string) error {
	field := counterField(i, ".Name")
	if !counterNamePattern.MatchString(name) {
		return &ValidationError{Field: field, Message: fmt.Sprintf("%q must match %s", name, counterNamePattern)}
	}
	for _, word := range reservedCounterWords {
		if name == word || strings.HasPrefix(name, word+".") {
			return &ValidationError{Field: field, Message: fmt.Sprintf("%q uses the reserved word %q", name, word)}
		}
	}
	// The log filter masks a field whose name holds a needle, so such a Counter would
	// silently mask its own .ok and .failing fields.
	for _, needle := range needles {
		if strings.Contains(name, strings.ToLower(needle)) {
			return &ValidationError{Field: field, Message: fmt.Sprintf("%q holds the log filter needle %q", name, needle)}
		}
	}
	return nil
}

func compileAttrSets(i int, sets []AttrSet) (map[string]map[string]bool, error) {
	attrs := make(map[string]map[string]bool, len(sets))
	series := 1
	for j, set := range sets {
		keyField := counterField(i, fmt.Sprintf(".Attrs[%d].Key", j))
		if !attrKeyPattern.MatchString(set.Key) {
			return nil, &ValidationError{Field: keyField, Message: fmt.Sprintf("%q must match %s", set.Key, attrKeyPattern)}
		}
		if _, dup := attrs[set.Key]; dup {
			return nil, &ValidationError{Field: keyField, Message: fmt.Sprintf("%q is declared twice", set.Key)}
		}
		values, err := compileAttrValues(counterField(i, fmt.Sprintf(".Attrs[%d].Values", j)), set.Values)
		if err != nil {
			return nil, err
		}
		series *= len(set.Values)
		if series > maxCounterSeries {
			return nil, &ValidationError{Field: counterField(i, ".Attrs"), Message: fmt.Sprintf("must declare at most %d series", maxCounterSeries)}
		}
		attrs[set.Key] = values
	}
	return attrs, nil
}

func compileAttrValues(field string, declared []string) (map[string]bool, error) {
	if len(declared) == 0 {
		return nil, &ValidationError{Field: field, Message: "must not be empty"}
	}
	values := make(map[string]bool, len(declared))
	for _, v := range declared {
		if !attrValuePattern.MatchString(v) {
			return nil, &ValidationError{Field: field, Message: fmt.Sprintf("%q must match %s", v, attrValuePattern)}
		}
		if values[v] {
			return nil, &ValidationError{Field: field, Message: fmt.Sprintf("%q is declared twice", v)}
		}
		values[v] = true
	}
	return values, nil
}

// declares reports whether attrs name every declared key once, each with a declared value.
func (c *counterSpec) declares(attrs []attribute.KeyValue) bool {
	if len(attrs) != len(c.attrs) {
		return false
	}
	seen := make(map[attribute.Key]bool, len(attrs))
	for _, kv := range attrs {
		values, ok := c.attrs[string(kv.Key)]
		// A non-string value reads as "", which no declared value can be.
		if !ok || seen[kv.Key] || !values[kv.Value.AsString()] {
			return false
		}
		seen[kv.Key] = true
	}
	return true
}

// add records n on the Counter's metric, with its declared attributes plus job.id.
func (c *counterSpec) add(ctx context.Context, n int64, attrs []attribute.KeyValue, job attribute.KeyValue) {
	if c.instrument == nil {
		return
	}
	kvs := make([]attribute.KeyValue, 0, len(attrs)+1)
	kvs = append(append(kvs, attrs...), job)
	c.instrument.Add(ctx, n, metric.WithAttributes(kvs...))
}

// failingKey renders name{k=v,...} with the attributes sorted by key.
func failingKey(name string, attrs []attribute.KeyValue) string {
	if len(attrs) == 0 {
		return name
	}
	pairs := make([]string, 0, len(attrs))
	for _, kv := range attrs {
		pairs = append(pairs, string(kv.Key)+"="+kv.Value.AsString())
	}
	slices.Sort(pairs)
	return name + "{" + strings.Join(pairs, ",") + "}"
}

// Recorder tallies one run's Counter outcomes. The scheduler seals it when the run
// returns or panics; a later call is dropped and counted in recorder.late.
type Recorder struct {
	ctx  context.Context // NOSONAR: the run's traced context, held for the run's lifetime so metric exemplars name the job span
	spec *windowedSpec
	late *atomic.Int64

	mu      sync.Mutex
	sealed  bool
	ok      []int64
	failing []int64
	keys    map[string]bool
}

func newRecorder(ctx context.Context, spec *windowedSpec, late *atomic.Int64) *Recorder {
	return &Recorder{
		ctx:     ctx,
		spec:    spec,
		late:    late,
		ok:      make([]int64, len(spec.counters)),
		failing: make([]int64, len(spec.counters)),
		keys:    make(map[string]bool),
	}
}

// Count adds n successful outcomes to the named Counter and its metric.
func (r *Recorder) Count(name string, n int64, attrs ...attribute.KeyValue) {
	r.record(name, n, attrs, false)
}

// Fail adds n failing outcomes to the named Counter and its metric; n > 0 makes the
// run failing.
func (r *Recorder) Fail(name string, n int64, attrs ...attribute.KeyValue) {
	r.record(name, n, attrs, true)
}

func (r *Recorder) record(name string, n int64, attrs []attribute.KeyValue, failing bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		if r.late != nil {
			r.late.Add(1)
		}
		return
	}
	i, ok := r.spec.index[name]
	if !ok || n < 0 || !r.spec.counters[i].declares(attrs) {
		r.keys[keyUndeclared] = true
		return
	}
	r.spec.counters[i].add(r.ctx, n, attrs, r.spec.jobAttr)
	if !failing {
		r.ok[i] += n
		return
	}
	r.failing[i] += n
	if n > 0 {
		r.keys[failingKey(name, attrs)] = true
	}
}

func (r *Recorder) seal() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sealed = true
}

// outcome returns the sealed run's tally; a sealed Recorder never writes it again.
func (r *Recorder) outcome() *runOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	return &runOutcome{ok: r.ok, failing: r.failing, keys: r.keys}
}
