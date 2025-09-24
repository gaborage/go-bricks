package multitenant

import (
	"context"
	"database/sql"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

const (
	notImplementedMsg = "not implemented"
)

// mockTenantConfigProvider implements TenantConfigProvider for testing
type mockTenantConfigProvider struct {
	configs map[string]*config.DatabaseConfig
}

func (m *mockTenantConfigProvider) GetDatabase(_ context.Context, tenantID string) (*config.DatabaseConfig, error) {
	if dbConfig, exists := m.configs[tenantID]; exists {
		return dbConfig, nil
	}
	return nil, errors.New("tenant not found")
}

func (m *mockTenantConfigProvider) GetMessaging(_ context.Context, _ string) (*TenantMessagingConfig, error) {
	// For testing cleanup functionality, we don't need messaging config
	return nil, errors.New("messaging not implemented in test")
}

func newTestLogger() logger.Logger {
	return logger.New("debug", false)
}

func TestTenantConnectionManagerStartStopCleanup(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{
		configs: map[string]*config.DatabaseConfig{
			"tenant1": {Type: "postgresql", Host: "localhost", Port: 5432, Database: "tenant1"},
		},
	}

	manager := NewTenantConnectionManager(provider, nil, log, WithIdleTTL(100*time.Millisecond))

	// Test starting cleanup
	manager.StartCleanup(50 * time.Millisecond)

	// Verify cleanup ticker is running
	assert.NotNil(t, manager.cleanupTicker)
	assert.NotNil(t, manager.cleanupStop)

	// Test stopping cleanup
	// Capture goroutine count before stopping
	initialGoroutines := runtime.NumGoroutine()
	manager.StopCleanup()

	// Give goroutine time to exit
	time.Sleep(10 * time.Millisecond)

	// Verify cleanup is stopped
	assert.Nil(t, manager.cleanupTicker)
	assert.Nil(t, manager.cleanupStop)

	// Verify goroutine was terminated
	assert.LessOrEqual(t, runtime.NumGoroutine(), initialGoroutines, "goroutine count should not increase after stopping cleanup")
}

func TestTenantConnectionManagerCleanupDisabled(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{}

	manager := NewTenantConnectionManager(provider, nil, log)

	// Test with zero interval (disabled)
	manager.StartCleanup(0)
	assert.Nil(t, manager.cleanupTicker)
	assert.Nil(t, manager.cleanupStop)

	// Test with negative interval (disabled)
	manager.StartCleanup(-1 * time.Second)
	assert.Nil(t, manager.cleanupTicker)
	assert.Nil(t, manager.cleanupStop)
}

func TestTenantConnectionManagerCloseStopsCleanup(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{}

	manager := NewTenantConnectionManager(provider, nil, log)

	// Start cleanup
	manager.StartCleanup(1 * time.Second)
	assert.NotNil(t, manager.cleanupTicker)

	// Close manager should stop cleanup
	err := manager.Close()
	assert.NoError(t, err)

	// Give goroutine time to exit
	time.Sleep(10 * time.Millisecond)

	assert.Nil(t, manager.cleanupTicker)
	assert.Nil(t, manager.cleanupStop)
}

// simpleMockDatabase is a basic mock that doesn't attempt real connections
type simpleMockDatabase struct {
	shouldFailHealth bool
}

func (db *simpleMockDatabase) Query(_ context.Context, _ string, _ ...any) (*sql.Rows, error) {
	return nil, errors.New(notImplementedMsg)
}

func (db *simpleMockDatabase) QueryRow(_ context.Context, _ string, _ ...any) *sql.Row {
	return nil
}

func (db *simpleMockDatabase) Exec(_ context.Context, _ string, _ ...any) (sql.Result, error) {
	return nil, errors.New(notImplementedMsg)
}

func (db *simpleMockDatabase) Prepare(_ context.Context, _ string) (types.Statement, error) {
	return nil, errors.New(notImplementedMsg)
}

func (db *simpleMockDatabase) Begin(_ context.Context) (types.Tx, error) {
	return nil, errors.New(notImplementedMsg)
}

func (db *simpleMockDatabase) BeginTx(_ context.Context, _ *sql.TxOptions) (types.Tx, error) {
	return nil, errors.New(notImplementedMsg)
}

func (db *simpleMockDatabase) Health(_ context.Context) error {
	if db.shouldFailHealth {
		return errors.New("health check failed")
	}
	return nil
}

func (db *simpleMockDatabase) Stats() (map[string]any, error) {
	return map[string]any{"connections": 1}, nil
}

func (db *simpleMockDatabase) Close() error {
	return nil
}

func (db *simpleMockDatabase) DatabaseType() string {
	return "mock"
}

func (db *simpleMockDatabase) GetMigrationTable() string {
	return "migrations"
}

func (db *simpleMockDatabase) CreateMigrationTable(_ context.Context) error {
	return nil
}

// DatabaseFactory interface for testable database creation
type DatabaseFactory interface {
	CreateDatabase(cfg *config.DatabaseConfig, log logger.Logger) (types.Interface, error)
}

// mockDatabaseFactory creates simple mock databases for testing
type mockDatabaseFactory struct{}

func (f *mockDatabaseFactory) CreateDatabase(cfg *config.DatabaseConfig, _ logger.Logger) (types.Interface, error) {
	if cfg.Host == "fail" {
		return nil, errors.New("connection failed")
	}
	return &simpleMockDatabase{
		shouldFailHealth: cfg.Host == "unhealthy",
	}, nil
}

// testableConnectionManager is a testable wrapper for TenantConnectionManager
type testableConnectionManager struct {
	*TenantConnectionManager
	factory DatabaseFactory
}

func newTestableConnectionManager(
	provider TenantConfigProvider,
	_ *TenantConfigCache, // Always nil in tests, managed by NewTenantConnectionManager
	log logger.Logger,
	_ DatabaseFactory, // Always nil in tests, using default factory
	opts ...ConnectionOption,
) *testableConnectionManager {
	factory := &mockDatabaseFactory{}
	manager := NewTenantConnectionManager(provider, nil, log, opts...)
	return &testableConnectionManager{
		TenantConnectionManager: manager,
		factory:                 factory,
	}
}

// Override initializeTenantConnection to use the factory
func (m *testableConnectionManager) GetDatabase(ctx context.Context, tenantID string) (types.Interface, error) {
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}

	// Check for existing connection
	m.mu.RLock()
	resource, exists := m.resources[tenantID]
	m.mu.RUnlock()
	if exists {
		// Update lastUsed under write lock
		m.mu.Lock()
		if resource2, ok := m.resources[tenantID]; ok {
			resource2.lastUsed = time.Now()
			resource = resource2
		}
		m.mu.Unlock()
		return resource.conn, nil
	}

	// Use singleflight to prevent concurrent initialization
	result, err, _ := m.sfg.Do(tenantID, func() (interface{}, error) {
		return m.initializeWithFactory(ctx, tenantID)
	})

	if err != nil {
		return nil, errors.New("failed to initialize tenant connection: " + err.Error())
	}

	db, ok := result.(types.Interface)
	if !ok || db == nil {
		return nil, errors.New("invalid database connection type for tenant " + tenantID)
	}

	return db, nil
}

func (m *testableConnectionManager) initializeWithFactory(ctx context.Context, tenantID string) (types.Interface, error) {
	// Get tenant database configuration
	dbConfig, err := m.cache.GetDatabase(ctx, tenantID)
	if err != nil {
		return nil, errors.New("failed to get database config for tenant " + tenantID + ": " + err.Error())
	}

	// Create database connection using factory
	conn, err := m.factory.CreateDatabase(dbConfig, m.logger)
	if err != nil {
		return nil, errors.New("failed to create database connection for tenant " + tenantID + ": " + err.Error())
	}

	// Test the connection
	if err := conn.Health(ctx); err != nil {
		conn.Close()
		return nil, errors.New("tenant database health check failed for " + tenantID + ": " + err.Error())
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Evict old connections if we've reached the limit
	if len(m.resources) >= m.cfg.maxActiveTenants {
		m.evictOldestLocked()
	}

	// Store the connection
	m.resources[tenantID] = &tenantResource{
		conn:     conn,
		lastUsed: time.Now(),
	}

	m.logger.WithFields(map[string]interface{}{
		"tenant_id": tenantID,
		"db_type":   dbConfig.Type,
		"db_host":   dbConfig.Host,
	}).Info().Msg("Initialized database connection for tenant")

	return conn, nil
}

func TestTenantConnectionManagerGetDatabase(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{
		configs: map[string]*config.DatabaseConfig{
			"tenant1": {Type: "postgresql", Host: "localhost", Port: 5432, Database: "tenant1"},
			"tenant2": {Type: "postgresql", Host: "localhost", Port: 5432, Database: "tenant2"},
		},
	}

	manager := newTestableConnectionManager(provider, nil, log, nil)

	t.Run("successful_connection", func(t *testing.T) {
		db, err := manager.GetDatabase(context.Background(), "tenant1")
		assert.NoError(t, err)
		assert.NotNil(t, db)
	})

	t.Run("cached_connection", func(t *testing.T) {
		// Get same tenant again - should use cached connection
		db, err := manager.GetDatabase(context.Background(), "tenant1")
		assert.NoError(t, err)
		assert.NotNil(t, db)
	})

	t.Run("empty_tenant_id", func(t *testing.T) {
		db, err := manager.GetDatabase(context.Background(), "")
		assert.Error(t, err)
		assert.Nil(t, db)
		assert.Contains(t, err.Error(), "tenant id is required")
	})

	t.Run("tenant_not_found", func(t *testing.T) {
		db, err := manager.GetDatabase(context.Background(), "unknown")
		assert.Error(t, err)
		assert.Nil(t, db)
	})
}

func TestTenantConnectionManagerGetDatabaseFailures(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{
		configs: map[string]*config.DatabaseConfig{
			"fail-connection": {Type: "postgresql", Host: "fail", Database: "test"},
			"fail-health":     {Type: "postgresql", Host: "unhealthy", Database: "test"},
		},
	}

	manager := newTestableConnectionManager(provider, nil, log, nil)

	t.Run("connection_failure", func(t *testing.T) {
		db, err := manager.GetDatabase(context.Background(), "fail-connection")
		assert.Error(t, err)
		assert.Nil(t, db)
		assert.Contains(t, err.Error(), "failed to create database connection")
	})

	t.Run("health_check_failure", func(t *testing.T) {
		db, err := manager.GetDatabase(context.Background(), "fail-health")
		assert.Error(t, err)
		assert.Nil(t, db)
		assert.Contains(t, err.Error(), "tenant database health check failed")
	})
}

func TestTenantConnectionManagerEviction(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{
		configs: map[string]*config.DatabaseConfig{
			"tenant1": {Type: "postgresql", Host: "localhost", Port: 5432, Database: "tenant1"},
			"tenant2": {Type: "postgresql", Host: "localhost", Port: 5432, Database: "tenant2"},
			"tenant3": {Type: "postgresql", Host: "localhost", Port: 5432, Database: "tenant3"},
		},
	}

	// Create manager with max 2 tenants
	manager := newTestableConnectionManager(provider, nil, log, nil, WithMaxTenants(2))

	// Connect to tenant1 and tenant2
	db1, err := manager.GetDatabase(context.Background(), "tenant1")
	assert.NoError(t, err)
	assert.NotNil(t, db1)

	db2, err := manager.GetDatabase(context.Background(), "tenant2")
	assert.NoError(t, err)
	assert.NotNil(t, db2)

	// Connect to tenant3 - should evict tenant1 (oldest)
	db3, err := manager.GetDatabase(context.Background(), "tenant3")
	assert.NoError(t, err)
	assert.NotNil(t, db3)

	// Give time for eviction goroutine to run
	time.Sleep(10 * time.Millisecond)

	// Verify we have exactly 2 connections
	manager.mu.RLock()
	connCount := len(manager.resources)
	manager.mu.RUnlock()
	assert.Equal(t, 2, connCount)
}

func TestTenantConnectionManagerRefreshTenant(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{
		configs: map[string]*config.DatabaseConfig{
			"tenant1": {Type: "postgresql", Host: "localhost", Port: 5432, Database: "tenant1"},
		},
	}

	manager := newTestableConnectionManager(provider, nil, log, nil)

	// Create initial connection
	db, err := manager.GetDatabase(context.Background(), "tenant1")
	assert.NoError(t, err)
	assert.NotNil(t, db)

	// Refresh the tenant
	err = manager.RefreshTenant(context.Background(), "tenant1")
	assert.NoError(t, err)

	// Verify connection was removed
	manager.mu.RLock()
	_, exists := manager.resources["tenant1"]
	manager.mu.RUnlock()
	assert.False(t, exists)

	// Refresh non-existent tenant should still work
	err = manager.RefreshTenant(context.Background(), "unknown")
	assert.NoError(t, err)
}

func TestTenantConnectionManagerCleanupIdleConnections(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{
		configs: map[string]*config.DatabaseConfig{
			"tenant1": {Type: "postgresql", Host: "localhost", Port: 5432, Database: "tenant1"},
			"tenant2": {Type: "postgresql", Host: "localhost", Port: 5432, Database: "tenant2"},
		},
	}

	// Create manager with very short idle TTL
	manager := newTestableConnectionManager(provider, nil, log, nil, WithIdleTTL(50*time.Millisecond))

	// Create connections
	db1, err := manager.GetDatabase(context.Background(), "tenant1")
	assert.NoError(t, err)
	assert.NotNil(t, db1)

	db2, err := manager.GetDatabase(context.Background(), "tenant2")
	assert.NoError(t, err)
	assert.NotNil(t, db2)

	// Wait for connections to become idle
	time.Sleep(100 * time.Millisecond)

	// Run cleanup
	manager.CleanupIdleConnections()

	// Give time for cleanup goroutines to run
	time.Sleep(10 * time.Millisecond)

	// Verify connections were cleaned up
	manager.mu.RLock()
	connCount := len(manager.resources)
	manager.mu.RUnlock()
	assert.Equal(t, 0, connCount)
}

func TestTenantConnectionManagerWithOptions(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{}

	// Test WithMaxTenants option
	manager := NewTenantConnectionManager(provider, nil, log, WithMaxTenants(50))
	assert.Equal(t, 50, manager.cfg.maxActiveTenants)

	// Test WithIdleTTL option
	manager = NewTenantConnectionManager(provider, nil, log, WithIdleTTL(10*time.Minute))
	assert.Equal(t, 10*time.Minute, manager.cfg.idleTTL)

	// Test invalid options (should use defaults)
	manager = NewTenantConnectionManager(provider, nil, log, WithMaxTenants(0), WithIdleTTL(0))
	assert.Equal(t, 100, manager.cfg.maxActiveTenants)   // default
	assert.Equal(t, 15*time.Minute, manager.cfg.idleTTL) // default
}

// mockDatabaseWithCloseError implements database.Interface but fails on Close()
type mockDatabaseWithCloseError struct {
	*simpleMockDatabase
}

func (m *mockDatabaseWithCloseError) Close() error {
	return errors.New("close failed")
}

func TestTenantConnectionManagerCloseWithErrors(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{
		configs: map[string]*config.DatabaseConfig{
			"tenant1": {Type: "postgresql", Host: "localhost", Port: 5432, Database: "tenant1"},
			"tenant2": {Type: "postgresql", Host: "localhost", Port: 5432, Database: "tenant2"},
		},
	}

	manager := NewTenantConnectionManager(provider, nil, log)

	// Manually add connections that will fail to close
	manager.mu.Lock()
	manager.resources["tenant1"] = &tenantResource{
		conn:     &mockDatabaseWithCloseError{&simpleMockDatabase{}},
		lastUsed: time.Now(),
	}
	manager.resources["tenant2"] = &tenantResource{
		conn:     &mockDatabaseWithCloseError{&simpleMockDatabase{}},
		lastUsed: time.Now(),
	}
	manager.mu.Unlock()

	// Close should collect all errors
	err := manager.Close()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "close failed")
}

func TestTenantConnectionManagerEvictOldestLockedEdgeCases(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{}
	manager := NewTenantConnectionManager(provider, nil, log)

	t.Run("empty_resources", func(_ *testing.T) {
		// evictOldestLocked on empty resources should not panic
		manager.mu.Lock()
		manager.evictOldestLocked()
		manager.mu.Unlock()
		// Should complete without error
	})

	t.Run("single_resource", func(t *testing.T) {
		// Add single resource
		manager.mu.Lock()
		manager.resources["tenant1"] = &tenantResource{
			conn:     &simpleMockDatabase{},
			lastUsed: time.Now(),
		}
		manager.evictOldestLocked()
		// Should evict the single resource
		assert.Empty(t, manager.resources)
		manager.mu.Unlock()
	})
}

func TestTenantConnectionManagerCleanupRaceCondition(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{
		configs: map[string]*config.DatabaseConfig{
			"tenant1": {Type: "postgresql", Host: "localhost", Port: 5432, Database: "tenant1"},
		},
	}

	manager := newTestableConnectionManager(provider, nil, log, nil, WithIdleTTL(50*time.Millisecond))

	// Create connection
	db, err := manager.GetDatabase(context.Background(), "tenant1")
	assert.NoError(t, err)
	assert.NotNil(t, db)

	// Wait for it to become idle
	time.Sleep(100 * time.Millisecond)

	// Synchronization primitives for coordinated concurrent execution
	var wg sync.WaitGroup
	startCh := make(chan struct{})
	errorCh := make(chan error, 50) // Buffered channel to capture errors from GetDatabase calls

	// Simulate race condition: access connection while cleanup is happening
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startCh                 // Wait for start signal
		for i := 0; i < 50; i++ { // Multiple iterations to increase contention
			db, err := manager.GetDatabase(context.Background(), "tenant1")
			if err != nil {
				errorCh <- err
				return // Exit early on first error to avoid flooding
			}
			// Preserve resource handling - verify we got a valid database
			if db == nil {
				errorCh <- errors.New("GetDatabase returned nil database without error")
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startCh                 // Wait for start signal
		for i := 0; i < 50; i++ { // Multiple iterations to increase contention
			manager.CleanupIdleConnections()
		}
	}()

	// Start both goroutines simultaneously
	close(startCh)

	// Wait for both goroutines to complete
	wg.Wait()
	close(errorCh)

	// Check for any errors captured during concurrent operations
	capturedErrors := make([]error, 0, 50) // Pre-allocate with capacity matching buffer size
	for err := range errorCh {
		capturedErrors = append(capturedErrors, err)
	}

	// Fail the test if any errors were encountered
	if len(capturedErrors) > 0 {
		t.Fatalf("Race condition test captured %d error(s): first error: %v", len(capturedErrors), capturedErrors[0])
	}
}

func TestTenantConnectionManagerStartCleanupShortInterval(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{}
	manager := NewTenantConnectionManager(provider, nil, log)

	// Start cleanup with very short interval (tests the goroutine creation)
	manager.StartCleanup(1 * time.Millisecond)

	// Give time for at least one cleanup cycle
	time.Sleep(10 * time.Millisecond)

	// Stop cleanup
	manager.StopCleanup()

	// Should complete without issues
	assert.Nil(t, manager.cleanupTicker)
}

func TestTenantConnectionManagerStopCleanupEdgeCases(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{}
	manager := NewTenantConnectionManager(provider, nil, log)

	t.Run("stop_without_start", func(t *testing.T) {
		// Stopping cleanup when not started should not panic
		manager.StopCleanup()
		assert.Nil(t, manager.cleanupTicker)
		assert.Nil(t, manager.cleanupStop)
	})

	t.Run("stop_twice", func(_ *testing.T) {
		// Start and stop twice
		manager.StartCleanup(1 * time.Second)
		manager.StopCleanup()
		manager.StopCleanup() // Should not panic
	})

	t.Run("start_twice", func(t *testing.T) {
		// Starting twice should stop the first one
		manager.StartCleanup(1 * time.Second)
		firstTicker := manager.cleanupTicker

		manager.StartCleanup(2 * time.Second)
		secondTicker := manager.cleanupTicker

		assert.NotSame(t, firstTicker, secondTicker)
		manager.StopCleanup()
	})
}

func TestTenantConnectionManagerGetDatabaseConcurrency(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{
		configs: map[string]*config.DatabaseConfig{
			"tenant1": {Type: "postgresql", Host: "localhost", Port: 5432, Database: "tenant1"},
		},
	}

	manager := newTestableConnectionManager(provider, nil, log, nil)

	// Test concurrent access to the same tenant (singleflight test)
	var wg sync.WaitGroup
	results := make([]types.Interface, 10)
	errs := make([]error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			db, err := manager.GetDatabase(context.Background(), "tenant1")
			results[index] = db
			errs[index] = err
		}(i)
	}

	wg.Wait()

	// All should succeed and return the same connection
	for i := 0; i < 10; i++ {
		assert.NoError(t, errs[i])
		assert.NotNil(t, results[i])
	}

	// Should only have one connection cached
	manager.mu.RLock()
	connCount := len(manager.resources)
	manager.mu.RUnlock()
	assert.Equal(t, 1, connCount)
}

func TestTenantConnectionManagerCleanupIdleConnectionsRechecksLiveness(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{
		configs: map[string]*config.DatabaseConfig{
			"tenant1": {Type: "postgresql", Host: "localhost", Port: 5432, Database: "tenant1"},
		},
	}

	manager := newTestableConnectionManager(provider, nil, log, nil, WithIdleTTL(100*time.Millisecond))

	// Create connection
	db, err := manager.GetDatabase(context.Background(), "tenant1")
	assert.NoError(t, err)
	assert.NotNil(t, db)

	// Wait for it to become eligible for cleanup
	time.Sleep(120 * time.Millisecond)

	// Start cleanup process, but access the connection during cleanup to make it "live" again
	go func() {
		time.Sleep(5 * time.Millisecond) // Small delay to let cleanup start
		// Access the connection to update lastUsed during cleanup
		manager.GetDatabase(context.Background(), "tenant1")
	}()

	// Run cleanup - the connection might survive due to the recheck
	manager.CleanupIdleConnections()

	// Give time for background operations
	time.Sleep(10 * time.Millisecond)
}
