package app

import (
	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/observability"
)

// namedCloser holds a resource with its name for cleanup tracking
type namedCloser struct {
	name   string
	closer interface{ Close() error }
}

// dependencyBundle holds the created resource managers and dependencies
type dependencyBundle struct {
	deps             *ModuleDeps
	dbManager        *database.DbManager
	messagingManager *messaging.Manager
	cacheManager     *cache.CacheManager
	provider         ResourceProvider
	observability    observability.Provider
}
