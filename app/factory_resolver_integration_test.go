//go:build integration

package app

import (
	"context"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/testing/containers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFactoryResolverRedisConnectorIntegration tests the Redis cache connector
func TestFactoryResolverRedisConnectorIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Start Redis container
	redisContainer := containers.MustStartRedisContainer(ctx, t, nil).WithCleanup(t)

	t.Run("single tenant - cache enabled", func(t *testing.T) {
		// Create config with Redis pointing to test container
		cfg := &config.Config{
			Cache: config.CacheConfig{
				Enabled: true,
				Redis: config.RedisConfig{
					Host:     redisContainer.Host(),
					Port:     redisContainer.Port(),
					Database: 0,
					PoolSize: 10,
				},
			},
		}

		// Create tenant store and factory resolver
		store := config.NewTenantStore(cfg)
		resolver := NewFactoryResolver(nil)
		log := logger.New("debug", true)

		// Get connector and create cache instance
		connector := resolver.CacheConnector(store, log)
		require.NotNil(t, connector)

		cache, err := connector(ctx, "") // Empty key for single-tenant
		require.NoError(t, err)
		require.NotNil(t, cache)

		// Test cache operations - Set/Get round-trip
		testKey := "test-key-single"
		testValue := []byte("test-value-single")

		err = cache.Set(ctx, testKey, testValue, time.Minute)
		assert.NoError(t, err)

		retrievedValue, err := cache.Get(ctx, testKey)
		assert.NoError(t, err)
		assert.Equal(t, testValue, retrievedValue)

		// Test cache expiration
		err = cache.Set(ctx, "short-ttl", []byte("expires-soon"), 100*time.Millisecond)
		assert.NoError(t, err)

		time.Sleep(200 * time.Millisecond)

		expiredValue, err := cache.Get(ctx, "short-ttl")
		assert.Error(t, err) // Should return error for expired/missing key
		assert.Empty(t, expiredValue)
	})

	t.Run("single tenant - cache disabled", func(t *testing.T) {
		// Create config with cache disabled
		cfg := &config.Config{
			Cache: config.CacheConfig{
				Enabled: false,
			},
		}

		store := config.NewTenantStore(cfg)
		resolver := NewFactoryResolver(nil)
		log := logger.New("debug", true)

		connector := resolver.CacheConnector(store, log)
		require.NotNil(t, connector)

		// Should return "not configured" error
		cache, err := connector(ctx, "")
		assert.Nil(t, cache)
		assert.Error(t, err)
		assert.True(t, config.IsNotConfigured(err), "error should be 'not configured' type")
	})

	t.Run("multi tenant - tenant exists with cache enabled", func(t *testing.T) {
		// Create config with multi-tenant setup
		cfg := &config.Config{
			Multitenant: config.MultitenantConfig{
				Enabled: true,
				Tenants: map[string]config.TenantEntry{
					"acme": {
						Cache: config.CacheConfig{
							Enabled: true,
							Redis: config.RedisConfig{
								Host:     redisContainer.Host(),
								Port:     redisContainer.Port(),
								Database: 1, // Different database for tenant isolation
								PoolSize: 10,
							},
						},
					},
				},
			},
		}

		store := config.NewTenantStore(cfg)
		resolver := NewFactoryResolver(nil)
		log := logger.New("debug", true)

		connector := resolver.CacheConnector(store, log)
		require.NotNil(t, connector)

		// Get cache for "acme" tenant
		cache, err := connector(ctx, "acme")
		require.NoError(t, err)
		require.NotNil(t, cache)

		// Test cache operations
		testKey := "test-key-acme"
		testValue := []byte("test-value-acme")

		err = cache.Set(ctx, testKey, testValue, time.Minute)
		assert.NoError(t, err)

		retrievedValue, err := cache.Get(ctx, testKey)
		assert.NoError(t, err)
		assert.Equal(t, testValue, retrievedValue)
	})

	t.Run("multi tenant - tenant not found", func(t *testing.T) {
		cfg := &config.Config{
			Multitenant: config.MultitenantConfig{
				Enabled: true,
				Tenants: map[string]config.TenantEntry{},
			},
		}

		store := config.NewTenantStore(cfg)
		resolver := NewFactoryResolver(nil)
		log := logger.New("debug", true)

		connector := resolver.CacheConnector(store, log)
		require.NotNil(t, connector)

		// Should return error for non-existent tenant
		cache, err := connector(ctx, "nonexistent")
		assert.Nil(t, cache)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "configuration not found")
	})

	t.Run("multi tenant - cache disabled for tenant", func(t *testing.T) {
		cfg := &config.Config{
			Multitenant: config.MultitenantConfig{
				Enabled: true,
				Tenants: map[string]config.TenantEntry{
					"globex": {
						Cache: config.CacheConfig{
							Enabled: false, // Cache disabled for this tenant
						},
					},
				},
			},
		}

		store := config.NewTenantStore(cfg)
		resolver := NewFactoryResolver(nil)
		log := logger.New("debug", true)

		connector := resolver.CacheConnector(store, log)
		require.NotNil(t, connector)

		// Should return "not enabled" error
		cache, err := connector(ctx, "globex")
		assert.Nil(t, cache)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not enabled")
	})

	t.Run("custom connector override", func(t *testing.T) {
		// Custom connector that always returns an error
		customError := assert.AnError
		customConnector := func(_ context.Context, _ string) (cache.Cache, error) {
			return nil, customError
		}

		opts := &Options{
			CacheConnector: customConnector,
		}

		resolver := NewFactoryResolver(opts)
		log := logger.New("debug", true)

		// Custom connector should be used instead of default
		connector := resolver.CacheConnector(&stubTenantResource{}, log)
		require.NotNil(t, connector)

		cache, err := connector(ctx, "test-key")
		assert.Nil(t, cache)
		assert.Equal(t, customError, err)
	})
}
