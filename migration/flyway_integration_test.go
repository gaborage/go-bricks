//go:build integration

package migration

import (
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
	result, err := env.migrator().MigrateFor(ctx, dbCfg, env.flywayConfig())
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
	res, err := migrator.MigrateFor(ctx, dbCfg, env.flywayConfig())
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
	res2, err := migrator.MigrateFor(ctx, dbCfg, env.flywayConfig())
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
