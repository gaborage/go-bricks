//go:build integration

package migration

import (
	"database/sql"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlywayMigrateFreshSchemaPopulatesResult exercises the happy path:
// applying migrations to a fresh schema. Verifies the structured Result
// fields against live Flyway output (rather than captured fixtures) and
// that flyway_schema_history is created automatically on first run.
func TestFlywayMigrateFreshSchemaPopulatesResult(t *testing.T) {
	env := newIntegrationEnv(t)
	env.writeMigration(t, 1, "create_users",
		"CREATE TABLE users (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL);")
	env.writeMigration(t, 2, "create_orders",
		"CREATE TABLE orders (id BIGSERIAL PRIMARY KEY, user_id BIGINT NOT NULL, total NUMERIC NOT NULL);")

	dbCfg := env.dbConfigFor(env.defaultDB)
	ctx, cancel := testCtx(t)
	defer cancel()
	result, err := env.migrator().MigrateFor(ctx, dbCfg, env.flywayConfig(t))
	require.NoError(t, err)

	assert.True(t, result.Success)
	assert.Equal(t, []string{"1", "2"}, result.AppliedVersions)
	assert.Empty(t, result.StartingVersion, "fresh schema has no starting version")
	assert.Equal(t, "2", result.EndingVersion)
	assert.Equal(t, "PostgreSQL", result.DatabaseType)
	assert.NotEmpty(t, result.FlywayVersion, "real Flyway should populate the engine version")

	// flyway_schema_history exists with exactly one row per applied version.
	entries := env.schemaHistoryEntries(t, env.defaultDB)
	require.Len(t, entries, 2)
	assert.Equal(t, "1", entries[0].Version)
	assert.Equal(t, "2", entries[1].Version)
	assert.True(t, entries[0].Success)
	assert.True(t, entries[1].Success)
}

// TestFlywayMigrateChecksumMismatchFailsFast modifies an applied script
// and asserts the next run fails with VALIDATE_ERROR before any new SQL is
// executed — the production failure mode the structured Result.ErrorCode
// is designed to expose to downstream alerting.
func TestFlywayMigrateChecksumMismatchFailsFast(t *testing.T) {
	env := newIntegrationEnv(t)
	scriptPath := env.writeMigration(t, 1, "create_users",
		"CREATE TABLE users (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL);")

	dbCfg := env.dbConfigFor(env.defaultDB)
	migrator := env.migrator()
	ctx, cancel := testCtx(t)
	defer cancel()

	// First run: applies cleanly.
	res, err := migrator.MigrateFor(ctx, dbCfg, env.flywayConfig(t))
	require.NoError(t, err)
	require.True(t, res.Success)
	require.Equal(t, []string{"1"}, res.AppliedVersions)

	// Tamper with the applied script — append a SQL comment so the resolved
	// checksum diverges from what Flyway recorded in flyway_schema_history.
	original, err := os.ReadFile(scriptPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(scriptPath, append(original, []byte("\n-- intentional drift\n")...), 0o600))

	// Second run: must fail fast on checksum mismatch. Live engine output
	// is verified to match the parsed error envelope shape.
	res2, err := migrator.MigrateFor(ctx, dbCfg, env.flywayConfig(t))
	require.Error(t, err, "tampered checksum must surface as an error from MigrateFor")
	assert.False(t, res2.Success)
	assert.Equal(t, "VALIDATE_ERROR", res2.ErrorCode,
		"the structured ErrorCode is the discriminator downstream alerting pins on")
	assert.Contains(t, res2.ErrorMessage, "checksum mismatch",
		"the human-readable message names the failure mode")

	// No new rows in schema history — Flyway aborted before executing any
	// pending migrations.
	entries := env.schemaHistoryEntries(t, env.defaultDB)
	assert.Len(t, entries, 1, "checksum failure must not produce a new flyway_schema_history row")
}

// TestMigrateForTargetsSchemaDespiteConfPin proves the CLI-passed -schemas /
// -defaultSchema flags override a shared conf that pins flyway.schemas=public,
// so a per-tenant schema on the DatabaseConfig wins and nothing lands in public.
func TestMigrateForTargetsSchemaDespiteConfPin(t *testing.T) {
	env := newIntegrationEnv(t)
	env.writeMigration(t, 1, "create_widgets", "CREATE TABLE widgets (id INT);")

	// Pin the WRONG schema in the conf — CLI args must win over it.
	confBytes, err := os.ReadFile(env.configPath)
	require.NoError(t, err)
	confBytes = append(confBytes, []byte("flyway.schemas=public\nflyway.defaultSchema=public\n")...)
	require.NoError(t, os.WriteFile(env.configPath, confBytes, 0o600))

	dbCfg := env.dbConfigFor(env.defaultDB)
	dbCfg.PostgreSQL.Schema = "tenant_a"

	admin := env.adminDB(t)
	ctx, cancel := testCtx(t)
	defer cancel()
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA tenant_a")
	require.NoError(t, err, "pre-create target schema")

	_, err = env.migrator().MigrateFor(ctx, dbCfg, env.flywayConfig(t))
	require.NoError(t, err)

	regclass := func(qualified string) bool {
		qctx, qcancel := testCtx(t)
		defer qcancel()
		var rel sql.NullString
		require.NoError(t, admin.QueryRowContext(qctx, "SELECT to_regclass($1)", qualified).Scan(&rel))
		return rel.Valid
	}

	assert.True(t, regclass("tenant_a.widgets"), "migration must land in the targeted schema")
	assert.True(t, regclass("tenant_a.flyway_schema_history"),
		"schema history must live in the targeted schema, not public")
	assert.False(t, regclass("public.widgets"),
		"conf's flyway.schemas=public must be overridden — nothing may land in public")
	assert.False(t, regclass("public.flyway_schema_history"),
		"schema history must not appear in public")
}
