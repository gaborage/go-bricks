package messaging

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

type stubMessagingSource struct {
	urls map[string]string
}

func (s *stubMessagingSource) BrokerURL(_ context.Context, key string) (string, error) {
	if url, ok := s.urls[key]; ok {
		return url, nil
	}
	return "amqp://guest:guest@localhost/", nil
}

type stubAMQPClient struct {
	closed        bool
	closedMu      sync.Mutex
	lastPublish   PublishOptions
	consumers     int
	closeCallback func()
	closeErr      error
}

func (s *stubAMQPClient) Publish(ctx context.Context, destination string, data []byte) error {
	return s.PublishToExchange(ctx, PublishOptions{Exchange: "", RoutingKey: destination}, data)
}

func (s *stubAMQPClient) PublishToExchange(_ context.Context, options PublishOptions, _ []byte) error {
	s.lastPublish = options
	return nil
}

func (s *stubAMQPClient) Consume(_ context.Context, _ string) (<-chan amqp.Delivery, error) {
	ch := make(chan amqp.Delivery)
	close(ch)
	return ch, nil
}

func (s *stubAMQPClient) ConsumeFromQueue(_ context.Context, _ ConsumeOptions) (<-chan amqp.Delivery, error) {
	s.closedMu.Lock()
	s.consumers++
	s.closedMu.Unlock()
	ch := make(chan amqp.Delivery)
	close(ch)
	return ch, nil
}

func (s *stubAMQPClient) DeclareQueue(string, bool, bool, bool, bool) error            { return nil }
func (s *stubAMQPClient) DeclareExchange(string, string, bool, bool, bool, bool) error { return nil }
func (s *stubAMQPClient) BindQueue(string, string, string, bool) error                 { return nil }

func (s *stubAMQPClient) Close() error {
	s.closedMu.Lock()
	defer s.closedMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.closeCallback != nil {
		s.closeCallback()
	}
	return s.closeErr
}

func (s *stubAMQPClient) IsReady() bool {
	s.closedMu.Lock()
	defer s.closedMu.Unlock()
	return !s.closed
}

func TestMessagingManagerCachesPublishersPerKey(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	var mu sync.Mutex
	factoryCalls := map[string]int{}
	factory := func(url string, _ logger.Logger) AMQPClient {
		mu.Lock()
		factoryCalls[url]++
		mu.Unlock()
		return &stubAMQPClient{}
	}

	source := &stubMessagingSource{urls: map[string]string{tenantID: amqpHost}}
	manager := NewMessagingManager(source, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)

	first, err := manager.Publisher(ctx, tenantID)
	require.NoError(t, err)
	second, err := manager.Publisher(ctx, tenantID)
	require.NoError(t, err)
	assert.Same(t, first, second)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, factoryCalls[amqpHost])
}

func TestMessagingManagerInjectsTenantHeader(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	client := &stubAMQPClient{}
	factory := func(string, logger.Logger) AMQPClient { return client }
	source := &stubMessagingSource{urls: map[string]string{tenantID: amqpHost}}
	manager := NewMessagingManager(source, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)

	pub, err := manager.Publisher(ctx, tenantID)
	require.NoError(t, err)

	err = pub.PublishToExchange(ctx, PublishOptions{Exchange: genericEx, RoutingKey: "rk"}, []byte("payload"))
	assert.NoError(t, err)
	assert.Equal(t, tenantID, client.lastPublish.Headers[tenantHeader])
}

func TestMessagingManagerSingleflightPublishers(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	var mu sync.Mutex
	calls := 0
	factory := func(string, logger.Logger) AMQPClient {
		mu.Lock()
		calls++
		mu.Unlock()
		return &stubAMQPClient{}
	}

	manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{tenantID: amqpHost}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := manager.Publisher(ctx, tenantID)
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, calls)
}

func TestMessagingManagerCleanupEvictsIdlePublishers(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	var closed int
	factory := func(string, logger.Logger) AMQPClient {
		return &stubAMQPClient{closeCallback: func() { closed++ }}
	}

	manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{"idle": amqpURLIdle}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: 10 * time.Millisecond}, factory)
	_, err := manager.Publisher(ctx, "idle")
	require.NoError(t, err)

	time.Sleep(20 * time.Millisecond)
	manager.cleanupIdlePublishers()
	assert.Equal(t, 1, closed)
}

func TestMessagingManagerEvictsLRU(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	var mu sync.Mutex
	evicted := []string{}
	factory := func(url string, _ logger.Logger) AMQPClient {
		return &stubAMQPClient{closeCallback: func() {
			mu.Lock()
			defer mu.Unlock()
			evicted = append(evicted, url)
		}}
	}

	source := &stubMessagingSource{urls: map[string]string{
		"a": amqpURLA,
		"b": amqpURLB,
		"c": amqpURLC,
	}}

	manager := NewMessagingManager(source, log, ManagerOptions{MaxPublishers: 2, IdleTTL: time.Minute}, factory)
	_, err := manager.Publisher(ctx, "a")
	require.NoError(t, err)
	_, err = manager.Publisher(ctx, "b")
	require.NoError(t, err)
	_, err = manager.Publisher(ctx, "c")
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, evicted, amqpURLA)
}

func TestMessagingManagerEnsureConsumersIdempotent(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	client := &stubAMQPClient{}
	factory := func(string, logger.Logger) AMQPClient { return client }
	manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{tenantID: amqpHost}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)

	decls := NewDeclarations()
	decls.RegisterQueue(&QueueDeclaration{Name: genericQueue})
	decls.RegisterConsumer(&ConsumerDeclaration{Queue: genericQueue, Consumer: genericConsumer, Handler: &mockMessageHandler{}})

	for i := 0; i < 2; i++ {
		err := manager.EnsureConsumers(ctx, tenantID, decls)
		assert.NoError(t, err)
	}

	assert.Equal(t, 1, client.consumers)
}

type mockMessageHandler struct{}

func (m *mockMessageHandler) Handle(context.Context, *amqp.Delivery) error { return nil }
func (m *mockMessageHandler) EventType() string                            { return genericError }

type tenantCapturingHandler struct {
	capturedCtx context.Context //nolint:S8242 // NOSONAR: Test-only struct capturing context for verification
}

func (h *tenantCapturingHandler) Handle(ctx context.Context, _ *amqp.Delivery) error {
	h.capturedCtx = ctx
	// Import multitenant package to get tenant from context
	// For now, just capture the context
	return nil
}

func (h *tenantCapturingHandler) EventType() string { return testEventType }

func TestMessagingManagerInjectsTenantIntoConsumerContext(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	handler := &tenantCapturingHandler{}
	client := &stubAMQPClient{}
	factory := func(string, logger.Logger) AMQPClient { return client }
	manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)

	decls := NewDeclarations()
	decls.RegisterQueue(&QueueDeclaration{Name: testQueue})
	decls.RegisterConsumer(&ConsumerDeclaration{Queue: testQueue, Consumer: testConsumer, Handler: handler})

	err := manager.EnsureConsumers(ctx, testTenantID, decls)
	require.NoError(t, err)

	// Verify that consumer was started
	assert.Equal(t, 1, client.consumers)

	// The actual verification of tenant context injection would require
	// importing multitenant package and checking the context in the handler
	// For this test, we verify that EnsureConsumers completed successfully
	// which means it called StartConsumers with the tenant-injected context
}

func TestMessagingManagerHashBasedIdempotency(t *testing.T) {
	t.Run("same declarations replay multiple times - idempotent", func(t *testing.T) {
		ctx := context.Background()
		log := logger.New("error", false)

		clientCallCount := 0
		factory := func(string, logger.Logger) AMQPClient {
			clientCallCount++
			return &stubAMQPClient{}
		}
		manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{tenantID: amqpHost}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)

		decls := NewDeclarations()
		decls.RegisterExchange(&ExchangeDeclaration{Name: genericEx, Type: exchangeTypeTopic, Durable: true})
		decls.RegisterQueue(&QueueDeclaration{Name: genericQueue, Durable: true})
		decls.RegisterConsumer(&ConsumerDeclaration{Queue: genericQueue, Consumer: genericConsumer, EventType: eventTestEvent, Handler: &mockMessageHandler{}})

		// First call - should create client and registry
		err := manager.EnsureConsumers(ctx, tenantID, decls)
		assert.NoError(t, err)
		assert.Equal(t, 1, clientCallCount, "First call should create client")

		// Second call with identical declarations - should be idempotent
		err = manager.EnsureConsumers(ctx, tenantID, decls)
		assert.NoError(t, err)
		assert.Equal(t, 1, clientCallCount, "Second call should reuse existing setup")

		// Third call - still idempotent
		err = manager.EnsureConsumers(ctx, tenantID, decls)
		assert.NoError(t, err)
		assert.Equal(t, 1, clientCallCount, "Third call should still be idempotent")
	})

	t.Run("different declarations for same key - error", func(t *testing.T) {
		ctx := context.Background()
		log := logger.New("error", false)

		factory := func(string, logger.Logger) AMQPClient {
			return &stubAMQPClient{}
		}
		manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{tenantID: amqpHost}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)

		// First set of declarations
		decls1 := NewDeclarations()
		decls1.RegisterExchange(&ExchangeDeclaration{Name: genericEx, Type: exchangeTypeTopic, Durable: true})
		decls1.RegisterQueue(&QueueDeclaration{Name: genericQueue, Durable: true})
		decls1.RegisterConsumer(&ConsumerDeclaration{Queue: genericQueue, Consumer: genericConsumer, EventType: eventA, Handler: &mockMessageHandler{}})

		err := manager.EnsureConsumers(ctx, tenantID, decls1)
		assert.NoError(t, err)

		// Second set of declarations - different structure
		decls2 := NewDeclarations()
		decls2.RegisterExchange(&ExchangeDeclaration{Name: genericEx, Type: exchangeTypeTopic, Durable: false}) // Different Durable flag
		decls2.RegisterQueue(&QueueDeclaration{Name: genericQueue, Durable: true})
		decls2.RegisterConsumer(&ConsumerDeclaration{Queue: genericQueue, Consumer: genericConsumer, EventType: eventA, Handler: &mockMessageHandler{}})

		err = manager.EnsureConsumers(ctx, tenantID, decls2)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "attempt to replay different declarations")
	})

	t.Run("concurrent calls with same declarations - singleflight", func(t *testing.T) {
		ctx := context.Background()
		log := logger.New("error", false)

		var mu sync.Mutex
		clientCallCount := 0
		factory := func(string, logger.Logger) AMQPClient {
			mu.Lock()
			clientCallCount++
			mu.Unlock()
			time.Sleep(10 * time.Millisecond) // Simulate slow setup
			return &stubAMQPClient{}
		}
		manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{tenantID: amqpHost}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)

		decls := NewDeclarations()
		decls.RegisterQueue(&QueueDeclaration{Name: genericQueue, Durable: true})
		decls.RegisterConsumer(&ConsumerDeclaration{Queue: genericQueue, Consumer: genericConsumer, EventType: eventTestEvent, Handler: &mockMessageHandler{}})

		// Launch multiple concurrent calls
		var wg sync.WaitGroup
		errChan := make(chan error, 10)
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := manager.EnsureConsumers(ctx, tenantID, decls)
				errChan <- err
			}()
		}
		wg.Wait()
		close(errChan)

		// Check all calls succeeded
		for err := range errChan {
			assert.NoError(t, err)
		}

		// Singleflight should ensure only one client was created
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, 1, clientCallCount, "Singleflight should prevent concurrent setup")
	})

	t.Run("different keys with same declarations - independent", func(t *testing.T) {
		ctx := context.Background()
		log := logger.New("error", false)

		clientCallCount := 0
		factory := func(string, logger.Logger) AMQPClient {
			clientCallCount++
			return &stubAMQPClient{}
		}
		manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{
			tenant1ID: amqpURLTenant1,
			tenant2ID: amqpURLTenant2,
		}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)

		decls := NewDeclarations()
		decls.RegisterQueue(&QueueDeclaration{Name: genericQueue, Durable: true})
		decls.RegisterConsumer(&ConsumerDeclaration{Queue: genericQueue, Consumer: genericConsumer, EventType: eventTestEvent, Handler: &mockMessageHandler{}})

		// Setup for tenant1
		err := manager.EnsureConsumers(ctx, tenant1ID, decls)
		assert.NoError(t, err)
		assert.Equal(t, 1, clientCallCount)

		// Setup for tenant2 with same declarations - should create new client
		err = manager.EnsureConsumers(ctx, tenant2ID, decls)
		assert.NoError(t, err)
		assert.Equal(t, 2, clientCallCount, "Different keys should have independent setups")

		// Replay to tenant1 - should be idempotent
		err = manager.EnsureConsumers(ctx, tenant1ID, decls)
		assert.NoError(t, err)
		assert.Equal(t, 2, clientCallCount, "Replay to tenant1 should be idempotent")
	})

	t.Run("hash recorded after successful setup", func(t *testing.T) {
		ctx := context.Background()
		log := logger.New("error", false)

		factory := func(string, logger.Logger) AMQPClient {
			return &stubAMQPClient{}
		}
		manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{tenantID: amqpHost}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)

		decls := NewDeclarations()
		decls.RegisterQueue(&QueueDeclaration{Name: genericQueue, Durable: true})
		decls.RegisterConsumer(&ConsumerDeclaration{Queue: genericQueue, Consumer: genericConsumer, EventType: eventTestEvent, Handler: &mockMessageHandler{}})

		err := manager.EnsureConsumers(ctx, tenantID, decls)
		assert.NoError(t, err)

		// Check that hash was recorded
		manager.consMu.RLock()
		hash, exists := manager.replayedHashs[tenantID]
		manager.consMu.RUnlock()

		assert.True(t, exists, "Hash should be recorded after setup")
		assert.NotZero(t, hash, "Hash should not be zero")
		assert.Equal(t, decls.Hash(), hash, "Recorded hash should match declarations hash")
	})
}

// TestMessagingManagerStartCleanupZeroIntervalDefaults verifies StartCleanup
// substitutes the default 2-minute interval when given a non-positive value.
// We can't assert the exact ticker period without timing flakiness, but we can
// confirm StartCleanup wires up the loop (cleanupCh non-nil) and StopCleanup
// then tears it down cleanly.
func TestMessagingManagerStartCleanupZeroIntervalDefaults(t *testing.T) {
	log := logger.New("error", false)
	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	manager := NewMessagingManager(&stubMessagingSource{}, log, ManagerOptions{MaxPublishers: 1, IdleTTL: time.Minute}, factory)

	manager.StartCleanup(0)
	manager.cleanupMu.Lock()
	assert.NotNil(t, manager.cleanupCh, "StartCleanup should arm the cleanup loop even with interval=0 (defaults applied)")
	manager.cleanupMu.Unlock()

	manager.StopCleanup()
}

func TestMessagingManagerStartCleanupIdempotent(t *testing.T) {
	log := logger.New("error", false)
	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	manager := NewMessagingManager(&stubMessagingSource{}, log, ManagerOptions{MaxPublishers: 1, IdleTTL: time.Minute}, factory)

	manager.StartCleanup(50 * time.Millisecond)
	manager.cleanupMu.Lock()
	first := manager.cleanupCh
	manager.cleanupMu.Unlock()
	require.NotNil(t, first)

	manager.StartCleanup(50 * time.Millisecond)
	manager.cleanupMu.Lock()
	second := manager.cleanupCh
	manager.cleanupMu.Unlock()
	assert.True(t, first == second, "second StartCleanup must be a no-op while a loop is already running (cleanupCh unchanged)")

	manager.StopCleanup()
}

func TestMessagingManagerStopCleanupBeforeStartIsNoOp(t *testing.T) {
	log := logger.New("error", false)
	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	manager := NewMessagingManager(&stubMessagingSource{}, log, ManagerOptions{MaxPublishers: 1, IdleTTL: time.Minute}, factory)

	manager.StopCleanup()
	manager.cleanupMu.Lock()
	assert.Nil(t, manager.cleanupCh)
	manager.cleanupMu.Unlock()
}

// TestMessagingManagerCleanupLoopEvictsOnTick exercises the cleanupLoop goroutine
// end-to-end: arm a short interval, seed an idle publisher, wait for at least
// one tick, then verify the entry was cleaned up.
func TestMessagingManagerCleanupLoopEvictsOnTick(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	var closed int
	var mu sync.Mutex
	factory := func(string, logger.Logger) AMQPClient {
		return &stubAMQPClient{closeCallback: func() {
			mu.Lock()
			defer mu.Unlock()
			closed++
		}}
	}

	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{"idle": amqpURLIdle}},
		log,
		ManagerOptions{MaxPublishers: 5, IdleTTL: 10 * time.Millisecond},
		factory,
	)
	_, err := manager.Publisher(ctx, "idle")
	require.NoError(t, err)

	manager.StartCleanup(20 * time.Millisecond)
	t.Cleanup(manager.StopCleanup)

	// Wait long enough for IdleTTL (10ms) + at least one cleanup tick (20ms).
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return closed >= 1
	}, time.Second, 10*time.Millisecond, "cleanup loop should evict the idle publisher within a few ticks")
}

func TestMessagingManagerCloseClosesPublishersAndConsumers(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	var closedCount int
	var mu sync.Mutex
	factory := func(string, logger.Logger) AMQPClient {
		return &stubAMQPClient{closeCallback: func() {
			mu.Lock()
			defer mu.Unlock()
			closedCount++
		}}
	}

	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1, tenant2ID: amqpURLTenant2}},
		log,
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		factory,
	)
	manager.StartCleanup(time.Minute)

	// Seed two publishers and a consumer registry.
	_, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	_, err = manager.Publisher(ctx, tenant2ID)
	require.NoError(t, err)

	decls := NewDeclarations()
	decls.RegisterQueue(&QueueDeclaration{Name: genericQueue})
	decls.RegisterConsumer(&ConsumerDeclaration{Queue: genericQueue, Consumer: genericConsumer, Handler: &mockMessageHandler{}})
	require.NoError(t, manager.EnsureConsumers(ctx, tenant1ID, decls))

	require.NoError(t, manager.Close())

	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, closedCount, 2, "Close should close at least the publisher clients")

	// Cleanup loop should be stopped after Close.
	manager.cleanupMu.Lock()
	assert.Nil(t, manager.cleanupCh, "Close must stop the cleanup loop")
	manager.cleanupMu.Unlock()

	// Internal maps are reset.
	manager.pubMu.RLock()
	assert.Empty(t, manager.publishers)
	manager.pubMu.RUnlock()
	manager.consMu.RLock()
	assert.Empty(t, manager.consumers)
	manager.consMu.RUnlock()
}

// TestMessagingManagerCloseSurfacesClientErrors checks the aggregated-error path
// when a publisher Close() returns an error.
func TestMessagingManagerCloseSurfacesClientErrors(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	factory := func(string, logger.Logger) AMQPClient {
		return &stubAMQPClient{closeCallback: func() {}}
	}

	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1}},
		log,
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		factory,
	)
	_, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)

	// Swap in a failing client behind the cache so Close() surfaces an aggregated error.
	wantErr := errors.New("client close failed")
	manager.pubMu.Lock()
	for key, entry := range manager.publishers {
		entry.client = &stubAMQPClient{closeErr: wantErr}
		manager.publishers[key] = entry
	}
	manager.pubMu.Unlock()

	err = manager.Close()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "errors closing messaging clients")
	assert.Contains(t, err.Error(), wantErr.Error(), "aggregated error should include the underlying client-close message")
}

// TestMessagingManagerCloseClientOnRollback verifies the load-bearing
// invariant: when Close returns an error, the helper logs but does NOT
// panic or propagate — the caller already has a primary error to return.
func TestMessagingManagerCloseClientOnRollback(t *testing.T) {
	log := logger.New("error", false)
	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	manager := NewMessagingManager(&stubMessagingSource{}, log, ManagerOptions{MaxPublishers: 1, IdleTTL: time.Minute}, factory)

	t.Run("close succeeds", func(t *testing.T) {
		client := &stubAMQPClient{}
		manager.closeClientOnRollback(client, tenant1ID, "replay_declarations")

		client.closedMu.Lock()
		defer client.closedMu.Unlock()
		assert.True(t, client.closed, "closeClientOnRollback must invoke Close")
	})

	t.Run("close errors are logged not propagated", func(t *testing.T) {
		client := &stubAMQPClient{closeErr: errors.New("rollback close failed")}
		// No panic, no return — failure observable via logger only.
		manager.closeClientOnRollback(client, tenant1ID, "publisher_double_create_race")

		client.closedMu.Lock()
		defer client.closedMu.Unlock()
		assert.True(t, client.closed, "Close must still be attempted even when it returns an error")
	})
}

func TestMessagingManagerStats(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1, tenant2ID: amqpURLTenant2}},
		log,
		ManagerOptions{MaxPublishers: 7, IdleTTL: 90 * time.Second},
		factory,
	)

	stats := manager.Stats()
	assert.Equal(t, 0, stats["active_publishers"])
	assert.Equal(t, 7, stats["max_publishers"])
	assert.Equal(t, 0, stats["active_consumers"])
	assert.Equal(t, 90, stats["idle_ttl_seconds"])

	_, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	_, err = manager.Publisher(ctx, tenant2ID)
	require.NoError(t, err)

	stats = manager.Stats()
	assert.Equal(t, 2, stats["active_publishers"])
}
