//go:build integration

package postgresql

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/testing/containers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	shouldCreateTableMsg = "Should create test table"
)

// setupTestContainer starts a PostgreSQL testcontainer and returns the connection
// The container is automatically cleaned up when the test finishes
func setupTestContainer(t *testing.T) (*Connection, context.Context) {
	t.Helper()

	// Create context with timeout to prevent indefinite hangs
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)

	// Register cleanup to cancel context and close connection
	t.Cleanup(func() {
		cancel()
	})

	// Start PostgreSQL container with default configuration
	pgContainer := containers.MustStartPostgreSQLContainer(ctx, t, nil).WithCleanup(t)

	// Create logger for tests (disabled output)
	log := logger.New("disabled", true)

	// Create config using connection string from container
	cfg := &config.DatabaseConfig{
		ConnectionString: pgContainer.ConnectionString(),
		Pool: config.PoolConfig{
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
		},
	}

	// Create PostgreSQL connection
	conn, err := NewConnection(cfg, log)
	require.NoError(t, err, "Failed to create PostgreSQL connection")

	// Register cleanup to close connection before container terminates
	t.Cleanup(func() {
		if conn != nil {
			_ = conn.Close()
		}
	})

	// Verify connection works
	err = conn.Health(ctx)
	require.NoError(t, err, "Failed to ping PostgreSQL")

	return conn.(*Connection), ctx
}

// =============================================================================
// Connection Lifecycle Tests
// =============================================================================

func TestConnectionHealth(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	defer conn.Close()

	err := conn.Health(ctx)
	assert.NoError(t, err, "Health check should succeed")

	// Health check should work multiple times
	err = conn.Health(ctx)
	assert.NoError(t, err, "Repeated health check should succeed")
}

func TestConnectionStats(t *testing.T) {
	conn, _ := setupTestContainer(t)
	defer conn.Close()

	stats, err := conn.Stats()
	assert.NoError(t, err, "Stats retrieval should succeed")
	assert.NotNil(t, stats, "Stats should not be nil")

	// Verify expected stats keys
	assert.Contains(t, stats, "max_open_connections", "Stats should contain max_open_connections")
	assert.Contains(t, stats, "open_connections", "Stats should contain open_connections")
	assert.Contains(t, stats, "in_use", "Stats should contain in_use")
	assert.Contains(t, stats, "idle", "Stats should contain idle")
	assert.Contains(t, stats, "wait_count", "Stats should contain wait_count")
	assert.Contains(t, stats, "wait_duration", "Stats should contain wait_duration")

	// Verify expected values
	assert.Equal(t, 25, stats["max_open_connections"], "Max connections should match config")
	assert.GreaterOrEqual(t, stats["open_connections"].(int), 0, "Open connections should be non-negative")
}

func TestConnectionDatabaseType(t *testing.T) {
	conn, _ := setupTestContainer(t)
	defer conn.Close()

	dbType := conn.DatabaseType()
	assert.Equal(t, "postgresql", dbType, "Database type should be postgresql")
}

func TestConnectionClose(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	// Connection should work before close
	err := conn.Health(ctx)
	assert.NoError(t, err, "Health check should succeed before close")

	// Close connection
	err = conn.Close()
	assert.NoError(t, err, "Close should succeed")

	// Health check should fail after close
	err = conn.Health(ctx)
	assert.Error(t, err, "Health check should fail after close")
}

// =============================================================================
// Migration Table Tests
// =============================================================================

func TestConnectionCreateMigrationTableIntegration(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	defer conn.Close()

	// Create migration table
	err := conn.CreateMigrationTable(ctx)
	assert.NoError(t, err, "CreateMigrationTable should succeed")

	// Verify table exists by querying it
	rows, err := conn.Query(ctx, "SELECT COUNT(*) FROM "+conn.GetMigrationTable())
	require.NoError(t, err, "Should be able to query migration table")
	defer rows.Close()

	require.True(t, rows.Next(), "Should have at least one row")
	var count int
	require.NoError(t, rows.Scan(&count))
	assert.Equal(t, 0, count, "New migration table should be empty")

	// Verify index exists by querying pg_indexes
	indexRows, err := conn.Query(ctx, "SELECT indexname FROM pg_indexes WHERE tablename = $1", conn.GetMigrationTable())
	require.NoError(t, err, "Should be able to query pg_indexes")
	defer indexRows.Close()

	hasIndex := false
	expectedIndex := conn.GetMigrationTable() + "_s_idx"
	for indexRows.Next() {
		var indexName string
		require.NoError(t, indexRows.Scan(&indexName))
		if indexName == expectedIndex {
			hasIndex = true
			break
		}
	}
	assert.True(t, hasIndex, "Migration table should have success index")

	// Creating table again should be idempotent (no error)
	err = conn.CreateMigrationTable(ctx)
	assert.NoError(t, err, "CreateMigrationTable should be idempotent")
}

// =============================================================================
// Basic Operations Tests
// =============================================================================

func TestConnectionQueryOperations(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	defer conn.Close()

	// Create a test table
	_, err := conn.Exec(ctx, "CREATE TABLE test_query (id SERIAL PRIMARY KEY, name TEXT, value INT)")
	require.NoError(t, err, shouldCreateTableMsg)

	// Insert test data
	_, err = conn.Exec(ctx, "INSERT INTO test_query (name, value) VALUES ($1, $2), ($3, $4)",
		"first", 100, "second", 200)
	require.NoError(t, err, "Should insert test data")

	// Test Query
	rows, err := conn.Query(ctx, "SELECT name, value FROM test_query ORDER BY id")
	require.NoError(t, err, "Query should succeed")
	defer rows.Close()

	// Verify results
	var results []struct {
		name  string
		value int
	}
	for rows.Next() {
		var r struct {
			name  string
			value int
		}
		require.NoError(t, rows.Scan(&r.name, &r.value))
		results = append(results, r)
	}
	require.NoError(t, rows.Err())

	assert.Len(t, results, 2, "Should have 2 rows")
	assert.Equal(t, "first", results[0].name)
	assert.Equal(t, 100, results[0].value)
	assert.Equal(t, "second", results[1].name)
	assert.Equal(t, 200, results[1].value)

	// Test QueryRow
	row := conn.QueryRow(ctx, "SELECT name FROM test_query WHERE value = $1", 200)
	var name string
	require.NoError(t, row.Scan(&name))
	assert.Equal(t, "second", name)
}

func TestConnectionPrepareStatement(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	defer conn.Close()

	// Create a test table
	_, err := conn.Exec(ctx, "CREATE TABLE test_prepare (id SERIAL PRIMARY KEY, name TEXT)")
	require.NoError(t, err, shouldCreateTableMsg)

	// Prepare statement
	stmt, err := conn.Prepare(ctx, "INSERT INTO test_prepare (name) VALUES ($1) RETURNING id")
	require.NoError(t, err, "Prepare should succeed")
	defer stmt.Close()

	// Execute prepared statement multiple times
	for _, name := range []string{"alice", "bob", "charlie"} {
		rows, err := stmt.Query(ctx, name)
		require.NoError(t, err, "Prepared statement execution should succeed")
		require.True(t, rows.Next(), "Should return inserted ID")
		var id int
		require.NoError(t, rows.Scan(&id))
		assert.Greater(t, id, 0, "Inserted ID should be positive")
		rows.Close()
	}

	// Verify all inserts
	row := conn.QueryRow(ctx, "SELECT COUNT(*) FROM test_prepare")
	var count int
	require.NoError(t, row.Scan(&count))
	assert.Equal(t, 3, count, "Should have 3 rows")
}

// =============================================================================
// Transaction Tests
// =============================================================================

func TestConnectionTransactionCommit(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	defer conn.Close()

	// Create test table
	_, err := conn.Exec(ctx, "CREATE TABLE test_tx_commit (id SERIAL PRIMARY KEY, value INT)")
	require.NoError(t, err, shouldCreateTableMsg)

	// Begin transaction
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, "Begin transaction should succeed")

	// Insert data in transaction
	_, err = tx.Exec(ctx, "INSERT INTO test_tx_commit (value) VALUES ($1)", 42)
	require.NoError(t, err, "Insert in transaction should succeed")

	// Commit transaction
	err = tx.Commit()
	require.NoError(t, err, "Commit should succeed")

	// Verify data is visible after commit
	row := conn.QueryRow(ctx, "SELECT value FROM test_tx_commit")
	var value int
	require.NoError(t, row.Scan(&value))
	assert.Equal(t, 42, value, "Committed data should be visible")
}

func TestConnectionTransactionRollback(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	defer conn.Close()

	// Create test table
	_, err := conn.Exec(ctx, "CREATE TABLE test_tx_rollback (id SERIAL PRIMARY KEY, value INT)")
	require.NoError(t, err, shouldCreateTableMsg)

	// Begin transaction
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, "Begin transaction should succeed")

	// Insert data in transaction
	_, err = tx.Exec(ctx, "INSERT INTO test_tx_rollback (value) VALUES ($1)", 99)
	require.NoError(t, err, "Insert in transaction should succeed")

	// Rollback transaction
	err = tx.Rollback()
	require.NoError(t, err, "Rollback should succeed")

	// Verify data is NOT visible after rollback
	row := conn.QueryRow(ctx, "SELECT COUNT(*) FROM test_tx_rollback")
	var count int
	require.NoError(t, row.Scan(&count))
	assert.Equal(t, 0, count, "Rolled back data should not be visible")
}

func TestConnectionTransactionIsolation(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	defer conn.Close()

	// Create test table with initial data
	_, err := conn.Exec(ctx, "CREATE TABLE test_tx_isolation (id SERIAL PRIMARY KEY, value INT)")
	require.NoError(t, err, shouldCreateTableMsg)
	_, err = conn.Exec(ctx, "INSERT INTO test_tx_isolation (value) VALUES ($1)", 10)
	require.NoError(t, err, "Should insert initial data")

	// Begin transaction with READ COMMITTED isolation
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	require.NoError(t, err, "BeginTx should succeed")

	// Read initial value in transaction
	row := tx.QueryRow(ctx, "SELECT value FROM test_tx_isolation WHERE id = $1", 1)
	var value1 int
	require.NoError(t, row.Scan(&value1))
	assert.Equal(t, 10, value1, "Initial read should see original value")

	// Update value outside transaction (in another implicit transaction)
	_, err = conn.Exec(ctx, "UPDATE test_tx_isolation SET value = $1 WHERE id = $2", 20, 1)
	require.NoError(t, err, "External update should succeed")

	// Read again in transaction - should see updated value (READ COMMITTED)
	row = tx.QueryRow(ctx, "SELECT value FROM test_tx_isolation WHERE id = $1", 1)
	var value2 int
	require.NoError(t, row.Scan(&value2))
	assert.Equal(t, 20, value2, "READ COMMITTED should see external update")

	// Rollback transaction
	err = tx.Rollback()
	require.NoError(t, err, "Rollback should succeed")
}

// =============================================================================
// Connection Pool Tests
// =============================================================================

func TestConnectionPoolConfiguration(t *testing.T) {
	// Create context with timeout to prevent indefinite hangs
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Start container
	pgContainer := containers.MustStartPostgreSQLContainer(ctx, t, nil).WithCleanup(t)
	log := logger.New("disabled", true)

	// Create connection with specific pool configuration
	cfg := &config.DatabaseConfig{
		ConnectionString: pgContainer.ConnectionString(),
		Pool: config.PoolConfig{
			Max: config.PoolMaxConfig{
				Connections: 5,
			},
			Idle: config.PoolIdleConfig{
				Connections: 2,
				Time:        10 * time.Second,
			},
			Lifetime: config.LifetimeConfig{
				Max: 30 * time.Second,
			},
		},
	}

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	defer conn.Close()

	// Verify pool configuration via stats
	stats, err := conn.Stats()
	require.NoError(t, err)

	assert.Equal(t, 5, stats["max_open_connections"], "Max connections should match config")
	assert.Equal(t, 2, stats["max_idle_connections"], "Max idle connections should match config")
}
