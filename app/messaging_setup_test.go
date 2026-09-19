package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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

// scriptedBrokerURLProvider scripts broker-URL resolution and counts the attempts,
// so a test can prove consumer bootstrap was reached — or never was. The zero
// value fails every call with errBrokerLookupFailed; succeedAfter lets it start
// succeeding, standing in for an external exchange that appears mid-wait.
type scriptedBrokerURLProvider struct {
	mu           sync.Mutex
	calls        int
	succeedAfter int
	err          error
}

func (p *scriptedBrokerURLProvider) BrokerURL(context.Context, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.succeedAfter > 0 && p.calls > p.succeedAfter {
		return "amqp://localhost", nil
	}
	if p.err != nil {
		return "", p.err
	}
	return "", errBrokerLookupFailed
}

func (p *scriptedBrokerURLProvider) callCount() int {
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
	source := &scriptedBrokerURLProvider{}
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
	source := &scriptedBrokerURLProvider{}
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
	source := &scriptedBrokerURLProvider{}
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
		source := &scriptedBrokerURLProvider{}
		a := newMinimalMessagingApp(log, newFailingConsumerManager(t, log, source), sharedCfg())

		err := a.prepareRuntimeConsumers(context.Background(), messaging.NewDeclarations())

		require.NoError(t, err, "no declared consumers means the failure is advisory")
		assert.Positive(t, source.callCount(),
			"shared tenancy must reach consumer bootstrap on the control-plane key")
	})

	t.Run("per_tenant_still_skips", func(t *testing.T) {
		log := logger.New("debug", true)
		source := &scriptedBrokerURLProvider{}
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
		source := &scriptedBrokerURLProvider{}
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
// exchange that does not exist yet (ADR-119) — the one refusal the startup wait
// retries.
var errExternalExchangeMissing = &amqp.Error{
	Code:   amqp.NotFound,
	Reason: "NOT_FOUND - no exchange '" + externalExchange + "' in vhost '/'",
}

// newExternalWaitApp wires a single-tenant App whose consumer bootstrap fails
// through source, with messaging.declare.externalwait set to wait. The client is
// programmed to SUCCEED, so a source that starts resolving lets the declare pass
// complete and the wait's success arm becomes observable.
func newExternalWaitApp(t *testing.T, source messaging.BrokerURLProvider, wait time.Duration) *App {
	t.Helper()
	log := logger.New("debug", true)
	manager := messaging.NewMessagingManager(source, log, messaging.ManagerOptions{},
		func(string, logger.Logger) messaging.AMQPClient {
			client := testmocks.NewMockAMQPClient()
			client.ExpectDeclareQueueAny(nil)
			client.ExpectDeclareExchangeAny(nil)
			client.ExpectBindQueueAny(nil)
			client.On("ConsumeFromQueue", mock.Anything, mock.Anything).Return(nil, nil)
			client.ExpectClose(nil)
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
	source := &scriptedBrokerURLProvider{succeedAfter: 2, err: errExternalExchangeMissing}
	a := newExternalWaitApp(t, source, 600*time.Millisecond)

	require.NoError(t, a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer()))
	assert.Greater(t, source.callCount(), 2, "the pass must be re-run until the exchange appears")
}

// TestPrepareRuntimeConsumersWaitsOnADeclarePass404 injects the 404 where a real
// one arrives — the declare pass against a live client — rather than at broker-URL
// resolution, which the other tests use and which fails before the dial.
func TestPrepareRuntimeConsumersWaitsOnADeclarePass404(t *testing.T) {
	log := logger.New("debug", true)
	var declares atomic.Int32
	manager := messaging.NewMessagingManager(&fixedBrokerURLProvider{}, log, messaging.ManagerOptions{},
		func(string, logger.Logger) messaging.AMQPClient {
			client := testmocks.NewMockAMQPClient()
			// The first two passes answer 404 as an absent external exchange
			// does; the third declares cleanly.
			var declareErr error
			if declares.Add(1) <= 2 {
				declareErr = errExternalExchangeMissing
			}
			client.ExpectDeclareQueueAny(declareErr)
			client.ExpectDeclareExchangeAny(declareErr)
			client.ExpectBindQueueAny(nil)
			client.On("ConsumeFromQueue", mock.Anything, mock.Anything).Return(nil, nil)
			client.ExpectClose(nil)
			return client
		})
	t.Cleanup(func() { _ = manager.Close() })
	a := newMinimalMessagingApp(log, manager, &config.Config{
		Multitenant: config.MultitenantConfig{Enabled: false},
		Messaging:   config.MessagingConfig{Declare: config.DeclareConfig{ExternalWait: 600 * time.Millisecond}},
	})

	require.NoError(t, a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer()))
	assert.Greater(t, int(declares.Load()), 2, "the declare pass must have been re-run")
}

// fixedBrokerURLProvider always resolves, so a test can put the failure in the
// declare pass instead of ahead of the dial.
type fixedBrokerURLProvider struct{}

func (*fixedBrokerURLProvider) BrokerURL(context.Context, string) (string, error) {
	return "amqp://localhost", nil
}

// TestPrepareRuntimeConsumersAbortsAfterExternalWaitElapses pins the bound: an
// exchange that never appears still aborts startup, the operator gets the
// broker's own 404 naming it rather than a bare timeout, and the abort does not
// overshoot the configured budget by a whole backoff ceiling.
func TestPrepareRuntimeConsumersAbortsAfterExternalWaitElapses(t *testing.T) {
	const wait = 300 * time.Millisecond
	source := &scriptedBrokerURLProvider{err: errExternalExchangeMissing}
	a := newExternalWaitApp(t, source, wait)

	start := time.Now()
	err := a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer())
	elapsed := time.Since(start)

	require.ErrorContains(t, err, externalExchange, "the abort must name the exchange the broker named")
	assert.Greater(t, source.callCount(), 1, "the wait must have re-run the pass at least once")
	// Tight on purpose: the clamped schedule at this budget is 75+150+75ms, the
	// unclamped one 75+150+300ms. A bound of wait+ceiling would accept both, so
	// it would pin nothing — and gremlins does not mutate min().
	assert.Less(t, elapsed, wait+200*time.Millisecond,
		"the last sleep must be clamped to the remaining budget, not the backoff ceiling")
	// The backoff must actually grow. With the loop counter walking backwards the
	// shift goes negative, every gap collapses to zero and the budget is spent
	// hammering the broker instead of waiting on it — same error, same elapsed
	// time, so only the attempt COUNT can see it.
	assert.Less(t, source.callCount(), 10,
		"a growing backoff must bound the attempts; a collapsed one spins")
}

// TestPrepareRuntimeConsumersHonorsACancelableContextDuringTheWait pins the
// loop's ctx arm. It is DEFENSIVE: slot.go hands this function
// context.WithoutCancel over a Background root, so Done() is nil and the arm
// cannot fire in production. It exists so the loop is already correct if that
// wrapper changes, per context_deadlines.md.
//
// The context must still be LIVE for the first pass: a context already canceled
// on entry is refused by Manager.EnsureConsumers with a context error, which is
// not a 404, so the wait never engages and the arm is never reached — an earlier
// version of this test canceled up front and passed with the arm deleted.
func TestPrepareRuntimeConsumersHonorsACancelableContextDuringTheWait(t *testing.T) {
	source := &scriptedBrokerURLProvider{err: errExternalExchangeMissing}
	a := newExternalWaitApp(t, source, time.Hour)

	// Expires while the loop is in its first backoff, which is 1s at this budget.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	require.Error(t, a.prepareRuntimeConsumers(ctx, declarationsWithConsumer()))

	// Attempt count, not elapsed time: without the arm the loop still returns
	// after ONE backoff, because the next EnsureConsumers is refused by the
	// expired context — about a second, which any generous time bound would
	// have accepted. The arm's actual effect is that the second pass never runs.
	assert.Equal(t, 1, source.callCount(),
		"cancellation during the backoff must return before re-running the pass")
}

// TestPrepareRuntimeConsumersWaitsUnderSharedTenancy pins the arm three doc
// pages now promise: messaging.tenancy: shared makes a multi-tenant deployment
// run the control-plane startup pass, so the wait applies there too.
// perTenantMessaging() is multiTenant() && !sharedMessaging(), and only the
// per-tenant half returns early.
func TestPrepareRuntimeConsumersWaitsUnderSharedTenancy(t *testing.T) {
	source := &scriptedBrokerURLProvider{succeedAfter: 2, err: errExternalExchangeMissing}
	log := logger.New("debug", true)
	manager := messaging.NewMessagingManager(source, log, messaging.ManagerOptions{},
		func(string, logger.Logger) messaging.AMQPClient {
			client := testmocks.NewMockAMQPClient()
			client.ExpectDeclareQueueAny(nil)
			client.ExpectDeclareExchangeAny(nil)
			client.ExpectBindQueueAny(nil)
			client.On("ConsumeFromQueue", mock.Anything, mock.Anything).Return(nil, nil)
			client.ExpectClose(nil)
			return client
		})
	t.Cleanup(func() { _ = manager.Close() })
	a := newMinimalMessagingApp(log, manager, &config.Config{
		Multitenant: config.MultitenantConfig{Enabled: true},
		Messaging: config.MessagingConfig{
			Tenancy: config.TenancyShared,
			Declare: config.DeclareConfig{ExternalWait: 600 * time.Millisecond},
		},
	})

	require.NoError(t, a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer()))
	assert.Greater(t, source.callCount(), 2, "the shared control-plane pass must be re-run too")
}

// TestPrepareRuntimeConsumersPerTenantNeverWaits pins the spec's last sentence.
// A per-tenant deployment declares lazily inside a request, so there is no
// startup pass to hold: it must return before the wait is even reached. The
// assertion is on the attempt count, which would move the moment the wait was
// pushed down into the manager.
func TestPrepareRuntimeConsumersPerTenantNeverWaits(t *testing.T) {
	source := &scriptedBrokerURLProvider{err: errExternalExchangeMissing}
	log := logger.New("debug", true)
	manager := messaging.NewMessagingManager(source, log, messaging.ManagerOptions{},
		func(string, logger.Logger) messaging.AMQPClient { return testmocks.NewMockAMQPClient() })
	t.Cleanup(func() { _ = manager.Close() })
	a := newMinimalMessagingApp(log, manager, &config.Config{
		Multitenant: config.MultitenantConfig{Enabled: true},
		Messaging: config.MessagingConfig{
			Tenancy: config.TenancyPerTenant,
			Declare: config.DeclareConfig{ExternalWait: time.Hour},
		},
	})

	require.NoError(t, a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer()))
	assert.Zero(t, source.callCount(), "a per-tenant deployment must not run a startup pass at all")
}

// capturingLogger records WARN messages through the logger.Logger seam the App
// already takes, so a test can pin a log line without swapping os.Stdout — the
// package's other capture helper does that, and it races against the manager
// goroutines these tests leave shutting down.
type capturingLogger struct {
	mu     sync.Mutex
	warns  []string
	debugs []loggedLine
}

// loggedLine keeps the int fields alongside the message, so a test can assert the
// value a line reports and not merely that it was emitted.
type loggedLine struct {
	msg  string
	ints map[string]int
}

func (l *capturingLogger) record(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg)
}

func (l *capturingLogger) recordDebug(line loggedLine) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.debugs = append(l.debugs, line)
}

// firstDebug returns the first DEBUG line whose message contains substr.
func (l *capturingLogger) firstDebug(substr string) (loggedLine, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, d := range l.debugs {
		if strings.Contains(d.msg, substr) {
			return d, true
		}
	}
	return loggedLine{}, false
}

func (l *capturingLogger) warned(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, w := range l.warns {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func (l *capturingLogger) Warn() logger.LogEvent         { return &capturingEvent{sink: l} }
func (l *capturingLogger) Info() logger.LogEvent         { return &capturingEvent{} }
func (l *capturingLogger) Error() logger.LogEvent        { return &capturingEvent{} }
func (l *capturingLogger) Debug() logger.LogEvent        { return &capturingEvent{debug: l} }
func (l *capturingLogger) Fatal() logger.LogEvent        { return &capturingEvent{} }
func (l *capturingLogger) WithContext(any) logger.Logger { return l }
func (l *capturingLogger) WithFields(map[string]any) logger.Logger {
	return l
}

// capturingEvent drops every field; only the message matters here. A nil sink is
// a level the test does not record.
type capturingEvent struct {
	sink  *capturingLogger
	debug *capturingLogger
	ints  map[string]int
}

func (e *capturingEvent) Str(_, _ string) logger.LogEvent       { return e }
func (e *capturingEvent) Err(error) logger.LogEvent             { return e }
func (e *capturingEvent) Uint64(string, uint64) logger.LogEvent { return e }
func (e *capturingEvent) Int(k string, v int) logger.LogEvent {
	if e.ints == nil {
		e.ints = map[string]int{}
	}
	e.ints[k] = v
	return e
}
func (e *capturingEvent) Int64(string, int64) logger.LogEvent       { return e }
func (e *capturingEvent) Dur(string, time.Duration) logger.LogEvent { return e }
func (e *capturingEvent) Interface(string, any) logger.LogEvent     { return e }
func (e *capturingEvent) Bytes(string, []byte) logger.LogEvent      { return e }
func (e *capturingEvent) Bool(string, bool) logger.LogEvent         { return e }
func (e *capturingEvent) Enabled() bool                             { return true }
func (e *capturingEvent) Msg(msg string) {
	if e.sink != nil {
		e.sink.record(msg)
	}
	if e.debug != nil {
		e.debug.recordDebug(loggedLine{msg: msg, ints: e.ints})
	}
}
func (e *capturingEvent) Msgf(format string, args ...any) { e.Msg(fmt.Sprintf(format, args...)) }

// TestPrepareRuntimeConsumersAnnouncesTheWaitOnlyWhenItWaits pins the `wait <= 0`
// clause, which attempt counts cannot see: weakened to `wait < 0`, a zero wait
// still falls through to a deadline of now and returns on the first
// `remaining <= 0`, so it makes exactly one attempt either way. The only
// observable difference is this WARN, announcing a wait that will not happen.
func TestPrepareRuntimeConsumersAnnouncesTheWaitOnlyWhenItWaits(t *testing.T) {
	const announcement = "re-running the startup declare pass"

	run := func(wait time.Duration) *capturingLogger {
		log := &capturingLogger{}
		source := &scriptedBrokerURLProvider{err: errExternalExchangeMissing}
		manager := messaging.NewMessagingManager(source, logger.New("error", false), messaging.ManagerOptions{},
			func(string, logger.Logger) messaging.AMQPClient { return testmocks.NewMockAMQPClient() })
		t.Cleanup(func() { _ = manager.Close() })
		a := newMinimalMessagingApp(log, manager, &config.Config{
			Multitenant: config.MultitenantConfig{Enabled: false},
			Messaging:   config.MessagingConfig{Declare: config.DeclareConfig{ExternalWait: wait}},
		})
		_ = a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer())
		return log
	}

	assert.False(t, run(0).warned(announcement), "externalwait 0 must not announce a wait")
	assert.True(t, run(150*time.Millisecond).warned(announcement), "a configured wait must be announced")
}

// TestPrepareRuntimeConsumersNumbersItsRetriesFromOne pins the attempt number the
// progress line reports. The loop counter is zero-based, so the line adds one; the
// value is otherwise unobservable, and an operator reading "attempt 0" for the
// first retry would mis-count how much of the budget is gone.
func TestPrepareRuntimeConsumersNumbersItsRetriesFromOne(t *testing.T) {
	log := &capturingLogger{}
	source := &scriptedBrokerURLProvider{err: errExternalExchangeMissing}
	manager := messaging.NewMessagingManager(source, logger.New("error", false), messaging.ManagerOptions{},
		func(string, logger.Logger) messaging.AMQPClient { return testmocks.NewMockAMQPClient() })
	t.Cleanup(func() { _ = manager.Close() })
	a := newMinimalMessagingApp(log, manager, &config.Config{
		Multitenant: config.MultitenantConfig{Enabled: false},
		Messaging:   config.MessagingConfig{Declare: config.DeclareConfig{ExternalWait: 300 * time.Millisecond}},
	})

	require.Error(t, a.prepareRuntimeConsumers(context.Background(), declarationsWithConsumer()))

	line, ok := log.firstDebug("still absent")
	require.True(t, ok, "each retry must report progress")
	assert.Equal(t, 1, line.ints["attempt"], "the first retry is attempt 1, not 0")
}

// TestPrepareRuntimeConsumersNeverWaits collects the arms where the wait must not
// engage at all — one attempt, then today's behavior. Each row pins one clause
// of the guard: externalwait 0, a non-404 refusal, and a publisher-only service,
// which has no abort to delay and so must not be held at startup.
func TestPrepareRuntimeConsumersNeverWaits(t *testing.T) {
	tests := []struct {
		name    string
		wait    time.Duration
		err     error
		decls   *messaging.Declarations
		wantErr bool
	}{
		{
			name:    "externalwait_zero_aborts_at_once",
			wait:    0,
			err:     errExternalExchangeMissing,
			decls:   declarationsWithConsumer(),
			wantErr: true,
		},
		{
			name:    "precondition_failed_is_not_retried",
			wait:    2 * time.Second,
			err:     &amqp.Error{Code: amqp.PreconditionFailed, Reason: "PRECONDITION_FAILED - inequivalent arg"},
			decls:   declarationsWithConsumer(),
			wantErr: true,
		},
		{
			name:    "plain_error_is_not_retried",
			wait:    2 * time.Second,
			err:     errBrokerLookupFailed,
			decls:   declarationsWithConsumer(),
			wantErr: true,
		},
		{
			// No abort to delay: a publisher-only service warns and continues
			// on this failure, so holding it at startup would buy nothing.
			name:  "publisher_only_is_never_held",
			wait:  2 * time.Second,
			err:   errExternalExchangeMissing,
			decls: messaging.NewDeclarations(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &scriptedBrokerURLProvider{err: tt.err}
			a := newExternalWaitApp(t, source, tt.wait)

			err := a.prepareRuntimeConsumers(context.Background(), tt.decls)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, 1, source.callCount(), "the pass must have run exactly once")
		})
	}
}
