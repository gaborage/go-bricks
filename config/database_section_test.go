package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/observability"
)

func TestDatabaseSectionConstructorsNamePathAndPlacement(t *testing.T) {
	tests := []struct {
		name          string
		section       section
		wantPath      string
		wantPlacement placement
	}{
		{name: "root", section: rootDatabaseSection(), wantPath: "database", wantPlacement: placementRoot},
		{name: "named", section: namedDatabaseSection("reporting"), wantPath: "databases.reporting", wantPlacement: placementNamed},
		{name: "tenant", section: tenantDatabaseSection("acme"), wantPath: "multitenant.tenants.acme.database", wantPlacement: placementTenant},
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
		section      section
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
		section   section
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
			if tt.section.placement != placementRoot {
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
	sections := []section{
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
		section section
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
		want section
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
	require.NoError(t, forEachDatabaseSection(cfg, func(s section, _ *DatabaseConfig) error {
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
	require.NoError(t, forEachDatabaseSection(cfg, func(s section, _ *DatabaseConfig) error {
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
	require.NoError(t, forEachDatabaseSection(cfg, func(_ section, db *DatabaseConfig) error {
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
	err := forEachDatabaseSection(cfg, func(s section, _ *DatabaseConfig) error {
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

func TestValidateDatabaseSuccess(t *testing.T) {
	tests := []struct {
		name string
		cfg  DatabaseConfig
	}{
		{
			name: "postgresql_config",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
		},
		{
			name: "oracle_config",
			cfg: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Database: "XE",
				Username: "oracleuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 50,
					},
				},
			},
		},
		{
			name: "minimum_values",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "db",
				Port:     1,
				Database: "d",
				Username: "u",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 1,
					},
				},
			},
		},
		{
			name: "zero_max_conns_gets_default",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 0, // Should get set to default (25)
					},
				},
			},
		},
		{
			name: "maximum_port",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     65535,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 100,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeDatabaseSection(&tt.cfg, rootDatabaseSection())
			assert.NoError(t, err)
		})
	}
}

func TestValidateDatabaseFailures(t *testing.T) {
	tests := []struct {
		name          string
		cfg           DatabaseConfig
		expectedError string
	}{
		{
			name: "invalid_type",
			cfg: DatabaseConfig{
				Type:     "mysql",
				Host:     "localhost",
				Port:     3306,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectedError: databaseType,
		},
		{
			name: "empty_host",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectedError: "database.host",
		},
		{
			name: "zero_port",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     0,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectedError: databasePort,
		},
		{
			name: "negative_port",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     -1,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectedError: databasePort,
		},
		{
			name: "port_too_high",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     65536,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectedError: databasePort,
		},
		{
			name: "empty_database",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectedError: "database.database",
		},
		{
			name: "empty_username",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectedError: "database.username",
		},
		{
			name: "negative_max_conns",
			cfg: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: -1,
					},
				},
			},
			expectedError: errMaxConnectionsNonNegative,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeDatabaseSection(&tt.cfg, rootDatabaseSection())
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

func TestIsDatabaseConfigured(t *testing.T) {
	tests := []struct {
		name     string
		config   DatabaseConfig
		expected bool
	}{
		{
			name:     "empty_config_not_configured",
			config:   DatabaseConfig{},
			expected: false,
		},
		{
			name: "host_only_is_configured",
			config: DatabaseConfig{
				Host: "localhost",
			},
			expected: true,
		},
		{
			name: "type_only_is_configured",
			config: DatabaseConfig{
				Type: "postgresql",
			},
			expected: true,
		},
		{
			name: "both_host_and_type_configured",
			config: DatabaseConfig{
				Host: "localhost",
				Type: "postgresql",
			},
			expected: true,
		},
		{
			name: "connection_string_is_configured",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
			},
			expected: true,
		},
		// A sole identity field is intent, not absence. Each of these was "not
		// configured" before ADR-047, which let a half-delivered secret load silently
		// and then fail at first query instead of at startup.
		{
			name:     "port_only_is_configured",
			config:   DatabaseConfig{Port: 5432},
			expected: true,
		},
		{
			name:     "database_only_is_configured",
			config:   DatabaseConfig{Database: "appdb"},
			expected: true,
		},
		{
			name:     "username_only_is_configured",
			config:   DatabaseConfig{Username: "app"},
			expected: true,
		},
		{
			name:     "password_only_is_configured",
			config:   DatabaseConfig{Password: "s3cret"},
			expected: true,
		},
		// Oracle identifies its target by service name or SID rather than a database
		// name, so a split config delivering only those is still intent.
		{
			name:     "oracle_service_name_only_is_configured",
			config:   DatabaseConfig{Oracle: OracleConfig{Service: ServiceConfig{Name: "ORCLPDB1"}}},
			expected: true,
		},
		{
			name:     "oracle_sid_only_is_configured",
			config:   DatabaseConfig{Oracle: OracleConfig{Service: ServiceConfig{SID: "ORCL"}}},
			expected: true,
		},
		// Pool/query/timezone defaults must NOT read as intent: applyDatabasePoolDefaults
		// fills them on every config, so counting them would make every service look
		// database-configured.
		{
			name:     "defaulted_fields_alone_are_not_configured",
			config:   DatabaseConfig{Timezone: "UTC", Pool: PoolConfig{Max: PoolMaxConfig{Connections: 25}}},
			expected: false,
		},
		{
			name: "connection_string_with_empty_host_type",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Host:             "",
				Type:             "",
			},
			expected: true,
		},
		{
			name: "whitespace_host_not_configured",
			config: DatabaseConfig{
				Host: "   ",
			},
			expected: true, // Whitespace is still considered configured
		},
		{
			name: "whitespace_type_not_configured",
			config: DatabaseConfig{
				Type: "   ",
			},
			expected: true, // Whitespace is still considered configured
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsDatabaseConfigured(&tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateDatabaseConditionalBehavior(t *testing.T) {
	tests := []struct {
		name          string
		config        DatabaseConfig
		expectError   bool
		errorContains string
	}{
		{
			name:        "empty_config_passes_validation",
			config:      DatabaseConfig{},
			expectError: false,
		},
		{
			name: "host_only_fails_validation",
			config: DatabaseConfig{
				Host: "localhost",
				// Missing required fields
			},
			expectError:   true,
			errorContains: databaseType,
		},
		{
			name: "type_only_fails_validation",
			config: DatabaseConfig{
				Type: "postgresql",
				// Missing required fields
			},
			expectError:   true,
			errorContains: "database.host",
		},
		{
			name: "partial_config_missing_database_name",
			config: DatabaseConfig{
				Type: "postgresql",
				Host: "localhost",
				Port: 5432,
				// Missing Database, Username, MaxConns
			},
			expectError:   true,
			errorContains: "database.database",
		},
		{
			name: "partial_config_missing_username",
			config: DatabaseConfig{
				Type:     "postgresql",
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				// Missing Username, MaxConns
			},
			expectError:   true,
			errorContains: "database.username",
		},
		{
			name: "partial_config_zero_max_conns_gets_default",
			config: DatabaseConfig{
				Type:     "postgresql",
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 0, // Should get set to default (25)
					},
				},
			},
			expectError: false, // Now passes with default
		},
		{
			name: "valid_postgresql_config_passes",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectError: false,
		},
		{
			name: "valid_oracle_config_passes",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Database: "XE",
				Username: "oracleuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 50,
					},
				},
			},
			expectError: false,
		},
		{
			name: "connection_string_minimal_config_passes",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectError: false,
		},
		{
			name: "connection_string_invalid_port_uses_optional_validation",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Port:             70000,
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectError:   true,
			errorContains: databasePort,
		},
		{
			name: "connection_string_with_invalid_type",
			config: DatabaseConfig{
				// Unrecognized scheme on purpose: a postgres:// DSN would be
				// intercepted by ADR-050's conflict branch, leaving the
				// validateDatabaseType call on this path with no failing test.
				ConnectionString: testUnknownSchemeConnString,
				Type:             "invalid",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectError:   true,
			errorContains: "'invalid' is not supported",
		},
		{
			name: "connection_string_missing_max_conns_applies_default",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 0,
					},
				},
			},
			expectError: false, // Should apply default of 25
		},
		{
			name: "invalid_database_type",
			config: DatabaseConfig{
				Type:     "mysql",
				Host:     "localhost",
				Port:     3306,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectError:   true,
			errorContains: databaseType,
		},
		{
			name: "invalid_port_range",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     70000, // Invalid port
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
			},
			expectError:   true,
			errorContains: databasePort,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeDatabaseSection(&tt.config, rootDatabaseSection())
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateDatabaseDisabledConfig(t *testing.T) {
	cfg := &Config{
		App: AppConfig{
			Name:    testAppName,
			Version: testAppVersion,
			Env:     EnvDevelopment,
			Rate:    RateConfig{Limit: 100},
		},
		Server: ServerConfig{
			Port: 8080,
			Timeout: TimeoutConfig{
				Read:       15 * time.Second,
				Write:      30 * time.Second,
				Middleware: 5 * time.Second,
				Shutdown:   10 * time.Second,
			},
		},
		Database: DatabaseConfig{
			// Empty database config - should skip validation
		},
		Log: LogConfig{
			Level: "info",
		},
	}

	err := Validate(cfg)
	assert.NoError(t, err, "Validation should pass with empty database config")
}

func TestValidateDatabaseWithConnectionStringEdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		config        DatabaseConfig
		expectError   bool
		errorContains string
	}{
		{
			name: "connection_string_with_negative_max_query_length",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Log: QueryLogConfig{
						MaxLength: -1,
					},
				},
			},
			expectError:   true,
			errorContains: "database.query.log.maxlength must be non-negative",
		},
		{
			name: "connection_string_with_zero_max_query_length_applies_default",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Log: QueryLogConfig{
						MaxLength: 0,
					},
				},
			},
			expectError: false,
		},
		{
			name: "connection_string_with_negative_slow_query_threshold",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Slow: SlowQueryConfig{
						Threshold: -1 * time.Millisecond,
					},
				},
			},
			expectError:   true,
			errorContains: "database.query.slow.threshold must be non-negative",
		},
		{
			name: "connection_string_with_zero_slow_query_threshold_applies_default",
			config: DatabaseConfig{
				ConnectionString: testConnectionString,
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Slow: SlowQueryConfig{
						Threshold: 0,
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeDatabaseSection(&tt.config, rootDatabaseSection())
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
				// Verify defaults were applied
				if tt.config.Query.Log.MaxLength == 0 {
					assert.Equal(t, defaultMaxQueryLength, tt.config.Query.Log.MaxLength)
				}
				if tt.config.Query.Slow.Threshold == 0 {
					assert.Equal(t, defaultSlowQueryThreshold, tt.config.Query.Slow.Threshold)
				}
			}
		})
	}
}

// dbTypeInferenceCase is one scheme-classification fixture. Both inference sites
// — config.Validate and the ApplyDatabasePoolDefaults seam — share the slice so
// they cannot drift apart on which schemes classify; their single deliberate
// divergence (an explicit Type contradicting the scheme: an error in Validate,
// left alone on the seam) is pinned by each site's own test instead.
type dbTypeInferenceCase struct {
	name         string
	config       DatabaseConfig
	expectedType string
}

// databaseTypeInferenceCases returns a fresh slice per call. Both callers already
// copy twice before mutating (range copies the element, then cfg := tt.config), so
// this is belt-and-braces against a future caller that takes a pointer straight
// into the slice and leaks one site's mutations into the other's fixtures.
func databaseTypeInferenceCases() []dbTypeInferenceCase {
	return []dbTypeInferenceCase{
		{
			name:         "postgres_scheme_infers_type",
			config:       DatabaseConfig{ConnectionString: testBarePostgresConnString},
			expectedType: PostgreSQL,
		},
		{
			name:         "postgresql_scheme_infers_type",
			config:       DatabaseConfig{ConnectionString: testConnectionString},
			expectedType: PostgreSQL,
		},
		{
			name:         "oracle_scheme_infers_type",
			config:       DatabaseConfig{ConnectionString: testOracleConnectionString},
			expectedType: Oracle,
		},
		{
			name:         "scheme_case_insensitive",
			config:       DatabaseConfig{ConnectionString: "POSTGRES://user:pass@localhost:5432/db"},
			expectedType: PostgreSQL,
		},
		{
			name:         "leading_space_infers_type",
			config:       DatabaseConfig{ConnectionString: " " + testBarePostgresConnString},
			expectedType: PostgreSQL,
		},
		{
			name:         "trailing_newline_infers_type",
			config:       DatabaseConfig{ConnectionString: testOracleConnectionString + "\n"},
			expectedType: Oracle,
		},
		{
			name:         "surrounding_tab_and_crlf_infers_type",
			config:       DatabaseConfig{ConnectionString: "\t" + testConnectionString + "\r\n"},
			expectedType: PostgreSQL,
		},
		{
			name:         "unknown_scheme_keeps_empty_type",
			config:       DatabaseConfig{ConnectionString: testUnknownSchemeConnString},
			expectedType: "",
		},
		{
			name:         "no_connection_string_keeps_empty_type",
			config:       DatabaseConfig{},
			expectedType: "",
		},
		{
			name: "explicit_matching_type_untouched",
			config: DatabaseConfig{
				Type:             PostgreSQL,
				ConnectionString: testBarePostgresConnString,
			},
			expectedType: PostgreSQL,
		},
	}
}

func TestValidateInfersDatabaseTypeFromConnectionString(t *testing.T) {
	for _, tt := range databaseTypeInferenceCases() {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.config
			dsn := cfg.ConnectionString

			require.NoError(t, normalizeDatabaseSection(&cfg, rootDatabaseSection()))

			assert.Equal(t, tt.expectedType, cfg.Type)
			assert.Equal(t, dsn, cfg.ConnectionString, "classification tolerates whitespace; the stored DSN stays byte-exact")
		})
	}

	// The deliberate divergence from the ApplyDatabasePoolDefaults seam: Validate
	// rejects an explicit Type contradicting the scheme (ADR-050 item 1).
	t.Run("explicit_type_conflicting_with_scheme_fails", func(t *testing.T) {
		cfg := DatabaseConfig{Type: Oracle, ConnectionString: testBarePostgresConnString}

		err := normalizeDatabaseSection(&cfg, rootDatabaseSection())

		assertValidationError(t, err, "conflicts with the connectionstring scheme")
	})
}

func TestValidateNamedDatabaseInfersTypeFromConnectionString(t *testing.T) {
	databases := map[string]DatabaseConfig{
		"reporting": {ConnectionString: testConnectionString},
	}

	err := normalizeNamedDatabases(databases)

	require.NoError(t, err)
	assert.Equal(t, PostgreSQL, databases["reporting"].Type)
}

func assertValidationSuccess(t *testing.T, err error, config *DatabaseConfig) {
	assert.NoError(t, err)
	// Verify defaults were applied
	if config.Pool.Max.Connections == 0 {
		assert.Equal(t, int32(25), config.Pool.Max.Connections)
	}
	if config.Query.Log.MaxLength == 0 {
		assert.Equal(t, defaultMaxQueryLength, config.Query.Log.MaxLength)
	}
	if config.Query.Slow.Threshold == 0 {
		assert.Equal(t, defaultSlowQueryThreshold, config.Query.Slow.Threshold)
	}
}

func TestApplyDatabasePoolDefaultsEdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		config        DatabaseConfig
		expectError   bool
		errorContains string
	}{
		{
			name: "negative_max_conns_error",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: -1,
					},
				},
			},
			expectError:   true,
			errorContains: errMaxConnectionsNonNegative,
		},
		{
			name: "zero_max_conns_applies_default",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 0,
					},
				},
			},
			expectError: false,
		},
		{
			name: "negative_max_query_length_error",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Log: QueryLogConfig{
						MaxLength: -1,
					},
				},
			},
			expectError:   true,
			errorContains: "database.query.log.maxlength must be non-negative",
		},
		{
			name: "zero_max_query_length_applies_default",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Log: QueryLogConfig{
						MaxLength: 0,
					},
				},
			},
			expectError: false,
		},
		{
			name: "negative_slow_query_threshold_error",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Slow: SlowQueryConfig{
						Threshold: -1 * time.Millisecond,
					},
				},
			},
			expectError:   true,
			errorContains: "database.query.slow.threshold must be non-negative",
		},
		{
			name: "zero_slow_query_threshold_applies_default",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{
						Connections: 25,
					},
				},
				Query: QueryConfig{
					Slow: SlowQueryConfig{
						Threshold: 0,
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeDatabaseSection(&tt.config, rootDatabaseSection())
			if tt.expectError {
				assertValidationError(t, err, tt.errorContains)
			} else {
				assertValidationSuccess(t, err, &tt.config)
			}
		})
	}
}

func TestApplyDatabasePoolDefaultsKeepAlive(t *testing.T) {
	tests := []struct {
		name             string
		config           DatabaseConfig
		expectedEnabled  bool
		expectedInterval time.Duration
	}{
		{
			name: "absent_key_applies_defaults",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max:       PoolMaxConfig{Connections: 25},
					KeepAlive: PoolKeepAliveConfig{}, // Enabled nil (absent), Interval 0
				},
			},
			expectedEnabled:  defaultKeepAliveEnabled,
			expectedInterval: defaultKeepAliveInterval,
		},
		{
			name: "explicit_disabled_with_zero_interval_honors_disable",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{Connections: 25},
					KeepAlive: PoolKeepAliveConfig{
						Enabled:  observability.BoolPtr(false), // Explicitly disabled
						Interval: 0,                            // Left at default (unset in YAML)
					},
				},
			},
			// M5 fix: an explicit enabled=false is honored regardless of Interval.
			// Interval is defaulted independently and never flips Enabled.
			expectedEnabled:  false,
			expectedInterval: defaultKeepAliveInterval,
		},
		{
			name: "explicit_interval_preserves_values",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{Connections: 25},
					KeepAlive: PoolKeepAliveConfig{
						Enabled:  observability.BoolPtr(true),
						Interval: 30 * time.Second,
					},
				},
			},
			expectedEnabled:  true,
			expectedInterval: 30 * time.Second,
		},
		{
			name: "explicit_disabled_with_interval_preserved",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{Connections: 25},
					KeepAlive: PoolKeepAliveConfig{
						Enabled:  observability.BoolPtr(false),
						Interval: 120 * time.Second,
					},
				},
			},
			expectedEnabled:  false,
			expectedInterval: 120 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeDatabaseSection(&tt.config, rootDatabaseSection())
			assert.NoError(t, err)
			require.NotNil(t, tt.config.Pool.KeepAlive.Enabled,
				"KeepAlive.Enabled must be non-nil after defaulting")
			assert.Equal(t, tt.expectedEnabled, *tt.config.Pool.KeepAlive.Enabled,
				"KeepAlive.Enabled mismatch")
			assert.Equal(t, tt.expectedInterval, tt.config.Pool.KeepAlive.Interval,
				"KeepAlive.Interval mismatch")
		})
	}
}

// TestApplyDatabasePoolDefaultsKeepAliveExplicitDisableHonored reproduces M5:
// an operator who explicitly sets database.pool.keepalive.enabled=false while
// leaving interval at its zero default must have that opt-out honored. Interval
// is still defaulted to 60s (independently), but the zero interval must not flip
// Enabled back to true.
func TestApplyDatabasePoolDefaultsKeepAliveExplicitDisableHonored(t *testing.T) {
	cfg := DatabaseConfig{
		Type:     PostgreSQL,
		Host:     "localhost",
		Port:     5432,
		Database: "testdb",
		Username: "testuser",
		Pool: PoolConfig{
			Max: PoolMaxConfig{Connections: 25},
			KeepAlive: PoolKeepAliveConfig{
				Enabled:  observability.BoolPtr(false), // Operator explicitly disables keep-alive.
				Interval: 0,                            // Left at default (unset in YAML).
			},
		},
	}

	err := normalizeDatabaseSection(&cfg, rootDatabaseSection())
	require.NoError(t, err)

	require.NotNil(t, cfg.Pool.KeepAlive.Enabled, "explicit enabled should remain set")
	assert.False(t, *cfg.Pool.KeepAlive.Enabled,
		"explicit enabled=false must survive defaulting regardless of interval")
	assert.Equal(t, defaultKeepAliveInterval, cfg.Pool.KeepAlive.Interval,
		"zero interval should be defaulted independently of enabled")
}

func TestApplyDatabasePoolDefaultsKeepAliveNegativeIntervalRejected(t *testing.T) {
	cfg := DatabaseConfig{
		Type:     PostgreSQL,
		Host:     "localhost",
		Port:     5432,
		Database: "testdb",
		Username: "testuser",
		Pool: PoolConfig{
			Max: PoolMaxConfig{Connections: 25},
			KeepAlive: PoolKeepAliveConfig{
				Enabled:  observability.BoolPtr(true),
				Interval: -5 * time.Second, // invalid: cannot be negative
			},
		},
	}

	err := normalizeDatabaseSection(&cfg, rootDatabaseSection())
	require.Error(t, err, "a negative keep-alive interval must be rejected, matching the other pool duration fields")
	assert.Contains(t, err.Error(), "database.pool.keepalive.interval",
		"error should identify the offending key")
}

func TestApplyDatabasePoolDefaultsIdleAndLifetime(t *testing.T) {
	tests := []struct {
		name                    string
		config                  DatabaseConfig
		expectedIdleTime        time.Duration
		expectedLifetimeMax     time.Duration
		expectedIdleConnections int32
	}{
		{
			name: "zero_values_apply_all_defaults",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool:     PoolConfig{}, // All zero values
			},
			expectedIdleTime:        defaultPoolIdleTime,
			expectedLifetimeMax:     defaultPoolLifetimeMax,
			expectedIdleConnections: defaultPoolMaxConnections,
		},
		{
			name: "explicit_values_preserved",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max:      PoolMaxConfig{Connections: 50},
					Idle:     PoolIdleConfig{Connections: 5, Time: 10 * time.Minute},
					Lifetime: LifetimeConfig{Max: 1 * time.Hour},
				},
			},
			expectedIdleTime:        10 * time.Minute,
			expectedLifetimeMax:     1 * time.Hour,
			expectedIdleConnections: 5,
		},
		{
			name: "partial_config_applies_missing_defaults",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max:  PoolMaxConfig{Connections: 25},
					Idle: PoolIdleConfig{Time: 3 * time.Minute}, // Only idle time set
				},
			},
			expectedIdleTime:        3 * time.Minute,
			expectedLifetimeMax:     defaultPoolLifetimeMax,    // Default applied
			expectedIdleConnections: defaultPoolMaxConnections, // Default applied
		},
		{
			name: "only_idle_connections_set",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Idle: PoolIdleConfig{Connections: 10},
				},
			},
			expectedIdleTime:        defaultPoolIdleTime,    // Default applied
			expectedLifetimeMax:     defaultPoolLifetimeMax, // Default applied
			expectedIdleConnections: 10,                     // Explicit value preserved
		},
		{
			name: "only_lifetime_set",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Lifetime: LifetimeConfig{Max: 15 * time.Minute},
				},
			},
			expectedIdleTime:        defaultPoolIdleTime,       // Default applied
			expectedLifetimeMax:     15 * time.Minute,          // Explicit value preserved
			expectedIdleConnections: defaultPoolMaxConnections, // Default applied
		},
		{
			name: "idle_tracks_custom_max",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max: PoolMaxConfig{Connections: 10}, // Custom max, idle left unset
				},
			},
			expectedIdleTime:        defaultPoolIdleTime,
			expectedLifetimeMax:     defaultPoolLifetimeMax,
			expectedIdleConnections: 10, // Idle defaults to track the configured max, never the old fixed 2
		},
		{
			name: "explicit_idle_above_max_clamped",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max:  PoolMaxConfig{Connections: 10},
					Idle: PoolIdleConfig{Connections: 50}, // Explicit idle above max
				},
			},
			expectedIdleTime:        defaultPoolIdleTime,
			expectedLifetimeMax:     defaultPoolLifetimeMax,
			expectedIdleConnections: 10, // Clamped to max — database/sql caps idle at max-open anyway
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeDatabaseSection(&tt.config, rootDatabaseSection())
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedIdleTime, tt.config.Pool.Idle.Time,
				"Pool.Idle.Time mismatch")
			assert.Equal(t, tt.expectedLifetimeMax, tt.config.Pool.Lifetime.Max,
				"Pool.Lifetime.Max mismatch")
			assert.Equal(t, tt.expectedIdleConnections, tt.config.Pool.Idle.Connections,
				"Pool.Idle.Connections mismatch")
		})
	}
}

func TestApplyDatabasePoolDefaultsNegativeValues(t *testing.T) {
	tests := []struct {
		name          string
		config        DatabaseConfig
		errorContains string
	}{
		{
			name: "negative_idle_time_rejected",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max:  PoolMaxConfig{Connections: 25},
					Idle: PoolIdleConfig{Time: -1 * time.Minute},
				},
			},
			errorContains: "database.pool.idle.time",
		},
		{
			name: "negative_lifetime_rejected",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max:      PoolMaxConfig{Connections: 25},
					Lifetime: LifetimeConfig{Max: -1 * time.Hour},
				},
			},
			errorContains: "database.pool.lifetime.max",
		},
		{
			name: "negative_idle_connections_rejected",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Pool: PoolConfig{
					Max:  PoolMaxConfig{Connections: 25},
					Idle: PoolIdleConfig{Connections: -1},
				},
			},
			errorContains: "database.pool.idle.connections",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeDatabaseSection(&tt.config, rootDatabaseSection())
			assertValidationError(t, err, tt.errorContains)
		})
	}
}

func TestApplyDatabaseTimezoneDefault(t *testing.T) {
	tests := []struct {
		name             string
		input            string
		expectedTimezone string
	}{
		{name: "empty_defaults_to_utc", input: "", expectedTimezone: "UTC"},
		{name: "explicit_utc_preserved", input: "UTC", expectedTimezone: "UTC"},
		{name: "iana_name_preserved", input: "America/New_York", expectedTimezone: "America/New_York"},
		{name: "asia_iana_preserved", input: "Asia/Tokyo", expectedTimezone: "Asia/Tokyo"},
		{name: "europe_iana_preserved", input: "Europe/London", expectedTimezone: "Europe/London"},
		{name: "dash_sentinel_preserved", input: "-", expectedTimezone: "-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Timezone: tt.input,
			}
			err := normalizeDatabaseSection(cfg, rootDatabaseSection())
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedTimezone, cfg.Timezone)
		})
	}
}

func TestApplyDatabaseTimezoneRejectsInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "unknown_iana_name", input: "Not/AZone"},
		{name: "garbage_string", input: "xyz"},
		{name: "numeric_offset_not_iana", input: "+05:30"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Timezone: tt.input,
			}
			err := normalizeDatabaseSection(cfg, rootDatabaseSection())
			assertValidationError(t, err, "database.timezone")
		})
	}
}

func TestApplyDatabaseTimezoneAppliesViaConnectionString(t *testing.T) {
	// Connection-string path goes through normalizeWithConnectionString (via
	// normalizeDatabaseSection), which must also default Timezone to UTC and
	// validate it.
	cfg := &DatabaseConfig{
		ConnectionString: "host=localhost port=5432 dbname=testdb user=testuser",
	}
	err := normalizeDatabaseSection(cfg, rootDatabaseSection())
	assert.NoError(t, err)
	assert.Equal(t, "UTC", cfg.Timezone)
}

func TestApplyDatabaseTimezoneInheritsToNamedDatabases(t *testing.T) {
	// Exercises real propagation through the top-level Validate(*Config) wiring,
	// not the standalone normalizeDatabaseSection call in isolation. Split into
	// two scenarios because the framework forbids a root Database alongside
	// static tenants. Either scenario regressing (e.g. normalizeNamedDatabases or
	// normalizeMultitenantTenants dropping their normalizeDatabaseSection call)
	// would fail this test.
	t.Run("root_explicit_named_defaults", func(t *testing.T) {
		cfg := createValidFullConfig()
		cfg.Database.Timezone = "America/New_York"
		cfg.Databases = map[string]DatabaseConfig{
			"legacy": {
				Type:     Oracle,
				Host:     "legacy.db",
				Port:     1521,
				Username: "legacy",
				Oracle:   OracleConfig{Service: ServiceConfig{Name: "LEGACY"}},
				// Timezone unset — Validate must default it to UTC.
			},
		}

		require.NoError(t, Validate(cfg))

		assert.Equal(t, "America/New_York", cfg.Database.Timezone, "root timezone explicitly set must be preserved")
		assert.Equal(t, "UTC", cfg.Databases["legacy"].Timezone, "named DB without explicit timezone must default to UTC via Validate wiring")
	})

	t.Run("tenant_database_defaults", func(t *testing.T) {
		// Multitenant mode forbids a root Database, so build from the helpers
		// without one and add static tenants.
		cfg := &Config{
			App:    createValidAppConfig(),
			Server: createValidServerConfig(),
			Log:    createValidLogConfig(),
			Multitenant: MultitenantConfig{
				Enabled: true,
				Resolver: ResolverConfig{
					Type:   "header",
					Header: "X-Tenant-ID",
				},
				Tenants: map[string]TenantEntry{
					"acme": {
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     "acme.db",
							Port:     5432,
							Database: "acme",
							Username: "acme_user",
							// Timezone unset — Validate must default it to UTC.
						},
					},
				},
			},
			Source: SourceConfig{Type: SourceTypeStatic},
		}

		require.NoError(t, Validate(cfg))

		assert.Equal(t, "UTC", cfg.Multitenant.Tenants["acme"].Database.Timezone,
			"tenant DB without explicit timezone must default to UTC via Validate wiring")
	})
}

func TestValidateOracleFields(t *testing.T) {
	tests := []struct {
		name          string
		config        DatabaseConfig
		expectError   bool
		errorContains string
	}{
		{
			name: "valid Oracle config with service name",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Username: "oracleuser",
				Oracle: OracleConfig{
					Service: ServiceConfig{
						Name: "XEPDB1",
					},
				},
			},
			expectError: false,
		},
		{
			name: "valid Oracle config with SID",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Username: "oracleuser",
				Oracle: OracleConfig{
					Service: ServiceConfig{
						SID: "XE",
					},
				},
			},
			expectError: false,
		},
		{
			name: "valid Oracle config with database name",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Database: "XE",
				Username: "oracleuser",
			},
			expectError: false,
		},
		{
			name: "Oracle config with no connection identifier",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Username: "oracleuser",
				// No Service.Name, SID, or Database — and no connection string to supply one.
			},
			expectError:   true,
			errorContains: oracleConnectionIdentifier + " exactly one required",
		},
		{
			name: "Oracle config with service name and SID",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Username: "oracleuser",
				Oracle: OracleConfig{
					Service: ServiceConfig{
						Name: "XEPDB1",
						SID:  "XE",
					},
				},
			},
			expectError:   true,
			errorContains: oracleConnectionIdentifier,
		},
		{
			name: "Oracle config with service name and database name",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Database: "XE",
				Username: "oracleuser",
				Oracle: OracleConfig{
					Service: ServiceConfig{
						Name: "XEPDB1",
					},
				},
			},
			expectError:   true,
			errorContains: oracleConnectionIdentifier,
		},
		{
			name: "Oracle config with SID and database name",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Database: "XE",
				Username: "oracleuser",
				Oracle: OracleConfig{
					Service: ServiceConfig{
						SID: "XE",
					},
				},
			},
			expectError:   true,
			errorContains: oracleConnectionIdentifier,
		},
		{
			name: "Oracle config with all three connection identifiers",
			config: DatabaseConfig{
				Type:     Oracle,
				Host:     testOracleHost,
				Port:     1521,
				Database: "XE",
				Username: "oracleuser",
				Oracle: OracleConfig{
					Service: ServiceConfig{
						Name: "XEPDB1",
						SID:  "XE",
					},
				},
			},
			expectError:   true,
			errorContains: oracleConnectionIdentifier,
		},
		{
			name: "non-Oracle type should not validate Oracle fields",
			config: DatabaseConfig{
				Type:     PostgreSQL,
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				Username: "testuser",
				Oracle: OracleConfig{
					Service: ServiceConfig{
						Name: "XEPDB1", // This should be ignored for PostgreSQL
						SID:  "XE",     // This should be ignored for PostgreSQL
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeDatabaseSection(&tt.config, rootDatabaseSection())
			if tt.expectError {
				assertValidationError(t, err, tt.errorContains)
			} else {
				assertValidationSuccess(t, err, &tt.config)
			}
		})
	}
}

func TestValidateOracleWithConnectionString(t *testing.T) {
	tests := []struct {
		name          string
		config        DatabaseConfig
		expectError   bool
		errorContains string
	}{
		{
			name: "valid Oracle with connection string and valid service name",
			config: DatabaseConfig{
				Type:             Oracle,
				ConnectionString: testOracleConnectionString,
				Oracle: OracleConfig{
					Service: ServiceConfig{
						Name: "XEPDB1",
					},
				},
			},
			expectError: false,
		},
		{
			name: "Oracle with connection string but multiple identifiers",
			config: DatabaseConfig{
				Type:             Oracle,
				ConnectionString: testOracleConnectionString,
				Oracle: OracleConfig{
					Service: ServiceConfig{
						Name: "XEPDB1",
						SID:  "XE",
					},
				},
			},
			expectError:   true,
			errorContains: oracleConnectionIdentifier,
		},
		{
			name: "Oracle with connection string and no identifiers",
			config: DatabaseConfig{
				Type:             Oracle,
				ConnectionString: testOracleConnectionString,
				// The DSN carries the identifier; buildOracleDSN ignores these fields.
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeDatabaseSection(&tt.config, rootDatabaseSection())
			if tt.expectError {
				assertValidationError(t, err, tt.errorContains)
			} else {
				assertValidationSuccess(t, err, &tt.config)
			}
		})
	}
}

func TestValidateOracleConnectionStringNeedsNoIdentifier(t *testing.T) {
	cfg := DatabaseConfig{ConnectionString: testOracleConnectionString}

	require.NoError(t, normalizeDatabaseSection(&cfg, rootDatabaseSection()))

	assert.Equal(t, Oracle, cfg.Type, "the oracle:// scheme must infer the type")
	require.Empty(t, cfg.Oracle.Service.Name)
	require.Empty(t, cfg.Oracle.Service.SID)
	require.Empty(t, cfg.Database, "buildOracleDSN returns the connection string verbatim, so no identifier field is needed")
}

func TestValidateOracleWithoutConnectionStringStillNeedsIdentifier(t *testing.T) {
	cfg := DatabaseConfig{Type: Oracle, Host: testOracleHost, Port: 1521, Username: "oracleuser"}

	err := normalizeDatabaseSection(&cfg, rootDatabaseSection())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one required",
		"without a DSN nothing else names the target, so the identifier stays mandatory")
}

func TestValidateRejectsShortDatabasePassword(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.Database.Password = "short" // 5 bytes
	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database.password")
	assert.NotContains(t, err.Error(), "short", "the error must never echo the password")
}

func TestValidateAllowsEmptyDatabasePassword(t *testing.T) {
	// Empty means trust/IAM auth; it never reaches the redaction-suppression path.
	cfg := createValidFullConfig()
	cfg.Database.Password = ""
	require.NoError(t, Validate(cfg))
}

func TestValidateAllowsLongEnoughDatabasePassword(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.Database.Password = "longenough-pw"
	require.NoError(t, Validate(cfg))
}

// The bound is <, not <=: a password of exactly MinDatabasePasswordLength is long
// enough for migration.redactPassword to substring-redact, so it is accepted.
func TestValidateAllowsPasswordOfExactlyMinimumLength(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.Database.Password = strings.Repeat("p", MinDatabasePasswordLength)
	require.NoError(t, Validate(cfg))
}

// Both ends of the optional-port range are inclusive, and 0 means "unset" rather
// than "invalid" — the field is optional precisely because a DSN can carry the port.
func TestValidateOptionalDatabasePortBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		port    int
		wantErr bool
	}{
		{name: "unset_port_accepted", port: 0},
		{name: "lowest_port_accepted", port: 1},
		{name: "highest_port_accepted", port: 65535},
		{name: "negative_port_rejected", port: -1, wantErr: true},
		{name: "port_above_range_rejected", port: 65536, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOptionalDatabasePort(tt.port)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), fieldDatabasePort)
		})
	}
}

// normalizeAndCheckNamedDatabases runs both halves of the named-database
// split in phase order: an incomplete section is normalize's rejection, a bad
// name or tenant-ID collision is check's.
func normalizeAndCheckNamedDatabases(databases map[string]DatabaseConfig, mt *MultitenantConfig) error {
	if err := normalizeNamedDatabases(databases); err != nil {
		return err
	}
	return checkNamedDatabases(databases, mt)
}

func TestValidateNamedDatabasesSuccess(t *testing.T) {
	tests := []struct {
		name      string
		databases map[string]DatabaseConfig
		mt        MultitenantConfig
	}{
		{
			name:      "nil_databases_map",
			databases: nil,
			mt:        MultitenantConfig{Enabled: false},
		},
		{
			name:      "empty_databases_map",
			databases: map[string]DatabaseConfig{},
			mt:        MultitenantConfig{Enabled: false},
		},
		{
			name: "single_postgresql_database",
			databases: map[string]DatabaseConfig{
				"legacy": {
					Type:     PostgreSQL,
					Host:     "legacy.db.local",
					Port:     5432,
					Database: "legacy_db",
					Username: "legacy_user",
				},
			},
			mt: MultitenantConfig{Enabled: false},
		},
		{
			name: "multiple_databases_mixed_vendors",
			databases: map[string]DatabaseConfig{
				"legacy": {
					Type:     Oracle,
					Host:     testOracleHost,
					Port:     1521,
					Username: "oracle_user",
					Oracle:   OracleConfig{Service: ServiceConfig{Name: "LEGACYDB"}},
				},
				"analytics": {
					Type:     PostgreSQL,
					Host:     "analytics.db.local",
					Port:     5432,
					Database: "analytics_db",
					Username: "analytics_user",
				},
			},
			mt: MultitenantConfig{Enabled: false},
		},
		{
			name: "named_databases_with_multitenant_no_conflict",
			databases: map[string]DatabaseConfig{
				"shared-analytics": {
					Type:     PostgreSQL,
					Host:     "shared.db.local",
					Port:     5432,
					Database: "shared_db",
					Username: "shared_user",
				},
			},
			mt: MultitenantConfig{
				Enabled: true,
				Tenants: map[string]TenantEntry{
					tenantA: { // Different from named database key
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     testTenantDBHost,
							Port:     5432,
							Database: "tenant_a",
							Username: "tenant_user",
						},
					},
				},
			},
		},
		{
			name: "database_with_connection_string",
			databases: map[string]DatabaseConfig{
				"external": {
					ConnectionString: testConnectionString,
				},
			},
			mt: MultitenantConfig{Enabled: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkNamedDatabases(tt.databases, &tt.mt)
			assert.NoError(t, err)
		})
	}
}

func TestValidateNamedDatabasesFailures(t *testing.T) {
	tests := []struct {
		name        string
		databases   map[string]DatabaseConfig
		mt          MultitenantConfig
		errContains string
	}{
		{
			name: "empty_database_name",
			databases: map[string]DatabaseConfig{
				"": {
					Type:     PostgreSQL,
					Host:     dbLocalField,
					Port:     5432,
					Database: "db",
					Username: "user",
				},
			},
			mt:          MultitenantConfig{Enabled: false},
			errContains: "cannot be empty",
		},
		{
			name: "reserved_prefix_in_name",
			databases: map[string]DatabaseConfig{
				"named:legacy": {
					Type:     PostgreSQL,
					Host:     dbLocalField,
					Port:     5432,
					Database: "db",
					Username: "user",
				},
			},
			mt:          MultitenantConfig{Enabled: false},
			errContains: "reserved prefix",
		},
		{
			name: "conflict_with_tenant_id",
			databases: map[string]DatabaseConfig{
				tenantA: { // Same as tenant ID
					Type:     PostgreSQL,
					Host:     dbLocalField,
					Port:     5432,
					Database: "db",
					Username: "user",
				},
			},
			mt: MultitenantConfig{
				Enabled: true,
				Tenants: map[string]TenantEntry{
					tenantA: {
						Database: DatabaseConfig{
							Type:     PostgreSQL,
							Host:     testTenantDBHost,
							Port:     5432,
							Database: "tenant_a",
							Username: "tenant_user",
						},
					},
				},
			},
			errContains: "conflicts with tenant ID",
		},
		{
			name: "incomplete_database_config",
			databases: map[string]DatabaseConfig{
				"incomplete": {
					// Missing required fields
				},
			},
			mt:          MultitenantConfig{Enabled: false},
			errContains: "incomplete",
		},
		{
			name: "invalid_database_type",
			databases: map[string]DatabaseConfig{
				"invalid": {
					Type:     "mysql", // Not supported
					Host:     dbLocalField,
					Port:     3306,
					Database: "db",
					Username: "user",
				},
			},
			mt:          MultitenantConfig{Enabled: false},
			errContains: "not supported",
		},
		{
			name: "invalid_port",
			databases: map[string]DatabaseConfig{
				"badport": {
					Type:     PostgreSQL,
					Host:     dbLocalField,
					Port:     -1,
					Database: "db",
					Username: "user",
				},
			},
			mt:          MultitenantConfig{Enabled: false},
			errContains: "port",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeAndCheckNamedDatabases(tt.databases, &tt.mt)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

// TestValidateNamedDatabasesRejectsDottedName proves a database name
// containing '.' is rejected: it collides with koanf's path delimiter, so
// constructed section paths like databases.<name> would become ambiguous.
func TestValidateNamedDatabasesRejectsDottedName(t *testing.T) {
	databases := map[string]DatabaseConfig{
		"legacy.reporting": {
			Type:     PostgreSQL,
			Host:     dbLocalField,
			Port:     5432,
			Database: "db",
			Username: "user",
		},
	}
	mt := MultitenantConfig{Enabled: false}

	err := checkNamedDatabases(databases, &mt)
	assertValidationError(t, err, "cannot contain '.'")
}

func TestValidateNamedDatabasesNoConflictWhenMultitenantDisabled(t *testing.T) {
	// When multitenant is disabled, no conflict check is needed
	databases := map[string]DatabaseConfig{
		tenantA: { // Would conflict if multitenant were enabled
			Type:     PostgreSQL,
			Host:     dbLocalField,
			Port:     5432,
			Database: "db",
			Username: "user",
		},
	}
	mt := MultitenantConfig{
		Enabled: false,
		Tenants: map[string]TenantEntry{
			tenantA: {
				Database: DatabaseConfig{},
			},
		},
	}

	err := checkNamedDatabases(databases, &mt)
	assert.NoError(t, err, "no conflict when multitenant is disabled")
}

func TestValidatePostgreSQLFieldsRejectsPartialClientCert(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*DatabaseConfig)
	}{
		{"cert_without_key", func(c *DatabaseConfig) { c.TLS.CertFile = "/etc/ssl/client.crt" }},
		{"key_without_cert", func(c *DatabaseConfig) { c.TLS.KeyFile = "/etc/ssl/client.key" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &DatabaseConfig{Type: PostgreSQL, Host: "h", Database: "d"}
			// A mandatory mode isolates the pairing rule from the material/mode rule.
			cfg.TLS.Mode = sslModeRequire
			tc.mutate(cfg)
			err := validatePostgreSQLFields(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "sslcert and sslkey",
				"a lone client cert/key is silently dropped under sslmode=disable, so reject the unpaired config up front")
		})
	}
}

func TestValidatePostgreSQLFieldsAllowsMaterialUnderMandatoryMode(t *testing.T) {
	both := &DatabaseConfig{Type: PostgreSQL}
	both.TLS.Mode = sslModeRequire
	both.TLS.CertFile = testTLSCertFile
	both.TLS.KeyFile = testTLSKeyFile
	require.NoError(t, validatePostgreSQLFields(both))

	caOnly := &DatabaseConfig{Type: PostgreSQL}
	// CA alone is valid under a mandatory mode (server auth, no client cert).
	caOnly.TLS.Mode = sslModeVerifyCA
	caOnly.TLS.CAFile = testTLSCAFile
	require.NoError(t, validatePostgreSQLFields(caOnly))
}

func TestValidatePostgreSQLFieldsRejectsUnknownTLSMode(t *testing.T) {
	cases := []struct {
		name string
		mode string
	}{
		{"typo", "requird"},
		{"wrong_case", "Require"},
		{"underscore", "verify_full"},
		{"whitespace_only", " "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &DatabaseConfig{Type: PostgreSQL, Host: "h", Database: "d"}
			cfg.TLS.Mode = tc.mode
			// Through the vendor seam so the trim runs first (" " must not be treated as a mode).
			err := validateVendorSpecificFields(cfg)
			if tc.mode == " " {
				require.NoError(t, err, "a whitespace-only mode trims to unset, not to an invalid value")
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "database.tls.mode")
			assert.Contains(t, err.Error(), tc.mode)
		})
	}
}

func TestValidatePostgreSQLFieldsTrimsTLSMode(t *testing.T) {
	cfg := &DatabaseConfig{Type: PostgreSQL, Host: "h", Database: "d"}
	cfg.TLS.Mode = " require "
	cfg.TLS.CertFile = " " + testTLSCertFile + " "
	cfg.TLS.KeyFile = " " + testTLSKeyFile + " "

	require.NoError(t, validateVendorSpecificFields(cfg))
	// The trim must persist: buildPostgresDSN reads these fields verbatim later.
	assert.Equal(t, sslModeRequire, cfg.TLS.Mode)
	assert.Equal(t, testTLSCertFile, cfg.TLS.CertFile)
	assert.Equal(t, testTLSKeyFile, cfg.TLS.KeyFile)
}

func TestValidatePostgreSQLFieldsTrimsTLSCAFile(t *testing.T) {
	cfg := &DatabaseConfig{Type: PostgreSQL, Host: "h", Database: "d"}
	cfg.TLS.Mode = sslModeVerifyFull
	cfg.TLS.CAFile = " " + testTLSCAFile + " "

	require.NoError(t, validateVendorSpecificFields(cfg))
	assert.Equal(t, testTLSCAFile, cfg.TLS.CAFile)
}

func TestValidatePostgreSQLFieldsRejectsMaterialWithoutMandatoryMode(t *testing.T) {
	materials := []struct {
		name   string
		mutate func(*DatabaseConfig)
	}{
		{"cert_and_key", func(c *DatabaseConfig) {
			c.TLS.CertFile = testTLSCertFile
			c.TLS.KeyFile = testTLSKeyFile
		}},
		{"ca_only", func(c *DatabaseConfig) { c.TLS.CAFile = testTLSCAFile }},
	}
	for _, mode := range []string{"", sslModeDisable, sslModeAllow, sslModePrefer} {
		for _, m := range materials {
			name := "unset"
			if mode != "" {
				name = mode
			}
			t.Run(name+"_"+m.name, func(t *testing.T) {
				cfg := &DatabaseConfig{Type: PostgreSQL, Host: "h", Database: "d"}
				cfg.TLS.Mode = mode
				m.mutate(cfg)
				err := validatePostgreSQLFields(cfg)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "require, verify-ca, or verify-full",
					"pgx discards TLS material under non-mandatory modes, so the config must not boot")
			})
		}
	}
}

func TestValidatePostgreSQLFieldsAllowsModeAloneAnyValid(t *testing.T) {
	for _, mode := range []string{"", sslModeDisable, sslModeAllow, sslModePrefer, sslModeRequire, sslModeVerifyCA, sslModeVerifyFull} {
		name := "unset"
		if mode != "" {
			name = mode
		}
		t.Run(name, func(t *testing.T) {
			cfg := &DatabaseConfig{Type: PostgreSQL, Host: "h", Database: "d"}
			cfg.TLS.Mode = mode
			require.NoError(t, validatePostgreSQLFields(cfg),
				"a valid mode without TLS material discards nothing, so it stays allowed")
		})
	}
}

func TestValidatePostgreSQLFieldsRejectsTLSBlockWithConnectionString(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*DatabaseConfig)
	}{
		{"mode_only", func(c *DatabaseConfig) { c.TLS.Mode = sslModeRequire }},
		{"ca_only", func(c *DatabaseConfig) { c.TLS.CAFile = testTLSCAFile }},
		{"cert_and_key", func(c *DatabaseConfig) {
			c.TLS.Mode = sslModeVerifyFull
			c.TLS.CertFile = testTLSCertFile
			c.TLS.KeyFile = testTLSKeyFile
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &DatabaseConfig{Type: PostgreSQL, ConnectionString: testConnectionString}
			tc.mutate(cfg)
			err := validatePostgreSQLFields(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "connectionstring",
				"the tls block never reaches a connection-string DSN, so accepting it advertises inert TLS")
		})
	}
}

func TestValidatePostgreSQLFieldsAllowsConnectionStringWithoutTLSBlock(t *testing.T) {
	cfg := &DatabaseConfig{Type: PostgreSQL, ConnectionString: testConnectionString}
	require.NoError(t, validatePostgreSQLFields(cfg))
}

func TestValidateOracleFieldsRejectsTLSMaterial(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*DatabaseConfig)
	}{
		{"ca", func(c *DatabaseConfig) { c.TLS.CAFile = testTLSCAFile }},
		{"cert", func(c *DatabaseConfig) { c.TLS.CertFile = testTLSCertFile }},
		{"key", func(c *DatabaseConfig) { c.TLS.KeyFile = testTLSKeyFile }},
		{"mode", func(c *DatabaseConfig) { c.TLS.Mode = sslModeRequire }},
	}
	bases := []struct {
		name string
		cfg  DatabaseConfig
	}{
		{"identifier", DatabaseConfig{Type: "oracle", Database: "PDB1"}},
		// A connection string waives the identifier requirement but never the TLS one.
		{"connection_string", DatabaseConfig{Type: "oracle", ConnectionString: testOracleConnectionString}},
	}
	for _, base := range bases {
		for _, tc := range cases {
			t.Run(base.name+"_"+tc.name, func(t *testing.T) {
				cfg := base.cfg
				tc.mutate(&cfg)
				err := validateOracleFields(&cfg)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "not supported for Oracle",
					"Oracle TLS material must be rejected, not silently dropped (which leaves the connection unauthenticated)")
			})
		}
	}
}

func TestValidateOracleFieldsAllowsAbsentTLSBlock(t *testing.T) {
	require.NoError(t, validateOracleFields(&DatabaseConfig{Type: Oracle, Database: "PDB1"}))
}

func TestValidateRejectsManagerBlockOnNamedDatabase(t *testing.T) {
	cfg := &Config{
		App: AppConfig{
			Name:    testAppName,
			Version: testAppVersion,
			Env:     EnvDevelopment,
			Rate:    RateConfig{Limit: 100},
		},
		Server: ServerConfig{
			Port: 8080,
			Timeout: TimeoutConfig{
				Read:       15 * time.Second,
				Write:      30 * time.Second,
				Middleware: 5 * time.Second,
				Shutdown:   10 * time.Second,
			},
		},
		Log: LogConfig{Level: "info"},
		Database: DatabaseConfig{
			Type: PostgreSQL, Host: "localhost", Port: 5432, Database: "app",
			Username: "user", Password: "longenough-pw",
		},
		Databases: map[string]DatabaseConfig{
			"legacy": {
				Type: PostgreSQL, Host: "localhost", Port: 5432, Database: "legacy",
				Username: "user", Password: "longenough-pw",
				Manager: DatabaseManagerConfig{MaxSize: 5},
			},
		},
	}

	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "databases.legacy.manager")
}

func TestApplyDatabasePoolDefaultsNilConfig(t *testing.T) {
	var err error
	require.NotPanics(t, func() {
		err = ApplyDatabasePoolDefaults(nil)
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "configuration is nil")
}

// TestValidateNoDeliveredEmptyDatabaseViaLoad drives validateNoDeliveredEmptyDatabase
// through the real Load() path (env + YAML), since the koanf presence semantics it
// relies on cannot be exercised through a hand-built Config literal (see
// TestValidateNoDeliveredEmptyDatabaseInertForLiteral for that guarantee instead).
func TestValidateNoDeliveredEmptyDatabaseViaLoad(t *testing.T) {
	tests := []struct {
		name           string
		env            map[string]string
		yaml           string
		wantErr        bool
		wantContains   []string
		wantNotContain []string
		// wantOrdered pins the sorted-path contract: each entry must appear
		// strictly before the next in the rendered error.
		wantOrdered []string
	}{
		{
			name:         "env_delivered_empty_host_fails",
			env:          map[string]string{"DATABASE_HOST": ""},
			wantErr:      true,
			wantContains: []string{"delivered empty", "database.host"},
		},
		{
			name:         "yaml_bare_host_key_fails",
			yaml:         "database:\n  host:\n",
			wantErr:      true,
			wantContains: []string{"database.host"},
		},
		{
			name: "empty_database_block_is_absence",
			yaml: "database: {}\n",
		},
		{
			name: "absent_section_is_absence",
		},
		{
			name: "real_value_bypasses",
			env: map[string]string{
				"DATABASE_TYPE":      PostgreSQL,
				"DATABASE_HOST":      "db.internal",
				"DATABASE_PORT":      "5432",
				testDatabaseDatabase: "testdb",
				testDatabaseUsername: "testuser",
			},
		},
		{
			name:         "named_database_empty_host_fails",
			yaml:         "databases:\n  reporting:\n    host:\n",
			wantErr:      true,
			wantContains: []string{"databases.reporting.host"},
		},
		{
			name: "tenant_database_empty_username_fails",
			yaml: "multitenant:\n  enabled: true\n  resolver:\n    type: header\n  tenants:\n" +
				"    acme:\n      database:\n        username:\n",
			wantErr:        true,
			wantContains:   []string{"multitenant.tenants.acme.database.username"},
			wantNotContain: []string{"configuration required"},
		},
		{
			name:         "multiple_offenders_listed",
			yaml:         "databases:\n  alpha:\n    host:\n  beta:\n    host:\n",
			wantErr:      true,
			wantContains: []string{"databases.alpha.host", "databases.beta.host"},
			wantOrdered:  []string{"databases.alpha.host", "databases.beta.host"},
		},
		{
			// Every empty key in one section is named, not just the first: an
			// operator who cleared only the first would hit the same abort again.
			// The keys are chosen so databaseIdentityKeys order (type before host)
			// contradicts sorted order — wantOrdered therefore pins slices.Sort,
			// not the traversal order it happens to share elsewhere.
			name:         "all_empty_keys_in_one_section_listed",
			yaml:         "database:\n  type:\n  host:\n  username:\n",
			wantErr:      true,
			wantContains: []string{"database.type", "database.host", "database.username"},
			wantOrdered:  []string{"database.host", "database.type", "database.username"},
		},
		{
			// Koanf populates Multitenant.Tenants from YAML regardless of the enabled
			// flag, but TenantStore, ManagerConfigBuilder and normalizeMultitenant all
			// ignore the block in single-tenant mode — so a leftover tenants section is
			// inert, and aborting startup over it would be a false abort on a config
			// that runs today.
			name: "disabled_multitenancy_leftover_tenant_ignored",
			yaml: "multitenant:\n  enabled: false\n  tenants:\n" +
				"    acme:\n      database:\n        username:\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadDeliveredEmptyFixture(t, tc.yaml, tc.env)
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, cfg)
				assertDeliveredEmptyError(t, err.Error(), tc.wantContains, tc.wantNotContain, tc.wantOrdered)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, cfg)
		})
	}
}

// TestValidateNoDeliveredEmptyDatabaseInertForLiteral pins the Exists nil-safety the
// whole design leans on: a hand-built Config (no koanf instance) never trips the
// validator, regardless of how empty its Database section is. The function is called
// directly so the pin does not depend on which other step of Validate rejects the
// zero Config first.
func TestValidateNoDeliveredEmptyDatabaseInertForLiteral(t *testing.T) {
	cfg := &Config{}
	assert.NoError(t, validateNoDeliveredEmptyDatabase(cfg))
}

// TestLoadDefaultsCarryNoDatabaseIdentityKeys pins the enabling invariant this whole
// design rests on (config/config.go's loadDefaults registers no database.* keys): if a
// default is ever added for one of these keys, every deployment would read as
// "delivered" and this test fails.
func TestLoadDefaultsCarryNoDatabaseIdentityKeys(t *testing.T) {
	clearEnvironmentVariables()
	defer clearEnvironmentVariables()
	t.Chdir(t.TempDir())

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	for _, k := range databaseIdentityKeys {
		assert.False(t, cfg.Exists("database."+k), "database.%s must not be a registered default", k)
	}
}

// TestDatabaseIdentityKeysMatchPredicate pins the one direction it can enforce
// automatically: every entry in databaseIdentityKeys corresponds to a DatabaseConfig
// field that IsDatabaseConfigured recognizes. The reverse direction — a field added to
// IsDatabaseConfigured but never to the list — is not detectable by iterating the list;
// the length assertion below is the tripwire for that direction.
func TestDatabaseIdentityKeysMatchPredicate(t *testing.T) {
	// grew IsDatabaseConfigured? grow this list first, then this number.
	require.Len(t, databaseIdentityKeys, 9)

	for _, key := range databaseIdentityKeys {
		t.Run(key, func(t *testing.T) {
			cfg := DatabaseConfig{}
			switch key {
			case "connectionstring":
				cfg.ConnectionString = testConnectionString
			case "type":
				cfg.Type = PostgreSQL
			case "host":
				cfg.Host = "db.internal"
			case "port":
				cfg.Port = 5432
			case "database":
				cfg.Database = "appdb"
			case "username":
				cfg.Username = "app"
			case "password":
				cfg.Password = "s3cretpw"
			case "oracle.service.name":
				cfg.Oracle.Service.Name = "ORCLPDB1"
			case "oracle.service.sid":
				cfg.Oracle.Service.SID = "ORCL"
			default:
				t.Fatalf("databaseIdentityKeys entry %q has no mapping in this test — add one, and check IsDatabaseConfigured too", key)
			}
			assert.True(t, IsDatabaseConfigured(&cfg))
		})
	}
}

// TestApplyDatabasePoolDefaultsInfersTypeFromScheme runs the shared inference
// fixtures against the seam. Unrecognized and absent DSNs leave Type empty and
// return no error: this seam is on the per-tenant connection path, so
// database.NewConnection's "unsupported database type" stays the failure surface
// for dynamic sources (ADR-050 keeps classification in the config/app layers).
func TestApplyDatabasePoolDefaultsInfersTypeFromScheme(t *testing.T) {
	for _, tt := range databaseTypeInferenceCases() {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.config
			dsn := cfg.ConnectionString

			require.NoError(t, ApplyDatabasePoolDefaults(&cfg))

			assert.Equal(t, tt.expectedType, cfg.Type)
			assert.Equal(t, dsn, cfg.ConnectionString, "classification tolerates whitespace; the stored DSN stays byte-exact")
		})
	}
}

// TestApplyDatabasePoolDefaultsRunsVendorValidation pins the fail-open this seam
// closes: before it ran validateVendorSpecificFields, a dynamic tenant whose Type
// resolved to oracle dialed with its TLS material silently dropped — the exact
// state validateOracleFields exists to prevent — and a PostgreSQL config with a
// lone sslcert connected with the certificate silently ignored under
// sslmode=disable.
func TestApplyDatabasePoolDefaultsRunsVendorValidation(t *testing.T) {
	const certPath = "/etc/certs/client.crt"

	t.Run("inferred_oracle_with_tls_material_rejected", func(t *testing.T) {
		cfg := DatabaseConfig{ConnectionString: testOracleConnectionString}
		cfg.TLS.CertFile = certPath
		cfg.TLS.KeyFile = "/etc/certs/client.key"
		original := cfg

		err := ApplyDatabasePoolDefaults(&cfg)

		assertValidationError(t, err, "not supported for Oracle")
		assert.Equal(t, original, cfg, "a rejected config must go back to its caller completely untouched")
	})

	// ADR-062: any database.tls field alongside a connectionstring is rejected
	// outright (R4), before the pairing rule can fire.
	t.Run("inferred_postgres_with_tls_block_rejected", func(t *testing.T) {
		cfg := DatabaseConfig{ConnectionString: testBarePostgresConnString}
		cfg.TLS.CertFile = certPath
		original := cfg

		err := ApplyDatabasePoolDefaults(&cfg)

		assertValidationError(t, err, "database.tls is ignored when connectionstring is set")
		assert.Equal(t, original, cfg, "a rejected config must go back to its caller completely untouched")
	})

	// The guard reaches typed dynamic configs too, not only the DSN-only shape
	// that motivated the seam: this pair connected before the guard landed.
	t.Run("explicit_oracle_type_with_ca_file_rejected", func(t *testing.T) {
		cfg := DatabaseConfig{Type: Oracle, Host: testOracleHost, Database: "XEPDB1"}
		cfg.TLS.CAFile = "/etc/certs/ca.pem"
		original := cfg

		err := ApplyDatabasePoolDefaults(&cfg)

		assertValidationError(t, err, "not supported for Oracle")
		assert.Equal(t, original, cfg, "a rejected config must go back to its caller completely untouched")
	})

	// ADR-062 forbids database.tls next to a connectionstring, so the accepted
	// shape is a typed config with a TLS-mandatory mode; inference-accepted paths
	// are pinned by the classification table and the rollback case below.
	t.Run("paired_postgres_certificate_under_require_accepted", func(t *testing.T) {
		cfg := DatabaseConfig{Type: PostgreSQL, Host: "h", Database: "d"}
		cfg.TLS.Mode = sslModeRequire
		cfg.TLS.CertFile = certPath
		cfg.TLS.KeyFile = "/etc/certs/client.key"

		require.NoError(t, ApplyDatabasePoolDefaults(&cfg))
		assert.Equal(t, int32(25), cfg.Pool.Max.Connections, "defaults still applied after vendor validation")
	})

	// Every case above is rejected by validateVendorSpecificFields, which returns
	// before any defaulting runs — so none of them proves the rollback holds when
	// the LATER step fails. This one does: vendor validation passes, Type is
	// inferred, defaulting then rejects the negative idle time, and the caller must
	// still get its config back whole (no inferred Type, no half-applied defaults).
	t.Run("rollback_after_pool_defaulting_error", func(t *testing.T) {
		cfg := DatabaseConfig{ConnectionString: testBarePostgresConnString}
		cfg.Pool.Idle.Time = -1
		original := cfg

		err := ApplyDatabasePoolDefaults(&cfg)

		assertValidationError(t, err, "database.pool.idle.time must be non-negative")
		assert.Equal(t, original, cfg, "a config rejected after inference must not keep the inferred Type or partial defaults")
		assert.Empty(t, cfg.Type, "Type is committed only after every step succeeds")
	})
}

func TestApplyDatabasePoolDefaultsKeepsExplicitType(t *testing.T) {
	// The conflicting case pins a deliberate divergence from
	// normalizeWithConnectionString (startup strictness), which rejects a Type
	// contradicting the scheme: this seam runs per connection (connect
	// strictness), so it leaves the explicit Type alone and lets the vendor dial
	// error be the failure (ADR-050). Do not "fix" it.
	// The matching-type case is covered by explicit_matching_type_untouched in
	// databaseTypeInferenceCases, which this seam's table test already runs.
	conflicting := DatabaseConfig{Type: Oracle, ConnectionString: testBarePostgresConnString}
	require.NoError(t, ApplyDatabasePoolDefaults(&conflicting))
	assert.Equal(t, Oracle, conflicting.Type)
}
