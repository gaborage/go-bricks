package testing_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	oteltrace "go.opentelemetry.io/otel/trace"

	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// recordUnderASampledSpan records one counter and one histogram sample inside a
// sampled span, which is what makes the SDK attach an exemplar at all.
func recordUnderASampledSpan(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()

	tp := obtest.NewTestTraceProvider()
	mp := obtest.NewTestMeterProvider()
	t.Cleanup(func() {
		require.NoError(t, tp.Shutdown(context.Background()))
		require.NoError(t, mp.Shutdown(context.Background()))
	})

	ctx, span := tp.Tracer("exemplars").Start(context.Background(), "work")
	require.True(t, span.SpanContext().IsSampled(), "an unsampled span attaches no exemplar")

	meter := mp.Meter("exemplars")
	counter, err := meter.Int64Counter("things.counted")
	require.NoError(t, err)
	counter.Add(ctx, 1, metric.WithAttributes())

	histogram, err := meter.Float64Histogram("things.timed")
	require.NoError(t, err)
	histogram.Record(ctx, 0.5, metric.WithAttributes())

	span.End()
	return mp.Collect(t)
}

func TestSumExemplarsNamesTheRecordingSpan(t *testing.T) {
	rm := recordUnderASampledSpan(t)

	exemplars := obtest.SumExemplars[int64](t, rm, "things.counted")

	require.NotEmpty(t, exemplars)
	assert.NotEmpty(t, exemplars[0].TraceID, "the exemplar carries the trace it was recorded under")
	assert.Len(t, exemplars[0].TraceID, len(oteltrace.TraceID{}))
}

func TestHistogramExemplarsNamesTheRecordingSpan(t *testing.T) {
	rm := recordUnderASampledSpan(t)

	exemplars := obtest.HistogramExemplars[float64](t, rm, "things.timed")

	require.NotEmpty(t, exemplars)
	assert.NotEmpty(t, exemplars[0].TraceID)
}

// Both helpers fail loudly rather than returning empty: an assertion helper that
// yields nothing turns "the exemplar is missing" into "I guessed the shape
// wrong", and both read as a passing zero-length result at the call site.
func TestExemplarHelpersFailRatherThanYieldNothing(t *testing.T) {
	rm := recordUnderASampledSpan(t)

	tests := []struct {
		name string
		call func(t obtest.TB)
	}{
		{
			name: "sum_metric_absent",
			call: func(t obtest.TB) { obtest.SumExemplars[int64](t, rm, "no.such.metric") },
		},
		{
			name: "histogram_metric_absent",
			call: func(t obtest.TB) { obtest.HistogramExemplars[float64](t, rm, "no.such.metric") },
		},
		{
			name: "sum_asked_for_the_wrong_numeric_type",
			call: func(t obtest.TB) { obtest.SumExemplars[float64](t, rm, "things.counted") },
		},
		{
			name: "histogram_asked_for_a_sum",
			call: func(t obtest.TB) { obtest.HistogramExemplars[float64](t, rm, "things.counted") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &failRecorder{}

			recorder.run(func() { tt.call(recorder) })

			assert.True(t, recorder.failed, "the helper must report rather than return empty")
		})
	}
}

// failRecorder captures a require failure instead of ending the test.
type failRecorder struct{ failed bool }

type stop struct{}

func (r *failRecorder) Errorf(string, ...any) { r.failed = true }
func (r *failRecorder) FailNow()              { panic(stop{}) }
func (r *failRecorder) Helper()               {}

func (r *failRecorder) run(fn func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if _, stopped := recovered.(stop); !stopped {
				panic(recovered)
			}
		}
	}()
	fn()
}
