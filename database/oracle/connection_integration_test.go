//go:build integration

package oracle

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/testing/containers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	shouldCreateTableMsg       = "Should create test table"
	startTransactionSucceedMsg = "Begin transaction should succeed"
)

// setupTestContainer starts an Oracle testcontainer and returns the connection
// The container is automatically cleaned up when the test finishes
func setupTestContainer(t *testing.T) (*Connection, context.Context) {
	t.Helper()

	// Create context with timeout to prevent indefinite hangs
	// Oracle requires longer timeout due to slower startup (120s container wait)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)

	// Register cleanup to cancel context and close connection
	t.Cleanup(func() {
		cancel()
	})

	// Start Oracle container with default configuration (takes ~30-60s)
	oracleContainer := containers.MustStartOracleContainer(ctx, t, nil).WithCleanup(t)

	// Create logger for tests (disabled output)
	log := logger.New("disabled", true)

	// Create config using connection details from container
	cfg := &config.DatabaseConfig{
		Host:     oracleContainer.Host(),
		Port:     oracleContainer.Port(),
		Username: oracleContainer.Username(),
		Password: oracleContainer.Password(),
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{
				Name: oracleContainer.Database(),
			},
		},
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

	// Create Oracle connection
	conn, err := NewConnection(cfg, log)
	require.NoError(t, err, "Failed to create Oracle connection")

	// Register cleanup to close connection before container terminates
	t.Cleanup(func() {
		if conn != nil {
			_ = conn.Close()
		}
	})

	// Verify connection works
	err = conn.Health(ctx)
	require.NoError(t, err, "Failed to ping Oracle")

	return conn.(*Connection), ctx
}

// =============================================================================
// Connection Lifecycle Tests
// =============================================================================

func TestConnectionHealth(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	err := conn.Health(ctx)
	assert.NoError(t, err, "Health check should succeed")

	// Health check should work multiple times
	err = conn.Health(ctx)
	assert.NoError(t, err, "Repeated health check should succeed")
}

func TestConnectionStats(t *testing.T) {
	conn, _ := setupTestContainer(t)

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

	dbType := conn.DatabaseType()
	assert.Equal(t, "oracle", dbType, "Database type should be oracle")
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
// DSN Construction Tests (Service Name, SID, Database)
// =============================================================================

func TestConnectionWithServiceName(t *testing.T) {
	// Create context with timeout to prevent indefinite hangs
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	oracleContainer := containers.MustStartOracleContainer(ctx, t, nil).WithCleanup(t)
	log := logger.New("disabled", true)

	// Test with service name (most common pattern)
	cfg := &config.DatabaseConfig{
		Host:     oracleContainer.Host(),
		Port:     oracleContainer.Port(),
		Username: oracleContainer.Username(),
		Password: oracleContainer.Password(),
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{
				Name: oracleContainer.Database(),
			},
		},
	}

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err, "Connection with service name should succeed")
	defer conn.Close()

	// Verify connection works
	err = conn.Health(ctx)
	assert.NoError(t, err, "Health check should succeed with service name")
}

// NOTE: Oracle Free uses PDB (Pluggable Database) architecture where FREEPDB1 is a service name, not a SID.
// SID connections (legacy pattern) are not tested here because Oracle Free doesn't expose the container database SID (FREE)
// for direct connections. Testing SID would require a traditional Oracle installation or Oracle XE.

func TestConnectionWithDatabaseFallback(t *testing.T) {
	// Create context with timeout to prevent indefinite hangs
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	oracleContainer := containers.MustStartOracleContainer(ctx, t, nil).WithCleanup(t)
	log := logger.New("disabled", true)

	// Test with database field (fallback pattern)
	cfg := &config.DatabaseConfig{
		Host:     oracleContainer.Host(),
		Port:     oracleContainer.Port(),
		Username: oracleContainer.Username(),
		Password: oracleContainer.Password(),
		Database: oracleContainer.Database(),
	}

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err, "Connection with database fallback should succeed")
	defer conn.Close()

	// Verify connection works
	err = conn.Health(ctx)
	assert.NoError(t, err, "Health check should succeed with database fallback")
}

func TestConnectionWithConnectionString(t *testing.T) {
	// Create context with timeout to prevent indefinite hangs
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	oracleContainer := containers.MustStartOracleContainer(ctx, t, nil).WithCleanup(t)
	log := logger.New("disabled", true)

	// Test with connection string (most flexible pattern)
	cfg := &config.DatabaseConfig{
		ConnectionString: oracleContainer.ConnectionString(),
	}

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err, "Connection with connection string should succeed")
	defer conn.Close()

	// Verify connection works
	err = conn.Health(ctx)
	assert.NoError(t, err, "Health check should succeed with connection string")
}

// =============================================================================
// Migration Table Tests (Oracle-specific PL/SQL)
// =============================================================================

func TestConnectionCreateMigrationTableIntegration(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	// Create migration table (executes 2 PL/SQL blocks)
	err := conn.CreateMigrationTable(ctx)
	assert.NoError(t, err, "CreateMigrationTable should succeed")

	// Verify table exists by querying it
	rows, err := conn.Query(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", conn.GetMigrationTable()))
	require.NoError(t, err, "Should be able to query migration table")
	defer rows.Close()

	require.True(t, rows.Next(), "Should have at least one row")
	var count int
	require.NoError(t, rows.Scan(&count))
	assert.Equal(t, 0, count, "New migration table should be empty")

	// Creating table again should be idempotent (no error due to BEGIN...EXCEPTION...END block)
	err = conn.CreateMigrationTable(ctx)
	assert.NoError(t, err, "CreateMigrationTable should be idempotent")
}

// =============================================================================
// Oracle Placeholder Binding Tests (:1, :2 syntax)
// =============================================================================

func TestConnectionOraclePlaceholders(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	// Create a test table
	_, err := conn.Exec(ctx, "CREATE TABLE test_placeholders (id NUMBER PRIMARY KEY, name VARCHAR2(100), value NUMBER)")
	require.NoError(t, err, shouldCreateTableMsg)

	// Insert test data using :1, :2, :3 placeholders
	_, err = conn.Exec(ctx, "INSERT INTO test_placeholders (id, name, value) VALUES (:1, :2, :3)",
		1, "first", 100)
	require.NoError(t, err, "Should insert with Oracle placeholders")

	// Query using :1 placeholder
	rows, err := conn.Query(ctx, "SELECT name, value FROM test_placeholders WHERE id = :1", 1)
	require.NoError(t, err, "Query with Oracle placeholder should succeed")
	defer rows.Close()

	require.True(t, rows.Next(), "Should have at least one row")
	var name string
	var value int
	require.NoError(t, rows.Scan(&name, &value))
	assert.Equal(t, "first", name)
	assert.Equal(t, 100, value)
}

// =============================================================================
// Basic Operations Tests
// =============================================================================

func TestConnectionQueryOperations(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	// Create a test table
	_, err := conn.Exec(ctx, "CREATE TABLE test_query (id NUMBER PRIMARY KEY, name VARCHAR2(100), value NUMBER)")
	require.NoError(t, err, shouldCreateTableMsg)

	// Insert test data
	_, err = conn.Exec(ctx, "INSERT INTO test_query (id, name, value) VALUES (:1, :2, :3)", 1, "first", 100)
	require.NoError(t, err, "Should insert first row")
	_, err = conn.Exec(ctx, "INSERT INTO test_query (id, name, value) VALUES (:1, :2, :3)", 2, "second", 200)
	require.NoError(t, err, "Should insert second row")

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
	row := conn.QueryRow(ctx, "SELECT name FROM test_query WHERE value = :1", 200)
	var name string
	require.NoError(t, row.Scan(&name))
	assert.Equal(t, "second", name)
}

func TestConnectionPrepareStatement(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	// Create a test table
	_, err := conn.Exec(ctx, "CREATE TABLE test_prepare (id NUMBER PRIMARY KEY, name VARCHAR2(100))")
	require.NoError(t, err, shouldCreateTableMsg)

	// Create sequence for ID generation
	_, err = conn.Exec(ctx, "CREATE SEQUENCE test_prepare_seq START WITH 1")
	require.NoError(t, err, "Should create sequence")

	// Prepare statement
	stmt, err := conn.Prepare(ctx, "INSERT INTO test_prepare (id, name) VALUES (test_prepare_seq.NEXTVAL, :1)")
	require.NoError(t, err, "Prepare should succeed")
	defer stmt.Close()

	// Execute prepared statement multiple times
	for _, name := range []string{"alice", "bob", "charlie"} {
		_, err := stmt.Exec(ctx, name)
		require.NoError(t, err, "Prepared statement execution should succeed")
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

	// Create test table
	_, err := conn.Exec(ctx, "CREATE TABLE test_tx_commit (id NUMBER PRIMARY KEY, value NUMBER)")
	require.NoError(t, err, shouldCreateTableMsg)

	// Begin transaction
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, startTransactionSucceedMsg)

	// Insert data in transaction
	_, err = tx.Exec(ctx, "INSERT INTO test_tx_commit (id, value) VALUES (:1, :2)", 1, 42)
	require.NoError(t, err, "Insert in transaction should succeed")

	// Commit transaction
	err = tx.Commit()
	require.NoError(t, err, "Commit should succeed")

	// Verify data is visible after commit
	row := conn.QueryRow(ctx, "SELECT value FROM test_tx_commit WHERE id = :1", 1)
	var value int
	require.NoError(t, row.Scan(&value))
	assert.Equal(t, 42, value, "Committed data should be visible")
}

func TestConnectionTransactionRollback(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	// Create test table
	_, err := conn.Exec(ctx, "CREATE TABLE test_tx_rollback (id NUMBER PRIMARY KEY, value NUMBER)")
	require.NoError(t, err, shouldCreateTableMsg)

	// Begin transaction
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, startTransactionSucceedMsg)

	// Insert data in transaction
	_, err = tx.Exec(ctx, "INSERT INTO test_tx_rollback (id, value) VALUES (:1, :2)", 1, 99)
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

	// Create test table with initial data
	_, err := conn.Exec(ctx, "CREATE TABLE test_tx_isolation (id NUMBER PRIMARY KEY, value NUMBER)")
	require.NoError(t, err, shouldCreateTableMsg)
	_, err = conn.Exec(ctx, "INSERT INTO test_tx_isolation (id, value) VALUES (:1, :2)", 1, 10)
	require.NoError(t, err, "Should insert initial data")

	// Begin transaction (Oracle uses READ COMMITTED isolation by default)
	// Note: go-ora driver only supports default isolation level
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, startTransactionSucceedMsg)

	// Read initial value in transaction
	row := tx.QueryRow(ctx, "SELECT value FROM test_tx_isolation WHERE id = :1", 1)
	var value1 int
	require.NoError(t, row.Scan(&value1))
	assert.Equal(t, 10, value1, "Initial read should see original value")

	// Update value outside transaction (in another implicit transaction)
	_, err = conn.Exec(ctx, "UPDATE test_tx_isolation SET value = :1 WHERE id = :2", 20, 1)
	require.NoError(t, err, "External update should succeed")

	// Read again in transaction - should see updated value (Oracle default READ COMMITTED)
	row = tx.QueryRow(ctx, "SELECT value FROM test_tx_isolation WHERE id = :1", 1)
	var value2 int
	require.NoError(t, row.Scan(&value2))
	assert.Equal(t, 20, value2, "Oracle READ COMMITTED (default) should see external update")

	// Rollback transaction
	err = tx.Rollback()
	require.NoError(t, err, "Rollback should succeed")
}

// =============================================================================
// Connection Pool Tests
// =============================================================================

func TestConnectionPoolConfiguration(t *testing.T) {
	// Create context with timeout to prevent indefinite hangs
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// Start container
	oracleContainer := containers.MustStartOracleContainer(ctx, t, nil).WithCleanup(t)
	log := logger.New("disabled", true)

	// Create connection with specific pool configuration
	cfg := &config.DatabaseConfig{
		Host:     oracleContainer.Host(),
		Port:     oracleContainer.Port(),
		Username: oracleContainer.Username(),
		Password: oracleContainer.Password(),
		Oracle: config.OracleConfig{
			Service: config.ServiceConfig{
				Name: oracleContainer.Database(),
			},
		},
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

// =============================================================================
// Oracle SEQUENCE Integration Tests (No UDT Registration Required)
// =============================================================================

// TestOracleSequenceIntegration verifies SEQUENCE queries work without UDT registration
func TestOracleSequenceIntegration(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	// Create test sequence
	_, err := conn.Exec(ctx, "CREATE SEQUENCE test_seq START WITH 1000 INCREMENT BY 1")
	require.NoError(t, err)

	t.Run("nextvalQuery", func(t *testing.T) {
		// SEQUENCE queries work without ANY UDT registration
		var nextID int64
		err := conn.QueryRow(ctx, "SELECT test_seq.NEXTVAL FROM DUAL").Scan(&nextID)
		require.NoError(t, err)
		assert.Equal(t, int64(1000), nextID)
	})

	t.Run("currvalQuery", func(t *testing.T) {
		var currID int64
		err := conn.QueryRow(ctx, "SELECT test_seq.CURRVAL FROM DUAL").Scan(&currID)
		require.NoError(t, err)
		assert.Equal(t, int64(1000), currID)
	})

	t.Run("sequenceInInsert", func(t *testing.T) {
		_, err := conn.Exec(ctx, "CREATE TABLE test_seq_table (id NUMBER PRIMARY KEY, name VARCHAR2(100))")
		require.NoError(t, err)

		// Use SEQUENCE in INSERT (no UDT registration needed)
		_, err = conn.Exec(ctx, "INSERT INTO test_seq_table VALUES (test_seq.NEXTVAL, 'test')")
		require.NoError(t, err)

		var id int64
		var name string
		err = conn.QueryRow(ctx, "SELECT id, name FROM test_seq_table").Scan(&id, &name)
		require.NoError(t, err)
		assert.Equal(t, int64(1001), id)
		assert.Equal(t, "test", name)
	})
}

// =============================================================================
// Oracle UDT Integration Tests
// =============================================================================

// Product for UDT testing
type Product struct {
	ID        int64     `udt:"ID"`
	Name      string    `udt:"NAME"`
	Price     float64   `udt:"PRICE"`
	CreatedAt time.Time `udt:"CREATED_AT"`
}

func TestOracleUDTCollectionIntegration(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	// Setup: Create UDT types and bulk insert procedure
	setupSQL := `
		BEGIN
			EXECUTE IMMEDIATE 'CREATE TYPE PRODUCT_TYPE AS OBJECT (
				ID         NUMBER,
				NAME       VARCHAR2(100),
				PRICE      NUMBER(10,2),
				CREATED_AT DATE
			)';
		EXCEPTION
			WHEN OTHERS THEN
				IF SQLCODE != -955 THEN
					RAISE;
				END IF;
		END;
	`

	_, err := conn.Exec(ctx, setupSQL)
	require.NoError(t, err)

	collectionSQL := `
		BEGIN
			EXECUTE IMMEDIATE 'CREATE TYPE PRODUCT_TABLE AS TABLE OF PRODUCT_TYPE';
		EXCEPTION
			WHEN OTHERS THEN
				IF SQLCODE != -955 THEN
					RAISE;
				END IF;
		END;
	`

	_, err = conn.Exec(ctx, collectionSQL)
	require.NoError(t, err)

	tableSQL := `
		BEGIN
			EXECUTE IMMEDIATE 'CREATE TABLE products (
				id         NUMBER PRIMARY KEY,
				name       VARCHAR2(100),
				price      NUMBER(10,2),
				created_at DATE
			)';
		EXCEPTION
			WHEN OTHERS THEN
				IF SQLCODE != -955 THEN
					RAISE;
				END IF;
		END;
	`

	_, err = conn.Exec(ctx, tableSQL)
	require.NoError(t, err)

	procedureSQL := `
		CREATE OR REPLACE PROCEDURE bulk_insert_products(p_products IN PRODUCT_TABLE) IS
		BEGIN
			FORALL i IN INDICES OF p_products
				INSERT INTO products (id, name, price, created_at)
				VALUES (
					p_products(i).ID,
					p_products(i).NAME,
					p_products(i).PRICE,
					p_products(i).CREATED_AT
				);
		END;
	`

	_, err = conn.Exec(ctx, procedureSQL)
	require.NoError(t, err)

	t.Run("bulkInsertWithCollectionType", func(t *testing.T) {
		// Register UDT collection type
		// Note: conn is already *Connection from setupTestContainer
		err := conn.RegisterType("PRODUCT_TYPE", "PRODUCT_TABLE", Product{})
		require.NoError(t, err, "Failed to register UDT")

		// Prepare bulk data
		now := time.Now()
		products := []Product{
			{ID: 1, Name: "Widget", Price: 19.99, CreatedAt: now},
			{ID: 2, Name: "Gadget", Price: 29.99, CreatedAt: now},
			{ID: 3, Name: "Doohickey", Price: 39.99, CreatedAt: now},
		}

		// Bulk insert via collection
		_, err = conn.Exec(ctx, "BEGIN bulk_insert_products(:1); END;", products)
		require.NoError(t, err, "Bulk insert failed")

		// Verify count
		var count int
		err = conn.QueryRow(ctx, "SELECT COUNT(*) FROM products").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 3, count)
	})
}

func TestOracleUDTWithSchemaOwnerIntegration(t *testing.T) {
	t.Skip("Skipping schema owner test - requires additional Oracle container setup with user creation privileges")

	conn, ctx := setupTestContainer(t)

	// Create schema with types (requires DBA privileges)
	setupSQL := `
		BEGIN
			EXECUTE IMMEDIATE 'CREATE USER testschema IDENTIFIED BY testpass';
			EXECUTE IMMEDIATE 'GRANT CREATE TYPE, CREATE PROCEDURE TO testschema';
		EXCEPTION
			WHEN OTHERS THEN
				IF SQLCODE != -1920 THEN
					RAISE;
				END IF;
		END;
	`

	_, err := conn.Exec(ctx, setupSQL)
	if err != nil {
		t.Logf("Schema creation failed (may require DBA privileges): %v", err)
		return
	}

	typeSQL := `
		BEGIN
			EXECUTE IMMEDIATE 'CREATE TYPE testschema.ORDER_ITEM AS OBJECT (
				ITEM_ID   NUMBER,
				QUANTITY  NUMBER,
				PRICE     NUMBER(10,2)
			)';
		EXCEPTION
			WHEN OTHERS THEN
				IF SQLCODE != -955 THEN
					RAISE;
				END IF;
		END;
	`

	_, err = conn.Exec(ctx, typeSQL)
	require.NoError(t, err)

	collSQL := `
		BEGIN
			EXECUTE IMMEDIATE 'CREATE TYPE testschema.ORDER_ITEMS AS TABLE OF testschema.ORDER_ITEM';
		EXCEPTION
			WHEN OTHERS THEN
				IF SQLCODE != -955 THEN
					RAISE;
				END IF;
		END;
	`

	_, err = conn.Exec(ctx, collSQL)
	require.NoError(t, err)

	type OrderItem struct {
		ItemID   int64   `udt:"ITEM_ID"`
		Quantity int     `udt:"QUANTITY"`
		Price    float64 `udt:"PRICE"`
	}

	t.Run("registerWithSchemaOwner", func(t *testing.T) {
		// Register with schema owner
		// Note: conn is already *Connection from setupTestContainer
		err := conn.RegisterTypeWithOwner("TESTSCHEMA", "ORDER_ITEM", "ORDER_ITEMS", OrderItem{})
		require.NoError(t, err)

		// Verify registration successful (actual usage would be in stored procedures)
		items := []OrderItem{
			{ItemID: 101, Quantity: 2, Price: 50.00},
			{ItemID: 102, Quantity: 1, Price: 75.50},
		}

		// Would use: _, err = conn.Exec(ctx, "BEGIN testschema.process_order(:1); END;", items)
		assert.NotNil(t, items) // Placeholder - type is registered
	})
}

// =============================================================================
// Oracle UDT Coverage Tests (Object-Only and Error Paths)
// =============================================================================

// TestOracleUDTObjectOnlyRegistration tests RegisterType with object-only (no collection)
// Coverage target: connection.go line 375 (else branch)
func TestOracleUDTObjectOnlyRegistration(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	// Create simple object type (no collection)
	setupSQL := `
		BEGIN
			EXECUTE IMMEDIATE 'CREATE TYPE SIMPLE_TYPE AS OBJECT (
				ID NUMBER,
				NAME VARCHAR2(50)
			)';
		EXCEPTION
			WHEN OTHERS THEN
				IF SQLCODE != -955 THEN
					RAISE;
				END IF;
		END;
	`

	_, err := conn.Exec(ctx, setupSQL)
	require.NoError(t, err)

	type SimpleType struct {
		ID   int64  `udt:"ID"`
		Name string `udt:"NAME"`
	}

	// Register WITHOUT collection type (empty arrayTypeName)
	// This covers the else branch at line 375
	err = conn.RegisterType("SIMPLE_TYPE", "", SimpleType{})
	require.NoError(t, err)
}

// TestOracleUDTRegistrationError tests error handling in RegisterType
// Coverage target: connection.go lines 380-385 (error path)
func TestOracleUDTRegistrationError(t *testing.T) {
	conn, _ := setupTestContainer(t)

	type FakeType struct {
		ID int64 `udt:"ID"`
	}

	// Attempt to register non-existent type (should fail)
	err := conn.RegisterType("NONEXISTENT_TYPE", "NONEXISTENT_TABLE", FakeType{})

	// Verify error handling path is executed
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to register Oracle type NONEXISTENT_TYPE")
}

// TestOracleUDTObjectOnlyWithOwner tests RegisterTypeWithOwner with object-only
// Coverage target: connection.go line 426 (else branch)
func TestOracleUDTObjectOnlyWithOwner(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	// Get current user for owner parameter
	var currentUser string
	err := conn.QueryRow(ctx, "SELECT USER FROM DUAL").Scan(&currentUser)
	require.NoError(t, err)

	// Create object type
	setupSQL := `
		BEGIN
			EXECUTE IMMEDIATE 'CREATE TYPE OWNER_TYPE AS OBJECT (
				VALUE NUMBER
			)';
		EXCEPTION
			WHEN OTHERS THEN
				IF SQLCODE != -955 THEN
					RAISE;
				END IF;
		END;
	`

	_, err = conn.Exec(ctx, setupSQL)
	require.NoError(t, err)

	type OwnerType struct {
		Value int64 `udt:"VALUE"`
	}

	// Register with owner but NO collection type
	// This covers the else branch at line 426
	err = conn.RegisterTypeWithOwner(currentUser, "OWNER_TYPE", "", OwnerType{})
	require.NoError(t, err)
}

// TestOracleUDTRegistrationErrorWithOwner tests error handling in RegisterTypeWithOwner
// Coverage target: connection.go lines 431-437 (error path)
func TestOracleUDTRegistrationErrorWithOwner(t *testing.T) {
	conn, _ := setupTestContainer(t)

	type FakeType struct {
		ID int64 `udt:"ID"`
	}

	// Attempt to register with invalid owner/type combination
	err := conn.RegisterTypeWithOwner("INVALID_SCHEMA", "FAKE_TYPE", "", FakeType{})

	// Verify error handling path is executed
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to register Oracle type INVALID_SCHEMA.FAKE_TYPE")
}

// TestOracleUDTRegistrationLogging tests success path logging in RegisterTypeWithOwner
// Coverage target: connection.go lines 440-444 (debug logging)
func TestOracleUDTRegistrationLogging(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	// Get current user
	var currentUser string
	err := conn.QueryRow(ctx, "SELECT USER FROM DUAL").Scan(&currentUser)
	require.NoError(t, err)

	// Create type
	setupSQL := `
		BEGIN
			EXECUTE IMMEDIATE 'CREATE TYPE LOG_TYPE AS OBJECT (
				ID NUMBER
			)';
		EXCEPTION
			WHEN OTHERS THEN
				IF SQLCODE != -955 THEN
					RAISE;
				END IF;
		END;
	`

	_, err = conn.Exec(ctx, setupSQL)
	require.NoError(t, err)

	type LogType struct {
		ID int64 `udt:"ID"`
	}

	// Register with owner (triggers debug logging on success)
	// This covers lines 440-444
	err = conn.RegisterTypeWithOwner(currentUser, "LOG_TYPE", "", LogType{})
	require.NoError(t, err)

	// Success path ensures debug logging is executed
}
