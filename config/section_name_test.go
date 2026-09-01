package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assertSectionNameRejected collapses the tail every env-reachability case
// shares: a *ConfigError naming the offending KEY PATH and an action telling
// the operator to rename it.
func assertSectionNameRejected(t *testing.T, err error, wantField string) {
	t.Helper()
	require.Error(t, err)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, wantField, cfgErr.Field)
	assert.ErrorContains(t, err, "rename")
}

// TestValidateRejectsAnUnreachableSectionThroughThePublicDoor drives Validate
// itself, not the per-section checker. ADR-090 and [C61.22] both promise that a
// hand-built Config is judged the same way — every construction path calls
// Validate (ADR-064) — and only this test would notice if a phase reorder or an
// early return left the rule unreached.
func TestValidateRejectsAnUnreachableSectionThroughThePublicDoor(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.Databases = map[string]DatabaseConfig{"report_db": createValidDatabaseConfig()}

	err := Validate(cfg)

	require.Error(t, err)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr, "the section error survives Validate's wrapping")
	assert.Equal(t, "databases.report_db", cfgErr.Field)
}

// TestCheckSectionNameGrammar exercises the shared rule directly, at the
// character-class boundary, so the three call sites are left proving only that
// they WIRE it — and its own branch is covered without going through a section.
func TestCheckSectionNameGrammar(t *testing.T) {
	tests := []struct {
		name     string
		section  string
		accepted bool
	}{
		{name: "lowercase", section: "reporting", accepted: true},
		{name: "digits", section: "db2", accepted: true},
		{name: "hyphen", section: "report-db", accepted: true},
		{name: "digits_only", section: "2", accepted: true},
		{name: "hyphen_only", section: "-", accepted: true},
		{name: "empty", section: ""},
		{name: "underscore", section: "report_db"},
		{name: "uppercase", section: "Reporting"},
		{name: "dot", section: "report.db"},
		{name: "space", section: "report db"},
		{name: "non_ascii", section: "reporté"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkSectionName("databases."+tt.section, tt.section)
			if tt.accepted {
				require.NoError(t, err)
				return
			}
			assertSectionNameRejected(t, err, "databases."+tt.section)
		})
	}
}

// TestValidateRejectsSectionNamesUnreachableByEnv is the reproducer: the env
// transform lowercases and maps '_' to '.', so a name carrying '_' or an
// uppercase letter cannot be addressed by any environment variable — its
// variable lands on a different key. Names are judged against ^[a-z0-9-]+$ at
// check, which makes the transform injective over every key that survives
// startup.
func TestValidateRejectsSectionNamesUnreachableByEnv(t *testing.T) {
	tests := []struct {
		name      string
		dbName    string
		wantField string
	}{
		{name: "underscore_in_name", dbName: "report_db", wantField: "databases.report_db"},
		{name: "uppercase_in_name", dbName: "Reporting", wantField: "databases.Reporting"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			databases := map[string]DatabaseConfig{
				tt.dbName: {
					Type:     PostgreSQL,
					Host:     dbLocalField,
					Port:     5432,
					Database: "db",
					Username: "user",
				},
			}
			mt := MultitenantConfig{Enabled: false}

			err := checkNamedDatabases(databases, &mt)

			assertSectionNameRejected(t, err, tt.wantField)
		})
	}
}

// TestValidateNamedDatabaseReportsADottedReservedNameAgainstTheParent: a name can
// break two rules at once. The reserved-prefix error embeds the name in its Field
// (databases.<name>), which is exactly what a dotted name makes ambiguous — so the
// dot rule has to win, whichever other rule also matches.
func TestValidateNamedDatabaseReportsADottedReservedNameAgainstTheParent(t *testing.T) {
	databases := map[string]DatabaseConfig{
		NamedDatabasePrefix + ".foo": {
			Type:     PostgreSQL,
			Host:     dbLocalField,
			Port:     5432,
			Database: "db",
			Username: "user",
		},
	}
	mt := MultitenantConfig{Enabled: false}

	err := checkNamedDatabases(databases, &mt)

	require.Error(t, err)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, fieldDatabases, cfgErr.Field, "a dotted name never embeds itself in the Field, whatever else it violates")
	assert.ErrorContains(t, err, "'.'")
}

// TestValidateAcceptsEnvReachableSectionNames pins the other half: the rule
// admits every name the transform round-trips, hyphen included.
func TestValidateAcceptsEnvReachableSectionNames(t *testing.T) {
	for _, name := range []string{"report-db", "reporting", "db2"} {
		t.Run(name, func(t *testing.T) {
			databases := map[string]DatabaseConfig{
				name: {
					Type:     PostgreSQL,
					Host:     dbLocalField,
					Port:     5432,
					Database: "db",
					Username: "user",
				},
			}
			mt := MultitenantConfig{Enabled: false}

			require.NoError(t, checkNamedDatabases(databases, &mt))
		})
	}
}

// TestValidateRejectsSiblingCollisionBeforeItCanHappen pins the silent shape the
// rule exists to prevent: with both a report and a report_db section,
// DATABASES_REPORT_DB_PORT used to land on report while report_db silently kept
// its YAML value. The failure is now at validation, so no resolved value is ever
// read from the wrong section.
func TestValidateRejectsSiblingCollisionBeforeItCanHappen(t *testing.T) {
	entry := DatabaseConfig{
		Type:     PostgreSQL,
		Host:     dbLocalField,
		Port:     5432,
		Database: "db",
		Username: "user",
	}
	databases := map[string]DatabaseConfig{"report": entry, "report_db": entry}
	mt := MultitenantConfig{Enabled: false}

	err := checkNamedDatabases(databases, &mt)

	assertSectionNameRejected(t, err, "databases.report_db")
}

// TestEnvVarToKeyIsUnchangedForEveryReachableName is seam 4: the rule constrains
// which names are legal, never how a variable maps to a key. Every mapping an
// existing deployment relies on must be byte-identical.
func TestEnvVarToKeyIsUnchangedForEveryReachableName(t *testing.T) {
	tests := []struct {
		envVar string
		key    string
	}{
		{envVar: "LOG_SENSITIVEFIELDS", key: "log.sensitivefields"},
		{envVar: "DATABASES_REPORTING_PORT", key: "databases.reporting.port"},
		{envVar: "MULTITENANT_TENANTS_ACME_DATABASE_HOST", key: "multitenant.tenants.acme.database.host"},
		{envVar: "KEYSTORE_KEYS_SIGNING_PUBLIC_FILE", key: "keystore.keys.signing.public.file"},
		{envVar: "MESSAGING_RECONNECT_CONNECTIONTIMEOUT", key: "messaging.reconnect.connectiontimeout"},
		{envVar: "DATABASE_POOL_MAX_CONNECTIONS", key: "database.pool.max.connections"},
	}

	for _, tt := range tests {
		t.Run(tt.envVar, func(t *testing.T) {
			assert.Equal(t, tt.key, envVarToKey(tt.envVar))
		})
	}
}
