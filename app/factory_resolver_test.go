package app

import (
	"context"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/stretchr/testify/assert"
)

const (
	testCacheKey        = "test-key"
	notConfiguredErrMsg = "error should be 'not configured' type"
)

func TestFactoryResolverCacheConnector(t *testing.T) {
	t.Run("returns default connector when options are nil", func(t *testing.T) {
		resolver := NewFactoryResolver(nil)

		connector := resolver.CacheConnector(&stubTenantResource{}, logger.New("debug", true))

		assert.NotNil(t, connector)

		// Default connector should return "not configured" error (stub returns Enabled=false)
		c, err := connector(context.Background(), testCacheKey)
		assert.Nil(t, c)
		assert.Error(t, err)
		assert.True(t, config.IsNotConfigured(err), notConfiguredErrMsg)
	})

	t.Run("returns default connector when cache connector option is nil", func(t *testing.T) {
		opts := &Options{
			CacheConnector: nil,
		}
		resolver := NewFactoryResolver(opts)

		connector := resolver.CacheConnector(&stubTenantResource{}, logger.New("debug", true))

		assert.NotNil(t, connector)

		// Default connector should return "not configured" error
		c, err := connector(context.Background(), testCacheKey)
		assert.Nil(t, c)
		assert.Error(t, err)
		assert.True(t, config.IsNotConfigured(err), notConfiguredErrMsg)
	})

	t.Run("returns custom connector from options", func(t *testing.T) {
		customConnectorCalled := false
		expectedCache := &mockCacheInstance{}

		opts := &Options{
			CacheConnector: func(_ context.Context, key string) (cache.Cache, error) {
				customConnectorCalled = true
				assert.Equal(t, testCacheKey, key)
				return expectedCache, nil
			},
		}

		resolver := NewFactoryResolver(opts)
		connector := resolver.CacheConnector(&stubTenantResource{}, logger.New("debug", true))

		assert.NotNil(t, connector)

		// Custom connector should be called
		result, err := connector(context.Background(), testCacheKey)
		assert.NoError(t, err)
		assert.Equal(t, expectedCache, result)
		assert.True(t, customConnectorCalled, "custom connector should have been called")
	})

	t.Run("custom connector can return errors", func(t *testing.T) {
		expectedError := assert.AnError

		opts := &Options{
			CacheConnector: func(_ context.Context, _ string) (cache.Cache, error) {
				return nil, expectedError
			},
		}

		resolver := NewFactoryResolver(opts)
		connector := resolver.CacheConnector(&stubTenantResource{}, logger.New("debug", true))

		c, err := connector(context.Background(), testCacheKey)
		assert.Nil(t, c)
		assert.Equal(t, expectedError, err)
	})

	t.Run("default connector with disabled cache returns not_configured error", func(t *testing.T) {
		resolver := NewFactoryResolver(nil)
		// stubTenantResource returns Enabled=false
		connector := resolver.CacheConnector(&stubTenantResource{}, logger.New("debug", true))

		_, err := connector(context.Background(), testCacheKey)

		assert.Error(t, err)

		// Check that it's a ConfigError with "not_configured"
		assert.True(t, config.IsNotConfigured(err), notConfiguredErrMsg)
	})
}

func TestFactoryResolverHasCustomFactories(t *testing.T) {
	t.Run("returns false when no custom factories", func(t *testing.T) {
		resolver := NewFactoryResolver(nil)
		assert.False(t, resolver.HasCustomFactories())

		resolver = NewFactoryResolver(&Options{})
		assert.False(t, resolver.HasCustomFactories())
	})

	t.Run("returns true when cache connector is provided", func(t *testing.T) {
		opts := &Options{
			CacheConnector: func(_ context.Context, _ string) (cache.Cache, error) {
				return nil, nil
			},
		}

		resolver := NewFactoryResolver(opts)
		assert.True(t, resolver.HasCustomFactories())
	})
}

// mockCacheInstance is a minimal mock implementation of cache.Cache for testing
type mockCacheInstance struct{}

func (m *mockCacheInstance) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func (m *mockCacheInstance) Set(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	return nil
}

func (m *mockCacheInstance) GetOrSet(_ context.Context, _ string, value []byte, _ time.Duration) (data []byte, loaded bool, err error) {
	return value, true, nil
}

func (m *mockCacheInstance) CompareAndSet(_ context.Context, _ string, _, _ []byte, _ time.Duration) (bool, error) {
	return true, nil
}

func (m *mockCacheInstance) Delete(_ context.Context, _ string) error {
	return nil
}

func (m *mockCacheInstance) Health(_ context.Context) error {
	return nil
}

func (m *mockCacheInstance) Stats() (map[string]any, error) {
	return map[string]any{}, nil
}

func (m *mockCacheInstance) Close() error {
	return nil
}

// TestFactoryResolverDefensiveValidation tests the defensive validation paths in newRedisConnector
func TestFactoryResolverDefensiveValidation(t *testing.T) {
	t.Run("nil cacheCfg returned from TenantStore", func(t *testing.T) {
		// Mock TenantStore that returns (nil, nil) from CacheConfig
		mockStore := &mockTenantStoreNilCacheCfg{}

		resolver := NewFactoryResolver(nil)
		connector := resolver.CacheConnector(mockStore, logger.New("debug", true))

		c, err := connector(context.Background(), testCacheKey)

		assert.Nil(t, c)
		assert.Error(t, err)

		// Should return typed ConfigError with "invalid" category
		var configErr *config.ConfigError
		assert.ErrorAs(t, err, &configErr)
		assert.Equal(t, "invalid", configErr.Category)
		assert.Contains(t, err.Error(), "configuration is nil")
	})

	t.Run("cache disabled (Enabled=false)", func(t *testing.T) {
		// Mock TenantStore that returns Enabled=false
		mockStore := &mockTenantStoreCacheDisabled{}

		resolver := NewFactoryResolver(nil)
		connector := resolver.CacheConnector(mockStore, logger.New("debug", true))

		c, err := connector(context.Background(), testCacheKey)

		assert.Nil(t, c)
		assert.Error(t, err)

		// Should return typed ConfigError with "not_configured" category
		assert.True(t, config.IsNotConfigured(err), "error should be 'not configured' type")

		var configErr *config.ConfigError
		assert.ErrorAs(t, err, &configErr)
		assert.Equal(t, "not_configured", configErr.Category)
		assert.Equal(t, "cache", configErr.Field)
	})

	t.Run("invalid cache type (not redis)", func(t *testing.T) {
		// Mock TenantStore that returns Type="memcached"
		mockStore := &mockTenantStoreInvalidType{}

		resolver := NewFactoryResolver(nil)
		connector := resolver.CacheConnector(mockStore, logger.New("debug", true))

		c, err := connector(context.Background(), testCacheKey)

		assert.Nil(t, c)
		assert.Error(t, err)

		// Should return typed ConfigError with "invalid" category
		var configErr *config.ConfigError
		assert.ErrorAs(t, err, &configErr)
		assert.Equal(t, "invalid", configErr.Category)
		assert.Equal(t, "cache.type", configErr.Field)
		assert.Contains(t, err.Error(), "memcached")
		assert.Contains(t, err.Error(), "redis")
	})

	t.Run("empty Redis host", func(t *testing.T) {
		// Mock TenantStore that returns Redis.Host=""
		mockStore := &mockTenantStoreEmptyHost{}

		resolver := NewFactoryResolver(nil)
		connector := resolver.CacheConnector(mockStore, logger.New("debug", true))

		c, err := connector(context.Background(), testCacheKey)

		assert.Nil(t, c)
		assert.Error(t, err)

		// Should return typed ConfigError with "missing" category
		var configErr *config.ConfigError
		assert.ErrorAs(t, err, &configErr)
		assert.Equal(t, "missing", configErr.Category)
		assert.Equal(t, "cache.redis.host", configErr.Field)
		assert.Contains(t, err.Error(), "CACHE_REDIS_HOST")
	})
}

// Mock TenantStore implementations for defensive validation tests

type mockTenantStoreNilCacheCfg struct{}

func (m *mockTenantStoreNilCacheCfg) CacheConfig(_ context.Context, _ string) (*config.CacheConfig, error) {
	// Returns (nil, nil) to trigger defensive nil check
	return nil, nil
}

func (m *mockTenantStoreNilCacheCfg) DBConfig(_ context.Context, _ string) (*config.DatabaseConfig, error) {
	return nil, nil
}

func (m *mockTenantStoreNilCacheCfg) BrokerURL(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *mockTenantStoreNilCacheCfg) IsDynamic() bool {
	return false
}

type mockTenantStoreCacheDisabled struct{}

func (m *mockTenantStoreCacheDisabled) CacheConfig(_ context.Context, _ string) (*config.CacheConfig, error) {
	return &config.CacheConfig{
		Enabled: false, // Cache disabled
		Type:    "redis",
	}, nil
}

func (m *mockTenantStoreCacheDisabled) DBConfig(_ context.Context, _ string) (*config.DatabaseConfig, error) {
	return nil, nil
}

func (m *mockTenantStoreCacheDisabled) BrokerURL(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *mockTenantStoreCacheDisabled) IsDynamic() bool {
	return false
}

type mockTenantStoreInvalidType struct{}

func (m *mockTenantStoreInvalidType) CacheConfig(_ context.Context, _ string) (*config.CacheConfig, error) {
	return &config.CacheConfig{
		Enabled: true,
		Type:    "memcached", // Invalid type (only "redis" supported)
	}, nil
}

func (m *mockTenantStoreInvalidType) DBConfig(_ context.Context, _ string) (*config.DatabaseConfig, error) {
	return nil, nil
}

func (m *mockTenantStoreInvalidType) BrokerURL(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *mockTenantStoreInvalidType) IsDynamic() bool {
	return false
}

type mockTenantStoreEmptyHost struct{}

func (m *mockTenantStoreEmptyHost) CacheConfig(_ context.Context, _ string) (*config.CacheConfig, error) {
	return &config.CacheConfig{
		Enabled: true,
		Type:    "redis",
		Redis: config.RedisConfig{
			Host: "", // Empty host - required field missing
			Port: 6379,
		},
	}, nil
}

func (m *mockTenantStoreEmptyHost) DBConfig(_ context.Context, _ string) (*config.DatabaseConfig, error) {
	return nil, nil
}

func (m *mockTenantStoreEmptyHost) BrokerURL(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *mockTenantStoreEmptyHost) IsDynamic() bool {
	return false
}
