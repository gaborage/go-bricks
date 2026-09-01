package config

import (
	"fmt"
	"regexp"
	"strings"
)

// sectionNamePattern is the grammar every USER-CHOSEN section key obeys:
// entries under databases, multitenant.tenants and keystore.keys. It is the
// resolver's tenant-ID grammar without the length bound, which stays the
// resolver's.
//
// The reason is reachability, not taste. Load maps an environment variable to
// a config key by lowercasing it and turning '_' into '.', which is not
// injective: DATABASES_REPORT_DB_PORT reaches databases.report.db.port, so a
// section named report_db cannot be addressed by any variable — its value
// either lands on a phantom key or, when a sibling named report exists,
// silently on the sibling. Uppercase is unreachable the same way. Rejecting
// the name at check makes the transform injective over every key that
// survives startup, without touching the transform itself (ADR-024).
//
// Hyphen is legal here; whether a hyphenated name is settable depends on the
// runtime (Docker and Kubernetes permit '-' in variable names, POSIX `export`
// does not), which the docs state and this rule does not police.
var sectionNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// checkSectionName rejects a user-chosen section key no environment variable
// can address. field is the key PATH, so an operator can find the entry.
func checkSectionName(field, name string) error {
	if sectionNamePattern.MatchString(name) {
		return nil
	}
	return &ConfigError{
		Category: errCategoryInvalid,
		Message:  fmt.Sprintf("name %q is not reachable by an environment variable", name),
		Field:    field,
		Action:   "rename it using lowercase letters, digits and '-' only: an environment variable lowercases and maps '_' to the config path delimiter, so any other name is unaddressable",
	}
}

// validateNamedDatabaseName checks the map key: non-empty, not the reserved
// prefix, reachable by an environment variable (checkSectionName), and not
// colliding with a static tenant ID.
func validateNamedDatabaseName(name string, mt *MultitenantConfig) error {
	if name == "" {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabases,
			Message:  "database name cannot be empty",
			Action:   "provide a non-empty key for each entry in databases section",
		}
	}
	// A '.' collides with koanf's path delimiter: constructed section paths
	// (databases.<name>) become ambiguous, so the bare "databases" Field is used
	// here rather than fmt.Sprintf(databasesFieldPrefix, name) — embedding the
	// dotted name would reproduce the same ambiguity in the error itself. This
	// runs BEFORE the reserved-prefix rule below, which does embed the name: a
	// name breaking both (`gb_.foo`) must still be reported against the parent.
	if strings.Contains(name, ".") {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabases,
			Message:  fmt.Sprintf("database name %q cannot contain '.' (the config path delimiter)", name),
			Action:   "rename the databases entry without dots",
		}
	}
	if strings.HasPrefix(name, NamedDatabasePrefix) {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fmt.Sprintf(databasesFieldPrefix, name),
			Message:  fmt.Sprintf("name cannot start with reserved prefix '%s'", NamedDatabasePrefix),
			Action:   fmt.Sprintf("rename databases.%s to remove the '%s' prefix", name, NamedDatabasePrefix),
		}
	}
	// Everything the dot rule above does not already reject is judged by the
	// shared reachability grammar. It runs after that rule because a dotted
	// name cannot carry an unambiguous Field, which this one needs.
	if err := checkSectionName(fmt.Sprintf(databasesFieldPrefix, name), name); err != nil {
		return err
	}
	if mt.Enabled && mt.Tenants != nil {
		if _, exists := mt.Tenants[name]; exists {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fmt.Sprintf(databasesFieldPrefix, name),
				Message:  fmt.Sprintf("name conflicts with tenant ID '%s'", name),
				Action:   fmt.Sprintf("rename databases.%s or multitenant.tenants.%s to avoid conflict", name, name),
			}
		}
	}
	return nil
}
