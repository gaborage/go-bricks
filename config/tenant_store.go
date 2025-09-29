package config

import (
	"context"
	"fmt"
	"sync"
)

// TenantStore provides per-key database and messaging configurations.
// This is the default config-backed implementation that uses the static tenant map.
type TenantStore struct {
	// Single-tenant configurations (used when key is "")
	defaultDB        *DatabaseConfig
	defaultMessaging *MessagingConfig

	// Multi-tenant configurations (used when key is tenant ID)
	tenants map[string]TenantEntry
	mu      sync.RWMutex
}

// NewTenantStore creates a config-backed tenant store
func NewTenantStore(cfg *Config) *TenantStore {
	source := &TenantStore{
		defaultDB:        &cfg.Database,
		defaultMessaging: &cfg.Messaging,
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
			return nil, fmt.Errorf("no default database configuration available")
		}
		return s.defaultDB, nil
	}

	// Multi-tenant case
	s.mu.RLock()
	tenant, exists := s.tenants[key]
	s.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("no database configuration found for tenant: %s", key)
	}

	return &tenant.Database, nil
}

// AMQPURL returns the AMQP URL for the given key.
// For single-tenant (key=""), returns the default broker URL.
// For multi-tenant (key=tenantID), returns the tenant-specific URL.
func (s *TenantStore) AMQPURL(_ context.Context, key string) (string, error) {
	// Single-tenant case
	if key == "" {
		if s.defaultMessaging == nil {
			return "", fmt.Errorf("no default messaging configuration available")
		}
		// Use the broker URL from messaging config
		if s.defaultMessaging.Broker.URL == "" {
			return "", fmt.Errorf("no broker URL configured in default messaging config")
		}
		return s.defaultMessaging.Broker.URL, nil
	}

	// Multi-tenant case
	s.mu.RLock()
	tenant, exists := s.tenants[key]
	s.mu.RUnlock()
	if !exists {
		return "", fmt.Errorf("no messaging configuration found for tenant: %s", key)
	}

	if tenant.Messaging.URL == "" {
		return "", fmt.Errorf("empty AMQP URL for tenant: %s", key)
	}

	return tenant.Messaging.URL, nil
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

// GetTenants returns a copy of all tenant configurations
func (s *TenantStore) GetTenants() map[string]TenantEntry {
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
