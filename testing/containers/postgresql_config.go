package containers

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"
)

// postgreSQLDataMount is the DataTmpfs mount point; PGDATA moves to postgreSQLTmpfsDataDir under it.
const (
	postgreSQLDataMount    = "/var/lib/postgresql"
	postgreSQLTmpfsDataDir = postgreSQLDataMount + "/tmpfs"
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
	// Settings are server parameters, each passed as a `-c key=value` argument in
	// sorted key order. A key that is empty or holds whitespace or '=' fails the
	// start before any container is created. Nil or empty changes nothing.
	//
	// The fixture waits for the server's ready line on the container log and
	// connects on the default port, so a setting that moves or silences the server
	// log (logging_collector, log_destination, log_min_messages) or changes the
	// listen address or port makes the start time out.
	Settings map[string]string
	// DataTmpfs, when set to a size such as "512m", mounts a tmpfs of that size at
	// /var/lib/postgresql and moves the data directory (PGDATA) to
	// /var/lib/postgresql/tmpfs on it, on every image tag. Empty leaves PGDATA and
	// the image's volume as they are.
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

// validatePostgreSQLSettings refuses a key PostgreSQL would misparse as a
// `-c key=value` argument.
func validatePostgreSQLSettings(settings map[string]string) error {
	for key := range settings {
		if key == "" || strings.ContainsFunc(key, func(r rune) bool { return r == '=' || unicode.IsSpace(r) }) {
			return fmt.Errorf("postgresql container: setting key %q must be non-empty and hold no whitespace or '='", key)
		}
	}
	return nil
}

// postgreSQLSettingArgs renders settings as `-c key=value` arguments sorted by key.
func postgreSQLSettingArgs(settings map[string]string) []string {
	if len(settings) == 0 {
		return nil
	}
	args := make([]string, 0, 2*len(settings))
	for _, key := range slices.Sorted(maps.Keys(settings)) {
		args = append(args, "-c", key+"="+settings[key])
	}
	return args
}

// postgreSQLDataTmpfs returns the tmpfs mount and the PGDATA env that must go with it, or nils when size is empty.
func postgreSQLDataTmpfs(size string) (tmpfs, env map[string]string) {
	if size == "" {
		return nil, nil
	}
	return map[string]string{postgreSQLDataMount: "rw,size=" + size},
		map[string]string{"PGDATA": postgreSQLTmpfsDataDir}
}
