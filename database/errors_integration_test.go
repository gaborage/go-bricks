//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/internal/dbtestlog"
	"github.com/gaborage/go-bricks/testing/containers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests prove the constraint classifiers in errors.go
// (IsUniqueViolation, IsForeignKeyViolation, ConstraintName) recognize REAL
// driver errors produced by a duplicate-key insert against real PostgreSQL and
// Oracle containers. The connection is built through the database-package
// factory (NewConnection -> database.Interface), so the error travels the full
// framework wrap chain that errors.As must traverse.

const (
	uniqueShouldCreateTableMsg = "Should create test table"
	uniqueShouldInsertFirstMsg = "First insert should succeed"
)

// defaultPoolConfig mirrors the pool sizing used by the existing per-vendor
// integration harnesses (database/postgresql and database/oracle).
func defaultPoolConfig() config.PoolConfig {
	return config.PoolConfig{
		Max: config.PoolMaxConfig{
			Connections: 25,
		},
		Idle: config.PoolIdleConfig{
			Connections: 10,
			Time:        30 * time.Minute,
		},
		Lifetime: config.LifetimeConfig{
			Max: time.Hour,
		},
	}
}

// TestIsUniqueViolationPostgresIntegration drives a real PostgreSQL duplicate-key
// insert and asserts the classifiers recognize SQLSTATE 23505 end to end.
func TestIsUniqueViolationPostgresIntegration(t *testing.T) {
	// Context with timeout to prevent indefinite hangs, mirroring the
	// database/postgresql setupTestContainer harness.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	// MustStartPostgreSQLContainer auto-skips (t.Skip) when Docker is unavailable.
	pgContainer := containers.MustStartPostgreSQLContainer(ctx, t, nil).WithCleanup(t)
	log := dbtestlog.NewDisabledTestLogger()

	// Build a database.Interface via the database-package factory (NewConnection).
	// Type must be set so the factory dispatches to the PostgreSQL driver.
	cfg := &config.DatabaseConfig{
		Type:             PostgreSQL,
		ConnectionString: pgContainer.ConnectionString(),
		Pool:             defaultPoolConfig(),
	}

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err, "Failed to create PostgreSQL connection")
	t.Cleanup(func() {
		_ = conn.Close()
	})

	require.NoError(t, conn.Health(ctx), "Failed to ping PostgreSQL")

	// Temp table with a PRIMARY KEY so a duplicate insert raises 23505 and the
	// driver populates ConstraintName.
	_, err = conn.Exec(ctx, "CREATE TABLE test_unique_pg (id INT PRIMARY KEY, name TEXT)")
	require.NoError(t, err, uniqueShouldCreateTableMsg)

	_, err = conn.Exec(ctx, "INSERT INTO test_unique_pg (id, name) VALUES ($1, $2)", 1, "first")
	require.NoError(t, err, uniqueShouldInsertFirstMsg)

	// Re-insert the SAME primary key — this is the violation under test.
	_, dupErr := conn.Exec(ctx, "INSERT INTO test_unique_pg (id, name) VALUES ($1, $2)", 1, "duplicate")
	require.Error(t, dupErr, "Duplicate primary key insert must error")

	assert.True(t, IsUniqueViolation(dupErr),
		"IsUniqueViolation must recognize PostgreSQL SQLSTATE 23505 through the framework wrap chain")
	assert.False(t, IsForeignKeyViolation(dupErr),
		"A unique violation must not be classified as a foreign-key violation")

	name, ok := ConstraintName(dupErr)
	assert.True(t, ok, "PostgreSQL must expose the violated constraint name")
	assert.NotEmpty(t, name, "Exposed constraint name must be non-empty")
}

// TestIsUniqueViolationOracleIntegration drives a real Oracle duplicate-key
// insert and asserts the classifiers recognize ORA-00001 end to end. Oracle
// does not expose a constraint name, so ConstraintName must report ok=false.
func TestIsUniqueViolationOracleIntegration(t *testing.T) {
	// Context with timeout sized for Oracle's slower startup, mirroring the
	// database/oracle harness.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	// StartOracleContainer calls t.Skip internally when Docker is unavailable,
	// so this test is skipped gracefully off a Docker host.
	oraContainer, err := containers.StartOracleContainer(ctx, t, nil)
	require.NoError(t, err, "Failed to start Oracle container")
	oraContainer.WithCleanup(t)
	log := dbtestlog.NewDisabledTestLogger()

	// Build a database.Interface via the database-package factory (NewConnection)
	// from the container's connection details. Type must be set so the factory
	// dispatches to the Oracle driver; the service name carries the PDB.
	cfg := &config.DatabaseConfig{
		Type:     Oracle,
		Host:     oraContainer.Host(),
		Port:     oraContainer.Port(),
		Username: oraContainer.Username(),
		Password: oraContainer.Password(),
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{
				Name: oraContainer.Database(),
			},
		},
		Pool: defaultPoolConfig(),
	}

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err, "Failed to create Oracle connection")
	t.Cleanup(func() {
		_ = conn.Close()
	})

	require.NoError(t, conn.Health(ctx), "Failed to ping Oracle")

	// Table with a PRIMARY KEY so a duplicate insert raises ORA-00001.
	_, err = conn.Exec(ctx, "CREATE TABLE test_unique_ora (id NUMBER PRIMARY KEY, name VARCHAR2(100))")
	require.NoError(t, err, uniqueShouldCreateTableMsg)

	_, err = conn.Exec(ctx, "INSERT INTO test_unique_ora (id, name) VALUES (:1, :2)", 1, "first")
	require.NoError(t, err, uniqueShouldInsertFirstMsg)

	// Re-insert the SAME primary key — this is the violation under test.
	_, dupErr := conn.Exec(ctx, "INSERT INTO test_unique_ora (id, name) VALUES (:1, :2)", 1, "duplicate")
	require.Error(t, dupErr, "Duplicate primary key insert must error")

	assert.True(t, IsUniqueViolation(dupErr),
		"IsUniqueViolation must recognize Oracle ORA-00001 through the framework wrap chain")
	assert.False(t, IsForeignKeyViolation(dupErr),
		"A unique violation must not be classified as a foreign-key violation")

	name, ok := ConstraintName(dupErr)
	assert.False(t, ok, "Oracle does not expose a constraint name on the driver error")
	assert.Empty(t, name, "Oracle constraint name must be empty when unavailable")
}
