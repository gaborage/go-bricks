package app

import (
	"context"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/stretchr/testify/assert"
)

const (
	testCacheKey = "test-key"
)

func TestFactoryResolverCacheConnector(t *testing.T) {
	t.Run("returns default connector when options are nil", func(t *testing.T) {
		resolver := NewFactoryResolver(nil)

		connector := resolver.CacheConnector()

		assert.NotNil(t, connector)

		// Default connector should return "not configured" error
		cache, err := connector(context.Background(), testCacheKey)
		assert.Nil(t, cache)
		assert.Error(t, err)
		assert.True(t, config.IsNotConfigured(err), "error should be 'not configured' type")
	})

	t.Run("returns default connector when cache connector option is nil", func(t *testing.T) {
		opts := &Options{
			CacheConnector: nil,
		}
		resolver := NewFactoryResolver(opts)

		connector := resolver.CacheConnector()

		assert.NotNil(t, connector)

		// Default connector should return "not configured" error
		cache, err := connector(context.Background(), testCacheKey)
		assert.Nil(t, cache)
		assert.Error(t, err)
		assert.True(t, config.IsNotConfigured(err), "error should be 'not configured' type")
	})

	t.Run("returns custom connector from options", func(t *testing.T) {
		customConnectorCalled := false
		expectedCache := &mockCacheInstance{}

		opts := &Options{
			CacheConnector: func(ctx context.Context, key string) (cache.Cache, error) {
				customConnectorCalled = true
				assert.Equal(t, testCacheKey, key)
				return expectedCache, nil
			},
		}

		resolver := NewFactoryResolver(opts)
		connector := resolver.CacheConnector()

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
			CacheConnector: func(ctx context.Context, key string) (cache.Cache, error) {
				return nil, expectedError
			},
		}

		resolver := NewFactoryResolver(opts)
		connector := resolver.CacheConnector()

		cache, err := connector(context.Background(), testCacheKey)
		assert.Nil(t, cache)
		assert.Equal(t, expectedError, err)
	})

	t.Run("default connector returns config error with proper fields", func(t *testing.T) {
		resolver := NewFactoryResolver(nil)
		connector := resolver.CacheConnector()

		_, err := connector(context.Background(), testCacheKey)

		assert.Error(t, err)

		// Check that it's a ConfigError with the right category
		var configErr *config.ConfigError
		assert.ErrorAs(t, err, &configErr)
		assert.Equal(t, "not_configured", configErr.Category)
		assert.Equal(t, "cache", configErr.Field)
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
			CacheConnector: func(ctx context.Context, key string) (cache.Cache, error) {
				return nil, nil
			},
		}

		resolver := NewFactoryResolver(opts)
		assert.True(t, resolver.HasCustomFactories())
	})
}

// mockCacheInstance is a minimal mock implementation of cache.Cache for testing
type mockCacheInstance struct{}

func (m *mockCacheInstance) Get(ctx context.Context, key string) ([]byte, error) {
	return nil, nil
}

func (m *mockCacheInstance) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return nil
}

func (m *mockCacheInstance) GetOrSet(ctx context.Context, key string, value []byte, ttl time.Duration) ([]byte, bool, error) {
	return value, true, nil
}

func (m *mockCacheInstance) CompareAndSet(ctx context.Context, key string, expected, value []byte, ttl time.Duration) (bool, error) {
	return true, nil
}

func (m *mockCacheInstance) Delete(ctx context.Context, key string) error {
	return nil
}

func (m *mockCacheInstance) Health(ctx context.Context) error {
	return nil
}

func (m *mockCacheInstance) Stats() (map[string]any, error) {
	return map[string]any{}, nil
}

func (m *mockCacheInstance) Close() error {
	return nil
}
