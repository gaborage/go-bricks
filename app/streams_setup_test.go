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

func newStreamsApp(t *testing.T, streamsCfg config.StreamsConfig, modules ...Module) *App {
	t.Helper()

	log := logger.New("error", false)
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

func TestPrepareStreamConsumersWithoutDeclarationsIsNoop(t *testing.T) {
	a := newStreamsApp(t, config.StreamsConfig{}, &minimalModule{name: "plain"})

	require.NoError(t, a.prepareStreamConsumers())

	assert.Nil(t, a.streamsManager)
	assert.Empty(t, a.healthProbes, "a streams-free service keeps its probe list unchanged")
	assert.Empty(t, a.closers)
}

func TestPrepareStreamConsumersRequiresRegistry(t *testing.T) {
	a := &App{logger: logger.New("error", false)}

	err := a.prepareStreamConsumers()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "module registry not initialized")
}

func TestPrepareStreamConsumersFailsWhenDeclaredButUnconfigured(t *testing.T) {
	a := newStreamsApp(t, config.StreamsConfig{},
		&streamModule{name: "orders", declaration: declareOneConsumer})

	err := a.prepareStreamConsumers()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream declarations were registered")
	assert.Contains(t, err.Error(), "streams=1, consumers=1")
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

	err := a.prepareStreamConsumers()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream declaration validation failed")
	assert.Nil(t, a.streamsManager)
}

func TestPrepareStreamConsumersFailsWhenBrokerUnreachable(t *testing.T) {
	a := newStreamsApp(t, config.StreamsConfig{URI: unreachableStreamURI},
		&streamModule{name: "orders", declaration: declareOneConsumer})

	err := a.prepareStreamConsumers()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start stream consumers")
	assert.NotContains(t, err.Error(), "guest:guest", "the stream URI's credentials must never reach an error message")
	assert.Nil(t, a.streamsManager, "a failed start registers neither a probe nor a closer")
	assert.Empty(t, a.healthProbes)
	assert.Empty(t, a.closers)
}

func TestPrepareStreamConsumersInvokesEveryDeclarerOnce(t *testing.T) {
	m := &streamModule{name: "orders", declaration: declareOneConsumer}
	a := newStreamsApp(t, config.StreamsConfig{}, m)

	_ = a.prepareStreamConsumers()

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
