package config

import (
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
