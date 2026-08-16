# Database-Config Normalization + Validate as the Universal Door — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse the five database-config entry points into one normalization module behind two doors, make `config.Validate` run on every app construction path, and delete the mirrored defaults and bypass guards that existed only because it did not.

**Architecture:** A new unexported module in `config` (`database_section.go`) takes a database section plus its *placement* (root/named/tenant) and *strictness* (startup/connect) and owns inference, defaults, vendor rules and placement rules; `Validate` and `ApplyDatabasePoolDefaults` become its two doors, and a shared tree iterator serves the delivered-empty walk and a new exported `UntypedDatabaseSections`. `app.Builder.WithConfig` then calls `config.Validate`, which retires the `app/` re-walk, the mirrored constants and the bypass guards.

**Tech Stack:** Go 1.26, koanf, testify. Gates: `make check`, `make mutate` (background), `/simplify` → `/security-audit` → `/code-review` before push.

**Spec:** `docs/superpowers/specs/2026-08-15-database-config-normalization-design.md` · glossary `CONTEXT.md`

## Global Constraints

- Three PRs, one `/gh-stack`: **A** `refactor(config)` → **B** `fix(app)!` → **C** `refactor(config,app)`. Each must pass `make check`, the three pre-push gates and `make mutate` on its own. Base A on `main`; B on A; C on B.
- Names come from `CONTEXT.md`: *database section*, *placement*, *normalization*, *strictness*, *verdict*, *absence*, *delivered-but-empty*. Never `mode`, `role`, `sanitize`, `hydrate`.
- No exported-name changes: `ApplyDatabasePoolDefaults`, `IsDatabaseConfigured`, `Validate` keep signatures. `UntypedDatabaseSections` is the only new exported symbol (additive).
- Behaviour in PR A is byte-identical except: (1) named/tenant error wording (`Field` becomes the section path; wrap prefix becomes `<path>: `); (2) tenant `manager` rejection becomes `Category: invalid` (was `missing`). No other observable change.
- Test names camelCase; table-case names snake_case. Tests through the module's doors (`normalizeDatabaseSection`, `Validate`, `ApplyDatabasePoolDefaults`), never through folded helpers.
- Comments: bare minimum; only non-obvious intent or `// SECURITY:`.
- Never `--no-gpg-sign`. Commit with `git commit -F <file>`; verify `git log -1` after.
- ADR-064 is the next free number (checked 2026-08-15: 063 highest, no in-flight holder). Atom is `[C59.12]` under hop E59 (v0.58.1 → v0.59.0); if the 0.59.0 release cuts before B merges, renumber to the new open hop.

---

## PR A — `refactor(config): one database-section normalization module behind two doors`

Branch: `feature/config-database-section-normalization` off `main`.

### Task A1: Module skeleton — placement, strictness, section constructors

**Files:**

- Create: `config/database_section.go`
- Test: `config/database_section_test.go`

**Interfaces:**

- Produces: `type dbPlacement int` (`dbPlacementRoot`, `dbPlacementNamed`, `dbPlacementTenant`); `type dbStrictness int` (`dbStrictnessStartup`, `dbStrictnessConnect`); `type dbSection struct{ path string; placement dbPlacement }`; `rootDatabaseSection() dbSection`, `namedDatabaseSection(name string) dbSection`, `tenantDatabaseSection(id string) dbSection`.

- [ ] **Step 1: Write the failing test**

```go
package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDatabaseSectionConstructorsNamePathAndPlacement(t *testing.T) {
	tests := []struct {
		name          string
		section       dbSection
		wantPath      string
		wantPlacement dbPlacement
	}{
		{name: "root", section: rootDatabaseSection(), wantPath: "database", wantPlacement: dbPlacementRoot},
		{name: "named", section: namedDatabaseSection("reporting"), wantPath: "databases.reporting", wantPlacement: dbPlacementNamed},
		{name: "tenant", section: tenantDatabaseSection("acme"), wantPath: "multitenant.tenants.acme.database", wantPlacement: dbPlacementTenant},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantPath, tt.section.path)
			assert.Equal(t, tt.wantPlacement, tt.section.placement)
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./config/ -run TestDatabaseSectionConstructorsNamePathAndPlacement -count=1`
Expected: FAIL — `undefined: dbSection` (compile error).

- [ ] **Step 3: Write minimal implementation**

```go
package config

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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./config/ -run TestDatabaseSectionConstructorsNamePathAndPlacement -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add config/database_section.go config/database_section_test.go
printf 'refactor(config): name database sections by placement\n\nIntroduce dbSection (path + placement) and dbStrictness as the inputs of the\ndatabase-section normalization module. Vocabulary per CONTEXT.md.\n' > /tmp/msg && git commit -F /tmp/msg && git log -1 --format=%s
```

### Task A2: `normalizeDatabaseValues` — the shared core with clone-commit, both strictnesses

**Files:**

- Modify: `config/database_section.go`
- Modify: `config/validation.go:696-763` (delete `validateDatabase`'s body and `validateDatabaseWithConnectionString`), `:860-914` (`ApplyDatabasePoolDefaults` becomes a door)
- Test: `config/database_section_test.go`

**Interfaces:**

- Consumes: `dbStrictness` (A1); existing `inferDatabaseTypeFromConnectionString`, `validateDatabaseType`, `validateDatabaseCoreFields`, `validateOptionalDatabasePort`, `validateVendorSpecificFields`, `applyDatabasePoolDefaults`, `errConflictColumnsRequired` — all untouched.
- Produces: `func normalizeDatabaseValues(db *DatabaseConfig, strictness dbStrictness) error` — clone-commit: on error `*db` is untouched.

- [ ] **Step 1: Write the failing tests**

```go
func TestNormalizeDatabaseValuesStartupRejectsTypeContradictingScheme(t *testing.T) {
	cfg := DatabaseConfig{ConnectionString: "postgres://u:p@h:5432/d", Type: Oracle}
	before := cfg

	err := normalizeDatabaseValues(&cfg, dbStrictnessStartup)

	assertValidationError(t, err, "conflicts with the connectionstring scheme")
	assert.Equal(t, before, cfg, "clone-commit: a rejected config must come back untouched")
}

func TestNormalizeDatabaseValuesConnectToleratesTypeContradictingScheme(t *testing.T) {
	cfg := DatabaseConfig{ConnectionString: "postgres://u:p@h:5432/d", Type: Oracle}

	require.NoError(t, normalizeDatabaseValues(&cfg, dbStrictnessConnect))

	assert.Equal(t, Oracle, cfg.Type, "connect strictness keeps the explicit type; the dial reports the conflict")
	assert.Equal(t, defaultPoolMaxConnections, cfg.Pool.Max.Connections, "defaults are still applied")
}

func TestNormalizeDatabaseValuesConnectSkipsIdentityChecks(t *testing.T) {
	// A dynamic provider may return host/port/user only (PostgreSQL defaults the
	// database name to the user); startup would reject this, connect must not.
	cfg := DatabaseConfig{Type: PostgreSQL, Host: "h", Port: 5432, Username: "u"}

	require.NoError(t, normalizeDatabaseValues(&cfg, dbStrictnessConnect))
	require.Error(t, normalizeDatabaseValues(&DatabaseConfig{Type: PostgreSQL, Host: "h", Port: 5432, Username: "u"}, dbStrictnessStartup))
}

func TestNormalizeDatabaseValuesStartupPreservesPathOrder(t *testing.T) {
	// Field path: type → core fields → vendor → pool. A bad type wins over a
	// missing host; a missing host wins over bad TLS. Connection-string path:
	// inference/conflict → type → optional port → pool → vendor.
	tests := []struct {
		name string
		cfg  DatabaseConfig
		want string
	}{
		{name: "fields_type_before_host", cfg: DatabaseConfig{Type: "mysql"}, want: "database.type"},
		{name: "fields_host_before_tls", cfg: DatabaseConfig{Type: PostgreSQL, TLS: TLSConfig{CertFile: "c"}}, want: "database.host"},
		{name: "cs_pool_before_vendor", cfg: DatabaseConfig{ConnectionString: "postgres://u:p@h/d", TLS: TLSConfig{Mode: "require"}, Pool: PoolConfig{Idle: PoolIdleConfig{Time: -1}}}, want: "database.pool.idle.time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertValidationError(t, normalizeDatabaseValues(&tt.cfg, dbStrictnessStartup), tt.want)
		})
	}
}
```

`TLSConfig`, `PoolConfig`, `PoolIdleConfig` are the real struct names (`config/types.go:180,217,232`); `defaultPoolMaxConnections` is `int32(25)`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./config/ -run 'TestNormalizeDatabaseValues' -count=1`
Expected: FAIL — `undefined: normalizeDatabaseValues`.

- [ ] **Step 3: Implement the core; make `ApplyDatabasePoolDefaults` a door**

Append to `config/database_section.go`:

```go
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
```

Add `"fmt"` to the file's imports.

In `config/validation.go`:

- Delete `validateDatabaseWithConnectionString` (lines 719-763) entirely.
- Replace the body of `ApplyDatabasePoolDefaults` (keep the doc comment, but replace its last paragraph — "Normalization happens on a clone…" — with one sentence: `It is the connect-strictness door of the database-section normalization module (database_section.go); a rejected config returns untouched.`) with:

```go
func ApplyDatabasePoolDefaults(cfg *DatabaseConfig) error {
	if cfg == nil {
		return NewValidationError("database", "configuration is nil")
	}
	return normalizeDatabaseValues(cfg, dbStrictnessConnect)
}
```

- Leave `validateDatabase` in place for now (Task A3 replaces it) but make its body call the module so the package still compiles:

```go
func validateDatabase(cfg *DatabaseConfig) error {
	if !IsDatabaseConfigured(cfg) {
		return nil
	}
	return normalizeDatabaseValues(cfg, dbStrictnessStartup)
}
```

- [ ] **Step 4: Run the whole config package**

Run: `go test ./config/ -count=1`
Expected: PASS — the existing `TestApplyDatabasePoolDefaults*`, `TestValidateDatabase*`, `TestValidateInfersDatabaseTypeFromConnectionString`, `TestApplyDatabasePoolDefaultsRunsVendorValidation`, `TestApplyDatabasePoolDefaultsKeepsExplicitType` all still pass. If any error-precedence test fails, the step order in Step 3 drifted from the pre-refactor order — fix the order, not the test.

- [ ] **Step 5: Commit**

```bash
git add config/database_section.go config/database_section_test.go config/validation.go
printf 'refactor(config): share one normalization core between Validate and ApplyDatabasePoolDefaults\n\nnormalizeDatabaseValues owns inference, vendor rules and pool defaults under\ntwo strictnesses; ApplyDatabasePoolDefaults becomes its connect door. Step\norder per path is unchanged so error precedence is unchanged.\n' > /tmp/msg && git commit -F /tmp/msg && git log -1 --format=%s
```

### Task A3: `normalizeDatabaseSection` — placement rules; collapse root, named and tenant callers

**Files:**

- Modify: `config/database_section.go`
- Modify: `config/validation.go:210-212` (root call), `:696-717` (delete `validateDatabase`), `:1056-1131` (`validateNamedDatabases`, `validateNamedDatabaseEntry`), `:1844-1887` (`validateMultitenantTenants`)
- Test: `config/database_section_test.go`, `config/validation_test.go`

**Interfaces:**

- Consumes: `dbSection` constructors (A1), `normalizeDatabaseValues` (A2), `IsDatabaseConfigured`, `DatabaseManagerConfig.isSet`.
- Produces: `func normalizeDatabaseSection(db *DatabaseConfig, section dbSection) error` — startup strictness plus placement rules. Root absent → `nil`, no mutation. Named/tenant absent → `*ConfigError{Category: "missing", Field: <path>}`. Named/tenant with a `manager` block → `*ConfigError{Category: "invalid", Field: <path>+".manager"}`. Normalization errors on named/tenant are wrapped `"<path>: %w"`; root errors are returned bare (Validate adds `"database config: "`).
- Produces: `func validateNamedDatabaseName(name string, mt *MultitenantConfig) error` — the name-only checks that used to open `validateNamedDatabaseEntry`.

- [ ] **Step 1: Write the failing placement-matrix test**

```go
func TestNormalizeDatabaseSectionPlacementRules(t *testing.T) {
	configured := func() DatabaseConfig {
		return DatabaseConfig{Type: PostgreSQL, Host: "h", Port: 5432, Database: "d", Username: "u"}
	}
	withManager := func() DatabaseConfig {
		c := configured()
		c.Manager.MaxSize = 3
		return c
	}
	tests := []struct {
		name         string
		section      dbSection
		cfg          DatabaseConfig
		wantErr      string
		wantCategory string
		wantField    string
	}{
		{name: "root_absent_is_a_verdict_not_an_error", section: rootDatabaseSection(), cfg: DatabaseConfig{}},
		{name: "root_manager_block_allowed", section: rootDatabaseSection(), cfg: withManager()},
		{name: "named_absent_missing", section: namedDatabaseSection("r"), cfg: DatabaseConfig{}, wantErr: "database configuration incomplete", wantCategory: errCategoryMissing, wantField: "databases.r"},
		{name: "tenant_absent_missing", section: tenantDatabaseSection("t"), cfg: DatabaseConfig{}, wantErr: "database configuration incomplete", wantCategory: errCategoryMissing, wantField: "multitenant.tenants.t.database"},
		{name: "named_manager_rejected", section: namedDatabaseSection("r"), cfg: withManager(), wantErr: "only supported on the primary database", wantCategory: errCategoryInvalid, wantField: "databases.r.manager"},
		{name: "tenant_manager_rejected", section: tenantDatabaseSection("t"), cfg: withManager(), wantErr: "only supported on the primary database", wantCategory: errCategoryInvalid, wantField: "multitenant.tenants.t.database.manager"},
		{name: "named_normalization_error_wrapped_with_path", section: namedDatabaseSection("r"), cfg: DatabaseConfig{Type: "mysql", Host: "h"}, wantErr: "databases.r: "},
		{name: "tenant_normalization_error_wrapped_with_path", section: tenantDatabaseSection("t"), cfg: DatabaseConfig{Type: "mysql", Host: "h"}, wantErr: "multitenant.tenants.t.database: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeDatabaseSection(&tt.cfg, tt.section)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			assertValidationError(t, err, tt.wantErr)
			if tt.wantField == "" {
				return
			}
			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tt.wantCategory, cfgErr.Category)
			assert.Equal(t, tt.wantField, cfgErr.Field)
		})
	}
}

func TestNormalizeDatabaseSectionRootAbsentLeavesConfigUntouched(t *testing.T) {
	cfg := DatabaseConfig{}
	require.NoError(t, normalizeDatabaseSection(&cfg, rootDatabaseSection()))
	assert.Equal(t, DatabaseConfig{}, cfg, "absence must not pick up pool defaults — the verdict is identical before and after")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./config/ -run 'TestNormalizeDatabaseSection' -count=1`
Expected: FAIL — `undefined: normalizeDatabaseSection`.

- [ ] **Step 3: Implement the module door and collapse the three callers**

Append to `config/database_section.go`:

```go
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
```

In `config/validation.go`:

- `Validate`: replace `if err := validateDatabase(&cfg.Database); err != nil {` with `if err := normalizeDatabaseSection(&cfg.Database, rootDatabaseSection()); err != nil {`.
- Delete `validateDatabase` entirely.
- Replace `validateNamedDatabases` and `validateNamedDatabaseEntry` with:

```go
func validateNamedDatabases(databases map[string]DatabaseConfig, mt *MultitenantConfig) error {
	for _, name := range slices.Sorted(maps.Keys(databases)) {
		if err := validateNamedDatabaseName(name, mt); err != nil {
			return err
		}
		dbCfg := databases[name]
		if err := normalizeDatabaseSection(&dbCfg, namedDatabaseSection(name)); err != nil {
			return err
		}
		// Write back so the defaults reach downstream consumers such as TenantStore.
		databases[name] = dbCfg
	}
	return nil
}

// validateNamedDatabaseName checks the map key: non-empty, not the reserved
// prefix, and not colliding with a static tenant ID.
func validateNamedDatabaseName(name string, mt *MultitenantConfig) error {
	if name == "" {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    "databases",
			Message:  "database name cannot be empty",
			Action:   "provide a non-empty key for each entry in databases section",
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
```

- In `validateMultitenantTenants`, replace the block from `// Validate tenant database configuration` through the manager rejection (the `IsDatabaseConfigured` check, the `validateDatabase` call, and the `tenant.Database.Manager.isSet()` block) with:

```go
		if err := normalizeDatabaseSection(&tenant.Database, tenantDatabaseSection(tenantID)); err != nil {
			return err
		}
```

The `tenants[tenantID] = tenant` write-back and `validateTenantCache` call stay.

- [ ] **Step 4: Migrate the direct `validateDatabase(` tests and run the package**

Run: `sed -i '' -E 's/validateDatabase\(&([A-Za-z_.]+)\)/normalizeDatabaseSection(\&\1, rootDatabaseSection())/g' config/validation_test.go && grep -c 'normalizeDatabaseSection(&' config/validation_test.go` (BSD sed syntax, as executed on macOS; GNU sed takes `-i` with no argument)
Expected: 16 replacements, `grep -c 'validateDatabase(' config/validation_test.go` prints `0`.

Run: `go test ./config/ -count=1`
Expected: PASS. Two named/tenant assertions may need wording updates only if they pinned the old `tenant 'x' database` field or the `tenant x database:` prefix — `git grep -n "tenant '" config/validation_test.go` and `git grep -n 'database: ' config/validation_test.go` before editing; adjust to the section path.

- [ ] **Step 5: Commit**

```bash
git add config/database_section.go config/database_section_test.go config/validation.go config/validation_test.go
printf 'refactor(config): normalize root, named and tenant database sections through one door\n\nnormalizeDatabaseSection takes placement as an input: absence is a verdict at\nthe root and a missing section elsewhere, a manager block outside the root is\nrejected, and normalization errors carry the section path. The named and\ntenant loops shrink to name checks plus one call.\n' > /tmp/msg && git commit -F /tmp/msg && git log -1 --format=%s
```

### Task A4: One tree iterator; `UntypedDatabaseSections`; `app/` consumes it

**Files:**

- Modify: `config/database_section.go`
- Modify: `config/validation.go:600-660` (`deliveredEmptyDatabaseKeys`, `validateNoDeliveredEmptyDatabase`)
- Modify: `app/app_builder.go:225-236` (guard), `:271-305` (delete `untypedConnectionStringPaths`)
- Test: `config/database_section_test.go` (new), `app/app_builder_test.go:545-660` (unchanged — must still pass)

**Interfaces:**

- Produces: `func forEachDatabaseSection(cfg *Config, visit func(section dbSection, db *DatabaseConfig) error) error` — root, then `databases.*` in sorted key order, then `multitenant.tenants.*.database` in sorted key order **only when `cfg.Multitenant.Enabled`**; map entries are written back after `visit`; first error stops the walk.
- Produces (exported, additive): `func UntypedDatabaseSections(cfg *Config) []string` — paths of every section with a `connectionstring` and no `type`, in walk order (which is already lexicographic: `database` < `databases.*` < `multitenant.*`).

- [ ] **Step 1: Write the failing tests**

```go
func TestForEachDatabaseSectionWalksRootNamedThenGatedTenantsInSortedOrder(t *testing.T) {
	cfg := &Config{
		Databases: map[string]DatabaseConfig{"zeta": {}, "alpha": {}},
		Multitenant: MultitenantConfig{Enabled: true, Tenants: map[string]TenantEntry{"t2": {}, "t1": {}}},
	}
	var seen []string
	require.NoError(t, forEachDatabaseSection(cfg, func(s dbSection, _ *DatabaseConfig) error {
		seen = append(seen, s.path)
		return nil
	}))
	assert.Equal(t, []string{
		"database", "databases.alpha", "databases.zeta",
		"multitenant.tenants.t1.database", "multitenant.tenants.t2.database",
	}, seen)
}

func TestForEachDatabaseSectionSkipsTenantsWhenMultitenantDisabled(t *testing.T) {
	cfg := &Config{Multitenant: MultitenantConfig{Tenants: map[string]TenantEntry{"t1": {}}}}
	var seen []string
	require.NoError(t, forEachDatabaseSection(cfg, func(s dbSection, _ *DatabaseConfig) error {
		seen = append(seen, s.path)
		return nil
	}))
	assert.Equal(t, []string{"database"}, seen)
}

func TestForEachDatabaseSectionWritesMapEntriesBack(t *testing.T) {
	cfg := &Config{
		Databases: map[string]DatabaseConfig{"r": {}},
		Multitenant: MultitenantConfig{Enabled: true, Tenants: map[string]TenantEntry{"t": {}}},
	}
	require.NoError(t, forEachDatabaseSection(cfg, func(_ dbSection, db *DatabaseConfig) error {
		db.Host = "written"
		return nil
	}))
	assert.Equal(t, "written", cfg.Database.Host)
	assert.Equal(t, "written", cfg.Databases["r"].Host)
	assert.Equal(t, "written", cfg.Multitenant.Tenants["t"].Database.Host)
}

func TestForEachDatabaseSectionStopsAtFirstError(t *testing.T) {
	cfg := &Config{Databases: map[string]DatabaseConfig{"a": {}, "b": {}}}
	calls := 0
	err := forEachDatabaseSection(cfg, func(s dbSection, _ *DatabaseConfig) error {
		calls++
		if s.path == "databases.a" {
			return errors.New("stop")
		}
		return nil
	})
	require.EqualError(t, err, "stop")
	assert.Equal(t, 2, calls, "root then databases.a; databases.b never visited")
}

func TestUntypedDatabaseSectionsReportsEveryUntypedDSNInWalkOrder(t *testing.T) {
	cfg := &Config{}
	cfg.Database.ConnectionString = "sqlserver://h:1433/db"
	cfg.Databases = map[string]DatabaseConfig{
		"reporting": {ConnectionString: "sqlserver://h1:1433/db1"},
		"typed":     {ConnectionString: "sqlserver://h1:1433/db1", Type: PostgreSQL},
		"analytics": {ConnectionString: "sqlserver://h3:1433/db3"},
		"nodsn":     {Host: "h"},
	}
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Tenants = map[string]TenantEntry{
		"acme": {Database: DatabaseConfig{ConnectionString: "sqlserver://h2:1433/db2"}},
	}
	assert.Equal(t, []string{
		"database", "databases.analytics", "databases.reporting", "multitenant.tenants.acme.database",
	}, UntypedDatabaseSections(cfg))
}

func TestUntypedDatabaseSectionsIgnoresTenantsWhenMultitenantDisabled(t *testing.T) {
	cfg := &Config{Multitenant: MultitenantConfig{Tenants: map[string]TenantEntry{
		"acme": {Database: DatabaseConfig{ConnectionString: "sqlserver://h2:1433/db2"}},
	}}}
	assert.Empty(t, UntypedDatabaseSections(cfg))
}

func TestUntypedDatabaseSectionsIsNilWhenEveryDSNIsTyped(t *testing.T) {
	cfg := &Config{}
	cfg.Database = DatabaseConfig{ConnectionString: "postgres://u:p@h/d", Type: PostgreSQL}
	assert.Nil(t, UntypedDatabaseSections(cfg))
}
```

Add `"errors"` and `"github.com/stretchr/testify/require"` to the test file's imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./config/ -run 'TestForEachDatabaseSection|TestUntypedDatabaseSections' -count=1`
Expected: FAIL — `undefined: forEachDatabaseSection`.

- [ ] **Step 3: Implement the iterator and the exported walker; rewire delivered-empty; rewire app**

Append to `config/database_section.go` (add `"maps"` and `"slices"` to imports):

```go
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
```

In `config/validation.go`, replace `deliveredEmptyDatabaseKeys` and `validateNoDeliveredEmptyDatabase` (lines 600-660, keep the doc comments' substance) with:

```go
// validateNoDeliveredEmptyDatabase fails startup when any database section the
// deployment consumes was delivered with only empty identity fields — the shape
// ADR-047 could not see (ADR-051). Inert for hand-built Config literals (no
// koanf instance) and for dynamic-source tenant configs (never in koanf). Every
// offending key is reported, not just the first: the error promises
// "field(s)", and an operator who clears only the one named would hit the same
// abort again. Sorted, so the startup error is deterministic.
func validateNoDeliveredEmptyDatabase(cfg *Config) error {
	var offending []string
	_ = forEachDatabaseSection(cfg, func(section dbSection, db *DatabaseConfig) error {
		if IsDatabaseConfigured(db) {
			return nil
		}
		for _, k := range databaseIdentityKeys {
			if key := section.path + "." + k; cfg.Exists(key) {
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
```

In `app/app_builder.go`:

- Replace `if paths := untypedConnectionStringPaths(b.cfg); len(paths) > 0 {` with `if paths := config.UntypedDatabaseSections(b.cfg); len(paths) > 0 {`. Keep the error message unchanged in this PR (PR B rewords it).
- Delete `untypedConnectionStringPaths` and its doc comment (lines 271-305).
- Remove the now-unused `"slices"` import if nothing else in the file uses it (`goimports -l app/` will tell).

- [ ] **Step 4: Run config and app tests**

Run: `go test ./config/ ./app/ -count=1`
Expected: PASS — including `TestValidateNoDeliveredEmptyDatabase*`, `TestAppBuilderConfigureRuntimeHelpersRejectsUntypedConnectionString`, `TestAppBuilderConfigureRuntimeHelpersListsAllUntypedPaths`, `TestAppBuilderConfigureRuntimeHelpersIgnoresTenantsWhenMultitenantDisabled`.

- [ ] **Step 5: Commit**

```bash
git add config/database_section.go config/database_section_test.go config/validation.go app/app_builder.go
printf 'refactor(config): walk the database tree once; report untyped DSNs from config\n\nforEachDatabaseSection is the one traversal (root, sorted named, sorted tenants\ngated on multitenant.enabled, with map write-back). validateNoDeliveredEmpty-\nDatabase and the new exported UntypedDatabaseSections use it; app.Builder\nconsumes the latter and drops its own re-walk. ADR-050 connector policy stays\nin app.\n' > /tmp/msg && git commit -F /tmp/msg && git log -1 --format=%s
```

### Task A5: ADR-050 amendment, doc sweep, gates

**Files:**

- Modify: `wiki/adr_050_connectionstring_type_inference.md:6-26` (amendment block)
- Modify: `wiki/migrations.md` — only if `[C59.6]`'s scope line names `validateDatabaseWithConnectionString`; a `git grep -n 'validateDatabaseWithConnectionString\|validateNamedDatabaseEntry\|untypedConnectionStringPaths' wiki/ CLAUDE.md llms.txt` sweep decides
- Modify: `config/validation.go` doc comments that still name the deleted helpers (`git grep -n 'validateDatabaseWithConnectionString\|validateNamedDatabaseEntry\|validateDatabase\b' config/*.go` after A3)

- [ ] **Step 1: Append a second amendment paragraph to ADR-050's amendment block**

After the sentence ending `See Consequences and [migrations.md](migrations.md) \`[C59.6]\`.` add, inside the same `>` block:

```markdown
>
> **Amended (2026-08-15):** the "two call sites" above are now two *doors* of one
> module. `config.Validate` (root, named and tenant sections) and
> `config.ApplyDatabasePoolDefaults` both call `normalizeDatabaseValues` in
> `config/database_section.go`; the asymmetry this ADR describes is the module's
> *strictness* input (`startup` errors on a conflicting explicit `type`,
> `connect` tolerates it). The recognized-scheme list still lives only in
> `inferDatabaseTypeFromConnectionString`. The startup guard in
> `app.Builder.ConfigureRuntimeHelpers` now reads
> `config.UntypedDatabaseSections` instead of walking the tree itself; the
> connector exemption in item 2 is unchanged and stays app-side.
```

- [ ] **Step 2: Sweep stale names**

Run: `git grep -n 'validateDatabaseWithConnectionString\|validateNamedDatabaseEntry\|untypedConnectionStringPaths\|deliveredEmptyDatabaseKeys' -- ':!docs/superpowers/plans/*'`
Expected: hits only in `wiki/migrations.md` history atoms (leave those — they describe past releases) and possibly `wiki/adr_050_*.md` Context (leave — historical). Fix any hit in `config/*.go` comments or `wiki/database.md`/`CLAUDE.md`/`llms.txt`.

- [ ] **Step 3: Gates**

Run in background: `pwd && make check`. Then in order: `/simplify` → `make check` (if it changed code) → `/security-audit` → `/code-review`. Then `make mutate` in background (expect `(N mutants on changed lines)` with N > 0 and zero LIVED).

- [ ] **Step 4: Commit and push; open PR A**

```bash
git add wiki/adr_050_connectionstring_type_inference.md config/validation.go
printf 'docs(adr): amend ADR-050 for the shared normalization module\n' > /tmp/msg && git commit -F /tmp/msg && git log -1 --format=%s
```

Push via `/gh-stack` (base `main`). PR body (three headings only):

```markdown
## What
Five entry points normalized a database section with five rule subsets and `app/` re-walked the tree to catch what they missed. One module (`config/database_section.go`) now owns it behind two doors — `Validate` (placement-aware) and `ApplyDatabasePoolDefaults` (connect strictness) — and `config.UntypedDatabaseSections` replaces the app-side walk.

## Impact
Named/tenant database errors now carry the section path (`databases.<name>`, `multitenant.tenants.<id>.database`) as `Field` and prefix; a tenant `manager` block is rejected as `invalid` (was `missing`). No exported signature changes; `UntypedDatabaseSections` is new and additive.

## Verification
`make mutate` on changed lines: all killed. Error-precedence order per path is pinned by `TestNormalizeDatabaseValuesStartupPreservesPathOrder`.
```

---

## PR B — `fix(app)!: validate the config on every construction path`

Branch: `feature/app-validate-hand-built-configs` off PR A's branch.

### Task B1: `Builder.WithConfig` runs `config.Validate`

**Files:**

- Modify: `app/app_builder.go` (`WithConfig`, ~line 44; `ConfigureRuntimeHelpers` guard message ~line 231)
- Test: `app/app_builder_test.go`, `app/app_test.go:1477-1486`

**Interfaces:**

- Consumes: `config.Validate` (unchanged), `config.UntypedDatabaseSections` (A4).
- Produces: every `Builder` chain and `NewWithConfig` call now fails on an invalid config with `invalid configuration: <ConfigError>`; a config that passes gets defaults stamped (pool, manager, messaging, cache, startup) exactly as `config.Load` output does.

- [ ] **Step 1: Write the failing tests**

```go
func TestAppBuilderWithConfigValidatesHandBuiltConfig(t *testing.T) {
	cfg := defaultTestConfig()
	cfg.Database.Type = "mysql" // invalid vendor: Validate must reject at construction

	app, log, err := NewWithConfig(cfg, &Options{})

	require.Error(t, err)
	assert.Nil(t, app)
	assert.NotNil(t, log)
	assert.Contains(t, err.Error(), "invalid configuration")
	assert.Contains(t, err.Error(), "database.type")
}

func TestAppBuilderWithConfigStampsDefaultsOnHandBuiltConfig(t *testing.T) {
	cfg := defaultTestConfig()

	_, _, err := NewWithConfig(cfg, &Options{})

	require.NoError(t, err)
	assert.Equal(t, int32(25), cfg.Database.Pool.Max.Connections, "pool defaults reach hand-built configs")
	assert.Positive(t, cfg.Messaging.Publisher.IdleTTL, "messaging defaults reach hand-built configs")
}

func TestAppBuilderWithConfigRejectsNilConfig(t *testing.T) {
	app, log, err := NewWithConfig(nil, &Options{})
	require.Error(t, err)
	assert.Nil(t, app)
	assert.NotNil(t, log)
}
```

`defaultTestConfig` (app/app_test.go:533) lacks `Server.Timeout` values and a `Database.Database`/`Username` — `validateServer` requires positive timeouts and the field path requires core identity — so the fixture itself must become Validate-passing in Step 3, which is what makes the second test honest.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./app/ -run 'TestAppBuilderWithConfig' -count=1`
Expected: the two new tests FAIL (`NewWithConfig` currently accepts the invalid config and stamps nothing).

- [ ] **Step 3: Implement**

In `app/app_builder.go`, `WithConfig`:

```go
func (b *Builder) WithConfig(cfg *config.Config, opts *Options) *Builder {
	if b.err != nil {
		return b
	}
	if cfg == nil {
		b.err = errors.New("configuration required")
		return b
	}
	// Every construction path validates — config.Load output included (Validate
	// is idempotent, so its second run is a no-op). This is what lets the
	// managers drop their mirrored defaults: no config reaches them unstamped.
	if err := config.Validate(cfg); err != nil {
		b.err = fmt.Errorf("invalid configuration: %w", err)
		return b
	}
	b.cfg = cfg
	b.opts = opts
	return b
}
```

In `ConfigureRuntimeHelpers`, reword the untyped-DSN remedy: replace `"run config.Validate or set <path>.type to postgresql or oracle"` with `"set <path>.type to postgresql or oracle, or use a recognized connectionstring scheme"` — the bypass the old wording pointed at no longer exists. Update the two assertions pinning it (`app/app_builder_test.go:595` and the comment block at `:583`).

Fixture repair (same step, mechanical): make `defaultTestConfig` Validate-passing — add `Server.Timeout` (Read 15s / Write 30s / Middleware 5s / Shutdown 10s), `Database.Database: "testdb"`, `Database.Username: "testuser"`. Then `go test ./app/ -count=1 2>&1 | head -60` and repair remaining hand-built configs file by file (expect hits in `app/app_builder_test.go` bare `&config.Config{…}` chains, `app/lifecycle_test.go`, `app/streams_setup_test.go:215` — that one documents the bypass and must now construct its invalid state through `Builder` struct literals (already the file's pattern at `app_builder_test.go:549`) or assert the new construction-time error). Delete the stale comment `app/app_test.go:1482` ("Config validation is NOT performed by NewWithConfig") and flip that subtest: an empty `config.Config{}` now fails (missing `app.name`).

- [ ] **Step 4: Run the affected packages**

Run: `go test ./app/ ./config/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A app/
printf 'fix(app)!: validate the config on every construction path\n\nBuilder.WithConfig runs config.Validate, so NewWithConfig and direct Builder\nuse fail fast on configs config.Load would have rejected, and defaults reach\nhand-built configs. ADR-064; migration atom C59.12.\n\nBREAKING CHANGE: a hand-built config that violates config.Validate now fails\napp construction instead of half-working.\n' > /tmp/msg && git commit -F /tmp/msg && git log -1 --format=%s
```

### Task B2: ADR-064, migrations atom, CLAUDE.md line

**Files:**

- Create: `wiki/adr_064_app_validates_every_config.md`
- Modify: `wiki/architecture_decisions.md` (entry after ADR-063 + foot counter `through ADR-063` → `through ADR-064`)
- Modify: `wiki/migrations.md` (atom + E59 hop-table row at line 47 + narrative preflight ~line 2333)
- Modify: `CLAUDE.md` (`## Breaking Changes`, one line after the ADR-062 entry at line 421)

- [ ] **Step 1: Check which hop is open**

Run: `GH_TOKEN=$(gh auth token -u gaborage) gh release list --repo gaborage/go-bricks --limit 1`
If the latest release is still v0.58.x, the atom is `[C59.12]` under hop E59. If v0.59.0 has shipped, open the next hop per the file's own pattern and number it `[C60.1]`; the steps below say C59.12 — substitute accordingly.

- [ ] **Step 2: Write the ADR**

`wiki/adr_064_app_validates_every_config.md`:

```markdown
# ADR-064: The App Validates Every Config It Is Handed

**Status:** Accepted
**Date:** 2026-08-15

## Context

`config.Load` validates; `app.NewWithConfig` did not. ADR-050 documented the
obligation — "hand-built configs must run `config.Validate` before
`NewWithConfig`" — but nothing enforced it, so parallel machinery softened the
bypass: `app.Builder.ConfigureRuntimeHelpers` re-walked the database tree for
untyped DSNs, `app/managers.go` mirrored config defaults ("kept in sync with
config/validation.go"), `messaging.NewMessagingManager` carried a
single-tenant-only fallback, and `app/lifecycle.go` guarded cleanup intervals
"only for Validate-bypassing callers". Every mirror was drift risk, and the
mode-blind ones could not honor the multi-tenant defaults.

## Decision

`app.Builder.WithConfig` runs `config.Validate` on the config it receives.
Every construction path — `New`, `NewWithOptions`, `NewWithConfig`, direct
`Builder` use — therefore validates and stamps defaults. `Validate` is
idempotent, so revalidating `config.Load` output costs microseconds and no
hidden "already validated" state is introduced.

## Consequences

- **Breaking:** a hand-built config that `config.Validate` rejects — missing
  `app.name`/`app.version`, zero server timeouts, an invalid vendor — now
  fails at construction instead of booting on whatever the mirrors papered
  over. Remedy per field is the `ConfigError`'s own action line. See
  [migrations.md](migrations.md) [C59.12].
- The app-side mirrors become dead weight and are deleted in the follow-up PR
  (`app/managers.go` default constants, the mode fallbacks in
  `resolveMaxSize`/`resolveIdleTTL`, `lifecycle.go`'s bypass guard).
  `messaging.NewMessagingManager` keeps its fallback: it is that standalone
  package's interface default for bare callers, not a mirror.
- Test fixtures must be valid configs — a fixture that could not boot in
  production should not boot in a test.
```

- [ ] **Step 3: Index entry + counter + atom + hop row + CLAUDE.md**

`wiki/architecture_decisions.md`: add after the ADR-063 block, matching the house format exactly (`### [ADR-064: The App Validates Every Config It Is Handed](adr_064_app_validates_every_config.md)`, `**Date:** 2026-08-15 | **Status:** Accepted`, one-paragraph summary naming the retired bypass machinery, `**Key Benefits:**` line, `---`). Foot: `ADR-001 through ADR-063` → `ADR-001 through ADR-064`.

`wiki/migrations.md` — append after `[C59.11]`:

```markdown
### [C59.12] `app.NewWithConfig` and `app.Builder.WithConfig` validate the config · breaking · when: match

- detect: `git grep -n 'NewWithConfig\|NewAppBuilder'` in your service. Every hit handing over a
  hand-built `*config.Config` (not `config.Load` output) is in scope — most services construct via
  `app.New()` and are unaffected.
- scope: `app.Builder.WithConfig` now runs `config.Validate`. The rules are the ones `config.Load`
  always applied; only the bypass is gone (ADR-050 documented the obligation, nothing enforced it).
- gate: match = a hand-built config violating any `config.Validate` rule — empty `app.name` or
  `app.version`, zero `server.timeout.*`, an invalid `database.type`, a negative pool value — now
  fails construction with `invalid configuration: …` naming the field. no-match = you construct via
  `app.New`/`NewWithOptions`, or you already ran `config.Validate` first.
- apply: fix the config, not the call site — each rejection's `ConfigError` carries an action line.
  Test fixtures are the common hit: give them `app.name`, `app.version`, positive server timeouts.
- verify: run your service's tests; construction-time failures name the field.
- ref: [ADR-064](adr_064_app_validates_every_config.md) · [ADR-050](adr_050_connectionstring_type_inference.md)
```

E59 hop-table row (line 47): count `11` → `12`, add C59.12 to the breaking list, append the preflight clause: *"and if any code hands `app.NewWithConfig` or a direct `app.Builder` chain a hand-built config, run it against `config.Validate`'s rules before the bump — missing `app.name`/`app.version`, zero server timeouts and invalid vendors now fail construction (C59.12)"*. Mirror in the E59 narrative preflight (~line 2333, "**nine** actions" → "**ten**").

`CLAUDE.md` after line 421:

```markdown

- **App validates every config (ADR-064):** `app.NewWithConfig`/`Builder.WithConfig` run `config.Validate`; hand-built configs that violate it fail construction.
```

Run `wc -c CLAUDE.md` before/after — file is over its 40,960 B ceiling; the line stays one sentence.

- [ ] **Step 4: Gates, commit, push**

`pwd && make check` in background; `/simplify` → `/security-audit` → `/code-review`; `make mutate` in background (expect N > 0, zero LIVED). Then:

```bash
git add wiki/adr_064_app_validates_every_config.md wiki/architecture_decisions.md wiki/migrations.md CLAUDE.md
printf 'docs: record ADR-064 and migration atom C59.12\n' > /tmp/msg && git commit -F /tmp/msg && git log -1 --format=%s
```

Push via `/gh-stack` (base: PR A's branch). CodeRabbit skips stacked PRs — post `@coderabbitai review` on the PR after push. PR body:

```markdown
## What
`app.Builder.WithConfig` now runs `config.Validate`, closing the documented-but-unenforced bypass: hand-built configs fail construction on the same rules `config.Load` always applied, and defaults reach them.

## Impact
Breaking for `NewWithConfig`/direct-`Builder` callers handing over configs that violate `config.Validate` — construction fails with the field named. `app.New`/`NewWithOptions` callers unaffected. ADR-064; atom C59.12.

## Verification
`make mutate` on changed lines: all killed.
```

---

## PR C — `refactor(config,app): delete the mirrored defaults the bypass required`

Branch: `feature/app-drop-mirrored-defaults` off PR B's branch.

**Load-bearing nuance (do not "simplify" past it):** `applyModeAwarePoolDefault` deliberately preserves a zero `MaxSize` in multi-tenant mode so the builder can scale the pool to the tenant limit (#661). The builder's multi-tenant `zero → tenantLimit` branches are therefore ALIVE and stay. What dies after PR B: the builder's *single-tenant* fallback constants (config stamps those), the *IdleTTL* mode fallbacks (config stamps IdleTTL unconditionally in both modes), and the negative-value laundering (Validate rejects negatives before the builder runs).

### Task C1: Shrink `ManagerConfigBuilder` to validated inputs + tenant-limit scaling

**Files:**

- Modify: `app/managers.go:15-35` (constants block), `:84-107` (`resolveMaxSize`, `resolveIdleTTL`), `:110-160` (`BuildDatabaseOptions`, `BuildMessagingOptions`, `BuildCacheOptions`)
- Modify: `app/lifecycle.go:39-51` (`cleanupIntervalTooLate` comment)
- Modify: `messaging/manager.go:108-120` (fallback comment)
- Test: `app/managers_test.go`

**Interfaces:**

- Consumes: config values already stamped by `config.Validate` (PR B).
- Produces: `resolveMaxSize(operatorValue int) int` — `operatorValue` if positive, else `b.tenantLimit` (multi-tenant zero-scaling; single-tenant zero cannot reach it). `resolveIdleTTL` deleted. `BuildDatabaseOptions`/`BuildMessagingOptions`/`BuildCacheOptions` pass validated values through.

- [ ] **Step 1: Update the tests to the post-Validate contract**

In `app/managers_test.go`, replace the bypass-era cases (the comment at `:108` names them: "Negatives can reach here via the app.NewWithConfig Validate-bypass") with the two surviving behaviours:

```go
func TestManagerConfigBuilderScalesZeroMaxSizeToTenantLimitInMultiTenant(t *testing.T) {
	b := NewManagerConfigBuilder(true, 250)
	// Multi-tenant zero is deliberate (config preserves it, #661): scale to the limit.
	assert.Equal(t, 250, b.BuildDatabaseOptions().MaxSize)
	assert.Equal(t, 250, b.BuildMessagingOptions().MaxPublishers)
	assert.Equal(t, 250, b.BuildCacheOptions().MaxSize)
}

func TestManagerConfigBuilderPassesValidatedValuesThrough(t *testing.T) {
	b := NewManagerConfigBuilder(false, 0)
	b.dbConfig = config.DatabaseManagerConfig{MaxSize: 7, IdleTTL: 3 * time.Minute}
	opts := b.BuildDatabaseOptions()
	assert.Equal(t, 7, opts.MaxSize)
	assert.Equal(t, 3*time.Minute, opts.IdleTTL)
}
```

Field spellings: confirm against the struct (`grep -n 'dbConfig\|publisherConfig\|cacheConfig' app/managers.go`) — they are unexported, tests live in package `app`.

- [ ] **Step 2: Run to see the new tests fail / old ones still pass**

Run: `go test ./app/ -run TestManagerConfigBuilder -count=1`
Expected: new tests may PASS already (multi-tenant scaling exists); the point of this step is a green baseline before deletion.

- [ ] **Step 3: Delete the dead halves**

- `app/managers.go` constants block: delete `defaultPublisherMaxCached`, `defaultPublisherIdleTTL`, `defaultPublisherIdleTTLMultiTenant`, `defaultCacheMaxSize`, `defaultCacheIdleTTL`, `defaultCacheCleanupInterval`, `defaultDatabaseMaxSize`, `defaultDatabaseIdleTTL`, `defaultDatabaseIdleTTLMultiTenant` and the "mirrors config/validation.go" comment block (lines 15-35).
- `resolveMaxSize` becomes:

```go
// resolveMaxSize returns the operator's validated value, or — multi-tenant
// only — scales a deliberately-preserved zero to the tenant limit (#661).
// Single-tenant zeros cannot reach here: config.Validate stamps the default.
func (b *ManagerConfigBuilder) resolveMaxSize(operatorValue int) int {
	if operatorValue > 0 {
		return operatorValue
	}
	return b.tenantLimit
}
```

- Delete `resolveIdleTTL`; call sites use the config value directly (`IdleTTL: b.dbConfig.IdleTTL`, `IdleTTL: b.publisherConfig.IdleTTL`).
- `BuildCacheOptions`: the `maxSize` fallback chain keeps only the multi-tenant `zero → tenantLimit` branch; `idleTTL`/`cleanupInterval` read the config values directly.
- `app/lifecycle.go:41-43`: replace the comment "config.Validate defaults IdleTTL unconditionally, so this guard only covers Validate-bypassing callers" with "idleTTL <= 0 cannot occur on a validated config; the branch defends direct App construction in tests." Code unchanged.
- `messaging/manager.go:110-118`: keep the fallback; replace the two comments with: "Interface default for bare callers constructing a manager without the app builder; single-tenant value — a bare caller supplies no deployment-mode signal. The app path always arrives with IdleTTL already stamped by config.Validate (ADR-064)." Delete the "kept in sync with app.defaultPublisherIdleTTL" sentence — there is no longer anything to sync with.

- [ ] **Step 4: Run the affected packages**

Run: `go test ./app/ ./messaging/ ./config/ -count=1 && go vet ./...`
Expected: PASS. Any failure here is a test that depended on the builder's own defaulting — fix the fixture to a validated config, not the builder.

- [ ] **Step 5: Commit**

```bash
git add app/managers.go app/managers_test.go app/lifecycle.go messaging/manager.go
printf 'refactor(app): drop the mirrored defaults ADR-064 made dead\n\nManagerConfigBuilder passes validated values through; only the deliberate\nmulti-tenant zero-to-tenant-limit scaling remains (#661). messaging keeps its\nown bare-caller fallback, now documented as an interface default rather than\na mirror.\n' > /tmp/msg && git commit -F /tmp/msg && git log -1 --format=%s
```

### Task C2: One source per shared default + pinning test

**Files:**

- Modify: `config/config.go:321-398` (`loadDefaults`)
- Test: `config/config_test.go`

**Interfaces:**

- Produces: `loadDefaults` renders every value that also has an `apply*Defaults` constant *from that constant* (the cache/redis block already does; `app.startup.timeout` is the one literal left — `"10s"` vs `defaultStartupTimeout`). Plus a pinning test locking koanf defaults to the apply-phase for the shared key set.

- [ ] **Step 1: Write the failing pinning test**

```go
func TestKoanfDefaultsMatchApplyDefaultsForSharedKeys(t *testing.T) {
	// Two mechanisms, one source: koanf defaults (Load path) and apply*Defaults
	// (Validate path) must produce the same values wherever both speak. A new
	// shared default belongs in a constant both sides render.
	loaded, err := loadDefaultConfig(t) // helper below
	require.NoError(t, err)

	applied := &Config{}
	require.NoError(t, applyStartupDefaults(&applied.App.Startup))
	applyRedisDefaults(&applied.Cache.Redis)

	assert.Equal(t, applied.App.Startup.Timeout, loaded.App.Startup.Timeout)
	assert.Equal(t, applied.Cache.Redis.DialTimeout, loaded.Cache.Redis.DialTimeout)
	assert.Equal(t, applied.Cache.Redis.ReadTimeout, loaded.Cache.Redis.ReadTimeout)
	assert.Equal(t, applied.Cache.Redis.WriteTimeout, loaded.Cache.Redis.WriteTimeout)
	assert.Equal(t, applied.Cache.Redis.MaxRetries, loaded.Cache.Redis.MaxRetries)
	assert.Equal(t, applied.Cache.Redis.MinRetryBackoff, loaded.Cache.Redis.MinRetryBackoff)
	assert.Equal(t, applied.Cache.Redis.MaxRetryBackoff, loaded.Cache.Redis.MaxRetryBackoff)
	assert.Equal(t, applied.Cache.Redis.PoolSize, loaded.Cache.Redis.PoolSize)
}
```

`loadDefaultConfig` builds a koanf instance, runs `loadDefaults`, unmarshals with `buildDecoderConfig()` — copy the unmarshal shape from `config.Load` (config/config.go:110-118). Check existing config_test.go for a reusable helper first (`grep -n 'loadDefaults' config/config_test.go`). Verify Redis field spellings against `RedisConfig` in config/types.go before writing.

- [ ] **Step 2: Run to verify current state**

Run: `go test ./config/ -run TestKoanfDefaultsMatchApplyDefaults -count=1`
Expected: PASS already if the constants line up (they should — redis renders from constants). This test is the *lock*, not a bug hunt; if it fails, a real drift exists — fix `loadDefaults` to render the constant.

- [ ] **Step 3: Replace the literals**

In `loadDefaults`: `"app.startup.timeout": "10s"` → `"app.startup.timeout": defaultStartupTimeout.String()`. Sweep for any other literal whose value duplicates an `apply*Defaults` constant (`grep -n '"[0-9]*[smh]"' config/config.go`) — scheduler `"30s"`/`"25s"` have no apply counterpart (koanf-only; consumers guard `> 0`), leave them. Trim the "keep the two in sync" comment at config/config.go:352-354 to: "Rendered from the same constants applyRedisDefaults uses; TestKoanfDefaultsMatchApplyDefaultsForSharedKeys pins the equality."

- [ ] **Step 4: Run the package**

Run: `go test ./config/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add config/config.go config/config_test.go
printf 'refactor(config): render shared defaults from one constant set, pin the equality\n' > /tmp/msg && git commit -F /tmp/msg && git log -1 --format=%s
```

### Task C3: Gates, push, follow-up issues

- [ ] **Step 1: Gates**

`pwd && make check` in background; `/simplify` → `/security-audit` → `/code-review`; `make mutate` in background (N > 0, zero LIVED — deletions change lines too).

- [ ] **Step 2: Push via `/gh-stack`** (base: PR B's branch); post `@coderabbitai review` (stacked PRs are skipped otherwise). PR body:

```markdown
## What
PR B made every construction path validate, which turned the bypass-era machinery into dead weight: the `app/managers.go` mirrored constants and mode fallbacks are deleted (only the deliberate multi-tenant zero→tenant-limit scaling remains, #661), and koanf defaults now render from the same constants the apply-phase uses, pinned by test.

## Impact
None — every deleted branch was unreachable on a validated config.

## Verification
`make mutate` on changed lines: all killed. Pinning test locks koanf/apply default equality for the shared key set.
```

- [ ] **Step 3: File the three follow-up issues**

```bash
T=$(GH_TOKEN=$(gh auth token -u gaborage) gh auth token -u gaborage)
for spec in presence loaddefaults twophase; do :; done  # bodies below, one gh call each with --body-file
```

Write each body to a scratch file, then `GH_TOKEN=$(gh auth token -u gaborage) gh issue create --repo gaborage/go-bricks --title "<title>" --label area/config --label needs-triage --body-file <file>` (check the label set first with `GH_TOKEN=$(gh auth token -u gaborage) gh label list --repo gaborage/go-bricks --search kind`; add the nearest `kind/*`):

1. `config: one presence module over koanf key-presence and decoded values` — `databaseIdentityKeys` mirrors `IsDatabaseConfigured` (pinned by `TestDatabaseIdentityKeysMatchPredicate`); presence is answered two ways with a documented blind spot for hand-built configs and dynamic tenant sources (config/validation.go:674-681). Deferred from the 2026-08-15 architecture review (needs a second adapter to justify the seam; ADR-047/051 mechanics).
2. `config: derive koanf loadDefaults from the normalize phase` — after ADR-064 both construction paths validate; deriving the koanf map from normalize(zero Config) would make defaults one mechanism, but changes hand-built semantics for koanf-only fields (`app.env`, `server.timeout.*`, `server.port` would default instead of erroring) — decide field by field. Review decision 9(a) chose the constants-table + pinning test instead; this is the deeper follow-up.
3. `config: split Validate into explicit normalize and check phases` — `Validate` mutates (defaults, inference, domain prefixing, map write-backs) and its section ordering is load-bearing but untested as a whole; a two-phase interface would make ordering checkable. Deferred: touches every section.

- [ ] **Step 4: Verify the stack**

`GH_TOKEN=$(gh auth token -u gaborage) gh pr list --repo gaborage/go-bricks --json number,title,baseRefName` — A targets `main`, B targets A, C targets B. Merging is bottom-up by the maintainer (admin-gated; never self-merge); after each merge, re-sync with `/gh-stack`.

---

## Self-review notes (kept for the executor)

- **Spec coverage:** decisions 1-15 → A1-A3 (2,3,4), A4 (6), A5 (11-amendment), B1 (5,12), B2 (11), C1 (8), C2 (9), C3 (15); decision 7/13 are follow-ups by design; decision 10 lands inside A3-Step 4; decision 14 is enforced by the Global Constraints.
- **Type consistency:** `dbSection`/`dbPlacement`/`dbStrictness`/`normalizeDatabaseValues`/`normalizeDatabaseSection`/`forEachDatabaseSection`/`UntypedDatabaseSections`/`validateNamedDatabaseName` — spelled identically in every task above.
- **Known wording deltas PR A may surface in tests** (fix the assertion, keep the shape): named-absent Action gains the path via concatenation (identical text today), tenant errors move from `tenant 'x' database` to `multitenant.tenants.x.database`, tenant manager rejection Category `missing` → `invalid`.
- Line numbers reference `main` at 931c856 + f8b0c78; re-grep before editing — every task names its anchor symbols for that purpose.
