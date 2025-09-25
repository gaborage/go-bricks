package multitenant

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// MockAMQPClientFactory creates RecordingAMQPClient instances for testing
type MockAMQPClientFactory struct{}

func (f *MockAMQPClientFactory) CreateClient(_ string, _ logger.Logger) messaging.AMQPClient {
	return NewRecordingAMQPClient()
}

// Mock implementation of TenantConfigProvider for messaging testing
type mockMessagingProvider struct {
	messagingConfigs map[string]*TenantMessagingConfig
	delay            time.Duration
	mu               sync.RWMutex
}

func newMockMessagingProvider() *mockMessagingProvider {
	return &mockMessagingProvider{
		messagingConfigs: make(map[string]*TenantMessagingConfig),
	}
}

func (m *mockMessagingProvider) GetMessaging(_ context.Context, tenantID string) (*TenantMessagingConfig, error) {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	msgConfig, ok := m.messagingConfigs[tenantID]
	if !ok {
		return nil, ErrTenantNotFound
	}
	return msgConfig, nil
}

func (m *mockMessagingProvider) GetDatabase(_ context.Context, _ string) (*config.DatabaseConfig, error) {
	return nil, fmt.Errorf("database not implemented in messaging mock")
}

func (m *mockMessagingProvider) setTenantConfig(tenantID string, msgConfig *TenantMessagingConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messagingConfigs[tenantID] = msgConfig
}

func (m *mockMessagingProvider) setDelay(delay time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delay = delay
}

func TestNewTenantMessagingManager(t *testing.T) {
	provider := newMockMessagingProvider()
	cache := NewTenantConfigCache(provider)
	log := logger.New("debug", true)

	manager := NewTenantMessagingManagerWithFactory(provider, cache, log, 5*time.Minute, 10, &MockAMQPClientFactory{})

	assert.NotNil(t, manager)
	assert.Equal(t, provider, manager.provider)
	assert.Equal(t, cache, manager.cache)
	assert.Equal(t, 5*time.Minute, manager.idleTTL)
	assert.Equal(t, 10, manager.maxActivePublishers)
}

func TestNewTenantMessagingManagerWithNilCache(t *testing.T) {
	provider := newMockMessagingProvider()
	log := logger.New("debug", true)

	manager := NewTenantMessagingManagerWithFactory(provider, nil, log, 5*time.Minute, 10, &MockAMQPClientFactory{})

	assert.NotNil(t, manager)
	assert.NotNil(t, manager.cache) // Should create a default cache
}

func TestTenantMessagingManager_GetPublisher(t *testing.T) {
	provider := newMockMessagingProvider()
	provider.setTenantConfig("tenant1", &TenantMessagingConfig{
		URL: "amqp://tenant1:pass@localhost:5672/tenant1",
	})

	cache := NewTenantConfigCache(provider)
	log := logger.New("debug", true)
	manager := NewTenantMessagingManagerWithFactory(provider, cache, log, 5*time.Minute, 10, &MockAMQPClientFactory{})

	ctx := context.Background()

	// First call should create a new publisher
	client1, err := manager.GetPublisher(ctx, "tenant1")
	require.NoError(t, err)
	assert.NotNil(t, client1)

	// Verify it's a TenantAMQPClient wrapper
	wrapper, ok := client1.(*TenantAMQPClient)
	assert.True(t, ok)
	assert.Equal(t, "tenant1", wrapper.tenantID)

	// Second call should return the same cached client
	client2, err := manager.GetPublisher(ctx, "tenant1")
	require.NoError(t, err)
	assert.Same(t, client1, client2)

	// Check that the publisher was cached
	manager.pubMu.RLock()
	assert.Len(t, manager.publishers, 1)
	manager.pubMu.RUnlock()
}

func TestTenantMessagingManager_GetPublisher_EmptyTenantID(t *testing.T) {
	provider := newMockMessagingProvider()
	cache := NewTenantConfigCache(provider)
	log := logger.New("debug", true)
	manager := NewTenantMessagingManagerWithFactory(provider, cache, log, 5*time.Minute, 10, &MockAMQPClientFactory{})

	ctx := context.Background()

	client, err := manager.GetPublisher(ctx, "")
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "tenant id is required")
}

func TestTenantMessagingManager_GetPublisher_TenantNotFound(t *testing.T) {
	provider := newMockMessagingProvider()
	cache := NewTenantConfigCache(provider)
	log := logger.New("debug", true)
	manager := NewTenantMessagingManagerWithFactory(provider, cache, log, 5*time.Minute, 10, &MockAMQPClientFactory{})

	ctx := context.Background()

	client, err := manager.GetPublisher(ctx, "nonexistent")
	assert.Error(t, err)
	assert.Nil(t, client)
}

func TestTenantMessagingManager_GetPublisher_LRUEviction(t *testing.T) {
	provider := newMockMessagingProvider()

	// Set up multiple tenant configurations
	for i := 1; i <= 5; i++ {
		provider.setTenantConfig(fmt.Sprintf("tenant%d", i), &TenantMessagingConfig{
			URL: fmt.Sprintf("amqp://tenant%d:pass@localhost:5672/tenant%d", i, i),
		})
	}

	cache := NewTenantConfigCache(provider)
	log := logger.New("debug", true)
	// Set max publishers to 3 to test eviction
	manager := NewTenantMessagingManagerWithFactory(provider, cache, log, 5*time.Minute, 3, &MockAMQPClientFactory{})

	ctx := context.Background()

	// Create publishers for tenants 1, 2, 3 (should all be cached)
	for i := 1; i <= 3; i++ {
		client, err := manager.GetPublisher(ctx, fmt.Sprintf("tenant%d", i))
		require.NoError(t, err)
		assert.NotNil(t, client)
	}

	// Verify all 3 are cached
	manager.pubMu.RLock()
	assert.Len(t, manager.publishers, 3)
	manager.pubMu.RUnlock()

	// Add 4th publisher - should evict the oldest (tenant1)
	client4, err := manager.GetPublisher(ctx, "tenant4")
	require.NoError(t, err)
	assert.NotNil(t, client4)

	// Should still have 3 publishers, but tenant1 should be evicted
	manager.pubMu.RLock()
	assert.Len(t, manager.publishers, 3)
	_, tenant1Exists := manager.publishers["tenant1"]
	_, tenant4Exists := manager.publishers["tenant4"]
	assert.False(t, tenant1Exists)
	assert.True(t, tenant4Exists)
	manager.pubMu.RUnlock()
}

func TestTenantMessagingManager_GetPublisher_Concurrent(t *testing.T) {
	provider := newMockMessagingProvider()
	provider.setTenantConfig("tenant1", &TenantMessagingConfig{
		URL: "amqp://tenant1:pass@localhost:5672/tenant1",
	})
	// Add delay to make race conditions more likely
	provider.setDelay(10 * time.Millisecond)

	cache := NewTenantConfigCache(provider)
	log := logger.New("debug", true)
	manager := NewTenantMessagingManagerWithFactory(provider, cache, log, 5*time.Minute, 10, &MockAMQPClientFactory{})

	ctx := context.Background()
	const numGoroutines = 10

	// Start multiple goroutines trying to get the same publisher
	var wg sync.WaitGroup
	clients := make([]messaging.AMQPClient, numGoroutines)
	errors := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			client, err := manager.GetPublisher(ctx, "tenant1")
			clients[idx] = client
			errors[idx] = err
		}(i)
	}

	wg.Wait()

	// All should succeed and return the same client (singleflight)
	var firstClient messaging.AMQPClient
	for i, client := range clients {
		assert.NoError(t, errors[i])
		assert.NotNil(t, client)

		if firstClient == nil {
			firstClient = client
		} else {
			assert.Same(t, firstClient, client, "All clients should be the same due to singleflight")
		}
	}

	// Should have exactly one publisher cached
	manager.pubMu.RLock()
	assert.Len(t, manager.publishers, 1)
	manager.pubMu.RUnlock()
}

func TestTenantMessagingManager_EnsureConsumers(t *testing.T) {
	provider := newMockMessagingProvider()
	provider.setTenantConfig("tenant1", &TenantMessagingConfig{
		URL: "amqp://tenant1:pass@localhost:5672/tenant1",
	})

	cache := NewTenantConfigCache(provider)
	log := logger.New("debug", true)
	manager := NewTenantMessagingManagerWithFactory(provider, cache, log, 5*time.Minute, 10, &MockAMQPClientFactory{})

	ctx := context.Background()
	declarations := NewMessagingDeclarations()

	// First call should start consumers
	err := manager.EnsureConsumers(ctx, "tenant1", declarations)
	require.NoError(t, err)

	// Verify consumer was created and marked as running
	manager.consMu.RLock()
	assert.Len(t, manager.consumers, 1)
	consumer := manager.consumers["tenant1"]
	assert.NotNil(t, consumer)
	assert.True(t, consumer.running)
	manager.consMu.RUnlock()

	// Second call should be idempotent (not create another consumer)
	err = manager.EnsureConsumers(ctx, "tenant1", declarations)
	require.NoError(t, err)

	// Should still have exactly one consumer
	manager.consMu.RLock()
	assert.Len(t, manager.consumers, 1)
	manager.consMu.RUnlock()
}

func TestTenantMessagingManager_EnsureConsumers_EmptyTenantID(t *testing.T) {
	provider := newMockMessagingProvider()
	cache := NewTenantConfigCache(provider)
	log := logger.New("debug", true)
	manager := NewTenantMessagingManagerWithFactory(provider, cache, log, 5*time.Minute, 10, &MockAMQPClientFactory{})

	ctx := context.Background()
	declarations := NewMessagingDeclarations()

	err := manager.EnsureConsumers(ctx, "", declarations)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tenant id is required")
}

func TestTenantMessagingManager_EnsureConsumers_TenantNotFound(t *testing.T) {
	provider := newMockMessagingProvider()
	cache := NewTenantConfigCache(provider)
	log := logger.New("debug", true)
	manager := NewTenantMessagingManagerWithFactory(provider, cache, log, 5*time.Minute, 10, &MockAMQPClientFactory{})

	ctx := context.Background()
	declarations := NewMessagingDeclarations()

	err := manager.EnsureConsumers(ctx, "nonexistent", declarations)
	assert.Error(t, err)
}

func TestTenantMessagingManager_EnsureConsumers_Concurrent(t *testing.T) {
	provider := newMockMessagingProvider()
	provider.setTenantConfig("tenant1", &TenantMessagingConfig{
		URL: "amqp://tenant1:pass@localhost:5672/tenant1",
	})
	// Add delay to make race conditions more likely
	provider.setDelay(10 * time.Millisecond)

	cache := NewTenantConfigCache(provider)
	log := logger.New("debug", true)
	manager := NewTenantMessagingManagerWithFactory(provider, cache, log, 5*time.Minute, 10, &MockAMQPClientFactory{})

	ctx := context.Background()
	declarations := NewMessagingDeclarations()
	const numGoroutines = 10

	// Start multiple goroutines trying to ensure consumers for same tenant
	var wg sync.WaitGroup
	errors := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := manager.EnsureConsumers(ctx, "tenant1", declarations)
			errors[idx] = err
		}(i)
	}

	wg.Wait()

	// All should succeed
	for i, err := range errors {
		assert.NoError(t, err, "Goroutine %d should not have error", i)
	}

	// Should have exactly one consumer (singleflight should prevent multiple creations)
	manager.consMu.RLock()
	assert.Len(t, manager.consumers, 1)
	consumer := manager.consumers["tenant1"]
	assert.NotNil(t, consumer)
	assert.True(t, consumer.running)
	manager.consMu.RUnlock()
}

func TestTenantMessagingManager_CleanupPublishers(t *testing.T) {
	provider := newMockMessagingProvider()
	provider.setTenantConfig("tenant1", &TenantMessagingConfig{
		URL: "amqp://tenant1:pass@localhost:5672/tenant1",
	})
	provider.setTenantConfig("tenant2", &TenantMessagingConfig{
		URL: "amqp://tenant2:pass@localhost:5672/tenant2",
	})

	cache := NewTenantConfigCache(provider)
	log := logger.New("debug", true)
	// Use very short TTL for testing
	manager := NewTenantMessagingManagerWithFactory(provider, cache, log, 50*time.Millisecond, 10, &MockAMQPClientFactory{})

	ctx := context.Background()

	// Create publishers for both tenants
	client1, err := manager.GetPublisher(ctx, "tenant1")
	require.NoError(t, err)
	assert.NotNil(t, client1)

	client2, err := manager.GetPublisher(ctx, "tenant2")
	require.NoError(t, err)
	assert.NotNil(t, client2)

	// Both should be cached
	manager.pubMu.RLock()
	assert.Len(t, manager.publishers, 2)
	manager.pubMu.RUnlock()

	// Wait for TTL to expire
	time.Sleep(100 * time.Millisecond)

	// Run cleanup
	manager.CleanupPublishers()

	// Both publishers should be cleaned up due to idle timeout
	manager.pubMu.RLock()
	assert.Empty(t, manager.publishers)
	manager.pubMu.RUnlock()
}

func TestTenantMessagingManager_CleanupPublishers_RecentlyUsed(t *testing.T) {
	provider := newMockMessagingProvider()
	provider.setTenantConfig("tenant1", &TenantMessagingConfig{
		URL: "amqp://tenant1:pass@localhost:5672/tenant1",
	})

	cache := NewTenantConfigCache(provider)
	log := logger.New("debug", true)
	// Use very short TTL for testing
	manager := NewTenantMessagingManagerWithFactory(provider, cache, log, 100*time.Millisecond, 10, &MockAMQPClientFactory{})

	ctx := context.Background()

	// Create publisher
	client1, err := manager.GetPublisher(ctx, "tenant1")
	require.NoError(t, err)
	assert.NotNil(t, client1)

	// Wait half the TTL
	time.Sleep(50 * time.Millisecond)

	// Use the publisher again (should update lastUsed)
	client2, err := manager.GetPublisher(ctx, "tenant1")
	require.NoError(t, err)
	assert.Same(t, client1, client2)

	// Wait the original TTL duration
	time.Sleep(60 * time.Millisecond)

	// Run cleanup - should NOT clean up because lastUsed was updated
	manager.CleanupPublishers()

	// Publisher should still be there
	manager.pubMu.RLock()
	assert.Len(t, manager.publishers, 1)
	manager.pubMu.RUnlock()
}
