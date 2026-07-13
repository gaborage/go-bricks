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
	NewStore        func(vendor, tableName string) (S, error)
	WarnMsg         string // e.g. "Inbox table creation failed (may already exist)"
}

// Cache lazily builds and caches one store per tenant ("" = single-tenant).
// Mutex-guarded so a failed init can retry.
type Cache[S TableCreator] struct {
	mu     sync.Mutex
	stores map[string]S
}

// Get returns the store for the tenant in ctx, creating it (and, if
// configured, its table) on first use for that tenant. Table auto-creation is
// warn-only, one attempt per tenant — map presence short-circuits later calls.
func (c *Cache[S]) Get(ctx context.Context, d *Deps[S]) (S, error) {
	var zero S
	tenantID, _ := multitenant.GetTenant(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	if store, ok := c.stores[tenantID]; ok {
		return store, nil
	}

	db, err := d.GetDB(ctx)
	if err != nil {
		return zero, fmt.Errorf("%s: database unavailable: %w", d.Name, err)
	}

	store, err := d.NewStore(db.DatabaseType(), d.TableName)
	if err != nil {
		return zero, err
	}

	if d.AutoCreateTable {
		if createErr := store.CreateTable(ctx, db); createErr != nil {
			d.Logger.Warn().Err(createErr).
				Str("table", d.TableName).
				Str("tenant", tenantID).
				Msg(d.WarnMsg)
		}
	}

	if c.stores == nil {
		c.stores = make(map[string]S)
	}
	c.stores[tenantID] = store
	return store, nil
}

// Cached reports the store already initialized for tenantID, if any.
func (c *Cache[S]) Cached(tenantID string) (S, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	store, ok := c.stores[tenantID]
	return store, ok
}
