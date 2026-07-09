package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"

	amqp "github.com/rabbitmq/amqp091-go"
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

// fakeReadinessClient is a minimal messaging.AMQPClient whose IsReady() can be
// toggled by the test, used to exercise ConnectionPreWarmer.awaitPublisherReady
// and PreWarmMessaging without a real broker.
type fakeReadinessClient struct {
	mu    sync.Mutex
	ready bool
}

func (f *fakeReadinessClient) setReady(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ready = v
}
func (f *fakeReadinessClient) IsReady() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready
}
func (f *fakeReadinessClient) Publish(context.Context, string, []byte) error { return nil }
func (f *fakeReadinessClient) Consume(context.Context, string) (<-chan amqp.Delivery, error) {
	return nil, nil
}
func (f *fakeReadinessClient) Close() error { return nil }
func (f *fakeReadinessClient) PublishToExchange(context.Context, messaging.PublishOptions, []byte) error {
	return nil
}
func (f *fakeReadinessClient) ConsumeFromQueue(context.Context, messaging.ConsumeOptions) (<-chan amqp.Delivery, error) {
	return nil, nil
}
func (f *fakeReadinessClient) DeclareQueue(string, bool, bool, bool, bool) error { return nil }
func (f *fakeReadinessClient) DeclareExchange(string, string, bool, bool, bool, bool) error {
	return nil
}
func (f *fakeReadinessClient) BindQueue(string, string, string, bool) error { return nil }

// fakeBrokerURLProvider is a minimal messaging.BrokerURLProvider for tests
// that need a real *messaging.Manager without a real broker.
type fakeBrokerURLProvider struct{ url string }

func (f *fakeBrokerURLProvider) BrokerURL(context.Context, string) (string, error) {
	return f.url, nil
}

func TestConnectionPreWarmerAwaitPublisherReady(t *testing.T) {
	log := logger.New("debug", true)
	prewarmer := &ConnectionPreWarmer{logger: log}

	t.Run("already ready returns immediately", func(t *testing.T) {
		fake := &fakeReadinessClient{ready: true}
		assert.True(t, prewarmer.awaitPublisherReady(context.Background(), fake))
	})

	t.Run("becomes ready during poll", func(t *testing.T) {
		fake := &fakeReadinessClient{}
		go func() {
			time.Sleep(150 * time.Millisecond)
			fake.setReady(true)
		}()
		assert.True(t, prewarmer.awaitPublisherReady(context.Background(), fake))
	})

	t.Run("ctx cancellation returns false without waiting out the timeout", func(t *testing.T) {
		fake := &fakeReadinessClient{}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		start := time.Now()
		ready := prewarmer.awaitPublisherReady(ctx, fake)
		elapsed := time.Since(start)

		assert.False(t, ready)
		assert.Less(t, elapsed, time.Second, "must return once ctx expires, not wait out the full preWarmReadinessTimeout")
	})
}

func TestPreWarmMessagingAwaitsPublisherReadiness(t *testing.T) {
	log := logger.New("debug", true)
	fake := &fakeReadinessClient{}
	factory := func(string, logger.Logger) messaging.AMQPClient { return fake }
	manager := messaging.NewMessagingManager(&fakeBrokerURLProvider{url: "amqp://localhost"}, log, messaging.ManagerOptions{MaxPublishers: 5, IdleTTL: time.Hour}, factory)
	defer func() { _ = manager.Close() }()

	prewarmer := NewConnectionPreWarmer(log, nil, manager)

	go func() {
		time.Sleep(150 * time.Millisecond)
		fake.setReady(true)
	}()

	start := time.Now()
	err := prewarmer.PreWarmMessaging(context.Background(), testKey, nil)
	elapsed := time.Since(start)

	assert.NoError(t, err)
	assert.Less(t, elapsed, preWarmReadinessTimeout, "must return once the client reports ready, not wait out the full timeout")
}

func TestPreWarmMessagingContinuesWhenPublisherNeverReady(t *testing.T) {
	log := logger.New("debug", true)
	fake := &fakeReadinessClient{} // never flips ready
	factory := func(string, logger.Logger) messaging.AMQPClient { return fake }
	manager := messaging.NewMessagingManager(&fakeBrokerURLProvider{url: "amqp://localhost"}, log, messaging.ManagerOptions{MaxPublishers: 5, IdleTTL: time.Hour}, factory)
	defer func() { _ = manager.Close() }()

	prewarmer := NewConnectionPreWarmer(log, nil, manager)

	// Force the "not ready in time" branch with a short ctx deadline rather than
	// waiting out the full 5s preWarmReadinessTimeout — same trick as
	// messaging/registry_test.go's TestRegistryDeclareInfrastructureClientNotReadyTimeoutSimple.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := prewarmer.PreWarmMessaging(ctx, testKey, nil)
	elapsed := time.Since(start)

	// Not-ready-in-time is a WARN, not a startup failure — pre-warm must not
	// propagate an error; PublishToExchange's own readytimeout pre-flight will
	// still absorb a slow first publish later.
	assert.NoError(t, err)
	assert.Less(t, elapsed, time.Second, "must return once ctx expires, not wait out the full preWarmReadinessTimeout")
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
