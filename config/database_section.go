package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// dbStrictness is how normalization treats what a loaded configuration must
// state. Startup fails fast on identity gaps and on an explicit type that
// contradicts the connectionstring scheme; connect infers what it can, enforces
// the vendor rules that would otherwise fail silently open, fills defaults, and
// leaves identity to the dial (ADR-050, "the seam stays asymmetric by design").
type dbStrictness int

const (
	dbStrictnessStartup dbStrictness = iota
	dbStrictnessConnect
)

func rootDatabaseSection() section {
	return section{rootField: fieldDatabase, path: fieldDatabase, placement: placementRoot}
}

func namedDatabaseSection(name string) section {
	return section{
		rootField:      fieldDatabase,
		path:           "databases." + name,
		placement:      placementNamed,
		envUnreachable: keyIsEnvUnreachable(name),
	}
}

func tenantDatabaseSection(id string) section {
	return section{
		rootField:      fieldDatabase,
		path:           "multitenant.tenants." + id + ".database",
		placement:      placementTenant,
		envUnreachable: keyIsEnvUnreachable(id),
	}
}

// sectionForResourceKey maps a DBConfigProvider resource key onto the database section it
// resolves, so the runtime door addresses its errors the way the startup doors do. The key
// vocabulary is the manager's, unchanged: "" is the root (single-tenant) database, a
// NamedDatabasePrefix key is databases.<name>, and anything else is a tenant id.
func sectionForResourceKey(key string) section {
	switch {
	case key == "":
		return rootDatabaseSection()
	case strings.HasPrefix(key, NamedDatabasePrefix):
		return namedDatabaseSection(strings.TrimPrefix(key, NamedDatabasePrefix))
	default:
		return tenantDatabaseSection(key)
	}
}

// normalizeDatabaseValues turns a database section into the shape a connection
// can be opened from. It works on a clone and commits only when every step
// succeeds, so a rejected section returns untouched. The per-strictness step
// order is what the two doors ran before they shared this module — kept as is,
// because it decides which error a doubly-wrong section reports first.
func normalizeDatabaseValues(db *DatabaseConfig, sec section, strictness dbStrictness) error {
	normalized := *db

	var err error
	switch {
	case strictness == dbStrictnessConnect:
		err = normalizeForConnect(&normalized)
	case normalized.ConnectionString != "":
		err = normalizeWithConnectionString(&normalized)
	default:
		err = normalizeWithFields(&normalized)
	}
	if err != nil {
		// Addressed here rather than at each door: the constructors below share the root
		// spelling with the connect door, and this is the one seam every door crosses.
		return sec.qualify(err)
	}

	*db = normalized
	return nil
}

// normalizeForConnect infers a missing Type from a recognized scheme without
// erroring on a contradiction, rejects vendor field shapes that would fail
// silently open, and fills pool/session defaults. Identity is the dial's job.
func normalizeForConnect(db *DatabaseConfig) error {
	if db.Type == "" {
		db.Type = inferDatabaseTypeFromConnectionString(db.ConnectionString)
	}
	if err := validateVendorSpecificFields(db); err != nil {
		return err
	}
	return applyDatabasePoolDefaults(db)
}

// normalizeWithConnectionString is the startup path for a DSN-carrying section:
// an explicit Type that contradicts the scheme is an error, not an override.
func normalizeWithConnectionString(db *DatabaseConfig) error {
	if inferred := inferDatabaseTypeFromConnectionString(db.ConnectionString); inferred != "" {
		if db.Type == "" {
			db.Type = inferred
		} else if db.Type != inferred {
			return NewInvalidFieldError("database.type",
				fmt.Sprintf("conflicts with the connectionstring scheme (which implies %s)", inferred),
				[]string{inferred})
		}
	}
	if db.Type != "" {
		if err := validateDatabaseType(db.Type); err != nil {
			return err
		}
	}
	if err := validateOptionalDatabasePort(db.Port); err != nil {
		return err
	}
	if err := applyDatabasePoolDefaults(db); err != nil {
		return err
	}
	return validateVendorSpecificFields(db)
}

// normalizeWithFields is the startup path for a host/port/user section.
func normalizeWithFields(db *DatabaseConfig) error {
	if err := validateDatabaseType(db.Type); err != nil {
		return err
	}
	if err := validateDatabaseCoreFields(db); err != nil {
		return err
	}
	if err := validateVendorSpecificFields(db); err != nil {
		return err
	}
	return applyDatabasePoolDefaults(db)
}

// normalizeDatabaseSection is the startup door of the database-section
// normalization module: placement rules first, then normalizeDatabaseValues at
// startup strictness. Absence is a verdict at the root (ADR-047) and a missing
// section elsewhere; a manager block outside the root is rejected because the
// named and tenant databases share the primary DbManager and it would be
// silently ignored.
func normalizeDatabaseSection(db *DatabaseConfig, sec section) error {
	if !IsDatabaseConfigured(db) {
		if sec.placement == placementRoot {
			return nil
		}
		return &ConfigError{
			Category: errCategoryMissing,
			Field:    sec.path,
			Message:  errDatabaseIncomplete,
			Action:   "add host/type or connectionstring to " + sec.path,
		}
	}

	if err := normalizeDatabaseValues(db, sec, dbStrictnessStartup); err != nil {
		return err
	}

	if sec.placement != placementRoot && db.Manager.isSet() {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    sec.path + ".manager",
			Message:  "database.manager.* is only supported on the primary database",
			Action:   "remove the manager block from " + sec.path + "; tune the shared pool via database.manager.*",
		}
	}
	return nil
}

// forEachDatabaseSection visits every database section the deployment
// consumes: the root, each databases.* entry, and — only when multitenancy is
// enabled, since a leftover tenants block is inert otherwise — each static
// tenant's database. Map entries are copied out, visited, and written back so a
// visitor that normalizes sees its work persist. Keys are visited in sorted
// order so the first error, and any list built from the walk, is deterministic.
func forEachDatabaseSection(cfg *Config, visit func(sec section, db *DatabaseConfig) error) error {
	if err := visit(rootDatabaseSection(), &cfg.Database); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Databases)) {
		db := cfg.Databases[name]
		if err := visit(namedDatabaseSection(name), &db); err != nil {
			return err
		}
		cfg.Databases[name] = db
	}
	if !cfg.Multitenant.Enabled {
		return nil
	}
	for _, id := range slices.Sorted(maps.Keys(cfg.Multitenant.Tenants)) {
		tenant := cfg.Multitenant.Tenants[id]
		if err := visit(tenantDatabaseSection(id), &tenant.Database); err != nil {
			return err
		}
		cfg.Multitenant.Tenants[id] = tenant
	}
	return nil
}

// UntypedDatabaseSections returns the path of every database section that
// carries a connectionstring whose vendor is still unresolved after
// normalization — a scheme inference does not recognize (ADR-050). Whether that
// is fatal depends on who connects, so this only reports; app.Builder decides.
// Paths come back in walk order, which is lexicographic. Nil when none.
func UntypedDatabaseSections(cfg *Config) []string {
	var paths []string
	_ = forEachDatabaseSection(cfg, func(sec section, db *DatabaseConfig) error {
		if db.ConnectionString != "" && db.Type == "" {
			paths = append(paths, sec.path)
		}
		return nil
	})
	return paths
}
