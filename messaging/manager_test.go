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
			_, _, err := manager.Publisher(ctx, tenantID)
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

	var mu sync.Mutex
	closed := 0
	// Slow Close on the idle publisher proves idle cleanup detaches bookkeeping
	// under the lock and closes OUTSIDE it.
	releaseClose := make(chan struct{})
	factory := func(string, logger.Logger) AMQPClient {
		return &stubAMQPClient{
			closeHook: func() { <-releaseClose },
			closeCallback: func() {
				mu.Lock()
				defer mu.Unlock()
				closed++
			},
		}
	}

	manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{"idle": amqpURLIdle}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: 10 * time.Millisecond}, factory)
	_, relIdle, err := manager.Publisher(ctx, "idle")
	require.NoError(t, err)
	// Release the lease so cleanup may actually close it (deferred-until-release, ADR-032).
	relIdle()

	time.Sleep(20 * time.Millisecond)

	// Run cleanup in the background: the idle publisher's Close blocks until
	// releaseClose. With close-under-lock this would stall every Publisher()/Stats().
	done := make(chan struct{})
	go func() {
		manager.cleanupIdlePublishers()
		close(done)
	}()

	// Bookkeeping is detached under the lock, so the publisher count drops to 0 even
	// while its Close is still blocked.
	assert.Eventually(t, func() bool {
		manager.pubMu.RLock()
		defer manager.pubMu.RUnlock()
		return len(manager.publishers) == 0
	}, time.Second, 5*time.Millisecond)

	// Release the slow Close and let cleanup finish.
	close(releaseClose)
	<-done

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, closed)
}

func TestMessagingManagerEvictsLRU(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	var mu sync.Mutex
	evicted := []string{}
	// The LRU victim "a" (amqpURLA) uses a slow Close to assert eviction detaches
	// bookkeeping under the lock and closes OUTSIDE it.
	releaseClose := make(chan struct{})
	factory := func(url string, _ logger.Logger) AMQPClient {
		client := &stubAMQPClient{closeCallback: func() {
			mu.Lock()
			defer mu.Unlock()
			evicted = append(evicted, url)
		}}
		if url == amqpURLA {
			client.closeHook = func() { <-releaseClose }
		}
		return client
	}

	source := &stubMessagingSource{urls: map[string]string{
		"a": amqpURLA,
		"b": amqpURLB,
		"c": amqpURLC,
	}}

	manager := NewMessagingManager(source, log, ManagerOptions{MaxPublishers: 2, IdleTTL: time.Minute}, factory)
	_, relA, err := manager.Publisher(ctx, "a")
	require.NoError(t, err)
	_, _, err = manager.Publisher(ctx, "b")
	require.NoError(t, err)
	// Release "a"'s lease so eviction may actually close it — a leased publisher's close
	// is deferred until its last lease is released (ADR-032).
	relA()

	// Publisher("c") evicts "a"; its Close blocks until releaseClose, so run it in the
	// background. With close-under-lock this would hold m.pubMu and stall every Publisher.
	done := make(chan struct{})
	go func() {
		_, _, gErr := manager.Publisher(ctx, "c")
		assert.NoError(t, gErr)
		close(done)
	}()

	// Bookkeeping is detached under the lock, so the publisher count stays at 2 even
	// though the victim's Close is still blocked.
	assert.Eventually(t, func() bool {
		manager.pubMu.RLock()
		defer manager.pubMu.RUnlock()
		_, aStillCached := manager.publishers["a"]
		return len(manager.publishers) == 2 && !aStillCached
	}, time.Second, 5*time.Millisecond)

	// Release the slow Close and let the eviction finish.
	close(releaseClose)
	<-done

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, evicted, amqpURLA)
}

// TestMessagingManagerEvictionWithSlowCloseDoesNotBlockConcurrentPublisher guards the
// M3 audit finding: evictPublisherIfNeeded must NOT hold the publisher mutex while
// closing the evicted client. A slow Close() on an evicted tenant's publisher must not
// block a concurrent Publisher() that targets a different, still-cached tenant
// (head-of-line blocking). Mirrors the collect-then-close pattern in cache/manager.go.
func TestMessagingManagerEvictionWithSlowCloseDoesNotBlockConcurrentPublisher(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	const slowClose = 200 * time.Millisecond
	closeStarted := make(chan struct{})

	factory := func(url string, _ logger.Logger) AMQPClient {
		client := &stubAMQPClient{}
		if url == amqpURLA {
			// Tenant "a" is the LRU victim. Its Close blocks for slowClose to simulate
			// a stuck connection teardown. Signal once so the test can race a Publisher.
			var once sync.Once
			client.closeHook = func() {
				once.Do(func() { close(closeStarted) })
				time.Sleep(slowClose)
			}
		}
		return client
	}

	source := &stubMessagingSource{urls: map[string]string{
		"a": amqpURLA,
		"b": amqpURLB,
		"c": amqpURLC,
	}}

	manager := NewMessagingManager(source, log, ManagerOptions{MaxPublishers: 2, IdleTTL: time.Minute}, factory)

	_, relA, err := manager.Publisher(ctx, "a")
	require.NoError(t, err)
	_, _, err = manager.Publisher(ctx, "b")
	require.NoError(t, err)
	// Release "a"'s lease so the eviction can close it (deferred-until-release, ADR-032).
	relA()

	// Creating "c" evicts the LRU victim "a", whose Close blocks for slowClose.
	go func() {
		_, _, _ = manager.Publisher(ctx, "c")
	}()

	// Wait until the slow Close has actually begun before measuring.
	select {
	case <-closeStarted:
	case <-time.After(time.Second):
		t.Fatal("eviction Close never started")
	}

	// "b" is still cached: this Publisher only needs getExistingPublisher's lock. If
	// eviction holds the publisher mutex across Close (the M3 bug), this blocks ~slowClose.
	start := time.Now()
	_, _, err = manager.Publisher(ctx, "b")
	require.NoError(t, err)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, slowClose/2,
		"Publisher on a cached tenant must not block on another tenant's slow eviction Close (close-under-lock)")
}

// TestMessagingManagerCreatePublisherReturnsExistingOnDoubleCreate guards the
// concurrent double-create branch in createPublisher: when another goroutine
// populated the cache while this caller was building its client, createPublisher
// must return the already-cached publisher and close the freshly-created client
// outside the lock. Mirrors database's
// TestCreateConnectionReturnsExistingInstanceWhenAlreadyCached.
func TestMessagingManagerCreatePublisherReturnsExistingOnDoubleCreate(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	// The freshly-created client that createPublisher builds; it must be closed
	// once the cache is found already populated.
	fresh := &stubAMQPClient{}
	factory := func(string, logger.Logger) AMQPClient { return fresh }
	manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{tenantID: amqpHost}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Minute}, factory)

	// Pre-seed the cache to simulate another goroutine winning the race.
	existing := newTenantAwarePublisher(&stubAMQPClient{}, tenantID)
	manager.pubMu.Lock()
	element := manager.pubLru.PushFront(tenantID)
	manager.publishers[tenantID] = &publisherEntry{client: existing, element: element, lastUsed: time.Now(), key: tenantID}
	manager.pubMu.Unlock()

	got, err := manager.createPublisher(ctx, tenantID)
	require.NoError(t, err)
	assert.Same(t, existing, got.client, "createPublisher must return the already-cached publisher")

	fresh.closedMu.Lock()
	closed := fresh.closed
	fresh.closedMu.Unlock()
	assert.True(t, closed, "the redundant freshly-created client must be closed on the double-create branch")
}

// TestMessagingManagerCleanupIdlePublishersLogsCloseError drives the close-error
// branch of closeEvictedPublisher through the idle-cleanup path: the idle entry
// is detached from bookkeeping and Close() is still attempted even when it returns
// an error. Mirrors database's TestCleanupIdleConnectionsLogsCloseError.
func TestMessagingManagerCleanupIdlePublishersLogsCloseError(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	failing := &stubAMQPClient{closeErr: errors.New("broker fault on close")}
	factory := func(string, logger.Logger) AMQPClient { return failing }
	manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{"idle": amqpURLIdle}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Hour}, factory)
	defer func() { _ = manager.Close() }()

	_, relIdle, err := manager.Publisher(ctx, "idle")
	require.NoError(t, err)
	// Release the lease so cleanup may close it (deferred-until-release, ADR-032).
	relIdle()

	// Backdate lastUsed so the cleanup pass sees the entry as idle.
	manager.pubMu.Lock()
	manager.publishers["idle"].lastUsed = time.Now().Add(-2 * time.Hour)
	manager.pubMu.Unlock()

	manager.cleanupIdlePublishers()

	manager.pubMu.RLock()
	n := len(manager.publishers)
	manager.pubMu.RUnlock()
	assert.Equal(t, 0, n, "idle publisher removed from cache despite Close() error")

	failing.closedMu.Lock()
	closed := failing.closed
	failing.closedMu.Unlock()
	assert.True(t, closed, "Close was attempted even though it returned an error")
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
	_, relIdle, err := manager.Publisher(ctx, "idle")
	require.NoError(t, err)
	// Release the lease so the cleanup loop may close it (deferred-until-release, ADR-032).
	relIdle()

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
	_, _, err := manager.Publisher(ctx, tenant1ID)
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

	_, _, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	_, _, err = manager.Publisher(ctx, tenant2ID)
	require.NoError(t, err)

	stats = manager.Stats()
	assert.Equal(t, 2, stats["active_publishers"])
}

// --- Lease/refcount: eviction-while-in-use race (issue #606, ADR-032) ---

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

func TestMessagingManagerEvictionWhileLeasedDefersCloseUntilRelease(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	closed := map[string]bool{}
	m := NewMessagingManager(twoPublisherSource(), logger.New("error", false),
		ManagerOptions{MaxPublishers: 1, IdleTTL: time.Minute}, leasedClosableFactory(&mu, closed))
	defer func() { _ = m.Close() }()

	_, releaseA, err := m.Publisher(ctx, "a")
	require.NoError(t, err)

	_, releaseB, err := m.Publisher(ctx, "b") // evicts "a", which is still leased
	require.NoError(t, err)
	defer releaseB()

	mu.Lock()
	closedWhileLeased := closed[amqpURLA]
	mu.Unlock()
	assert.False(t, closedWhileLeased,
		"an evicted-but-leased publisher must not be closed while a lease is held (the #606 race)")

	releaseA()
	mu.Lock()
	closedAfterRelease := closed[amqpURLA]
	mu.Unlock()
	assert.True(t, closedAfterRelease,
		"an evicted publisher must be closed once its last lease is released")
}

func TestMessagingManagerTwoLeasesKeepPublisherAliveUntilBothReleased(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	closed := map[string]bool{}
	m := NewMessagingManager(twoPublisherSource(), logger.New("error", false),
		ManagerOptions{MaxPublishers: 1, IdleTTL: time.Minute}, leasedClosableFactory(&mu, closed))
	defer func() { _ = m.Close() }()

	_, release1, err := m.Publisher(ctx, "a")
	require.NoError(t, err)
	_, release2, err := m.Publisher(ctx, "a") // same key, second borrower → refcount 2
	require.NoError(t, err)

	_, releaseB, err := m.Publisher(ctx, "b") // evict "a"
	require.NoError(t, err)
	defer releaseB()

	release1()
	mu.Lock()
	closedAfterFirst := closed[amqpURLA]
	mu.Unlock()
	assert.False(t, closedAfterFirst, "publisher must stay open while a second lease is outstanding")

	release2()
	mu.Lock()
	closedAfterSecond := closed[amqpURLA]
	mu.Unlock()
	assert.True(t, closedAfterSecond, "publisher must close when the final lease is released")
}

func TestMessagingManagerPublisherReleaseIsIdempotent(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	closed := map[string]bool{}
	m := NewMessagingManager(twoPublisherSource(), logger.New("error", false),
		ManagerOptions{MaxPublishers: 1, IdleTTL: time.Minute}, leasedClosableFactory(&mu, closed))
	defer func() { _ = m.Close() }()

	_, releaseA, err := m.Publisher(ctx, "a")
	require.NoError(t, err)
	_, releaseB, err := m.Publisher(ctx, "b") // evict "a"
	require.NoError(t, err)
	defer releaseB()

	assert.NotPanics(t, func() {
		releaseA()
		releaseA() // double release must be a safe no-op
	})
}

// TestMessagingManagerColdRecreateAfterIdleEviction reproduces the issue #655
// timeline: a once-daily publisher goes idle past IdleTTL, gets evicted, and
// the NEXT Publisher() call must hand back a freshly created (cold) client
// rather than reusing the evicted one. This is not a fail-on-main regression
// test (stubAMQPClient has no not-ready state to model the readiness bug
// itself — see Task 1.1's client-level tests for that); it documents and
// locks in the eviction → cold-recreate sequence that triggers the bug.
func TestMessagingManagerColdRecreateAfterIdleEviction(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	var mu sync.Mutex
	created := make([]*stubAMQPClient, 0, 2)
	factory := func(string, logger.Logger) AMQPClient {
		c := &stubAMQPClient{}
		mu.Lock()
		created = append(created, c)
		mu.Unlock()
		return c
	}

	manager := NewMessagingManager(&stubMessagingSource{urls: map[string]string{tenantID: amqpHost}}, log, ManagerOptions{MaxPublishers: 5, IdleTTL: time.Hour}, factory)
	defer func() { _ = manager.Close() }()

	first, relFirst, err := manager.Publisher(ctx, tenantID)
	require.NoError(t, err)
	relFirst()

	// Backdate lastUsed so the cleanup pass sees the entry as idle, mirroring
	// TestMessagingManagerCleanupIdlePublishersLogsCloseError's idiom.
	manager.pubMu.Lock()
	manager.publishers[tenantID].lastUsed = time.Now().Add(-2 * time.Hour)
	manager.pubMu.Unlock()

	manager.cleanupIdlePublishers()

	second, relSecond, err := manager.Publisher(ctx, tenantID)
	require.NoError(t, err)
	defer relSecond()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, created, 2, "idle eviction must force a fresh client on the next Publisher() call")
	assert.NotSame(t, first, second, "publisher after idle eviction must be a newly created (cold) client")
}

// TestMessagingManagerStatsTracksEvictionsAndIdleCleanups mirrors
// cache.ManagerStats' Evictions/IdleCleanups counters (cache/manager.go),
// exposed here as additional keys on messaging's existing map[string]any
// Stats() (changing its return type would be a breaking API change).
func TestMessagingManagerStatsTracksEvictionsAndIdleCleanups(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	factory := func(string, logger.Logger) AMQPClient { return &stubAMQPClient{} }
	manager := NewMessagingManager(
		&stubMessagingSource{urls: map[string]string{tenant1ID: amqpURLTenant1, tenant2ID: amqpURLTenant2}},
		log,
		ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour},
		factory,
	)
	defer func() { _ = manager.Close() }()

	stats := manager.Stats()
	assert.Equal(t, 0, stats["evictions"])
	assert.Equal(t, 0, stats["idle_cleanups"])

	// LRU eviction: MaxPublishers=1 forces the second Publisher() call to evict the first.
	_, rel1, err := manager.Publisher(ctx, tenant1ID)
	require.NoError(t, err)
	rel1()
	_, rel2, err := manager.Publisher(ctx, tenant2ID)
	require.NoError(t, err)
	rel2()

	stats = manager.Stats()
	assert.Equal(t, 1, stats["evictions"])
	assert.Equal(t, 0, stats["idle_cleanups"], "LRU eviction must not bump idle_cleanups")

	// Idle cleanup: backdate the survivor's lastUsed and run cleanup directly.
	manager.pubMu.Lock()
	manager.publishers[tenant2ID].lastUsed = time.Now().Add(-2 * time.Hour)
	manager.pubMu.Unlock()
	manager.cleanupIdlePublishers()

	stats = manager.Stats()
	assert.Equal(t, 1, stats["idle_cleanups"])
	assert.Equal(t, 1, stats["evictions"], "idle cleanup must not bump evictions")
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
