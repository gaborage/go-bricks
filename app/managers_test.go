package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	zeroLimitMultiTenantTest     = "multi-tenant with zero tenant limit"
	negativeLimitMultiTenantTest = "multi-tenant with negative tenant limit"
	largeLimitTenantTest         = "large tenant limit"
)

func TestNewManagerConfigBuilder(t *testing.T) {
	t.Run("single-tenant configuration", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)

		assert.NotNil(t, builder)
		assert.False(t, builder.multiTenantEnabled)
		assert.Equal(t, 100, builder.tenantLimit)
	})

	t.Run("multi-tenant configuration", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 500)

		assert.NotNil(t, builder)
		assert.True(t, builder.multiTenantEnabled)
		assert.Equal(t, 500, builder.tenantLimit)
	})

	t.Run("zero tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 0)

		assert.NotNil(t, builder)
		assert.True(t, builder.multiTenantEnabled)
		assert.Equal(t, 0, builder.tenantLimit)
	})

	t.Run("negative tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, -10)

		assert.NotNil(t, builder)
		assert.True(t, builder.multiTenantEnabled)
		assert.Equal(t, -10, builder.tenantLimit)
	})
}

func TestManagerConfigBuilderBuildDatabaseOptions(t *testing.T) {
	t.Run("single-tenant database options", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, 10, options.MaxSize)
		assert.Equal(t, 1*time.Hour, options.IdleTTL)
	})

	t.Run("multi-tenant database options", func(t *testing.T) {
		tenantLimit := 250
		builder := NewManagerConfigBuilder(true, tenantLimit)

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, tenantLimit, options.MaxSize)
		assert.Equal(t, 30*time.Minute, options.IdleTTL)
	})

	t.Run(zeroLimitMultiTenantTest, func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 0)

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, 0, options.MaxSize)
		assert.Equal(t, 30*time.Minute, options.IdleTTL)
	})

	t.Run(negativeLimitMultiTenantTest, func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, -5)

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, -5, options.MaxSize)
		assert.Equal(t, 30*time.Minute, options.IdleTTL)
	})

	t.Run(largeLimitTenantTest, func(t *testing.T) {
		largeLimit := 10000
		builder := NewManagerConfigBuilder(true, largeLimit)

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, largeLimit, options.MaxSize)
		assert.Equal(t, 30*time.Minute, options.IdleTTL)
	})
}

func TestManagerConfigBuilderBuildMessagingOptions(t *testing.T) {
	t.Run("single-tenant messaging options", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)

		options := builder.BuildMessagingOptions()

		// With no operator override, single-tenant falls back to the documented
		// messaging.publisher defaults (maxcached=50, idlettl=10m).
		assert.Equal(t, 50, options.MaxPublishers)
		assert.Equal(t, 10*time.Minute, options.IdleTTL)
	})

	t.Run("multi-tenant messaging options", func(t *testing.T) {
		tenantLimit := 300
		builder := NewManagerConfigBuilder(true, tenantLimit)

		options := builder.BuildMessagingOptions()

		assert.Equal(t, tenantLimit, options.MaxPublishers)
		assert.Equal(t, 5*time.Minute, options.IdleTTL)
	})

	t.Run(zeroLimitMultiTenantTest, func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 0)

		options := builder.BuildMessagingOptions()

		assert.Equal(t, 0, options.MaxPublishers)
		assert.Equal(t, 5*time.Minute, options.IdleTTL)
	})

	t.Run(negativeLimitMultiTenantTest, func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, -3)

		options := builder.BuildMessagingOptions()

		assert.Equal(t, -3, options.MaxPublishers)
		assert.Equal(t, 5*time.Minute, options.IdleTTL)
	})

	t.Run(largeLimitTenantTest, func(t *testing.T) {
		largeLimit := 5000
		builder := NewManagerConfigBuilder(true, largeLimit)

		options := builder.BuildMessagingOptions()

		assert.Equal(t, largeLimit, options.MaxPublishers)
		assert.Equal(t, 5*time.Minute, options.IdleTTL)
	})

	t.Run("connection_timeout_propagated_to_options", func(t *testing.T) {
		// Single-tenant branch carries the configured per-publish timeout.
		st := NewManagerConfigBuilder(false, 100)
		st.connectionTimeout = 12 * time.Second
		assert.Equal(t, 12*time.Second, st.BuildMessagingOptions().ConnectionTimeout)

		// Multi-tenant branch carries it too.
		mt := NewManagerConfigBuilder(true, 100)
		mt.connectionTimeout = 8 * time.Second
		assert.Equal(t, 8*time.Second, mt.BuildMessagingOptions().ConnectionTimeout)

		// Unset leaves zero, so the client falls back to its 30s default.
		zero := NewManagerConfigBuilder(false, 100)
		assert.Equal(t, time.Duration(0), zero.BuildMessagingOptions().ConnectionTimeout)
	})
}

func TestManagerConfigBuilderHonorsConfigDefaults(t *testing.T) {
	// Config validation applies defaults: messaging.publisher.maxcached=50, messaging.publisher.idlettl=10m
	// cache.manager.maxsize=100, cache.manager.idlettl=15m, cache.manager.cleanupinterval=5m

	t.Run("single-tenant BuildMessagingOptions should honor config defaults not hardcoded 10", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)
		// Currently hardcodes MaxPublishers=10, should instead read from cfg.Messaging.Publisher.MaxCached
		opts := builder.BuildMessagingOptions()
		assert.Equal(t, 50, opts.MaxPublishers, "should honor messaging.publisher.maxcached config default of 50, not hardcode 10")
	})

	t.Run("single-tenant BuildMessagingOptions IdleTTL should honor config default 10m not hardcoded 30m", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)
		opts := builder.BuildMessagingOptions()
		assert.Equal(t, 10*time.Minute, opts.IdleTTL, "should honor messaging.publisher.idlettl config default of 10m, not hardcode 30m")
	})

	t.Run("single-tenant BuildCacheOptions should honor config defaults not hardcoded 10", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)
		opts := builder.BuildCacheOptions()
		assert.Equal(t, 100, opts.MaxSize, "should honor cache.manager.maxsize config default of 100, not hardcode 10")
	})

	t.Run("single-tenant BuildCacheOptions IdleTTL should honor config default 15m not hardcoded 1h", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)
		opts := builder.BuildCacheOptions()
		assert.Equal(t, 15*time.Minute, opts.IdleTTL, "should honor cache.manager.idlettl config default of 15m, not hardcode 1h")
	})

	t.Run("single-tenant BuildCacheOptions CleanupInterval should honor config default 5m not hardcoded 15m", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)
		opts := builder.BuildCacheOptions()
		assert.Equal(t, 5*time.Minute, opts.CleanupInterval, "should honor cache.manager.cleanupinterval config default of 5m, not hardcode 15m")
	})

	t.Run("operator override reaches messaging options", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)
		builder.publisherConfig = config.PublisherPoolConfig{MaxCached: 77, IdleTTL: 3 * time.Minute}
		opts := builder.BuildMessagingOptions()
		assert.Equal(t, 77, opts.MaxPublishers, "operator messaging.publisher.maxcached override must reach ManagerOptions")
		assert.Equal(t, 3*time.Minute, opts.IdleTTL, "operator messaging.publisher.idlettl override must reach ManagerOptions")
	})

	t.Run("operator override reaches cache options", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)
		builder.cacheConfig = config.CacheManagerConfig{MaxSize: 42, IdleTTL: 2 * time.Minute, CleanupInterval: 90 * time.Second}
		opts := builder.BuildCacheOptions()
		assert.Equal(t, 42, opts.MaxSize, "operator cache.manager.maxsize override must reach ManagerConfig")
		assert.Equal(t, 2*time.Minute, opts.IdleTTL, "operator cache.manager.idlettl override must reach ManagerConfig")
		assert.Equal(t, 90*time.Second, opts.CleanupInterval, "operator cache.manager.cleanupinterval override must reach ManagerConfig")
	})

	t.Run("multi-tenant cache MaxSize honors operator override over tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 250)
		builder.cacheConfig = config.CacheManagerConfig{MaxSize: 999}
		opts := builder.BuildCacheOptions()
		assert.Equal(t, 999, opts.MaxSize, "operator cache.manager.maxsize override must win over tenant limit")
	})

	t.Run("multi-tenant messaging MaxPublishers honors operator override over tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 250)
		builder.publisherConfig = config.PublisherPoolConfig{MaxCached: 888}
		opts := builder.BuildMessagingOptions()
		assert.Equal(t, 888, opts.MaxPublishers, "operator messaging.publisher.maxcached override must win over tenant limit")
	})
}

func TestManagerConfigBuilderIsMultiTenant(t *testing.T) {
	t.Run("single-tenant returns false", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 50)

		assert.False(t, builder.IsMultiTenant())
	})

	t.Run("multi-tenant returns true", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 50)

		assert.True(t, builder.IsMultiTenant())
	})
}

func TestManagerConfigBuilderTenantLimit(t *testing.T) {
	t.Run("returns configured tenant limit", func(t *testing.T) {
		limit := 150
		builder := NewManagerConfigBuilder(true, limit)

		assert.Equal(t, limit, builder.TenantLimit())
	})

	t.Run("returns zero tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 0)

		assert.Equal(t, 0, builder.TenantLimit())
	})

	t.Run("returns negative tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, -20)

		assert.Equal(t, -20, builder.TenantLimit())
	})
}

func TestManagerConfigBuilderStaticTenantCount(t *testing.T) {
	t.Run("defaults to zero", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 100)
		assert.Equal(t, 0, builder.StaticTenantCount())
	})

	t.Run("reflects assigned value", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 100)
		builder.staticTenantCount = 3
		assert.Equal(t, 3, builder.StaticTenantCount())
	})
}

// TestPoolBelowTenantCount guards the M3 non-breaking startup mitigation: an
// advisory WARN fires only when a per-tenant pool's MaxSize is genuinely below the
// number of statically-configured tenants. Dynamic sources (tenantCount == 0) and
// unbounded pools (maxSize <= 0) must never trip the warning.
func TestPoolBelowTenantCount(t *testing.T) {
	tests := []struct {
		name        string
		maxSize     int
		tenantCount int
		want        bool
	}{
		{name: "pool_below_tenants_warns", maxSize: 2, tenantCount: 5, want: true},
		{name: "pool_equals_tenants_ok", maxSize: 5, tenantCount: 5, want: false},
		{name: "pool_above_tenants_ok", maxSize: 10, tenantCount: 5, want: false},
		{name: "no_static_tenants_skipped", maxSize: 1, tenantCount: 0, want: false},
		{name: "negative_static_tenants_skipped", maxSize: 1, tenantCount: -3, want: false},
		{name: "unbounded_pool_skipped", maxSize: 0, tenantCount: 5, want: false},
		{name: "negative_pool_skipped", maxSize: -1, tenantCount: 5, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, poolBelowTenantCount(tc.maxSize, tc.tenantCount))
		})
	}
}

// TestResourceManagerFactoryWarnsOnUnderProvisionedPool exercises the WARN path
// end-to-end through the factory: an under-provisioned static-tenant deployment must
// still create a working manager (advisory, non-fatal) without panicking.
func TestResourceManagerFactoryWarnsOnUnderProvisionedPool(t *testing.T) {
	configBuilder := NewManagerConfigBuilder(true, 2) // tenant limit (pool MaxSize) = 2
	configBuilder.staticTenantCount = 5               // but 5 tenants statically configured
	factoryResolver := createTestFactoryResolver(t)
	log := logger.New("error", false)
	factory := NewResourceManagerFactory(factoryResolver, configBuilder, log)

	cfg := &config.Config{
		Database: config.DatabaseConfig{Type: "postgresql", Host: "localhost", Port: 5432},
	}
	resourceSource := config.NewTenantStore(cfg)

	var manager *database.DbManager
	require.NotPanics(t, func() {
		manager = factory.CreateDatabaseManager(resourceSource)
	})
	assert.NotNil(t, manager, "under-provisioned pool must still yield a working manager (WARN is advisory)")
}

func TestManagerConfigBuilderBuildCacheOptions(t *testing.T) {
	t.Run("single-tenant cache options", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)

		options := builder.BuildCacheOptions()

		// With no operator override, single-tenant falls back to the documented
		// cache.manager defaults (maxsize=100, idlettl=15m, cleanupinterval=5m).
		assert.Equal(t, 100, options.MaxSize)
		assert.Equal(t, 15*time.Minute, options.IdleTTL)
		assert.Equal(t, 5*time.Minute, options.CleanupInterval)
	})

	t.Run("multi-tenant cache options", func(t *testing.T) {
		tenantLimit := 250
		builder := NewManagerConfigBuilder(true, tenantLimit)

		options := builder.BuildCacheOptions()

		assert.Equal(t, tenantLimit, options.MaxSize)
		assert.Equal(t, 15*time.Minute, options.IdleTTL)
		assert.Equal(t, 5*time.Minute, options.CleanupInterval)
	})

	t.Run(zeroLimitMultiTenantTest, func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 0)

		options := builder.BuildCacheOptions()

		assert.Equal(t, 0, options.MaxSize)
		assert.Equal(t, 15*time.Minute, options.IdleTTL)
		assert.Equal(t, 5*time.Minute, options.CleanupInterval)
	})

	t.Run(negativeLimitMultiTenantTest, func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, -3)

		options := builder.BuildCacheOptions()

		assert.Equal(t, -3, options.MaxSize)
		assert.Equal(t, 15*time.Minute, options.IdleTTL)
		assert.Equal(t, 5*time.Minute, options.CleanupInterval)
	})

	t.Run(largeLimitTenantTest, func(t *testing.T) {
		largeLimit := 5000
		builder := NewManagerConfigBuilder(true, largeLimit)

		options := builder.BuildCacheOptions()

		assert.Equal(t, largeLimit, options.MaxSize)
		assert.Equal(t, 15*time.Minute, options.IdleTTL)
		assert.Equal(t, 5*time.Minute, options.CleanupInterval)
	})
}

func TestManagerConfigBuilderConsistency(t *testing.T) {
	t.Run("single-tenant configuration consistency", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 999) // Tenant limit should be ignored

		dbOptions := builder.BuildDatabaseOptions()
		msgOptions := builder.BuildMessagingOptions()

		// Single-tenant ignores tenantLimit: DB keeps its fixed size, messaging
		// falls back to the documented messaging.publisher.maxcached default (50).
		assert.Equal(t, 10, dbOptions.MaxSize)
		assert.Equal(t, 50, msgOptions.MaxPublishers)

		// Database has longer TTL (1h) than messaging (10m) in single-tenant mode
		assert.True(t, dbOptions.IdleTTL > msgOptions.IdleTTL)
	})

	t.Run("multi-tenant configuration consistency", func(t *testing.T) {
		tenantLimit := 200
		builder := NewManagerConfigBuilder(true, tenantLimit)

		dbOptions := builder.BuildDatabaseOptions()
		msgOptions := builder.BuildMessagingOptions()

		// Multi-tenant uses tenant limit for both
		assert.Equal(t, tenantLimit, dbOptions.MaxSize)
		assert.Equal(t, tenantLimit, msgOptions.MaxPublishers)

		// Database has longer TTL than messaging in multi-tenant mode
		assert.True(t, dbOptions.IdleTTL > msgOptions.IdleTTL)
	})

	t.Run("builder instance immutability", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 100)

		// Multiple calls should return consistent results
		dbOptions1 := builder.BuildDatabaseOptions()
		dbOptions2 := builder.BuildDatabaseOptions()
		msgOptions1 := builder.BuildMessagingOptions()
		msgOptions2 := builder.BuildMessagingOptions()

		assert.Equal(t, dbOptions1, dbOptions2)
		assert.Equal(t, msgOptions1, msgOptions2)

		// Getters should return same values
		assert.Equal(t, builder.IsMultiTenant(), builder.IsMultiTenant())
		assert.Equal(t, builder.TenantLimit(), builder.TenantLimit())
	})
}

func TestManagerConfigBuilderEdgeCases(t *testing.T) {
	t.Run("configuration differences between modes", func(t *testing.T) {
		singleTenant := NewManagerConfigBuilder(false, 100)
		multiTenant := NewManagerConfigBuilder(true, 100)

		singleDbOpts := singleTenant.BuildDatabaseOptions()
		multiDbOpts := multiTenant.BuildDatabaseOptions()
		singleMsgOpts := singleTenant.BuildMessagingOptions()
		multiMsgOpts := multiTenant.BuildMessagingOptions()

		// Verify different behaviors between modes
		assert.NotEqual(t, singleDbOpts.MaxSize, multiDbOpts.MaxSize)
		assert.NotEqual(t, singleDbOpts.IdleTTL, multiDbOpts.IdleTTL)
		assert.NotEqual(t, singleMsgOpts.MaxPublishers, multiMsgOpts.MaxPublishers)
		assert.NotEqual(t, singleMsgOpts.IdleTTL, multiMsgOpts.IdleTTL)
	})

	t.Run("expected TTL relationships", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 100)

		dbOptions := builder.BuildDatabaseOptions()
		msgOptions := builder.BuildMessagingOptions()

		// Multi-tenant: DB (30min) > Messaging (5min)
		assert.Equal(t, 30*time.Minute, dbOptions.IdleTTL)
		assert.Equal(t, 5*time.Minute, msgOptions.IdleTTL)
		assert.True(t, dbOptions.IdleTTL > msgOptions.IdleTTL)
	})

	t.Run("single-tenant ignores tenant limit parameter", func(t *testing.T) {
		builder1 := NewManagerConfigBuilder(false, 1)
		builder2 := NewManagerConfigBuilder(false, 10000)

		// Both should produce identical options since single-tenant ignores tenantLimit
		dbOpts1 := builder1.BuildDatabaseOptions()
		dbOpts2 := builder2.BuildDatabaseOptions()
		msgOpts1 := builder1.BuildMessagingOptions()
		msgOpts2 := builder2.BuildMessagingOptions()

		assert.Equal(t, dbOpts1, dbOpts2)
		assert.Equal(t, msgOpts1, msgOpts2)

		// But tenantLimit getter should still return the configured value
		assert.NotEqual(t, builder1.TenantLimit(), builder2.TenantLimit())
	})
}

func TestResourceManagerFactoryCreateDatabaseManager(t *testing.T) {
	t.Run("single-tenant creates database manager", func(t *testing.T) {
		configBuilder := NewManagerConfigBuilder(false, 50)
		factoryResolver := createTestFactoryResolver(t)
		log := logger.New("error", false)
		factory := NewResourceManagerFactory(factoryResolver, configBuilder, log)

		cfg := &config.Config{
			Database: config.DatabaseConfig{
				Type: "postgresql",
				Host: "localhost",
				Port: 5432,
			},
		}
		resourceSource := config.NewTenantStore(cfg)

		manager := factory.CreateDatabaseManager(resourceSource)

		assert.NotNil(t, manager)
	})

	t.Run("multi-tenant creates database manager", func(t *testing.T) {
		configBuilder := NewManagerConfigBuilder(true, 100)
		factoryResolver := createTestFactoryResolver(t)
		log := logger.New("error", false)
		factory := NewResourceManagerFactory(factoryResolver, configBuilder, log)

		cfg := &config.Config{
			Database: config.DatabaseConfig{
				Type: "postgresql",
				Host: "localhost",
				Port: 5432,
			},
		}
		resourceSource := config.NewTenantStore(cfg)

		manager := factory.CreateDatabaseManager(resourceSource)

		assert.NotNil(t, manager)
	})
}

func TestResourceManagerFactoryCreateMessagingManager(t *testing.T) {
	t.Run("single-tenant creates messaging manager", func(t *testing.T) {
		configBuilder := NewManagerConfigBuilder(false, 50)
		factoryResolver := createTestFactoryResolver(t)
		log := logger.New("error", false)
		factory := NewResourceManagerFactory(factoryResolver, configBuilder, log)

		cfg := &config.Config{
			Messaging: config.MessagingConfig{
				Broker: config.BrokerConfig{URL: "amqp://guest:guest@localhost:5672/"},
			},
		}
		resourceSource := config.NewTenantStore(cfg)

		manager := factory.CreateMessagingManager(resourceSource)

		assert.NotNil(t, manager)
	})

	t.Run("multi-tenant creates messaging manager", func(t *testing.T) {
		configBuilder := NewManagerConfigBuilder(true, 100)
		factoryResolver := createTestFactoryResolver(t)
		log := logger.New("error", false)
		factory := NewResourceManagerFactory(factoryResolver, configBuilder, log)

		cfg := &config.Config{
			Messaging: config.MessagingConfig{
				Broker: config.BrokerConfig{URL: "amqp://guest:guest@localhost:5672/"},
			},
		}
		resourceSource := config.NewTenantStore(cfg)

		manager := factory.CreateMessagingManager(resourceSource)

		assert.NotNil(t, manager)
	})
}

func TestResourceManagerFactoryCreateCacheManager(t *testing.T) {
	t.Run("single-tenant creates cache manager", func(t *testing.T) {
		configBuilder := NewManagerConfigBuilder(false, 50)
		factoryResolver := createTestFactoryResolver(t)
		log := logger.New("error", false)
		factory := NewResourceManagerFactory(factoryResolver, configBuilder, log)

		manager := factory.CreateCacheManager(nil)

		assert.NotNil(t, manager)
	})

	t.Run("multi-tenant creates cache manager", func(t *testing.T) {
		configBuilder := NewManagerConfigBuilder(true, 100)
		factoryResolver := createTestFactoryResolver(t)
		log := logger.New("error", false)
		factory := NewResourceManagerFactory(factoryResolver, configBuilder, log)

		manager := factory.CreateCacheManager(nil)

		assert.NotNil(t, manager)
	})

	t.Run("creates cache manager with default connector", func(t *testing.T) {
		configBuilder := NewManagerConfigBuilder(false, 50)
		factoryResolver := NewFactoryResolver(&Options{
			CacheConnector: nil, // Uses default connector from factory resolver
		})
		log := logger.New("error", false)
		factory := NewResourceManagerFactory(factoryResolver, configBuilder, log)

		manager := factory.CreateCacheManager(nil)

		// Manager creation succeeds with default connector
		assert.NotNil(t, manager)
	})

	t.Run("creates cache manager despite connector errors", func(t *testing.T) {
		configBuilder := NewManagerConfigBuilder(false, 50)
		expectedErr := errors.New("cache connector failed")
		factoryResolver := NewFactoryResolver(&Options{
			CacheConnector: func(_ context.Context, _ string) (cache.Cache, error) {
				return nil, expectedErr
			},
		})
		log := logger.New("error", false)
		factory := NewResourceManagerFactory(factoryResolver, configBuilder, log)

		manager := factory.CreateCacheManager(nil)

		// Manager creation succeeds even with failing connector
		assert.NotNil(t, manager)

		// Verify Get() operations fail with the connector error
		ctx := context.Background()
		_, err := manager.Get(ctx, "test-key")
		assert.Error(t, err)
		assert.ErrorContains(t, err, "cache connector failed")
	})
}

func TestResourceManagerFactoryLogFactoryInfo(t *testing.T) {
	t.Run("logs custom factories when provided", func(_ *testing.T) {
		configBuilder := NewManagerConfigBuilder(false, 50)
		factoryResolver := NewFactoryResolver(&Options{
			DatabaseConnector: func(_ *config.DatabaseConfig, _ logger.Logger) (database.Interface, error) {
				return &testmocks.MockDatabase{}, nil
			},
		})
		log := logger.New("error", false)
		factory := NewResourceManagerFactory(factoryResolver, configBuilder, log)

		// Should not panic
		factory.LogFactoryInfo()
	})

	t.Run("logs default factories when none provided", func(_ *testing.T) {
		configBuilder := NewManagerConfigBuilder(false, 50)
		factoryResolver := NewFactoryResolver(&Options{})
		log := logger.New("error", false)
		factory := NewResourceManagerFactory(factoryResolver, configBuilder, log)

		// Should not panic
		factory.LogFactoryInfo()
	})
}

// Helper function to create a test factory resolver with working connectors
func createTestFactoryResolver(t *testing.T) *FactoryResolver {
	t.Helper()
	return NewFactoryResolver(&Options{
		DatabaseConnector: func(_ *config.DatabaseConfig, _ logger.Logger) (database.Interface, error) {
			return &testmocks.MockDatabase{}, nil
		},
		MessagingClientFactory: func(_ string, _ logger.Logger) messaging.AMQPClient {
			return testmocks.NewMockAMQPClient()
		},
		CacheConnector: func(_ context.Context, _ string) (cache.Cache, error) {
			return &mockCacheInstance{}, nil
		},
	})
}
