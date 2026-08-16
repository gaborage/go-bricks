package config

import (
	"fmt"
	"maps"
	"slices"
)

// dbPlacement is where a database section sits in the configuration tree. It
// decides whether the section may be absent, whether a manager block is
// allowed, and how its errors are addressed.
type dbPlacement int

const (
	dbPlacementRoot   dbPlacement = iota // database — may be absent (ADR-047)
	dbPlacementNamed                     // databases.<name>
	dbPlacementTenant                    // multitenant.tenants.<id>.database
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

// dbSection names one database section: its path in the tree and its placement.
type dbSection struct {
	path      string
	placement dbPlacement
}

func rootDatabaseSection() dbSection {
	return dbSection{path: fieldDatabase, placement: dbPlacementRoot}
}

func namedDatabaseSection(name string) dbSection {
	return dbSection{path: "databases." + name, placement: dbPlacementNamed}
}

func tenantDatabaseSection(id string) dbSection {
	return dbSection{path: "multitenant.tenants." + id + ".database", placement: dbPlacementTenant}
}

// normalizeDatabaseValues turns a database section into the shape a connection
// can be opened from. It works on a clone and commits only when every step
// succeeds, so a rejected section returns untouched. The per-strictness step
// order is what the two doors ran before they shared this module — kept as is,
// because it decides which error a doubly-wrong section reports first.
func normalizeDatabaseValues(db *DatabaseConfig, strictness dbStrictness) error {
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
		return err
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
func normalizeDatabaseSection(db *DatabaseConfig, section dbSection) error {
	if !IsDatabaseConfigured(db) {
		if section.placement == dbPlacementRoot {
			return nil
		}
		return &ConfigError{
			Category: errCategoryMissing,
			Field:    section.path,
			Message:  "database configuration incomplete",
			Action:   "add host/type or connectionstring to " + section.path,
		}
	}

	if err := normalizeDatabaseValues(db, dbStrictnessStartup); err != nil {
		if section.placement == dbPlacementRoot {
			return err
		}
		return fmt.Errorf("%s: %w", section.path, err)
	}

	if section.placement != dbPlacementRoot && db.Manager.isSet() {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    section.path + ".manager",
			Message:  "database.manager.* is only supported on the primary database",
			Action:   "remove the manager block from " + section.path + "; tune the shared pool via database.manager.*",
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
func forEachDatabaseSection(cfg *Config, visit func(section dbSection, db *DatabaseConfig) error) error {
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
	_ = forEachDatabaseSection(cfg, func(section dbSection, db *DatabaseConfig) error {
		if db.ConnectionString != "" && db.Type == "" {
			paths = append(paths, section.path)
		}
		return nil
	})
	return paths
}
