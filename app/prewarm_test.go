package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

// fakeBrokerURLProvider is a minimal messaging.BrokerURLProvider for tests
// that need a real *messaging.Manager without a real broker.
type fakeBrokerURLProvider struct{ url string }

func (f *fakeBrokerURLProvider) BrokerURL(context.Context, string) (string, error) {
	return f.url, nil
}

// newPrewarmMockClient returns a MockAMQPClient in the not-ready state with a
// Close expectation, ready for readiness-wait tests (NewMockAMQPClient defaults
// to ready; Manager.Close() closes cached publisher clients).
func newPrewarmMockClient() *testmocks.MockAMQPClient {
	client := testmocks.NewMockAMQPClient()
	client.SetReady(false)
	client.ExpectClose(nil)
	return client
}

// newPrewarmTestManager wires a mock-backed *messaging.Manager for pre-warm tests.
func newPrewarmTestManager(log logger.Logger, client *testmocks.MockAMQPClient) *messaging.Manager {
	factory := func(string, logger.Logger) messaging.AMQPClient { return client }
	return messaging.NewMessagingManager(&fakeBrokerURLProvider{url: "amqp://localhost"}, log,
		messaging.ManagerOptions{MaxPublishers: 5, IdleTTL: time.Hour}, factory)
}

// newPreWarmApp builds the minimal App the pre-warm pass reads: a logger, the
// messaging manager under test, and the config its readiness budget comes from.
func newPreWarmApp(log logger.Logger, manager *messaging.Manager, readyTimeout time.Duration) *App {
	return &App{
		logger:           log,
		messagingManager: manager,
		cfg: &config.Config{
			Messaging: config.MessagingConfig{
				Reconnect: config.ReconnectConfig{ReadyTimeout: readyTimeout},
			},
		},
	}
}

// TestPreWarmSingleTenantSkipsAbsentManagers pins the absence guard: with neither
// manager built, pre-warming is a silent no-op and never reports a problem.
func TestPreWarmSingleTenantSkipsAbsentManagers(t *testing.T) {
	a := &App{logger: logger.New("debug", true), cfg: &config.Config{}}

	require.NoError(t, a.preWarmSingleTenant(context.Background(), messaging.NewDeclarations()))
	require.NoError(t, a.preWarmSingleTenant(context.Background(), nil))
}

func TestAppAwaitPublisherReady(t *testing.T) {
	log := logger.New("debug", true)
	a := newPreWarmApp(log, nil, 0)

	t.Run("already_ready_returns_immediately", func(t *testing.T) {
		client := testmocks.NewMockAMQPClient() // defaults to ready
		assert.Equal(t, preWarmReady, a.awaitPublisherReady(context.Background(), client))
	})

	t.Run("becomes_ready_during_poll", func(t *testing.T) {
		client := newPrewarmMockClient()
		go func() {
			time.Sleep(150 * time.Millisecond)
			client.SetReady(true)
		}()
		assert.Equal(t, preWarmReady, a.awaitPublisherReady(context.Background(), client))
	})

	t.Run("ctx_cancellation_reported_distinctly", func(t *testing.T) {
		client := newPrewarmMockClient()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		start := time.Now()
		outcome := a.awaitPublisherReady(ctx, client)
		elapsed := time.Since(start)

		assert.Equal(t, preWarmCanceled, outcome)
		assert.Less(t, elapsed, time.Second, "must return once ctx expires, not wait out the readiness budget")
	})

	t.Run("configured_budget_elapses_without_readiness", func(t *testing.T) {
		short := newPreWarmApp(log, nil, 150*time.Millisecond)
		client := newPrewarmMockClient()

		start := time.Now()
		outcome := short.awaitPublisherReady(context.Background(), client)
		elapsed := time.Since(start)

		assert.Equal(t, preWarmNotReadyInTime, outcome)
		assert.Less(t, elapsed, time.Second, "must honor the configured budget, not the 5s fallback")
	})
}

// TestAppPublisherReadinessTimeout pins where the pre-warm budget comes from. This is
// the operator-key pin that used to sit on Builder.ConfigureRuntimeHelpers: the value
// is messaging.reconnect.readytimeout, read straight off the App's config.
func TestAppPublisherReadinessTimeout(t *testing.T) {
	tests := []struct {
		cfg  *config.Config
		name string
		want time.Duration
	}{
		{name: "nil_config_falls_back_to_default", cfg: nil, want: defaultPreWarmReadinessTimeout},
		{name: "unset_key_falls_back_to_default", cfg: &config.Config{}, want: defaultPreWarmReadinessTimeout},
		{
			name: "operator_value_wins",
			cfg: &config.Config{Messaging: config.MessagingConfig{
				Reconnect: config.ReconnectConfig{ReadyTimeout: 20 * time.Second},
			}},
			want: 20 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &App{cfg: tt.cfg}
			assert.Equal(t, tt.want, a.publisherReadinessTimeout())
		})
	}
}

func TestPreWarmSingleTenantAwaitsPublisherReadiness(t *testing.T) {
	log := logger.New("debug", true)
	client := newPrewarmMockClient()
	manager := newPrewarmTestManager(log, client)
	defer func() { _ = manager.Close() }()

	a := newPreWarmApp(log, manager, 0)

	go func() {
		time.Sleep(150 * time.Millisecond)
		client.SetReady(true)
	}()

	start := time.Now()
	err := a.preWarmSingleTenant(context.Background(), nil)
	elapsed := time.Since(start)

	assert.NoError(t, err)
	assert.Less(t, elapsed, defaultPreWarmReadinessTimeout,
		"must return once the client reports ready, not wait out the full budget")
}

func TestPreWarmSingleTenantContinuesWhenPublisherNeverReady(t *testing.T) {
	log := logger.New("debug", true)
	client := newPrewarmMockClient() // never flips ready
	manager := newPrewarmTestManager(log, client)
	defer func() { _ = manager.Close() }()

	// A short operator budget (messaging.reconnect.readytimeout) so the genuine
	// timeout branch fires without waiting out the 5s fallback.
	a := newPreWarmApp(log, manager, 200*time.Millisecond)

	start := time.Now()
	err := a.preWarmSingleTenant(context.Background(), nil)
	elapsed := time.Since(start)

	// Not-ready-in-time is a WARN, not a startup failure — pre-warm must not
	// propagate an error; PublishToExchange's own readytimeout pre-flight will
	// still absorb a slow first publish later.
	assert.NoError(t, err)
	assert.Less(t, elapsed, time.Second, "must return once the configured budget elapses, not the 5s fallback")
}

func TestPreWarmSingleTenantPropagatesContextCancellation(t *testing.T) {
	log := logger.New("debug", true)
	client := newPrewarmMockClient() // never flips ready
	manager := newPrewarmTestManager(log, client)
	defer func() { _ = manager.Close() }()

	a := newPreWarmApp(log, manager, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := a.preWarmSingleTenant(ctx, nil)
	elapsed := time.Since(start)

	// Cancellation means shutdown/startup abort, not a broker-readiness problem —
	// it propagates instead of being mislabeled by the generic not-ready WARN.
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, time.Second, "must return once ctx expires")
}
