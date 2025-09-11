package database

import (
	"fmt"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/oracle"
	"github.com/gaborage/go-bricks/database/postgresql"
	"github.com/gaborage/go-bricks/logger"
)

// NewConnection creates a new database connection based on the configuration
func NewConnection(cfg *config.DatabaseConfig, log logger.Logger) (Interface, error) {
	switch cfg.Type {
	case PostgreSQL:
		return postgresql.NewConnection(cfg, log)
	case Oracle:
		return oracle.NewConnection(cfg, log)
	default:
		return nil, fmt.Errorf("unsupported database type: %s (supported: postgresql, oracle)", cfg.Type)
	}
}

// ValidateDatabaseType checks if the database type is supported
func ValidateDatabaseType(dbType string) error {
	supportedTypes := []string{PostgreSQL, Oracle}
	for _, supported := range supportedTypes {
		if dbType == supported {
			return nil
		}
	}
	return fmt.Errorf("unsupported database type: %s (supported: %v)", dbType, supportedTypes)
}

// GetSupportedDatabaseTypes returns a list of supported database types
func GetSupportedDatabaseTypes() []string {
	return []string{PostgreSQL, Oracle}
}
