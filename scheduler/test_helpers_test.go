package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// assertWaitGroupDrains fails if module m's in-flight WaitGroup does not reach
// zero within 100ms after the code path under test returned. A hang means a
// wg.Add(1) was not matched by a deferred wg.Done() on that path — surfaced as a
// failure, not a hung test.
func assertWaitGroupDrains(t *testing.T, m *Module, afterContext string) {
	t.Helper()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("wg.Wait() blocked after %s — Add(1) was not balanced by Done()", afterContext)
	}
}

type testSchedulerOption func(*app.ModuleDeps)

func withTracer(tr trace.Tracer) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.Tracer = tr }
}

func withMeterProvider(mp metric.MeterProvider) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.MeterProvider = mp }
}

func withDB(fn func(context.Context) (types.Interface, error)) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.DB = fn }
}

func withMessaging(fn func(context.Context) (messaging.AMQPClient, error)) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.Messaging = fn }
}

func withSlowJobThreshold(threshold time.Duration) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.Config.Scheduler.Timeout.SlowJob = threshold }
}

func withTimezone(tz string) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.Config.Scheduler.Timezone = tz }
}

// newTestScheduler creates and initializes a scheduler module for testing.
func newTestScheduler(t *testing.T, shutdownTimeout time.Duration, opts ...testSchedulerOption) (*Module, app.JobRegistrar) {
	module := NewModule()

	appDeps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Scheduler: config.SchedulerConfig{
				Timeout: config.SchedulerTimeoutConfig{
					Shutdown: shutdownTimeout,
				},
			},
		},
		Tracer:        nil,
		MeterProvider: nil,
		DB: func(_ context.Context) (types.Interface, error) {
			return nil, nil
		},
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	for _, o := range opts {
		o(appDeps)
	}

	require.NoError(t, module.Init(appDeps), "Module initialization should succeed")
	t.Cleanup(func() {
		if err := module.Shutdown(); err != nil {
			t.Errorf("module shutdown failed during cleanup: %v", err)
		}
	})

	return module, module
}
