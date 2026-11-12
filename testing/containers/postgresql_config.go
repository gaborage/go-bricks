package containers

import (
	"fmt"
	"time"
)

// PostgreSQLContainerConfig holds configuration for PostgreSQL test container
type PostgreSQLContainerConfig struct {
	// ImageTag specifies the PostgreSQL version (default: the pin in DefaultPostgreSQLConfig)
	ImageTag string
	// Username for PostgreSQL authentication (default: "testuser")
	Username string
	// Password for PostgreSQL authentication (default: "testpass")
	Password string
	// Database name to create (default: "testdb")
	Database string
	// StartupTimeout for container initialization (default: 60 seconds)
	StartupTimeout time.Duration
}

// DefaultPostgreSQLConfig returns a PostgreSQLContainerConfig populated with sensible defaults.
//
// The returned configuration sets Username to "testuser", Password to "testpass",
// Database to "testdb", and StartupTimeout to 60 seconds.
func DefaultPostgreSQLConfig() *PostgreSQLContainerConfig {
	return &PostgreSQLContainerConfig{
		// renovate: datasource=docker depName=postgres
		ImageTag:       "18.7-alpine",
		Username:       "testuser",
		Password:       "testpass",
		Database:       "testdb",
		StartupTimeout: 60 * time.Second,
	}
}

// postgreSQLStartLogText names where a started container listens. It never
// renders the connection string: testcontainers does not escape the password in
// it, so no masking of that string can be trusted.
func postgreSQLStartLogText(cfg *PostgreSQLContainerConfig, host string, port int) string {
	return fmt.Sprintf("PostgreSQL container started successfully at %s:%d (database: %s, user: %s)",
		host, port, cfg.Database, cfg.Username)
}
