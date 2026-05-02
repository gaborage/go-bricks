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
	t.Cleanup(func() { _ = module.Shutdown() })

	return module, module
}
