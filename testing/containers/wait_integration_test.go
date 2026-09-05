//go:build integration

package containers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// waitFromOptions applies every customizer a module-based helper's seam returns to an
// empty request and hands back the wait strategy they left behind — proving the strategy
// survives the module wrapper rather than only that one was built. No Docker involved:
// ContainerCustomizer.Customize only mutates the request struct.
func waitFromOptions(t *testing.T, opts []testcontainers.ContainerCustomizer) wait.Strategy {
	t.Helper()
	var req testcontainers.GenericContainerRequest
	for i, opt := range opts {
		require.NoErrorf(t, opt.Customize(&req), "customizer %d (%T)", i, opt)
	}
	return req.WaitingFor
}

// assertBoundWithin pins every level of a helper's wait strategy to want: the composite's
// deadline, its lack of a WithStartupTimeoutDefault, and each leaf's own startup timeout.
//
// testcontainers-go v0.44.0 exposes no getter for MultiStrategy.deadline, so instead of
// reflection on the unexported field the deadline check copies the struct (a plain
// assignment, legal across packages), clears Strategies, and compares it with a reference
// multi carrying the same deadline — require.Equal deep-compares unexported fields. That
// pins both halves at once: deadline equals the configured bound, and timeout stays nil,
// so the rejected WithStartupTimeoutDefault shape fails here. Asserting the multi's
// Timeout() instead would be vacuous — it is nil in the correct shape.
func assertBoundWithin(t *testing.T, strategy wait.Strategy, want time.Duration) {
	t.Helper()

	multi, ok := strategy.(*wait.MultiStrategy)
	require.Truef(t, ok, "wait strategy is %T, not a *wait.MultiStrategy", strategy)

	got := *multi
	got.Strategies = nil
	reference := *wait.ForAll().WithDeadline(want)
	reference.Strategies = nil
	require.Equal(t, reference, got, "composite must carry deadline %s and no default leaf timeout", want)

	require.NotEmpty(t, multi.Strategies, "composite has no leaf strategies")
	for i, s := range multi.Strategies {
		leaf, ok := s.(wait.StrategyTimeout)
		require.Truef(t, ok, "leaf %d (%T) does not expose Timeout()", i, s)
		require.NotNilf(t, leaf.Timeout(), "leaf %d (%T) has no startup timeout, so it self-caps at 60s", i, s)
		require.Equalf(t, want, *leaf.Timeout(), "leaf %d (%T)", i, s)
	}
}

// TestContainerWaitStrategiesBindConfiguredStartupTimeout asserts that each helper's
// configured StartupTimeout is the effective bound at every nesting level. Each helper is
// checked twice: at its shipped default, and at an override no level could satisfy by
// coincidence — three of the four defaults are 60s, which is exactly both testcontainers'
// defaultStartupTimeout and the deadline WithWaitStrategy hardcodes.
func TestContainerWaitStrategiesBindConfiguredStartupTimeout(t *testing.T) {
	const overrideTimeout = 90 * time.Second

	tests := []struct {
		name string
		want time.Duration
		// build returns the helper's wait strategy with StartupTimeout forced to timeout.
		build func(t *testing.T, timeout time.Duration) wait.Strategy
	}{
		{
			name: "oracle",
			want: DefaultOracleConfig().StartupTimeout,
			build: func(_ *testing.T, timeout time.Duration) wait.Strategy {
				cfg := DefaultOracleConfig()
				cfg.StartupTimeout = timeout
				return oracleContainerRequest(cfg).WaitingFor
			},
		},
		{
			name: "postgresql",
			want: DefaultPostgreSQLConfig().StartupTimeout,
			build: func(t *testing.T, timeout time.Duration) wait.Strategy {
				cfg := DefaultPostgreSQLConfig()
				cfg.StartupTimeout = timeout
				return waitFromOptions(t, postgreSQLOptions(cfg))
			},
		},
		{
			name: "redis",
			want: DefaultRedisConfig().StartupTimeout,
			build: func(t *testing.T, timeout time.Duration) wait.Strategy {
				cfg := DefaultRedisConfig()
				cfg.StartupTimeout = timeout
				return waitFromOptions(t, redisOptions(cfg))
			},
		},
		{
			name: "rabbitmq",
			want: DefaultRabbitMQConfig().StartupTimeout,
			build: func(t *testing.T, timeout time.Duration) wait.Strategy {
				cfg := DefaultRabbitMQConfig()
				cfg.StartupTimeout = timeout
				return waitFromOptions(t, rabbitMQOptions(cfg))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertBoundWithin(t, tt.build(t, tt.want), tt.want)
		})
		t.Run(tt.name+"_overridden_timeout", func(t *testing.T) {
			assertBoundWithin(t, tt.build(t, overrideTimeout), overrideTimeout)
		})
	}
}
