package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	cachetesting "github.com/gaborage/go-bricks/cache/testing"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	"github.com/gaborage/go-bricks/internal/leasescope"
	"github.com/gaborage/go-bricks/internal/testutil"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/gaborage/go-bricks/server"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

// keyedDBConfigSource returns a DatabaseConfig whose Database field is the lookup key, so a
// connector can mint a distinct (and individually closeable) mock per tenant.
type keyedDBConfigSource struct{}

func (keyedDBConfigSource) DBConfig(_ context.Context, key string) (*config.DatabaseConfig, error) {
	return &config.DatabaseConfig{Type: "postgresql", Host: localHost, Database: key}, nil
}

// TestResourceProviderDBLeaseSurvivesConcurrentEviction is the end-to-end proof of issue
// #606: a handle borrowed via deps.DB during a unit of work (with a lease scope installed,
// as the HTTP middleware / job runner do) is NOT closed when a concurrent unit of work for
// another tenant evicts it — it is closed only once the borrowing scope releases its lease.
func TestResourceProviderDBLeaseSurvivesConcurrentEviction(t *testing.T) {
	mocks := map[string]*testmocks.MockDatabase{}
	connector := func(cfg *config.DatabaseConfig, _ logger.Logger) (database.Interface, error) {
		m := &testmocks.MockDatabase{}
		m.On("Close").Return(nil)
		mocks[cfg.Database] = m
		return m, nil
	}
	// MaxSize 1: borrowing a second tenant evicts the first.
	dbMgr := database.NewDbManager(keyedDBConfigSource{}, logger.New("error", false),
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Hour}, connector)
	defer func() { _ = dbMgr.Close() }()
	provider := NewMultiTenantResourceProvider(dbMgr, nil, nil, nil)

	// Unit of work for tenant "a" borrows its handle under a lease scope (the framework
	// installs this scope in the HTTP middleware / scheduler job runner).
	ctxA, scopeA := leasescope.Install(multitenant.SetTenant(context.Background(), "a"))
	connA, err := provider.DB(ctxA)
	require.NoError(t, err)
	require.NotNil(t, connA)

	// A concurrent unit of work for tenant "b" evicts "a" from the (size-1) pool.
	ctxB, scopeB := leasescope.Install(multitenant.SetTenant(context.Background(), "b"))
	_, err = provider.DB(ctxB)
	require.NoError(t, err)

	// "a" was evicted but is still leased by scope A → it must NOT have been closed.
	mocks["a"].AssertNotCalled(t, "Close")

	// Scope A's unit of work completes → its lease is released → "a" is closed now.
	scopeA.ReleaseAll()
	mocks["a"].AssertCalled(t, "Close")

	scopeB.ReleaseAll()
}

// TestResourceProviderDBWithoutScopeReleasesImmediately documents the unscoped fallback:
// when ctx carries no lease scope (framework probes, ad-hoc background work) the lease is
// released as soon as DB() returns, so nothing leaks (but the handle is unprotected).
func TestResourceProviderDBWithoutScopeReleasesImmediately(t *testing.T) {
	mocks := map[string]*testmocks.MockDatabase{}
	connector := func(cfg *config.DatabaseConfig, _ logger.Logger) (database.Interface, error) {
		m := &testmocks.MockDatabase{}
		m.On("Close").Return(nil)
		mocks[cfg.Database] = m
		return m, nil
	}
	dbMgr := database.NewDbManager(keyedDBConfigSource{}, logger.New("error", false),
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Hour}, connector)
	defer func() { _ = dbMgr.Close() }()
	provider := NewMultiTenantResourceProvider(dbMgr, nil, nil, nil)

	// No leasescope.Install: the request context carries no scope.
	_, err := provider.DB(multitenant.SetTenant(context.Background(), "a"))
	require.NoError(t, err)

	// Borrowing "b" evicts "a"; because "a"'s lease was released immediately (no scope),
	// the evicted handle is closed right away — non-leaking, but unprotected.
	_, err = provider.DB(multitenant.SetTenant(context.Background(), "b"))
	require.NoError(t, err)
	mocks["a"].AssertCalled(t, "Close")
}

const (
	testTenantID         = "tenant-123"
	testNamedDBHost      = "legacy.db"
	testNamedDBName      = "legacy_db"
	testNamedDBConnError = "named db connection failed"
)

func testNamedDBConfig() map[string]config.DatabaseConfig {
	return map[string]config.DatabaseConfig{
		"legacy": {Type: "postgresql", Host: testNamedDBHost, Port: 5432, Database: testNamedDBName, Username: "user"},
	}
}

func TestSingleTenantResourceProvider(t *testing.T) {
	t.Run("constructor", func(t *testing.T) {
		dbManager := createTestDbManager(t)
		msgManager := createTestMessagingManager(t)
		declarations := &messaging.Declarations{}

		provider := NewSingleTenantResourceProvider(dbManager, msgManager, nil, declarations)

		assert.NotNil(t, provider)
		assert.Equal(t, dbManager, provider.dbManager)
		assert.Equal(t, msgManager, provider.messagingManager)
		assert.Equal(t, declarations, provider.declarations)
	})

	t.Run("GetDB success", func(t *testing.T) {
		mockDB := &testmocks.MockDatabase{}
		dbManager := createTestDbManagerWithMock(t, mockDB)
		provider := NewSingleTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DB(context.Background())

		require.NoError(t, err)
		assert.Equal(t, mockDB, db)
	})

	t.Run("GetDB with nil database manager", func(t *testing.T) {
		provider := NewSingleTenantResourceProvider(nil, nil, nil, nil)

		db, err := provider.DB(context.Background())

		require.ErrorContains(t, err, "database")
		assert.Contains(t, err.Error(), "not_configured")
		assert.Nil(t, db)
	})

	t.Run("GetDB with database manager error", func(t *testing.T) {
		dbManager := createTestDbManagerWithError(t, errors.New("connection failed"))
		provider := NewSingleTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DB(context.Background())

		require.ErrorContains(t, err, "connection failed")
		assert.Nil(t, db)
	})

	t.Run("DBByName success", func(t *testing.T) {
		mockDB := &testmocks.MockDatabase{}
		dbManager := createTestDbManagerWithNamedDBMock(t, mockDB, testNamedDBConfig())
		provider := NewSingleTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "legacy")

		require.NoError(t, err)
		assert.Equal(t, mockDB, db)
	})

	t.Run("DBByName with nil database manager", func(t *testing.T) {
		provider := NewSingleTenantResourceProvider(nil, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "legacy")

		require.ErrorContains(t, err, "database")
		assert.Contains(t, err.Error(), "not_configured")
		assert.Nil(t, db)
	})

	t.Run("DBByName with empty name", func(t *testing.T) {
		mockDB := &testmocks.MockDatabase{}
		dbManager := createTestDbManagerWithNamedDBMock(t, mockDB, testNamedDBConfig())
		provider := NewSingleTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "")

		require.ErrorContains(t, err, "database_name")
		assert.Contains(t, err.Error(), "cannot be empty")
		assert.Nil(t, db)
	})

	t.Run("DBByName with database manager error", func(t *testing.T) {
		dbManager := createTestDbManagerWithNamedDBError(t, testNamedDBConfig(), errors.New(testNamedDBConnError))
		provider := NewSingleTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "legacy")

		require.ErrorContains(t, err, testNamedDBConnError)
		assert.Nil(t, db)
	})

	t.Run("DBByName with unknown database name", func(t *testing.T) {
		mockDB := &testmocks.MockDatabase{}
		dbManager := createTestDbManagerWithNamedDBMock(t, mockDB, testNamedDBConfig())
		provider := NewSingleTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "unknown_db")

		require.ErrorContains(t, err, "unknown_db")
		assert.Contains(t, err.Error(), "not found")
		assert.Nil(t, db)
	})

	t.Run("GetMessaging success without declarations", func(t *testing.T) {
		mockClient := testmocks.NewMockAMQPClient()
		mockClient.On("Close").Return(nil).Maybe()
		msgManager := createTestMessagingManagerWithMock(t, mockClient)
		provider := NewSingleTenantResourceProvider(nil, msgManager, nil, nil)

		client, err := provider.Messaging(context.Background())

		require.NoError(t, err)
		// The manager wraps every client it hands out so the tenant stamp is written
		// for clients the framework did not build; identity is no longer the contract.
		assert.NotNil(t, client)
	})

	t.Run("GetMessaging success with declarations", func(t *testing.T) {
		mockClient := testmocks.NewMockAMQPClient()
		mockClient.On("Close").Return(nil).Maybe()
		msgManager := createTestMessagingManagerWithMock(t, mockClient)
		declarations := &messaging.Declarations{}
		provider := NewSingleTenantResourceProvider(nil, msgManager, nil, declarations)

		client, err := provider.Messaging(context.Background())

		require.NoError(t, err)
		// The manager wraps every client it hands out so the tenant stamp is written
		// for clients the framework did not build; identity is no longer the contract.
		assert.NotNil(t, client)
	})

	t.Run("GetMessaging with nil messaging manager", func(t *testing.T) {
		provider := NewSingleTenantResourceProvider(nil, nil, nil, nil)

		client, err := provider.Messaging(context.Background())

		require.ErrorContains(t, err, "messaging")
		assert.Contains(t, err.Error(), "not_configured")
		assert.Nil(t, client)
	})

	// Note: EnsureConsumers error testing is complex due to concrete Manager type
	// and would require significant mocking infrastructure. Since this follows 80/20 rule,
	// we focus on testing the more important error paths like nil managers and GetPublisher errors.

	// Note: GetPublisher error testing is complex due to concrete Manager type
	// For the 80/20 rule, we focus on the nil manager case which covers the main error path

	t.Run("SetDeclarations", func(t *testing.T) {
		provider := NewSingleTenantResourceProvider(nil, nil, nil, nil)
		newDeclarations := &messaging.Declarations{}

		provider.SetDeclarations(newDeclarations)

		assert.Equal(t, newDeclarations, provider.declarations)
	})

	t.Run("GetCache success", func(t *testing.T) {
		cacheManager := createTestCacheManager(t)
		provider := NewSingleTenantResourceProvider(nil, nil, cacheManager, nil)

		c, err := provider.Cache(context.Background())

		require.NoError(t, err)
		assert.NotNil(t, c)
	})

	t.Run("GetCache with nil cache manager", func(t *testing.T) {
		provider := NewSingleTenantResourceProvider(nil, nil, nil, nil)

		c, err := provider.Cache(context.Background())

		require.ErrorContains(t, err, "cache")
		assert.Contains(t, err.Error(), "not_configured")
		assert.Nil(t, c)
	})
}

func TestMultiTenantResourceProvider(t *testing.T) {
	t.Run("constructor", func(t *testing.T) {
		dbManager := createTestDbManager(t)
		msgManager := createTestMessagingManager(t)
		declarations := &messaging.Declarations{}

		provider := NewMultiTenantResourceProvider(dbManager, msgManager, nil, declarations)

		assert.NotNil(t, provider)
		assert.Equal(t, dbManager, provider.dbManager)
		assert.Equal(t, msgManager, provider.messagingManager)
		assert.Equal(t, declarations, provider.declarations)
	})

	// Note: Multi-tenant success testing requires mock resource source configuration
	// For the 80/20 rule, we focus on testing error paths which are more critical

	t.Run("shared tenancy resolves the control-plane key without a tenant", func(t *testing.T) {
		mockClient := testmocks.NewMockAMQPClient()
		mockClient.On("Close").Return(nil).Maybe()
		provider := NewMultiTenantResourceProvider(nil,
			createTestMessagingManagerWithMock(t, mockClient), nil, nil)
		provider.SetMessagingTenancy(config.TenancyShared)

		client, err := provider.Messaging(context.Background())

		require.NoError(t, err)
		// The manager wraps every client it hands out so the tenant stamp is written
		// for clients the framework did not build; identity is no longer the contract.
		assert.NotNil(t, client)
	})

	t.Run("shared tenancy ignores a tenant in context", func(t *testing.T) {
		mockClient := testmocks.NewMockAMQPClient()
		mockClient.On("Close").Return(nil).Maybe()
		provider := NewMultiTenantResourceProvider(nil,
			createTestMessagingManagerWithMock(t, mockClient), nil, nil)
		provider.SetMessagingTenancy(config.TenancyShared)
		ctx := multitenant.SetTenant(context.Background(), testTenantID)

		client, err := provider.Messaging(ctx)

		require.NoError(t, err, "a tenant in ctx is ignored under shared tenancy, not an error")
		assert.NotNil(t, client)
	})

	t.Run("per-tenant tenancy still requires a tenant", func(t *testing.T) {
		provider := NewMultiTenantResourceProvider(nil,
			createTestMessagingManagerWithMock(t, testmocks.NewMockAMQPClient()), nil, nil)

		client, err := provider.Messaging(context.Background())

		// app.ErrNoTenantInContext IS multitenant.ErrNoTenant (app.go:50), so one
		// ErrorIs covers both spellings; asserting each name separately tested the
		// alias twice rather than testing two properties.
		assert.Nil(t, client)
		require.ErrorIs(t, err, ErrNoTenantInContext,
			"the app sentinel is the multitenant one, so errors.Is matches through either name")
	})

	t.Run("GetDB with nil database manager", func(t *testing.T) {
		provider := NewMultiTenantResourceProvider(nil, nil, nil, nil)
		ctx := multitenant.SetTenant(context.Background(), testTenantID)

		db, err := provider.DB(ctx)

		require.ErrorContains(t, err, "database")
		assert.Contains(t, err.Error(), "not_configured")
		assert.Nil(t, db)
	})

	t.Run("GetDB with no tenant in context", func(t *testing.T) {
		dbManager := createTestDbManager(t)
		provider := NewMultiTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DB(context.Background())

		assert.Nil(t, db)
		require.ErrorIs(t, err, ErrNoTenantInContext)
	})

	t.Run("GetDB with empty tenant in context", func(t *testing.T) {
		dbManager := createTestDbManager(t)
		provider := NewMultiTenantResourceProvider(dbManager, nil, nil, nil)
		ctx := multitenant.SetTenant(context.Background(), "")

		db, err := provider.DB(ctx)

		assert.Nil(t, db)
		require.ErrorIs(t, err, ErrNoTenantInContext)
	})

	// Note: Database manager error testing with tenant would require complex tenant configuration
	// For the 80/20 rule, the nil database manager and no-tenant-in-context tests cover the main error scenarios

	t.Run("DBByName success", func(t *testing.T) {
		mockDB := &testmocks.MockDatabase{}
		dbManager := createTestDbManagerWithNamedDBMock(t, mockDB, testNamedDBConfig())
		provider := NewMultiTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "legacy")

		require.NoError(t, err)
		assert.Equal(t, mockDB, db)
	})

	t.Run("DBByName with nil database manager", func(t *testing.T) {
		provider := NewMultiTenantResourceProvider(nil, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "legacy")

		require.ErrorContains(t, err, "databases")
		assert.Contains(t, err.Error(), "not_configured")
		assert.Nil(t, db)
	})

	t.Run("DBByName with empty name", func(t *testing.T) {
		mockDB := &testmocks.MockDatabase{}
		dbManager := createTestDbManagerWithNamedDBMock(t, mockDB, testNamedDBConfig())
		provider := NewMultiTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "")

		require.ErrorContains(t, err, "database_name")
		assert.Contains(t, err.Error(), "cannot be empty")
		assert.Nil(t, db)
	})

	t.Run("DBByName with database manager error", func(t *testing.T) {
		dbManager := createTestDbManagerWithNamedDBError(t, testNamedDBConfig(), errors.New(testNamedDBConnError))
		provider := NewMultiTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "legacy")

		require.ErrorContains(t, err, testNamedDBConnError)
		assert.Nil(t, db)
	})

	t.Run("DBByName with unknown database name", func(t *testing.T) {
		mockDB := &testmocks.MockDatabase{}
		dbManager := createTestDbManagerWithNamedDBMock(t, mockDB, testNamedDBConfig())
		provider := NewMultiTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "unknown_db")

		require.ErrorContains(t, err, "unknown_db")
		assert.Contains(t, err.Error(), "not found")
		assert.Nil(t, db)
	})

	// Note: Multi-tenant messaging success testing requires tenant-specific configuration
	// For the 80/20 rule, we focus on testing error paths which are more critical

	t.Run("GetMessaging with nil messaging manager", func(t *testing.T) {
		provider := NewMultiTenantResourceProvider(nil, nil, nil, nil)
		ctx := multitenant.SetTenant(context.Background(), testTenantID)

		client, err := provider.Messaging(ctx)

		require.ErrorContains(t, err, "messaging")
		assert.Contains(t, err.Error(), "not_configured")
		assert.Nil(t, client)
	})

	t.Run("GetMessaging with no tenant in context", func(t *testing.T) {
		msgManager := createTestMessagingManager(t)
		provider := NewMultiTenantResourceProvider(nil, msgManager, nil, nil)

		client, err := provider.Messaging(context.Background())

		assert.Nil(t, client)
		require.ErrorIs(t, err, ErrNoTenantInContext)
	})

	t.Run("GetMessaging with empty tenant in context", func(t *testing.T) {
		msgManager := createTestMessagingManager(t)
		provider := NewMultiTenantResourceProvider(nil, msgManager, nil, nil)
		ctx := multitenant.SetTenant(context.Background(), "")

		client, err := provider.Messaging(ctx)

		assert.Nil(t, client)
		require.ErrorIs(t, err, ErrNoTenantInContext)
	})

	// Note: EnsureConsumers error testing is complex due to concrete Manager type
	// and would require significant mocking infrastructure. Since this follows 80/20 rule,
	// we focus on testing the more important error paths like nil managers and GetPublisher errors.

	// Note: GetPublisher error testing is complex due to concrete Manager type
	// For the 80/20 rule, we focus on the nil manager case which covers the main error path

	t.Run("SetDeclarations", func(t *testing.T) {
		provider := NewMultiTenantResourceProvider(nil, nil, nil, nil)
		newDeclarations := &messaging.Declarations{}

		provider.SetDeclarations(newDeclarations)

		assert.Equal(t, newDeclarations, provider.declarations)
	})

	t.Run("GetCache success", func(t *testing.T) {
		cacheManager := createTestCacheManager(t)
		provider := NewMultiTenantResourceProvider(nil, nil, cacheManager, nil)
		ctx := multitenant.SetTenant(context.Background(), testTenantID)

		c, err := provider.Cache(ctx)

		require.NoError(t, err)
		assert.NotNil(t, c)
	})

	t.Run("GetCache with nil cache manager", func(t *testing.T) {
		provider := NewMultiTenantResourceProvider(nil, nil, nil, nil)
		ctx := multitenant.SetTenant(context.Background(), testTenantID)

		c, err := provider.Cache(ctx)

		require.ErrorContains(t, err, "cache")
		assert.Contains(t, err.Error(), "not_configured")
		assert.Nil(t, c)
	})

	t.Run("GetCache with no tenant in context", func(t *testing.T) {
		cacheManager := createTestCacheManager(t)
		provider := NewMultiTenantResourceProvider(nil, nil, cacheManager, nil)

		c, err := provider.Cache(context.Background())

		assert.Nil(t, c)
		require.ErrorIs(t, err, ErrNoTenantInContext)
	})

	t.Run("GetCache with empty tenant in context", func(t *testing.T) {
		cacheManager := createTestCacheManager(t)
		provider := NewMultiTenantResourceProvider(nil, nil, cacheManager, nil)
		ctx := multitenant.SetTenant(context.Background(), "")

		c, err := provider.Cache(ctx)

		assert.Nil(t, c)
		require.ErrorIs(t, err, ErrNoTenantInContext)
	})
}

func TestResourceProviderInterface(t *testing.T) {
	t.Run("SingleTenantResourceProvider implements ResourceProvider", func(t *testing.T) {
		var provider ResourceProvider = NewSingleTenantResourceProvider(nil, nil, nil, nil)
		assert.NotNil(t, provider)
	})

	t.Run("MultiTenantResourceProvider implements ResourceProvider", func(t *testing.T) {
		var provider ResourceProvider = NewMultiTenantResourceProvider(nil, nil, nil, nil)
		assert.NotNil(t, provider)
	})
}

// Helper functions to create test managers with mocked dependencies

func createTestDbManager(t *testing.T) *database.DbManager {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Type: "postgresql",
			Host: "localhost",
			Port: 5432,
		},
	}
	resourceSource := config.NewTenantStore(cfg)
	log := logger.New("debug", true)

	m := database.NewDbManager(resourceSource, log,
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Hour},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			db := &testmocks.MockDatabase{}
			// The manager's Close (registered below) closes every connection it created, and
			// the mock panics on an unexpected call. Maybe: a test that leased none closes none.
			db.On("Close").Return(nil).Maybe()
			return db, nil
		},
	)
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func createTestDbManagerWithMock(t *testing.T, mockDB *testmocks.MockDatabase) *database.DbManager {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Type: "postgresql",
			Host: "localhost",
			Port: 5432,
		},
	}
	resourceSource := config.NewTenantStore(cfg)
	log := logger.New("debug", true)

	// See createTestDbManager: the cleanup below closes what the manager created.
	mockDB.On("Close").Return(nil).Maybe()
	m := database.NewDbManager(resourceSource, log,
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Hour},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return mockDB, nil
		},
	)
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func createTestDbManagerWithError(t *testing.T, err error) *database.DbManager {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Type: "postgresql",
			Host: "localhost",
			Port: 5432,
		},
	}
	resourceSource := config.NewTenantStore(cfg)
	log := logger.New("debug", true)

	m := database.NewDbManager(resourceSource, log,
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Hour},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return nil, err
		},
	)
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func createTestDbManagerWithNamedDBMock(t *testing.T, mockDB *testmocks.MockDatabase, namedDBs map[string]config.DatabaseConfig) *database.DbManager {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Type: "postgresql",
			Host: "localhost",
			Port: 5432,
		},
		Databases: namedDBs,
	}
	resourceSource := config.NewTenantStore(cfg)
	log := logger.New("debug", true)

	// See createTestDbManager: the cleanup below closes what the manager created.
	mockDB.On("Close").Return(nil).Maybe()
	m := database.NewDbManager(resourceSource, log,
		database.DbManagerOptions{MaxSize: 10, IdleTTL: time.Hour},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return mockDB, nil
		},
	)
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func createTestDbManagerWithNamedDBError(t *testing.T, namedDBs map[string]config.DatabaseConfig, err error) *database.DbManager {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Type: "postgresql",
			Host: "localhost",
			Port: 5432,
		},
		Databases: namedDBs,
	}
	resourceSource := config.NewTenantStore(cfg)
	log := logger.New("debug", true)

	m := database.NewDbManager(resourceSource, log,
		database.DbManagerOptions{MaxSize: 10, IdleTTL: time.Hour},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return nil, err
		},
	)
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func createTestMessagingManager(t *testing.T) *messaging.Manager {
	t.Helper()
	cfg := &config.Config{
		Messaging: config.MessagingConfig{
			Broker: config.BrokerConfig{URL: "amqp://guest:guest@localhost:5672/"},
		},
	}
	resourceSource := config.NewTenantStore(cfg)
	log := logger.New("debug", true)

	m := messaging.NewMessagingManager(resourceSource, log,
		messaging.ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour},
		func(string, logger.Logger) messaging.AMQPClient {
			client := testmocks.NewMockAMQPClient()
			// The manager's Close (registered below) closes every publisher it created, and
			// the mock panics on an unexpected call. Maybe: a test that leased none closes none.
			client.On("Close").Return(nil).Maybe()
			return client
		},
	)
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// createTestMessagingManagerWithMock builds a manager whose every key resolves to mockClient.
// The manager is closed at test end, which closes each publisher it created, so a testify
// double passed here must allow Close.
func createTestMessagingManagerWithMock(t *testing.T, mockClient messaging.AMQPClient) *messaging.Manager {
	t.Helper()
	cfg := &config.Config{
		Messaging: config.MessagingConfig{
			Broker: config.BrokerConfig{URL: "amqp://guest:guest@localhost:5672/"},
		},
	}
	resourceSource := config.NewTenantStore(cfg)
	log := logger.New("debug", true)

	m := messaging.NewMessagingManager(resourceSource, log,
		messaging.ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour},
		func(string, logger.Logger) messaging.AMQPClient {
			return mockClient
		},
	)
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func createTestCacheManager(t *testing.T) *cache.CacheManager {
	t.Helper()
	return createTestCacheManagerWithConnector(t, func(_ context.Context, _ string) (cache.Cache, error) {
		return &mockCacheInstance{}, nil
	})
}

func createTestCacheManagerWithConnector(t *testing.T, connector cache.Connector) *cache.CacheManager {
	t.Helper()
	manager, err := cache.NewCacheManager(
		cache.ManagerConfig{
			MaxSize:         10,
			IdleTTL:         time.Hour,
			CleanupInterval: 5 * time.Minute,
		},
		connector,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

// openHandleCounter mints a closeable mock per tenant and tracks how many are open at once.
type openHandleCounter struct {
	mu         sync.Mutex
	open, peak int
}

func (c *openHandleCounter) connector(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
	m := &testmocks.MockDatabase{}
	m.On("Close").Run(func(mock.Arguments) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.open--
	}).Return(nil)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.open++
	c.peak = max(c.peak, c.open)
	return m, nil
}

func (c *openHandleCounter) snapshot() (open, peak int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open, c.peak
}

// TestForEachTenantBoundsOpenHandlesDuringASweep sweeps six tenants through a real DbManager of max size 2.
func TestForEachTenantBoundsOpenHandlesDuringASweep(t *testing.T) {
	const maxSize = 2
	tenants := []string{"t1", "t2", "t3", "t4", "t5", "t6"}

	newProvider := func(t *testing.T) (*MultiTenantResourceProvider, *openHandleCounter) {
		counter := &openHandleCounter{}
		dbMgr := database.NewDbManager(keyedDBConfigSource{}, logger.New("error", false),
			database.DbManagerOptions{MaxSize: maxSize, IdleTTL: time.Hour}, counter.connector)
		t.Cleanup(func() { _ = dbMgr.Close() })
		return NewMultiTenantResourceProvider(dbMgr, nil, nil, nil), counter
	}

	t.Run("for_each_tenant", func(t *testing.T) {
		provider, counter := newProvider(t)
		jobCtx, jobScope := leasescope.Install(context.Background())
		defer jobScope.ReleaseAll()

		err := multitenant.ForEachTenant(jobCtx, tenants, func(ctx context.Context, _ string) error {
			_, err := provider.DB(ctx)
			return err
		})

		require.NoError(t, err)
		_, peak := counter.snapshot()
		assert.LessOrEqual(t, peak, maxSize+1)
	})

	t.Run("job_scope_only_control", func(t *testing.T) {
		provider, counter := newProvider(t)
		jobCtx, jobScope := leasescope.Install(context.Background())

		for _, tenantID := range tenants {
			_, err := provider.DB(multitenant.SetTenant(jobCtx, tenantID))
			require.NoError(t, err)
		}

		open, _ := counter.snapshot()
		assert.Equal(t, len(tenants), open, "every leased handle stays open until the job scope drains")
		jobScope.ReleaseAll()
	})
}

const (
	knownTenantID     = "acme"
	unknownTenantID   = "ghost"
	malformedTenantID = "Bad_ID"
)

type unknownTenantRequest struct{}

// accessorModule exposes each per-tenant accessor on a raw route, which returns the
// accessor's error as is, and on a typed route, which returns it through the door.
type accessorModule struct{ deps *ModuleDeps }

func (m *accessorModule) Name() string                { return "accessors" }
func (m *accessorModule) Init(deps *ModuleDeps) error { m.deps = deps; return nil }
func (m *accessorModule) Shutdown() error             { return nil }

func (m *accessorModule) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	accessors := []struct {
		kind string
		use  func(context.Context) error
	}{
		{"db", func(ctx context.Context) error { _, err := m.deps.DB(ctx); return err }},
		{"cache", func(ctx context.Context) error { _, err := m.deps.Cache(ctx); return err }},
		{"messaging", func(ctx context.Context) error { _, err := m.deps.Messaging(ctx); return err }},
	}
	for _, a := range accessors {
		r.Add(http.MethodGet, "/raw/"+a.kind, func(c server.HandlerContext) error {
			if err := a.use(c.RequestContext()); err != nil {
				return err
			}
			return c.JSON(http.StatusOK, map[string]string{"ok": a.kind})
		})
		server.GET(hr, r, "/typed/"+a.kind, func(_ unknownTenantRequest, c server.HandlerContext) (server.Result[string], server.IAPIError) {
			if err := a.use(c.RequestContext()); err != nil {
				return server.Result[string]{}, server.NewUnknownTenantError(err)
			}
			return server.Result[string]{Data: a.kind}, nil
		})
	}
}

// contractStore is a dynamic store keeping the contract: an unknown key wraps the sentinel.
type contractStore struct{ *config.TenantStore }

func (contractStore) DBConfig(_ context.Context, key string) (*config.DatabaseConfig, error) {
	if key == knownTenantID {
		return &config.DatabaseConfig{Type: config.PostgreSQL, Host: localHost, Database: key}, nil
	}
	return nil, fmt.Errorf("%w: no secret for this tenant", multitenant.ErrUnknownTenant)
}

func (contractStore) IsDynamic() bool { return true }

// startTenantApp runs a real App on loopback: header resolver, app.env production, app.debug
// off. A nil store means the built-in store serving knownTenantID only.
func startTenantApp(t *testing.T, store TenantStore, opts *Options) string {
	t.Helper()
	cfg := &config.Config{
		App:         config.AppConfig{Name: testApp, Version: "1.0.0", Env: config.EnvProduction},
		Log:         config.LogConfig{Level: "error"},
		Multitenant: config.MultitenantConfig{Enabled: true, Resolver: config.ResolverConfig{Type: config.ResolverTypeHeader}},
	}
	cfg.Server.Host, cfg.Server.Port = "127.0.0.1", testutil.ReserveFreePort(t)
	cfg.Server.Timeout = config.TimeoutConfig{Read: probeRunDeadline, Write: 2 * probeRunDeadline, Middleware: probeRunDeadline, Shutdown: probeRunDeadline}
	if store != nil {
		cfg.Source.Type = config.SourceTypeDynamic
	} else {
		cfg.Multitenant.Tenants = map[string]config.TenantEntry{knownTenantID: {
			Database:  config.DatabaseConfig{Type: config.PostgreSQL, Host: localHost, Port: 5432, Database: knownTenantID, Username: knownTenantID},
			Messaging: config.TenantMessagingConfig{URL: "amqp://" + localHost},
			Cache:     config.CacheConfig{Enabled: true, Type: "redis", Redis: config.RedisConfig{Host: localHost, Port: 6379}},
		}}
	}
	sig := newQuitSignalHandler()
	opts.SignalHandler, opts.ResourceSource = sig, store
	opts.DatabaseConnector = func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
		return dbtesting.NewTestDB(config.PostgreSQL), nil
	}
	opts.MessagingClientFactory = func(string, logger.Logger) messaging.AMQPClient {
		client := testmocks.NewMockAMQPClient()
		client.On("Close").Return(nil)
		return client
	}
	a, _, err := NewWithConfig(cfg, opts)
	require.NoError(t, err)
	require.NoError(t, a.RegisterModule(&accessorModule{}))
	done := runInBackground(a)
	t.Cleanup(func() {
		sig.requestShutdown(t)
		assert.NoError(t, awaitRun(t, done))
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
	require.Eventually(t, func() bool {
		code, _, err := fetch(probeRunClient(), base+"/health")
		return err == nil && code == http.StatusOK
	}, probeRunDeadline, 10*time.Millisecond, "the listener never came up")
	return base
}

// tenantGet sends GET url as tenant and decodes the envelope.
func tenantGet(t *testing.T, url, tenant string) (int, server.APIResponse) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	require.NoError(t, err)
	req.Header.Set(server.HeaderXTenantID, tenant)
	resp, err := probeRunClient().Do(req)
	require.NoError(t, err)
	defer func() { assert.NoError(t, resp.Body.Close()) }()
	var env server.APIResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&env))
	return resp.StatusCode, env
}

func TestUnknownTenantAccessorsAnswer400(t *testing.T) {
	tests := []struct {
		name    string
		store   TenantStore
		path    string
		knownOK bool // false where the known tenant would dial a real Redis
	}{
		{name: "raw_db_accessor", path: "/raw/db", knownOK: true},
		{name: "raw_cache_accessor_builtin_connector", path: "/raw/cache"},
		{name: "raw_messaging_accessor_per_tenant", path: "/raw/messaging", knownOK: true},
		{name: "typed_db_accessor", path: "/typed/db", knownOK: true},
		{name: "typed_stub_store_error", store: contractStore{config.NewTenantStore(&config.Config{})}, path: "/typed/db", knownOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := startTenantApp(t, tc.store, &Options{})

			status, env := tenantGet(t, base+tc.path, unknownTenantID)

			assert.Equal(t, http.StatusBadRequest, status)
			assert.Equal(t, &server.APIErrorResponse{Code: "BAD_REQUEST", Message: "Invalid tenant"}, env.Error)
			if tc.knownOK {
				status, env = tenantGet(t, base+tc.path, knownTenantID)
				assert.Equal(t, http.StatusOK, status, "%+v", env.Error)
			}
		})
	}
}

func TestUnknownTenantTypedAndRawAnswersMatchMalformed(t *testing.T) {
	base := startTenantApp(t, nil, &Options{})
	seatStatus, seat := tenantGet(t, base+"/raw/db", malformedTenantID)
	require.Equal(t, http.StatusBadRequest, seatStatus)
	require.NotNil(t, seat.Error)
	require.Contains(t, seat.Meta, "timestamp")

	for _, path := range []string{"/typed/db", "/raw/db"} {
		status, env := tenantGet(t, base+path, unknownTenantID)

		assert.Equal(t, seatStatus, status, path)
		assert.Equal(t, seat.Error, env.Error, path)
		metaKeys := slices.Sorted(maps.Keys(env.Meta))
		assert.Equal(t, slices.Sorted(maps.Keys(seat.Meta)), metaKeys, path)
		assert.Subset(t, []string{"timestamp", "traceId"}, metaKeys, "%s: framework meta keys only", path)
	}
}

func TestKnownAndMalformedTenantAnswersUnchanged(t *testing.T) {
	var unknownDials atomic.Int32
	dialing := Options{CacheConnector: func(_ context.Context, key string) (cache.Cache, error) {
		if key == unknownTenantID {
			unknownDials.Add(1)
		}
		return cachetesting.NewMockCache(), nil
	}}
	tests := []struct {
		name, path, tenant string
		opts               Options
		want               int
	}{
		{name: "known_tenant_answers_200", path: "/raw/db", tenant: knownTenantID, want: http.StatusOK},
		{name: "malformed_tenant_answers_400", path: "/raw/db", tenant: malformedTenantID, want: http.StatusBadRequest},
		{name: "custom_cache_connector_still_dials", path: "/raw/cache", tenant: unknownTenantID, opts: dialing, want: http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, env := tenantGet(t, startTenantApp(t, nil, &tc.opts)+tc.path, tc.tenant)

			assert.Equal(t, tc.want, status)
			if tc.want == http.StatusBadRequest {
				assert.Equal(t, &server.APIErrorResponse{Code: "BAD_REQUEST", Message: "Invalid tenant"}, env.Error)
			}
		})
	}
	assert.Equal(t, int32(1), unknownDials.Load(), "a custom CacheConnector still serves the unknown tenant")
}
