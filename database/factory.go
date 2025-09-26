package database

import (
	"fmt"
	"slices"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/internal/tracking"
	"github.com/gaborage/go-bricks/database/oracle"
	"github.com/gaborage/go-bricks/database/postgresql"
	"github.com/gaborage/go-bricks/logger"
)

// NewConnection creates a new database connection according to cfg and returns it wrapped
// with performance tracking. The concrete driver is selected by cfg.Type (supported: "postgresql",
// "oracle"). If cfg.Type is unsupported an error is returned; if the chosen driver fails to
// NewConnection creates a database connection for the provided configuration and returns it wrapped with performance tracking.
// If cfg is nil, it returns an error. Supported cfg.Type values are "postgresql" and "oracle"; unsupported types return an error listing supported types. If driver initialization fails, the underlying error is returned.
func NewConnection(cfg *config.DatabaseConfig, log logger.Logger) (Interface, error) {
	if cfg == nil {
		return nil, fmt.Errorf("database configuration is nil")
	}

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
	return tracking.NewConnection(conn, log, cfg), nil
}

// ValidateDatabaseType returns nil if dbType is one of the supported database types.
// ValidateDatabaseType validates that dbType is one of the supported database types.
// If dbType is not supported, it returns an error that includes the invalid value and the list of supported types.
func ValidateDatabaseType(dbType string) error {
	supportedTypes := GetSupportedDatabaseTypes()
	if !slices.Contains(supportedTypes, dbType) {
		return fmt.Errorf("unsupported database type: %s (supported: %v)", dbType, supportedTypes)
	}
	return nil
}

// GetSupportedDatabaseTypes returns a list of supported database types
func GetSupportedDatabaseTypes() []string {
	return []string{PostgreSQL, Oracle}
}
