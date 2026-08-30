package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
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

// sectionForResourceKey maps a DBConfigProvider resource key onto the database section it
// resolves, so the runtime door addresses its errors the way the startup doors do. The key
// vocabulary is the manager's, unchanged: "" is the root (single-tenant) database, a
// NamedDatabasePrefix key is databases.<name>, and anything else is a tenant id.
func sectionForResourceKey(key string) dbSection {
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
func normalizeDatabaseValues(db *DatabaseConfig, section dbSection, strictness dbStrictness) error {
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
		return section.qualify(err)
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
			Message:  errDatabaseIncomplete,
			Action:   "add host/type or connectionstring to " + section.path,
		}
	}

	if err := normalizeDatabaseValues(db, section, dbStrictnessStartup); err != nil {
		return err
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

// qualify re-addresses an error raised against the root spelling to this section, so a
// consumer matching on ConfigError.Field learns WHICH section failed. The root section
// returns the error untouched; every other placement gets a rewritten copy — the original
// is left alone because normalization shares its error constructors with the connect door,
// which has no section path to speak of.
//
// The path is carried by Field alone, never also by a wrapping message: printing it in both
// places is how the same section path ends up in one error twice.
//
// Action is re-pointed with Field: a hint naming DATABASE_PORT for a databases.reporting
// failure sends an operator to write a partial root block, which ADR-047 then rejects as an
// incomplete section — a second failure manufactured by the hint itself (ADR-076 addendum).
func (s dbSection) qualify(err error) error {
	if s.placement == dbPlacementRoot {
		return err
	}
	return qualifyConfigError(err, s.path, s.qualifyField)
}

// qualifyConfigError re-addresses a ConfigError to a subtree: Field through addressField,
// Action re-pointed to match, Details cloned so the copy owns them. A non-ConfigError has no
// field to move, so it is wrapped with path instead — the only place a path is allowed into
// the message rather than the field.
//
// Shared with BOTH cache doors — the startup check (checkTenantCache) and the runtime factory,
// which reach it through the exported QualifyCacheConfigErrorForKey. The producers used to carry
// their own copy of this recipe, and one of them missed the Action step when it was added, which
// is the same drift C60.19 exists to end.
func qualifyConfigError(err error, path string, addressField func(string) string) error {
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		return fmt.Errorf("%s: %w", path, err)
	}
	qualified := *cfgErr
	qualified.Field = addressField(cfgErr.Field)
	qualified.Action = requalifyAction(cfgErr.Action, cfgErr.Field, qualified.Field)
	qualified.Details = slices.Clone(cfgErr.Details)
	return &qualified
}

// requalifyAction re-points a generated "set X env var or add Y to config.yaml" hint at the
// qualified key. It rewrites only a hint this package generated, recognized by rebuilding that
// hint for the key the hint itself names and comparing — so a hand-written Action, and one
// naming a key outside the field being qualified, are both left exactly as they are.
//
// The key in the hint is not always the Field: NewNotConfiguredError puts the FEATURE in Field
// ("cache") and the YAML path in the hint ("cache.enabled"), behind a "to enable: " lead-in. So
// the key is read out of the hint and re-pointed by the same field-to-field move, which is what
// keeps that hint from surviving qualification and sending an operator at the root key.
//
// A hint shape this function does not recognize is left as it is, which is the safe direction but
// not a free one: a future constructor whose Action does not rebuild from missingFieldAction —
// a different lead-in, or two keys in one hint — keeps a root-spelled hint beside a qualified
// Field until it is taught here.
func requalifyAction(action, origField, qualifiedField string) string {
	if action == "" || origField == "" {
		return action
	}
	lead, body := "", action
	if rest, found := strings.CutPrefix(action, actionEnableLeadIn); found {
		lead, body = actionEnableLeadIn, rest
	}
	key, ok := yamlKeyFromAction(body)
	if !ok || body != missingFieldAction(key) {
		return action
	}
	qualifiedKey, ok := reattachHead(key, origField, qualifiedField)
	if !ok {
		return action
	}
	return lead + missingFieldAction(qualifiedKey)
}

// yamlKeyFromAction reads back the YAML key a generated hint names. Both templates end in
// "add <key> to config.yaml", so the key is what sits between them. The caller still rebuilds
// the hint from the key and compares, which is what proves the text was generated rather than
// merely shaped like it — including that its env half is the one envVarForKey derives.
func yamlKeyFromAction(action string) (string, bool) {
	const addPrefix, yamlSuffix = "add ", " to config.yaml"
	rest, ok := strings.CutSuffix(action, yamlSuffix)
	if !ok {
		return "", false
	}
	i := strings.LastIndex(rest, addPrefix)
	if i < 0 {
		return "", false
	}
	return rest[i+len(addPrefix):], true
}

// reattachHead moves one dotted key from oldHead to newHead: the head itself becomes newHead, a
// key UNDER it keeps its remainder, and anything else is not oldHead's to move and reports false.
// It is the one place that rule lives — the field qualifiers and the hint re-pointer all read a
// key against a head this way, and the dot is the delimiter each of them measures in, so the
// trap missingFieldAction documents (a dot inside a section or tenant NAME) is one trap here
// rather than one per caller.
func reattachHead(key, oldHead, newHead string) (string, bool) {
	switch {
	case key == oldHead:
		return newHead, true
	case strings.HasPrefix(key, oldHead+"."):
		return newHead + strings.TrimPrefix(key, oldHead), true
	default:
		return "", false
	}
}

// missingFieldAction is the hint NewMissingFieldError builds for key. The env half is
// dropped when no variable reaches the key (see envVarForKey), leaving the YAML path,
// which is always reachable.
//
// The guard covers every non-injective case of the transform EXCEPT a dot inside a section
// or tenant NAME, since the dot is the delimiter the round trip is measured in:
// multitenant.tenants.acme.corp.database.port round-trips cleanly but unflattens to tenant
// "acme", sub-key "corp". No producer can reach that today — koanf cannot deliver a map key
// with an embedded dot, and the connect door raises no missing-field errors — so it is a
// trap for a future caller rather than a live hole. Suppress the env half explicitly if you
// ever raise one of these from a free-form key.
func missingFieldAction(key string) string {
	if envVar := envVarForKey(key); envVar != "" {
		return fmt.Sprintf(actionSetEnvOrYAMLPath, envVar, key)
	}
	return fmt.Sprintf(actionAddYAMLPath, key)
}

// qualifyField rewrites one root-spelled field to this section. A key under the root
// section swaps its "database" head for the section path, so "database.host" reads
// "databases.reporting.host" and the tenant spelling keeps its own trailing ".database".
// A field that is not key-shaped — the Oracle connection-identifier check names one — is
// prefixed instead, which keeps the offending name rather than dropping it.
func (s dbSection) qualifyField(field string) string {
	if field == "" {
		return s.path
	}
	if qualified, ok := reattachHead(field, fieldDatabase, s.path); ok {
		return qualified
	}
	return s.path + "." + field
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
