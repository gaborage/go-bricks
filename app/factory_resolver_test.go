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
