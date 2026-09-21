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
	"github.com/gaborage/go-bricks/multitenant"
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
	consumers      int
	consumeCtx     context.Context //nolint:containedctx // test-only: captures the ctx the supervisor subscribes with, to assert its lifecycle
	closeCallback  func()
	closeHook      func() // optional: invoked at the start of Close (e.g. to simulate a slow close)
	closeErr       error
	declaredQueues []string
}

func (s *stubAMQPClient) publishBytes(_ context.Context, _ publishOptions, _ []byte) error {
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

// TestMessagingManagerStampsEveryClientItHandsOut pins the wiring the tenant stamp
// depends on. The factory is replaceable (app.Options.MessagingClientFactory), so
// this must hold for a client the FRAMEWORK never built: a deployment with its own
// factory that published unstamped would, under messaging.tenancy: shared, have its
// own consumers nack every delivery with nothing at startup saying why.
func TestMessagingManagerStampsEveryClientItHandsOut(t *testing.T) {
	lease := func(t *testing.T, key string, client AMQPClient) AMQPClient {
		t.Helper()
		manager := NewMessagingManager(
			&stubMessagingSource{urls: map[string]string{key: amqpHost}},
			logger.New("error", false),
			ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
			func(string, logger.Logger) AMQPClient { return client })
		t.Cleanup(func() { _ = manager.Close() })

		pub, release, err := manager.Publisher(context.Background(), key)
		require.NoError(t, err)
		t.Cleanup(release)
		return pub
	}

	t.Run("a_consumer_supplied_client_is_stamped_too", func(t *testing.T) {
		// stubAMQPClient is exactly what a consumer's own ClientFactory returns: an
		// AMQPClient the framework did not build and cannot recognize by type.
		client := &recordingStubClient{}
		pub := lease(t, tenantID, client)

		require.NoError(t, rawPublish(
			multitenant.SetTenant(context.Background(), tenantID), pub,
			publishOptions{Exchange: genericEx, RoutingKey: "rk"}, []byte("payload")))

		assert.Equal(t, tenantID, client.lastHeaders[TenantStampHeader])
	})

	t.Run("the_replay_key_stamps_when_the_context_has_no_tenant", func(t *testing.T) {
		client := &recordingStubClient{}
		pub := lease(t, tenantID, client)

		require.NoError(t, rawPublish(context.Background(), pub,
			publishOptions{Exchange: genericEx, RoutingKey: "rk"}, []byte("payload")))

		assert.Equal(t, tenantID, client.lastHeaders[TenantStampHeader])
	})

	t.Run("the_control_plane_client_carries_no_stamp_without_a_tenant", func(t *testing.T) {
		client := &recordingStubClient{}
		pub := lease(t, "", client)

		require.NoError(t, rawPublish(context.Background(), pub,
			publishOptions{Exchange: genericEx, RoutingKey: "rk"}, []byte("payload")))

		assert.NotContains(t, client.lastHeaders, TenantStampHeader)
	})

	t.Run("a_caller_supplied_stamp_is_refused_before_the_client", func(t *testing.T) {
		client := &recordingStubClient{}
		pub := lease(t, tenantID, client)

		err := rawPublish(multitenant.SetTenant(context.Background(), tenantID), pub,
			publishOptions{
				Exchange: genericEx, RoutingKey: "rk",
				Headers: map[string]any{TenantStampHeader: tenantID},
			}, []byte("payload"))

		require.ErrorIs(t, err, ErrTenantStampConflict)
		assert.Zero(t, client.publishes, "a refused publish never reaches the client")
	})

	t.Run("the_callers_header_map_is_never_written_to", func(t *testing.T) {
		client := &recordingStubClient{}
		pub := lease(t, tenantID, client)
		callerHeaders := map[string]any{"keep": "me"}

		require.NoError(t, rawPublish(context.Background(), pub,
			publishOptions{Exchange: genericEx, RoutingKey: "rk", Headers: callerHeaders},
			[]byte("payload")))

		assert.Equal(t, map[string]any{"keep": "me"}, callerHeaders)
		assert.Equal(t, tenantID, client.lastHeaders[TenantStampHeader])
		assert.Equal(t, "me", client.lastHeaders["keep"])
	})
}

// recordingStubClient is a client the framework did not build: it records what
// actually reached it, so a test can tell a stamped publish from an unstamped one.
type recordingStubClient struct {
	stubAMQPClient
	lastHeaders map[string]any
	publishes   int
}

// publishBytes always succeeds: the stub records what the wrapper handed it, and
// the interface requires the error result.
//
//nolint:unparam // signature fixed by bytePublisher
func (c *recordingStubClient) publishBytes(_ context.Context, options publishOptions, _ []byte) error {
	c.publishes++
	c.lastHeaders = options.Headers
	return nil
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
		require.NoError(t, err)
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
		decls.RegisterExchange(&ExchangeDeclaration{Name: genericEx, Type: ExchangeTypeTopic, Durable: true})
		decls.RegisterQueue(&QueueDeclaration{Name: genericQueue, Durable: true})
		decls.RegisterConsumer(&ConsumerDeclaration{Queue: genericQueue, Consumer: genericConsumer, EventType: eventTestEvent, Handler: &mockMessageHandler{}})

		// First call - should create client and registry
		err := manager.EnsureConsumers(ctx, tenantID, decls)
		require.NoError(t, err)
		assert.Equal(t, 1, clientCallCount, "First call should create client")

		// Second call with identical declarations - should be idempotent
		err = manager.EnsureConsumers(ctx, tenantID, decls)
		require.NoError(t, err)
		assert.Equal(t, 1, clientCallCount, "Second call should reuse existing setup")

		// Third call - still idempotent
		err = manager.EnsureConsumers(ctx, tenantID, decls)
		require.NoError(t, err)
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
		decls1.RegisterExchange(&ExchangeDeclaration{Name: genericEx, Type: ExchangeTypeTopic, Durable: true})
		decls1.RegisterQueue(&QueueDeclaration{Name: genericQueue, Durable: true})
		decls1.RegisterConsumer(&ConsumerDeclaration{Queue: genericQueue, Consumer: genericConsumer, EventType: eventA, Handler: &mockMessageHandler{}})

		err := manager.EnsureConsumers(ctx, tenantID, decls1)
		require.NoError(t, err)

		// Second set of declarations - different structure
		decls2 := NewDeclarations()
		decls2.RegisterExchange(&ExchangeDeclaration{Name: genericEx, Type: ExchangeTypeTopic, Durable: false}) // Different Durable flag
		decls2.RegisterQueue(&QueueDeclaration{Name: genericQueue, Durable: true})
		decls2.RegisterConsumer(&ConsumerDeclaration{Queue: genericQueue, Consumer: genericConsumer, EventType: eventA, Handler: &mockMessageHandler{}})

		err = manager.EnsureConsumers(ctx, tenantID, decls2)
		require.Error(t, err)
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
			require.NoError(t, err)
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
		require.NoError(t, err)
		assert.Equal(t, 1, clientCallCount)

		// Setup for tenant2 with same declarations - should create new client
		err = manager.EnsureConsumers(ctx, tenant2ID, decls)
		require.NoError(t, err)
		assert.Equal(t, 2, clientCallCount, "Different keys should have independent setups")

		// Replay to tenant1 - should be idempotent
		err = manager.EnsureConsumers(ctx, tenant1ID, decls)
		require.NoError(t, err)
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
		require.NoError(t, err)

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
	_, rel1, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	_, rel2, err := manager.Publisher(ctx, tenant2ID)
	require.NoError(t, err)
	rel1()
	rel2()

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
	_, rel, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	rel()

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
// including consumer_registries (from the consumer map) and the evictions counter (surfaced
// from the pool after an LRU eviction). The per-pool counter semantics are exercised directly in
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
	assert.Equal(t, 0, stats["consumer_registries"])
	assert.Equal(t, 0, stats["declared_consumers"])
	assert.Equal(t, 0, stats["subscribed_consumers"])
	assert.Equal(t, uint64(0), stats["consumer_resubscribes"])
	assert.Equal(t, 90, stats["idle_ttl_seconds"])
	assert.Equal(t, 0, stats["evictions"])
	assert.Equal(t, 0, stats["idle_cleanups"])
	assert.Equal(t, 0, stats["errors"])

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
		ManagerOptions{MaxPublishers: 5, IdleTTL: 10 * time.Millisecond, CleanupInterval: 10 * time.Millisecond},
		factory,
	)
	defer func() { _ = manager.Close() }()

	_, rel, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	rel() // release so the idle publisher becomes eligible for cleanup

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
	assert.Nil(t, pub)
	assert.Nil(t, release)
	require.ErrorIs(t, err, ErrManagerClosed, "Publisher after Close must fail closed, not resurrect a publisher (F22)")
}

// TestMessagingManagerPublisherMidCloseWindowFailsClosed pins the gap CodeRabbit flagged on PR
// #950 (review comment 3750514780): Close flips m.closed BEFORE it closes m.pubPool (see Close),
// so a caller landing in that narrow window would otherwise reach a still-open pool and could
// get back a live, cached publisher on a manager that has begun shutting down. Setting the flag
// directly — instead of calling Close — reproduces exactly that window without also closing the
// pool, which is what Close's two-step teardown leaves behind for the brief interval between
// them. Unlike TestMessagingManagerPublisherAfterCloseReturnsError above (where the pool is ALSO
// closed, so the pre-existing ErrPoolClosed translation alone would already catch it), this test
// isolates the manager-level guard: the pool's own closed check cannot fire here.
func TestMessagingManagerPublisherMidCloseWindowFailsClosed(t *testing.T) {
	ctx := context.Background()
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1}},
		logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} },
	)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close messaging manager: %v", err)
		}
	})

	// Warm the pool with a live, cached publisher for tenant1ID.
	_, release, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	release()
	created := manager.pubPool.Stats().TotalCreated

	// Close flips closed BEFORE closing the pool, so a caller racing Close can observe
	// closed=true while the pool is still open and fully able to hand back the cached entry.
	manager.closed.Store(true)

	pub, rel, err := manager.Publisher(ctx, tenant1ID)
	assert.Nil(t, pub, "a manager mid-Close must not hand back the still-cached publisher")
	assert.Nil(t, rel)
	assert.Equal(t, created, manager.pubPool.Stats().TotalCreated, "the still-open pool must not create anything new during the close window")
	require.ErrorIs(t, err, ErrManagerClosed, "Publisher must fail closed once Close begins, even while the pool is still open")
}

// TestMessagingManagerStatsSurfacesPoolErrors pins that a deferred-close failure — a publisher
// still borrowed when Close runs, closed only at its final release (ADR-032, C581.3) — is not
// silently dropped: PoolStats.Errors must reach Stats()["errors"] so callers can observe it,
// since it is deliberately excluded from Close()'s returned error (contrast with
// TestMessagingManagerCloseSurfacesClientErrors, the synchronous-close counterpart).
func TestMessagingManagerStatsSurfacesPoolErrors(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	client := &stubAMQPClient{closeErr: errors.New("deferred close failure")}
	factory := func(string, logger.Logger) AMQPClient { return client }
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1}},
		log,
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		factory,
	)

	_, release, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	assert.Equal(t, 0, manager.Stats()["errors"], "no close attempted yet")

	// Close leaves the still-borrowed publisher open (liveLeases > 0); the deferred close
	// attempt — and its failure — only happens once the lease is released.
	require.NoError(t, manager.Close(), "Close must not surface a deferred close failure")
	release()

	assert.Equal(t, 1, manager.Stats()["errors"], "deferred publisher-close failure must be counted and surfaced")
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
	assert.Equal(t, 0, stats["consumer_registries"])
	assert.Equal(t, 0, stats["declared_consumers"])
	assert.Equal(t, 0, stats["subscribed_consumers"])
	assert.Equal(t, uint64(0), stats["consumer_resubscribes"])
	assert.Equal(t, 0, stats["idle_ttl_seconds"])
	assert.Equal(t, 0, stats["evictions"])
	assert.Equal(t, 0, stats["idle_cleanups"])

	pub, release, err := m.Publisher(context.Background(), "any")
	assert.Nil(t, pub)
	assert.Nil(t, release)
	require.ErrorIs(t, err, ErrManagerClosed, "zero-value Publisher must fail closed, not panic")

	assert.NotPanics(t, func() {
		m.StartCleanup(time.Minute)
		m.StopCleanup()
	}, "zero-value StartCleanup/StopCleanup must be no-ops, not panic")

	assert.NoError(t, m.Close(), "closing a never-initialized manager is a no-op")
}

// TestNewMessagingManagerStartsIdleCleanup pins ADR-067 decision 4 for the publisher pool: the
// sweep starts at construction, with no StartCleanup call from the caller.
func TestNewMessagingManagerStartsIdleCleanup(t *testing.T) {
	ctx := context.Background()
	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1}},
		logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: 10 * time.Millisecond, CleanupInterval: 10 * time.Millisecond},
		factory,
	)
	defer func() { _ = manager.Close() }()

	_, rel, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	rel()

	assert.Eventually(t, func() bool {
		count, _ := manager.Stats()["idle_cleanups"].(int)
		return count >= 1
	}, 2*time.Second, 10*time.Millisecond, "the constructor must start the idle-publisher sweep")
}

// TestNewMessagingManagerClosesCleanlyWithALiveCleanupLoop pins that Close stops the sweep the
// constructor started, so a caller that never touches StartCleanup/StopCleanup shuts down clean.
func TestNewMessagingManagerClosesCleanlyWithALiveCleanupLoop(t *testing.T) {
	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1}},
		logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: 10 * time.Millisecond, CleanupInterval: 10 * time.Millisecond},
		factory,
	)

	require.NoError(t, manager.Close(), "Close must stop the constructor-started sweep and report success")
	require.NoError(t, manager.Close(), "Close stays idempotent")
}

// TestNewMessagingManagerWarnsWhenCleanupIntervalIsNotBelowIdleTTL pins that the advisory that
// used to live in App.warnIfCleanupIntervalTooLate now fires from the manager, naming the
// messaging.publisher keys. The predicate itself is exhausted in
// internal/resourcepool/cleanup_warning_test.go, so only what is manager-specific stays here:
// the non-positive-CleanupInterval default is applied BEFORE the check (a raw 0 would be below
// any TTL and stay silent), and a genuinely faster sweep still says nothing.
func TestNewMessagingManagerWarnsWhenCleanupIntervalIsNotBelowIdleTTL(t *testing.T) {
	tests := []struct {
		name            string
		cleanupInterval time.Duration
		idleTTL         time.Duration
		wantWarn        bool
	}{
		{name: "interval_below_ttl_silent", cleanupInterval: time.Minute, idleTTL: time.Hour, wantWarn: false},
		{name: "unset_interval_takes_the_default_and_warns_against_a_short_ttl", cleanupInterval: 0, idleTTL: time.Minute, wantWarn: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			log := &stubLogger{}
			factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
			manager := NewMessagingManager(
				&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1}},
				log,
				ManagerOptions{MaxPublishers: 5, IdleTTL: tc.idleTTL, CleanupInterval: tc.cleanupInterval},
				factory,
			)
			defer func() { _ = manager.Close() }()

			entries := log.getEntries()
			if !tc.wantWarn {
				assert.Empty(t, entries, "a sweep that outpaces the TTL must not WARN")
				return
			}
			require.Len(t, entries, 1, "the advisory must fire exactly once per manager")
			assert.Contains(t, entries[0], "messaging.publisher.cleanupinterval is >= messaging.publisher.idlettl")
		})
	}
}

// TestMessagingManagerStartCleanupIsIdempotent pins that a second StartCleanup observes the
// already-running pool cleanup loop and short-circuits, and StopCleanup is a safe no-op when
// called again. The manager's StartCleanup is a thin passthrough to the pool.
func TestMessagingManagerStartCleanupIsIdempotent(t *testing.T) {
	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	m := NewMessagingManager(&stubMessagingSource{}, logger.New("error", false),
		ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour}, factory)
	defer func() { _ = m.Close() }()

	// The constructor already started a loop (ADR-067); stop it so the first call below is the
	// one that starts a loop and the second is the one that must short-circuit.
	m.StopCleanup()

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

	m.StopCleanup()

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

// TestNewMessagingManagerDefaultFactoryForwardsPublishTimeout pins the last hop
// of messaging.publishtimeout: ManagerOptions -> default client factory ->
// AMQPClientImpl.publishTimeout. 41s is deliberately not any client default, so
// a dropped or cross-wired assignment cannot pass by coincidence.
func TestNewMessagingManagerDefaultFactoryForwardsPublishTimeout(t *testing.T) {
	oldDial := getAmqpDialFunc()
	setAmqpDialFunc(func(_ string) (amqpConnection, error) { return nil, errors.New(dialFailMsg) })
	t.Cleanup(func() { setAmqpDialFunc(oldDial) })

	log := logger.New("error", false)
	manager := NewMessagingManager(&stubMessagingSource{}, log, ManagerOptions{
		MaxPublishers:  1,
		IdleTTL:        time.Minute,
		PublishTimeout: 41 * time.Second,
	}, nil)

	client := manager.clientFactory(amqpHost, log).(*AMQPClientImpl)
	t.Cleanup(func() { closeAndWaitForReconnect(client) })

	assert.Equal(t, 41*time.Second, client.publishTimeout)

	// Unset stays unbounded: the option ignores the zero, so no deployment that
	// never configured the key acquires a deadline.
	unbounded := NewMessagingManager(&stubMessagingSource{}, log, ManagerOptions{
		MaxPublishers: 1,
		IdleTTL:       time.Minute,
	}, nil)
	unboundedClient := unbounded.clientFactory(amqpHost, log).(*AMQPClientImpl)
	t.Cleanup(func() { closeAndWaitForReconnect(unboundedClient) })
	assert.Equal(t, time.Duration(0), unboundedClient.publishTimeout)
}

// TestNewMessagingManagerDefaultFactoryForwardsAppName pins the last hop of ADR-105's
// app_id wiring: ManagerOptions.AppName -> default client factory -> AMQPClientImpl.appID.
func TestNewMessagingManagerDefaultFactoryForwardsAppName(t *testing.T) {
	oldDial := getAmqpDialFunc()
	setAmqpDialFunc(func(_ string) (amqpConnection, error) { return nil, errors.New(dialFailMsg) })
	t.Cleanup(func() { setAmqpDialFunc(oldDial) })

	log := logger.New("error", false)
	manager := NewMessagingManager(&stubMessagingSource{}, log, ManagerOptions{
		MaxPublishers: 1,
		IdleTTL:       time.Minute,
		AppName:       "orders-api",
	}, nil)

	client := manager.clientFactory(amqpHost, log).(*AMQPClientImpl)
	t.Cleanup(func() { closeAndWaitForReconnect(client) })

	assert.Equal(t, "orders-api", client.appID)

	// Unset stays empty: a deployment that configured no app.name stamps no app_id
	// rather than a placeholder.
	unnamed := NewMessagingManager(&stubMessagingSource{}, log, ManagerOptions{
		MaxPublishers: 1,
		IdleTTL:       time.Minute,
	}, nil)
	unnamedClient := unnamed.clientFactory(amqpHost, log).(*AMQPClientImpl)
	t.Cleanup(func() { closeAndWaitForReconnect(unnamedClient) })
	assert.Empty(t, unnamedClient.appID)
}

// newSetupDeclarations builds the exchange/queue/binding/consumer set the consumer-setup tests
// share, so a fixture change lands in one place instead of three.
func newSetupDeclarations() *Declarations {
	decls := NewDeclarations()
	decls.RegisterExchange(&ExchangeDeclaration{Name: testExchange, Type: ExchangeTypeTopic})
	decls.RegisterQueue(&QueueDeclaration{Name: testQueue})
	decls.RegisterBinding(&BindingDeclaration{Queue: testQueue, Exchange: testExchange, RoutingKey: testQueue})
	decls.RegisterConsumer(&ConsumerDeclaration{Queue: testQueue, Consumer: testConsumer, Handler: &mockMessageHandler{}})
	return decls
}

// consumerSetupHarness holds one consumer-setup pass in flight so a test can observe how another
// caller behaves while the shared pass is blocked. The client factory parks until unblock is
// called; started closes on the first factory call, so a test can be sure the NEXT caller is a
// collapsed waiter and not a fresh leader. Without that hold, a select whose ctx.Done() and result
// channel are both ready picks either one at random and the test flakes.
type consumerSetupHarness struct {
	manager *Manager
	client  *stubAMQPClient
	decls   *Declarations
	started <-chan struct{} // closed once the blocked setup pass has entered the factory
	unblock func()          // idempotent: safe from any path, including a t.Fatal or cleanup
	calls   func() int      // factory invocations observed so far
}

func newConsumerSetupHarness(t *testing.T) *consumerSetupHarness {
	t.Helper()

	client := &stubAMQPClient{}
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }

	var mu sync.Mutex
	calls := 0
	factory := func(string, logger.Logger) AMQPClient {
		mu.Lock()
		calls++
		mu.Unlock()
		startedOnce.Do(func() { close(started) })
		<-release
		return client
	}

	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		factory,
	)
	// Unblock BEFORE Close: a blocked setup pass holds consMu, and Close takes it.
	t.Cleanup(func() { unblock(); _ = manager.Close() })

	return &consumerSetupHarness{
		manager: manager,
		client:  client,
		decls:   newSetupDeclarations(),
		started: started,
		unblock: unblock,
		calls:   func() int { mu.Lock(); defer mu.Unlock(); return calls },
	}
}

// awaitStarted blocks until the setup pass has entered the client factory, failing fast rather
// than hanging the package if it never does.
func (h *consumerSetupHarness) awaitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-h.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the setup pass never entered the client factory")
	}
}

// TestEnsureConsumersExpiredCallerReturnsWhileSetupCompletes pins both halves of the collapse
// contract for a caller whose budget is already spent: it must return on ITS OWN context rather
// than sit through the shared setup's 45s infraSetupTimeout, and the setup it walked away from
// must still run to completion so the consumers exist for the next caller. Before the DoChan
// rewrite the first half was impossible — sfg.Do blocked uncancelably.
func TestEnsureConsumersExpiredCallerReturnsWhileSetupCompletes(t *testing.T) {
	h := newConsumerSetupHarness(t)

	// Caller context is already expired BEFORE EnsureConsumers is even called — simulates a
	// lazy-start request whose ~5s deadline passed before the first tenant touch.
	callerCtx, cancel := context.WithCancel(context.Background())
	cancel()

	returned := make(chan error, 1)
	go func() { returned <- h.manager.EnsureConsumers(callerCtx, testTenantID, h.decls) }()

	select {
	case err := <-returned:
		require.ErrorIs(t, err, context.Canceled,
			"an over-budget caller must fail on its OWN context, not block on the shared setup")
		require.ErrorContains(t, err, `consumer setup for key "test-tenant"`,
			"the wrap must name the operation and key, not just surface the bare context error")
	case <-time.After(2 * time.Second):
		t.Fatal("EnsureConsumers blocked on the shared setup instead of honoring its dead context")
	}

	// The abandoned setup must still finish and install the consumers.
	h.unblock()
	require.Eventually(t, func() bool { return h.client.consumerCount() == 1 },
		2*time.Second, 5*time.Millisecond,
		"the abandoned setup must still start its consumer")
	assert.Contains(t, h.client.declaredQueueNames(), testQueue,
		"the abandoned setup must still declare its infrastructure")
}

// TestEnsureConsumersCollapsedWaiterHonorsOwnContext is the scenario issue #835 reports: a caller
// that arrives while someone else's setup pass is in flight is collapsed onto it, and with sfg.Do
// that wait was uncancelable — an over-budget follower blocked up to infraSetupTimeout (45s) on a
// leader's work. The follower must fail on its own context WITHOUT canceling the leader's setup.
// Mirrors TestPoolGetOrCreateWaiterHonorsOwnContext in internal/resourcepool.
func TestEnsureConsumersCollapsedWaiterHonorsOwnContext(t *testing.T) {
	h := newConsumerSetupHarness(t)

	leader := make(chan error, 1)
	go func() { leader <- h.manager.EnsureConsumers(context.Background(), testTenantID, h.decls) }()
	h.awaitStarted(t) // the setup pass is in flight, so the next caller is a collapsed waiter

	dead, cancel := context.WithCancel(context.Background())
	cancel()

	follower := make(chan error, 1)
	go func() { follower <- h.manager.EnsureConsumers(dead, testTenantID, h.decls) }()

	select {
	case err := <-follower:
		require.ErrorIs(t, err, context.Canceled,
			"a collapsed waiter must fail on its OWN context, not on the leader's budget")
	case <-time.After(2 * time.Second):
		t.Fatal("the collapsed waiter blocked on the leader's setup instead of its own dead context")
	}

	h.unblock()
	require.NoError(t, <-leader, "the abandoning waiter must not cancel the leader's setup")
	assert.Equal(t, 1, h.calls(), "singleflight must still collapse the two callers into one setup")
	assert.Equal(t, 1, h.client.consumerCount())
}

// TestEnsureConsumersRecoversPanicFromSetup pins that a panic in the setup path becomes an error
// instead of killing the process. x/sync re-panics on a NEW goroutine once any caller used DoChan
// (`go panic(e)` in doCall), which no recover — not even Echo's middleware.Recover — can catch, so
// without the closure's own recover one tenant's bad broker config would crash-loop the service.
// The setup path runs consumer-supplied code (ClientFactory, BrokerURLProvider, handlers), so this
// is reachable from a project's own mistake.
func TestEnsureConsumersRecoversPanicFromSetup(t *testing.T) {
	log := logger.New("error", false)
	// A per-tenant broker configuration is exactly where a credential lives, so the
	// panic value here stands in for one (ADR-081).
	const setupPanicSecret = "not-a-real-secret-3390"
	factory := func(string, logger.Logger) AMQPClient { panic(setupPanicSecret) }
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		log,
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		factory,
	)
	defer func() { _ = manager.Close() }()

	decls := newSetupDeclarations()

	err := manager.EnsureConsumers(context.Background(), testTenantID, decls)
	require.Error(t, err, "a panicking setup must surface as an error, not escape the process")
	assert.ErrorContains(t, err, "panic during consumer setup") //nolint:testifylint // message clause; the independent ADR-081 leak check and retry assertion follow
	// ADR-081: the error is returned to the caller and reaches its logs; report the
	// panic value's type, never the value.
	assert.ErrorContains(t, err, "(type: string)") //nolint:testifylint // message clause; the independent ADR-081 leak check and retry assertion follow
	require.NotContains(t, err.Error(), setupPanicSecret,
		"the setup panic value must not ride along in the returned error")

	// A failed pass must not install anything, so the next caller retries and fails the same way
	// rather than inheriting a stale success.
	require.ErrorContains(t, manager.EnsureConsumers(context.Background(), testTenantID, decls),
		"panic during consumer setup",
		"a recovered panic must not leave a warm entry behind")
}

// TestEnsureConsumersWarmKeySkipsSetupOnDeadContext pins the pre-singleflight fast path: once a key
// is replayed, EnsureConsumers is a no-op and must not consult the caller's context at all. Without
// it a warm resolution still enters the select, where an already-closed ctx.Done() deterministically
// beats a not-yet-scheduled goroutine — so every request on a warm tenant with a spent budget would
// fail on work that had nothing left to do. SingleTenantResourceProvider.Messaging takes this path
// on every request.
func TestEnsureConsumersWarmKeySkipsSetupOnDeadContext(t *testing.T) {
	log := logger.New("error", false)
	client := &stubAMQPClient{}
	var mu sync.Mutex
	calls := 0
	factory := func(string, logger.Logger) AMQPClient {
		mu.Lock()
		calls++
		mu.Unlock()
		return client
	}
	callCount := func() int { mu.Lock(); defer mu.Unlock(); return calls }

	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		log,
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		factory,
	)
	defer func() { _ = manager.Close() }()

	decls := newSetupDeclarations()

	require.NoError(t, manager.EnsureConsumers(context.Background(), testTenantID, decls))
	warmCalls := callCount()

	dead, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, manager.EnsureConsumers(dead, testTenantID, decls),
		"a warm key must short-circuit before the select, so a spent caller budget is irrelevant")
	assert.Equal(t, warmCalls, callCount(), "the warm path must not run another setup pass")
}

// TestEnsureConsumersRejectsNilDeclarations pins the nil guard at the boundary. The hash is
// computed on the caller's goroutine, before and outside the closure's recover, so a nil
// Declarations would nil-deref the caller rather than surface as an error — and
// app/messaging_setup.go passes its argument through unguarded.
func TestEnsureConsumersRejectsNilDeclarations(t *testing.T) {
	log := logger.New("error", false)
	var mu sync.Mutex
	calls := 0
	factory := func(string, logger.Logger) AMQPClient {
		mu.Lock()
		calls++
		mu.Unlock()
		return &stubAMQPClient{}
	}
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		log,
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		factory,
	)
	defer func() { _ = manager.Close() }()

	err := manager.EnsureConsumers(context.Background(), testTenantID, nil)
	require.Error(t, err, "nil declarations must be rejected, not panic on the caller's goroutine")
	require.ErrorContains(t, err, "nil declarations")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 0, calls, "a rejected call must not reach the client factory")
}

// TestMessagingManagerEnsureConsumersAfterCloseFailsClosed pins failure mode (a): a
// previously-replayed key must not take the consumersReplayed fast path on a closed manager,
// which would otherwise report success for consumers that Close already tore down.
func TestMessagingManagerEnsureConsumersAfterCloseFailsClosed(t *testing.T) {
	ctx := context.Background()
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} },
	)
	decls := newSetupDeclarations()

	require.NoError(t, manager.EnsureConsumers(ctx, testTenantID, decls))
	require.NoError(t, manager.Close())

	err := manager.EnsureConsumers(ctx, testTenantID, decls)
	assert.ErrorIs(t, err, ErrManagerClosed, "a replayed key must not report success on a closed manager")
}

// TestMessagingManagerEnsureConsumersAfterCloseDoesNotDialNewKey pins failure mode (b): a
// closed manager must fail closed for a brand-new key too, rather than dialing a fresh AMQP
// connection into a map Close already drained (a connection nothing will ever close).
func TestMessagingManagerEnsureConsumersAfterCloseDoesNotDialNewKey(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	calls := 0
	factory := func(string, logger.Logger) AMQPClient {
		mu.Lock()
		calls++
		mu.Unlock()
		return &stubAMQPClient{}
	}
	callCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost, tenant2ID: amqpURLTenant2}},
		logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		factory,
	)
	decls := newSetupDeclarations()

	require.NoError(t, manager.EnsureConsumers(ctx, testTenantID, decls))
	require.NoError(t, manager.Close())
	snapshot := callCount()

	err := manager.EnsureConsumers(ctx, tenant2ID, decls)
	assert.Equal(t, snapshot, callCount(), "a closed manager must not dial a new broker connection")
	require.ErrorIs(t, err, ErrManagerClosed, "a new key must fail closed once the manager is closed")
}

// TestMessagingManagerEnsureConsumersInternalRechecksClosedUnderLock pins Step 1.4
// specifically: ensureConsumersInternal must re-check the closed flag under consMu, not just
// rely on EnsureConsumers' outer pre-lock read. Calling the unexported method directly
// bypasses the outer guard, so this test fails if and only if the re-check is missing.
func TestMessagingManagerEnsureConsumersInternalRechecksClosedUnderLock(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	factory := func(string, logger.Logger) AMQPClient {
		mu.Lock()
		calls++
		mu.Unlock()
		return &stubAMQPClient{}
	}
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		factory,
	)
	decls := newSetupDeclarations()
	manager.closed.Store(true)

	err := manager.ensureConsumersInternal(context.Background(), testTenantID, decls, decls.Hash())
	require.ErrorIs(t, err, ErrManagerClosed, "ensureConsumersInternal must re-check closed under consMu")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 0, calls, "a closed manager's internal setup must not reach the client factory")
}

// TestMessagingManagerCloseClearsReplayState pins Step 1.6: Close must invalidate
// replayedHashs, not just drain the consumer map, so a would-be replay of the same
// declarations after a hypothetical restart cannot skip setup based on stale state.
func TestMessagingManagerCloseClearsReplayState(t *testing.T) {
	ctx := context.Background()
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} },
	)
	decls := newSetupDeclarations()
	require.NoError(t, manager.EnsureConsumers(ctx, testTenantID, decls))

	require.NoError(t, manager.Close())

	manager.consMu.RLock()
	defer manager.consMu.RUnlock()
	assert.Empty(t, manager.replayedHashs, "Close must invalidate replay state, not just the consumer map")
}

// TestMessagingManagerStopConsumersKeepsReplayState pins Step 1.5: StopConsumers is the
// weaker "stop delivering, manager still queryable" phase. Unlike Close it must not mark the
// manager closed or clear replayedHashs, so a subsequent EnsureConsumers for the same key stays
// on the warm fast path instead of re-dialing mid-drain.
func TestMessagingManagerStopConsumersKeepsReplayState(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	calls := 0
	factory := func(string, logger.Logger) AMQPClient {
		mu.Lock()
		calls++
		mu.Unlock()
		return &stubAMQPClient{}
	}
	callCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		factory,
	)
	t.Cleanup(func() { _ = manager.Close() })
	decls := newSetupDeclarations()
	require.NoError(t, manager.EnsureConsumers(ctx, testTenantID, decls))

	manager.StopConsumers()
	snapshot := callCount()

	err := manager.EnsureConsumers(ctx, testTenantID, decls)
	require.NoError(t, err)
	assert.Equal(t, snapshot, callCount(), "Stop must leave the fast path warm — no re-dial")
}

// TestMessagingManagerEnsureConsumersWarmHashLosesToClosedGuard pins EnsureConsumers' outer
// closed check specifically, independent of ensureConsumersInternal's consMu-guarded re-check:
// Close flips closed before it acquires consMu to clear replayedHashs, so a caller racing Close
// can observe closed=true with the replay hash still warm. Only the outer guard — which runs
// before the consumersReplayed fast path — defends that window; the fast path itself has no
// closed check at all.
func TestMessagingManagerEnsureConsumersWarmHashLosesToClosedGuard(t *testing.T) {
	ctx := context.Background()
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} },
	)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close messaging manager: %v", err)
		}
	})
	decls := newSetupDeclarations()
	require.NoError(t, manager.EnsureConsumers(ctx, testTenantID, decls))

	// Close flips closed BEFORE taking consMu to clear replayedHashs, so a caller racing Close
	// sees the flag with the hash still warm — the window only EnsureConsumers' guard defends.
	manager.closed.Store(true)

	err := manager.EnsureConsumers(ctx, testTenantID, decls)
	assert.ErrorIs(t, err, ErrManagerClosed, "a warm replay hash must not beat the closed guard")
}

// rawPublish reaches the byte door on a leased client the way the typed handle
// does: through the unexported bytePublisher, which every framework-built or
// in-package client satisfies.
func rawPublish(ctx context.Context, client AMQPClient, opts publishOptions, data []byte) error {
	return publishThroughDoor(ctx, client, opts, data)
}

// TestMessagingManagerStatsCountsConsumersAcrossRegistries pins the consumer-side
// counters: consumer_registries counts the tenant keys holding a registry,
// declared_consumers counts the consumers those registries declare, and
// subscribed_consumers counts the ones with a live subscription.
func TestMessagingManagerStatsCountsConsumersAcrossRegistries(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	client := &stubAMQPClient{}
	factory := func(string, logger.Logger) AMQPClient { return client }
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		log,
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		factory,
	)
	defer func() { _ = manager.Close() }() // stop supervisor goroutines

	decls := NewDeclarations()
	for _, queue := range []string{testQueue, testQueue1Name, testQueue2Name} {
		decls.RegisterQueue(&QueueDeclaration{Name: queue})
		decls.RegisterConsumer(&ConsumerDeclaration{
			Queue:     queue,
			Consumer:  testConsumer,
			EventType: testEventType,
			Handler:   &countingTestHandler{},
		})
	}
	require.NoError(t, manager.EnsureConsumers(ctx, testTenantID, decls))

	stats := manager.Stats()
	assert.Equal(t, 1, stats["consumer_registries"], "one tenant key holds a consumer registry")
	assert.Equal(t, 3, stats["declared_consumers"])
	assert.Equal(t, 3, stats["subscribed_consumers"])
	assert.Equal(t, uint64(0), stats["consumer_resubscribes"])
	assert.Equal(t, 0, stats["consumer_max_fail_streak"], "nothing is failing")
}

// TestMessagingManagerStatsPublishTheWorstCurrentFailStreak pins the one counter that makes
// the intermediate state — a consumer unsubscribed but still inside its streak — readable
// from /ready and /_sys/health-debug. The client is frozen INSIDE the fourth re-subscribe
// attempt, so the three that already failed are read at rest rather than raced, and the
// number is a bare count: it says how far the unluckiest consumer has got, never which one.
func TestMessagingManagerStatsPublishTheWorstCurrentFailStreak(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	const pauseAt = 4
	client := newPausingConsumeClient(pauseAt)
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		log,
		ManagerOptions{MaxPublishers: 1, IdleTTL: time.Minute, ConsumerResubscribeDelay: time.Millisecond},
		func(string, logger.Logger) AMQPClient { return client },
	)
	defer func() { _ = manager.Close() }() // stop supervisor goroutines

	decls := NewDeclarations()
	decls.RegisterQueue(&QueueDeclaration{Name: testQueue})
	decls.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueue,
		Consumer:  testConsumer,
		EventType: testEventType,
		Handler:   &countingTestHandler{},
	})
	require.NoError(t, manager.EnsureConsumers(ctx, testTenantID, decls))

	require.Equal(t, 0, manager.Stats()["consumer_max_fail_streak"], "a subscribed consumer has no streak")

	close(client.first)
	select {
	case <-client.paused:
	case <-time.After(5 * time.Second):
		t.Fatal("the re-subscribe loop never reached the paused attempt")
	}
	defer close(client.release)

	stats := manager.Stats()

	assert.Equal(t, pauseAt-1, stats["consumer_max_fail_streak"], "three attempts have failed and the fourth is in flight")
	assert.Equal(t, 0, stats["subscribed_consumers"], "and the consumer reads unsubscribed while it retries")
	assert.Equal(t, 1, stats["declared_consumers"])
}

// TestMessagingManagerConsumerStatesSpanEveryRegistry pins the detail door behind the
// counters: one entry per declared consumer across every tenant key holding a registry,
// each carrying its own queue and live flag.
func TestMessagingManagerConsumerStatesSpanEveryRegistry(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1, tenant2ID: amqpURLTenant2}},
		log,
		ManagerOptions{MaxPublishers: 2, IdleTTL: time.Minute},
		factory,
	)
	defer func() { _ = manager.Close() }() // stop supervisor goroutines

	assert.Empty(t, manager.ConsumerStates(), "a manager with no consumers has no state to report")

	for tenant, queue := range map[string]string{tenant1ID: testQueue1Name, tenant2ID: testQueue2Name} {
		decls := NewDeclarations()
		decls.RegisterQueue(&QueueDeclaration{Name: queue})
		decls.RegisterConsumer(&ConsumerDeclaration{
			Queue:     queue,
			Consumer:  testConsumer,
			EventType: testEventType,
			Handler:   &countingTestHandler{},
		})
		require.NoError(t, manager.EnsureConsumers(ctx, tenant, decls))
	}

	states := manager.ConsumerStates()
	require.Len(t, states, 2)
	queues := make([]string, 0, len(states))
	for _, state := range states {
		queues = append(queues, state.Queue)
		assert.True(t, state.Subscribed, "queue %s is not subscribed", state.Queue)
		assert.Zero(t, state.Resubscribes)
	}
	assert.ElementsMatch(t, []string{testQueue1Name, testQueue2Name}, queues)
}

// TestMessagingManagerConsumerStatesCarryTheManagerKey pins the other half of a consumer's
// identity: under per-tenant replay every key declares the same consumers, so rows that are
// otherwise identical are told apart only by the key their registry was leased under.
func TestMessagingManagerConsumerStatesCarryTheManagerKey(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1, tenant2ID: amqpURLTenant2}},
		log,
		ManagerOptions{MaxPublishers: 2, IdleTTL: time.Minute},
		factory,
	)
	defer func() { _ = manager.Close() }() // stop supervisor goroutines

	for _, tenant := range []string{tenant1ID, tenant2ID} {
		decls := NewDeclarations()
		decls.RegisterQueue(&QueueDeclaration{Name: testQueue})
		decls.RegisterConsumer(&ConsumerDeclaration{
			Queue:     testQueue,
			Consumer:  testConsumer,
			EventType: testEventType,
			Handler:   &countingTestHandler{},
		})
		require.NoError(t, manager.EnsureConsumers(ctx, tenant, decls))
	}

	states := manager.ConsumerStates()

	require.Len(t, states, 2)
	keys := make([]string, 0, len(states))
	for _, state := range states {
		keys = append(keys, state.Key)
		assert.Equal(t, testQueue, state.Queue, "the declarations are identical apart from their key")
	}
	assert.ElementsMatch(t, []string{tenant1ID, tenant2ID}, keys)
}

// TestMessagingManagerAnyConsumerGivenUpSpansEveryRegistry pins that the readiness predicate
// sees every tenant key, not just the first: under per-tenant replay one tenant's broker can
// fail while the rest are healthy, and that tenant's consumers are the ones nothing else
// reports.
func TestMessagingManagerAnyConsumerGivenUpSpansEveryRegistry(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	healthy, _ := newOutageClient()
	failing, failingDeliveries := newOutageClient()
	clients := map[string]AMQPClient{tenant1ID: healthy, tenant2ID: failing}

	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1, tenant2ID: amqpURLTenant2}},
		log,
		ManagerOptions{MaxPublishers: 2, IdleTTL: time.Minute, ConsumerResubscribeDelay: time.Millisecond},
		func(url string, _ logger.Logger) AMQPClient {
			if url == amqpURLTenant2 {
				return failing
			}
			return healthy
		},
	)
	defer func() { _ = manager.Close() }() // stop supervisor goroutines

	for tenant := range clients {
		decls := NewDeclarations()
		decls.RegisterQueue(&QueueDeclaration{Name: testQueue})
		decls.RegisterConsumer(&ConsumerDeclaration{
			Queue:     testQueue,
			Consumer:  testConsumer,
			EventType: testEventType,
			Handler:   &countingTestHandler{},
		})
		require.NoError(t, manager.EnsureConsumers(ctx, tenant, decls))
	}

	require.False(t, manager.AnyConsumerGivenUp(), "every tenant is subscribed")

	failing.setFailing(true)
	close(failingDeliveries)

	assert.Eventually(t, manager.AnyConsumerGivenUp, 5*time.Second, 2*time.Millisecond,
		"one tenant's consumer gave up and the manager did not report it")
}

// TestMessagingManagerAppliesTheConsumerResubscribeDelay pins that the option reaches
// the registries the manager builds. At the default pace exhausting a re-subscribe
// streak takes tens of seconds of jittered backoff, so a streak that completes inside
// this test's budget can only come from the configured delay.
func TestMessagingManagerAppliesTheConsumerResubscribeDelay(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	client, deliveries := newOutageClient()
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		log,
		ManagerOptions{MaxPublishers: 1, IdleTTL: time.Minute, ConsumerResubscribeDelay: time.Millisecond},
		func(string, logger.Logger) AMQPClient { return client },
	)
	defer func() { _ = manager.Close() }() // stop supervisor goroutines

	decls := NewDeclarations()
	decls.RegisterQueue(&QueueDeclaration{Name: testQueue})
	decls.RegisterConsumer(&ConsumerDeclaration{
		Queue:     testQueue,
		Consumer:  testConsumer,
		EventType: testEventType,
		Handler:   &countingTestHandler{},
	})
	require.NoError(t, manager.EnsureConsumers(ctx, testTenantID, decls))

	client.setFailing(true)
	close(deliveries)

	require.Eventually(t, func() bool {
		states := manager.ConsumerStates()
		return len(states) == 1 && states[0].GivenUp()
	}, 5*time.Second, 2*time.Millisecond, "the configured re-subscribe delay did not reach the registry")
}

// panicSecretValue stands in for a consumer-chosen panic payload. ADR-081 says a
// recovered panic is reported by TYPE, never by value, so this string must never
// reach the error the caller sees.
const panicSecretValue = "declare exploded with tenant-secret-9f3a"

// closePanicValue is a DIFFERENT type from panicSecretValue on purpose, so a
// test can tell which panic reached the caller.
const closePanicValue = 42

// panickingDeclareClient panics from the declare path — AFTER the factory handed
// the client over, which is the window where the rollback the singleflight
// recover sits above is the only thing that can close it.
type panickingDeclareClient struct {
	*stubAMQPClient
}

func (c *panickingDeclareClient) DeclareExchange(context.Context, *ExchangeDeclaration) error {
	panic(panicSecretValue)
}

// TestEnsureConsumersKeepsTheSetupPanicWhenClosingAlsoPanics pins the guard
// around the rollback close. Both the client and its logger are
// consumer-supplied, so a panic while closing would otherwise replace the setup
// panic still in flight — the caller would be told the close's type instead of
// the setup's, and the client this defer exists to close would stay open.
func TestEnsureConsumersKeepsTheSetupPanicWhenClosingAlsoPanics(t *testing.T) {
	factory := func(string, logger.Logger) AMQPClient {
		return &panickingDeclareClient{stubAMQPClient: &stubAMQPClient{closeCallback: func() {
			panic(closePanicValue)
		}}}
	}
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		factory,
	)
	defer func() { _ = manager.Close() }()

	err := manager.EnsureConsumers(context.Background(), testTenantID, newSetupDeclarations())

	require.ErrorContains(t, err, "panic during consumer setup")
	require.ErrorContains(t, err, "(type: string)", "the setup panic's type must survive a panicking close")
	require.NotContains(t, err.Error(), "(type: int)", "the close panic must not replace the setup panic")
}

// TestEnsureConsumersClosesTheClientWhenSetupPanics pins the leak a panic past
// client creation used to open: the recover lives in the singleflight closure,
// one frame above every rollback, so the client survived unclosed with its
// reconnect supervisor and dial loop running, and because nothing was recorded
// every later request for the key built another one.
func TestEnsureConsumersClosesTheClientWhenSetupPanics(t *testing.T) {
	var mu sync.Mutex
	built, closed := 0, 0
	factory := func(string, logger.Logger) AMQPClient {
		mu.Lock()
		built++
		mu.Unlock()
		return &panickingDeclareClient{stubAMQPClient: &stubAMQPClient{closeCallback: func() {
			mu.Lock()
			closed++
			mu.Unlock()
		}}}
	}
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		logger.New("error", false),
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		factory,
	)
	defer func() { _ = manager.Close() }()
	decls := newSetupDeclarations()

	const attempts = 3
	for range attempts {
		err := manager.EnsureConsumers(context.Background(), testTenantID, decls)
		require.ErrorContains(t, err, "panic during consumer setup")
		// ADR-081: re-panicking leaves the singleflight recover as the only converter,
		// so the caller still gets the type and never the consumer-chosen value.
		require.ErrorContains(t, err, "(type: string)")
		require.NotContains(t, err.Error(), panicSecretValue)
	}

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, attempts, built, "a panicked setup records nothing, so every attempt rebuilds")
	assert.Equal(t, attempts, closed, "every client a panicked setup built must be closed, not left dialing")
}

// clientCounter hands out a ClientFactory and counts what it built against what
// was closed, which is the whole question for a client held only in a local.
type clientCounter struct {
	mu     sync.Mutex
	built  int
	closed int
}

func (c *clientCounter) factory() ClientFactory {
	return func(string, logger.Logger) AMQPClient {
		c.mu.Lock()
		c.built++
		c.mu.Unlock()
		return &stubAMQPClient{closeCallback: func() {
			c.mu.Lock()
			c.closed++
			c.mu.Unlock()
		}}
	}
}

func (c *clientCounter) counts() (built, closed int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.built, c.closed
}

// TestEnsureConsumersClosesTheClientWhenTheCreationLogPanics pins the window one
// statement ABOVE the setup guard: createAMQPClient logs through the
// consumer-supplied logger while the fresh client is still only a local, so a
// panic there leaks exactly the client this guard exists to close.
func TestEnsureConsumersClosesTheClientWhenTheCreationLogPanics(t *testing.T) {
	clients := &clientCounter{}
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		&stubLogger{panicOn: "Created AMQP client for key", panicWith: panicSecretValue},
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		clients.factory(),
	)
	defer func() { _ = manager.Close() }()

	err := manager.EnsureConsumers(context.Background(), testTenantID, newSetupDeclarations())

	require.ErrorContains(t, err, "panic during consumer setup")
	require.ErrorContains(t, err, "(type: string)")
	require.NotContains(t, err.Error(), panicSecretValue)

	built, closed := clients.counts()
	require.Equal(t, 1, built)
	assert.Equal(t, 1, closed, "a panic in the creation log must not leave the client it just built dialing")
}

// TestEnsureConsumersKeepsTheMapOwnedClientWhenTheStartedLogPanics pins the other
// side of the same guard. Past the consumers map write the entry owns the client,
// so a panic in the trailing log must leave it open: closing it there strands a
// started entry over a dead client — every later EnsureConsumers a silent no-op,
// and Close closing the same client a second time.
func TestEnsureConsumersKeepsTheMapOwnedClientWhenTheStartedLogPanics(t *testing.T) {
	clients := &clientCounter{}
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{testTenantID: amqpHost}},
		&stubLogger{panicOn: "Consumers started for key", panicWith: panicSecretValue},
		ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute},
		clients.factory(),
	)
	defer func() { _ = manager.Close() }()
	decls := newSetupDeclarations()

	err := manager.EnsureConsumers(context.Background(), testTenantID, decls)
	require.ErrorContains(t, err, "panic during consumer setup")

	built, closed := clients.counts()
	require.Equal(t, 1, built)
	require.Equal(t, 0, closed, "the map owns the client past the entry write, and Close is what closes it")

	manager.consMu.RLock()
	entry, exists := manager.consumers[testTenantID]
	manager.consMu.RUnlock()
	require.True(t, exists, "the entry written before the panic must survive it")
	require.True(t, entry.client.IsReady(), "the surviving entry must still hold a live client")

	// The recorded state makes every later call a no-op, so that no-op has to be
	// over consumers that are actually running.
	require.NoError(t, manager.EnsureConsumers(context.Background(), testTenantID, decls))
	built, closed = clients.counts()
	assert.Equal(t, 1, built, "the no-op must not build a replacement client")
	assert.Equal(t, 0, closed)
}

// ===== Redeclare driven by a pooled publisher client (#1761) =====

// newQueuedClientFactory hands the manager the given clients in creation order,
// so a test can tell the consumer client apart from the pooled publisher client
// the manager builds next.
func newQueuedClientFactory(t *testing.T, clients ...AMQPClient) ClientFactory {
	t.Helper()
	var mu sync.Mutex
	queue := clients
	return func(string, logger.Logger) AMQPClient {
		mu.Lock()
		defer mu.Unlock()
		if len(queue) == 0 {
			// Errorf, not Fatalf: the factory runs inside the singleflight setup
			// pass, which neither recovers nor forwards a runtime.Goexit.
			t.Errorf("client factory called more often than the test queued clients")
			return &simpleMockAMQPClient{isReady: true}
		}
		client := queue[0]
		queue = queue[1:]
		return client
	}
}

// publisherOnlyDeclarations declares one exchange and the publisher targeting
// it, and no consumer: the shape whose only channel rotation happens on the
// pooled publisher client.
func publisherOnlyDeclarations() *Declarations {
	decls := NewDeclarations()
	decls.RegisterExchange(&ExchangeDeclaration{Name: testExchangeName, Type: ExchangeTypeTopic, Durable: true})
	decls.RegisterPublisher(&PublisherDeclaration{Exchange: testExchangeName, RoutingKey: testKeyValue, EventType: testEventType})
	return decls
}

// TestManagerRedeclaresTopologyFromAPooledPublisherChannel is the acceptance test
// for the second redeclare driver: the client that eats the broker's 404 is a
// pooled publisher, not the registry's own, so a rotation only that client saw
// still has to run a pass — and the pass has to declare through the registry's
// own client, because that is the connection the registry owns.
func TestManagerRedeclaresTopologyFromAPooledPublisherChannel(t *testing.T) {
	consumerClient, publisherClient := newReconnectingMockClient(), newReconnectingMockClient()
	m := NewMessagingManager(&stubMessagingSource{}, logger.New("error", false), ManagerOptions{},
		newQueuedClientFactory(t, consumerClient, publisherClient))
	t.Cleanup(func() { _ = m.Close() })

	ctx := context.Background()
	require.NoError(t, m.EnsureConsumers(ctx, "", publisherOnlyDeclarations()))
	_, release, err := m.Publisher(ctx, "")
	require.NoError(t, err)
	defer release()

	// Every recorded generation is the DECLARING client's own, and the consumer
	// client never rotates here — the pooled publisher does.
	key := "exchange:" + testExchangeName
	// The pooled client numbers its channels independently of the registry's, so
	// its first ready channel is already a generation the registry never declared
	// on: one pass lands before the first publish.
	awaitDeclares(t, consumerClient, key, "1", "1")
	// And one per rotation after it — the rotation an operator's exchange delete
	// causes, which only this client sees.
	publisherClient.newChannel()
	awaitDeclares(t, consumerClient, key, "1", "1", "1")
	assert.Empty(t, publisherClient.declaresOf(key), "the pass must declare through the registry's own client")
}

// observedPublisherCount reports how many pooled publishers the manager still
// runs a redeclare observer for, read under the lock that guards the map.
func observedPublisherCount(m *Manager) int {
	m.obsMu.Lock()
	defer m.obsMu.Unlock()
	return len(m.publisherObservers)
}

// redeclareSourceCount reports how many sources the registry still records a
// channel generation for, read under the lock that guards the ledger.
func redeclareSourceCount(r *Registry) int {
	r.redeclareMu.Lock()
	defer r.redeclareMu.Unlock()
	return len(r.handledGenerations)
}

// TestManagerPublisherChannelObserverEndsWithItsClient pins that the observer the
// manager attaches to a pooled publisher cannot outlive it: the pool stops the
// observer and closes the client on eviction, on the idle sweep and on Close, and
// a goroutine that survived any of those would leak one per retired client.
func TestManagerPublisherChannelObserverEndsWithItsClient(t *testing.T) {
	consumerClient, publisherClient := newReconnectingMockClient(), newReconnectingMockClient()
	m := NewMessagingManager(&stubMessagingSource{}, logger.New("error", false), ManagerOptions{},
		newQueuedClientFactory(t, consumerClient, publisherClient))

	ctx := context.Background()
	require.NoError(t, m.EnsureConsumers(ctx, "", publisherOnlyDeclarations()))
	_, release, err := m.Publisher(ctx, "")
	require.NoError(t, err)
	release()
	require.Equal(t, 1, observedPublisherCount(m), "the pooled publisher was never observed")

	require.NoError(t, m.Close())

	require.Eventually(t, func() bool {
		return observedPublisherCount(m) == 0
	}, 5*time.Second, time.Millisecond, "the pooled publisher's redeclare observer outlived the manager")
}

// TestManagerForgetsAPooledPublisherOnceItCloses pins the bookkeeping the
// per-source guard makes possible to get wrong: a registry outlives every
// publisher the pool evicts, so a generation entry that is never dropped
// accumulates one dead source per eviction for the process lifetime.
func TestManagerForgetsAPooledPublisherOnceItCloses(t *testing.T) {
	consumerClient, publisherClient := newReconnectingMockClient(), newReconnectingMockClient()
	m := NewMessagingManager(&stubMessagingSource{}, logger.New("error", false), ManagerOptions{},
		newQueuedClientFactory(t, consumerClient, publisherClient))
	t.Cleanup(func() { _ = m.Close() })

	ctx := context.Background()
	require.NoError(t, m.EnsureConsumers(ctx, "", publisherOnlyDeclarations()))
	_, release, err := m.Publisher(ctx, "")
	require.NoError(t, err)
	release()

	registry := m.registryFor("")
	require.NotNil(t, registry)
	require.Eventually(t, func() bool {
		return redeclareSourceCount(registry) == 2
	}, 5*time.Second, time.Millisecond, "the pooled publisher never became a recorded source")

	// What an LRU eviction or the idle sweep does to a pooled client.
	require.NoError(t, publisherClient.Close())

	require.Eventually(t, func() bool {
		return redeclareSourceCount(registry) == 1
	}, 5*time.Second, time.Millisecond, "the closed publisher's generation entry outlived it")
}
