package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestNormalizeDatabaseValuesStartupRejectsTypeContradictingScheme(t *testing.T) {
	cfg := DatabaseConfig{ConnectionString: "postgres://u:p@h:5432/d", Type: Oracle}
	before := cfg

	err := normalizeDatabaseValues(&cfg, rootDatabaseSection(), dbStrictnessStartup)

	assertValidationError(t, err, "conflicts with the connectionstring scheme")
	assert.Equal(t, before, cfg, "clone-commit: a rejected config must come back untouched")
}

func TestNormalizeDatabaseValuesConnectToleratesTypeContradictingScheme(t *testing.T) {
	cfg := DatabaseConfig{ConnectionString: "postgres://u:p@h:5432/d", Type: Oracle}

	require.NoError(t, normalizeDatabaseValues(&cfg, rootDatabaseSection(), dbStrictnessConnect))

	assert.Equal(t, Oracle, cfg.Type, "connect strictness keeps the explicit type; the dial reports the conflict")
	assert.Equal(t, defaultPoolMaxConnections, cfg.Pool.Max.Connections, "defaults are still applied")
}

func TestNormalizeDatabaseValuesConnectSkipsIdentityChecks(t *testing.T) {
	// A dynamic provider may return host/port/user only (PostgreSQL defaults the
	// database name to the user); startup would reject this, connect must not.
	cfg := DatabaseConfig{Type: PostgreSQL, Host: "h", Port: 5432, Username: "u"}

	require.NoError(t, normalizeDatabaseValues(&cfg, rootDatabaseSection(), dbStrictnessConnect))
	require.Error(t, normalizeDatabaseValues(&DatabaseConfig{Type: PostgreSQL, Host: "h", Port: 5432, Username: "u"}, rootDatabaseSection(), dbStrictnessStartup))
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
			assertValidationError(t, normalizeDatabaseValues(&tt.cfg, rootDatabaseSection(), dbStrictnessStartup), tt.want)
		})
	}
}

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
		{name: "named_absent_missing", section: namedDatabaseSection("r"), cfg: DatabaseConfig{}, wantErr: errDatabaseIncomplete, wantCategory: errCategoryMissing, wantField: "databases.r"},
		{
			name:         "tenant_absent_missing",
			section:      tenantDatabaseSection("t"),
			cfg:          DatabaseConfig{},
			wantErr:      errDatabaseIncomplete,
			wantCategory: errCategoryMissing,
			wantField:    "multitenant.tenants.t.database",
		},
		{name: "named_manager_rejected", section: namedDatabaseSection("r"), cfg: withManager(), wantErr: "only supported on the primary database", wantCategory: errCategoryInvalid, wantField: "databases.r.manager"},
		{
			name:         "tenant_manager_rejected",
			section:      tenantDatabaseSection("t"),
			cfg:          withManager(),
			wantErr:      "only supported on the primary database",
			wantCategory: errCategoryInvalid,
			wantField:    "multitenant.tenants.t.database.manager",
		},
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

// TestNormalizeDatabaseSectionQualifiesFieldWithSectionPath pins the addressing rule
// across every placement: a consumer matching on ConfigError.Field must be told WHICH
// section failed, not the root spelling of a key that belongs to another section.
func TestNormalizeDatabaseSectionQualifiesFieldWithSectionPath(t *testing.T) {
	missingIdentity := DatabaseConfig{Type: PostgreSQL, Host: "h", Username: "u"}
	typeConflict := DatabaseConfig{Type: Oracle, ConnectionString: "postgres://x/y"}
	tlsMaterial := DatabaseConfig{
		Type: PostgreSQL, Host: "h", Port: 5432, Database: "d", Username: "u",
		TLS: TLSConfig{CertFile: "c.pem"},
	}
	// The one field the database path names that is not key-shaped.
	oracleOutlier := DatabaseConfig{Type: Oracle, Host: "h", Port: 1521, Username: "u"}

	tests := []struct {
		name      string
		section   dbSection
		cfg       DatabaseConfig
		wantField string
	}{
		{name: "root_missing_identity", section: rootDatabaseSection(), cfg: missingIdentity, wantField: "database.port"},
		{name: "root_type_conflict", section: rootDatabaseSection(), cfg: typeConflict, wantField: "database.type"},
		{name: "root_tls_material", section: rootDatabaseSection(), cfg: tlsMaterial, wantField: "database.tls"},
		{name: "root_oracle_outlier", section: rootDatabaseSection(), cfg: oracleOutlier, wantField: "oracle connection identifier"},

		{name: "named_missing_identity", section: namedDatabaseSection("reporting"), cfg: missingIdentity, wantField: "databases.reporting.port"},
		{name: "named_type_conflict", section: namedDatabaseSection("reporting"), cfg: typeConflict, wantField: "databases.reporting.type"},
		{name: "named_tls_material", section: namedDatabaseSection("reporting"), cfg: tlsMaterial, wantField: "databases.reporting.tls"},
		{name: "named_oracle_outlier", section: namedDatabaseSection("reporting"), cfg: oracleOutlier, wantField: "databases.reporting.oracle connection identifier"},

		{name: "tenant_missing_identity", section: tenantDatabaseSection("acme"), cfg: missingIdentity, wantField: "multitenant.tenants.acme.database.port"},
		{name: "tenant_type_conflict", section: tenantDatabaseSection("acme"), cfg: typeConflict, wantField: "multitenant.tenants.acme.database.type"},
		{name: "tenant_tls_material", section: tenantDatabaseSection("acme"), cfg: tlsMaterial, wantField: "multitenant.tenants.acme.database.tls"},
		{name: "tenant_oracle_outlier", section: tenantDatabaseSection("acme"), cfg: oracleOutlier, wantField: "multitenant.tenants.acme.database.oracle connection identifier"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg

			err := normalizeDatabaseSection(&cfg, tt.section)

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tt.wantField, cfgErr.Field)
			if tt.section.placement != dbPlacementRoot {
				assert.False(t, strings.HasPrefix(err.Error(), tt.section.path+": "),
					"the path is carried by Field; a wrapper prefix would print it twice")
				// The error proper names the path once. An Action may name it a second
				// time on purpose — a hint has to say WHICH key to set, and a hint stuck
				// on the root spelling is what C60.19 fixes — so the hint's occurrences
				// are discounted rather than counted as duplication.
				rendered := strings.Count(err.Error(), tt.section.path)
				inAction := strings.Count(cfgErr.Action, tt.section.path)
				assert.Equal(t, 1, rendered-inAction,
					"and outside its hint it appears exactly once in the rendered error")
			}
		})
	}
}

// TestQualifiedFieldMatchesDeliveredEmptySpelling cross-checks the two producers instead of
// trusting that two hand-typed tables agree: ADR-076's whole point is that a key has ONE
// spelling per section, and the delivered-empty check (ADR-051) is the other producer of it.
func TestQualifiedFieldMatchesDeliveredEmptySpelling(t *testing.T) {
	sections := []dbSection{
		rootDatabaseSection(),
		namedDatabaseSection("reporting"),
		tenantDatabaseSection("acme"),
	}

	for _, section := range sections {
		t.Run(section.path, func(t *testing.T) {
			for _, key := range databaseIdentityKeys {
				// What validateNoDeliveredEmptyDatabase reports for this key and section.
				deliveredEmpty := section.path + "." + key

				// What a normalization error against the same key is re-addressed to.
				qualified := section.qualifyField(fieldDatabase + "." + key)

				assert.Equal(t, deliveredEmpty, qualified, "key %q must have one spelling", key)
			}
		})
	}
}

// TestRuntimeDoorSpellingMatchesStartupDoor is the C60.19 half of the check above: the two
// doors are reached by different callers and used to disagree, so comparing them against
// each other — rather than each against a literal — is what keeps them from drifting apart
// again. Every section is addressed identically whichever door raised the error.
func TestRuntimeDoorSpellingMatchesStartupDoor(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		section dbSection
	}{
		{name: "root", key: "", section: rootDatabaseSection()},
		{name: "named", key: NamedDatabasePrefix + "reporting", section: namedDatabaseSection("reporting")},
		{name: "tenant", key: "acme", section: tenantDatabaseSection("acme")},
	}

	// TLS material on PostgreSQL without a mode is rejected by BOTH strictnesses, so one
	// config exercises both doors and the comparison is like-for-like.
	invalid := DatabaseConfig{
		Type: PostgreSQL, Host: "h", Port: 5432, Database: "d", Username: "u",
		TLS: TLSConfig{CertFile: "c.pem"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startupCfg, runtimeCfg := invalid, invalid

			startupErr := normalizeDatabaseValues(&startupCfg, tt.section, dbStrictnessStartup)
			runtimeErr := ApplyDatabasePoolDefaultsForKey(&runtimeCfg, tt.key)

			var startupCfgErr, runtimeCfgErr *ConfigError
			require.ErrorAs(t, startupErr, &startupCfgErr)
			require.ErrorAs(t, runtimeErr, &runtimeCfgErr)
			assert.Equal(t, startupCfgErr.Field, runtimeCfgErr.Field,
				"the door a failure came through must not change how it is addressed")
		})
	}
}

// TestSectionForResourceKey pins the key vocabulary the runtime door translates. It is the
// manager's, unchanged — "" single-tenant, NamedDatabasePrefix for a named database, any
// other string a tenant id — and getting it wrong would address a real failure to a section
// that does not exist.
func TestSectionForResourceKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want dbSection
	}{
		{name: "empty_is_root", key: "", want: rootDatabaseSection()},
		{name: "named_prefix", key: NamedDatabasePrefix + "reporting", want: namedDatabaseSection("reporting")},
		{name: "bare_is_tenant", key: "acme", want: tenantDatabaseSection("acme")},
		// A tenant id that merely CONTAINS the prefix is not a named database.
		{name: "prefix_mid_string_is_tenant", key: "co-named:x", want: tenantDatabaseSection("co-named:x")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sectionForResourceKey(tt.key))
		})
	}
}

// TestApplyDatabasePoolDefaultsForKeyAddressesResourceKey drives the EXPORTED runtime door, the
// one DbManager and the migrate CLI call, so the key-to-section translation is pinned at the
// surface a consumer actually sees rather than only at the internal seam.
func TestApplyDatabasePoolDefaultsForKeyAddressesResourceKey(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		wantField string
	}{
		{name: "single_tenant", key: "", wantField: "database.tls"},
		{name: "named_database", key: NamedDatabasePrefix + "reporting", wantField: "databases.reporting.tls"},
		{name: "dynamic_tenant", key: "acme", wantField: "multitenant.tenants.acme.database.tls"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DatabaseConfig{
				Type: PostgreSQL, Host: "h", Port: 5432, Database: "d", Username: "u",
				TLS: TLSConfig{CertFile: "c.pem"},
			}

			err := ApplyDatabasePoolDefaultsForKey(&cfg, tt.key)

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tt.wantField, cfgErr.Field)
		})
	}
}

// TestApplyDatabasePoolDefaultsForKeyNilConfigAddressesItsSection covers the door's own guard,
// which reports before any normalization runs and so has its own qualify call.
func TestApplyDatabasePoolDefaultsForKeyNilConfigAddressesItsSection(t *testing.T) {
	err := ApplyDatabasePoolDefaultsForKey(nil, "acme")

	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "multitenant.tenants.acme.database", cfgErr.Field)
}

func TestNormalizeDatabaseSectionRootAbsentLeavesConfigUntouched(t *testing.T) {
	cfg := DatabaseConfig{}
	require.NoError(t, normalizeDatabaseSection(&cfg, rootDatabaseSection()))
	assert.Equal(t, DatabaseConfig{}, cfg, "absence must not pick up pool defaults — the verdict is identical before and after")
}

func TestForEachDatabaseSectionWalksRootNamedThenGatedTenantsInSortedOrder(t *testing.T) {
	cfg := &Config{
		Databases:   map[string]DatabaseConfig{"zeta": {}, "alpha": {}},
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
		Databases:   map[string]DatabaseConfig{"r": {}},
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

// TestQualifiedActionNamesAReachableEnvVar drives the hint through a real Load, because the
// property under test is a round trip through Load's OWN transform: a hint is only useful if
// the variable it names comes back to the key that failed. Before C60.19 every section was
// told to set DATABASE_PORT, and following that on a multitenant config writes a partial
// root block, which ADR-047 then rejects — the hint manufactured a second failure.
func TestQualifiedActionNamesAReachableEnvVar(t *testing.T) {
	const header = "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n"

	tests := []struct {
		name        string
		yaml        string
		wantField   string
		wantEnvVar  string // "" = the hint must name no variable at all
		wantYAMLKey string
	}{
		{
			name:        "named_section",
			yaml:        header + "databases:\n  reporting:\n    type: postgresql\n    host: h\n    username: u\n",
			wantField:   "databases.reporting.port",
			wantEnvVar:  "DATABASES_REPORTING_PORT",
			wantYAMLKey: "databases.reporting.port",
		},
		{
			name: "tenant_section",
			yaml: header + "multitenant:\n  enabled: true\n  resolver:\n    type: header\n  tenants:\n    acme:\n" +
				"      database:\n        type: postgresql\n        host: h\n        username: u\n",
			wantField:   "multitenant.tenants.acme.database.port",
			wantEnvVar:  "MULTITENANT_TENANTS_ACME_DATABASE_PORT",
			wantYAMLKey: "multitenant.tenants.acme.database.port",
		},
		{
			// The round-trip guard. DATABASES_REPORT_DB_PORT reaches
			// databases.report.db.port, a DIFFERENT key, so naming it would send an
			// operator to configure a section that does not exist. No hint beats a
			// wrong one; the YAML path still works and is still qualified.
			name:        "underscore_in_section_name_suppresses_the_env_half",
			yaml:        header + "databases:\n  report_db:\n    type: postgresql\n    host: h\n    username: u\n",
			wantField:   "databases.report_db.port",
			wantEnvVar:  "",
			wantYAMLKey: "databases.report_db.port",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadDeliveredEmptyFixture(t, tt.yaml, nil)

			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tt.wantField, cfgErr.Field)
			assert.Contains(t, cfgErr.Action, tt.wantYAMLKey, "the YAML half is always reachable")
			if tt.wantEnvVar == "" {
				assert.NotContains(t, cfgErr.Action, "env var",
					"a variable that lands on another key must not be suggested at all")
				return
			}
			assert.Contains(t, cfgErr.Action, tt.wantEnvVar)
			// Anchored on the leading "set ": the qualified variables END with
			// DATABASE_PORT, so a bare substring check passes for the wrong reason.
			assert.NotContains(t, cfgErr.Action, "set DATABASE_PORT env var",
				"the root spelling must not survive into a non-root section's hint")
		})
	}
}

// TestQualifiedActionEnvVarActuallyReachesTheKey closes the loop the test above only
// asserts about: it SETS the suggested variable and shows the failure goes away, which is
// the operator's actual experience and the only proof the hint is correct.
func TestQualifiedActionEnvVarActuallyReachesTheKey(t *testing.T) {
	yaml := "app:\n  name: a\n  version: v1\nserver:\n  port: 8080\n" +
		"databases:\n  reporting:\n    type: postgresql\n    host: h\n    username: u\n"

	_, err := loadDeliveredEmptyFixture(t, yaml, nil)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	require.Contains(t, cfgErr.Action, "DATABASES_REPORTING_PORT")

	cfg, err := loadDeliveredEmptyFixture(t, yaml, map[string]string{
		"DATABASES_REPORTING_PORT":     "5432",
		"DATABASES_REPORTING_DATABASE": "d",
	})

	require.NoError(t, err, "following the hint must resolve the failure, not move it")
	assert.Equal(t, 5432, cfg.Databases["reporting"].Port)
}
