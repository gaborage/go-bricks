package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

	obtest "github.com/gaborage/go-bricks/observability/testing"
)

func noopRun(JobContext, *Recorder) error { return nil }

func valuesN(n int) []string {
	values := make([]string, n)
	for i := range values {
		values[i] = fmt.Sprintf("v%d", i)
	}
	return values
}

func countersN(n int) []Counter {
	counters := make([]Counter, n)
	for i := range counters {
		counters[i] = Counter{Name: fmt.Sprintf("c%02d", i)}
	}
	return counters
}

type windowedValidationCase struct {
	name      string
	run       func(JobContext, *Recorder) error
	counters  []Counter
	wantField string
}

// reservedWordCases refuses each reserved word as a Counter name, and a name under it.
func reservedWordCases() []windowedValidationCase {
	words := []string{"job", "run", "runs", "window", "failing", "edges", "log", "recorder", "undeclared"}
	cases := make([]windowedValidationCase, 0, 2*len(words))
	for _, word := range words {
		cases = append(cases,
			windowedValidationCase{name: "reserved_" + word, run: noopRun, counters: []Counter{{Name: word}}, wantField: "counters[0].Name"},
			windowedValidationCase{name: "under_reserved_" + word, run: noopRun, counters: []Counter{{Name: word + ".x"}}, wantField: "counters[0].Name"},
		)
	}
	return cases
}

func TestWindowedRegistrationValidatesEveryRule(t *testing.T) {
	lane := func(values ...string) []AttrSet { return []AttrSet{{Key: "lane", Values: values}} }
	tests := []windowedValidationCase{
		{name: "nil_run", counters: nil, wantField: "run"},
		{name: "thirty_three_counters", run: noopRun, counters: countersN(33), wantField: "counters"},
		{name: "uppercase_name", run: noopRun, counters: []Counter{{Name: "Published"}}, wantField: "counters[0].Name"},
		{name: "empty_name", run: noopRun, counters: []Counter{{Name: ""}}, wantField: "counters[0].Name"},
		{name: "sixty_four_char_name", run: noopRun, counters: []Counter{{Name: "a" + strings.Repeat("b", 63)}}, wantField: "counters[0].Name"},
		{name: "duplicate_name", run: noopRun, counters: []Counter{{Name: "published"}, {Name: "published"}}, wantField: "counters[1].Name"},
		{name: "filter_needle", run: noopRun, counters: []Counter{{Name: "x.token.count"}}, wantField: "counters[0].Name"},
		{name: "invalid_attr_key", run: noopRun, counters: []Counter{{Name: "published", Attrs: []AttrSet{{Key: "Lane", Values: []string{"a"}}}}}, wantField: "counters[0].Attrs[0].Key"},
		{name: "thirty_three_char_attr_key", run: noopRun, counters: []Counter{{Name: "published", Attrs: []AttrSet{{Key: "k" + strings.Repeat("e", 32), Values: []string{"a"}}}}}, wantField: "counters[0].Attrs[0].Key"},
		{name: "sixty_five_char_value", run: noopRun, counters: []Counter{{Name: "published", Attrs: lane(strings.Repeat("v", 65))}}, wantField: "counters[0].Attrs[0].Values"},
		{name: "empty_value", run: noopRun, counters: []Counter{{Name: "published", Attrs: lane("")}}, wantField: "counters[0].Attrs[0].Values"},
		{name: "duplicate_attr_key", run: noopRun, counters: []Counter{{Name: "published", Attrs: append(lane("a"), lane("b")...)}}, wantField: "counters[0].Attrs[1].Key"},
		{name: "no_values", run: noopRun, counters: []Counter{{Name: "published", Attrs: lane()}}, wantField: "counters[0].Attrs[0].Values"},
		{name: "invalid_value", run: noopRun, counters: []Counter{{Name: "published", Attrs: lane("Main")}}, wantField: "counters[0].Attrs[0].Values"},
		{name: "duplicate_value", run: noopRun, counters: []Counter{{Name: "published", Attrs: lane("a", "a")}}, wantField: "counters[0].Attrs[0].Values"},
		{name: "over_two_thousand_series", run: noopRun, counters: []Counter{{Name: "published", Attrs: []AttrSet{
			{Key: "lane", Values: valuesN(50)}, {Key: "kind", Values: valuesN(41)},
		}}}, wantField: "counters[0].Attrs"},
	}

	for _, tt := range append(tests, reservedWordCases()...) {
		t.Run(tt.name, func(t *testing.T) {
			module := newWindowedTestScheduler(t, newFakeClock(windowTestStart))

			err := module.FixedRate("relay", Windowed(tt.run, tt.counters...), windowTestInterval)

			var validation *ValidationError
			require.ErrorAs(t, err, &validation)
			assert.Equal(t, tt.wantField, validation.Field)
		})
	}
}

func TestWindowedRegistrationAcceptsEachLimit(t *testing.T) {
	tests := []struct {
		name     string
		counters []Counter
	}{
		{name: "thirty_two_counters", counters: countersN(maxWindowedCounters)},
		{name: "sixty_three_char_name", counters: []Counter{{Name: "a" + strings.Repeat("b", 62)}}},
		{name: "thirty_two_char_attr_key_and_sixty_four_char_value", counters: []Counter{{Name: "published", Attrs: []AttrSet{
			{Key: "k" + strings.Repeat("e", 31), Values: []string{strings.Repeat("v", 64)}},
		}}}},
		{name: "name_merely_starting_with_a_reserved_word", counters: []Counter{{Name: "runsheet"}}},
		{name: "two_thousand_series", counters: []Counter{{Name: "published", Attrs: []AttrSet{
			{Key: "lane", Values: valuesN(50)}, {Key: "kind", Values: valuesN(40)},
		}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			module := newWindowedTestScheduler(t, newFakeClock(windowTestStart))

			require.NoError(t, module.FixedRate("relay", Windowed(noopRun, tt.counters...), windowTestInterval))
		})
	}
}

func TestWindowedJobRegistersOnlyWithFixedRate(t *testing.T) {
	job := Windowed(noopRun)
	at := ParseTime("03:00")
	doors := map[string]func(*Module) error{
		"daily":   func(m *Module) error { return m.DailyAt("relay", job, at) },
		"weekly":  func(m *Module) error { return m.WeeklyAt("relay", job, time.Monday, at) },
		"hourly":  func(m *Module) error { return m.HourlyAt("relay", job, 5) },
		"monthly": func(m *Module) error { return m.MonthlyAt("relay", job, 1, at) },
	}

	for name, register := range doors {
		t.Run(name, func(t *testing.T) {
			module := newWindowedTestScheduler(t, newFakeClock(windowTestStart))

			var validation *ValidationError
			require.ErrorAs(t, register(module), &validation)
			assert.Equal(t, "job", validation.Field)
		})
	}
}

func TestWindowedRegistrationRefusesAnUnnormalizedZeroWindow(t *testing.T) {
	module, _ := newTestScheduler(t, 5*time.Second)

	err := module.FixedRate("relay", Windowed(noopRun), windowTestInterval)

	var validation *ValidationError
	require.ErrorAs(t, err, &validation)
	assert.Equal(t, "scheduler.summary.window", validation.Field)
}

func TestWindowedIntervalNotShorterThanTheWindowWarnsAndRegisters(t *testing.T) {
	tests := []struct {
		name         string
		interval     time.Duration
		wantWarnings int
	}{
		{name: "interval_equal_to_the_window", interval: windowTestWidth, wantWarnings: 1},
		{name: "interval_shorter_than_the_window", interval: windowTestWidth - time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := newRecordingLogger()
			module := newWindowedTestScheduler(t, newFakeClock(windowTestStart), withLogger(log))

			require.NoError(t, module.FixedRate("relay", Windowed(noopRun), tt.interval))

			warnings := log.messageLines("Windowed job interval is not shorter than scheduler.summary.window")
			assert.Len(t, warnings, tt.wantWarnings)
			registeredEntry(t, module, "relay")
		})
	}
}

func TestWindowedExecuteRefusesToRunOutsideTheScheduler(t *testing.T) {
	called := false
	job := Windowed(func(JobContext, *Recorder) error {
		called = true
		return nil
	})

	err := job.Execute(newJobContext(context.Background(), "relay", triggerScheduled, nil, nil, nil, nil))

	require.ErrorIs(t, err, errWindowedExecute)
	assert.False(t, called)
}

var (
	laneA = attribute.String("lane", "a")
	kindX = attribute.String("kind", "x")
)

func TestUndeclaredRecorderCallFailsTheRunUnderUndeclared(t *testing.T) {
	tests := []struct {
		name   string
		record func(*Recorder)
	}{
		{name: "unknown_name", record: func(r *Recorder) { r.Count("unknown", 1, laneA, kindX) }},
		{name: "missing_attribute", record: func(r *Recorder) { r.Count("published", 1, laneA) }},
		{name: "extra_attribute", record: func(r *Recorder) {
			r.Count("published", 1, laneA, kindX, attribute.String("zone", "a"))
		}},
		{name: "foreign_attribute_key", record: func(r *Recorder) { r.Count("published", 1, laneA, attribute.String("zone", "x")) }},
		{name: "repeated_attribute_key", record: func(r *Recorder) {
			r.Count("published", 1, laneA, attribute.String("lane", "b"))
		}},
		{name: "undeclared_value", record: func(r *Recorder) { r.Count("published", 1, attribute.String("lane", "z"), kindX) }},
		{name: "non_string_value", record: func(r *Recorder) { r.Count("published", 1, attribute.Int("lane", 1), kindX) }},
		{name: "negative_count", record: func(r *Recorder) { r.Count("published", -1, laneA, kindX) }},
		{name: "negative_fail", record: func(r *Recorder) { r.Fail("published", -1, laneA, kindX) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := newRecordingLogger()
			module := newWindowedTestScheduler(t, newFakeClock(windowTestStart), withLogger(log))
			job := Windowed(func(_ JobContext, rec *Recorder) error {
				tt.record(rec)
				return nil
			}, Counter{Name: "published", Attrs: []AttrSet{{Key: "lane", Values: []string{"a", "b"}}, {Key: "kind", Values: []string{"x", "y"}}}})
			require.NoError(t, module.FixedRate("relay", job, windowTestInterval))

			require.NotPanics(t, func() { module.runJobBody(registeredEntry(t, module, "relay"), triggerScheduled) })
			require.NoError(t, module.Shutdown())

			windows := log.windowLines()
			require.Len(t, windows, 1)
			assert.Equal(t, int64(1), windows[0].fields["runs.failing"])
			assert.Equal(t, []string{keyUndeclared}, windows[0].fields["failing.keys"])
			assert.Equal(t, int64(0), windows[0].fields["published.ok"], "an undeclared call adds nothing")
			assert.Equal(t, int64(0), windows[0].fields["published.failing"])
		})
	}
}

func TestDeclaredRecorderCallsSumPerCounter(t *testing.T) {
	log := newRecordingLogger()
	module := newWindowedTestScheduler(t, newFakeClock(windowTestStart), withLogger(log))
	lane := []AttrSet{{Key: "lane", Values: []string{"a", "b"}}}
	job := Windowed(func(_ JobContext, rec *Recorder) error {
		rec.Count("published", 2, attribute.String("lane", "a"))
		rec.Count("published", 3, attribute.String("lane", "b"))
		rec.Fail("published", 0, attribute.String("lane", "a"))
		rec.Fail("parked", 4)
		return nil
	}, Counter{Name: "published", Attrs: lane}, Counter{Name: "parked"})
	require.NoError(t, module.FixedRate("relay", job, windowTestInterval))

	module.runJobBody(registeredEntry(t, module, "relay"), triggerScheduled)
	module.runJobBody(registeredEntry(t, module, "relay"), triggerScheduled)
	require.NoError(t, module.Shutdown())

	windows := log.windowLines()
	require.Len(t, windows, 1)
	line := windows[0]
	assert.Equal(t, "warn", line.level)
	assert.Equal(t, int64(10), line.fields["published.ok"])
	assert.Equal(t, int64(0), line.fields["published.failing"])
	assert.Equal(t, int64(0), line.fields["parked.ok"])
	assert.Equal(t, int64(8), line.fields["parked.failing"])
	assert.Equal(t, []string{"parked"}, line.fields["failing.keys"], "a Fail of zero adds no key")
	assert.Equal(t, int64(2), line.fields["runs.failing"])
}

func TestCleanWindowWritesInfo(t *testing.T) {
	log := newRecordingLogger()
	module := newWindowedTestScheduler(t, newFakeClock(windowTestStart), withLogger(log))
	require.NoError(t, module.FixedRate("relay", Windowed(func(JobContext, *Recorder) error {
		return errors.New("relay failed")
	}), windowTestInterval))
	require.NoError(t, module.FixedRate("clean", Windowed(noopRun), windowTestInterval))

	module.runJobBody(registeredEntry(t, module, "relay"), triggerScheduled)
	module.runJobBody(registeredEntry(t, module, "clean"), triggerScheduled)
	require.NoError(t, module.Shutdown())

	levels := map[any]string{}
	for _, line := range log.windowLines() {
		levels[line.fields["job.id"]] = line.level
		if line.fields["job.id"] == "relay" {
			assert.Equal(t, []string{keyRunError}, line.fields["failing.keys"])
			assert.Equal(t, "relay failed", line.fields["error"])
		}
	}
	assert.Equal(t, map[any]string{"relay": "warn", "clean": "info"}, levels)
}

func TestWindowedCounterMetricsSumCountAndFail(t *testing.T) {
	mp := obtest.NewTestMeterProvider()
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.Background())) })
	module := newWindowedTestScheduler(t, newFakeClock(windowTestStart), withMeterProvider(mp.MeterProvider))
	var kept *Recorder
	job := Windowed(func(_ JobContext, rec *Recorder) error {
		kept = rec
		rec.Count("published", 2, laneA, kindX)
		rec.Fail("published", 3, laneA, kindX)
		rec.Count("published", 5, attribute.String("lane", "b"), kindX)
		rec.Count("published", 7, attribute.String("lane", "z"), kindX)
		rec.Fail("parked", 4)
		return nil
	}, Counter{Name: "published", Attrs: []AttrSet{{Key: "lane", Values: []string{"a", "b"}}, {Key: "kind", Values: []string{"x", "y"}}}},
		Counter{Name: "parked"})
	require.NoError(t, module.FixedRate("relay", job, windowTestInterval))

	module.runJobBody(registeredEntry(t, module, "relay"), triggerScheduled)
	kept.Count("parked", 9)

	assert.Equal(t, map[string]int64{
		"job.id=relay,kind=x,lane=a": 5,
		"job.id=relay,kind=x,lane=b": 5,
	}, counterPoints(t, mp, "published"), "Count and Fail both add; an undeclared value adds nothing")
	assert.Equal(t, map[string]int64{"job.id=relay": 4}, counterPoints(t, mp, "parked"), "a late call adds nothing")
}
