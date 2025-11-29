package config

import (
	"context"
	"fmt"
	"sync"
)

const (
	configNotFoundErrMsg = "configuration not found"
	notConfiguredErrMsg  = "not configured"
	notEnabledErrMsg     = "not enabled"
)

// TenantStore provides per-key database, messaging, and cache configurations.
// This is the default config-backed implementation that uses the static tenant map.
type TenantStore struct {
	// Single-tenant configurations (used when key is "")
	defaultDB        *DatabaseConfig
	defaultMessaging *MessagingConfig
	defaultCache     *CacheConfig

	// Multi-tenant configurations (used when key is tenant ID)
	tenants map[string]TenantEntry
	mu      sync.RWMutex
}

// NewTenantStore creates a config-backed tenant store
func NewTenantStore(cfg *Config) *TenantStore {
	source := &TenantStore{
		defaultDB:        &cfg.Database,
		defaultMessaging: &cfg.Messaging,
		defaultCache:     &cfg.Cache,
		tenants:          make(map[string]TenantEntry),
		mu:               sync.RWMutex{},
	}

	// Copy tenant configurations if multi-tenant is enabled
	if cfg.Multitenant.Enabled && cfg.Multitenant.Tenants != nil {
		for tenantID := range cfg.Multitenant.Tenants {
			source.tenants[tenantID] = cfg.Multitenant.Tenants[tenantID]
		}
	}

	return source
}

// DBConfig returns the database configuration for the given key.
// For single-tenant (key=""), returns the default database config.
// For multi-tenant (key=tenantID), returns the tenant-specific database config.
func (s *TenantStore) DBConfig(_ context.Context, key string) (*DatabaseConfig, error) {
	// Single-tenant case
	if key == "" {
		if s.defaultDB == nil {
			return nil, NewNotConfiguredError("database", "DATABASE_HOST", "database.host")
		}
		return s.defaultDB, nil
	}

	// Multi-tenant case
	s.mu.RLock()
	tenant, exists := s.tenants[key]
	s.mu.RUnlock()
	if !exists {
		return nil, NewMultiTenantError(key, "database", configNotFoundErrMsg, fmt.Sprintf("check multitenant.tenants.%s.database section or verify dynamic tenant source", key))
	}

	return &tenant.Database, nil
}

// BrokerURL returns the AMQP broker URL for the given key.
// For single-tenant (key=""), returns the default broker URL.
// For multi-tenant (key=tenantID), returns the tenant-specific URL.
// Returns an error if messaging is not configured or misconfigured.
func (s *TenantStore) BrokerURL(_ context.Context, key string) (string, error) {
	// Single-tenant case
	if key == "" {
		if s.defaultMessaging == nil || s.defaultMessaging.Broker.URL == "" {
			return "", NewNotConfiguredError("messaging.broker.url", "MESSAGING_BROKER_URL", "messaging.broker.url")
		}
		return s.defaultMessaging.Broker.URL, nil
	}

	// Multi-tenant case
	s.mu.RLock()
	tenant, exists := s.tenants[key]
	s.mu.RUnlock()
	if !exists {
		return "", NewMultiTenantError(key, "tenant", configNotFoundErrMsg, fmt.Sprintf("check multitenant.tenants.%s section or verify dynamic tenant source", key))
	}

	if tenant.Messaging.URL == "" {
		return "", NewMultiTenantError(key, "messaging.url", notConfiguredErrMsg, fmt.Sprintf("add multitenant.tenants.%s.messaging.url", key))
	}

	return tenant.Messaging.URL, nil
}

// CacheConfig returns the cache configuration for the given key.
// For single-tenant (key=""), returns the default cache config.
// For multi-tenant (key=tenantID), returns the tenant-specific cache config.
func (s *TenantStore) CacheConfig(_ context.Context, key string) (*CacheConfig, error) {
	// Single-tenant case
	if key == "" {
		if s.defaultCache == nil || !s.defaultCache.Enabled {
			return nil, NewNotConfiguredError("cache", "CACHE_REDIS_HOST", "cache.redis.host")
		}
		return s.defaultCache, nil
	}

	// Multi-tenant case
	s.mu.RLock()
	tenant, exists := s.tenants[key]
	s.mu.RUnlock()
	if !exists {
		return nil, NewMultiTenantError(key, "tenant", configNotFoundErrMsg, fmt.Sprintf("check multitenant.tenants.%s section or verify dynamic tenant source", key))
	}

	if !tenant.Cache.Enabled {
		return nil, NewMultiTenantError(key, "cache", notEnabledErrMsg, fmt.Sprintf("set multitenant.tenants.%s.cache.enabled: true", key))
	}

	return &tenant.Cache, nil
}

// AddTenant adds a new tenant configuration at runtime (useful for dynamic tenant management)
func (s *TenantStore) AddTenant(tenantID string, entry *TenantEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants[tenantID] = *entry
}

// RemoveTenant removes a tenant configuration at runtime
func (s *TenantStore) RemoveTenant(tenantID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tenants, tenantID)
}

// Tenants returns a copy of all tenant configurations
func (s *TenantStore) Tenants() map[string]TenantEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]TenantEntry, len(s.tenants))
	for k := range s.tenants {
		result[k] = s.tenants[k]
	}
	return result
}

// HasTenant checks if a tenant configuration exists
func (s *TenantStore) HasTenant(tenantID string) bool {
	s.mu.RLock()
	_, exists := s.tenants[tenantID]
	s.mu.RUnlock()
	return exists
}

// IsDynamic returns false since this store uses static YAML configuration
func (s *TenantStore) IsDynamic() bool {
	return false
}
