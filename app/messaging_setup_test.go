package app

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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

// The coordinates of the one consumer declarationsWithConsumer declares. Named because the
// readiness tests assert they never reach the unauthenticated /ready body.
const (
	declaredQueue     = "orders.queue"
	declaredConsumer  = "orders-consumer"
	declaredEventType = "order.created"
)

// noopMessageHandler is a real (non-documentation-only) consumer handler, so the
// fixture below models a service that actually consumes.
type noopMessageHandler struct{}

func (noopMessageHandler) Handle(context.Context, *amqp.Delivery) error { return nil }
func (noopMessageHandler) EventType() string                            { return declaredEventType }

// declarationsWithConsumer builds the declaration set of a service that actually
// consumes — the only population whose failed bootstrap aborts startup.
func declarationsWithConsumer() *messaging.Declarations {
	decls := messaging.NewDeclarations()
	decls.RegisterQueue(&messaging.QueueDeclaration{Name: declaredQueue})
	decls.RegisterConsumer(&messaging.ConsumerDeclaration{
		Queue:     declaredQueue,
		Consumer:  declaredConsumer,
		EventType: declaredEventType,
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

// externalExchange is the exchange another service owns in the startup-wait
// tests; it appears in the broker's own 404 reason, which is what the abort
// error must carry through to the operator.
const externalExchange = "billing.events"

// errExternalExchangeMissing is the broker's answer to a passive declare for an
// exchange that does not exist yet (ADR-119) — the one failure the startup wait
// is allowed to retry.
var errExternalExchangeMissing = &amqp.Error{
	Code:   amqp.NotFound,
	Reason: "NOT_FOUND - no exchange '" + externalExchange + "' in vhost '/'",
}

// flakyBrokerURLProvider fails the first failures resolutions with err and then
// succeeds, counting every attempt. It stands in for a declare pass that answers
// 404 until the owning service declares the exchange: the attempt count is what
// the wait tests assert on, so ordering is observed rather than slept for.
type flakyBrokerURLProvider struct {
	mu       sync.Mutex
	calls    int
	failures int
	err      error
}

func (p *flakyBrokerURLProvider) BrokerURL(context.Context, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls <= p.failures {
		return "", p.err
	}
	return "amqp://guest:guest@localhost:5672/", nil
}

func (p *flakyBrokerURLProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// newExternalWaitApp wires a single-tenant App whose consumer bootstrap fails
// through source, with messaging.declare.externalwait set to wait.
func newExternalWaitApp(t *testing.T, source messaging.BrokerURLProvider, wait time.Duration) *App {
	t.Helper()
	log := logger.New("debug", true)
	manager := messaging.NewMessagingManager(source, log, messaging.ManagerOptions{},
		func(string, logger.Logger) messaging.AMQPClient {
			// Programmed to SUCCEED: once the broker URL resolves, the declare
			// pass and the subscribe must go through, or the wait's success arm
			// could never be observed.
			client := testmocks.NewMockAMQPClient()
			client.ExpectDeclareQueueAny(nil)
			client.ExpectDeclareExchangeAny(nil)
			client.ExpectBindQueueAny(nil)
			client.On("ConsumeFromQueue", mock.Anything, mock.Anything).Return(nil, nil)
			client.On("Close").Return(nil)
			return client
		})
	t.Cleanup(func() { _ = manager.Close() })
	return newMinimalMessagingApp(log, manager, &config.Config{
		Multitenant: config.MultitenantConfig{Enabled: false},
		Messaging:   config.MessagingConfig{Declare: config.DeclareConfig{ExternalWait: wait}},
	})
}

// TestPrepareRuntimeConsumersWaitsForExternalExchange is the feature: a
// consumer-declaring service deployed before the service that owns its external
// exchange starts consuming once the exchange appears, without a restart.
func TestPrepareRuntimeConsumersWaitsForExternalExchange(t *testing.T) {
	source := &flakyBrokerURLProvider{failures: 2, err: errExternalExchangeMissing}
	a := newExternalWaitApp(t, source, 2*time.Second)

	require.NoError(t, a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer()))
	assert.Greater(t, source.callCount(), 2, "the pass must be re-run until the exchange appears")
}

// TestPrepareRuntimeConsumersAbortsAfterExternalWaitElapses pins the bound: an
// exchange that never appears still aborts startup, and the operator gets the
// broker's own 404 naming it rather than a bare timeout.
func TestPrepareRuntimeConsumersAbortsAfterExternalWaitElapses(t *testing.T) {
	source := &flakyBrokerURLProvider{failures: math.MaxInt, err: errExternalExchangeMissing}
	a := newExternalWaitApp(t, source, 150*time.Millisecond)

	err := a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer())

	require.ErrorContains(t, err, externalExchange, "the abort must name the exchange the broker named")
	assert.Greater(t, source.callCount(), 1, "the wait must have re-run the pass at least once")
}

// TestPrepareRuntimeConsumersDoesNotWaitWhenExternalWaitZero pins the default:
// zero keeps the pre-key semantics exactly, which is one attempt and abort.
func TestPrepareRuntimeConsumersDoesNotWaitWhenExternalWaitZero(t *testing.T) {
	source := &flakyBrokerURLProvider{failures: math.MaxInt, err: errExternalExchangeMissing}
	a := newExternalWaitApp(t, source, 0)

	require.Error(t, a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer()))
	assert.Equal(t, 1, source.callCount(), "externalwait 0 must abort at once")
}

// TestPrepareRuntimeConsumersDoesNotWaitOnOtherErrors keeps the wait narrow:
// only a 404 is retried. Every other startup failure stays fatal immediately,
// which is what TestPrepareRuntimeConsumersFailsStartupOnEnsureError pins.
func TestPrepareRuntimeConsumersDoesNotWaitOnOtherErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "precondition_failed_is_not_retried", err: &amqp.Error{Code: amqp.PreconditionFailed, Reason: "PRECONDITION_FAILED - inequivalent arg"}},
		{name: "plain_error_is_not_retried", err: errBrokerLookupFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &flakyBrokerURLProvider{failures: math.MaxInt, err: tt.err}
			a := newExternalWaitApp(t, source, 2*time.Second)

			require.Error(t, a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer()))
			assert.Equal(t, 1, source.callCount(), "only a 404 is worth waiting on")
		})
	}
}

// TestPrepareRuntimeConsumersPublisherOnlyNeverWaits pins the rule that keeps
// the wait honest: it only DELAYS an abort that would otherwise happen, it never
// introduces one. A publisher-only service does not abort on this failure at
// all, so it must not be held at startup either — the next channel generation
// redeclares its topology.
func TestPrepareRuntimeConsumersPublisherOnlyNeverWaits(t *testing.T) {
	source := &flakyBrokerURLProvider{failures: math.MaxInt, err: errExternalExchangeMissing}
	a := newExternalWaitApp(t, source, 2*time.Second)

	require.NoError(t, a.prepareRuntimeConsumers(context.Background(), messaging.NewDeclarations()))
	assert.Equal(t, 1, source.callCount(), "a publisher-only service must not be held at startup")
}
