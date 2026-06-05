//go:build integration

package inbox

import (
	"context"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	testconsts "github.com/gaborage/go-bricks/testing"
	"github.com/gaborage/go-bricks/testing/containers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests prove the inbox ledger Store works end to end against REAL
// PostgreSQL and Oracle containers. The connection is built through the
// database-package factory (NewConnection -> database.Interface), so CreateTable
// DDL, MarkProcessed (insert + duplicate detection), and DeleteProcessed travel
// the full framework wrap chain on each vendor.
//
// The load-bearing case is the duplicate MarkProcessed: PostgreSQL relies on
// INSERT ... ON CONFLICT DO NOTHING returning zero rows (no error, tx survives),
// while Oracle catches ORA-00001 via database.IsUniqueViolation (statement-level,
// tx survives). Both vendors must leave the transaction usable after a duplicate,
// which the test proves by committing — and on Oracle by performing a subsequent
// successful insert within the SAME transaction.

const (
	inboxTableName       = "inbox_it"
	inboxTenantID        = "t1"
	inboxShouldCreateMsg = "Should create inbox table"
	inboxShouldBeginMsg  = "Should begin transaction"
	inboxShouldCommitMsg = "Should commit transaction"
)

// newDisabledTestLogger mirrors database/internal/dbtestlog.NewDisabledTestLogger
// (which is internal to the database/ subtree and not importable here): a
// disabled logger reduces noise in integration tests that verify functionality
// without needing logs.
func newDisabledTestLogger() logger.Logger {
	return logger.New(testconsts.TestLoggerLevelDisabled, true)
}

// inboxPoolConfig mirrors the pool sizing used by the existing integration
// harnesses (database/errors_integration_test.go defaultPoolConfig).
func inboxPoolConfig() config.PoolConfig {
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

// TestInboxStorePostgresIntegration exercises the PostgreSQL inbox store against
// a real container: CreateTable DDL, first-insert, duplicate (ON CONFLICT DO
// NOTHING returns inserted=false with the tx still usable), a second distinct
// event, and retention DeleteProcessed.
func TestInboxStorePostgresIntegration(t *testing.T) {
	// Context with timeout to prevent indefinite hangs, mirroring the
	// database/postgresql setupTestContainer harness.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	// MustStartPostgreSQLContainer auto-skips (t.Skip) when Docker is unavailable.
	pgContainer := containers.MustStartPostgreSQLContainer(ctx, t, nil).WithCleanup(t)
	log := newDisabledTestLogger()

	// Build a database.Interface via the database-package factory (NewConnection).
	// Type must be set so the factory dispatches to the PostgreSQL driver.
	cfg := &config.DatabaseConfig{
		Type:             database.PostgreSQL,
		ConnectionString: pgContainer.ConnectionString(),
		Pool:             inboxPoolConfig(),
	}

	conn, err := database.NewConnection(cfg, log)
	require.NoError(t, err, "Failed to create PostgreSQL connection")
	t.Cleanup(func() {
		_ = conn.Close()
	})

	require.NoError(t, conn.Health(ctx), "Failed to ping PostgreSQL")

	store, err := NewPostgresStore(inboxTableName)
	require.NoError(t, err, "Failed to build PostgreSQL inbox store")

	// CreateTable proves the DDL (PK + processed_at index) is valid on real PG.
	require.NoError(t, store.CreateTable(ctx, conn), inboxShouldCreateMsg)
	t.Cleanup(func() {
		// Best-effort drop so reruns against a reused volume stay clean.
		_, _ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+inboxTableName)
	})

	now := time.Now()

	// First insert: inserted=true.
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, inboxShouldBeginMsg)
	inserted, err := store.MarkProcessed(ctx, tx, Record{TenantID: inboxTenantID, EventID: "evt-1", ProcessedAt: now})
	require.NoError(t, err, "First MarkProcessed should not error")
	assert.True(t, inserted, "First (tenant_id, event_id) must be inserted")
	require.NoError(t, tx.Commit(ctx), inboxShouldCommitMsg)

	// Duplicate insert in a NEW transaction: inserted=false, no error, and the
	// transaction must remain usable afterward. PG: ON CONFLICT returns 0 rows.
	dupTx, err := conn.Begin(ctx)
	require.NoError(t, err, inboxShouldBeginMsg)
	inserted, err = store.MarkProcessed(ctx, dupTx, Record{TenantID: inboxTenantID, EventID: "evt-1", ProcessedAt: now})
	require.NoError(t, err, "Duplicate MarkProcessed must NOT error on PostgreSQL")
	assert.False(t, inserted, "Duplicate (tenant_id, event_id) must report inserted=false")
	// The tx survives the duplicate: a distinct event inserts within the SAME tx.
	inserted, err = store.MarkProcessed(ctx, dupTx, Record{TenantID: inboxTenantID, EventID: "evt-2", ProcessedAt: now})
	require.NoError(t, err, "Distinct MarkProcessed after duplicate must not error")
	assert.True(t, inserted, "Distinct event after a duplicate must be inserted in the same tx")
	require.NoError(t, dupTx.Commit(ctx), inboxShouldCommitMsg)

	// DeleteProcessed removes both rows processed before now+1h.
	deleted, err := store.DeleteProcessed(ctx, conn, now.Add(time.Hour))
	require.NoError(t, err, "DeleteProcessed should not error")
	assert.GreaterOrEqual(t, deleted, int64(2), "DeleteProcessed must remove at least the two inserted rows")
}

// TestInboxStoreOracleIntegration exercises the Oracle inbox store against a real
// container. The load-bearing assertion is the duplicate path: Oracle has no ON
// CONFLICT, so MarkProcessed catches ORA-00001 via database.IsUniqueViolation;
// because the violation is statement-level the SAME transaction must still be
// usable, which the test proves with a subsequent successful insert + commit.
func TestInboxStoreOracleIntegration(t *testing.T) {
	// Context with timeout sized for Oracle's slower startup, mirroring the
	// database/oracle harness.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	// StartOracleContainer calls t.Skip internally when Docker is unavailable,
	// so this test is skipped gracefully off a Docker host.
	oraContainer, err := containers.StartOracleContainer(ctx, t, nil)
	require.NoError(t, err, "Failed to start Oracle container")
	oraContainer.WithCleanup(t)
	log := newDisabledTestLogger()

	// Build a database.Interface via the database-package factory (NewConnection)
	// from the container's connection details. Type must be set so the factory
	// dispatches to the Oracle driver; the service name carries the PDB.
	cfg := &config.DatabaseConfig{
		Type:     database.Oracle,
		Host:     oraContainer.Host(),
		Port:     oraContainer.Port(),
		Username: oraContainer.Username(),
		Password: oraContainer.Password(),
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{
				Name: oraContainer.Database(),
			},
		},
		Pool: inboxPoolConfig(),
	}

	conn, err := database.NewConnection(cfg, log)
	require.NoError(t, err, "Failed to create Oracle connection")
	t.Cleanup(func() {
		_ = conn.Close()
	})

	require.NoError(t, conn.Health(ctx), "Failed to ping Oracle")

	store, err := NewOracleStore(inboxTableName)
	require.NoError(t, err, "Failed to build Oracle inbox store")

	// CreateTable proves the Oracle DDL (named PK constraint + function-free
	// index) is valid on the real vendor.
	require.NoError(t, store.CreateTable(ctx, conn), inboxShouldCreateMsg)
	t.Cleanup(func() {
		// Best-effort drop; Oracle has no DROP TABLE IF EXISTS, so ignore the
		// ORA-00942 that occurs if the table was never created.
		_, _ = conn.Exec(context.Background(), "DROP TABLE "+inboxTableName)
	})

	now := time.Now()

	// First insert: inserted=true.
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, inboxShouldBeginMsg)
	inserted, err := store.MarkProcessed(ctx, tx, Record{TenantID: inboxTenantID, EventID: "evt-1", ProcessedAt: now})
	require.NoError(t, err, "First MarkProcessed should not error")
	assert.True(t, inserted, "First (tenant_id, event_id) must be inserted")
	require.NoError(t, tx.Commit(ctx), inboxShouldCommitMsg)

	// Duplicate insert in a NEW transaction: ORA-00001 is caught and returned as
	// inserted=false with no error. The CRITICAL property is that the SAME tx
	// remains usable, proven by inserting a distinct event afterward + commit.
	dupTx, err := conn.Begin(ctx)
	require.NoError(t, err, inboxShouldBeginMsg)
	inserted, err = store.MarkProcessed(ctx, dupTx, Record{TenantID: inboxTenantID, EventID: "evt-1", ProcessedAt: now})
	require.NoError(t, err, "Duplicate MarkProcessed must catch ORA-00001 and NOT error on Oracle")
	assert.False(t, inserted, "Duplicate (tenant_id, event_id) must report inserted=false")
	// The tx survives ORA-00001: a distinct event inserts within the SAME tx.
	inserted, err = store.MarkProcessed(ctx, dupTx, Record{TenantID: inboxTenantID, EventID: "evt-2", ProcessedAt: now})
	require.NoError(t, err, "Distinct MarkProcessed after ORA-00001 must not error (tx must survive)")
	assert.True(t, inserted, "Distinct event after a duplicate must be inserted in the same tx")
	require.NoError(t, dupTx.Commit(ctx), inboxShouldCommitMsg)

	// DeleteProcessed removes both rows processed before now+1h.
	deleted, err := store.DeleteProcessed(ctx, conn, now.Add(time.Hour))
	require.NoError(t, err, "DeleteProcessed should not error")
	assert.GreaterOrEqual(t, deleted, int64(2), "DeleteProcessed must remove at least the two inserted rows")
}
