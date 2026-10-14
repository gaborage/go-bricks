package scheduler

import (
	"go.opentelemetry.io/otel/attribute"
)

// Counter declares one domain outcome a Windowed job reports.
type Counter struct {
	Name  string
	Attrs []AttrSet
}

// AttrSet declares one attribute key a Counter's calls carry, and every value it may take.
type AttrSet struct {
	Key    string
	Values []string
}

// Recorder tallies one run's Counter outcomes.
type Recorder struct{}

// Count adds n successful outcomes to the named Counter.
func (r *Recorder) Count(_ string, _ int64, _ ...attribute.KeyValue) {}

// Fail adds n failing outcomes to the named Counter.
func (r *Recorder) Fail(_ string, _ int64, _ ...attribute.KeyValue) {}

// Windowed wraps run as a FixedRate job.
func Windowed(run func(JobContext, *Recorder) error, _ ...Counter) Executor {
	return windowedStub(func(ctx JobContext) error { return run(ctx, &Recorder{}) })
}

type windowedStub func(JobContext) error

func (f windowedStub) Execute(ctx JobContext) error { return f(ctx) }
