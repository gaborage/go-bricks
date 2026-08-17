package app

import (
	"context"
	"time"

	"github.com/gaborage/go-bricks/config"
)

// A slot is the framework-side module that owns one resource kind's application lifecycle —
// probe, pre-init, close — so that adding a kind is one slot, not an edit in every place
// that enumerates kinds (CONTEXT.md, ADR-067).
//
// ADR-045: the interface lives in app/ and names only what app calls. The managers behind
// it (database.DbManager, messaging.Manager, cache.CacheManager, streams.Manager) know
// nothing about it.
type resourceSlot interface {
	// name is the kind's fixed component identifier. SECURITY: the probe description carries
	// this same string onto the unauthenticated /ready body, so it is never a tenant, host or
	// database name.
	name() string

	// probe returns the kind's readiness description and whether it is registered at all.
	// Only the streams slot withholds one — see streamsSlot.probe.
	probe() (probeDescription, bool)

	// preInit establishes the kind's fixed-"" -key connection during Builder construction.
	// It returns the raw failure; preInitFatal decides what that costs.
	preInit(ctx context.Context) error

	// preInitFatal reports whether a preInit failure aborts startup.
	preInitFatal() bool

	// closer hands over the resource the close phase must Close. ok is false when the kind
	// has nothing to close yet, which is how an unconfigured kind and a streams manager that
	// has not started stay out of the FIFO close list.
	closer() (namedCloser, bool)
}

var (
	_ resourceSlot = (*databaseSlot)(nil)
	_ resourceSlot = (*messagingSlot)(nil)
	_ resourceSlot = (*cacheSlot)(nil)
	_ resourceSlot = (*streamsSlot)(nil)
)

// slotInputs carries the verdicts a slot cannot reach from App alone. Only one qualifies:
// the cache's absence under the fixed "" key reads Options (rootCacheAbsent), which the
// Builder holds and App does not.
type slotInputs struct {
	cacheAbsent bool
}

// installSlots builds the one slot list every lifecycle phase walks, in the one
// registration order: database → messaging → cache → streams. Close stays FIFO over the
// same order. Each slot holds the App rather than a snapshot of its managers, so a manager
// swapped in later (the streams manager, which only exists after start) is seen by the next
// walk without rebuilding the list.
func (a *App) installSlots(inputs slotInputs) {
	a.slots = []resourceSlot{
		&databaseSlot{app: a},
		&messagingSlot{app: a},
		&cacheSlot{app: a, absent: inputs.cacheAbsent},
		&streamsSlot{app: a},
	}
}

// collectProbes is the readiness walk: every slot that has a description to register, in
// registration order.
func (a *App) collectProbes() []Prober {
	probes := make([]Prober, 0, len(a.slots))
	for _, s := range a.slots {
		if description, ok := s.probe(); ok {
			probes = append(probes, description)
		}
	}
	return probes
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

// databaseSlot owns the database kind.
type databaseSlot struct{ app *App }

func (s *databaseSlot) name() string { return componentDatabase }

func (s *databaseSlot) probe() (probeDescription, bool) {
	return databaseProbe(s.app.dbManager, s.app.multiTenant()), true
}

func (s *databaseSlot) preInitFatal() bool { return true }

// preInit leases the fixed "" key under app.startup.database to verify connectivity, then
// releases it. A failure is startup-fatal: a misconfigured backing store must not boot green.
func (s *databaseSlot) preInit(ctx context.Context) error {
	if s.app.dbManager == nil {
		return nil
	}
	return s.app.preInitLease(ctx, componentDatabase,
		config.IsDatabaseConfigured(&s.app.cfg.Database), s.app.cfg.App.Startup.Database,
		func(ctx context.Context) (func(), error) {
			_, release, err := s.app.dbManager.Get(ctx, "")
			return release, err
		})
}

func (s *databaseSlot) closer() (namedCloser, bool) {
	if s.app.dbManager == nil {
		return namedCloser{}, false
	}
	return namedCloser{name: "database manager", closer: s.app.dbManager}, true
}

// messagingSlot owns the AMQP kind.
type messagingSlot struct{ app *App }

func (s *messagingSlot) name() string { return componentMessaging }

func (s *messagingSlot) probe() (probeDescription, bool) {
	return messagingProbe(s.app.messagingManager, s.app.multiTenant()), true
}

func (s *messagingSlot) preInitFatal() bool { return true }

// preInit leases the fixed "" key's publisher under app.startup.messaging to verify
// connectivity, then releases it. Startup-fatal, for the same reason as the database.
func (s *messagingSlot) preInit(ctx context.Context) error {
	if s.app.messagingManager == nil {
		return nil
	}
	return s.app.preInitLease(ctx, componentMessaging,
		config.IsMessagingConfigured(&s.app.cfg.Messaging), s.app.cfg.App.Startup.Messaging,
		func(ctx context.Context) (func(), error) {
			_, release, err := s.app.messagingManager.Publisher(ctx, "")
			return release, err
		})
}

func (s *messagingSlot) closer() (namedCloser, bool) {
	if s.app.messagingManager == nil {
		return namedCloser{}, false
	}
	return namedCloser{name: "messaging manager", closer: s.app.messagingManager}, true
}

// cacheSlot owns the cache kind.
type cacheSlot struct {
	app *App
	// absent is the Builder's rootCacheAbsent verdict, captured once at installSlots because
	// it reads Options, which App does not hold.
	absent bool
}

func (s *cacheSlot) name() string { return componentCache }

func (s *cacheSlot) probe() (probeDescription, bool) {
	return cacheProbe(s.app.cacheManager, s.app.cfg.IsCacheCritical(), s.absent, s.app.multiTenant()), true
}

func (s *cacheSlot) preInitFatal() bool { return false }

// preInit leases the fixed "" key under app.startup.cache, unless that key can never
// resolve (absent). Best-effort: reaching the cache is a runtime concern, distinct from the
// manager-creation contract, which already failed closed at CreateCacheManager. A lease that
// reports not-configured is a silent skip, not a failure.
func (s *cacheSlot) preInit(ctx context.Context) error {
	if s.app.cacheManager == nil || s.absent {
		return nil
	}

	ctx, cancel := startupContext(ctx, s.app.cfg.App.Startup.Cache)
	defer cancel()

	_, release, err := s.app.cacheManager.Get(ctx, "")
	if err != nil {
		if config.IsNotConfigured(err) {
			s.app.logger.Debug().Msg("Skipping cache pre-initialization: not configured")
			return nil
		}
		return err
	}
	release() // startup probe only verifies connectivity; release the lease immediately
	s.app.logger.Debug().Msg("Pre-initialized cache connection")
	return nil
}

func (s *cacheSlot) closer() (namedCloser, bool) {
	if s.app.cacheManager == nil {
		return namedCloser{}, false
	}
	return namedCloser{name: "cache manager", closer: s.app.cacheManager}, true
}

// streamsSlot owns the native stream-protocol kind. Its manager does not exist until
// prepareStreamConsumers builds it, at runtime.
type streamsSlot struct{ app *App }

func (s *streamsSlot) name() string { return componentStreams }

// probe withholds a description until the manager exists. Registering a disabled one at
// build time would add "streams" and "streams_stats" to the /ready body of every service in
// the fleet, the overwhelming majority of which never declared a stream (ADR-066 rule 5
// renders every registered kind). prepareStreamConsumers registers the description once it
// has produced the manager, so it appears exactly where the runtime registration put it.
func (s *streamsSlot) probe() (probeDescription, bool) {
	if s.app.streamsManager == nil {
		return probeDescription{}, false
	}
	return streamsProbe(s.app.streamsManager), true
}

func (s *streamsSlot) preInit(context.Context) error { return nil }

func (s *streamsSlot) preInitFatal() bool { return false }

func (s *streamsSlot) closer() (namedCloser, bool) {
	if s.app.streamsManager == nil {
		return namedCloser{}, false
	}
	return namedCloser{name: "streams manager", closer: s.app.streamsManager}, true
}

// preInitLease is the arm the two startup-fatal kinds share: an unconfigured kind is skipped
// without leasing, and a configured one leases the fixed "" key under its own budget and
// releases it at once. It returns the raw lease failure; preInitFatal grades it.
func (a *App) preInitLease(ctx context.Context, kind string, configured bool, timeout time.Duration,
	lease func(context.Context) (func(), error),
) error {
	if !configured {
		a.logger.Debug().Msgf("Skipping %s pre-initialization: not configured", kind)
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
