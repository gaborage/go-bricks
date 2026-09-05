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
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

const (
	zeroLimitMultiTenantTest     = "multi-tenant with zero tenant limit"
	negativeLimitMultiTenantTest = "multi-tenant with negative tenant limit"
	largeLimitTenantTest         = "large tenant limit"
)

var (
	validSingleTenantDBConfig        = config.DatabaseManagerConfig{MaxSize: 10, IdleTTL: time.Hour}
	validMultiTenantDBConfig         = config.DatabaseManagerConfig{IdleTTL: 30 * time.Minute}
	validSingleTenantPublisherConfig = config.PublisherPoolConfig{MaxCached: 50, IdleTTL: time.Hour}
	validMultiTenantPublisherConfig  = config.PublisherPoolConfig{IdleTTL: 10 * time.Minute}
	validMultiTenantCacheConfig      = config.CacheManagerConfig{IdleTTL: 15 * time.Minute, CleanupInterval: 5 * time.Minute}
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
		builder.dbConfig = validSingleTenantDBConfig

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, 10, options.MaxSize)
		assert.Equal(t, 1*time.Hour, options.IdleTTL)
	})

	t.Run("multi-tenant database options", func(t *testing.T) {
		tenantLimit := 250
		builder := NewManagerConfigBuilder(true, tenantLimit)
		builder.dbConfig = validMultiTenantDBConfig

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, tenantLimit, options.MaxSize)
		assert.Equal(t, 30*time.Minute, options.IdleTTL)
	})

	t.Run(zeroLimitMultiTenantTest, func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 0)
		builder.dbConfig = validMultiTenantDBConfig

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, 0, options.MaxSize)
		assert.Equal(t, 30*time.Minute, options.IdleTTL)
	})

	t.Run(negativeLimitMultiTenantTest, func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, -5)
		builder.dbConfig = validMultiTenantDBConfig

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, -5, options.MaxSize)
		assert.Equal(t, 30*time.Minute, options.IdleTTL)
	})

	t.Run(largeLimitTenantTest, func(t *testing.T) {
		largeLimit := 10000
		builder := NewManagerConfigBuilder(true, largeLimit)
		builder.dbConfig = validMultiTenantDBConfig

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, largeLimit, options.MaxSize)
		assert.Equal(t, 30*time.Minute, options.IdleTTL)
	})
}

func TestManagerConfigBuilderScalesZeroMaxSizeToTenantLimitInMultiTenant(t *testing.T) {
	b := NewManagerConfigBuilder(true, 250)
	// Multi-tenant zero is deliberate (config preserves it, #661): scale to the limit.
	assert.Equal(t, 250, b.BuildDatabaseOptions().MaxSize)
	assert.Equal(t, 250, b.BuildMessagingOptions().MaxPublishers)
	assert.Equal(t, 250, b.BuildCacheOptions().MaxSize)
}

// TestBuildMessagingOptionsTenantStamps pins the one input that decides whether
// consumers read a tenant stamp: both multitenant.enabled AND messaging.tenancy:
// shared. Either alone leaves stamps off — single-tenant has no stamp to read, and
// per-tenant tenancy already knows the tenant from its replay key.
func TestBuildMessagingOptionsTenantStamps(t *testing.T) {
	tests := []struct {
		name        string
		multitenant bool
		tenancy     string
		want        bool
	}{
		{name: "multitenant_shared", multitenant: true, tenancy: config.TenancyShared, want: true},
		{name: "multitenant_per_tenant", multitenant: true, tenancy: config.TenancyPerTenant},
		{name: "single_tenant_shared", multitenant: false, tenancy: config.TenancyShared},
		{name: "single_tenant_per_tenant", multitenant: false, tenancy: config.TenancyPerTenant},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Multitenant: config.MultitenantConfig{Enabled: tt.multitenant},
				Messaging:   config.MessagingConfig{Tenancy: tt.tenancy},
			}

			got := newManagerConfigBuilderFromConfig(cfg).BuildMessagingOptions().TenantStamps

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestManagerConfigBuilderPassesValidatedValuesThrough(t *testing.T) {
	b := NewManagerConfigBuilder(false, 0)
	b.dbConfig = config.DatabaseManagerConfig{MaxSize: 7, IdleTTL: 3 * time.Minute}
	opts := b.BuildDatabaseOptions()
	assert.Equal(t, 7, opts.MaxSize)
	assert.Equal(t, 3*time.Minute, opts.IdleTTL)
}

func TestManagerConfigBuilderBuildMessagingOptions(t *testing.T) {
	t.Run("single-tenant messaging options", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)
		builder.publisherConfig = validSingleTenantPublisherConfig

		options := builder.BuildMessagingOptions()

		assert.Equal(t, 50, options.MaxPublishers)
		assert.Equal(t, 1*time.Hour, options.IdleTTL)
	})

	t.Run("multi-tenant messaging options", func(t *testing.T) {
		tenantLimit := 300
		builder := NewManagerConfigBuilder(true, tenantLimit)
		builder.publisherConfig = validMultiTenantPublisherConfig

		options := builder.BuildMessagingOptions()

		assert.Equal(t, tenantLimit, options.MaxPublishers)
		assert.Equal(t, 10*time.Minute, options.IdleTTL)
	})

	t.Run(zeroLimitMultiTenantTest, func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 0)
		builder.publisherConfig = validMultiTenantPublisherConfig

		options := builder.BuildMessagingOptions()

		assert.Equal(t, 0, options.MaxPublishers)
		assert.Equal(t, 10*time.Minute, options.IdleTTL)
	})

	t.Run(negativeLimitMultiTenantTest, func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, -3)
		builder.publisherConfig = validMultiTenantPublisherConfig

		options := builder.BuildMessagingOptions()

		assert.Equal(t, -3, options.MaxPublishers)
		assert.Equal(t, 10*time.Minute, options.IdleTTL)
	})

	t.Run(largeLimitTenantTest, func(t *testing.T) {
		largeLimit := 5000
		builder := NewManagerConfigBuilder(true, largeLimit)
		builder.publisherConfig = validMultiTenantPublisherConfig

		options := builder.BuildMessagingOptions()

		assert.Equal(t, largeLimit, options.MaxPublishers)
		assert.Equal(t, 10*time.Minute, options.IdleTTL)
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

	t.Run("ready_timeout_propagated_to_options", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)
		builder.readyTimeout = 9 * time.Second
		assert.Equal(t, 9*time.Second, builder.BuildMessagingOptions().ReadyTimeout)

		zero := NewManagerConfigBuilder(false, 100)
		assert.Equal(t, time.Duration(0), zero.BuildMessagingOptions().ReadyTimeout)
	})

	t.Run("publish_timeout_propagated_to_options", func(t *testing.T) {
		// 41s is not any client or config default, so a dropped assignment shows
		// up as a zero rather than as a plausible-looking value.
		builder := NewManagerConfigBuilder(false, 100)
		builder.publishTimeout = 41 * time.Second
		assert.Equal(t, 41*time.Second, builder.BuildMessagingOptions().PublishTimeout)

		mt := NewManagerConfigBuilder(true, 100)
		mt.publishTimeout = 41 * time.Second
		assert.Equal(t, 41*time.Second, mt.BuildMessagingOptions().PublishTimeout)

		// Unset stays zero, which the client reads as unbounded.
		zero := NewManagerConfigBuilder(false, 100)
		assert.Equal(t, time.Duration(0), zero.BuildMessagingOptions().PublishTimeout)
	})

	t.Run("reconnect_delays_propagated_to_options", func(t *testing.T) {
		// Four distinct values so a cross-field swap is caught.
		b := NewManagerConfigBuilder(false, 100)
		b.reconnectDelay = 7 * time.Second
		b.reconnectMaxDelay = 90 * time.Second
		b.reInitDelay = 3 * time.Second
		b.resendDelay = 11 * time.Second

		opts := b.BuildMessagingOptions()
		assert.Equal(t, 7*time.Second, opts.ReconnectDelay)
		assert.Equal(t, 90*time.Second, opts.ReconnectMaxDelay)
		assert.Equal(t, 3*time.Second, opts.ReinitDelay)
		assert.Equal(t, 11*time.Second, opts.ResendDelay)

		// Unset leaves zero so the client keeps its own defaults.
		zero := NewManagerConfigBuilder(false, 100).BuildMessagingOptions()
		assert.Equal(t, time.Duration(0), zero.ReconnectDelay)
		assert.Equal(t, time.Duration(0), zero.ReconnectMaxDelay)
		assert.Equal(t, time.Duration(0), zero.ReinitDelay)
		assert.Equal(t, time.Duration(0), zero.ResendDelay)
	})
}

func TestManagerConfigBuilderHonorsConfigDefaults(t *testing.T) {
	t.Run("operator override reaches messaging options", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)
		builder.publisherConfig = config.PublisherPoolConfig{MaxCached: 77, IdleTTL: 3 * time.Minute, CleanupInterval: 45 * time.Second}
		opts := builder.BuildMessagingOptions()
		assert.Equal(t, 77, opts.MaxPublishers, "operator messaging.publisher.maxcached override must reach ManagerOptions")
		assert.Equal(t, 3*time.Minute, opts.IdleTTL, "operator messaging.publisher.idlettl override must reach ManagerOptions")
		assert.Equal(t, 45*time.Second, opts.CleanupInterval, "operator messaging.publisher.cleanupinterval override must reach ManagerOptions")
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

	t.Run("operator override reaches database options", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)
		builder.dbConfig = config.DatabaseManagerConfig{MaxSize: 33, IdleTTL: 8 * time.Minute, CleanupInterval: 90 * time.Second}
		opts := builder.BuildDatabaseOptions()
		assert.Equal(t, 33, opts.MaxSize, "operator database.manager.maxsize override must reach DbManagerOptions")
		assert.Equal(t, 8*time.Minute, opts.IdleTTL, "operator database.manager.idlettl override must reach DbManagerOptions")
		assert.Equal(t, 90*time.Second, opts.CleanupInterval, "operator database.manager.cleanupinterval override must reach DbManagerOptions")
	})

	t.Run("multi-tenant database MaxSize honors operator override over tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 250)
		builder.dbConfig = config.DatabaseManagerConfig{MaxSize: 777}
		opts := builder.BuildDatabaseOptions()
		assert.Equal(t, 777, opts.MaxSize, "operator database.manager.maxsize override must win over tenant limit")
	})

	t.Run("multi-tenant database zero dbConfig scales to tenant limit", func(t *testing.T) {
		// Pins #661: unset database.manager.maxsize must fall through to the tenant limit, not literal 0.
		builder := NewManagerConfigBuilder(true, 250)
		builder.dbConfig = validMultiTenantDBConfig
		opts := builder.BuildDatabaseOptions()
		assert.Equal(t, 250, opts.MaxSize, "unset database.manager.maxsize must scale to the tenant limit")
		assert.Equal(t, 30*time.Minute, opts.IdleTTL, "validated database.manager.idlettl must pass through unchanged")
	})

	t.Run("multi-tenant cache zero cacheConfig scales to tenant limit", func(t *testing.T) {
		// Pins #668: unset cache.manager.maxsize must scale to the tenant limit (>100), not cap at 100.
		builder := NewManagerConfigBuilder(true, 500)
		builder.cacheConfig = validMultiTenantCacheConfig
		opts := builder.BuildCacheOptions()
		assert.Equal(t, 500, opts.MaxSize, "unset cache.manager.maxsize must scale to the tenant limit")
		assert.Equal(t, 15*time.Minute, opts.IdleTTL, "validated cache.manager.idlettl must pass through unchanged")
		assert.Equal(t, 5*time.Minute, opts.CleanupInterval, "validated cache.manager.cleanupinterval must pass through unchanged")
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
		builder.cacheConfig = config.CacheManagerConfig{MaxSize: 100, IdleTTL: 15 * time.Minute, CleanupInterval: 5 * time.Minute}

		options := builder.BuildCacheOptions()

		assert.Equal(t, 100, options.MaxSize)
		assert.Equal(t, 15*time.Minute, options.IdleTTL)
		assert.Equal(t, 5*time.Minute, options.CleanupInterval)
	})

	t.Run("multi-tenant cache options", func(t *testing.T) {
		tenantLimit := 250
		builder := NewManagerConfigBuilder(true, tenantLimit)
		builder.cacheConfig = validMultiTenantCacheConfig

		options := builder.BuildCacheOptions()

		assert.Equal(t, tenantLimit, options.MaxSize)
		assert.Equal(t, 15*time.Minute, options.IdleTTL)
		assert.Equal(t, 5*time.Minute, options.CleanupInterval)
	})

	t.Run(zeroLimitMultiTenantTest, func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 0)
		builder.cacheConfig = validMultiTenantCacheConfig

		options := builder.BuildCacheOptions()

		assert.Equal(t, 0, options.MaxSize)
		assert.Equal(t, 15*time.Minute, options.IdleTTL)
		assert.Equal(t, 5*time.Minute, options.CleanupInterval)
	})

	t.Run(negativeLimitMultiTenantTest, func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, -3)
		builder.cacheConfig = validMultiTenantCacheConfig

		options := builder.BuildCacheOptions()

		assert.Equal(t, -3, options.MaxSize)
		assert.Equal(t, 15*time.Minute, options.IdleTTL)
		assert.Equal(t, 5*time.Minute, options.CleanupInterval)
	})

	t.Run(largeLimitTenantTest, func(t *testing.T) {
		largeLimit := 5000
		builder := NewManagerConfigBuilder(true, largeLimit)
		builder.cacheConfig = validMultiTenantCacheConfig

		options := builder.BuildCacheOptions()

		assert.Equal(t, largeLimit, options.MaxSize)
		assert.Equal(t, 15*time.Minute, options.IdleTTL)
		assert.Equal(t, 5*time.Minute, options.CleanupInterval)
	})
}

func TestManagerConfigBuilderConsistency(t *testing.T) {
	t.Run("single-tenant configuration consistency", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 999) // Tenant limit should be ignored: validated operator values win.
		builder.dbConfig = validSingleTenantDBConfig
		builder.publisherConfig = validSingleTenantPublisherConfig

		dbOptions := builder.BuildDatabaseOptions()
		msgOptions := builder.BuildMessagingOptions()

		assert.Equal(t, 10, dbOptions.MaxSize)
		assert.Equal(t, 50, msgOptions.MaxPublishers)

		// Invariant: the DB pool must never be shorter-lived than the messaging pool
		// (DB connections are heavier to re-establish).
		assert.GreaterOrEqual(t, dbOptions.IdleTTL, msgOptions.IdleTTL, "DB pool IdleTTL must be >= messaging pool IdleTTL in single-tenant mode")
	})

	t.Run("multi-tenant configuration consistency", func(t *testing.T) {
		tenantLimit := 200
		builder := NewManagerConfigBuilder(true, tenantLimit)
		builder.dbConfig = validMultiTenantDBConfig
		builder.publisherConfig = validMultiTenantPublisherConfig

		dbOptions := builder.BuildDatabaseOptions()
		msgOptions := builder.BuildMessagingOptions()

		// Multi-tenant uses tenant limit for both
		assert.Equal(t, tenantLimit, dbOptions.MaxSize)
		assert.Equal(t, tenantLimit, msgOptions.MaxPublishers)

		// Database has longer TTL than messaging in multi-tenant mode
		assert.Greater(t, dbOptions.IdleTTL, msgOptions.IdleTTL)
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

		// Getters keep reporting the constructor arguments after the repeated Build calls
		assert.True(t, builder.IsMultiTenant())
		assert.Equal(t, 100, builder.TenantLimit())
	})
}

func TestManagerConfigBuilderEdgeCases(t *testing.T) {
	t.Run("configuration differences between modes", func(t *testing.T) {
		singleTenant := NewManagerConfigBuilder(false, 100)
		singleTenant.dbConfig = validSingleTenantDBConfig
		singleTenant.publisherConfig = validSingleTenantPublisherConfig

		multiTenant := NewManagerConfigBuilder(true, 100)
		multiTenant.dbConfig = validMultiTenantDBConfig
		multiTenant.publisherConfig = validMultiTenantPublisherConfig

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
		builder.dbConfig = validMultiTenantDBConfig
		builder.publisherConfig = validMultiTenantPublisherConfig

		dbOptions := builder.BuildDatabaseOptions()
		msgOptions := builder.BuildMessagingOptions()

		// Multi-tenant: DB (30min) > Messaging (10min)
		assert.Equal(t, 30*time.Minute, dbOptions.IdleTTL)
		assert.Equal(t, 10*time.Minute, msgOptions.IdleTTL)
		assert.Greater(t, dbOptions.IdleTTL, msgOptions.IdleTTL)
	})

	t.Run("single-tenant ignores tenant limit parameter", func(t *testing.T) {
		builder1 := NewManagerConfigBuilder(false, 1)
		builder1.dbConfig = validSingleTenantDBConfig
		builder1.publisherConfig = validSingleTenantPublisherConfig

		builder2 := NewManagerConfigBuilder(false, 10000)
		builder2.dbConfig = validSingleTenantDBConfig
		builder2.publisherConfig = validSingleTenantPublisherConfig

		// Both should produce identical options since a validated operator value ignores tenantLimit
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

		manager, err := factory.CreateCacheManager(nil)

		require.NoError(t, err)
		assert.NotNil(t, manager)
	})

	t.Run("multi-tenant creates cache manager", func(t *testing.T) {
		configBuilder := NewManagerConfigBuilder(true, 100)
		factoryResolver := createTestFactoryResolver(t)
		log := logger.New("error", false)
		factory := NewResourceManagerFactory(factoryResolver, configBuilder, log)

		manager, err := factory.CreateCacheManager(nil)

		require.NoError(t, err)
		assert.NotNil(t, manager)
	})

	t.Run("creates cache manager with default connector", func(t *testing.T) {
		configBuilder := NewManagerConfigBuilder(false, 50)
		factoryResolver := NewFactoryResolver(&Options{
			CacheConnector: nil, // Uses default connector from factory resolver
		})
		log := logger.New("error", false)
		factory := NewResourceManagerFactory(factoryResolver, configBuilder, log)

		manager, err := factory.CreateCacheManager(nil)

		// Manager creation succeeds with default connector
		require.NoError(t, err)
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

		manager, err := factory.CreateCacheManager(nil)

		// A connector that fails at Get() time is not a construction failure
		require.NoError(t, err)
		require.NotNil(t, manager)

		// Verify Get() operations fail with the connector error
		ctx := context.Background()
		_, _, getErr := manager.Get(ctx, "test-key")
		require.Error(t, getErr)
		assert.ErrorContains(t, getErr, "cache connector failed")
	})
}

// TestResourceManagerFactoryCreateCacheManagerFailsClosed pins the fail-closed contract:
// BuildCacheOptions forwards a negative cache.manager.* value unclamped so
// cache.NewCacheManager rejects it, and the factory must surface that as an error rather
// than the bare nil that used to boot the service green with no cache.
func TestResourceManagerFactoryCreateCacheManagerFailsClosed(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(cfg *config.CacheManagerConfig)
		wantCause string
	}{
		{
			name:      "negative_maxsize",
			mutate:    func(cfg *config.CacheManagerConfig) { cfg.MaxSize = -1 },
			wantCause: "maxsize cannot be negative",
		},
		{
			name:      "negative_idlettl",
			mutate:    func(cfg *config.CacheManagerConfig) { cfg.IdleTTL = -time.Second },
			wantCause: "idlettl cannot be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configBuilder := NewManagerConfigBuilder(false, 50)
			tt.mutate(&configBuilder.cacheConfig)
			factory := NewResourceManagerFactory(createTestFactoryResolver(t), configBuilder, logger.New("error", false))

			manager, err := factory.CreateCacheManager(nil)

			require.Error(t, err)
			assert.Nil(t, manager, "a rejected configuration must not yield a usable manager")
			// %w, not %v: consumers must be able to unwrap to the cache package's cause.
			// Hoisted above the message clauses: it is an independent property, so a
			// clause mismatch must not hide it.
			assert.Error(t, errors.Unwrap(err), "the underlying cause must stay unwrappable")
			// "cache manager" is the WRAPPER's prefix (app/managers.go) and tt.wantCause
			// is the inner error's detail (cache/manager.go) — two code paths, so a
			// wrapper regression must not hide whether the cause still surfaces.
			assert.ErrorContains(t, err, "cache manager")
			require.ErrorContains(t, err, tt.wantCause)
		})
	}
}

// TestResourceManagerFactoryCreateCacheManagerReportsResolvedPoolSize covers the input that
// reaches the same failure without naming a cache.manager.* key: in multi-tenant mode an
// unset maxsize takes multitenant.limits.tenants, so a negative tenant limit lands here.
// The error has to report the resolved value or it points at a key nobody set.
func TestResourceManagerFactoryCreateCacheManagerReportsResolvedPoolSize(t *testing.T) {
	configBuilder := NewManagerConfigBuilder(true, -1)
	require.Zero(t, configBuilder.cacheConfig.MaxSize, "the substitution only happens for an unset maxsize")
	factory := NewResourceManagerFactory(createTestFactoryResolver(t), configBuilder, logger.New("error", false))

	manager, err := factory.CreateCacheManager(nil)

	require.Error(t, err)
	assert.Nil(t, manager)
	// The first two clauses are one format string in app/managers.go; the third is the
	// wrapped cache error. Neither wrapper clause may abort, or a prefix regression hides
	// whether the inner cause still reaches the operator.
	assert.ErrorContains(t, err, "maxsize=-1", "the resolved pool size must appear in the error")
	assert.ErrorContains(t, err, "multitenant.limits.tenants", "and the key it actually came from")
	require.ErrorContains(t, err, "maxsize cannot be negative")
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

// TestResourceManagerFactoryCreateCacheManagerWiresTheLogger pins that the factory hands its
// logger to cache.NewCacheManager, observed through the only thing the manager logs at
// construction: the late-cleanup-interval WARN under cache.manager keys.
func TestResourceManagerFactoryCreateCacheManagerWiresTheLogger(t *testing.T) {
	configBuilder := NewManagerConfigBuilder(false, 50)
	configBuilder.cacheConfig = config.CacheManagerConfig{MaxSize: 5, IdleTTL: time.Minute, CleanupInterval: 0}
	log := &recLogger{}
	factory := NewResourceManagerFactory(createTestFactoryResolver(t), configBuilder, log)

	manager, err := factory.CreateCacheManager(nil)
	require.NoError(t, err)
	defer func() { _ = manager.Close() }()

	var warns []recEvent
	for _, e := range log.events {
		if e.level == "warn" {
			warns = append(warns, e)
		}
	}
	require.Len(t, warns, 1, "the cache manager's advisory must reach the app logger")
	assert.Contains(t, warns[0].msg, "cache.manager.cleanupinterval is >= cache.manager.idlettl")
	assert.Equal(t, 5*time.Minute, warns[0].dur["cleanupinterval"])
}
