package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/messaging/streams"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

// Test helper modules
type simpleTestModule struct{}

func (m *simpleTestModule) Name() string             { return "simple-test-module" }
func (m *simpleTestModule) Init(_ *ModuleDeps) error { return nil }
func (m *simpleTestModule) Shutdown() error          { return nil }

// newMinimalMessagingApp builds the minimal App shared by the pre-warm and
// consumer-bootstrap test suites: a logger, the messaging manager under test,
// and the config each call site cares about, with the slots installed so the
// start phase is reachable.
func newMinimalMessagingApp(log logger.Logger, manager *messaging.Manager, cfg *config.Config) *App {
	a := &App{
		logger:           log,
		messagingManager: manager,
		cfg:              cfg,
	}
	a.installSlots(slotInputs{})
	return a
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
	a := newMinimalMessagingApp(log, newFailingConsumerManager(t, log, source),
		&config.Config{Multitenant: config.MultitenantConfig{Enabled: false}})

	err := a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer())

	require.Error(t, err)
	assert.Equal(t, 1, source.callCount(), "the error must come from consumer bootstrap")
	assert.ErrorIs(t, err, errBrokerLookupFailed) //nolint:testifylint // paired error-clause assertion follows
	require.ErrorContains(t, err, "failed to start consumers on the control-plane key")
}

// TestPrepareRuntimeConsumersWarnsOnlyWithoutConsumers pins the gate on the
// fatal path. A service that declared no consumers — including every service
// with no messaging configured at all, which reaches this call with an empty
// declaration set and an unresolvable broker URL — must still boot.
func TestPrepareRuntimeConsumersWarnsOnlyWithoutConsumers(t *testing.T) {
	log := logger.New("debug", true)
	source := &failingBrokerURLProvider{}
	a := newMinimalMessagingApp(log, newFailingConsumerManager(t, log, source),
		&config.Config{Multitenant: config.MultitenantConfig{Enabled: false}})

	require.NoError(t, a.prepareRuntimeConsumers(context.Background(), messaging.NewDeclarations()))
	assert.Equal(t, 1, source.callCount(), "topology setup must still be attempted")
}

// TestPrepareRuntimeConsumersSkipsEnsureInMultiTenantMode guards the other
// direction: multi-tenant consumers start lazily per tenant, so a broker that
// cannot be resolved at startup must not abort the boot.
func TestPrepareRuntimeConsumersSkipsEnsureInMultiTenantMode(t *testing.T) {
	log := logger.New("debug", true)
	source := &failingBrokerURLProvider{}
	a := newMinimalMessagingApp(log, newFailingConsumerManager(t, log, source),
		&config.Config{Multitenant: config.MultitenantConfig{Enabled: true}})

	require.NoError(t, a.prepareRuntimeConsumers(context.Background(), messaging.NewDeclarations()))
	assert.Zero(t, source.callCount(), "multi-tenant mode must not start consumers at startup")
}

// TestPrepareRuntimeConsumersSucceedsSingleTenant proves the fail-fast return
// is scoped to real failures: a reachable broker still boots green.
// TestTheTwoLanesShareOneStampSentinel is asserted from app because it is the only
// package that imports both: messaging/streams must not import messaging (import
// cycle), so neither lane's own tests can prove the two exported sentinels are one
// value. A consumer writing errors.Is(err, messaging.ErrTenantStampConflict) must
// match a refusal raised by either lane.
func TestTheTwoLanesShareOneStampSentinel(t *testing.T) {
	require.ErrorIs(t, streams.ErrTenantStampConflict, messaging.ErrTenantStampConflict)
	require.ErrorIs(t, messaging.ErrTenantStampConflict, streams.ErrTenantStampConflict)
	assert.Equal(t, messaging.TenantStampHeader, streams.TenantStampProperty,
		"both lanes must name the same carrier entry, or a stamp written by one is invisible to the other")
}

// TestPrepareRuntimeConsumersUnderSharedTenancy pins the control-plane branch:
// under messaging.tenancy: shared a multi-tenant deployment replays its declared
// consumers ONCE on the control-plane key at boot, exactly as single-tenant does,
// instead of deferring them to a per-tenant replay that never comes.
func TestPrepareRuntimeConsumersUnderSharedTenancy(t *testing.T) {
	sharedCfg := func() *config.Config {
		return &config.Config{
			Multitenant: config.MultitenantConfig{Enabled: true},
			Messaging:   config.MessagingConfig{Tenancy: config.TenancyShared},
		}
	}

	t.Run("shared_replays_on_control_plane_key", func(t *testing.T) {
		log := logger.New("debug", true)
		source := &failingBrokerURLProvider{}
		a := newMinimalMessagingApp(log, newFailingConsumerManager(t, log, source), sharedCfg())

		err := a.prepareRuntimeConsumers(context.Background(), messaging.NewDeclarations())

		require.NoError(t, err, "no declared consumers means the failure is advisory")
		assert.Positive(t, source.callCount(),
			"shared tenancy must reach consumer bootstrap on the control-plane key")
	})

	t.Run("per_tenant_still_skips", func(t *testing.T) {
		log := logger.New("debug", true)
		source := &failingBrokerURLProvider{}
		a := newMinimalMessagingApp(log, newFailingConsumerManager(t, log, source),
			&config.Config{
				Multitenant: config.MultitenantConfig{Enabled: true},
				Messaging:   config.MessagingConfig{Tenancy: config.TenancyPerTenant},
			})

		require.NoError(t, a.prepareRuntimeConsumers(context.Background(), messaging.NewDeclarations()))
		assert.Zero(t, source.callCount(), "per-tenant tenancy must not start consumers at startup")
	})
}

func TestPrepareRuntimeConsumersSucceedsSingleTenant(t *testing.T) {
	log := logger.New("debug", true)
	client := testmocks.NewMockAMQPClient()
	client.ExpectClose(nil)
	manager := messaging.NewMessagingManager(
		&fakeBrokerURLProvider{url: "amqp://localhost"}, log, messaging.ManagerOptions{},
		func(string, logger.Logger) messaging.AMQPClient { return client })
	defer func() { _ = manager.Close() }()

	a := newMinimalMessagingApp(log, manager, &config.Config{Multitenant: config.MultitenantConfig{Enabled: false}})

	require.NoError(t, a.prepareRuntimeConsumers(context.Background(), messaging.NewDeclarations()))
}

// TestPrepareRuntimeConsumersNoOpsWithoutManagerOrDeclarations pins the single
// guard that replaced the old two-layer one: nothing is attempted, and nothing
// fails, when there is no messaging manager or nothing to replay.
func TestPrepareRuntimeConsumersNoOpsWithoutManagerOrDeclarations(t *testing.T) {
	log := logger.New("debug", true)

	t.Run("nil_manager", func(t *testing.T) {
		a := newMinimalMessagingApp(log, nil, &config.Config{Multitenant: config.MultitenantConfig{Enabled: false}})
		require.NoError(t, a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer()))
	})

	t.Run("nil_declarations", func(t *testing.T) {
		source := &failingBrokerURLProvider{}
		a := newMinimalMessagingApp(log, newFailingConsumerManager(t, log, source),
			&config.Config{Multitenant: config.MultitenantConfig{Enabled: false}})
		require.NoError(t, a.prepareRuntimeConsumers(context.Background(), nil))
		assert.Zero(t, source.callCount(), "no declarations means nothing to replay")
	})
}
