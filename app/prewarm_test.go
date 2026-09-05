package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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

// TestSlotStartSkipsAbsentManagers pins the absence guard: with neither manager built, the
// start phase is a silent no-op and never reports a problem.
func TestSlotStartSkipsAbsentManagers(t *testing.T) {
	log := logger.New("debug", true)
	cfg := &config.Config{}
	// The streams kind's "absent" is a registry that declares no stream, so it gets one:
	// its start delegates to prepareStreamConsumers, which refuses a nil registry outright.
	a := &App{logger: log, cfg: cfg, registry: NewModuleRegistry(&ModuleDeps{Logger: log, Config: cfg})}
	a.installSlots(slotInputs{})

	for _, slot := range a.slots {
		advisory, fatal := slot.start(context.Background())
		require.NoError(t, fatal, slot.name())
		require.NoError(t, advisory, slot.name())
	}
}

func TestAppAwaitPublisherReady(t *testing.T) {
	log := logger.New("debug", true)
	a := newMinimalMessagingApp(log, nil, &config.Config{})

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
		short := newMinimalMessagingApp(log, nil, &config.Config{
			Messaging: config.MessagingConfig{Reconnect: config.ReconnectConfig{ReadyTimeout: 150 * time.Millisecond}},
		})
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

func TestMessagingSlotStartAwaitsPublisherReadiness(t *testing.T) {
	log := logger.New("debug", true)
	client := newPrewarmMockClient()
	manager := newPrewarmTestManager(log, client)
	defer func() { _ = manager.Close() }()

	a := newMinimalMessagingApp(log, manager, &config.Config{})

	go func() {
		time.Sleep(150 * time.Millisecond)
		client.SetReady(true)
	}()

	start := time.Now()
	err, fatal := slotOf(t, a, componentMessaging).start(context.Background())
	elapsed := time.Since(start)

	require.NoError(t, fatal, "pre-warming is never fatal")
	assert.NoError(t, err)
	assert.Less(t, elapsed, defaultPreWarmReadinessTimeout,
		"must return once the client reports ready, not wait out the full budget")
}

func TestMessagingSlotStartContinuesWhenPublisherNeverReady(t *testing.T) {
	log := logger.New("debug", true)
	client := newPrewarmMockClient() // never flips ready
	manager := newPrewarmTestManager(log, client)
	defer func() { _ = manager.Close() }()

	// A short operator budget (messaging.reconnect.readytimeout) so the genuine
	// timeout branch fires without waiting out the 5s fallback.
	a := newMinimalMessagingApp(log, manager, &config.Config{
		Messaging: config.MessagingConfig{Reconnect: config.ReconnectConfig{ReadyTimeout: 200 * time.Millisecond}},
	})

	start := time.Now()
	err, fatal := slotOf(t, a, componentMessaging).start(context.Background())
	elapsed := time.Since(start)

	// Not-ready-in-time is a WARN, not a startup failure — pre-warm must not
	// propagate an error; PublishToExchange's own readytimeout pre-flight will
	// still absorb a slow first publish later.
	require.NoError(t, fatal, "pre-warming is never fatal")
	assert.NoError(t, err)
	assert.Less(t, elapsed, time.Second, "must return once the configured budget elapses, not the 5s fallback")
}

func TestMessagingSlotStartPropagatesContextCancellation(t *testing.T) {
	log := logger.New("debug", true)
	client := newPrewarmMockClient() // never flips ready
	manager := newPrewarmTestManager(log, client)
	defer func() { _ = manager.Close() }()

	a := newMinimalMessagingApp(log, manager, &config.Config{})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err, fatal := slotOf(t, a, componentMessaging).start(ctx)
	elapsed := time.Since(start)

	// Cancellation means shutdown/startup abort, not a broker-readiness problem —
	// it propagates instead of being mislabeled by the generic not-ready WARN.
	require.NoError(t, fatal, "pre-warming is never fatal")
	assert.Less(t, elapsed, time.Second, "must return once ctx expires")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestPreWarmGateIsPerKind pins WHICH gate each slot hands preWarmKind. The two
// arguments are both bools, so a swapped pair compiles and every pre-existing test
// still passes; only these cases separate them.
//
// Messaging under shared tenancy resolves the control-plane key, so it pre-warms
// even though multitenant.enabled is true — while the database, resolved per
// tenant in the same deployment, must not be warmed on the "" key.
func TestPreWarmGateIsPerKind(t *testing.T) {
	sharedMT := &config.Config{
		Multitenant: config.MultitenantConfig{Enabled: true},
		Messaging:   config.MessagingConfig{Tenancy: config.TenancyShared},
	}
	perTenantMT := &config.Config{
		Multitenant: config.MultitenantConfig{Enabled: true},
		Messaging:   config.MessagingConfig{Tenancy: config.TenancyPerTenant},
	}

	t.Run("shared_messaging_pre_warms_on_the_control_plane_key", func(t *testing.T) {
		rec := &recLogger{}
		client := newPrewarmMockClient()
		client.SetReady(true)
		manager := newPrewarmTestManager(rec, client)
		defer func() { _ = manager.Close() }()
		a := newMinimalMessagingApp(rec, manager, sharedMT)

		advisory, fatal := slotOf(t, a, componentMessaging).start(context.Background())

		require.NoError(t, fatal)
		require.NoError(t, advisory)
		assert.Positive(t, loggedCount(rec, "Pre-warmed messaging publisher"),
			"shared tenancy has a fixed key to warm, so the messaging pre-warm must run")
	})

	t.Run("per_tenant_messaging_does_not_pre_warm", func(t *testing.T) {
		rec := &recLogger{}
		client := newPrewarmMockClient()
		client.SetReady(true)
		manager := newPrewarmTestManager(rec, client)
		defer func() { _ = manager.Close() }()
		a := newMinimalMessagingApp(rec, manager, perTenantMT)

		advisory, fatal := slotOf(t, a, componentMessaging).start(context.Background())

		require.NoError(t, fatal)
		require.NoError(t, advisory)
		assert.Zero(t, loggedCount(rec, "Pre-warmed messaging publisher"),
			"per-tenant tenancy has no fixed key, so nothing may be warmed at startup")
	})

	t.Run("database_pre_warm_ignores_the_messaging_tenancy", func(t *testing.T) {
		rec := &recLogger{}
		client := newPrewarmMockClient()
		client.SetReady(true)
		manager := newPrewarmTestManager(rec, client)
		defer func() { _ = manager.Close() }()
		a := newMinimalMessagingApp(rec, manager, sharedMT)

		advisory, fatal := slotOf(t, a, componentDatabase).start(context.Background())

		require.NoError(t, fatal)
		require.NoError(t, advisory)
		assert.Zero(t, loggedCount(rec, "Pre-warmed control-plane database connection"),
			"the database is still resolved per tenant when only messaging is shared")
	})
}

// declaredConsumerFixture returns declarationsWithConsumer() (see
// messaging_setup_test.go) plus the queue its one consumer references, so
// Declarations.Validate() — which rejects a consumer pointing at an
// unregistered queue — accepts it. Shared by every call site that needs a
// non-nil, non-empty, genuinely valid declaration set.
func declaredConsumerFixture(t *testing.T) *messaging.Declarations {
	t.Helper()
	decls := declarationsWithConsumer()
	decls.RegisterQueue(&messaging.QueueDeclaration{Name: "orders.queue"})
	require.NoError(t, decls.Validate())
	return decls
}

// consumerBootstrapAnnouncements counts the app-layer lines announcing a consumer
// bootstrap: the surviving one prepareRuntimeConsumers emits, plus the retired one
// preWarmMessaging emitted from its own second EnsureConsumers call. The mock client's
// ConsumeFromQueue count cannot stand in for it — the manager's replay cache absorbs a
// duplicate EnsureConsumers silently, so the broker-facing calls read 1 whether the
// bootstrap ran once or twice.
func consumerBootstrapAnnouncements(rec *recLogger) int {
	return loggedCount(rec, "Consumers started on the control-plane key") +
		loggedCount(rec, "Ensured messaging consumers")
}

// TestMessagingSlotStartBootstrapsConsumersOnce pins where the consumer bootstrap lives:
// once, in prepareRuntimeConsumers. preWarmMessaging used to run EnsureConsumers a second
// time on the way to the publisher, which announced the same bootstrap twice and left one
// failure reachable under two different gradings — fatal from the bootstrap, advisory from
// the pre-warm. The slot's start must now bootstrap exactly once and go straight on to
// publisher readiness.
func TestMessagingSlotStartBootstrapsConsumersOnce(t *testing.T) {
	rec := &recLogger{}
	client := testmocks.NewMockAMQPClient() // defaults to ready
	client.ExpectClose(nil)
	client.ExpectDeclareQueueAny(nil)
	client.On("ConsumeFromQueue", mock.Anything, mock.Anything).Return(nil, nil)

	manager := newPrewarmTestManager(rec, client)
	defer func() { _ = manager.Close() }()

	a := newMinimalMessagingApp(rec, manager, &config.Config{})
	a.messagingDeclarations = declaredConsumerFixture(t)

	advisory, fatal := slotOf(t, a, componentMessaging).start(context.Background())

	require.NoError(t, fatal)
	require.NoError(t, advisory)
	assert.Equal(t, 1, consumerBootstrapAnnouncements(rec),
		"the consumer bootstrap runs once per start, in prepareRuntimeConsumers")
	assert.Equal(t, 1, loggedCount(rec, "Pre-warmed messaging publisher"),
		"the pre-warm still reaches the publisher it exists to warm")
}
