// Package tenantstore provides the lazy, tenant-keyed store cache shared by
// the inbox and outbox modules.
package tenantstore

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gaborage/go-bricks/config"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
)

// TableCreator is the store capability Cache needs for table auto-creation.
type TableCreator interface {
	CreateTable(ctx context.Context, db dbtypes.Interface) error
}

// Deps carries the module-specific pieces of store initialization.
type Deps[S TableCreator] struct {
	Name            string // error prefix, e.g. "inbox"
	TableName       string
	AutoCreateTable bool
	Logger          logger.Logger
	GetDB           func(context.Context) (dbtypes.Interface, error)
	NewPostgres     func(tableName string) (S, error)
	NewOracle       func(tableName string) (S, error)
	WarnMsg         string // e.g. "Inbox table creation failed (may already exist)"
	// SingleKey caches under the empty key regardless of ctx tenant — used by
	// shared-ledger tenancy so the dialect is resolved once against the shared DB.
	SingleKey bool
}

// Cache lazily builds and caches one store per tenant ("" = single-tenant).
// Failed inits cache nothing, so they can retry. Both internal maps grow with
// the tenant set — an accepted tradeoff, the values are small and stateless.
type Cache[S TableCreator] struct {
	mu       sync.RWMutex
	stores   map[string]S
	tenantMu map[string]*sync.Mutex // per-tenant init locks, guarded by mu
}

// Get returns the store for the tenant in ctx, creating it (and, if
// configured, its table) on first use for that tenant. Table auto-creation is
// warn-only, one attempt per tenant — map presence short-circuits later calls.
func (c *Cache[S]) Get(ctx context.Context, d *Deps[S]) (S, error) {
	var zero S
	tenantID, _ := multitenant.GetTenant(ctx)
	if d.SingleKey {
		tenantID = ""
	}

	if store, ok := c.Cached(tenantID); ok {
		return store, nil
	}

	// Serialize initialization per tenant only — unrelated tenants must not
	// block on each other's (possibly slow) DB connection or table creation.
	tenantLock := c.tenantLock(tenantID)
	tenantLock.Lock()
	defer tenantLock.Unlock()

	if store, ok := c.Cached(tenantID); ok {
		return store, nil
	}

	db, err := d.GetDB(ctx)
	if err != nil {
		return zero, fmt.Errorf("%s: database unavailable: %w", d.Name, err)
	}

	var store S
	switch db.DatabaseType() {
	case dbtypes.PostgreSQL:
		store, err = d.NewPostgres(d.TableName)
	case dbtypes.Oracle:
		store, err = d.NewOracle(d.TableName)
	default:
		return zero, fmt.Errorf("%s: unsupported database vendor: %s", d.Name, db.DatabaseType())
	}
	if err != nil {
		return zero, fmt.Errorf("%s: failed to create store: %w", d.Name, err)
	}

	if d.AutoCreateTable {
		if createErr := store.CreateTable(ctx, db); createErr != nil {
			d.Logger.Warn().Err(createErr).
				Str("table", d.TableName).
				Str("tenant", tenantID).
				Msg(d.WarnMsg)
		}
	}

	c.mu.Lock()
	if c.stores == nil {
		c.stores = make(map[string]S)
	}
	c.stores[tenantID] = store
	c.mu.Unlock()
	return store, nil
}

// tenantLock returns the init lock for tenantID, creating it on first use. mu
// is held only for the map lookup — never across the returned lock — so one
// tenant's slow initialization cannot block another's lookup.
func (c *Cache[S]) tenantLock(tenantID string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if lock, ok := c.tenantMu[tenantID]; ok {
		return lock
	}
	lock := &sync.Mutex{}
	if c.tenantMu == nil {
		c.tenantMu = make(map[string]*sync.Mutex)
	}
	c.tenantMu[tenantID] = lock
	return lock
}

// Cached reports the store already initialized for tenantID, if any.
func (c *Cache[S]) Cached(tenantID string) (S, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	store, ok := c.stores[tenantID]
	return store, ok
}

// SharedResolversRequired builds the module-prefixed error for tenancy=shared
// without the framework-injected shared resolvers (set by app.RegisterModule via
// its setter duck-type). Shared by the outbox and inbox modules so the operator
// guidance cannot drift between them.
func SharedResolversRequired(module string) error {
	return fmt.Errorf("%s: tenancy=shared requires framework-injected shared resolvers; "+
		"register the module via app.RegisterModule (direct construction must call SetSharedResolvers)", module)
}

// RejectSharedWithStaticTenants fails tenancy=shared combined with static
// multitenant.tenants: the shared "" key resolves the root database:/messaging:
// blocks, but static tenant mode forbids those root blocks (config/validation.go's
// validateNoSingleTenantConflict). Returns nil when there is no conflict. Shared by
// the outbox and inbox modules so the predicate and message cannot drift.
func RejectSharedWithStaticTenants(module string, cfg *config.Config) error {
	if cfg != nil && cfg.Multitenant.Enabled &&
		cfg.Source.Type != config.SourceTypeDynamic && len(cfg.Multitenant.Tenants) > 0 {
		return fmt.Errorf("%s: tenancy=shared cannot be combined with static multitenant.tenants "+
			"(static tenant mode forbids the root database/messaging blocks the shared \"\" key resolves); "+
			"use the default per-tenant tenancy, or a dynamic source", module)
	}
	return nil
}

// defaultStartupTimeout bounds the Init-time database probe when
// app.startup.database is unset. config.Validate always defaults that key to
// the same 10s, so this only matters for a hand-built *config.Config.
const defaultStartupTimeout = 10 * time.Second

// StartupCheckApplies reports whether the "" key is statically resolvable at
// Init time: single-tenant, or shared-ledger tenancy, both with a static
// source. Per-tenant fan-out resolves databases per tenant at runtime, and
// dynamic sources resolve "" at runtime. The app builder's skipPreInit and
// app.rootDatabaseAbsent exempt a third mode this predicate cannot see: a
// dynamic Options.ResourceSource behind a static source.type, which is
// invisible from config alone and is therefore probed here. Shared by the
// outbox and inbox modules' Init so the predicate cannot drift between them.
func StartupCheckApplies(cfg *config.Config, sharedLedger bool) bool {
	return cfg != nil &&
		cfg.Source.Type != config.SourceTypeDynamic &&
		(!cfg.Multitenant.Enabled || sharedLedger)
}

// StartupDatabase derives a bounded context from cfg.App.Startup.Database
// (defaulting to defaultStartupTimeout when unset), resolves the "" database
// via getDB, and classifies a resolver failure: a config.IsNotConfigured
// error becomes an actionable "requires a database" message, any other error
// becomes "database unreachable at startup", and a (nil, nil) result — a
// resolver contract violation — is rejected explicitly (without this guard,
// the caller's next call, db.DatabaseType(), would panic instead of
// erroring). The returned cancel is always non-nil; callers must defer it
// unconditionally and use the returned context for every subsequent call
// (ensureStoreInitialized, the probe) so they all share one deadline. Shared
// by the outbox and inbox modules so the error text and classification
// cannot drift between them.
func StartupDatabase(
	parent context.Context,
	cfg *config.Config,
	module string,
	getDB func(context.Context) (dbtypes.Interface, error),
) (ctx context.Context, cancel context.CancelFunc, db dbtypes.Interface, err error) {
	timeout := cfg.App.Startup.Database
	if timeout <= 0 {
		timeout = defaultStartupTimeout
	}
	ctx, cancel = context.WithTimeout(parent, timeout)

	db, err = getDB(ctx)
	if err != nil {
		if config.IsNotConfigured(err) {
			return ctx, cancel, nil, fmt.Errorf("%s: %s.enabled=true requires a database, but none is configured; "+
				"configure the database section (or the control-plane database for tenancy=shared) "+
				"or set %s.enabled=false: %w", module, module, module, err)
		}
		return ctx, cancel, nil, fmt.Errorf("%s: database unreachable at startup: %w", module, err)
	}
	if db == nil {
		// Contract violation (resolver returned nil, nil). Without this guard the
		// caller's db.DatabaseType() call panics inside Init instead of erroring.
		return ctx, cancel, nil, fmt.Errorf("%s: database resolver returned a nil database", module)
	}
	return ctx, cancel, db, nil
}

// TableUnusableError formats the startup-probe's table-unusable error: the
// database is reachable, but the module's table is missing, or the
// credentials can't use it. autocreateKey is the config key that would have
// created it, e.g. "outbox.autocreatetable". Shared so the message cannot
// drift between outbox and inbox.
func TableUnusableError(module, tableName, autocreateKey string, cause error) error {
	return fmt.Errorf("%s: table %q is not usable (missing table or insufficient privileges); "+
		"run migrations or set %s=true: %w", module, tableName, autocreateKey, cause)
}
