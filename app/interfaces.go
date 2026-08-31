package app

import (
	"context"
	"os"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// SignalHandler interface allows for injectable signal handling for testing
type SignalHandler interface {
	Notify(c chan<- os.Signal, sig ...os.Signal)
	WaitForSignal(c <-chan os.Signal)
}

// TimeoutProvider interface allows for injectable timeout creation for testing
type TimeoutProvider interface {
	WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc)
}

// ServerRunner abstracts the HTTP server to allow injecting test-friendly implementations
type ServerRunner interface {
	Start() error
	Shutdown(ctx context.Context) error
	RootGroup() server.RouteRegistrar
	ModuleGroup() server.RouteRegistrar
	RegisterReadyHandler(handler server.Handler)
}

// TenantStore combines the interfaces required by the database, messaging, and cache managers.
type TenantStore interface {
	database.DBConfigProvider
	messaging.BrokerURLProvider
	cache.ConfigProvider

	// IsDynamic returns true if this store loads tenant configurations dynamically
	// from external sources (e.g., AWS Secrets Manager, Vault). Returns false for
	// stores that use static YAML configuration. This controls pre-initialization behavior.
	IsDynamic() bool
}

// declarationSetter is an internal interface for setting messaging declarations
type declarationSetter interface {
	SetDeclarations(*messaging.Declarations)
}

// holdLedgerProvider is implemented by a module that offers a hold ledger — the
// inbox, when its hold is enabled. The streams lane parks through it.
type holdLedgerProvider interface {
	HoldLedger() HoldLedger
}

// holdReplayerSetter is implemented by the module that drains the hold. The
// replayer is the streams manager, which does not exist at registration time, so
// the module receives a source rather than a value.
type holdReplayerSetter interface {
	SetHoldReplayer(func() HoldReplayer)
}

// sharedResolverSetter is an internal interface implemented by ledger modules
// (outbox, inbox) that can run against the shared ("" key) control-plane
// resources when configured with tenancy=shared. The resolvers are injected at
// registration so they are available regardless of when Init runs; modules use
// them only when their tenancy config says so.
type sharedResolverSetter interface {
	SetSharedResolvers(
		db func(context.Context) (database.Interface, error),
		msg func(context.Context) (messaging.AMQPClient, error),
	)
}
