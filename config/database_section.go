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

// identityKeyHost and identityKeyPort name recurring identity-key suffixes as
// constants (goconst: both bare literals recur elsewhere in the package's
// test fixtures).

// databaseIdentityKeys mirrors IsDatabaseConfigured's field set — the koanf
// key suffixes whose PRESENCE marks a database section as delivered. Keep the
// two in lockstep; TestDatabaseIdentityKeysMatchPredicate pins it.
var databaseIdentityKeys = []string{
	"connectionstring", "type", identityKeyHost, identityKeyPort, fieldDatabase,
	"username", "password", "oracle.service.name", "oracle.service.sid",
}

// pgSSLModes mirrors the sslmode values pgx v5 accepts in configTLS; anything
// else fails only at connect time with a redacted parse error, so gate it here.
var pgSSLModes = []string{sslModeDisable, sslModeAllow, sslModePrefer, sslModeRequire, sslModeVerifyCA, sslModeVerifyFull}

// pgTLSMandatorySSLModes are the modes under which pgx is guaranteed to use
// configured TLS material; the opportunistic modes silently discard or
// downgrade it.
var pgTLSMandatorySSLModes = []string{sslModeRequire, sslModeVerifyCA, sslModeVerifyFull}

// validateNoDeliveredEmptyDatabase fails startup when any database section the
// deployment consumes was delivered with only empty identity fields — the shape
// ADR-047 could not see (ADR-051). Inert for hand-built Config literals (no
// koanf instance) and for dynamic-source tenant configs (never in koanf). Every
// offending key is reported, not just the first: the error promises
// "field(s)", and an operator who clears only the one named would hit the same
// abort again. Sorted, so the startup error is deterministic.
func validateNoDeliveredEmptyDatabase(cfg *Config) error {
	var offending []string
	_ = forEachDatabaseSection(cfg, func(sec section, db *DatabaseConfig) error {
		if IsDatabaseConfigured(db) {
			return nil
		}
		for _, k := range databaseIdentityKeys {
			if key := sec.path + "." + k; cfg.Exists(key) {
				offending = append(offending, key)
			}
		}
		return nil
	})
	if len(offending) == 0 {
		return nil
	}
	slices.Sort(offending)
	return &ConfigError{
		Category: errCategoryInvalid,
		Field:    offending[0],
		Message:  fmt.Sprintf("database identity field(s) delivered empty: %v", offending),
		Action: "set real values (empty secretKeyRef / unset envsubst variable?) or remove the keys entirely — " +
			"an absent database section is the supported database-free posture (ADR-047, ADR-051)",
	}
}

// IsDatabaseConfigured reports whether a database is intentionally configured
// (ADR-003, ADR-047).
//
// Any connection-identity field counts as intent. The strictness is the point: a
// partially delivered database section must fail validation loudly rather than read
// as an intentionally database-free service, because everything downstream treats
// "no database at all" as a benign posture. Only a section with literally zero
// identity fields is absence.
//
// Fields that applyDatabasePoolDefaults fills in (timezone, pool, query) are
// deliberately excluded, so the verdict is identical before and after defaulting.
//
// A field delivered as an EMPTY string (an empty secretKeyRef, envsubst over an
// unset variable) is indistinguishable from an unset one here and reads as
// absence — but that shape is now caught at Load time by
// validateNoDeliveredEmptyDatabase (ADR-051), which consults koanf key presence
// rather than decoded values. The remaining blind spots are hand-built Config
// values (no koanf instance to consult) and dynamic-source tenant configs
// (resolved from a remote store, never koanf). TLS material is likewise
// excluded from this predicate — it identifies no database on its own.
//
// The answer must not change across normalizeDatabaseSection: normalization
// never adds identity to a section that had none, which is what lets check
// consult this predicate after normalize (validateNoSingleTenantConflict).
func IsDatabaseConfigured(cfg *DatabaseConfig) bool {
	return cfg.ConnectionString != "" ||
		cfg.Type != "" ||
		cfg.Host != "" ||
		cfg.Port != 0 ||
		cfg.Database != "" ||
		cfg.Username != "" ||
		cfg.Password != "" ||
		// Oracle names its target with a service name or SID instead of a database
		// name, so a split config delivering only those is still intent.
		cfg.Oracle.Service.Name != "" ||
		cfg.Oracle.Service.SID != ""
}

// inferDatabaseTypeFromConnectionString maps a recognized DSN scheme to its vendor.
// Surrounding whitespace is tolerated for classification only — a DSN read from a
// file, a mounted secret, or a command substitution routinely carries a trailing
// newline, and losing the scheme match there would silently leave the config
// untyped. The caller's stored DSN is never rewritten; whether the untrimmed value
// then fails at dial is the driver's business. An unrecognized scheme returns "" and
// is deliberately not an error here: whether an untyped DSN is fatal depends on who
// connects (ADR-050).
func inferDatabaseTypeFromConnectionString(cs string) string {
	lower := strings.ToLower(strings.TrimSpace(cs))
	switch {
	case strings.HasPrefix(lower, "postgres://"), strings.HasPrefix(lower, "postgresql://"):
		return PostgreSQL
	case strings.HasPrefix(lower, "oracle://"):
		return Oracle
	}
	return ""
}

// validateDatabaseType validates that dbType is one of the supported database type
// constants (PostgreSQL or Oracle). It returns nil when dbType is valid and an
// error describing the invalid value and the allowed types when it is not.
func validateDatabaseType(dbType string) error {
	validTypes := []string{PostgreSQL, Oracle}
	if !slices.Contains(validTypes, dbType) {
		return NewInvalidFieldError("database.type", fmt.Sprintf(errNotSupportedFmt, dbType), validTypes)
	}
	return nil
}

func validateDatabaseCoreFields(cfg *DatabaseConfig) error {
	if cfg.Host == "" {
		return NewMissingFieldError("database.host", "DATABASE_HOST", "database.host")
	}

	if err := validateRequiredDatabasePort(cfg.Port); err != nil {
		return err
	}

	// For Oracle, database name is optional if Service.Name or SID is provided
	// Oracle-specific validation will provide more detailed error messages
	if cfg.Type != Oracle && cfg.Database == "" {
		return NewMissingFieldError("database.database", "DATABASE_DATABASE", "database.database")
	}

	if cfg.Username == "" {
		return NewMissingFieldError("database.username", "DATABASE_USERNAME", "database.username")
	}

	// A non-empty password below MinDatabasePasswordLength cannot be safely
	// redacted from Flyway output at migration time, so the engine would suppress
	// the whole output and hide the migration outcome. Reject it here (message
	// never echoes the value). Empty passwords (trust/IAM auth) are exempt.
	if cfg.Password != "" && len(cfg.Password) < MinDatabasePasswordLength {
		return NewInvalidFieldError(
			fieldDatabasePassword,
			fmt.Sprintf("must be at least %d characters when set", MinDatabasePasswordLength),
			[]string{fmt.Sprintf("%d+ characters", MinDatabasePasswordLength)},
		)
	}

	return nil
}

func validateOptionalDatabasePort(port int) error {
	if port < 0 || port > 65535 {
		return NewInvalidFieldError(fieldDatabasePort, fmt.Sprintf(errInvalidField, port), []string{portRange})
	}
	return nil
}

func validateRequiredDatabasePort(port int) error {
	if port <= 0 {
		return NewMissingFieldError(fieldDatabasePort, "DATABASE_PORT", fieldDatabasePort)
	}
	if port > 65535 {
		return NewInvalidFieldError(fieldDatabasePort, "invalid port; must be between 1 and 65535", []string{portRange})
	}
	return nil
}

// applyConnectionCountDefaults defaults and validates the max/idle connection
// counts. Max is defaulted first; idle then defaults to — and is capped at — max
// so the pool reuses warm connections instead of churning them (TCP+TLS+auth) on
// every burst. database/sql caps idle at max-open, so an explicit idle above max
// is clamped here to keep the reported value (startup log, Stats(), OTEL gauges)
// truthful. An explicit idle below max is honored. See ADR-025.
func applyConnectionCountDefaults(cfg *DatabaseConfig) error {
	if cfg.Pool.Max.Connections == 0 {
		cfg.Pool.Max.Connections = defaultPoolMaxConnections
	} else if cfg.Pool.Max.Connections < 0 {
		return NewValidationError("database.pool.max.connections", errMustBeNonNegative)
	}

	if cfg.Pool.Idle.Connections < 0 {
		return NewValidationError("database.pool.idle.connections", errMustBeNonNegative)
	}
	if cfg.Pool.Idle.Connections == 0 || cfg.Pool.Idle.Connections > cfg.Pool.Max.Connections {
		cfg.Pool.Idle.Connections = cfg.Pool.Max.Connections
	}
	return nil
}

// ApplyDatabasePoolDefaults normalizes a DatabaseConfig for connection: it infers
// a missing Type from a recognized connectionstring scheme, rejects vendor field
// combinations the driver would silently drop, and fills zero-value Pool,
// Timezone, and Query (log/slow-threshold) settings with the documented defaults
// (25 max connections, idle tracks max, keepalive rules, UTC timezone).
//
// Exported so callers that bypass Validate — notably dynamic multi-tenant
// DBConfigProviders resolved in DbManager — get the same normalization as static
// config; the inference (ADR-050) is what lets a provider's DSN-only config dial
// instead of failing on the factory's empty-type dispatch. Unlike config.Validate,
// this seam never errors on an explicit Type that contradicts the scheme — it is on
// the per-tenant connection path, where the vendor dial error is the right failure.
// It does reject Oracle TLS material and an unpaired PostgreSQL sslcert/sslkey,
// because that failure mode is silent and open rather than loud at dial. The
// asymmetry is deliberate.
//
// It is the connect-strictness door of the database-section normalization
// module (database_section.go); a rejected config returns untouched.
//
// It addresses its errors to the ROOT database section. A caller that knows which section
// the config came from should use ApplyDatabasePoolDefaultsForKey instead, so a failure
// names that section rather than the root (ADR-076 addendum, C60.19).
func ApplyDatabasePoolDefaults(cfg *DatabaseConfig) error {
	return ApplyDatabasePoolDefaultsForKey(cfg, "")
}

// ApplyDatabasePoolDefaultsForKey is ApplyDatabasePoolDefaults addressed to the section the
// config was resolved for. resourceKey is the DBConfigProvider key: "" is the root database,
// a NamedDatabasePrefix key is databases.<name>, and anything else is a tenant id.
//
// Passing it is what lets a dynamically-resolved tenant report the same
// multitenant.tenants.<id>.database.<key> field a statically-declared one does, so a consumer
// routing on ConfigError.Field cannot tell the startup and runtime doors apart. It is a
// separate function rather than a second parameter because tools/migration is a separate
// module pinned to a RELEASED go-bricks, so an arity change there cannot compile until the
// next tag — see C60.19.
func ApplyDatabasePoolDefaultsForKey(cfg *DatabaseConfig, resourceKey string) error {
	sec := kindDatabase(resourceKey)
	if cfg == nil {
		return sec.qualify(NewValidationError(fieldDatabase, "configuration is nil"))
	}
	return normalizeDatabaseValues(cfg, sec, dbStrictnessConnect)
}

// applyDatabasePoolDefaults sets production-safe defaults and validates database pool/query/session settings.
//
// It modifies cfg in-place:
//   - Timezone: if empty, sets to "UTC"; validates with time.LoadLocation unless set to "-".
//   - Pool.Max.Connections / Pool.Idle.Connections: defaulted and validated by
//     applyConnectionCountDefaults (idle defaults to and is capped at max; see ADR-025).
//   - Pool.Idle.Time: if 0, sets to 5m (closes idle connections before NAT/firewall timeout); if negative, returns an error.
//   - Pool.Lifetime.Max: if 0, sets to 30m (forces periodic connection recycling); if negative, returns an error.
//   - Pool.KeepAlive.Enabled: if absent (nil), sets to true (recommended for cloud); an
//     explicit true or false is honored regardless of Interval.
//   - Pool.KeepAlive.Interval: if 0, sets to 60s (below typical NAT timeouts); this never
//     flips Enabled.
//   - Query.Log.MaxLength: if negative, returns an error; if 0, sets to defaultMaxQueryLength.
//   - Query.Slow.Threshold: if negative, returns an error; if 0, sets to defaultSlowQueryThreshold.
//
// Returns an error when any value is invalid; otherwise returns nil.
func applyDatabasePoolDefaults(cfg *DatabaseConfig) error {
	if err := applyDatabaseTimezoneDefault(cfg); err != nil {
		return err
	}

	if err := applyConnectionCountDefaults(cfg); err != nil {
		return err
	}

	// Apply default idle time - closes connections before NAT/firewall timeout
	if cfg.Pool.Idle.Time == 0 {
		cfg.Pool.Idle.Time = defaultPoolIdleTime
	} else if cfg.Pool.Idle.Time < 0 {
		return NewValidationError("database.pool.idle.time", errMustBeNonNegative)
	}

	// Apply default connection lifetime - forces periodic recycling
	if cfg.Pool.Lifetime.Max == 0 {
		cfg.Pool.Lifetime.Max = defaultPoolLifetimeMax
	} else if cfg.Pool.Lifetime.Max < 0 {
		return NewValidationError("database.pool.lifetime.max", errMustBeNonNegative)
	}

	// Apply keep-alive defaults for cloud deployments. Enabled and Interval are
	// defaulted independently so an explicit enabled=false is always honored,
	// even when Interval is left at its zero default (the natural opt-out).
	//   - Enabled: default to true only when the key is absent (nil). An explicit
	//     true or false survives untouched.
	//   - Interval: default to 60s when zero; this never flips Enabled.
	if cfg.Pool.KeepAlive.Enabled == nil {
		enabled := defaultKeepAliveEnabled
		cfg.Pool.KeepAlive.Enabled = &enabled
	}
	if cfg.Pool.KeepAlive.Interval == 0 {
		cfg.Pool.KeepAlive.Interval = defaultKeepAliveInterval
	} else if cfg.Pool.KeepAlive.Interval < 0 {
		return NewValidationError("database.pool.keepalive.interval", errMustBeNonNegative)
	}

	if cfg.Query.Log.MaxLength < 0 {
		return NewValidationError("database.query.log.maxlength", errMustBeNonNegative)
	}
	if cfg.Query.Log.MaxLength == 0 {
		cfg.Query.Log.MaxLength = defaultMaxQueryLength
	}

	if cfg.Query.Slow.Threshold < 0 {
		return NewValidationError("database.query.slow.threshold", errMustBeNonNegative)
	}
	if cfg.Query.Slow.Threshold == 0 {
		cfg.Query.Slow.Threshold = defaultSlowQueryThreshold
	}

	return nil
}

// applyDatabaseTimezoneDefault sets cfg.Timezone to the default ("UTC") when unset
// and validates the configured value as a loadable IANA timezone. The opt-out
// sentinel "-" skips validation and tells the connection layer to leave session
// timezone untouched.
func applyDatabaseTimezoneDefault(cfg *DatabaseConfig) error {
	normalized, err := normalizeIANATimezone("database.timezone", cfg.Timezone)
	cfg.Timezone = normalized
	return err
}

// normalizeNamedDatabases shapes every databases.* section (opaque; see
// normalizeDatabaseSection) and writes the result back so the defaults reach
// downstream consumers such as TenantStore. The map-key rules (empty name,
// reserved prefix, tenant-ID collision) are check's — see checkNamedDatabases.
func normalizeNamedDatabases(databases map[string]DatabaseConfig) error {
	// Sorted, like forEachDatabaseSection: with several malformed entries the
	// startup error names the same one every run.
	for _, name := range slices.Sorted(maps.Keys(databases)) {
		dbCfg := databases[name]
		if err := normalizeDatabaseSection(&dbCfg, namedDatabaseSection(name)); err != nil {
			return err
		}
		databases[name] = dbCfg
	}
	return nil
}

// checkNamedDatabases rejects a databases.* map key without touching its
// section's content (that is normalizeNamedDatabases' job).
func checkNamedDatabases(databases map[string]DatabaseConfig, mt *MultitenantConfig) error {
	for _, name := range slices.Sorted(maps.Keys(databases)) {
		if err := validateNamedDatabaseName(name, mt); err != nil {
			return err
		}
	}
	return nil
}

// validateVendorSpecificFields validates database vendor-specific configuration fields
//
// Reached from outside the module only through ApplyDatabasePoolDefaults, which the
// tools/migration CLI calls on every config it resolves — so a rule added here also
// tightens go-bricks-migrate at its next pin bump, with no separate copy to update.
func validateVendorSpecificFields(cfg *DatabaseConfig) error {
	// Trim once here so both vendors and the downstream DSN builder see canonical values.
	cfg.TLS.Mode = strings.TrimSpace(cfg.TLS.Mode)
	cfg.TLS.CertFile = strings.TrimSpace(cfg.TLS.CertFile)
	cfg.TLS.KeyFile = strings.TrimSpace(cfg.TLS.KeyFile)
	cfg.TLS.CAFile = strings.TrimSpace(cfg.TLS.CAFile)

	switch cfg.Type {
	case Oracle:
		return validateOracleFields(cfg)
	case PostgreSQL:
		return validatePostgreSQLFields(cfg)
	default:
		// Unknown database type should have been caught by validateDatabaseType
		return nil
	}
}

// validatePostgreSQLFields fails closed on database.tls shapes that pgx would silently
// discard or downgrade (ADR-062). Check order is
// load-bearing: connectionstring short-circuits, then the mode allowlist, then the
// material/mode coherence rule, then the cert/key pairing.
func validatePostgreSQLFields(cfg *DatabaseConfig) error {
	if cfg.ConnectionString != "" {
		if cfg.TLS.Mode != "" || cfg.TLS.CertFile != "" || cfg.TLS.KeyFile != "" || cfg.TLS.CAFile != "" {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fieldDatabaseTLS,
				Message:  "database.tls is ignored when connectionstring is set (the DSN is used verbatim)",
				Action:   "move TLS settings into the connection string (sslmode/sslrootcert/sslcert/sslkey) and remove the database.tls block",
			}
		}
		return nil
	}

	if cfg.TLS.Mode != "" && !slices.Contains(pgSSLModes, cfg.TLS.Mode) {
		return NewInvalidFieldError("database.tls.mode", fmt.Sprintf(errInvalidField, cfg.TLS.Mode), pgSSLModes)
	}

	hasMaterial := cfg.TLS.CertFile != "" || cfg.TLS.KeyFile != "" || cfg.TLS.CAFile != ""
	if hasMaterial && !slices.Contains(pgTLSMandatorySSLModes, cfg.TLS.Mode) {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabaseTLS,
			Message: "TLS cert/key/ca require a mode that guarantees TLS; under disable/allow/prefer " +
				"(or an unset mode, which defaults to prefer) pgx silently discards the material, downgrades " +
				"to plaintext, or (for ca: system) silently upgrades the mode — none of which is what the config says",
			Action: "set database.tls.mode to require, verify-ca, or verify-full",
		}
	}

	// Client-certificate (mTLS) auth requires BOTH sslcert and sslkey; pgx rejects a lone
	// one only at connect time, where go-bricks redacts the parse error.
	if (cfg.TLS.CertFile != "") != (cfg.TLS.KeyFile != "") {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabaseTLS,
			Message:  "sslcert and sslkey must be configured together for client-certificate (mTLS) auth",
			Action:   "set both database.tls.cert and database.tls.key, or neither",
		}
	}
	return nil
}

// validateOracleFields validates Oracle-specific configuration fields.
// It ensures that exactly one of Service.Name, SID, or Database is configured,
// mirroring the DSN selection logic in database/oracle/connection.go — except in
// connection-string mode, where the DSN supplies the identifier and none is required.
func validateOracleFields(cfg *DatabaseConfig) error {
	// Oracle TLS (tcps/wallet) is not implemented in go-bricks, so reject the whole
	// database.tls block rather than silently ignoring it — mode included, since nothing
	// wires the TLS it implies, leaving an operator to believe the connection is encrypted.
	if cfg.TLS.Mode != "" || cfg.TLS.CertFile != "" || cfg.TLS.KeyFile != "" || cfg.TLS.CAFile != "" {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabaseTLS,
			Message:  "database.tls settings are not supported for Oracle (tcps/wallet is not implemented)",
			Action:   "remove the database.tls block for Oracle connections",
		}
	}

	serviceSet := cfg.Oracle.Service.Name != ""
	sidSet := cfg.Oracle.Service.SID != ""
	databaseSet := cfg.Database != ""

	count := 0
	if serviceSet {
		count++
	}
	if sidSet {
		count++
	}
	if databaseSet {
		count++
	}

	// buildOracleDSN returns ConnectionString verbatim, so none of these fields is
	// consulted in that mode — the DSN carries the identifier. Requiring a separate
	// one that is then ignored would reject a valid connection-string-only config.
	if count == 0 && cfg.ConnectionString == "" {
		return &ConfigError{
			Category: errCategoryMissing,
			Field:    "oracle connection identifier",
			Message:  "exactly one required",
			Action:   "set database.oracle.service.name, database.oracle.service.sid, or database.database",
		}
	}

	if count > 1 {
		configured := make([]string, 0, 3)
		if serviceSet {
			configured = append(configured, "service name")
		}
		if sidSet {
			configured = append(configured, "SID")
		}
		if databaseSet {
			configured = append(configured, "database name")
		}
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    "oracle connection identifier",
			Message:  "multiple identifiers configured",
			Action:   fmt.Sprintf("remove all but one of: %s", strings.Join(configured, ", ")),
		}
	}

	return nil
}
