package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
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
		require.Error(t, err)
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
		require.Error(t, err)
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
		require.NoError(t, err)
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

		require.Error(t, err)

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

func TestFactoryResolverMessagingClientFactory(t *testing.T) {
	t.Run("default factory builds an AMQP client", func(t *testing.T) {
		resolver := NewFactoryResolver(nil)
		factory := resolver.MessagingClientFactory(7*time.Second, 5)
		assert.NotNil(t, factory)

		// Port 1 refuses immediately, so the reconnect goroutine fails fast; Close is
		// non-blocking and signals it to exit. No broker is required for this path.
		client := factory("amqp://127.0.0.1:1", logger.New("error", true))
		assert.NotNil(t, client)
		t.Cleanup(func() { _ = client.Close() })
	})

	t.Run("WithOptions variant carries ReadyTimeout and PublishTimeout, old method stays byte-identical", func(t *testing.T) {
		resolver := NewFactoryResolver(nil)

		// Old 2-arg method must still work unchanged.
		oldFactory := resolver.MessagingClientFactory(7*time.Second, 5)
		assert.NotNil(t, oldFactory)

		// New WithOptions method carries ReadyTimeout and the reconnect delays (#662)
		// through to the client. The app package can't read messaging's private fields,
		// so deep verification lives in messaging's tests; here we assert construction.
		newFactory := resolver.MessagingClientFactoryWithOptions(MessagingClientFactoryOptions{
			ConnectionTimeout:  7 * time.Second,
			MaxPublishAttempts: 5,
			ReadyTimeout:       9 * time.Second,
			PublishTimeout:     41 * time.Second,
			ReconnectDelay:     7 * time.Second,
			ReconnectMaxDelay:  90 * time.Second,
			ReinitDelay:        3 * time.Second,
			ResendDelay:        11 * time.Second,
		})
		assert.NotNil(t, newFactory)

		client := newFactory("amqp://127.0.0.1:1", logger.New("error", true))
		assert.NotNil(t, client)
		t.Cleanup(func() { _ = client.Close() })
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

func (m *mockCacheInstance) CompareAndDelete(_ context.Context, _ string, expectedValue []byte) (bool, error) {
	if expectedValue == nil {
		return false, cache.ErrNilExpectedValue
	}
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
		require.Error(t, err)

		// Should return typed ConfigError with "invalid" category
		var configErr *config.ConfigError
		require.ErrorAs(t, err, &configErr)
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
		require.Error(t, err)

		// Should return typed ConfigError with "not_configured" category
		assert.True(t, config.IsNotConfigured(err), "error should be 'not configured' type")

		var configErr *config.ConfigError
		require.ErrorAs(t, err, &configErr)
		assert.Equal(t, "not_configured", configErr.Category)
		// testCacheKey is a resource key, so the error is addressed to that tenant (C61.23).
		assert.Equal(t, "multitenant.tenants.test-key.cache", configErr.Field)
	})

	t.Run("invalid cache type (not redis)", func(t *testing.T) {
		// Mock TenantStore that returns Type="memcached"
		mockStore := &mockTenantStoreInvalidType{}

		resolver := NewFactoryResolver(nil)
		connector := resolver.CacheConnector(mockStore, logger.New("debug", true))

		c, err := connector(context.Background(), testCacheKey)

		assert.Nil(t, c)
		require.Error(t, err)

		// Should return typed ConfigError with "invalid" category
		var configErr *config.ConfigError
		require.ErrorAs(t, err, &configErr)
		assert.Equal(t, "invalid", configErr.Category)
		assert.Equal(t, "multitenant.tenants.test-key.cache.type", configErr.Field)
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
		require.Error(t, err)

		// Should return typed ConfigError with "missing" category
		var configErr *config.ConfigError
		require.ErrorAs(t, err, &configErr)
		assert.Equal(t, "missing", configErr.Category)
		assert.Equal(t, "multitenant.tenants.test-key.cache.redis.host", configErr.Field)
		assert.Contains(t, err.Error(), "MULTITENANT_TENANTS_TEST-KEY_CACHE_REDIS_HOST")
	})

	t.Run("redis client validation failure - invalid port", func(t *testing.T) {
		// Mock TenantStore that returns valid host but INVALID port
		// This passes app-level validation (line 139: Host != "")
		// but fails Redis client validation (port > 65535)
		mockStore := &mockTenantStoreInvalidPort{}

		resolver := NewFactoryResolver(nil)
		connector := resolver.CacheConnector(mockStore, logger.New("debug", true))

		c, err := connector(context.Background(), testCacheKey)

		assert.Nil(t, c)
		require.Error(t, err)

		// Should return cache.ConfigError from redis.Config.Validate()
		// This tests the error logging path at factory_resolver.go:174-182
		assert.Contains(t, err.Error(), "invalid port")
	})
}

// TestCacheConnectorAddressesConfigErrorsToTheKey pins that the runtime cache door spells its
// config errors the way the startup door already does: a non-empty resource key is a tenant id,
// so Field names that tenant's cache subtree and the hint names the tenant's env var — or drops
// the env half when the key does not round-trip. The empty key is the root and stays byte-identical.
func TestCacheConnectorAddressesConfigErrorsToTheKey(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		store       TenantStore
		wantField   string
		wantCat     string
		wantAction  string
		absentInErr string
	}{
		{
			name:      "tenant_key_empty_host",
			key:       "acme",
			store:     &mockTenantStoreEmptyHost{},
			wantField: "multitenant.tenants.acme.cache.redis.host",
			wantCat:   "missing",
			wantAction: "set MULTITENANT_TENANTS_ACME_CACHE_REDIS_HOST env var or add " +
				"multitenant.tenants.acme.cache.redis.host to config.yaml",
		},
		{
			name:        "underscored_tenant_key_drops_the_env_hint",
			key:         "acme_corp",
			store:       &mockTenantStoreEmptyHost{},
			wantField:   "multitenant.tenants.acme_corp.cache.redis.host",
			wantCat:     "missing",
			wantAction:  "add multitenant.tenants.acme_corp.cache.redis.host to config.yaml",
			absentInErr: "MULTITENANT_",
		},
		{
			name:      "tenant_key_cache_disabled",
			key:       "acme",
			store:     &mockTenantStoreCacheDisabled{},
			wantField: "multitenant.tenants.acme.cache",
			wantCat:   "not_configured",
			wantAction: "to enable: set MULTITENANT_TENANTS_ACME_CACHE_ENABLED env var or add " +
				"multitenant.tenants.acme.cache.enabled to config.yaml",
		},
		{
			name:       "tenant_key_invalid_type_keeps_its_handwritten_action",
			key:        "acme",
			store:      &mockTenantStoreInvalidType{},
			wantField:  "multitenant.tenants.acme.cache.type",
			wantCat:    "invalid",
			wantAction: "must be one of: redis",
		},
		{
			name:      "tenant_key_nil_config",
			key:       "acme",
			store:     &mockTenantStoreNilCacheCfg{},
			wantField: "multitenant.tenants.acme.cache",
			wantCat:   "invalid",
		},
		{
			name:       "root_key_empty_host_is_byte_identical",
			key:        "",
			store:      &mockTenantStoreEmptyHost{},
			wantField:  "cache.redis.host",
			wantCat:    "missing",
			wantAction: "set CACHE_REDIS_HOST env var or add cache.redis.host to config.yaml",
		},
		{
			name:       "root_key_cache_disabled_is_byte_identical",
			key:        "",
			store:      &mockTenantStoreCacheDisabled{},
			wantField:  "cache",
			wantCat:    "not_configured",
			wantAction: "to enable: set CACHE_ENABLED env var or add cache.enabled to config.yaml",
		},
		{
			name:       "root_key_invalid_type_is_byte_identical",
			key:        "",
			store:      &mockTenantStoreInvalidType{},
			wantField:  "cache.type",
			wantCat:    "invalid",
			wantAction: "must be one of: redis",
		},
		{
			name:      "root_key_nil_config_is_byte_identical",
			key:       "",
			store:     &mockTenantStoreNilCacheCfg{},
			wantField: "cache",
			wantCat:   "invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := NewFactoryResolver(nil)
			connector := resolver.CacheConnector(tt.store, logger.New("debug", true))

			c, err := connector(context.Background(), tt.key)

			assert.Nil(t, c)
			var configErr *config.ConfigError
			require.ErrorAs(t, err, &configErr)
			assert.Equal(t, tt.wantField, configErr.Field)
			assert.Equal(t, tt.wantCat, configErr.Category)
			assert.Equal(t, tt.wantAction, configErr.Action)
			if tt.absentInErr != "" {
				assert.NotContains(t, err.Error(), tt.absentInErr)
			}
		})
	}
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

type mockTenantStoreInvalidPort struct{}

func (m *mockTenantStoreInvalidPort) CacheConfig(_ context.Context, _ string) (*config.CacheConfig, error) {
	return &config.CacheConfig{
		Enabled: true,
		Type:    "redis",
		Redis: config.RedisConfig{
			Host:     "localhost", // Valid - passes app-level validation
			Port:     99999,       // INVALID - fails Redis validation (> 65535)
			Database: 0,
			PoolSize: 10,
		},
	}, nil
}

func (m *mockTenantStoreInvalidPort) DBConfig(_ context.Context, _ string) (*config.DatabaseConfig, error) {
	return nil, nil
}

func (m *mockTenantStoreInvalidPort) BrokerURL(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *mockTenantStoreInvalidPort) IsDynamic() bool {
	return false
}

// TestFactoryResolverDottedTenantIDSuppressesEnvHint drives the reachable producer of the
// flattening trap end to end: TenantStore.AddTenant takes a FREE-FORM tenant id — the resolver
// grammar constrains the static config, not the dynamic store — so "acme.corp" reaches the
// runtime cache door, whose empty-host branch raises NewMissingFieldError. Flattened, its
// variable would name tenant "acme", sub-key "corp"; the engine therefore emits the YAML-only
// hint, whose path carries the dotted id verbatim.
func TestFactoryResolverDottedTenantIDSuppressesEnvHint(t *testing.T) {
	const dottedTenant = "acme.corp"

	store := config.NewTenantStore(&config.Config{})
	store.AddTenant(dottedTenant, &config.TenantEntry{
		Cache: config.CacheConfig{Enabled: true, Type: config.CacheTypeRedis},
	})

	resolver := NewFactoryResolver(nil)
	connector := resolver.CacheConnector(store, logger.New("debug", true))

	c, err := connector(context.Background(), dottedTenant)

	assert.Nil(t, c)
	var configErr *config.ConfigError
	require.ErrorAs(t, err, &configErr)
	assert.Equal(t, "missing", configErr.Category)
	assert.Equal(t, "multitenant.tenants.acme.corp.cache.redis.host", configErr.Field)
	assert.Equal(t, "add multitenant.tenants.acme.corp.cache.redis.host to config.yaml", configErr.Action)
	assert.NotContains(t, configErr.Action, "env var")
	assert.NotContains(t, err.Error(), "MULTITENANT_TENANTS_ACME_CORP")
}
