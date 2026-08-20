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
				assert.Equal(t, 1, strings.Count(err.Error(), tt.section.path),
					"and it appears exactly once in the rendered error")
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

// TestNormalizeDatabaseValuesConnectKeepsRootSpelling pins the other half of the seam:
// the connect door shares these error constructors but has no section to speak of, so it
// must keep the root spelling rather than inherit a path from the startup door.
// TestDbSectionQualifyContract exercises the rewriter directly. Its two defensive
// branches cannot be reached through normalizeDatabaseSection today — every producer on
// that path returns a *ConfigError with a "database."-shaped field — but they are the
// module's contract with a future validator, so they are pinned here rather than left as
// untested code that only looks safe.
func TestDbSectionQualifyContract(t *testing.T) {
	named := namedDatabaseSection("reporting")

	t.Run("root_returns_the_error_untouched", func(t *testing.T) {
		original := NewInvalidFieldError("database.host", "bad", nil)

		got := rootDatabaseSection().qualify(original)

		assert.Same(t, original, got, "the root section has nothing to add")
	})

	t.Run("non_config_error_keeps_the_path_in_the_message", func(t *testing.T) {
		got := named.qualify(errors.New("boom"))

		var cfgErr *ConfigError
		assert.False(t, errors.As(got, &cfgErr))
		assert.ErrorContains(t, got, "databases.reporting: boom")
	})

	t.Run("original_error_is_left_alone", func(t *testing.T) {
		original := NewInvalidFieldError("database.host", "bad", nil)

		got := named.qualify(original)

		var cfgErr *ConfigError
		require.ErrorAs(t, got, &cfgErr)
		assert.Equal(t, "databases.reporting.host", cfgErr.Field)
		assert.Equal(t, "database.host", original.Field,
			"the connect door shares these constructors and must not inherit a section path")
	})

	t.Run("field_shapes", func(t *testing.T) {
		tests := []struct {
			name  string
			field string
			want  string
		}{
			{name: "key_under_database", field: "database.host", want: "databases.reporting.host"},
			{name: "nested_key", field: "database.pool.max.connections", want: "databases.reporting.pool.max.connections"},
			{name: "bare_database", field: fieldDatabase, want: "databases.reporting"},
			{name: "empty", field: "", want: "databases.reporting"},
			{name: "not_key_shaped", field: "oracle connection identifier", want: "databases.reporting.oracle connection identifier"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assert.Equal(t, tt.want, named.qualifyField(tt.field))
			})
		}
	})
}

func TestNormalizeDatabaseValuesConnectKeepsRootSpelling(t *testing.T) {
	cfg := DatabaseConfig{
		Type: PostgreSQL, Host: "h", Port: 5432, Database: "d", Username: "u",
		TLS: TLSConfig{CertFile: "c.pem"},
	}

	err := normalizeDatabaseValues(&cfg, dbStrictnessConnect)

	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "database.tls", cfgErr.Field)
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
