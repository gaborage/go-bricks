package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

func TestCollectDeclarations(t *testing.T) {
	t.Run("collects declarations from empty registry", func(t *testing.T) {
		log := logger.New("debug", true)
		initializer := NewMessagingInitializer(log, nil, false)

		// Create empty registry
		deps := &ModuleDeps{
			Logger: log,
		}
		registry := NewModuleRegistry(deps)

		// Test CollectDeclarations with empty registry
		declarations, err := initializer.CollectDeclarations(registry)
		assert.NoError(t, err)
		assert.NotNil(t, declarations)
	})

	t.Run("collects declarations from registry with simple module", func(t *testing.T) {
		log := logger.New("debug", true)
		initializer := NewMessagingInitializer(log, nil, false)

		// Create a simple module that doesn't declare messaging
		module := &simpleTestModule{}
		deps := &ModuleDeps{
			Logger: log,
		}

		// Create registry and register module
		registry := NewModuleRegistry(deps)
		err := registry.Register(module)
		require.NoError(t, err)

		// Test CollectDeclarations
		declarations, err := initializer.CollectDeclarations(registry)
		assert.NoError(t, err)
		assert.NotNil(t, declarations)
	})
}

func TestSetupMultiTenantLazyInit(t *testing.T) {
	t.Run("sets declarations on provider", func(t *testing.T) {
		log := logger.New("debug", true)
		initializer := NewMessagingInitializer(log, nil, true)

		// Create a multi-tenant resource provider
		provider := &MultiTenantResourceProvider{}

		// Create declarations
		declarations := messaging.NewDeclarations()

		// Call setupMultiTenantLazyInit
		err := initializer.setupMultiTenantLazyInit(provider, declarations)

		assert.NoError(t, err)
		assert.Equal(t, declarations, provider.declarations)
	})
}

func TestPrepareRuntimeConsumers(t *testing.T) {
	t.Run("returns error when manager is nil", func(t *testing.T) {
		log := logger.New("debug", true)
		initializer := NewMessagingInitializer(log, nil, false)

		declarations := messaging.NewDeclarations()
		ctx := context.Background()

		err := initializer.PrepareRuntimeConsumers(ctx, declarations)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "messaging manager not configured")
	})
}

// Test helper modules
type simpleTestModule struct{}

func (m *simpleTestModule) Name() string             { return "simple-test-module" }
func (m *simpleTestModule) Init(_ *ModuleDeps) error { return nil }
func (m *simpleTestModule) Shutdown() error          { return nil }

func TestSetupLazyConsumerInit(t *testing.T) {
	t.Run("returns error when manager is nil", func(t *testing.T) {
		log := logger.New("debug", true)
		initializer := NewMessagingInitializer(log, nil, false)
		declarations := messaging.NewDeclarations()

		err := initializer.SetupLazyConsumerInit(nil, declarations)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "messaging manager not configured")
	})

	t.Run("sets up single tenant lazy init", func(t *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		initializer := NewMessagingInitializer(log, manager, false)

		provider := &SingleTenantResourceProvider{}
		declarations := messaging.NewDeclarations()

		err := initializer.SetupLazyConsumerInit(provider, declarations)
		assert.NoError(t, err)
		assert.Equal(t, declarations, provider.declarations)
	})

	t.Run("sets up multi tenant lazy init", func(t *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		initializer := NewMessagingInitializer(log, manager, true)

		provider := &MultiTenantResourceProvider{}
		declarations := messaging.NewDeclarations()

		err := initializer.SetupLazyConsumerInit(provider, declarations)
		assert.NoError(t, err)
		assert.Equal(t, declarations, provider.declarations)
	})

	t.Run("warns for unknown provider type", func(t *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		initializer := NewMessagingInitializer(log, manager, false)

		// Use an unknown provider type that implements ResourceProvider
		provider := &unknownResourceProvider{}
		declarations := messaging.NewDeclarations()

		err := initializer.SetupLazyConsumerInit(provider, declarations)
		assert.NoError(t, err) // Should not error, just warn
	})
}

func TestPrepareRuntimeConsumersComprehensive(t *testing.T) {
	t.Run("multi-tenant mode logs and returns success", func(t *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		initializer := NewMessagingInitializer(log, manager, true)

		declarations := messaging.NewDeclarations()
		ctx := context.Background()

		err := initializer.PrepareRuntimeConsumers(ctx, declarations)
		assert.NoError(t, err)
	})
}

// errBrokerLookupFailed stands in for the broker-config and broker-availability
// failures that make single-tenant consumer bootstrap fail at startup.
var errBrokerLookupFailed = errors.New("broker lookup failed")

// failingBrokerURLProvider fails every broker-URL resolution and counts the
// attempts, so a test can prove consumer bootstrap was reached — or never was.
type failingBrokerURLProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *failingBrokerURLProvider) BrokerURL(context.Context, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return "", errBrokerLookupFailed
}

func (p *failingBrokerURLProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// newFailingConsumerManager wires a *messaging.Manager whose consumer bootstrap
// always fails at broker-URL resolution, before any AMQP client is created.
func newFailingConsumerManager(t *testing.T, log logger.Logger, source messaging.BrokerURLProvider) *messaging.Manager {
	t.Helper()
	return messaging.NewMessagingManager(source, log, messaging.ManagerOptions{},
		func(string, logger.Logger) messaging.AMQPClient {
			t.Errorf("client factory must not run when broker URL resolution fails")
			return nil
		})
}

// noopMessageHandler is a real (non-documentation-only) consumer handler, so the
// fixture below models a service that actually consumes.
type noopMessageHandler struct{}

func (noopMessageHandler) Handle(context.Context, *amqp.Delivery) error { return nil }
func (noopMessageHandler) EventType() string                            { return "order.created" }

// declarationsWithConsumer builds the declaration set of a service that actually
// consumes — the only population whose failed bootstrap aborts startup.
func declarationsWithConsumer() *messaging.Declarations {
	decls := messaging.NewDeclarations()
	decls.RegisterConsumer(&messaging.ConsumerDeclaration{
		Queue:     "orders.queue",
		Consumer:  "orders-consumer",
		EventType: "order.created",
		Handler:   noopMessageHandler{},
	})
	return decls
}

// TestPrepareRuntimeConsumersFailsStartupOnEnsureError pins the fail-fast
// contract: a single-tenant service that declared consumers and cannot start
// them must abort startup rather than boot deaf, serving HTTP while consuming
// nothing.
func TestPrepareRuntimeConsumersFailsStartupOnEnsureError(t *testing.T) {
	log := logger.New("debug", true)
	source := &failingBrokerURLProvider{}
	initializer := NewMessagingInitializer(log, newFailingConsumerManager(t, log, source), false)

	err := initializer.PrepareRuntimeConsumers(context.Background(), declarationsWithConsumer())

	require.Error(t, err)
	assert.ErrorIs(t, err, errBrokerLookupFailed)
	assert.ErrorContains(t, err, "failed to start single-tenant consumers")
	assert.Equal(t, 1, source.callCount(), "the error must come from consumer bootstrap")
}

// TestPrepareRuntimeConsumersWarnsOnlyWithoutConsumers pins the gate on the
// fatal path. A service that declared no consumers — including every service
// with no messaging configured at all, which reaches this call with an empty
// declaration set and an unresolvable broker URL — must still boot.
func TestPrepareRuntimeConsumersWarnsOnlyWithoutConsumers(t *testing.T) {
	log := logger.New("debug", true)
	source := &failingBrokerURLProvider{}
	initializer := NewMessagingInitializer(log, newFailingConsumerManager(t, log, source), false)

	require.NoError(t, initializer.PrepareRuntimeConsumers(context.Background(), messaging.NewDeclarations()))
	assert.Equal(t, 1, source.callCount(), "topology setup must still be attempted")
}

// TestPrepareRuntimeConsumersToleratesNilDeclarations guards the fatality gate's
// own dereference: messaging.EnsureConsumers documents that this call site
// forwards its argument unguarded, so a nil set must warn rather than panic.
func TestPrepareRuntimeConsumersToleratesNilDeclarations(t *testing.T) {
	log := logger.New("debug", true)
	source := &failingBrokerURLProvider{}
	initializer := NewMessagingInitializer(log, newFailingConsumerManager(t, log, source), false)

	require.NotPanics(t, func() {
		require.NoError(t, initializer.PrepareRuntimeConsumers(context.Background(), nil))
	})
}

// TestPrepareRuntimeConsumersSkipsEnsureInMultiTenantMode guards the other
// direction: multi-tenant consumers start lazily per tenant, so a broker that
// cannot be resolved at startup must not abort the boot.
func TestPrepareRuntimeConsumersSkipsEnsureInMultiTenantMode(t *testing.T) {
	log := logger.New("debug", true)
	source := &failingBrokerURLProvider{}
	initializer := NewMessagingInitializer(log, newFailingConsumerManager(t, log, source), true)

	require.NoError(t, initializer.PrepareRuntimeConsumers(context.Background(), messaging.NewDeclarations()))
	assert.Zero(t, source.callCount(), "multi-tenant mode must not start consumers at startup")
}

// TestPrepareRuntimeConsumersSucceedsSingleTenant proves the fail-fast return
// is scoped to real failures: a reachable broker still boots green.
func TestPrepareRuntimeConsumersSucceedsSingleTenant(t *testing.T) {
	log := logger.New("debug", true)
	client := testmocks.NewMockAMQPClient()
	client.ExpectClose(nil)
	manager := messaging.NewMessagingManager(
		&fakeBrokerURLProvider{url: "amqp://localhost"}, log, messaging.ManagerOptions{},
		func(string, logger.Logger) messaging.AMQPClient { return client })
	defer func() { _ = manager.Close() }()

	initializer := NewMessagingInitializer(log, manager, false)

	require.NoError(t, initializer.PrepareRuntimeConsumers(context.Background(), messaging.NewDeclarations()))
}

func TestIsAvailable(t *testing.T) {
	t.Run("returns true when manager is available", func(t *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		initializer := NewMessagingInitializer(log, manager, false)

		assert.True(t, initializer.IsAvailable())
	})

	t.Run("returns false when manager is nil", func(t *testing.T) {
		log := logger.New("debug", true)
		initializer := NewMessagingInitializer(log, nil, false)

		assert.False(t, initializer.IsAvailable())
	})
}

func TestLogDeploymentMode(t *testing.T) {
	t.Run("logs multi-tenant mode", func(_ *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		initializer := NewMessagingInitializer(log, manager, true)

		// This test primarily ensures the function runs without panic
		// In a real scenario, you might capture logs to verify content
		initializer.LogDeploymentMode()
	})

	t.Run("logs single-tenant mode", func(_ *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		initializer := NewMessagingInitializer(log, manager, false)

		// This test primarily ensures the function runs without panic
		initializer.LogDeploymentMode()
	})
}

func TestNewMessagingInitializer(t *testing.T) {
	t.Run("creates initializer with correct fields", func(t *testing.T) {
		log := logger.New("debug", true)
		manager := &messaging.Manager{}
		multiTenant := true

		initializer := NewMessagingInitializer(log, manager, multiTenant)

		assert.NotNil(t, initializer)
		assert.Equal(t, multiTenant, initializer.multiTenant)
		assert.Equal(t, manager, initializer.manager)
		assert.Equal(t, log, initializer.logger)
	})
}

// Mock types for testing
type unknownResourceProvider struct{}

func (u *unknownResourceProvider) DB(_ context.Context) (database.Interface, error) {
	return nil, nil
}

func (u *unknownResourceProvider) DBByName(_ context.Context, _ string) (database.Interface, error) {
	return nil, nil
}

func (u *unknownResourceProvider) Messaging(_ context.Context) (messaging.AMQPClient, error) {
	return nil, nil
}

func (u *unknownResourceProvider) Cache(_ context.Context) (cache.Cache, error) {
	return nil, nil
}
