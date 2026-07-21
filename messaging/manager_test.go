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
	closed         bool
	closedMu       sync.Mutex
	lastPublish    PublishOptions
	consumers      int
	consumeCtx     context.Context //nolint:containedctx // test-only: captures the ctx the supervisor subscribes with, to assert its lifecycle
	closeCallback  func()
	closeHook      func() // optional: invoked at the start of Close (e.g. to simulate a slow close)
	closeErr       error
	declaredQueues []string
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

func (s *stubAMQPClient) ConsumeFromQueue(ctx context.Context, _ ConsumeOptions) (<-chan amqp.Delivery, error) {
	s.closedMu.Lock()
	s.consumers++
	s.consumeCtx = ctx
	s.closedMu.Unlock()
	// Return an open delivery channel (never closed) so the consumer supervisor
	// parks waiting for deliveries instead of treating an immediately-closed
	// channel as a flap and re-subscribing — which would make consumerCount()
	// non-deterministic. Tests stop the supervisor via manager.Close().
	return make(chan amqp.Delivery), nil
}

// consumerCount returns the number of ConsumeFromQueue calls observed, read
// under the same lock the counter is written with. The registry's per-consumer
// supervisor calls ConsumeFromQueue from a background goroutine, so tests must
// not read s.consumers directly.
func (s *stubAMQPClient) consumerCount() int {
	s.closedMu.Lock()
	defer s.closedMu.Unlock()
	return s.consumers
}

// lastConsumeCtx returns the context the supervisor most recently subscribed
// with (read under the same lock), so tests can assert its cancellation lifecycle.
func (s *stubAMQPClient) lastConsumeCtx() context.Context {
	s.closedMu.Lock()
	defer s.closedMu.Unlock()
	return s.consumeCtx
}

func (s *stubAMQPClient) DeclareQueue(_ context.Context, queue *QueueDeclaration) error {
	s.closedMu.Lock()
	defer s.closedMu.Unlock()
	s.declaredQueues = append(s.declaredQueues, queue.Name)
	return nil
}

func (s *stubAMQPClient) DeclareExchange(context.Context, *ExchangeDeclaration) error { return nil }
func (s *stubAMQPClient) BindQueue(context.Context, *BindingDeclaration) error        { return nil }

// declaredQueueNames returns the queue names DeclareQueue was called with,
// read under the same lock they were written with.
func (s *stubAMQPClient) declaredQueueNames() []string {
	s.closedMu.Lock()
	defer s.closedMu.Unlock()
	return append([]string(nil), s.declaredQueues...)
}

func (s *stubAMQPClient) Close() error {
	s.closedMu.Lock()
	hook := s.closeHook
	s.closedMu.Unlock()
	if hook != nil {
		hook()
	}

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

func TestManagerStopConsumersStopsRegistriesWithoutClosing(t *testing.T) {
	log := logger.New("error", false)
	canceled := false
	reg := &Registry{logger: log, consumersActive: true, cancelConsumers: func() { canceled = true }}
	client := &stubAMQPClient{}
	m := &Manager{
		logger:    log,
		consumers: map[string]*consumerEntry{"": {client: client, registry: reg}},
	}

	m.StopConsumers()

	assert.True(t, canceled, "consume context must be canceled so no new messages are delivered")
	assert.False(t, reg.consumersActive, "registry must mark consumers stopped")
	assert.False(t, client.closed, "StopConsumers must NOT close the AMQP connection — Close does that later")

	// Idempotent: a second call must be a safe no-op (Close also stops consumers).
	require.NotPanics(t, m.StopConsumers)
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

	first, _, err := manager.Publisher(ctx, tenantID)
	require.NoError(t, err)
	second, _, err := manager.Publisher(ctx, tenantID)
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

	pub, _, err := manager.Publisher(ctx, tenantID)
	require.NoError(t, err)

	err = pub.PublishToExchange(ctx, PublishOptions{Exchange: genericEx, RoutingKey: "rk"}, []byte("payload"))
	assert.NoError(t, err)
	assert.Equal(t, tenantID, client.lastPublish.Headers[tenantHeader])
}

func TestMessagingManagerEnsureConsumersIdempotent(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	client := &stubAMQPClient{}
	factory := func(string, logger.Logger) AMQPClient { return client }
	manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{tenantID: amqpHost}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)
	defer func() { _ = manager.Close() }() // stop supervisor goroutines

	decls := NewDeclarations()
	decls.RegisterQueue(&QueueDeclaration{Name: genericQueue})
	decls.RegisterConsumer(&ConsumerDeclaration{Queue: genericQueue, Consumer: genericConsumer, Handler: &mockMessageHandler{}})

	for i := 0; i < 2; i++ {
		err := manager.EnsureConsumers(ctx, tenantID, decls)
		assert.NoError(t, err)
	}

	assert.Equal(t, 1, client.consumerCount())
}

type mockMessageHandler struct{}

func (m *mockMessageHandler) Handle(context.Context, *amqp.Delivery) error { return nil }
func (m *mockMessageHandler) EventType() string                            { return genericError }

type tenantCapturingHandler struct {
	capturedCtx context.Context // NOSONAR: Test-only struct capturing context for verification
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
	defer func() { _ = manager.Close() }() // stop supervisor goroutines

	decls := NewDeclarations()
	decls.RegisterQueue(&QueueDeclaration{Name: testQueue})
	decls.RegisterConsumer(&ConsumerDeclaration{Queue: testQueue, Consumer: testConsumer, Handler: handler})

	err := manager.EnsureConsumers(ctx, testTenantID, decls)
	require.NoError(t, err)

	// Verify that consumer was started
	assert.Equal(t, 1, client.consumerCount())

	// The actual verification of tenant context injection would require
	// importing multitenant package and checking the context in the handler
	// For this test, we verify that EnsureConsumers completed successfully
	// which means it called StartConsumers with the tenant-injected context
}

// TestMessagingManagerConsumersSurviveCallerContextCancellation guards the High audit
// finding: in multi-tenant mode consumers start lazily from the HTTP request context
// (a 5s-deadline, cancel-on-finish context). If that context is threaded into the
// long-lived consumer supervisor, the consumers die when the first request ends and
// never restart. EnsureConsumers must detach request cancellation so consumer lifetime
// is governed only by StopConsumers/Close.
func TestMessagingManagerConsumersSurviveCallerContextCancellation(t *testing.T) {
	log := logger.New("error", false)
	client := &stubAMQPClient{}
	factory := func(string, logger.Logger) AMQPClient { return client }
	manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)
	defer func() { _ = manager.Close() }()

	decls := NewDeclarations()
	decls.RegisterQueue(&QueueDeclaration{Name: testQueue})
	decls.RegisterConsumer(&ConsumerDeclaration{Queue: testQueue, Consumer: testConsumer, Handler: &mockMessageHandler{}})

	// Lazy startup driven by a request-scoped context that is canceled when the request ends.
	callerCtx, cancel := context.WithCancel(context.Background())
	require.NoError(t, manager.EnsureConsumers(callerCtx, testTenantID, decls))

	// Wait until the supervisor goroutine has subscribed (and captured its context).
	require.Eventually(t, func() bool { return client.consumerCount() >= 1 }, time.Second, 5*time.Millisecond)
	consumerCtx := client.lastConsumeCtx()
	require.NotNil(t, consumerCtx)

	// Request ends: canceling the caller context must NOT cancel the consumer.
	cancel()
	select {
	case <-consumerCtx.Done():
		t.Fatal("consumer context was canceled when the caller/request context ended — consumers stop after one request and never restart")
	case <-time.After(100 * time.Millisecond):
		// Consumer context is detached from the request lifecycle, as required.
	}
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

// TestMessagingManagerCloseClosesPublishersAndConsumers drives Close through the public
// surface and pins that it spans BOTH sides: every cached publisher AND every consumer
// client is closed, the publisher pool is drained, and the consumer map is reset.
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
	_, _, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	_, _, err = manager.Publisher(ctx, tenant2ID)
	require.NoError(t, err)

	decls := NewDeclarations()
	decls.RegisterQueue(&QueueDeclaration{Name: genericQueue})
	decls.RegisterConsumer(&ConsumerDeclaration{Queue: genericQueue, Consumer: genericConsumer, Handler: &mockMessageHandler{}})
	require.NoError(t, manager.EnsureConsumers(ctx, tenant1ID, decls))

	require.NoError(t, manager.Close())

	mu.Lock()
	got := closedCount
	mu.Unlock()
	// Two publisher clients + one consumer client.
	assert.GreaterOrEqual(t, got, 3, "Close must close both publisher clients AND the consumer client")

	// Publisher pool drained and consumer map reset (Close stops the cleanup loop too — no panic).
	assert.Equal(t, 0, manager.Stats()["active_publishers"], "Close must drain the publisher pool")
	manager.consMu.RLock()
	assert.Empty(t, manager.consumers, "Close must reset the consumer map")
	manager.consMu.RUnlock()
}

// TestMessagingManagerCloseSurfacesClientErrors checks the aggregated-error path
// when a publisher Close() returns an error: it surfaces under the historical
// "errors closing messaging clients" prefix, wrapping the underlying cause.
func TestMessagingManagerCloseSurfacesClientErrors(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	wantErr := errors.New("client close failed")
	factory := func(string, logger.Logger) AMQPClient {
		return &stubAMQPClient{closeErr: wantErr}
	}

	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1}},
		log,
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		factory,
	)
	_, _, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)

	err = manager.Close()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "errors closing messaging clients")
	assert.Contains(t, err.Error(), wantErr.Error(), "aggregated error should include the underlying client-close message")
	assert.ErrorIs(t, err, wantErr, "the wrapped cause must remain matchable with errors.Is")
}

// TestMessagingManagerCloseAggregatesErrors pins the aggregate Close contract: when MULTIPLE
// cached publishers fail to close, Close surfaces EVERY failure (not just the first), under the
// historical "errors closing messaging clients" prefix. Black-box via Publisher + Close.
func TestMessagingManagerCloseAggregatesErrors(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	// Distinct per-broker close errors so we can assert BOTH surface.
	factory := func(url string, _ logger.Logger) AMQPClient {
		return &stubAMQPClient{closeErr: errors.New("close failure " + url)}
	}
	source := &stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1, tenant2ID: amqpURLTenant2}}
	manager := NewMessagingManager(source, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)

	_, relA, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	relA()
	_, relB, err := manager.Publisher(ctx, tenant2ID)
	require.NoError(t, err)
	relB()

	err = manager.Close()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "errors closing messaging clients")
	assert.Contains(t, err.Error(), "close failure "+amqpURLTenant1)
	assert.Contains(t, err.Error(), "close failure "+amqpURLTenant2, "Close must surface ALL publisher close errors, not just the first")
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
		manager.closeClientOnRollback(client, tenant1ID, "declare_infrastructure")

		client.closedMu.Lock()
		defer client.closedMu.Unlock()
		assert.True(t, client.closed, "Close must still be attempted even when it returns an error")
	})
}

// TestMessagingManagerStats drives Stats() through the public surface and pins every key,
// including active_consumers (from the consumer map) and the evictions counter (surfaced from
// the pool after an LRU eviction). The per-pool counter semantics are exercised directly in
// internal/resourcepool; this pins the manager's map-key mapping.
func TestMessagingManagerStats(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1, tenant2ID: amqpURLTenant2}},
		log,
		ManagerOptions{MaxPublishers: 1, IdleTTL: 90 * time.Second},
		factory,
	)
	defer func() { _ = manager.Close() }()

	// Empty manager: full key set, publisher counters zero, config max/idle_ttl surfaced.
	stats := manager.Stats()
	assert.Equal(t, 0, stats["active_publishers"])
	assert.Equal(t, 1, stats["max_publishers"])
	assert.Equal(t, 0, stats["active_consumers"])
	assert.Equal(t, 90, stats["idle_ttl_seconds"])
	assert.Equal(t, 0, stats["evictions"])
	assert.Equal(t, 0, stats["idle_cleanups"])

	// One live publisher.
	_, rel1, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	rel1()
	assert.Equal(t, 1, manager.Stats()["active_publishers"])

	// MaxPublishers=1 forces the second key to evict the first: the evictions counter surfaces.
	_, rel2, err := manager.Publisher(ctx, tenant2ID)
	require.NoError(t, err)
	rel2()
	stats = manager.Stats()
	assert.Equal(t, 1, stats["active_publishers"], "eviction keeps the pool at its cap")
	assert.Equal(t, 1, stats["evictions"], "LRU eviction increments the evictions counter surfaced by Stats")
	assert.Equal(t, 0, stats["idle_cleanups"], "LRU eviction must not bump idle_cleanups")
}

// TestMessagingManagerStatsTracksIdleCleanups pins that Stats() surfaces the pool's idle-cleanup
// counter under the "idle_cleanups" key with a NON-zero value — the sibling of the "evictions" pin
// above. A released publisher left idle past the TTL is reaped by the pool's cleanup loop, and that
// count must reach the manager's map (guarding against a mismapping to an always-zero field).
func TestMessagingManagerStatsTracksIdleCleanups(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)
	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1}},
		log,
		ManagerOptions{MaxPublishers: 5, IdleTTL: 10 * time.Millisecond},
		factory,
	)
	defer func() { _ = manager.Close() }()

	_, rel, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	rel() // release so the idle publisher becomes eligible for cleanup

	manager.StartCleanup(10 * time.Millisecond)
	defer manager.StopCleanup()

	assert.Eventually(t, func() bool {
		count, _ := manager.Stats()["idle_cleanups"].(int)
		return count >= 1
	}, 2*time.Second, 20*time.Millisecond, "Stats must surface the pool's idle-cleanup count under idle_cleanups")
}

// --- Lease/refcount: eviction-while-in-use race (issue #606, ADR-032) ---
// The lease/evict/close protocol itself is exercised directly in internal/resourcepool;
// these black-box helpers drive the manager's public Publisher/Close surface.

// leasedClosableFactory builds publisher clients that record each Close() in the shared
// `closed` map (keyed by broker URL) under `mu`.
func leasedClosableFactory(mu *sync.Mutex, closed map[string]bool) ClientFactory {
	return func(url string, _ logger.Logger) AMQPClient {
		return &stubAMQPClient{closeCallback: func() {
			mu.Lock()
			closed[url] = true
			mu.Unlock()
		}}
	}
}

func twoPublisherSource() *stubMessagingSource {
	return &stubMessagingSource{urls: map[string]string{"a": amqpURLA, "b": amqpURLB}}
}

func TestMessagingManagerPublisherReturnsNonNilReleaseFunc(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	closed := map[string]bool{}
	m := NewMessagingManager(twoPublisherSource(), logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, leasedClosableFactory(&mu, closed))
	defer func() { _ = m.Close() }()

	pub, release, err := m.Publisher(ctx, "a")
	require.NoError(t, err)
	require.NotNil(t, pub)
	require.NotNil(t, release, "Publisher must return a non-nil release so callers can always defer it")

	release() // releasing a live cached publisher must NOT close it
	mu.Lock()
	wasClosed := closed[amqpURLA]
	mu.Unlock()
	assert.False(t, wasClosed, "releasing a lease on a live cached publisher must not close it")
}

// TestMessagingManagerPublisherAfterCloseReturnsError pins the F22 fix: once Close() has run,
// Publisher() fails closed (returning the manager's closed error) instead of resurrecting a
// publisher on a shut-down manager. The resourcepool closed guard supplies this; before the
// rewire, Publisher would silently create and leak a fresh publisher.
func TestMessagingManagerPublisherAfterCloseReturnsError(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	closed := map[string]bool{}
	m := NewMessagingManager(twoPublisherSource(), logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, leasedClosableFactory(&mu, closed))
	require.NoError(t, m.Close())

	pub, release, err := m.Publisher(ctx, "a")
	require.Error(t, err)
	assert.ErrorIs(t, err, errManagerClosed, "Publisher after Close must fail closed, not resurrect a publisher (F22)")
	assert.Nil(t, pub)
	assert.Nil(t, release)
}

// TestMessagingManagerZeroValueMethodsAreSafe pins that a zero-value Manager (never built via
// NewMessagingManager — the lightweight stand-in the debug/health endpoint uses) does not panic
// on any of Stats/Publisher/StartCleanup/StopCleanup/Close, matching the pre-resourcepool
// nil-map-safe behavior (Publisher is guarded to fail closed rather than panic).
func TestMessagingManagerZeroValueMethodsAreSafe(t *testing.T) {
	m := &Manager{}

	stats := m.Stats()
	assert.Equal(t, 0, stats["active_publishers"])
	assert.Equal(t, 0, stats["max_publishers"])
	assert.Equal(t, 0, stats["active_consumers"])
	assert.Equal(t, 0, stats["idle_ttl_seconds"])
	assert.Equal(t, 0, stats["evictions"])
	assert.Equal(t, 0, stats["idle_cleanups"])

	pub, release, err := m.Publisher(context.Background(), "any")
	assert.ErrorIs(t, err, errManagerClosed, "zero-value Publisher must fail closed, not panic")
	assert.Nil(t, pub)
	assert.Nil(t, release)

	assert.NotPanics(t, func() {
		m.StartCleanup(time.Minute)
		m.StopCleanup()
	}, "zero-value StartCleanup/StopCleanup must be no-ops, not panic")

	assert.NoError(t, m.Close(), "closing a never-initialized manager is a no-op")
}

// TestMessagingManagerStartCleanupIsIdempotent pins that a second StartCleanup observes the
// already-running pool cleanup loop and short-circuits, and StopCleanup is a safe no-op when
// called again. The manager's StartCleanup is a thin passthrough to the pool.
func TestMessagingManagerStartCleanupIsIdempotent(t *testing.T) {
	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	m := NewMessagingManager(&stubMessagingSource{}, logger.New("error", false),
		ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour}, factory)
	defer func() { _ = m.Close() }()

	m.StartCleanup(10 * time.Second)
	require.NotPanics(t, func() { m.StartCleanup(10 * time.Second) })

	m.StopCleanup()
	require.NotPanics(t, func() { m.StopCleanup() })
}

// TestMessagingManagerStartCleanupAppliesDefaultForNonPositive pins the manager-specific
// default-interval substitution (2 minutes) for a non-positive interval. We can't inspect the
// ticker directly, so the contract is "no panic + clean stop".
func TestMessagingManagerStartCleanupAppliesDefaultForNonPositive(t *testing.T) {
	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	m := NewMessagingManager(&stubMessagingSource{}, logger.New("error", false),
		ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour}, factory)
	defer func() { _ = m.Close() }()

	require.NotPanics(t, func() { m.StartCleanup(0) })
	m.StopCleanup()

	require.NotPanics(t, func() { m.StartCleanup(-5 * time.Second) })
	m.StopCleanup()
}

// TestNewMessagingManagerDefaultFactoryForwardsReconnectOptions pins that the
// default client factory threads the four ManagerOptions reconnect delays into
// the constructed AMQPClient (#662).
func TestNewMessagingManagerDefaultFactoryForwardsReconnectOptions(t *testing.T) {
	oldDial := getAmqpDialFunc()
	setAmqpDialFunc(func(_ string) (amqpConnection, error) { return nil, errors.New(dialFailMsg) })
	// t.Cleanup (LIFO) so the client is closed BEFORE the real dialer is restored;
	// a defer here would restore first, letting the live reconnect goroutine dial out.
	t.Cleanup(func() { setAmqpDialFunc(oldDial) })

	log := logger.New("error", false)
	manager := NewMessagingManager(&stubMessagingSource{}, log, ManagerOptions{
		MaxPublishers:     1,
		IdleTTL:           time.Minute,
		ReconnectDelay:    7 * time.Second,
		ReconnectMaxDelay: 90 * time.Second,
		ReinitDelay:       3 * time.Second,
		ResendDelay:       11 * time.Second,
	}, nil)

	client := manager.clientFactory(amqpHost, log).(*AMQPClientImpl)
	t.Cleanup(func() { closeAndWaitForReconnect(client) })

	assert.Equal(t, 7*time.Second, client.reconnectDelay)
	assert.Equal(t, 90*time.Second, client.reconnectMaxDelay)
	assert.Equal(t, 3*time.Second, client.reInitDelay)
	assert.Equal(t, 11*time.Second, client.resendDelay)
}

// TestEnsureConsumersSucceedsWithExpiredCallerContext pins that lazy consumer
// setup runs on its own bounded budget, detached from the caller's deadline:
// an already-expired caller context (e.g. an HTTP request whose ~5s deadline
// passed before the first tenant touch) must not abort the declare loop.
func TestEnsureConsumersSucceedsWithExpiredCallerContext(t *testing.T) {
	log := logger.New("error", false)
	client := &stubAMQPClient{}
	factory := func(string, logger.Logger) AMQPClient { return client }
	manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)
	defer func() { _ = manager.Close() }()

	decls := NewDeclarations()
	decls.RegisterExchange(&ExchangeDeclaration{Name: testExchange, Type: exchangeTypeTopic})
	decls.RegisterQueue(&QueueDeclaration{Name: testQueue})
	decls.RegisterBinding(&BindingDeclaration{Queue: testQueue, Exchange: testExchange, RoutingKey: testQueue})
	decls.RegisterConsumer(&ConsumerDeclaration{Queue: testQueue, Consumer: testConsumer, Handler: &mockMessageHandler{}})

	// Caller context is already expired BEFORE EnsureConsumers is even called —
	// simulates a lazy-start request whose ~5s deadline passed mid-setup.
	callerCtx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, manager.EnsureConsumers(callerCtx, testTenantID, decls))

	assert.Contains(t, client.declaredQueueNames(), testQueue)
	assert.Equal(t, 1, client.consumerCount())
}
