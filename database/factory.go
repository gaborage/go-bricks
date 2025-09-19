package database

import (
	"fmt"
	"slices"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/oracle"
	"github.com/gaborage/go-bricks/database/postgresql"
	"github.com/gaborage/go-bricks/logger"
)

// NewConnection creates a new database connection according to cfg and returns it wrapped
// with performance tracking. The concrete driver is selected by cfg.Type (supported: "postgresql",
// "oracle"). If cfg.Type is unsupported an error is returned; if the chosen driver fails to
// initialize, that underlying error is returned.
func NewConnection(cfg *config.DatabaseConfig, log logger.Logger) (Interface, error) {
	var conn Interface
	var err error

	switch cfg.Type {
	case PostgreSQL:
		conn, err = postgresql.NewConnection(cfg, log)
	case Oracle:
		conn, err = oracle.NewConnection(cfg, log)
	default:
		return nil, fmt.Errorf("unsupported database type: %s (supported: postgresql, oracle)", cfg.Type)
	}

	if err != nil {
		return nil, err
	}

	// Wrap the connection with performance tracking
	return NewTrackedConnection(conn, log, cfg), nil
}

// ValidateDatabaseType returns nil if dbType is one of the supported database types.
// If dbType is not supported, it returns an error describing the invalid value and listing the supported types.
func ValidateDatabaseType(dbType string) error {
	supportedTypes := []string{PostgreSQL, Oracle}
	if !slices.Contains(supportedTypes, dbType) {
		return fmt.Errorf("unsupported database type: %s (supported: %v)", dbType, supportedTypes)
	}
	return nil
}

// GetSupportedDatabaseTypes returns a list of supported database types
func GetSupportedDatabaseTypes() []string {
	return []string{PostgreSQL, Oracle}
}
