package app

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/cache/redis"
	cachetest "github.com/gaborage/go-bricks/cache/testing"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/clienttls"
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

	t.Run("returns custom connector from options behind the namespace view", func(t *testing.T) {
		customConnectorCalled := false
		customCache := &mockCacheInstance{}

		opts := &Options{
			CacheConnector: func(_ context.Context, key string) (cache.Cache, error) {
				customConnectorCalled = true
				assert.Equal(t, testCacheKey, key)
				return customCache, nil
			},
		}

		resolver := NewFactoryResolver(opts)
		connector := resolver.CacheConnector(&stubTenantResource{}, logger.New("debug", true))

		assert.NotNil(t, connector)

		// Custom connector should be called
		result, err := connector(context.Background(), testCacheKey)
		require.NoError(t, err)
		assert.True(t, customConnectorCalled, "custom connector should have been called")

		// A custom connector's cache is namespaced like any other (ADR-117). This
		// resolver carries no app name, so the resource key alone is the namespace and
		// what comes back is the view over the custom cache, which still delegates to it.
		assert.NotSame(t, customCache, result)
		_, err = result.CompareAndDelete(context.Background(), "k", nil)
		require.ErrorIs(t, err, cache.ErrNilExpectedValue)
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

// mockTenantStoreBadTLSMaterial passes every app-level check and the Redis
// structural check, and fails only where the TLS material is loaded: cavalue
// is not base64.
type mockTenantStoreBadTLSMaterial struct{}

func (m *mockTenantStoreBadTLSMaterial) CacheConfig(_ context.Context, _ string) (*config.CacheConfig, error) {
	return &config.CacheConfig{
		Enabled: true,
		Type:    "redis",
		Redis: config.RedisConfig{
			Host:     "localhost",
			Port:     6379,
			Database: 0,
			PoolSize: 10,
			TLS:      config.RedisTLSConfig{Enabled: true, CAValue: "not-base64"},
		},
	}, nil
}

func (m *mockTenantStoreBadTLSMaterial) DBConfig(_ context.Context, _ string) (*config.DatabaseConfig, error) {
	return nil, nil
}

func (m *mockTenantStoreBadTLSMaterial) BrokerURL(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *mockTenantStoreBadTLSMaterial) IsDynamic() bool {
	return false
}

// TestCacheConnectorAddressesTLSMaterialErrorToTheKey pins that a config-class
// error raised inside redis.NewClient — the TLS material load, which is root-
// spelled "redis.tls" by the cache package — reaches the caller addressed to
// the tenant whose config produced it, not as an anonymous root-spelled error.
func TestCacheConnectorAddressesTLSMaterialErrorToTheKey(t *testing.T) {
	resolver := NewFactoryResolver(nil)
	connector := resolver.CacheConnector(&mockTenantStoreBadTLSMaterial{}, logger.New("debug", true))

	c, err := connector(context.Background(), testCacheKey)

	assert.Nil(t, c)
	var configErr *config.ConfigError
	require.ErrorAs(t, err, &configErr)
	assert.True(t,
		strings.HasPrefix(configErr.Field, "multitenant.tenants."+testCacheKey+".cache.redis.tls"),
		"field %q must name the tenant's TLS subtree", configErr.Field)
	assert.Contains(t, err.Error(), "cache: redis: tls:")
}

// TestCacheConnectorLeavesDialErrorsUnqualified pins the other half of the same
// contract: a dial failure is not a config-shape error, so it keeps the cache
// package's own spelling instead of being addressed to a config key.
func TestCacheConnectorLeavesDialErrorsUnqualified(t *testing.T) {
	resolver := NewFactoryResolver(nil)
	connector := resolver.CacheConnector(&mockTenantStoreUnreachable{}, logger.New("debug", true))

	c, err := connector(context.Background(), testCacheKey)

	assert.Nil(t, c)
	require.Error(t, err)
	var connErr *cache.ConnectionError
	require.ErrorAs(t, err, &connErr)
	var configErr *config.ConfigError
	assert.NotErrorAs(t, err, &configErr, "a dial failure must not be addressed to a config key")
}

// mockTenantStoreUnreachable points at a port nothing listens on, so the client
// fails at PING rather than at any config check.
type mockTenantStoreUnreachable struct{}

func (m *mockTenantStoreUnreachable) CacheConfig(_ context.Context, _ string) (*config.CacheConfig, error) {
	return &config.CacheConfig{
		Enabled: true,
		Type:    "redis",
		Redis: config.RedisConfig{
			Host:        "127.0.0.1",
			Port:        1,
			Database:    0,
			PoolSize:    10,
			DialTimeout: 100 * time.Millisecond,
			MaxRetries:  -1,
		},
	}, nil
}

func (m *mockTenantStoreUnreachable) DBConfig(_ context.Context, _ string) (*config.DatabaseConfig, error) {
	return nil, nil
}

func (m *mockTenantStoreUnreachable) BrokerURL(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *mockTenantStoreUnreachable) IsDynamic() bool {
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

// TestRedisTLSConfigFieldParity proves the three TLS structs stay in lockstep by
// shape rather than by a list of literals a later field can quietly escape:
// every clienttls.Material field exists, by name AND type, in both config-layer
// blocks, and the two config blocks carry identical field sets. That is what
// makes the struct conversion in redisClientConfig safe, and it fails when
// someone adds a field to one side only.
func TestRedisTLSConfigFieldParity(t *testing.T) {
	fieldsOf := func(typ reflect.Type) map[string]reflect.Type {
		out := make(map[string]reflect.Type, typ.NumField())
		for i := range typ.NumField() {
			f := typ.Field(i)
			out[f.Name] = f.Type
		}
		return out
	}

	material := fieldsOf(reflect.TypeOf(clienttls.Material{}))
	appSide := fieldsOf(reflect.TypeOf(config.RedisTLSConfig{}))
	clientSide := fieldsOf(reflect.TypeOf(redis.TLSConfig{}))

	require.NotEmpty(t, material)
	for name, typ := range material {
		assert.Equal(t, typ, appSide[name], "config.RedisTLSConfig is missing loader field %s", name)
		assert.Equal(t, typ, clientSide[name], "redis.TLSConfig is missing loader field %s", name)
	}
	assert.Equal(t, appSide, clientSide, "the two config blocks must carry identical field sets")
}

// TestRedisClientConfigCarriesTLSBlock keeps one round-trip assertion beside the
// parity check above: the struct conversion actually moves a non-zero block, so
// a parity-clean pair cannot pass while the copy itself is dropped.
func TestRedisClientConfigCarriesTLSBlock(t *testing.T) {
	tlsBlock := config.RedisTLSConfig{
		Enabled:    true,
		CAFile:     "/etc/ca-file.pem",
		CertFile:   "/etc/cert-file.pem",
		KeyFile:    "/etc/key-file.pem",
		ServerName: "sni.example",
		MinVersion: "1.3",
	}
	cacheCfg := &config.CacheConfig{
		Redis: config.RedisConfig{Host: "cache.example", Port: 6380, TLS: tlsBlock},
	}

	got := redisClientConfig(cacheCfg)

	assert.Equal(t, redis.TLSConfig(tlsBlock), got.TLS)
	assert.NotZero(t, got.TLS)
}

// redisConfigParityExclusions names the config.RedisConfig fields redisClientConfig
// does not carry across by a same-named field copy, each with the reason it is
// exempt. A field absent from this map must survive the hand copy by name and by
// value, so adding one to config.RedisConfig without adding it to redisClientConfig
// is a test failure rather than a setting that silently never reaches the dial.
var redisConfigParityExclusions = map[string]string{
	"TLS": "moved as a whole block by struct conversion, asserted below",
	"KeyPrefix": "deliberately never reaches the transport: the namespace is applied by the " +
		"cache.WithKeyPrefix decorator above the client, so the Redis config has no field for it (ADR-117)",
}

// fillDistinctFields sets every settable field behind v to a distinct non-zero
// value. Driven by reflection rather than a literal so a field added tomorrow is
// covered without editing this helper — and so a dropped field shows up as a
// zero on the far side instead of matching an all-zero source by accident.
func fillDistinctFields(t *testing.T, v reflect.Value, seed *int) {
	t.Helper()

	for i := range v.NumField() {
		field := v.Type().Field(i)
		target := v.Field(i)
		if !target.CanSet() {
			continue
		}
		*seed++
		switch target.Kind() {
		case reflect.String:
			target.SetString(strings.ToLower(field.Name) + "-value")
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			target.SetInt(int64(*seed))
		case reflect.Bool:
			target.SetBool(true)
		case reflect.Struct:
			fillDistinctFields(t, target, seed)
		case reflect.Pointer:
			// Tri-state settings (*string) are filled non-nil rather than skipped: a
			// skipped field would stay nil on both sides and compare equal to a copy
			// that dropped it, which is the very failure this helper exists to catch.
			if target.Type().Elem().Kind() != reflect.String {
				t.Fatalf("fillDistinctFields cannot fill *%s (field %s)", target.Type().Elem().Kind(), field.Name)
			}
			filled := strings.ToLower(field.Name) + "-value"
			target.Set(reflect.ValueOf(&filled))
		default:
			t.Fatalf("fillDistinctFields cannot fill %s (field %s)", target.Kind(), field.Name)
		}
	}
}

// TestRedisConfigFieldParity proves the hand copy in redisClientConfig is total:
// every exported config.RedisConfig field reaches cache/redis.Config under the
// same name with the same value, unless it is named in
// redisConfigParityExclusions with a reason. The source block is filled by
// reflection, so a field added to config.RedisConfig and forgotten in the copy
// fails here instead of reaching production as a setting that never dials.
func TestRedisConfigFieldParity(t *testing.T) {
	cacheCfg := &config.CacheConfig{}
	seed := 0
	fillDistinctFields(t, reflect.ValueOf(&cacheCfg.Redis).Elem(), &seed)

	got := redisClientConfig(cacheCfg)
	gotValue := reflect.ValueOf(*got)
	srcValue := reflect.ValueOf(cacheCfg.Redis)
	srcType := srcValue.Type()

	require.NotZero(t, srcType.NumField())
	for i := range srcType.NumField() {
		field := srcType.Field(i)
		if !field.IsExported() {
			continue
		}
		if _, excluded := redisConfigParityExclusions[field.Name]; excluded {
			continue
		}

		dst := gotValue.FieldByName(field.Name)
		require.True(t, dst.IsValid(), "redis.Config has no %s field to carry config.RedisConfig.%s", field.Name, field.Name)
		assert.Equal(t, srcValue.Field(i).Interface(), dst.Interface(), "redisClientConfig drops config.RedisConfig.%s", field.Name)
	}

	assert.Equal(t, redis.TLSConfig(cacheCfg.Redis.TLS), got.TLS, "the excluded TLS block must still survive the struct conversion")
}

const (
	keyPrefixAppName = "orders"
	keyPrefixTenant  = "acme"
	keyPrefixLogical = "user:1"
)

// cacheSectionWithKeyPrefix builds an enabled Redis cache section carrying keyPrefix
// in its tri-state form: nil is an absent key, a pointer is an explicit value.
func cacheSectionWithKeyPrefix(keyPrefix *string) *config.CacheConfig {
	return &config.CacheConfig{
		Enabled: true,
		Type:    config.CacheTypeRedis,
		Redis:   config.RedisConfig{Host: "localhost", Port: 6379, PoolSize: 10, KeyPrefix: keyPrefix},
	}
}

// storeServingCacheSection returns the shipped store carrying section under key: the
// root cache for the empty key, a tenant mirror otherwise. The real store is used
// rather than a fake so the prefix lookup meets the same key semantics and the same
// not-configured errors the framework meets in production.
func storeServingCacheSection(key string, section *config.CacheConfig) TenantStore {
	cfg := &config.Config{}
	if key == "" {
		cfg.Cache = *section
	} else {
		cfg.Multitenant.Enabled = true
		cfg.Multitenant.Tenants = map[string]config.TenantEntry{key: {Cache: *section}}
	}
	return config.NewTenantStore(cfg)
}

// assertWireKey writes one logical key through c and asserts the single key that
// reached the inner cache, which is the whole observable effect of the namespace.
func assertWireKey(t *testing.T, c cache.Cache, inner *cachetest.MockCache, want string) {
	t.Helper()
	require.NoError(t, c.Set(context.Background(), keyPrefixLogical, []byte("v"), time.Minute))
	assert.Equal(t, []string{want}, inner.AllKeys())
}

// connectorOverMock returns a resolver whose cache connector hands out mock, plus the
// store that answers the prefix lookup.
func connectorOverMock(t *testing.T, mock cache.Cache, store TenantStore) cache.Connector {
	t.Helper()
	resolver := newFactoryResolverForConfig(&Options{
		CacheConnector: func(context.Context, string) (cache.Cache, error) { return mock, nil },
	}, &config.Config{App: config.AppConfig{Name: keyPrefixAppName}})
	return resolver.CacheConnector(store, logger.New("error", true))
}

// TestFactoryResolverCacheConnectorNamespacesKeys pins the whole prefix resolution at
// its one wiring site: the app name is the default namespace, a tenant id folds in
// after it, an explicit prefix displaces the app name, and an explicit empty prefix
// opts out — at the root entirely, and for a tenant down to the tenant id alone,
// because cross-tenant isolation on a shared endpoint is not optional.
func TestFactoryResolverCacheConnectorNamespacesKeys(t *testing.T) {
	optOut := ""
	override := "legacy"

	tests := []struct {
		name        string
		key         string
		keyPrefix   *string
		wantWireKey string
	}{
		{name: "root_takes_the_app_name", key: "", wantWireKey: "orders:user:1"},
		{name: "tenant_folds_the_tenant_id", key: keyPrefixTenant, wantWireKey: "orders:acme:user:1"},
		{name: "root_opt_out_is_unprefixed", key: "", keyPrefix: &optOut, wantWireKey: "user:1"},
		{name: "tenant_opt_out_keeps_the_tenant_id", key: keyPrefixTenant, keyPrefix: &optOut, wantWireKey: "acme:user:1"},
		{name: "root_override_displaces_the_app_name", key: "", keyPrefix: &override, wantWireKey: "legacy:user:1"},
		{name: "tenant_override_still_folds_the_tenant_id", key: keyPrefixTenant, keyPrefix: &override, wantWireKey: "legacy:acme:user:1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := cachetest.NewMockCache()
			store := storeServingCacheSection(tt.key, cacheSectionWithKeyPrefix(tt.keyPrefix))

			connector := connectorOverMock(t, mock, store)
			c, err := connector(context.Background(), tt.key)
			require.NoError(t, err)

			assertWireKey(t, c, mock, tt.wantWireKey)
		})
	}
}

// TestFactoryResolverCacheConnectorFailsClosedOnAnUnusablePrefix proves the wiring
// never hands back an instance it could not namespace. A prefix that reached the
// connector without passing config.Validate — a dynamic tenant source is not obliged
// to run it — closes the instance the inner connector just dialed rather than leaking
// an unprefixed cache into the pool.
func TestFactoryResolverCacheConnectorFailsClosedOnAnUnusablePrefix(t *testing.T) {
	unusable := "bad name"
	mock := cachetest.NewMockCache()
	store := storeServingCacheSection(keyPrefixTenant, cacheSectionWithKeyPrefix(&unusable))

	connector := connectorOverMock(t, mock, store)
	c, err := connector(context.Background(), keyPrefixTenant)

	require.ErrorIs(t, err, cache.ErrInvalidKeyPrefix)
	assert.Nil(t, c)
	cachetest.AssertCacheClosed(t, mock)
}

// TestFactoryResolverCacheConnectorFallsBackToTheAppNameWithoutASection proves an
// unreadable cache section still namespaces. A section that cannot be read carries no
// override to honor, and the app-name default is what a custom CacheConnector — whose
// deployment may declare no cache.* block at all — gets. The unsafe outcome would be
// an unprefixed instance, which is exactly what the fallback prevents.
func TestFactoryResolverCacheConnectorFallsBackToTheAppNameWithoutASection(t *testing.T) {
	t.Run("store_reports_not_configured", func(t *testing.T) {
		mock := cachetest.NewMockCache()

		connector := connectorOverMock(t, mock, config.NewTenantStore(&config.Config{}))
		c, err := connector(context.Background(), "")
		require.NoError(t, err)

		assertWireKey(t, c, mock, "orders:user:1")
	})

	t.Run("no_store_at_all", func(t *testing.T) {
		mock := cachetest.NewMockCache()

		connector := connectorOverMock(t, mock, nil)
		c, err := connector(context.Background(), keyPrefixTenant)
		require.NoError(t, err)

		assertWireKey(t, c, mock, "orders:acme:user:1")
	})
}

// TestFactoryResolverCacheConnectorReportsANilCacheInsteadOfPanicking pins the one
// path where the wrapper has nothing to close: a connector that broke its contract and
// returned a nil cache with a nil error. The framework reports it rather than closing
// the nil it was handed.
func TestFactoryResolverCacheConnectorReportsANilCacheInsteadOfPanicking(t *testing.T) {
	resolver := newFactoryResolverForConfig(&Options{
		CacheConnector: func(context.Context, string) (cache.Cache, error) { return nil, nil },
	}, &config.Config{App: config.AppConfig{Name: keyPrefixAppName}})

	connector := resolver.CacheConnector(config.NewTenantStore(&config.Config{}), logger.New("error", true))
	c, err := connector(context.Background(), "")

	require.ErrorIs(t, err, cache.ErrNilCache)
	assert.Nil(t, c)
}

// timedMockCache is a cache that carries a configured load-through bound, the shape
// the framework's Redis client has.
type timedMockCache struct {
	*cachetest.MockCache
	loadTimeout time.Duration
}

func (c *timedMockCache) LoadTimeout() time.Duration { return c.loadTimeout }

// TestCacheManagerNamespacesOncePerInstance proves the decorator is installed by the
// connector and therefore exactly once per pooled instance: a second lease returns the
// same pointer, which is what keeps LoadThrough's per-instance singleflight scoping
// intact, and the pooled view still reports the deployment's cache.loadtimeout instead
// of falling back to the hand-written-cache bound.
func TestCacheManagerNamespacesOncePerInstance(t *testing.T) {
	inner := &timedMockCache{MockCache: cachetest.NewMockCache(), loadTimeout: 250 * time.Millisecond}
	store := storeServingCacheSection("", cacheSectionWithKeyPrefix(nil))
	resolver := newFactoryResolverForConfig(&Options{
		CacheConnector: func(context.Context, string) (cache.Cache, error) { return inner, nil },
	}, &config.Config{App: config.AppConfig{Name: keyPrefixAppName}})
	factory := NewResourceManagerFactory(resolver, NewManagerConfigBuilder(false, 50), logger.New("error", false))

	manager, err := factory.CreateCacheManager(store)
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })

	first, releaseFirst, err := manager.Get(context.Background(), "")
	require.NoError(t, err)
	defer releaseFirst()
	second, releaseSecond, err := manager.Get(context.Background(), "")
	require.NoError(t, err)
	defer releaseSecond()

	assert.Same(t, first, second, "the pool must hand out one namespaced instance per key")
	provider, ok := first.(cache.LoadTimeoutProvider)
	require.True(t, ok, "the pooled cache must still carry its load-through bound")
	assert.Equal(t, 250*time.Millisecond, provider.LoadTimeout())

	require.NoError(t, first.Set(context.Background(), keyPrefixLogical, []byte("v"), time.Minute))
	assert.Equal(t, []string{"orders:user:1"}, inner.AllKeys())
}

// failingCacheConfigStore reports an opaque failure rather than a *config.ConfigError,
// the shape a dynamic tenant source has when its own backend is unreachable — as
// distinct from the store reporting that no cache section is declared.
type failingCacheConfigStore struct {
	TenantStore
	err error
}

func (s *failingCacheConfigStore) CacheConfig(context.Context, string) (*config.CacheConfig, error) {
	return nil, s.err
}

// namespaceFallbackWarning is the substring that proves the fallback was reported.
const namespaceFallbackWarning = "falls back to the application name"

// connectorLoggingAt returns the cache connector over mock at a log level that lets
// warnings through, built inside the caller's stdout capture because the framework
// logger binds stdout at construction.
func connectorLoggingAt(store TenantStore, mock cache.Cache) cache.Connector {
	resolver := newFactoryResolverForConfig(&Options{
		CacheConnector: func(context.Context, string) (cache.Cache, error) { return mock, nil },
	}, &config.Config{App: config.AppConfig{Name: keyPrefixAppName}})
	return resolver.CacheConnector(store, logger.New("warn", false))
}

// TestFactoryResolverCacheConnectorReportsAnUnreadableSection pins the one case the
// app.name fallback must not be silent about. A store that FAILED is not a store
// reporting no section: a section carrying an explicit keyprefix resolves to the
// default namespace instead of its own and strands the entries already written under
// it, so the fallback is logged. A store merely reporting that nothing is declared —
// the custom-connector deployment with no cache.* block — stays silent, or every such
// deployment would warn on every pooled instance.
func TestFactoryResolverCacheConnectorReportsAnUnreadableSection(t *testing.T) {
	t.Run("an_opaque_read_failure_is_logged", func(t *testing.T) {
		mock := cachetest.NewMockCache()
		store := &failingCacheConfigStore{TenantStore: config.NewTenantStore(&config.Config{}), err: assert.AnError}

		out := captureStdout(t, func() {
			c, err := connectorLoggingAt(store, mock)(context.Background(), keyPrefixTenant)
			require.NoError(t, err)
			assertWireKey(t, c, mock, "orders:acme:user:1")
		})

		assert.Contains(t, out, namespaceFallbackWarning)
	})

	t.Run("no_section_declared_stays_silent", func(t *testing.T) {
		mock := cachetest.NewMockCache()

		out := captureStdout(t, func() {
			c, err := connectorLoggingAt(config.NewTenantStore(&config.Config{}), mock)(context.Background(), "")
			require.NoError(t, err)
			assertWireKey(t, c, mock, "orders:user:1")
		})

		assert.NotContains(t, out, namespaceFallbackWarning)
	})
}

// refusedCloseWarning is the substring that proves the close of a refused instance was
// reported as having failed.
const refusedCloseWarning = "Failed to close the cache instance rejected by the key-prefix check"

// TestFactoryResolverCacheConnectorReportsAFailedCloseOfARefusedInstance pins the one
// thing the fail-closed path can still get wrong after it has decided to refuse: the
// close it performs on the way out is itself fallible, and a connection it could not
// release is a leak the operator only learns about from the log — the caller is already
// getting the namespace error either way. The clean close is the half that must stay
// silent, or every refusal would report a failure that did not happen.
func TestFactoryResolverCacheConnectorReportsAFailedCloseOfARefusedInstance(t *testing.T) {
	unusable := "bad name"
	refuse := func(t *testing.T, mock *cachetest.MockCache) string {
		t.Helper()
		store := storeServingCacheSection(keyPrefixTenant, cacheSectionWithKeyPrefix(&unusable))
		return captureStdout(t, func() {
			c, err := connectorLoggingAt(store, mock)(context.Background(), keyPrefixTenant)
			require.ErrorIs(t, err, cache.ErrInvalidKeyPrefix)
			assert.Nil(t, c)
		})
	}

	t.Run("a_close_that_failed_is_reported", func(t *testing.T) {
		out := refuse(t, cachetest.NewMockCache().WithCloseFailure(assert.AnError))

		assert.Contains(t, out, refusedCloseWarning)
		assert.Contains(t, out, assert.AnError.Error(), "the warning must carry the close error itself")
	})

	t.Run("a_close_that_succeeded_stays_silent", func(t *testing.T) {
		mock := cachetest.NewMockCache()

		out := refuse(t, mock)

		cachetest.AssertCacheClosed(t, mock)
		assert.NotContains(t, out, refusedCloseWarning)
	})
}

// TestFactoryResolverCacheConnectorTenantDoesNotInheritTheRootPrefix pins that each
// section resolves its own namespace. A tenant that sets no keyprefix takes app.name,
// never the root's explicit value: TenantStore serves the tenant's own mirror and
// never folds the root section into it, so a root prefix is not a deployment-wide
// setting that tenants narrow.
func TestFactoryResolverCacheConnectorTenantDoesNotInheritTheRootPrefix(t *testing.T) {
	mock := cachetest.NewMockCache()
	cfg := &config.Config{Cache: *cacheSectionWithKeyPrefix(new("legacy"))}
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Tenants = map[string]config.TenantEntry{
		keyPrefixTenant: {Cache: *cacheSectionWithKeyPrefix(nil)},
	}

	connector := connectorOverMock(t, mock, config.NewTenantStore(cfg))
	c, err := connector(context.Background(), keyPrefixTenant)
	require.NoError(t, err)

	assertWireKey(t, c, mock, "orders:acme:user:1")
}

// TestFactoryResolverCacheConnectorRefusesAnUnresolvedNamespace pins the second
// fail-closed arm. The exported NewFactoryResolver carries no app name, so a consumer
// wiring its own resolver with a custom connector would otherwise join the root key to
// the empty prefix and get its cache back unwrapped — two such services on one endpoint
// writing each other's keys, which is the collision the namespace exists to prevent. An
// explicit keyprefix: "" is a different event: it is the documented opt-out, and it is
// still honored, unwrapped and without error.
func TestFactoryResolverCacheConnectorRefusesAnUnresolvedNamespace(t *testing.T) {
	connectorFor := func(resolver *FactoryResolver, store TenantStore) cache.Connector {
		return resolver.CacheConnector(store, logger.New("error", true))
	}
	customFor := func(mock cache.Cache) *Options {
		return &Options{CacheConnector: func(context.Context, string) (cache.Cache, error) { return mock, nil }}
	}

	t.Run("no_app_name_and_no_section_is_refused", func(t *testing.T) {
		mock := cachetest.NewMockCache()

		c, err := connectorFor(NewFactoryResolver(customFor(mock)), config.NewTenantStore(&config.Config{}))(
			context.Background(), "")

		require.ErrorIs(t, err, errUnresolvedCacheNamespace)
		assert.Nil(t, c)
		cachetest.AssertCacheClosed(t, mock)
	})

	t.Run("an_explicit_opt_out_is_still_honored", func(t *testing.T) {
		mock := cachetest.NewMockCache()
		store := storeServingCacheSection("", cacheSectionWithKeyPrefix(new("")))

		c, err := connectorFor(NewFactoryResolver(customFor(mock)), store)(context.Background(), "")

		require.NoError(t, err)
		assert.Same(t, mock, c, "the opt-out must hand back the cache itself, unwrapped")
		assertWireKey(t, c, mock, keyPrefixLogical)
	})

	t.Run("a_tenant_key_is_a_namespace_of_its_own", func(t *testing.T) {
		mock := cachetest.NewMockCache()

		c, err := connectorFor(NewFactoryResolver(customFor(mock)), config.NewTenantStore(&config.Config{}))(
			context.Background(), keyPrefixTenant)

		require.NoError(t, err)
		assertWireKey(t, c, mock, "acme:user:1")
	})
}
