package app

import (
	"errors"
	"fmt"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/jose"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// ModuleRegistry manages the registration and lifecycle of application modules.
// It handles module initialization, route registration, messaging setup, and shutdown.
type ModuleRegistry struct {
	modules         []Module
	deps            *ModuleDeps
	logger          logger.Logger
	registeredNames map[string]Module // Tracks registered modules to prevent duplicates
	// rootDBAbsent records the builder's rootDatabaseAbsent verdict, gating the
	// DatabaseRequirer check. Zero value (false) leaves that check inert.
	rootDBAbsent bool
	// routeSpans are the per-module registration spans of the last RegisterRoutes (no
	// framework span; a closing span ends the last module), which attribute ModuleName on
	// the route table handed to Options.PostRegisterRoutes.
	routeSpans []routeSpan
}

// NewModuleRegistry creates a new module registry with the given dependencies.
// It initializes an empty registry ready to accept module registrations.
func NewModuleRegistry(deps *ModuleDeps) *ModuleRegistry {
	return &ModuleRegistry{
		modules:         make([]Module, 0),
		deps:            deps,
		logger:          deps.Logger,
		registeredNames: make(map[string]Module),
	}
}

// Register adds a module to the registry and initializes it.
// It calls the module's Init method with the injected dependencies.
// Returns an error if a module with the same name is already registered, or if the
// module implements DatabaseRequirer on a deployment with no database — the one
// special case that rejects a module rather than wiring it.
// Special handling: modules implementing JobRegistrar, OutboxProvider, InboxProvider,
// or KeyStoreProvider are automatically wired into the corresponding ModuleDeps fields
// (Scheduler, Outbox, Inbox, KeyStore) so subsequent modules can use them.
//
// IMPORTANT: Duplicate module errors are unrecoverable and must be handled with log.Fatal().
func (r *ModuleRegistry) Register(module Module) error {
	moduleName := module.Name()

	// Deduplication: check if module already registered
	if existing, ok := r.registeredNames[moduleName]; ok {
		return fmt.Errorf(
			"module registry: duplicate module '%s' detected (already registered %T)",
			moduleName, existing,
		)
	}

	r.logger.Info().
		Str("module", moduleName).
		Msg("Registering module")

	if err := r.checkDatabaseRequirement(module); err != nil {
		return err
	}

	if err := module.Init(r.deps); err != nil {
		return err
	}

	// Special case: If this module is a JobRegistrar (scheduler module),
	// make it available to other modules via deps.Scheduler
	if jobRegistrar, ok := module.(JobRegistrar); ok {
		r.deps.Scheduler = jobRegistrar
		r.logger.Info().
			Str("module", moduleName).
			Msg("Scheduler module registered - available to other modules via deps.Scheduler")
	}

	// Special case: If this module is an OutboxProvider (outbox module),
	// make it available to other modules via deps.Outbox
	if outboxProvider, ok := module.(OutboxProvider); ok {
		r.deps.Outbox = outboxProvider.OutboxPublisher()
		r.logger.Info().
			Str("module", moduleName).
			Msg("Outbox module registered - available to other modules via deps.Outbox")
	}

	// Special case: If this module is an InboxProvider (inbox module),
	// make it available to other modules via deps.Inbox
	if inboxProvider, ok := module.(InboxProvider); ok {
		r.deps.Inbox = inboxProvider.InboxProcessor()
		r.logger.Info().
			Str("module", moduleName).
			Msg("Inbox module registered - available to other modules via deps.Inbox")
	}

	// Special case: If this module is a KeyStoreProvider (keystore module),
	// make it available to other modules via deps.KeyStore
	if ksProvider, ok := module.(KeyStoreProvider); ok {
		if r.deps.KeyStore != nil {
			return fmt.Errorf(
				"module registry: multiple KeyStore providers detected (module %q attempted to override existing provider)",
				moduleName,
			)
		}
		r.deps.KeyStore = ksProvider.KeyStore()
		r.logger.Info().
			Str("module", moduleName).
			Msg("KeyStore module registered - available to other modules via deps.KeyStore")
	}

	// Add to deduplication map BEFORE appending to modules slice
	r.registeredNames[moduleName] = module

	// Add to lifecycle registry
	r.modules = append(r.modules, module)

	// Register with metadata registry for introspection
	DefaultModuleRegistry.RegisterModule(moduleName, module, getModulePackage(module))

	r.logger.Info().
		Str("module", moduleName).
		Msg("Registered module")

	return nil
}

// checkDatabaseRequirement rejects a module that declared DatabaseRequirer on a
// deployment with no database. It runs before Init so the module never sees a
// dependency it declared as mandatory and cannot get.
//
// rootDBAbsent is supplied by the builder, which is the only place that can see both
// the config and the Options needed to evaluate rootDatabaseAbsent. Its zero value
// disables the check, so a registry built directly — outside the builder, with no
// Options to consult — stays inert rather than aborting on a verdict it cannot reach.
func (r *ModuleRegistry) checkDatabaseRequirement(module Module) error {
	requirer, ok := module.(DatabaseRequirer)
	if !ok || !requirer.RequiresDatabase() || !r.rootDBAbsent {
		return nil
	}

	// Deliberately the "missing" category, never "not_configured": the latter marks a
	// feature as intentionally absent and is used framework-wide as a skip-and-degrade
	// predicate (config.IsNotConfigured — see app/prewarm.go, app/health.go). A caller
	// mirroring that idiom would turn this fatal into a silent module skip, dropping the
	// module's routes and global middleware while the rest of the app still served.
	err := config.NewMissingFieldError(componentDatabase, "DATABASE_TYPE", "database")
	err.Message = fmt.Sprintf("required by module %q", module.Name())
	return err
}

// RegisterRoutes calls RegisterRoutes on modules that implement RouteRegisterer.
// Modules without routes are silently skipped.
//
// If a KeyStore-providing module has registered (and r.deps.KeyStore is therefore
// populated), a jose.KeyStoreResolver is wired into the handler registry so any route
// declaring jose: tags can resolve its kids at registration time. Logger, tracer, and
// meter from deps are also threaded into the registry so JOSE failures get audit-grade
// structured logs and OTEL telemetry. Routes without jose tags are unaffected.
func (r *ModuleRegistry) RegisterRoutes(registrar server.RouteRegistrar) {
	opts := []server.HandlerRegistryOption{}
	if r.deps.KeyStore != nil {
		opts = append(opts, server.WithJOSEResolver(jose.NewKeyStoreResolver(r.deps.KeyStore)))
	}
	if r.deps.Logger != nil {
		opts = append(opts, server.WithLogger(r.deps.Logger), server.WithJOSELogger(r.deps.Logger))
	}
	if r.deps.Tracer != nil {
		opts = append(opts, server.WithJOSETracer(r.deps.Tracer))
	}
	if r.deps.MeterProvider != nil {
		opts = append(opts, server.WithJOSEMeterProvider(r.deps.MeterProvider))
	}
	handlerRegistry := server.NewHandlerRegistry(r.deps.Config, opts...)

	logRoutes := r.deps.Config != nil && r.deps.Config.ShouldLogRoutes()

	// Attribution is by registration-order delta against DefaultRouteRegistry, NOT
	// RouteDescriptor.ModuleName. Startup registration is single-threaded and append-only, so
	// recorded start indices resolve consistently against one post-loop Routes() snapshot. The
	// leading framework span covers what registered before this loop — the health/ready
	// probes and debug/_sys routes (single-app-per-process assumed).
	spans := []routeSpan{{module: frameworkRouteAttribution, start: 0}}
	for _, module := range r.modules {
		rr, ok := module.(RouteRegisterer)
		if !ok {
			continue
		}
		r.logger.Info().
			Str("module", module.Name()).
			Msg("Registering module routes")

		spans = append(spans, routeSpan{module: module.Name(), start: server.DefaultRouteRegistry.Count()})
		rr.RegisterRoutes(handlerRegistry, registrar)
	}
	// A closing span with no module ends the last module's range where this loop ended.
	spans = append(spans, routeSpan{start: server.DefaultRouteRegistry.Count()})
	r.routeSpans = spans[1:]

	if logRoutes {
		for _, e := range collectRouteLogEntries(spans, server.DefaultRouteRegistry.Routes()) {
			r.logger.Info().
				Str("module", e.module).
				Str("method", e.method).
				Str("path", e.path).
				Str("listener", routeListenerLabel(e.listener)).
				Msg("Route registered")
		}
	}
}

// frameworkRouteAttribution labels routes registered before the module loop
// (health/ready probes, debug/_sys endpoints) in the route-registered log.
const frameworkRouteAttribution = "framework"

// applicationListenerLabel is the route log's listener value for a descriptor whose
// Listener is empty; every line names its listener.
const applicationListenerLabel = "application"

func routeListenerLabel(listener string) string {
	if listener == "" {
		return applicationListenerLabel
	}
	return listener
}

// routeSpan marks the half-open registry index range [start, next.start) whose
// descriptors were appended by module. Recorded during the module loop; resolved
// against one post-loop DefaultRouteRegistry.Routes() snapshot.
type routeSpan struct {
	module string
	start  int
}

// routeLogEntry is the flattened, logger-free result of resolving spans against
// a routes snapshot.
type routeLogEntry struct {
	module, method, path string
	listener             string // RouteDescriptor.Listener, unlabeled
}

// forEachSpanRoute calls fn with each span's module and every route in the span's
// [start, next.start) range. A start past the snapshot resolves to no routes, and a bogus
// successor start is clamped to the snapshot rather than corrupting this span.
func forEachSpanRoute(spans []routeSpan, routes []server.RouteDescriptor, fn func(module string, route *server.RouteDescriptor)) {
	for i, span := range spans {
		if span.start < 0 {
			continue // defensive: a negative start is impossible single-threaded
		}
		end := len(routes)
		if i+1 < len(spans) {
			end = spans[i+1].start
		}
		end = min(end, len(routes))
		for j := span.start; j < end; j++ {
			fn(span.module, &routes[j])
		}
	}
}

// collectRouteLogEntries resolves each span's [start, next.start) range against
// the routes snapshot. Pure (no logger, no globals) so attribution — raw routes,
// zero-route modules, and the pre-loop framework span — is unit-testable.
// Attribution is positional and ignores RouteDescriptor.ModuleName.
func collectRouteLogEntries(spans []routeSpan, routes []server.RouteDescriptor) []routeLogEntry {
	var out []routeLogEntry
	forEachSpanRoute(spans, routes, func(module string, route *server.RouteDescriptor) {
		out = append(out, routeLogEntry{module: module, method: route.Method, path: route.Path, listener: route.Listener})
	})
	return out
}

// attributeModuleNames sets ModuleName on every route inside a module's span that did not
// name its own module with server.WithModule. Routes outside every module span keep theirs empty.
func attributeModuleNames(spans []routeSpan, routes []server.RouteDescriptor) {
	forEachSpanRoute(spans, routes, func(module string, route *server.RouteDescriptor) {
		if route.ModuleName == "" {
			route.ModuleName = module
		}
	})
}

// routesSince returns the registry's routes from start on, with ModuleName attributed from
// the spans of the last RegisterRoutes.
func (r *ModuleRegistry) routesSince(start int) []server.RouteDescriptor {
	all := server.DefaultRouteRegistry.Routes()
	attributeModuleNames(r.routeSpans, all)
	return all[start:]
}

// CollectGlobalMiddleware gathers middleware from modules that implement
// GlobalMiddlewareRegisterer, in registration order. Modules without global middleware are
// silently skipped.
func (r *ModuleRegistry) CollectGlobalMiddleware() []server.MiddlewareFunc {
	var mws []server.MiddlewareFunc
	for _, module := range r.modules {
		if gm, ok := module.(GlobalMiddlewareRegisterer); ok {
			r.logger.Info().
				Str("module", module.Name()).
				Msg("Collecting module global middleware")
			mws = append(mws, gm.GlobalMiddleware()...)
		}
	}
	return mws
}

// DeclareMessaging calls DeclareMessaging on modules that implement MessagingDeclarer.
// Modules without messaging declarations are silently skipped.
func (r *ModuleRegistry) DeclareMessaging(decls *messaging.Declarations) error {
	if decls == nil {
		return errors.New("declarations store is nil")
	}

	for _, module := range r.modules {
		if md, ok := module.(MessagingDeclarer); ok {
			// Per-module attribution, not the running total, so a topology conflict
			// names its owner. Zeros mean the module added nothing new:
			// RegisterExchange and RegisterQueue both merge a compatible
			// re-declaration, so re-declaring a neighbor's topology contributes none.
			before := decls.Stats()
			md.DeclareMessaging(decls)
			after := decls.Stats()
			logDeclStats(r.logger.Info().Str("module", module.Name()), before, after).
				Msg("Collecting module messaging declarations")
		}
	}

	// Validate all declarations after collection
	r.logger.Info().Msg("Validating messaging declarations")
	if err := decls.Validate(); err != nil {
		r.logger.Error().Err(err).Msg("Declaration validation failed")
		return fmt.Errorf("declaration validation failed: %w", err)
	}

	stats := decls.Stats()
	logDeclStats(r.logger.Info(), messaging.DeclarationStats{}, stats).
		Msg("Messaging declarations collected and validated successfully")

	return nil
}

// logDeclStats is the single enumeration of the DeclarationStats field set for the
// declarer log lines; the field names and their order are an operator-facing contract.
func logDeclStats(e logger.LogEvent, before, after messaging.DeclarationStats) logger.LogEvent {
	return e.
		Int("exchanges", after.Exchanges-before.Exchanges).
		Int("queues", after.Queues-before.Queues).
		Int("bindings", after.Bindings-before.Bindings).
		Int("publishers", after.Publishers-before.Publishers).
		Int("consumers", after.Consumers-before.Consumers)
}

// RegisterJobs calls RegisterJobs on modules that implement JobProvider interface.
// This method is called after all modules have been initialized, making module registration
// order irrelevant for job scheduling. If no scheduler is registered, this method skips silently.
func (r *ModuleRegistry) RegisterJobs() error {
	if r.deps.Scheduler == nil {
		r.logger.Debug().Msg("No scheduler registered, skipping job registration")
		return nil
	}

	jobProviderCount := 0
	for _, module := range r.modules {
		if jobProvider, ok := module.(JobProvider); ok {
			jobProviderCount++
			r.logger.Info().
				Str("module", module.Name()).
				Msg("Registering module jobs")

			if err := jobProvider.RegisterJobs(r.deps.Scheduler); err != nil {
				return fmt.Errorf("module '%s' job registration failed: %w", module.Name(), err)
			}
		}
	}

	if jobProviderCount > 0 {
		r.logger.Info().
			Int("modules", jobProviderCount).
			Msg("Job registration completed successfully")
	}

	return nil
}

// Shutdown gracefully shuts down all registered modules.
// It calls each module's Shutdown method (continuing past failures), logs each
// error, and returns them joined via errors.Join (nil if all shut down cleanly).
// Messaging shutdown is handled by the messaging manager.
func (r *ModuleRegistry) Shutdown() error {
	var errs []error
	for _, module := range r.modules {
		r.logger.Info().
			Str("module", module.Name()).
			Msg("Shutting down module")

		if err := module.Shutdown(); err != nil {
			r.logger.Error().
				Err(err).
				Str("module", module.Name()).
				Msg("Failed to shutdown module")
			errs = append(errs, fmt.Errorf("shutdown module %s: %w", module.Name(), err))
		}
	}
	return errors.Join(errs...)
}
