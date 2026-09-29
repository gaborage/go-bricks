package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/config"
)

// A slot is the framework-side module that owns one resource kind's application lifecycle —
// probe, pre-init, start, stop, close — so that adding a kind is one slot, not an edit in
// every place that enumerates kinds (CONTEXT.md, ADR-067).
//
// ADR-045: the interface lives in app/ and names only what app calls. The managers behind
// it (database.DbManager, messaging.Manager, cache.CacheManager, streams.Manager) know
// nothing about it.
type resourceSlot interface {
	// name is the kind's fixed component identifier, used in the startup log lines, the fatal
	// startup error, the `Readiness check failed` line, the debug view and the readiness
	// gauge. seal stamps it onto what describe() built — never a tenant, host or database
	// name.
	name() string

	// describe builds the kind's probe description, and reports whether the kind renders at
	// all. It is called once per slot, from the startSlots walk. Only the streams slot
	// withholds a description — see streamsSlot.describe.
	describe() (probeDescription, bool)

	// seal stores what describe built, stamping the slot's own kind onto it as the rendered
	// name. Called exactly once, inside the startSlots walk, immediately after this slot's
	// start returned without a fatal error; nothing rewrites it afterwards, so a /ready
	// overlapping stopSlots reads a stable value.
	seal(description probeDescription, shown bool)

	// readiness returns the description this slot sealed, or nil when the kind renders
	// nothing — it has not sealed yet, or its describe withheld one. The pointer aims into
	// the slot's own storage, so reading it allocates nothing; Run's own allocations are
	// unaffected.
	readiness() *probeDescription

	// preInit establishes the kind's fixed-"" -key connection during Builder construction.
	// It returns the raw failure; preInitFatal decides what that costs.
	preInit(ctx context.Context) error

	// preInitFatal reports whether a preInit failure aborts startup.
	preInitFatal() bool

	// start brings the kind up in prepareRuntime. A non-nil fatal aborts startup at once; a
	// non-nil advisory is aggregated into the single pre-warm WARN and never fails startup.
	start(ctx context.Context) (advisory, fatal error)

	// stop halts the kind's inbound work before module Shutdown (ADR-029). It never closes
	// connections — that is the close phase, which runs after modules are torn down.
	stop(ctx context.Context)

	// closer hands over the resource the close phase must Close. ok is false when the kind
	// has nothing to close yet, which is how an unconfigured kind and a streams manager that
	// has not started stay out of the FIFO close list. Builder.RegisterClosers walks every
	// slot's closer at build time, for the kinds whose manager exists by then; the streams
	// slot calls its own again from start, once its manager exists.
	closer() (namedCloser, bool)
}

var (
	_ resourceSlot = (*databaseSlot)(nil)
	_ resourceSlot = (*messagingSlot)(nil)
	_ resourceSlot = (*cacheSlot)(nil)
	_ resourceSlot = (*streamsSlot)(nil)
)

// installSlots builds the one slot list every lifecycle phase walks, in the one
// registration order: database → messaging → cache → streams. Close stays FIFO over the
// same order. Each slot holds the App rather than a snapshot of its managers, so a manager
// swapped in later (the streams manager, which only exists after start) is seen by the next
// walk without rebuilding the list. It is the one writer of App.plan, and hands each
// slot its own kind's row of it.
func (a *App) installSlots(plan resourcePlan) {
	a.plan = plan
	a.slots = []resourceSlot{
		&databaseSlot{sealedReadiness: sealedReadiness{kind: componentDatabase}, app: a, plan: plan.database},
		&messagingSlot{sealedReadiness: sealedReadiness{kind: componentMessaging}, app: a, plan: plan.messaging},
		&cacheSlot{sealedReadiness: sealedReadiness{kind: componentCache}, app: a, plan: plan.cache},
		&streamsSlot{sealedReadiness: sealedReadiness{kind: componentStreams}, app: a},
	}
}

// requireSlots is the precondition every slot walk shares: CreateApp installed the slot
// list. An empty walk would silently register no probe, no closer, pre-initialize nothing,
// and start no kind at all — Builder.requireSlots and prepareRuntime both call this rather
// than each carrying their own copy of the check.
func (a *App) requireSlots(step string) error {
	if len(a.slots) == 0 {
		return fmt.Errorf("slots not installed before %s — CreateApp must run first", step)
	}
	return nil
}

// requireJudge is prepareRuntime's second precondition, beside requireSlots: the readiness
// judge was installed over the slot list. startSlots ends by marking the judge started, so
// a chain that ran CreateApp but skipped CreateHealthProbes would leave an EMPTY judge
// marked started — /ready answering 200 with nothing gated on at all, and the service
// booting green with no database and no consumers behind it. Called after requireSlots, so
// an empty judge beside a non-empty slot list can only mean the step never ran.
func (a *App) requireJudge() error {
	if len(a.judge.slots) == 0 {
		return errors.New("readiness judge not installed before prepareRuntime — Builder.CreateHealthProbes must run first")
	}
	return nil
}

// registerSlotCloser appends one slot's closer to the FIFO close list, if it has one.
func (a *App) registerSlotCloser(s resourceSlot) {
	if c, ok := s.closer(); ok {
		a.registerCloser(c.name, c.closer)
	}
}

// registerSlotClosers is the close walk: every slot's closer, in registration order.
func (a *App) registerSlotClosers() {
	for _, s := range a.slots {
		a.registerSlotCloser(s)
	}
}

// sealedReadiness is the readiness storage every production slot embeds: the kind's fixed
// name and the description it sealed once, after its start phase (ADR-066 as amended).
type sealedReadiness struct {
	description *probeDescription
	kind        string
}

// name is the kind's fixed component identifier, shared by the startup log lines and every
// readiness view but the /ready body, which names no kind — never a tenant, host or database
// name.
func (s *sealedReadiness) name() string { return s.kind }

// seal stores what the slot's describe built; a kind that renders nothing seals nil. The
// rendered name is stamped here from the slot's own kind, so each kind is spelled once —
// at installSlots — instead of again in every describe literal.
func (s *sealedReadiness) seal(description probeDescription, shown bool) {
	if !shown {
		s.description = nil
		return
	}
	description.name = s.kind
	s.description = &description
}

// readiness hands the judge the sealed description, or nil when the kind renders nothing.
func (s *sealedReadiness) readiness() *probeDescription { return s.description }

// databaseSlot owns the database kind.
type databaseSlot struct {
	sealedReadiness
	app  *App
	plan kindPlan
}

// describe builds the database kind's description: critical, leased through the fixed ""
// key, live when the leased connection's Health passes. The plan's probe always leases and
// only relabels a not-configured verdict (probeDescription.perTenant).
func (s *databaseSlot) describe() (probeDescription, bool) {
	m := s.app.dbManager
	if m == nil {
		return disabledProbe(s.kind), true
	}
	return s.plan.probe(probeDescription{
		critical: true,
		acquire: func(ctx context.Context) (func(context.Context) error, func(), error) {
			conn, release, err := m.Get(ctx, "")
			if err != nil {
				return nil, nil, err
			}
			return conn.Health, release, nil
		},
		stats: m.Stats,
	}), true
}

func (s *databaseSlot) preInitFatal() bool { return true }

// preInit leases the fixed "" key under app.startup.database to verify connectivity, then
// releases it. A failure is startup-fatal: a misconfigured backing store must not boot green.
func (s *databaseSlot) preInit(ctx context.Context) error {
	if s.app.dbManager == nil {
		return nil
	}
	return s.app.preInitLease(ctx, s.plan, s.name(), s.app.cfg.App.Startup.Database,
		func(ctx context.Context) (func(), error) {
			_, release, err := s.app.dbManager.Get(ctx, "")
			return release, err
		})
}

// start pre-warms the single-tenant connection so the first request does not pay the dial.
// Advisory only: a cold database is a runtime condition, and pre-init has already made a
// *misconfigured* one fatal.
func (s *databaseSlot) start(ctx context.Context) (advisory, fatal error) {
	return s.app.preWarmKind(ctx, s.plan, s.name(), "database connection",
		kindPresent(s.app.dbManager != nil), s.app.preWarmDatabase), nil
}

func (s *databaseSlot) stop(context.Context) {
	// no runtime teardown: the pool is released by the FIFO close list, via closer()
}

func (s *databaseSlot) closer() (namedCloser, bool) {
	return slotCloser("database manager", s.app.dbManager)
}

// messagingSlot owns the AMQP kind.
type messagingSlot struct {
	sealedReadiness
	app  *App
	plan kindPlan
}

// describe builds the messaging kind's description: leased through the fixed "" key, live when
// the leased client reports ready and, under messaging.consumers.critical, when no declared
// consumer has given up re-subscribing. The knob decides criticality once, here (ADR-066); see
// ADR-114 for what it covers. The consumer arm is a lease-independent live check, so it applies
// in every tenancy mode.
func (s *messagingSlot) describe() (probeDescription, bool) {
	m := s.app.messagingManager
	if m == nil {
		return disabledProbe(s.kind), true
	}
	description := probeDescription{
		critical:  s.app.cfg.IsMessagingConsumersCritical(),
		perTenant: s.app.multiTenant(),
		acquire: func(ctx context.Context) (func(context.Context) error, func(), error) {
			client, release, err := m.Publisher(ctx, "")
			if err != nil {
				return nil, nil, err
			}
			return func(context.Context) error {
				if !client.IsReady() {
					return errPublisherNotReady
				}
				return nil
			}, release, nil
		},
		stats: m.Stats,
	}
	if description.critical {
		// Installed only when the knob is on. An absent arm is how the judge is told there is
		// nothing lease-independent to check, so the closure never has to re-test the knob and
		// never runs on the poll path of a deployment that did not opt in.
		description.live = func(context.Context) error {
			if m.AnyConsumerGivenUp() {
				return errConsumerResubscribeExhausted
			}
			return nil
		}
	}
	return description, true
}

func (s *messagingSlot) preInitFatal() bool { return true }

// preInit leases the fixed "" key's publisher under app.startup.messaging to verify
// connectivity, then releases it. Startup-fatal, for the same reason as the database.
func (s *messagingSlot) preInit(ctx context.Context) error {
	if s.app.messagingManager == nil {
		return nil
	}
	return s.app.preInitLease(ctx, s.plan, s.name(), s.app.cfg.App.Startup.Messaging,
		func(ctx context.Context) (func(), error) {
			_, release, err := s.app.messagingManager.Publisher(ctx, "")
			return release, err
		})
}

// start runs the kind's two runtime steps in the order prepareRuntime always ran them: the
// consumer bootstrap, whose failure is fatal once consumers were declared (#907), then the
// single-tenant pre-warm, which is advisory.
func (s *messagingSlot) start(ctx context.Context) (advisory, fatal error) {
	// Values only, no cancellation: consumers outlive prepareRuntime and are stopped by
	// the messaging slot's stop phase (shutdownConsumers, ADR-029), never by the startup
	// context.
	if err := s.app.prepareRuntimeConsumers(context.WithoutCancel(ctx), s.app.messagingDeclarations); err != nil {
		return nil, err
	}

	return s.app.preWarmKind(ctx, s.plan, s.name(), componentMessaging,
		kindPresent(s.app.messagingManager != nil), s.app.preWarmMessaging), nil
}

func (s *messagingSlot) stop(context.Context) { s.app.shutdownConsumers() }

func (s *messagingSlot) closer() (namedCloser, bool) {
	return slotCloser("messaging manager", s.app.messagingManager)
}

// cacheSlot owns the cache kind.
type cacheSlot struct {
	sealedReadiness
	app  *App
	plan kindPlan
}

// describe builds the cache kind's description: critical per config (ADR-094), absent when
// the plan knows the fixed "" key holds no cache, live when a bounded PING of the leased
// instance passes — a pooled instance is returned without a round trip, so it is pinged
// explicitly.
func (s *cacheSlot) describe() (probeDescription, bool) {
	m := s.app.cacheManager
	if m == nil {
		return disabledProbe(s.kind), true
	}
	return s.plan.probe(probeDescription{
		critical: s.app.cfg.IsCacheCritical(),
		acquire: func(ctx context.Context) (func(context.Context) error, func(), error) {
			instance, release, err := m.Get(ctx, "")
			if err != nil {
				return nil, nil, err
			}
			return func(ctx context.Context) error {
				pingCtx, cancel := context.WithTimeout(ctx, cacheProbePingTimeout)
				defer cancel()
				return instance.Health(pingCtx)
			}, release, nil
		},
		stats: func() map[string]any { return convertCacheStatsToMap(m.Stats()) },
	}), true
}

func (s *cacheSlot) preInitFatal() bool { return false }

// preInit leases the fixed "" key under app.startup.cache when the plan says so.
// Best-effort: reaching the cache is a runtime concern, distinct from the manager-creation
// contract, which already failed closed at CreateCacheManager. A lease that reports
// not-configured is a silent skip, not a failure.
func (s *cacheSlot) preInit(ctx context.Context) error {
	if s.app.cacheManager == nil {
		return nil
	}
	err := s.app.preInitLease(ctx, s.plan, s.name(), s.app.cfg.App.Startup.Cache,
		func(ctx context.Context) (func(), error) {
			_, release, err := s.app.cacheManager.Get(ctx, "")
			return release, err
		})
	if config.IsNotConfigured(err) {
		s.app.logger.Debug().Msgf("Skipping %s pre-initialization: not configured", s.name())
		return nil
	}
	return err
}

// start pre-warms the fixed "" key when the plan says so. The cache has no runtime
// bootstrap.
func (s *cacheSlot) start(ctx context.Context) (advisory, fatal error) {
	return s.app.preWarmKind(ctx, s.plan, s.name(), "cache connection",
		kindPresent(s.app.cacheManager != nil), s.app.preWarmCache), nil
}

func (s *cacheSlot) stop(context.Context) {
	// no runtime teardown: the manager is released by the FIFO close list, via closer()
}

func (s *cacheSlot) closer() (namedCloser, bool) {
	return slotCloser("cache manager", s.app.cacheManager)
}

// streamsSlot owns the native stream-protocol kind. Its manager does not exist until
// prepareStreamConsumers builds it, at runtime.
type streamsSlot struct {
	sealedReadiness
	app *App
}

// describe withholds a description until the manager exists. Sealing a disabled one would
// add a "streams" entry to the debug health view and the readiness report of every service
// in the fleet, the overwhelming majority of which never declared a stream (ADR-066 rule 5
// judges every kind that describes itself at all). The seal runs after this slot's start,
// so a service that did declare streams has its manager by then.
//
// Otherwise: NON-critical (the reliable consumers reconnect on their own, so a broker flap
// must not take the service out of the load balancer), lease-less, live when every consumer
// and publisher is open.
func (s *streamsSlot) describe() (probeDescription, bool) {
	m := s.app.streamsManager
	if m == nil {
		return probeDescription{}, false
	}
	return probeDescription{
		live: func(context.Context) error {
			if !m.Ready() {
				return errStreamsNotOpen
			}
			return nil
		},
		stats: m.Stats,
	}, true
}

func (s *streamsSlot) preInit(context.Context) error { return nil }

func (s *streamsSlot) preInitFatal() bool { return false }

// start builds the stream environment and starts the declared consumers and publishers,
// then puts the manager on the FIFO close list — see prepareStreamConsumers for why a
// failure here is fatal. PR5 folds prepareStreamConsumers' body in here.
func (s *streamsSlot) start(ctx context.Context) (advisory, fatal error) {
	if err := s.app.prepareStreamConsumers(ctx); err != nil {
		return nil, err
	}
	s.app.registerSlotCloser(s)
	return nil, nil
}

func (s *streamsSlot) stop(context.Context) { s.app.shutdownStreamConsumers() }

func (s *streamsSlot) closer() (namedCloser, bool) {
	if s.app.streamsManager == nil {
		return namedCloser{}, false
	}
	return namedCloser{name: "streams manager", closer: s.app.streamsManager}, true
}

// slotCloser hands a built manager to the FIFO close list. The nil test runs on the concrete
// pointer, never on a boxed interface, so a nil manager contributes nothing instead of a
// non-nil interface holding nil.
func slotCloser[T any, P interface {
	*T
	Close() error
}](name string, mgr P) (namedCloser, bool) {
	if mgr == nil {
		return namedCloser{}, false
	}
	return namedCloser{name: name, closer: mgr}, true
}

// preWarmSubject is the operator-facing name of the thing warmed, distinct from kind (a
// plain string) so a slot's own name() cannot be passed into the subject parameter by
// mistake — the two would otherwise be interchangeable positional strings.
type preWarmSubject string

// kindPresent reports whether the kind's manager was built at all, which only the slot can
// read.
type kindPresent bool

// preWarmKind opens the kind's fixed-key resource once at startup so the first request does
// not pay the dial, when the kind's row says it pre-warms. subject names the thing warmed in
// the two operator-facing lines, which is not the kind's own name for the database
// ("database connection" vs "messaging"). A not-configured kind is a silent skip; anything
// else is advisory, never fatal.
func (a *App) preWarmKind(ctx context.Context, plan kindPlan, kind string, subject preWarmSubject,
	present kindPresent, warm func(context.Context) error,
) error {
	if !plan.preWarms() {
		return nil
	}
	if !present {
		a.logger.Debug().Msgf("Skipping control-plane %s pre-warming: manager unavailable", kind)
		return nil
	}

	if err := warm(ctx); err != nil {
		if config.IsNotConfigured(err) {
			a.logger.Debug().Msgf("Skipping control-plane %s pre-warming: not configured", kind)
			return nil
		}
		a.logger.Warn().Err(err).Msgf("Failed to pre-warm control-plane %s", subject)
		return fmt.Errorf("%s pre-warming failed: %w", kind, err)
	}

	a.logger.Info().Msgf("Pre-warmed control-plane %s", subject)
	return nil
}

// preInitLease is the arm the leasing kinds share: a kind whose row does not pre-init is
// skipped without leasing, and one that does leases the fixed "" key under its own budget and
// releases it at once. It returns the raw lease failure; preInitFatal grades it.
func (a *App) preInitLease(ctx context.Context, plan kindPlan, kind string, timeout time.Duration,
	lease func(context.Context) (func(), error),
) error {
	if !plan.preInits() {
		a.logger.Debug().Msgf("Skipping %s pre-initialization: %s, control-plane key %s", kind, plan.tenancy, plan.presence)
		return nil
	}

	ctx, cancel := startupContext(ctx, timeout)
	defer cancel()

	release, err := lease(ctx)
	if err != nil {
		return err
	}
	release() // startup probe only verifies connectivity; release the lease immediately
	a.logger.Debug().Msgf("Pre-initialized %s connection", kind)
	return nil
}

// startupContext derives one kind's pre-init context from parent. A non-positive budget means
// "no explicit budget", NOT "already expired": WithConfig's config.Validate call resolves the
// three-level fallback (config.applyStartupDefaults) for every config reaching NewWithConfig, but a
// Builder assembled without WithConfig can still carry a zero-valued Startup, and
// context.WithTimeout(parent, 0) would hand every kind a context that is dead on arrival.
func startupContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}
