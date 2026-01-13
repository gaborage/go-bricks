package app

import (
	"context"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
)

const (
	testMessage            = "(optional)"
	testMessageMultiTenant = "(multi-tenant mode requires per-tenant configuration)"
)

// ResourceProvider abstracts database, messaging, and cache access with support for
// both single-tenant and multi-tenant deployment modes.
type ResourceProvider interface {
	DB(ctx context.Context) (database.Interface, error)
	DBByName(ctx context.Context, name string) (database.Interface, error)
	Messaging(ctx context.Context) (messaging.AMQPClient, error)
	Cache(ctx context.Context) (cache.Cache, error)
}

// SingleTenantResourceProvider provides database, messaging, and cache resources
// for single-tenant deployments using a fixed empty key.
type SingleTenantResourceProvider struct {
	dbManager        *database.DbManager
	messagingManager *messaging.Manager
	cacheManager     *cache.CacheManager
	declarations     *messaging.Declarations
}

// NewSingleTenantResourceProvider creates a resource provider for single-tenant mode.
func NewSingleTenantResourceProvider(
	dbManager *database.DbManager,
	messagingManager *messaging.Manager,
	cacheManager *cache.CacheManager,
	declarations *messaging.Declarations,
) *SingleTenantResourceProvider {
	return &SingleTenantResourceProvider{
		dbManager:        dbManager,
		messagingManager: messagingManager,
		cacheManager:     cacheManager,
		declarations:     declarations,
	}
}

// DB returns the database interface for single-tenant mode.
func (p *SingleTenantResourceProvider) DB(ctx context.Context) (database.Interface, error) {
	if p.dbManager == nil {
		return nil, &config.ConfigError{
			Category: "not_configured",
			Field:    "database",
			Message:  testMessage,
			Action:   "to enable: set DATABASE_HOST env var or add database.host to config.yaml",
		}
	}
	return p.dbManager.Get(ctx, "")
}

// DBByName returns a named database interface for single-tenant mode.
// Use this for explicit database selection when working with multiple databases.
// The name must match a key in the 'databases:' config section.
func (p *SingleTenantResourceProvider) DBByName(ctx context.Context, name string) (database.Interface, error) {
	if p.dbManager == nil {
		return nil, &config.ConfigError{
			Category: "not_configured",
			Field:    "database",
			Message:  testMessage,
			Action:   "to enable: set DATABASE_HOST env var or add database.host to config.yaml",
		}
	}
	if name == "" {
		return nil, &config.ConfigError{
			Category: "invalid",
			Field:    "database_name",
			Message:  "database name cannot be empty",
			Action:   "provide a valid database name from 'databases:' config section",
		}
	}
	// Use "named:" prefix to distinguish from tenant keys
	return p.dbManager.Get(ctx, config.NamedDatabasePrefix+name)
}

// Messaging returns the messaging client for single-tenant mode.
// It ensures consumers are initialized before returning the publisher.
func (p *SingleTenantResourceProvider) Messaging(ctx context.Context) (messaging.AMQPClient, error) {
	if p.messagingManager == nil {
		return nil, &config.ConfigError{
			Category: "not_configured",
			Field:    "messaging",
			Message:  testMessage,
			Action:   "to enable: set MESSAGING_BROKER_URL env var or add messaging.broker.url to config.yaml",
		}
	}

	if p.declarations != nil {
		if err := p.messagingManager.EnsureConsumers(ctx, "", p.declarations); err != nil {
			return nil, err
		}
	}

	return p.messagingManager.Publisher(ctx, "")
}

// Cache returns the cache instance for single-tenant mode.
func (p *SingleTenantResourceProvider) Cache(ctx context.Context) (cache.Cache, error) {
	if p.cacheManager == nil {
		return nil, &config.ConfigError{
			Category: "not_configured",
			Field:    "cache",
			Message:  testMessage,
			Action:   "to enable: set CACHE_REDIS_HOST env var or add cache.redis.host to config.yaml",
		}
	}
	return p.cacheManager.Get(ctx, "")
}

// SetDeclarations updates the declaration store used for ensuring consumers.
func (p *SingleTenantResourceProvider) SetDeclarations(declarations *messaging.Declarations) {
	p.declarations = declarations
}

// MultiTenantResourceProvider provides database, messaging, and cache resources
// for multi-tenant deployments using tenant ID from context.
type MultiTenantResourceProvider struct {
	dbManager        *database.DbManager
	messagingManager *messaging.Manager
	cacheManager     *cache.CacheManager
	declarations     *messaging.Declarations
}

// NewMultiTenantResourceProvider creates a resource provider for multi-tenant mode.
func NewMultiTenantResourceProvider(
	dbManager *database.DbManager,
	messagingManager *messaging.Manager,
	cacheManager *cache.CacheManager,
	declarations *messaging.Declarations,
) *MultiTenantResourceProvider {
	return &MultiTenantResourceProvider{
		dbManager:        dbManager,
		messagingManager: messagingManager,
		cacheManager:     cacheManager,
		declarations:     declarations,
	}
}

// DB returns the database interface for the tenant specified in context.
func (p *MultiTenantResourceProvider) DB(ctx context.Context) (database.Interface, error) {
	if p.dbManager == nil {
		return nil, &config.ConfigError{
			Category: "not_configured",
			Field:    "database",
			Message:  testMessageMultiTenant,
			Action:   "configure multitenant.tenants.<tenant_id>.database sections",
		}
	}

	tenantID, ok := multitenant.GetTenant(ctx)
	if !ok {
		return nil, ErrNoTenantInContext
	}

	return p.dbManager.Get(ctx, tenantID)
}

// DBByName returns a named database interface for multi-tenant mode.
// Named databases are shared across all tenants (tenant-agnostic configuration).
// Use this for explicit database selection when working with multiple databases.
// The name must match a key in the 'databases:' config section.
func (p *MultiTenantResourceProvider) DBByName(ctx context.Context, name string) (database.Interface, error) {
	if p.dbManager == nil {
		return nil, &config.ConfigError{
			Category: "not_configured",
			Field:    "databases",
			Message:  "(named databases are shared across tenants)",
			Action:   "add databases.<name> sections to config.yaml for named database access",
		}
	}
	if name == "" {
		return nil, &config.ConfigError{
			Category: "invalid",
			Field:    "database_name",
			Message:  "database name cannot be empty",
			Action:   "provide a valid database name from 'databases:' config section",
		}
	}
	// Named databases are tenant-agnostic - shared configuration across all tenants
	return p.dbManager.Get(ctx, config.NamedDatabasePrefix+name)
}

// Messaging returns the messaging client for the tenant specified in context.
// It ensures tenant-specific consumers are initialized before returning the publisher.
func (p *MultiTenantResourceProvider) Messaging(ctx context.Context) (messaging.AMQPClient, error) {
	if p.messagingManager == nil {
		return nil, &config.ConfigError{
			Category: "not_configured",
			Field:    "messaging",
			Message:  testMessageMultiTenant,
			Action:   "configure multitenant.tenants.<tenant_id>.messaging sections",
		}
	}

	tenantID, ok := multitenant.GetTenant(ctx)
	if !ok {
		return nil, ErrNoTenantInContext
	}

	// Ensure consumers are set up for this tenant
	if p.declarations != nil {
		if err := p.messagingManager.EnsureConsumers(ctx, tenantID, p.declarations); err != nil {
			return nil, err // Pass through the error from manager (already well-formatted)
		}
	}

	return p.messagingManager.Publisher(ctx, tenantID)
}

// Cache returns the cache instance for the tenant specified in context.
func (p *MultiTenantResourceProvider) Cache(ctx context.Context) (cache.Cache, error) {
	if p.cacheManager == nil {
		return nil, &config.ConfigError{
			Category: "not_configured",
			Field:    "cache",
			Message:  testMessageMultiTenant,
			Action:   "configure multitenant.tenants.<tenant_id>.cache sections",
		}
	}

	tenantID, ok := multitenant.GetTenant(ctx)
	if !ok {
		return nil, ErrNoTenantInContext
	}

	return p.cacheManager.Get(ctx, tenantID)
}

// SetDeclarations updates the declaration store used for ensuring consumers.
func (p *MultiTenantResourceProvider) SetDeclarations(declarations *messaging.Declarations) {
	p.declarations = declarations
}
