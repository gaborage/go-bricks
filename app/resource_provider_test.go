package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/multitenant"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

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

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "database")
		assert.Contains(t, err.Error(), "not_configured")
		assert.Nil(t, db)
	})

	t.Run("GetDB with database manager error", func(t *testing.T) {
		dbManager := createTestDbManagerWithError(t, errors.New("connection failed"))
		provider := NewSingleTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DB(context.Background())

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "connection failed")
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

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "database")
		assert.Contains(t, err.Error(), "not_configured")
		assert.Nil(t, db)
	})

	t.Run("DBByName with empty name", func(t *testing.T) {
		mockDB := &testmocks.MockDatabase{}
		dbManager := createTestDbManagerWithNamedDBMock(t, mockDB, testNamedDBConfig())
		provider := NewSingleTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "database_name")
		assert.Contains(t, err.Error(), "cannot be empty")
		assert.Nil(t, db)
	})

	t.Run("DBByName with database manager error", func(t *testing.T) {
		dbManager := createTestDbManagerWithNamedDBError(t, testNamedDBConfig(), errors.New(testNamedDBConnError))
		provider := NewSingleTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "legacy")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), testNamedDBConnError)
		assert.Nil(t, db)
	})

	t.Run("DBByName with unknown database name", func(t *testing.T) {
		mockDB := &testmocks.MockDatabase{}
		dbManager := createTestDbManagerWithNamedDBMock(t, mockDB, testNamedDBConfig())
		provider := NewSingleTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "unknown_db")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown_db")
		assert.Contains(t, err.Error(), "not found")
		assert.Nil(t, db)
	})

	t.Run("GetMessaging success without declarations", func(t *testing.T) {
		mockClient := testmocks.NewMockAMQPClient()
		msgManager := createTestMessagingManagerWithMock(t, mockClient)
		provider := NewSingleTenantResourceProvider(nil, msgManager, nil, nil)

		client, err := provider.Messaging(context.Background())

		require.NoError(t, err)
		assert.Equal(t, mockClient, client)
	})

	t.Run("GetMessaging success with declarations", func(t *testing.T) {
		mockClient := testmocks.NewMockAMQPClient()
		msgManager := createTestMessagingManagerWithMock(t, mockClient)
		declarations := &messaging.Declarations{}
		provider := NewSingleTenantResourceProvider(nil, msgManager, nil, declarations)

		client, err := provider.Messaging(context.Background())

		require.NoError(t, err)
		assert.Equal(t, mockClient, client)
	})

	t.Run("GetMessaging with nil messaging manager", func(t *testing.T) {
		provider := NewSingleTenantResourceProvider(nil, nil, nil, nil)

		client, err := provider.Messaging(context.Background())

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "messaging")
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

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cache")
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

	t.Run("GetDB with nil database manager", func(t *testing.T) {
		provider := NewMultiTenantResourceProvider(nil, nil, nil, nil)
		ctx := multitenant.SetTenant(context.Background(), testTenantID)

		db, err := provider.DB(ctx)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "database")
		assert.Contains(t, err.Error(), "not_configured")
		assert.Nil(t, db)
	})

	t.Run("GetDB with no tenant in context", func(t *testing.T) {
		dbManager := createTestDbManager(t)
		provider := NewMultiTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DB(context.Background())

		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrNoTenantInContext)
		assert.Nil(t, db)
	})

	t.Run("GetDB with empty tenant in context", func(t *testing.T) {
		dbManager := createTestDbManager(t)
		provider := NewMultiTenantResourceProvider(dbManager, nil, nil, nil)
		ctx := multitenant.SetTenant(context.Background(), "")

		db, err := provider.DB(ctx)

		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrNoTenantInContext)
		assert.Nil(t, db)
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

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "databases")
		assert.Contains(t, err.Error(), "not_configured")
		assert.Nil(t, db)
	})

	t.Run("DBByName with empty name", func(t *testing.T) {
		mockDB := &testmocks.MockDatabase{}
		dbManager := createTestDbManagerWithNamedDBMock(t, mockDB, testNamedDBConfig())
		provider := NewMultiTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "database_name")
		assert.Contains(t, err.Error(), "cannot be empty")
		assert.Nil(t, db)
	})

	t.Run("DBByName with database manager error", func(t *testing.T) {
		dbManager := createTestDbManagerWithNamedDBError(t, testNamedDBConfig(), errors.New(testNamedDBConnError))
		provider := NewMultiTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "legacy")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), testNamedDBConnError)
		assert.Nil(t, db)
	})

	t.Run("DBByName with unknown database name", func(t *testing.T) {
		mockDB := &testmocks.MockDatabase{}
		dbManager := createTestDbManagerWithNamedDBMock(t, mockDB, testNamedDBConfig())
		provider := NewMultiTenantResourceProvider(dbManager, nil, nil, nil)

		db, err := provider.DBByName(context.Background(), "unknown_db")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown_db")
		assert.Contains(t, err.Error(), "not found")
		assert.Nil(t, db)
	})

	// Note: Multi-tenant messaging success testing requires tenant-specific configuration
	// For the 80/20 rule, we focus on testing error paths which are more critical

	t.Run("GetMessaging with nil messaging manager", func(t *testing.T) {
		provider := NewMultiTenantResourceProvider(nil, nil, nil, nil)
		ctx := multitenant.SetTenant(context.Background(), testTenantID)

		client, err := provider.Messaging(ctx)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "messaging")
		assert.Contains(t, err.Error(), "not_configured")
		assert.Nil(t, client)
	})

	t.Run("GetMessaging with no tenant in context", func(t *testing.T) {
		msgManager := createTestMessagingManager(t)
		provider := NewMultiTenantResourceProvider(nil, msgManager, nil, nil)

		client, err := provider.Messaging(context.Background())

		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrNoTenantInContext)
		assert.Nil(t, client)
	})

	t.Run("GetMessaging with empty tenant in context", func(t *testing.T) {
		msgManager := createTestMessagingManager(t)
		provider := NewMultiTenantResourceProvider(nil, msgManager, nil, nil)
		ctx := multitenant.SetTenant(context.Background(), "")

		client, err := provider.Messaging(ctx)

		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrNoTenantInContext)
		assert.Nil(t, client)
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

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cache")
		assert.Contains(t, err.Error(), "not_configured")
		assert.Nil(t, c)
	})

	t.Run("GetCache with no tenant in context", func(t *testing.T) {
		cacheManager := createTestCacheManager(t)
		provider := NewMultiTenantResourceProvider(nil, nil, cacheManager, nil)

		c, err := provider.Cache(context.Background())

		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrNoTenantInContext)
		assert.Nil(t, c)
	})

	t.Run("GetCache with empty tenant in context", func(t *testing.T) {
		cacheManager := createTestCacheManager(t)
		provider := NewMultiTenantResourceProvider(nil, nil, cacheManager, nil)
		ctx := multitenant.SetTenant(context.Background(), "")

		c, err := provider.Cache(ctx)

		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrNoTenantInContext)
		assert.Nil(t, c)
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

	return database.NewDbManager(resourceSource, log,
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Hour},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return &testmocks.MockDatabase{}, nil
		},
	)
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

	return database.NewDbManager(resourceSource, log,
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Hour},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return mockDB, nil
		},
	)
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

	return database.NewDbManager(resourceSource, log,
		database.DbManagerOptions{MaxSize: 1, IdleTTL: time.Hour},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return nil, err
		},
	)
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

	return database.NewDbManager(resourceSource, log,
		database.DbManagerOptions{MaxSize: 10, IdleTTL: time.Hour},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return mockDB, nil
		},
	)
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

	return database.NewDbManager(resourceSource, log,
		database.DbManagerOptions{MaxSize: 10, IdleTTL: time.Hour},
		func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return nil, err
		},
	)
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

	return messaging.NewMessagingManager(resourceSource, log,
		messaging.ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour},
		func(string, logger.Logger) messaging.AMQPClient {
			return testmocks.NewMockAMQPClient()
		},
	)
}

func createTestMessagingManagerWithMock(t *testing.T, mockClient messaging.AMQPClient) *messaging.Manager {
	t.Helper()
	cfg := &config.Config{
		Messaging: config.MessagingConfig{
			Broker: config.BrokerConfig{URL: "amqp://guest:guest@localhost:5672/"},
		},
	}
	resourceSource := config.NewTenantStore(cfg)
	log := logger.New("debug", true)

	return messaging.NewMessagingManager(resourceSource, log,
		messaging.ManagerOptions{MaxPublishers: 1, IdleTTL: time.Hour},
		func(string, logger.Logger) messaging.AMQPClient {
			return mockClient
		},
	)
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
	return manager
}
