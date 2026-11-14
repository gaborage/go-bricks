package containers

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

// postgreSQLDataMount is the image's declared volume. It holds PGDATA on every
// tag: /var/lib/postgresql/data before 18, /var/lib/postgresql/<major>/docker from 18.
const postgreSQLDataMount = "/var/lib/postgresql"

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
	// Settings are server parameters, each passed as a `-c key=value` argument in
	// sorted key order. A key that is empty or holds whitespace or '=' fails the
	// start before any container is created. Nil or empty changes nothing.
	Settings map[string]string
	// DataTmpfs, when set to a size such as "512m", backs the data directory with
	// a tmpfs of that size. Empty keeps the image's volume.
	DataTmpfs string
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

// postgreSQLSettingArgs renders Settings as `-c key=value` server arguments,
// sorted by key.
func postgreSQLSettingArgs(settings map[string]string) ([]string, error) {
	if len(settings) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(settings))
	for key := range settings {
		if key == "" || strings.ContainsFunc(key, func(r rune) bool { return r == '=' || unicode.IsSpace(r) }) {
			return nil, fmt.Errorf("postgresql container: setting key %q must be non-empty and hold no whitespace or '='", key)
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	args := make([]string, 0, 2*len(keys))
	for _, key := range keys {
		args = append(args, "-c", key+"="+settings[key])
	}
	return args, nil
}

// postgreSQLDataTmpfs maps the data mount to a tmpfs of the given size, or
// returns nil when size is empty.
func postgreSQLDataTmpfs(size string) map[string]string {
	if size == "" {
		return nil
	}
	return map[string]string{postgreSQLDataMount: "rw,size=" + size}
}
