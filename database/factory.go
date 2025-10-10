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

// NewConnection creates a tracked database connection for the provided configuration.
// NewConnection creates a tracked database connection based on the provided configuration.
// 
// It initializes a concrete driver connection for the configured database type, wraps it with
// performance/tracing tracking, and attaches server metadata (host, port and an OTel namespace)
// to the tracking wrapper when available.
//
// Errors are returned if cfg is nil, cfg.Type is not supported (supported: "postgresql", "oracle"),
// or if the underlying driver initialization fails.
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
	trackedConn := tracking.NewConnection(conn, log, cfg)

	// Set server metadata for OTel attributes based on database type
	var namespace string
	switch cfg.Type {
	case PostgreSQL:
		namespace = tracking.BuildPostgreSQLNamespace(cfg.Database)
	case Oracle:
		namespace = tracking.BuildOracleNamespace(cfg.Oracle.Service.Name, cfg.Oracle.Service.SID, cfg.Database)
	}

	// Set server info on the tracking wrapper
	if tc, ok := trackedConn.(*tracking.Connection); ok {
		tc.SetServerInfo(cfg.Host, cfg.Port, namespace)
	}

	return trackedConn, nil
}

// ValidateDatabaseType reports an error when dbType is not among the supported database types.
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