// Package app provides the core application structure and lifecycle management.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/messaging/streams"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/observability"
	"github.com/gaborage/go-bricks/server"
)

const (
	serverErrorMsg = "server error: %w"
	// observabilityShutdownWarnMsg reports a failed teardown of the telemetry sink; see
	// shutdownObservability for why it is best-effort.
	observabilityShutdownWarnMsg = "Observability shutdown failed; telemetry may have been lost"
	disabledStatus               = "disabled"
	healthyStatus                = "healthy"
	unhealthyStatus              = "unhealthy"
	notConfiguredStatus          = "not_configured"
	readyStatus                  = "ready"
	// perTenantStatus marks a component whose configuration is resolved per tenant and
	// is therefore not probed by the fixed-key readiness check. Distinct from
	// not_configured, which would claim the service has no database at all — false on a
	// multi-tenant deployment with N tenant databases.
	perTenantStatus    = "per_tenant"
	degradedStatus     = "degraded"
	statusKey          = "status"
	componentDatabase  = "database"
	componentMessaging = "messaging"
	componentCache     = "cache"
	componentStreams   = "streams"
	errorKey           = "error"
)

// ErrNoTenantInContext is multitenant.ErrNoTenant under the name app's accessors
// have always returned. One value, so errors.Is matches through either name
// whichever layer produced the error.
var ErrNoTenantInContext = multitenant.ErrNoTenant

// OSSignalHandler implements SignalHandler using the real OS signal package
type OSSignalHandler struct{}

func (osh *OSSignalHandler) Notify(c chan<- os.Signal, sig ...os.Signal) {
	signal.Notify(c, sig...)
}

func (osh *OSSignalHandler) WaitForSignal(c <-chan os.Signal) {
	<-c
}

// StandardTimeoutProvider implements TimeoutProvider using context.WithTimeout
type StandardTimeoutProvider struct{}

func (stp *StandardTimeoutProvider) WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
}

// App represents the main application instance.
// It manages the lifecycle and coordination of all application components.
type App struct {
	cfg             *config.Config
	server          ServerRunner
	logger          logger.Logger
	registry        *ModuleRegistry
	signalHandler   SignalHandler
	timeoutProvider TimeoutProvider

	// Observability
	observability observability.Provider

	// Unified managers
	dbManager        *database.DbManager
	messagingManager *messaging.Manager
	cacheManager     *cache.CacheManager
	resourceProvider ResourceProvider

	// Messaging declarations, collected once at startup and replayed per tenant
	messagingDeclarations *messaging.Declarations

	// streamsManager exists only from prepareRuntime onward; see streamsSlot in slot.go
	// for why its probe and closer are registered separately from the build-time walks.
	streamsManager *streams.Manager
	// holdLedgers are the modules offering a hold ledger, captured at registration.
	// More than one is a configuration error the streams setup reports.
	holdLedgers []holdLedgerProvider

	closers      []namedCloser
	healthProbes []Prober

	// slots is the one per-kind lifecycle list every phase walks, in the fixed order
	// database → messaging → cache → streams (installSlots, ADR-067).
	slots []resourceSlot
}

// multiTenant reports whether this deployment resolves its resources per tenant. Nil-guarded
// because a directly-constructed App may carry no config.
func (a *App) multiTenant() bool {
	return a.cfg != nil && a.cfg.Multitenant.Enabled
}

// sharedMessaging reports whether the messaging kind resolves and replays on the
// control-plane key rather than per tenant. It is deliberately independent of
// multiTenant: under multitenant.enabled: false the two branches are the same one
// (ADR-041 env-parity), so shared is a no-op there rather than an error.
func (a *App) sharedMessaging() bool {
	return a.cfg != nil && a.cfg.Messaging.Tenancy == config.TenancyShared
}

// perTenantMessaging reports whether messaging is resolved per tenant — the only
// case in which startup defers consumer replay to the first request for a tenant.
func (a *App) perTenantMessaging() bool {
	return a.multiTenant() && !a.sharedMessaging()
}

func (a *App) buildMessagingDeclarations() error {
	if a.messagingDeclarations != nil {
		return nil
	}

	if a.registry == nil {
		return errors.New("module registry not initialized")
	}

	decls := messaging.NewDeclarations()
	if err := a.registry.DeclareMessaging(decls); err != nil {
		return err
	}

	a.messagingDeclarations = decls

	if setter, ok := a.resourceProvider.(declarationSetter); ok && a.resourceProvider != nil {
		setter.SetDeclarations(decls)
	}

	return nil
}

func resolveSignalAndTimeout(opts *Options) (SignalHandler, TimeoutProvider) {
	signalHandler := SignalHandler(&OSSignalHandler{})
	timeoutProvider := TimeoutProvider(&StandardTimeoutProvider{})

	if opts != nil {
		if opts.SignalHandler != nil {
			signalHandler = opts.SignalHandler
		}
		if opts.TimeoutProvider != nil {
			timeoutProvider = opts.TimeoutProvider
		}
	}

	return signalHandler, timeoutProvider
}

func resolveServer(cfg *config.Config, log logger.Logger, opts *Options) ServerRunner {
	if opts != nil && opts.Server != nil {
		log.Debug().Msg("Using provided server instance")
		return opts.Server
	}

	return server.New(cfg, log)
}

// createBootstrapLogger creates a logger with smart defaults for bootstrap/initialization logging.
// This logger is available even when configuration loading fails.
func createBootstrapLogger() logger.Logger {
	level := logger.LevelInfo
	env := strings.TrimSpace(os.Getenv("APP_ENV"))
	if env == "" || config.IsDevelopment(env) {
		level = logger.LevelDebug
	}
	if envLevel := os.Getenv("LOG_LEVEL"); envLevel != "" {
		level = envLevel
	}

	// Bootstrap runs before observability config is unmarshaled, so we
	// optimistically assume OTLP logs are off. If they turn out to be on,
	// WithOTelProvider's fail-fast will surface the conflict.
	pretty := logger.ResolvePretty(
		os.Getenv("LOG_OUTPUT_FORMAT"),
		false,
		false,
		logger.StdoutIsTerminal(),
	)
	return logger.New(level, pretty)
}

// New creates a new application instance with dependencies determined by configuration.
// It initializes only the services that are configured, failing fast if configured services cannot connect.
// Returns the app instance, a logger (always available even on failure), and any error.
func New() (*App, logger.Logger, error) {
	return NewWithOptions(nil)
}

// NewWithOptions creates a new application instance allowing overrides for config loading and dependencies.
// Returns the app instance, a logger (always available even on failure), and any error.
func NewWithOptions(opts *Options) (*App, logger.Logger, error) {
	// Create bootstrap logger first - always available
	bootstrapLog := createBootstrapLogger()

	loader := config.Load
	if opts != nil && opts.ConfigLoader != nil {
		loader = opts.ConfigLoader
	}

	cfg, err := loader()
	if err != nil {
		return nil, bootstrapLog, fmt.Errorf("failed to load config: %w", err)
	}

	return NewWithConfig(cfg, opts)
}

// NewWithConfig creates a new application instance with the provided config and optional overrides.
// This factory method allows for dependency injection while maintaining fail-fast behavior.
// Returns the app instance, a logger (always available even on failure), and any error.
func NewWithConfig(cfg *config.Config, opts *Options) (*App, logger.Logger, error) {
	builder := NewAppBuilder()

	app, log, err := builder.
		WithConfig(cfg, opts).
		CreateLogger().
		CreateBootstrap().
		ResolveDependencies().
		CreateApp().
		InitializeRegistry().
		ConfigureRuntimeHelpers().
		CreateHealthProbes().
		RegisterClosers().
		RegisterReadyHandler().
		Build()
	if err != nil {
		// No nil check on log: Build substitutes a bootstrap logger when the
		// builder never made one, and logger.NewWithFilter cannot return nil, so
		// the guarantee in this function's doc comment holds by construction
		// rather than by a fallback here.
		return nil, log, fmt.Errorf("failed to create app: %w", err)
	}

	return app, log, nil
}

// RegisterModule registers a new module with the application.
// It adds the module to the registry for initialization and route registration.
func (a *App) RegisterModule(module Module) error {
	if setter, ok := module.(sharedResolverSetter); ok {
		setter.SetSharedResolvers(a.sharedDBResolver(), a.sharedMessagingResolver())
	}
	if provider, ok := module.(holdLedgerProvider); ok {
		a.holdLedgers = append(a.holdLedgers, provider)
	}
	if setter, ok := module.(holdReplayerSetter); ok {
		// A source, not a value: the streams manager is built later, in
		// prepareStreamConsumers, so a value captured here would always be nil. The
		// manager becomes the replayer when the drain that drives it lands; until
		// then the source answers nil, which is what a module with no drain to run
		// does with it.
		setter.SetHoldReplayer(func() streams.HoldReplayer { return nil })
	}
	return a.registry.Register(module)
}

// sharedDBResolver returns a resolver for the control-plane ("" key) database,
// used by ledger modules in shared tenancy. Key "" maps to the root database:
// block for the built-in store, or whatever a custom resource source returns.
func (a *App) sharedDBResolver() func(context.Context) (database.Interface, error) {
	return func(ctx context.Context) (database.Interface, error) {
		if a.dbManager == nil {
			return nil, &config.ConfigError{
				Category: notConfiguredStatus,
				Field:    componentDatabase,
				Message:  "(shared-ledger tenancy requires a database)",
				Action:   "set the root database: block (or have the custom resource source resolve the \"\" key)",
			}
		}
		conn, release, err := a.dbManager.Get(ctx, "")
		return acquireLease(ctx, conn, release, err)
	}
}

// sharedMessagingResolver returns a resolver for the control-plane ("" key)
// messaging publisher, used by ledger modules in shared tenancy. Publisher-only:
// unlike SingleTenantResourceProvider.Messaging, it never calls EnsureConsumers —
// shared-broker consumer bootstrap is explicitly out of scope (see ADR-041).
func (a *App) sharedMessagingResolver() func(context.Context) (messaging.AMQPClient, error) {
	return func(ctx context.Context) (messaging.AMQPClient, error) {
		if a.messagingManager == nil {
			return nil, &config.ConfigError{
				Category: notConfiguredStatus,
				Field:    componentMessaging,
				Message:  "(shared-ledger tenancy requires messaging)",
				Action:   "set the root messaging.broker.url (or have the custom resource source resolve the \"\" key)",
			}
		}
		client, release, err := a.messagingManager.Publisher(ctx, "")
		return acquireLease(ctx, client, release, err)
	}
}

func (a *App) registerCloser(name string, closer interface{ Close() error }) {
	if closer == nil {
		return
	}

	a.closers = append(a.closers, namedCloser{name: name, closer: closer})
}

// MessagingDeclarations returns the captured messaging declarations.
// This is used by tenant managers to replay infrastructure for each tenant.
func (a *App) MessagingDeclarations() *messaging.Declarations {
	return a.messagingDeclarations
}
