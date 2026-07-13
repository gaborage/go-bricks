// Package tenantstore provides the lazy, tenant-keyed store cache shared by
// the inbox and outbox modules.
package tenantstore

import (
	"context"
	"fmt"
	"sync"

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
}

// Cache lazily builds and caches one store per tenant ("" = single-tenant).
// Failed inits cache nothing, so they can retry. Both internal maps grow with
// the tenant set — an accepted tradeoff, the values are small and stateless.
type Cache[S TableCreator] struct {
	mu       sync.RWMutex
	stores   map[string]S
	tenantMu sync.Map // map[string]*sync.Mutex — per-tenant init locks
}

// Get returns the store for the tenant in ctx, creating it (and, if
// configured, its table) on first use for that tenant. Table auto-creation is
// warn-only, one attempt per tenant — map presence short-circuits later calls.
func (c *Cache[S]) Get(ctx context.Context, d *Deps[S]) (S, error) {
	var zero S
	tenantID, _ := multitenant.GetTenant(ctx)

	if store, ok := c.Cached(tenantID); ok {
		return store, nil
	}

	// Serialize initialization per tenant only — unrelated tenants must not
	// block on each other's (possibly slow) DB connection or table creation.
	lockAny, _ := c.tenantMu.LoadOrStore(tenantID, &sync.Mutex{})
	tenantLock := lockAny.(*sync.Mutex)
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

// Cached reports the store already initialized for tenantID, if any.
func (c *Cache[S]) Cached(tenantID string) (S, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	store, ok := c.stores[tenantID]
	return store, ok
}
