package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/streams"
)

const (
	testStreamName         = "orders"
	testStreamConsumer     = "orders-processor"
	unreachableStreamURI   = "rabbitmq-stream://guest:guest@127.0.0.1:1/%2f"
	streamsUnconfiguredMsg = "set messaging.streams.uri"
)

// streamModule implements Module + StreamDeclarer.
type streamModule struct {
	name        string
	calls       int
	declaration func(decls *streams.Declarations)
}

func (m *streamModule) Name() string             { return m.name }
func (m *streamModule) Init(_ *ModuleDeps) error { return nil }
func (m *streamModule) Shutdown() error          { return nil }
func (m *streamModule) DeclareStreams(decls *streams.Declarations) {
	m.calls++
	if m.declaration != nil {
		m.declaration(decls)
	}
}

func declareOneConsumer(decls *streams.Declarations) {
	decls.DeclareStream(testStreamName, nil)
	decls.DeclareConsumer(&streams.ConsumerOptions{
		Stream:  testStreamName,
		Name:    testStreamConsumer,
		Handler: func(context.Context, *streams.Message) error { return nil },
	})
}

func declareOnePublisher(decls *streams.Declarations) {
	decls.DeclareStream(testStreamName, nil)
	decls.DeclarePublisher(&streams.PublisherOptions{Stream: testStreamName})
}

func newStreamsApp(t *testing.T, streamsCfg config.StreamsConfig, modules ...Module) *App {
	t.Helper()
	return newStreamsAppWithLogger(t, logger.New("error", false), streamsCfg, modules...)
}

func newStreamsAppWithLogger(t *testing.T, log logger.Logger, streamsCfg config.StreamsConfig, modules ...Module) *App {
	t.Helper()

	cfg := &config.Config{
		App:       config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"},
		Messaging: config.MessagingConfig{Streams: streamsCfg},
	}
	registry := NewModuleRegistry(&ModuleDeps{Logger: log, Config: cfg})
	for _, m := range modules {
		require.NoError(t, registry.Register(m))
	}

	return &App{cfg: cfg, logger: log, registry: registry}
}

func withUnlinkedStreamRuntime(t *testing.T) {
	t.Helper()
	prev := swapStreamRuntime(nil)
	t.Cleanup(func() { swapStreamRuntime(prev) })
}

func TestPrepareStreamConsumersFailsWhenConfiguredButNotLinked(t *testing.T) {
	withUnlinkedStreamRuntime(t)
	a := newStreamsApp(t, config.StreamsConfig{URI: unreachableStreamURI})

	err := a.prepareStreamConsumers(context.Background())

	require.ErrorIs(t, err, ErrStreamsNotLinked)
	assert.Contains(t, err.Error(), `import _ "github.com/gaborage/go-bricks/messaging/streams"`)
	assert.Nil(t, a.streamsManager)
}

func TestPrepareStreamConsumersStartsCleanWhenUnlinkedAndUnconfigured(t *testing.T) {
	withUnlinkedStreamRuntime(t)
	a := newStreamsApp(t, config.StreamsConfig{})

	require.NoError(t, a.prepareStreamConsumers(context.Background()))
	assert.Nil(t, a.streamsManager)
}

func TestRegisterStreamRuntimeRejectsNil(t *testing.T) {
	assert.Panics(t, func() { RegisterStreamRuntime(nil) })
}

func TestPrepareStreamConsumersWithoutDeclarationsIsNoop(t *testing.T) {
	a := newStreamsApp(t, config.StreamsConfig{}, &minimalModule{name: "plain"})

	require.NoError(t, a.prepareStreamConsumers(context.Background()))

	assert.Nil(t, a.streamsManager)
	assert.Empty(t, a.closers)
}

func TestPrepareStreamConsumersRequiresRegistry(t *testing.T) {
	a := &App{logger: logger.New("error", false)}

	err := a.prepareStreamConsumers(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "module registry not initialized")
}

func TestPrepareStreamConsumersFailsWhenDeclaredButUnconfigured(t *testing.T) {
	a := newStreamsApp(t, config.StreamsConfig{},
		&streamModule{name: "orders", declaration: declareOneConsumer})

	err := a.prepareStreamConsumers(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream declarations were registered")
	assert.Contains(t, err.Error(), "streams=1, consumers=1, publishers=0")
	assert.Contains(t, err.Error(), streamsUnconfiguredMsg)
	assert.Nil(t, a.streamsManager)
}

// TestPrepareStreamConsumersFailsWhenOnlyAPublisherIsDeclared mirrors the
// consumer case for a service that only publishes: the gate still fires, and the
// count names the publisher that tripped it rather than reporting zero of
// everything the operator can see.
func TestPrepareStreamConsumersFailsWhenOnlyAPublisherIsDeclared(t *testing.T) {
	a := newStreamsApp(t, config.StreamsConfig{},
		&streamModule{name: "orders", declaration: declareOnePublisher})

	err := a.prepareStreamConsumers(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "streams=1, consumers=0, publishers=1")
	assert.Contains(t, err.Error(), streamsUnconfiguredMsg)
	assert.Nil(t, a.streamsManager)
}

func TestPrepareStreamConsumersPropagatesValidationFailure(t *testing.T) {
	a := newStreamsApp(t, config.StreamsConfig{URI: unreachableStreamURI},
		&streamModule{name: "orders", declaration: func(decls *streams.Declarations) {
			decls.DeclareConsumer(&streams.ConsumerOptions{
				Stream:  "undeclared",
				Name:    testStreamConsumer,
				Handler: func(context.Context, *streams.Message) error { return nil },
			})
		}})

	err := a.prepareStreamConsumers(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream declaration validation failed")
	assert.Nil(t, a.streamsManager)
}

func TestPrepareStreamConsumersFailsWhenBrokerUnreachable(t *testing.T) {
	a := newStreamsApp(t, config.StreamsConfig{URI: unreachableStreamURI},
		&streamModule{name: "orders", declaration: declareOneConsumer})

	err := a.prepareStreamConsumers(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start stream consumers")
	assert.NotContains(t, err.Error(), "guest:guest", "the stream URI's credentials must never reach an error message")
	assert.Nil(t, a.streamsManager, "a failed start publishes no manager for the slot to register")
	assert.Empty(t, a.closers)
}

// TestPrepareStreamConsumersIgnoresCallerCancellation is the app half of the
// consume-context contract: the startup context reaches the manager for its
// values, and its cancellation carries no weight here — consumer lifetime belongs
// to StopConsumers, so an already-canceled context changes nothing about how
// startup succeeds or fails.
func TestPrepareStreamConsumersIgnoresCallerCancellation(t *testing.T) {
	a := newStreamsApp(t, config.StreamsConfig{URI: unreachableStreamURI},
		&streamModule{name: "orders", declaration: declareOneConsumer})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := a.prepareStreamConsumers(ctx)

	require.Error(t, err, "the premise: this broker is unreachable")
	assert.Contains(t, err.Error(), "failed to start stream consumers")
	assert.NotErrorIs(t, err, context.Canceled, "startup never consults the caller's cancellation")
}

// TestPrepareStreamConsumersReportsOnlyRealCleanupFailures pins the guard on the
// failed-start cleanup. The dial never completed, so the manager holds no
// environment and Close is a no-op; a guard reading its result the wrong way
// round would tell the operator the environment failed to close on every single
// failed startup, burying the real cause.
func TestPrepareStreamConsumersReportsOnlyRealCleanupFailures(t *testing.T) {
	log := &recLogger{}
	a := newStreamsAppWithLogger(t, log, config.StreamsConfig{URI: unreachableStreamURI},
		&streamModule{name: "orders", declaration: declareOneConsumer})

	err := a.prepareStreamConsumers(context.Background())

	require.Error(t, err, "the premise: the start fails before an environment exists")
	assert.False(t, loggedMsgContains(log, "Failed to close stream environment"),
		"a cleanup that had nothing to close is not reported as a failure")
}

func TestPrepareStreamConsumersInvokesEveryDeclarerOnce(t *testing.T) {
	m := &streamModule{name: "orders", declaration: declareOneConsumer}
	a := newStreamsApp(t, config.StreamsConfig{}, m)

	_ = a.prepareStreamConsumers(context.Background())

	assert.Equal(t, 1, m.calls)
}

func TestShutdownStreamConsumersWithoutManagerIsNoop(t *testing.T) {
	a := &App{logger: logger.New("error", false)}

	assert.NotPanics(t, a.shutdownStreamConsumers)
}

func TestShutdownStreamConsumersStopsTheManager(t *testing.T) {
	a := &App{logger: logger.New("error", false)}
	a.streamsManager = streams.NewManager(streams.ManagerOptions{
		URI:    unreachableStreamURI,
		Logger: a.logger,
	})

	a.shutdownStreamConsumers()

	assert.Equal(t, false, a.streamsManager.Stats()["started"])
}

// TestPrepareStreamConsumersRejectsMultiTenantBypass pins the runtime re-check
// (assertStreamsNotPerTenant) as a defense-in-depth backstop: it builds the App
// directly, bypassing Builder.WithConfig's config.Validate call (which now runs on
// every NewWithConfig — see B1), so a multi-tenant service with a stream URI and a
// declaring module would otherwise boot green and run handlers with no tenant in
// context.
func TestPrepareStreamConsumersRejectsMultiTenantBypass(t *testing.T) {
	a := newStreamsApp(t, config.StreamsConfig{URI: unreachableStreamURI},
		&streamModule{name: "orders", declaration: declareOneConsumer})
	a.cfg.Multitenant.Enabled = true

	err := a.prepareStreamConsumers(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "single-tenant mode or messaging.tenancy: shared")
	assert.Nil(t, a.streamsManager, "no Environment is built for a multi-tenant service")
	assert.Empty(t, a.closers)
}

// TestPrepareStreamConsumersAllowsSingleTenant is the negative half: the re-check
// must not block the supported deployment.
func TestPrepareStreamConsumersAllowsSingleTenant(t *testing.T) {
	a := newStreamsApp(t, config.StreamsConfig{}, &minimalModule{name: "plain"})
	a.cfg.Multitenant.Enabled = false

	require.NoError(t, a.assertStreamsNotPerTenant())
}

// TestPrepareStreamConsumersAdmitsSharedTenancy is the half this slice adds: the
// gate refuses PER-TENANT stream consumption, which would need one Environment per
// tenant, not multi-tenancy as such. Under messaging.tenancy: shared the lane
// consumes once on the control-plane key, exactly as single-tenant does.
func TestPrepareStreamConsumersAdmitsSharedTenancy(t *testing.T) {
	a := newStreamsApp(t, config.StreamsConfig{}, &minimalModule{name: "plain"})
	a.cfg.Multitenant.Enabled = true
	a.cfg.Messaging.Tenancy = config.TenancyShared

	require.NoError(t, a.assertStreamsNotPerTenant())
}

func TestWarnIfPlaintextStreamURI(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		uri      string
		wantWarn bool
	}{
		{name: "plaintext_outside_development_warns", env: "production", uri: "rabbitmq-stream://broker:5552/", wantWarn: true},
		{name: "plaintext_in_development_is_silent", env: "development", uri: "rabbitmq-stream://broker:5552/"},
		{name: "tls_scheme_is_silent", env: "production", uri: "rabbitmq-stream+tls://broker:5551/"},
		{name: "unparseable_uri_is_silent", env: "production", uri: "rabbitmq-stream://broker:55 52/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &recLogger{}
			a := &App{
				cfg:    &config.Config{App: config.AppConfig{Name: testApp, Env: tt.env, Version: "1.0.0"}},
				logger: log,
			}

			a.warnIfPlaintextStreamURI(tt.uri)

			warnings := log.warnLines()
			if !tt.wantWarn {
				assert.Empty(t, warnings)
				return
			}
			require.Len(t, warnings, 1)
			assert.Contains(t, warnings[0].msg, "plaintext stream protocol")
			assert.NotContains(t, warnings[0].msg, "broker:5552", "the endpoint itself stays out of the warning")
		})
	}
}
