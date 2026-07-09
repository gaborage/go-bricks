package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

const (
	testKey = "test-key"
)

func TestNewConnectionPreWarmer(t *testing.T) {
	t.Run("creates prewarmer with all components", func(t *testing.T) {
		log := logger.New("debug", true)
		dbManager := &database.DbManager{}
		messagingManager := &messaging.Manager{}

		prewarmer := NewConnectionPreWarmer(log, dbManager, messagingManager)

		assert.NotNil(t, prewarmer)
		assert.Equal(t, log, prewarmer.logger)
		assert.Equal(t, dbManager, prewarmer.dbManager)
		assert.Equal(t, messagingManager, prewarmer.messagingManager)
	})

	t.Run("creates prewarmer with nil managers", func(t *testing.T) {
		log := logger.New("debug", true)

		prewarmer := NewConnectionPreWarmer(log, nil, nil)

		assert.NotNil(t, prewarmer)
		assert.Equal(t, log, prewarmer.logger)
		assert.Nil(t, prewarmer.dbManager)
		assert.Nil(t, prewarmer.messagingManager)
	})
}

func TestPreWarmSingleTenant(t *testing.T) {
	t.Run("works with nil managers", func(t *testing.T) {
		log := logger.New("debug", true)

		prewarmer := &ConnectionPreWarmer{
			logger:           log,
			dbManager:        nil,
			messagingManager: nil,
		}

		declarations := messaging.NewDeclarations()
		err := prewarmer.PreWarmSingleTenant(context.Background(), declarations)

		assert.NoError(t, err)
	})

	t.Run("logs debug messages for nil managers", func(t *testing.T) {
		log := logger.New("debug", true)

		prewarmer := NewConnectionPreWarmer(log, nil, nil)

		declarations := messaging.NewDeclarations()
		err := prewarmer.PreWarmSingleTenant(context.Background(), declarations)

		// Should complete without error when managers are nil
		assert.NoError(t, err)
	})
}

func TestPreWarmDatabase(t *testing.T) {
	t.Run("database prewarming with nil manager", func(t *testing.T) {
		log := logger.New("debug", true)

		prewarmer := &ConnectionPreWarmer{
			logger:    log,
			dbManager: nil,
		}

		err := prewarmer.PreWarmDatabase(context.Background(), testKey)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "database manager not available")
	})
}

func TestPreWarmMessaging(t *testing.T) {
	t.Run("messaging prewarming with nil manager", func(t *testing.T) {
		log := logger.New("debug", true)

		prewarmer := &ConnectionPreWarmer{
			logger:           log,
			messagingManager: nil,
		}

		declarations := messaging.NewDeclarations()
		err := prewarmer.PreWarmMessaging(context.Background(), testKey, declarations)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "messaging manager not available")
	})

	t.Run("messaging prewarming with nil declarations", func(t *testing.T) {
		log := logger.New("debug", true)

		prewarmer := &ConnectionPreWarmer{
			logger:           log,
			messagingManager: nil,
		}

		err := prewarmer.PreWarmMessaging(context.Background(), testKey, nil)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "messaging manager not available")
	})
}

func TestPrewarmerIsAvailable(t *testing.T) {
	t.Run("returns true when both managers available", func(t *testing.T) {
		prewarmer := &ConnectionPreWarmer{
			dbManager:        &database.DbManager{},
			messagingManager: &messaging.Manager{},
		}

		assert.True(t, prewarmer.IsAvailable())
	})

	t.Run("returns true when only db manager available", func(t *testing.T) {
		prewarmer := &ConnectionPreWarmer{
			dbManager:        &database.DbManager{},
			messagingManager: nil,
		}

		assert.True(t, prewarmer.IsAvailable())
	})

	t.Run("returns true when only messaging manager available", func(t *testing.T) {
		prewarmer := &ConnectionPreWarmer{
			dbManager:        nil,
			messagingManager: &messaging.Manager{},
		}

		assert.True(t, prewarmer.IsAvailable())
	})

	t.Run("returns false when no managers available", func(t *testing.T) {
		prewarmer := &ConnectionPreWarmer{
			dbManager:        nil,
			messagingManager: nil,
		}

		assert.False(t, prewarmer.IsAvailable())
	})
}

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
	return messaging.NewMessagingManager(&fakeBrokerURLProvider{url: "amqp://localhost"}, log, messaging.ManagerOptions{MaxPublishers: 5, IdleTTL: time.Hour}, factory)
}

func TestConnectionPreWarmerAwaitPublisherReady(t *testing.T) {
	log := logger.New("debug", true)
	prewarmer := &ConnectionPreWarmer{logger: log}

	t.Run("already ready returns immediately", func(t *testing.T) {
		client := testmocks.NewMockAMQPClient() // defaults to ready
		assert.Equal(t, preWarmReady, prewarmer.awaitPublisherReady(context.Background(), client))
	})

	t.Run("becomes ready during poll", func(t *testing.T) {
		client := newPrewarmMockClient()
		go func() {
			time.Sleep(150 * time.Millisecond)
			client.SetReady(true)
		}()
		assert.Equal(t, preWarmReady, prewarmer.awaitPublisherReady(context.Background(), client))
	})

	t.Run("ctx cancellation reported distinctly without waiting out the budget", func(t *testing.T) {
		client := newPrewarmMockClient()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		start := time.Now()
		outcome := prewarmer.awaitPublisherReady(ctx, client)
		elapsed := time.Since(start)

		assert.Equal(t, preWarmCanceled, outcome)
		assert.Less(t, elapsed, time.Second, "must return once ctx expires, not wait out the readiness budget")
	})

	t.Run("configured budget elapses without readiness", func(t *testing.T) {
		shortPrewarmer := &ConnectionPreWarmer{logger: log, readinessTimeout: 150 * time.Millisecond}
		client := newPrewarmMockClient()

		start := time.Now()
		outcome := shortPrewarmer.awaitPublisherReady(context.Background(), client)
		elapsed := time.Since(start)

		assert.Equal(t, preWarmNotReadyInTime, outcome)
		assert.Less(t, elapsed, time.Second, "must honor the configured budget, not the 5s fallback")
	})
}

func TestConnectionPreWarmerPublisherReadinessTimeout(t *testing.T) {
	t.Run("falls back to default when unset", func(t *testing.T) {
		prewarmer := &ConnectionPreWarmer{}
		assert.Equal(t, defaultPreWarmReadinessTimeout, prewarmer.publisherReadinessTimeout())
	})

	t.Run("uses the threaded operator value", func(t *testing.T) {
		prewarmer := &ConnectionPreWarmer{readinessTimeout: 20 * time.Second}
		assert.Equal(t, 20*time.Second, prewarmer.publisherReadinessTimeout())
	})
}

func TestPreWarmMessagingAwaitsPublisherReadiness(t *testing.T) {
	log := logger.New("debug", true)
	client := newPrewarmMockClient()
	manager := newPrewarmTestManager(log, client)
	defer func() { _ = manager.Close() }()

	prewarmer := NewConnectionPreWarmer(log, nil, manager)

	go func() {
		time.Sleep(150 * time.Millisecond)
		client.SetReady(true)
	}()

	start := time.Now()
	err := prewarmer.PreWarmMessaging(context.Background(), testKey, nil)
	elapsed := time.Since(start)

	assert.NoError(t, err)
	assert.Less(t, elapsed, defaultPreWarmReadinessTimeout, "must return once the client reports ready, not wait out the full budget")
}

func TestPreWarmMessagingContinuesWhenPublisherNeverReady(t *testing.T) {
	log := logger.New("debug", true)
	client := newPrewarmMockClient() // never flips ready
	manager := newPrewarmTestManager(log, client)
	defer func() { _ = manager.Close() }()

	prewarmer := NewConnectionPreWarmer(log, nil, manager)
	// Thread a short budget (as ConfigureRuntimeHelpers does from
	// messaging.reconnect.readytimeout) so the genuine timeout branch fires
	// without waiting out the 5s fallback.
	prewarmer.readinessTimeout = 200 * time.Millisecond

	start := time.Now()
	err := prewarmer.PreWarmMessaging(context.Background(), testKey, nil)
	elapsed := time.Since(start)

	// Not-ready-in-time is a WARN, not a startup failure — pre-warm must not
	// propagate an error; PublishToExchange's own readytimeout pre-flight will
	// still absorb a slow first publish later.
	assert.NoError(t, err)
	assert.Less(t, elapsed, time.Second, "must return once the configured budget elapses, not the 5s fallback")
}

func TestPreWarmMessagingPropagatesContextCancellation(t *testing.T) {
	log := logger.New("debug", true)
	client := newPrewarmMockClient() // never flips ready
	manager := newPrewarmTestManager(log, client)
	defer func() { _ = manager.Close() }()

	prewarmer := NewConnectionPreWarmer(log, nil, manager)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := prewarmer.PreWarmMessaging(ctx, testKey, nil)
	elapsed := time.Since(start)

	// Cancellation means shutdown/startup abort, not a broker-readiness problem —
	// it propagates instead of being mislabeled by the generic not-ready WARN.
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, time.Second, "must return once ctx expires")
}

func TestLogAvailability(t *testing.T) {
	t.Run("logs availability with both managers", func(_ *testing.T) {
		log := logger.New("debug", true)
		prewarmer := &ConnectionPreWarmer{
			logger:           log,
			dbManager:        &database.DbManager{},
			messagingManager: &messaging.Manager{},
		}

		// This test primarily ensures the function runs without panic
		prewarmer.LogAvailability()
	})

	t.Run("logs availability with no managers", func(_ *testing.T) {
		log := logger.New("debug", true)
		prewarmer := &ConnectionPreWarmer{
			logger:           log,
			dbManager:        nil,
			messagingManager: nil,
		}

		// This test primarily ensures the function runs without panic
		prewarmer.LogAvailability()
	})

	t.Run("logs availability with only db manager", func(_ *testing.T) {
		log := logger.New("debug", true)
		prewarmer := &ConnectionPreWarmer{
			logger:           log,
			dbManager:        &database.DbManager{},
			messagingManager: nil,
		}

		// This test primarily ensures the function runs without panic
		prewarmer.LogAvailability()
	})
}
