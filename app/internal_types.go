package app

import (
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/messaging"
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
	provider         ResourceProvider
}
