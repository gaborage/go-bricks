package multitenant

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
)

// mockTenantConfigProvider implements TenantConfigProvider for testing
type mockTenantConfigProvider struct {
	configs map[string]*config.DatabaseConfig
}

func (m *mockTenantConfigProvider) GetDatabase(_ context.Context, tenantID string) (*config.DatabaseConfig, error) {
	if dbConfig, exists := m.configs[tenantID]; exists {
		return dbConfig, nil
	}
	return nil, errors.New("tenant not found")
}

func (m *mockTenantConfigProvider) GetMessaging(_ context.Context, _ string) (*TenantMessagingConfig, error) {
	// For testing cleanup functionality, we don't need messaging config
	return nil, errors.New("messaging not implemented in test")
}

func newTestLogger() logger.Logger {
	return logger.New("debug", false)
}

func TestTenantConnectionManagerStartStopCleanup(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{
		configs: map[string]*config.DatabaseConfig{
			"tenant1": {Type: "postgres", Host: "localhost", Database: "tenant1"},
		},
	}

	manager := NewTenantConnectionManager(provider, nil, log, WithIdleTTL(100*time.Millisecond))

	// Test starting cleanup
	manager.StartCleanup(50 * time.Millisecond)

	// Verify cleanup ticker is running
	assert.NotNil(t, manager.cleanupTicker)
	assert.NotNil(t, manager.cleanupStop)

	// Test stopping cleanup
	manager.StopCleanup()

	// Give goroutine time to exit
	time.Sleep(10 * time.Millisecond)

	// Verify cleanup is stopped
	assert.Nil(t, manager.cleanupTicker)
	assert.Nil(t, manager.cleanupStop)
}

func TestTenantConnectionManagerCleanupDisabled(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{}

	manager := NewTenantConnectionManager(provider, nil, log)

	// Test with zero interval (disabled)
	manager.StartCleanup(0)
	assert.Nil(t, manager.cleanupTicker)
	assert.Nil(t, manager.cleanupStop)

	// Test with negative interval (disabled)
	manager.StartCleanup(-1 * time.Second)
	assert.Nil(t, manager.cleanupTicker)
	assert.Nil(t, manager.cleanupStop)
}

func TestTenantConnectionManagerCloseStopsCleanup(t *testing.T) {
	log := newTestLogger()
	provider := &mockTenantConfigProvider{}

	manager := NewTenantConnectionManager(provider, nil, log)

	// Start cleanup
	manager.StartCleanup(1 * time.Second)
	assert.NotNil(t, manager.cleanupTicker)

	// Close manager should stop cleanup
	err := manager.Close()
	assert.NoError(t, err)

	// Give goroutine time to exit
	time.Sleep(10 * time.Millisecond)

	assert.Nil(t, manager.cleanupTicker)
	assert.Nil(t, manager.cleanupStop)
}

func TestTenantConnectionManagerGetDatabase(t *testing.T) {
	t.Skip("TODO: implement full database connection tests with proper mocking")
}
