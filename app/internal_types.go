package app

import (
	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/observability"
)

// namedCloser holds a resource with its name for cleanup tracking
type namedCloser struct {
	name   string
	closer interface{ Close() error }
}

// dependencyBundle holds the created resource managers and dependencies
type dependencyBundle struct {
	plan             resourcePlan // computed once, before any manager (ADR-126)
	deps             *ModuleDeps
	dbManager        *database.DbManager
	messagingManager *messaging.Manager
	cacheManager     *cache.CacheManager
	provider         ResourceProvider
	observability    observability.Provider
	// tenantChecker is the resource source's existence check, nil when the source has no
	// HasTenant; the server's tenant seat asks it about a path-read tenant (ADR-170).
	tenantChecker multitenant.TenantChecker
}
