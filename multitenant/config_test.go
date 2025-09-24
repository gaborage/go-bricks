package multitenant

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
)

// mockTenantProvider implements TenantConfigProvider for testing
type mockTenantProvider struct {
	dbConfigs  map[string]*config.DatabaseConfig
	msgConfigs map[string]*TenantMessagingConfig
	dbError    error
	msgError   error
}

func (m *mockTenantProvider) GetDatabase(_ context.Context, tenantID string) (*config.DatabaseConfig, error) {
	if m.dbError != nil {
		return nil, m.dbError
	}
	if dbConfig, exists := m.dbConfigs[tenantID]; exists {
		return dbConfig, nil
	}
	return nil, ErrTenantNotFound
}

func (m *mockTenantProvider) GetMessaging(_ context.Context, tenantID string) (*TenantMessagingConfig, error) {
	if m.msgError != nil {
		return nil, m.msgError
	}
	if msgConfig, exists := m.msgConfigs[tenantID]; exists {
		return msgConfig, nil
	}
	return nil, ErrTenantNotFound
}

func TestNewTenantConfigCache(t *testing.T) {
	provider := &mockTenantProvider{}

	t.Run("default_options", func(t *testing.T) {
		cache := NewTenantConfigCache(provider)
		assert.NotNil(t, cache)
		assert.Equal(t, provider, cache.provider)
		assert.Equal(t, 5*time.Minute, cache.cfg.ttl)
		assert.Equal(t, 100, cache.cfg.maxSize)
		assert.Equal(t, 30*time.Minute, cache.cfg.staleGracePeriod)
		assert.NotNil(t, cache.entries)
	})

	t.Run("with_options", func(t *testing.T) {
		cache := NewTenantConfigCache(provider,
			WithTTL(10*time.Minute),
			WithMaxSize(50),
			WithStaleGracePeriod(2*time.Minute),
		)
		assert.NotNil(t, cache)
		assert.Equal(t, 10*time.Minute, cache.cfg.ttl)
		assert.Equal(t, 50, cache.cfg.maxSize)
		assert.Equal(t, 2*time.Minute, cache.cfg.staleGracePeriod)
	})
}

func TestTenantConfigCacheGetDatabase(t *testing.T) {
	dbConfig := &config.DatabaseConfig{
		Type:     "postgres",
		Host:     "localhost",
		Database: "test_db",
	}

	provider := &mockTenantProvider{
		dbConfigs: map[string]*config.DatabaseConfig{
			"tenant1": dbConfig,
		},
	}

	cache := NewTenantConfigCache(provider, WithTTL(100*time.Millisecond))

	t.Run("successful_fetch", func(t *testing.T) {
		result, err := cache.GetDatabase(context.Background(), "tenant1")
		assert.NoError(t, err)
		assert.Equal(t, dbConfig, result)
	})

	t.Run("cached_result", func(t *testing.T) {
		// First call
		result1, err := cache.GetDatabase(context.Background(), "tenant1")
		assert.NoError(t, err)
		assert.Equal(t, dbConfig, result1)

		// Second call should use cache
		result2, err := cache.GetDatabase(context.Background(), "tenant1")
		assert.NoError(t, err)
		assert.Equal(t, dbConfig, result2)
	})

	t.Run("tenant_not_found", func(t *testing.T) {
		result, err := cache.GetDatabase(context.Background(), "unknown")
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, ErrTenantNotFound, err)
	})

	t.Run("provider_error", func(t *testing.T) {
		errorProvider := &mockTenantProvider{
			dbError: errors.New("provider error"),
		}
		errorCache := NewTenantConfigCache(errorProvider)

		result, err := errorCache.GetDatabase(context.Background(), "tenant1")
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "provider error")
	})

	t.Run("cache_expiration", func(t *testing.T) {
		// Get initial result
		result1, err := cache.GetDatabase(context.Background(), "tenant1")
		assert.NoError(t, err)
		assert.NotNil(t, result1)

		// Wait for cache to expire
		time.Sleep(150 * time.Millisecond)

		// Should still get result but will refresh in background
		result2, err := cache.GetDatabase(context.Background(), "tenant1")
		assert.NoError(t, err)
		assert.NotNil(t, result2)
	})
}

func TestTenantConfigCacheGetMessaging(t *testing.T) {
	msgConfig := &TenantMessagingConfig{
		URL: "amqp://localhost:5672/tenant1",
	}

	provider := &mockTenantProvider{
		msgConfigs: map[string]*TenantMessagingConfig{
			"tenant1": msgConfig,
		},
	}

	cache := NewTenantConfigCache(provider)

	t.Run("successful_fetch", func(t *testing.T) {
		result, err := cache.GetMessaging(context.Background(), "tenant1")
		assert.NoError(t, err)
		assert.Equal(t, msgConfig, result)
	})

	t.Run("cached_result", func(t *testing.T) {
		// First call
		result1, err := cache.GetMessaging(context.Background(), "tenant1")
		assert.NoError(t, err)
		assert.Equal(t, msgConfig, result1)

		// Second call should use cache
		result2, err := cache.GetMessaging(context.Background(), "tenant1")
		assert.NoError(t, err)
		assert.Equal(t, msgConfig, result2)
	})

	t.Run("tenant_not_found", func(t *testing.T) {
		result, err := cache.GetMessaging(context.Background(), "unknown")
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, ErrTenantNotFound, err)
	})

	t.Run("provider_error", func(t *testing.T) {
		errorProvider := &mockTenantProvider{
			msgError: errors.New("messaging error"),
		}
		errorCache := NewTenantConfigCache(errorProvider)

		result, err := errorCache.GetMessaging(context.Background(), "tenant1")
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "messaging error")
	})
}

func TestTenantConfigCacheEviction(t *testing.T) {
	provider := &mockTenantProvider{
		dbConfigs: map[string]*config.DatabaseConfig{
			"tenant1": {Type: "postgresql", Host: "host1", Database: "db1"},
			"tenant2": {Type: "postgresql", Host: "host2", Database: "db2"},
			"tenant3": {Type: "postgresql", Host: "host3", Database: "db3"},
		},
	}

	// Create cache with max size of 2
	cache := NewTenantConfigCache(provider, WithMaxSize(2))

	// Fill cache
	_, err := cache.GetDatabase(context.Background(), "tenant1")
	assert.NoError(t, err)

	_, err = cache.GetDatabase(context.Background(), "tenant2")
	assert.NoError(t, err)

	// This should evict tenant1 (oldest)
	_, err = cache.GetDatabase(context.Background(), "tenant3")
	assert.NoError(t, err)

	// Verify cache size is still 2
	cache.mu.RLock()
	cacheSize := len(cache.entries)
	cache.mu.RUnlock()
	assert.Equal(t, 2, cacheSize)
}

func TestTenantConfigCacheOptions(t *testing.T) {
	provider := &mockTenantProvider{}

	t.Run("with_ttl", func(t *testing.T) {
		cache := NewTenantConfigCache(provider, WithTTL(5*time.Minute))
		assert.Equal(t, 5*time.Minute, cache.cfg.ttl)
	})

	t.Run("with_max_size", func(t *testing.T) {
		cache := NewTenantConfigCache(provider, WithMaxSize(20))
		assert.Equal(t, 20, cache.cfg.maxSize)
	})

	t.Run("with_stale_grace_period", func(t *testing.T) {
		cache := NewTenantConfigCache(provider, WithStaleGracePeriod(30*time.Second))
		assert.Equal(t, 30*time.Second, cache.cfg.staleGracePeriod)
	})

	t.Run("zero_values_ignored", func(t *testing.T) {
		cache := NewTenantConfigCache(provider,
			WithTTL(0),
			WithMaxSize(0),
			WithStaleGracePeriod(0),
		)
		// Should use default values when zero is passed
		assert.Equal(t, 5*time.Minute, cache.cfg.ttl)
		assert.Equal(t, 100, cache.cfg.maxSize)
		assert.Equal(t, 30*time.Minute, cache.cfg.staleGracePeriod)
	})
}

func TestCacheEntryState(t *testing.T) {
	now := time.Now()

	t.Run("fresh_entry", func(t *testing.T) {
		entry := &cacheEntry{
			fetchedAt: now,
		}
		ttl := 5 * time.Minute
		gracePeriod := 1 * time.Minute

		assert.False(t, entry.isExpired(ttl))
		assert.False(t, entry.isStale(gracePeriod))
	})

	t.Run("stale_but_not_expired", func(t *testing.T) {
		entry := &cacheEntry{
			fetchedAt: now.Add(-3 * time.Minute), // older than grace period but not TTL
		}
		ttl := 5 * time.Minute
		gracePeriod := 2 * time.Minute

		assert.False(t, entry.isExpired(ttl))
		assert.True(t, entry.isStale(gracePeriod))
	})

	t.Run("expired_entry", func(t *testing.T) {
		entry := &cacheEntry{
			fetchedAt: now.Add(-8 * time.Minute), // older than TTL + grace period
		}
		ttl := 5 * time.Minute
		gracePeriod := 1 * time.Minute

		assert.True(t, entry.isExpired(ttl))
		assert.True(t, entry.isStale(gracePeriod))
	})
}
