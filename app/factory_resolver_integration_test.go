//go:build integration

package app

import (
	"context"
	"testing"
	"time"

	cachepkg "github.com/gaborage/go-bricks/cache"
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

		c, err := connector(ctx, "") // Empty key for single-tenant
		require.NoError(t, err)
		require.NotNil(t, c)

		// Test cache operations - Set/Get round-trip
		testKey := "test-key-single"
		testValue := []byte("test-value-single")

		err = c.Set(ctx, testKey, testValue, time.Minute)
		assert.NoError(t, err)

		retrievedValue, err := c.Get(ctx, testKey)
		assert.NoError(t, err)
		assert.Equal(t, testValue, retrievedValue)

		// Test cache expiration
		err = c.Set(ctx, "short-ttl", []byte("expires-soon"), 200*time.Millisecond)
		assert.NoError(t, err)

		time.Sleep(300 * time.Millisecond)

		expiredValue, err := c.Get(ctx, "short-ttl")
		assert.ErrorIs(t, err, cachepkg.ErrNotFound, "expired key should return ErrNotFound")
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
		c, err := connector(ctx, "")
		assert.Nil(t, c)
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
		c, err := connector(ctx, "acme")
		require.NoError(t, err)
		require.NotNil(t, c)

		// Test cache operations
		testKey := "test-key-acme"
		testValue := []byte("test-value-acme")

		err = c.Set(ctx, testKey, testValue, time.Minute)
		assert.NoError(t, err)

		retrievedValue, err := c.Get(ctx, testKey)
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
		c, err := connector(ctx, "nonexistent")
		assert.Nil(t, c)
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
		c, err := connector(ctx, "globex")
		assert.Nil(t, c)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not enabled")
	})

	t.Run("custom connector override", func(t *testing.T) {
		// Custom connector that always returns an error
		customError := assert.AnError
		customConnector := func(_ context.Context, _ string) (cachepkg.Cache, error) {
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

		c, err := connector(ctx, "test-key")
		assert.Nil(t, c)
		assert.Equal(t, customError, err)
	})

	t.Run("CBOR serialization round-trip", func(t *testing.T) {
		// Create config with Redis
		cfg := &config.Config{
			Cache: config.CacheConfig{
				Enabled: true,
				Redis: config.RedisConfig{
					Host:     redisContainer.Host(),
					Port:     redisContainer.Port(),
					Database: 2,
					PoolSize: 10,
				},
			},
		}

		store := config.NewTenantStore(cfg)
		resolver := NewFactoryResolver(nil)
		log := logger.New("debug", true)

		connector := resolver.CacheConnector(store, log)
		c, err := connector(ctx, "")
		require.NoError(t, err)

		// Test CBOR serialization with complex struct
		type User struct {
			ID        int64
			Name      string
			Email     string
			CreatedAt time.Time
			Tags      []string
			Metadata  map[string]string
		}

		originalUser := User{
			ID:        12345,
			Name:      "Alice Smith",
			Email:     "alice@example.com",
			CreatedAt: time.Now().UTC().Truncate(time.Second),
			Tags:      []string{"premium", "verified"},
			Metadata:  map[string]string{"region": "us-west", "tier": "gold"},
		}

		// Marshal to CBOR
		data, err := cachepkg.Marshal(originalUser)
		require.NoError(t, err)

		// Store in Redis
		err = c.Set(ctx, "user:12345", data, time.Minute)
		require.NoError(t, err)

		// Retrieve from Redis
		retrievedData, err := c.Get(ctx, "user:12345")
		require.NoError(t, err)

		// Unmarshal from CBOR
		retrievedUser, err := cachepkg.Unmarshal[User](retrievedData)
		require.NoError(t, err)

		// Verify round-trip integrity
		assert.Equal(t, originalUser.ID, retrievedUser.ID)
		assert.Equal(t, originalUser.Name, retrievedUser.Name)
		assert.Equal(t, originalUser.Email, retrievedUser.Email)
		assert.Equal(t, originalUser.CreatedAt.Unix(), retrievedUser.CreatedAt.Unix())
		assert.Equal(t, originalUser.Tags, retrievedUser.Tags)
		assert.Equal(t, originalUser.Metadata, retrievedUser.Metadata)
	})

	t.Run("concurrent deduplication with GetOrSet", func(t *testing.T) {
		cfg := &config.Config{
			Cache: config.CacheConfig{
				Enabled: true,
				Redis: config.RedisConfig{
					Host:     redisContainer.Host(),
					Port:     redisContainer.Port(),
					Database: 3,
					PoolSize: 20,
				},
			},
		}

		store := config.NewTenantStore(cfg)
		resolver := NewFactoryResolver(nil)
		log := logger.New("debug", true)

		connector := resolver.CacheConnector(store, log)
		c, err := connector(ctx, "")
		require.NoError(t, err)

		// Spawn concurrent GetOrSet calls
		const numWorkers = 30
		results := make(chan bool, numWorkers)

		for i := 0; i < numWorkers; i++ {
			go func() {
				_, wasSet, err := c.GetOrSet(ctx, "dedupe:test", []byte("shared-value"), 30*time.Second)
				assert.NoError(t, err)
				results <- wasSet
			}()
		}

		// Collect results
		setCount := 0
		for i := 0; i < numWorkers; i++ {
			if <-results {
				setCount++
			}
		}

		// Exactly one worker should have set the value
		assert.Equal(t, 1, setCount, "Exactly one worker should set the value (deduplication)")
	})

	t.Run("distributed locking with CompareAndSet", func(t *testing.T) {
		cfg := &config.Config{
			Cache: config.CacheConfig{
				Enabled: true,
				Redis: config.RedisConfig{
					Host:     redisContainer.Host(),
					Port:     redisContainer.Port(),
					Database: 4,
					PoolSize: 10,
				},
			},
		}

		store := config.NewTenantStore(cfg)
		resolver := NewFactoryResolver(nil)
		log := logger.New("debug", true)

		connector := resolver.CacheConnector(store, log)
		c, err := connector(ctx, "")
		require.NoError(t, err)

		lockKey := "lock:resource:123"

		// Acquire lock (expectedValue=nil means "set only if key doesn't exist")
		acquired, err := c.CompareAndSet(ctx, lockKey, nil, []byte("worker-1"), 10*time.Second)
		require.NoError(t, err)
		assert.True(t, acquired, "First worker should acquire lock")

		// Try to acquire same lock (should fail)
		acquired, err = c.CompareAndSet(ctx, lockKey, nil, []byte("worker-2"), 10*time.Second)
		require.NoError(t, err)
		assert.False(t, acquired, "Second worker should NOT acquire lock")

		// Release lock (delete the key)
		err = c.Delete(ctx, lockKey)
		require.NoError(t, err)

		// Now second worker can acquire
		acquired, err = c.CompareAndSet(ctx, lockKey, nil, []byte("worker-2"), 10*time.Second)
		require.NoError(t, err)
		assert.True(t, acquired, "Second worker should acquire lock after release")
	})

	t.Run("cache health and stats", func(t *testing.T) {
		cfg := &config.Config{
			Cache: config.CacheConfig{
				Enabled: true,
				Redis: config.RedisConfig{
					Host:     redisContainer.Host(),
					Port:     redisContainer.Port(),
					Database: 5,
					PoolSize: 10,
				},
			},
		}

		store := config.NewTenantStore(cfg)
		resolver := NewFactoryResolver(nil)
		log := logger.New("debug", true)

		connector := resolver.CacheConnector(store, log)
		c, err := connector(ctx, "")
		require.NoError(t, err)

		// Health check
		err = c.Health(ctx)
		assert.NoError(t, err, "Health check should pass")

		// Perform some operations to generate stats
		_ = c.Set(ctx, "stats:test", []byte("value"), time.Minute)
		_, _ = c.Get(ctx, "stats:test")

		// Get stats
		stats, err := c.Stats()
		require.NoError(t, err)
		require.NotNil(t, stats)

		// Verify stats structure
		assert.Contains(t, stats, "redis_info", "Stats should contain Redis server info")
		assert.Contains(t, stats, "pool_hits", "Stats should contain pool hit count")
		assert.Contains(t, stats, "pool_misses", "Stats should contain pool miss count")
		assert.Contains(t, stats, "pool_total_conns", "Stats should contain total connections")
	})

	t.Run("multi-tenant cache isolation", func(t *testing.T) {
		// Create config with two tenants using different Redis databases
		cfg := &config.Config{
			Multitenant: config.MultitenantConfig{
				Enabled: true,
				Tenants: map[string]config.TenantEntry{
					"tenant-a": {
						Cache: config.CacheConfig{
							Enabled: true,
							Redis: config.RedisConfig{
								Host:     redisContainer.Host(),
								Port:     redisContainer.Port(),
								Database: 6, // Tenant A uses database 6
								PoolSize: 10,
							},
						},
					},
					"tenant-b": {
						Cache: config.CacheConfig{
							Enabled: true,
							Redis: config.RedisConfig{
								Host:     redisContainer.Host(),
								Port:     redisContainer.Port(),
								Database: 7, // Tenant B uses database 7
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

		// Get cache instances for both tenants
		cacheA, err := connector(ctx, "tenant-a")
		require.NoError(t, err)
		require.NotNil(t, cacheA)

		cacheB, err := connector(ctx, "tenant-b")
		require.NoError(t, err)
		require.NotNil(t, cacheB)

		// Use the SAME key for both tenants
		sharedKey := "isolation:test:key"
		valueA := []byte("tenant-a-value")
		valueB := []byte("tenant-b-value")

		// Set value in tenant A's cache
		err = cacheA.Set(ctx, sharedKey, valueA, time.Minute)
		require.NoError(t, err)

		// Set value in tenant B's cache (same key, different value)
		err = cacheB.Set(ctx, sharedKey, valueB, time.Minute)
		require.NoError(t, err)

		// Retrieve from tenant A - should get tenant A's value
		retrievedA, err := cacheA.Get(ctx, sharedKey)
		require.NoError(t, err)
		assert.Equal(t, valueA, retrievedA, "Tenant A should retrieve its own value")

		// Retrieve from tenant B - should get tenant B's value (not tenant A's)
		retrievedB, err := cacheB.Get(ctx, sharedKey)
		require.NoError(t, err)
		assert.Equal(t, valueB, retrievedB, "Tenant B should retrieve its own value, not tenant A's")

		// Delete from tenant A
		err = cacheA.Delete(ctx, sharedKey)
		require.NoError(t, err)

		// Tenant A's key should be gone
		_, err = cacheA.Get(ctx, sharedKey)
		assert.ErrorIs(t, err, cachepkg.ErrNotFound, "Tenant A's key should be deleted")

		// Tenant B's key should still exist (isolation verified)
		retrievedB, err = cacheB.Get(ctx, sharedKey)
		require.NoError(t, err)
		assert.Equal(t, valueB, retrievedB, "Tenant B's key should still exist after tenant A deletion")
	})
}
