package mocks

import (
	"context"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/multitenant"
)

// MockProvider is a simple in-memory TenantConfigProvider for tests.
type MockProvider struct {
	DatabaseConfigs  map[string]*config.DatabaseConfig
	MessagingConfigs map[string]*multitenant.TenantMessagingConfig
}

// GetDatabase returns a database config for the tenant if available.
func (m *MockProvider) GetDatabase(_ context.Context, tenantID string) (*config.DatabaseConfig, error) {
	if cfg, ok := m.DatabaseConfigs[tenantID]; ok {
		return cfg, nil
	}
	return nil, multitenant.ErrTenantNotFound
}

// GetMessaging returns a messaging config for the tenant if available.
func (m *MockProvider) GetMessaging(_ context.Context, tenantID string) (*multitenant.TenantMessagingConfig, error) {
	if cfg, ok := m.MessagingConfigs[tenantID]; ok {
		return cfg, nil
	}
	return nil, multitenant.ErrTenantNotFound
}

// NewMockProvider creates a MockProvider with optional seed data.
func NewMockProvider() *MockProvider {
	return &MockProvider{
		DatabaseConfigs:  make(map[string]*config.DatabaseConfig),
		MessagingConfigs: make(map[string]*multitenant.TenantMessagingConfig),
	}
}
