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

const (
	tenantA = "tenant-a"
	tenantB = "tenant-b"
	tenantC = "tenant-c"
)

func TestMultiTenantMessagingEndToEnd(t *testing.T) {
	// Setup provider with multiple tenant configurations
	provider := newMockMessagingProvider()
	provider.setTenantConfig(tenantA, &TenantMessagingConfig{
		URL: "amqp://tenant-a:pass@localhost:5672/tenant-a",
	})
	provider.setTenantConfig(tenantB, &TenantMessagingConfig{
		URL: "amqp://tenant-b:pass@localhost:5672/tenant-b",
	})
	provider.setTenantConfig(tenantC, &TenantMessagingConfig{
		URL: "amqp://tenant-c:pass@localhost:5672/tenant-c",
	})

	// Create messaging manager with mock factory
	cache := NewTenantConfigCache(provider)
	log := logger.New("debug", true)
	mockFactory := &MockAMQPClientFactory{}
	manager := NewTenantMessagingManagerWithFactory(provider, cache, log, 1*time.Minute, 10, mockFactory)

	ctx := context.Background()

	// Test 1: Multiple tenants can get publishers concurrently
	t.Run("ConcurrentPublisherAccess", func(t *testing.T) {
		var wg sync.WaitGroup
		results := make([]messaging.AMQPClient, 3)
		errors := make([]error, 3)

		tenants := []string{tenantA, tenantB, tenantC}

		for i, tenant := range tenants {
			wg.Add(1)
			go func(idx int, tid string) {
				defer wg.Done()
				client, err := manager.GetPublisher(ctx, tid)
				results[idx] = client
				errors[idx] = err
			}(i, tenant)
		}

		wg.Wait()

		// All should succeed
		for i, tenant := range tenants {
			assert.NoError(t, errors[i], "Tenant %s should get publisher without error", tenant)
			assert.NotNil(t, results[i], "Tenant %s should have non-nil client", tenant)

			// Verify it's the correct wrapper type
			wrapper, ok := results[i].(*TenantAMQPClient)
			assert.True(t, ok, "Client should be TenantAMQPClient wrapper")
			assert.Equal(t, tenant, wrapper.tenantID, "Wrapper should have correct tenant ID")
		}

		// Verify all publishers are cached
		manager.pubMu.RLock()
		assert.Len(t, manager.publishers, 3, "All publishers should be cached")
		manager.pubMu.RUnlock()
	})

	// Test 2: Consumer setup works for multiple tenants
	t.Run("ConsumerSetup", func(t *testing.T) {
		declarations := NewMessagingDeclarations()

		// Add some declarations to test replay functionality
		declarations.Exchanges[testExchange] = &messaging.ExchangeDeclaration{
			Name:       testExchange,
			Type:       "topic",
			Durable:    true,
			AutoDelete: false,
			Internal:   false,
			NoWait:     false,
			Args:       nil,
		}
		declarations.Queues[testQueue] = &messaging.QueueDeclaration{
			Name:       testQueue,
			Durable:    true,
			AutoDelete: false,
			Exclusive:  false,
			NoWait:     false,
			Args:       nil,
		}
		declarations.Bindings = append(declarations.Bindings, &messaging.BindingDeclaration{
			Queue:      testQueue,
			Exchange:   testExchange,
			RoutingKey: "test.key",
			NoWait:     false,
			Args:       nil,
		})

		// Set up consumers for all tenants
		tenants := []string{tenantA, tenantB, tenantC}
		for _, tenant := range tenants {
			err := manager.EnsureConsumers(ctx, tenant, declarations)
			assert.NoError(t, err, "Consumer setup should succeed for tenant %s", tenant)
		}

		// Verify all consumers are created and running
		manager.consMu.RLock()
		assert.Len(t, manager.consumers, 3, "All consumers should be created")
		for _, tenant := range tenants {
			consumer := manager.consumers[tenant]
			assert.NotNil(t, consumer, "Consumer should exist for tenant %s", tenant)
			assert.True(t, consumer.running, "Consumer should be running for tenant %s", tenant)
		}
		manager.consMu.RUnlock()

		// Test idempotency - second call should not create duplicate consumers
		for _, tenant := range tenants {
			err := manager.EnsureConsumers(ctx, tenant, declarations)
			assert.NoError(t, err, "Idempotent consumer setup should succeed for tenant %s", tenant)
		}

		// Should still have exactly 3 consumers
		manager.consMu.RLock()
		assert.Len(t, manager.consumers, 3, "Should still have exactly 3 consumers after idempotent calls")
		manager.consMu.RUnlock()
	})

	// Test 3: Publisher publishing with tenant ID injection
	t.Run("PublisherTenantIDInjection", func(t *testing.T) {
		client, err := manager.GetPublisher(ctx, tenantA)
		require.NoError(t, err)
		require.NotNil(t, client)

		// Verify it's a wrapper that injects tenant_id
		wrapper, ok := client.(*TenantAMQPClient)
		require.True(t, ok, "Client should be TenantAMQPClient wrapper")
		assert.Equal(t, tenantA, wrapper.tenantID)

		// Test publishing (using RecordingAMQPClient underneath)
		err = wrapper.PublishToExchange(ctx, messaging.PublishOptions{
			Exchange:   testExchange,
			RoutingKey: "test.key",
			Headers: map[string]any{
				"custom_header": "custom_value",
			},
		}, []byte("test message"))
		assert.NoError(t, err, "Publishing should succeed")

		// Verify the base client was called
		baseClient, ok := wrapper.base.(*RecordingAMQPClient)
		require.True(t, ok, "Base client should be RecordingAMQPClient")
		counts := baseClient.GetCallCounts()
		assert.Equal(t, 1, counts.Publish, "Should have made one publish call")
	})

	// Test 4: Cleanup functionality
	t.Run("PublisherCleanup", func(t *testing.T) {
		// Create a manager with very short TTL for testing
		// Create a manager with very short TTL for testing
		shortTTLManager := NewTenantMessagingManagerWithFactory(provider, cache, log, 50*time.Millisecond, 10, &MockAMQPClientFactory{})
		defer shortTTLManager.Close()

		// Get publishers for multiple tenants
		tenants := []string{tenantA, tenantB}
		for _, tenant := range tenants {
			client, err := shortTTLManager.GetPublisher(ctx, tenant)
			assert.NoError(t, err)
			assert.NotNil(t, client)
		}

		// Verify they're cached
		shortTTLManager.pubMu.RLock()
		initialCount := len(shortTTLManager.publishers)
		shortTTLManager.pubMu.RUnlock()
		assert.Equal(t, 2, initialCount)

		// Wait for TTL to expire
		time.Sleep(100 * time.Millisecond)

		// Run cleanup
		shortTTLManager.CleanupPublishers()

		// Publishers should be cleaned up
		shortTTLManager.pubMu.RLock()
		finalCount := len(shortTTLManager.publishers)
		shortTTLManager.pubMu.RUnlock()
		assert.Equal(t, 0, finalCount, "All publishers should be cleaned up after TTL expiry")

		// Close the test manager
		err := shortTTLManager.Close()
		assert.NoError(t, err)
	})

	// Test 5: Manager lifecycle (startup, cleanup, shutdown)
	t.Run("ManagerLifecycle", func(t *testing.T) {
		// Create fresh manager for lifecycle test
		lifecycleManager := NewTenantMessagingManagerWithFactory(provider, cache, log, 1*time.Minute, 5, &MockAMQPClientFactory{})

		// Create some publishers using existing tenant configurations
		testTenants := []string{tenantA, tenantB, tenantC}
		for _, tenant := range testTenants {
			client, err := lifecycleManager.GetPublisher(ctx, tenant)
			assert.NoError(t, err)
			assert.NotNil(t, client)
		}

		// Start cleanup with longer interval to reduce race conditions
		lifecycleManager.StartCleanup(500 * time.Millisecond)

		// Give time for cleanup to initialize properly
		time.Sleep(100 * time.Millisecond)

		// Close manager (which will also stop cleanup)
		err := lifecycleManager.Close()
		assert.NoError(t, err)

		// The Close method should have cleaned up all resources
		// We verify success by the fact that Close() returns without error
	})

	// Test 6: Error handling scenarios
	t.Run("ErrorHandling", func(t *testing.T) {
		// Test empty tenant ID
		client, err := manager.GetPublisher(ctx, "")
		assert.Error(t, err)
		assert.Nil(t, client)
		assert.Contains(t, err.Error(), "tenant id is required")

		// Test nonexistent tenant
		client, err = manager.GetPublisher(ctx, "nonexistent-tenant")
		assert.Error(t, err)
		assert.Nil(t, client)

		// Test consumer setup with empty tenant ID
		declarations := NewMessagingDeclarations()
		err = manager.EnsureConsumers(ctx, "", declarations)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "tenant id is required")

		// Test consumer setup with nonexistent tenant
		err = manager.EnsureConsumers(ctx, "nonexistent-tenant", declarations)
		assert.Error(t, err)
	})

	// Clean up the main manager
	err := manager.Close()
	assert.NoError(t, err)
}

func TestMultiTenantMessagingLRUEviction(t *testing.T) {
	provider := newMockMessagingProvider()

	// Set up 5 tenant configurations
	for i := 1; i <= 5; i++ {
		provider.setTenantConfig(fmt.Sprintf("tenant-%d", i), &TenantMessagingConfig{
			URL: fmt.Sprintf("amqp://tenant-%d:pass@localhost:5672/tenant-%d", i, i),
		})
	}

	cache := NewTenantConfigCache(provider)
	log := logger.New("debug", true)

	// Create manager with max 3 publishers to test eviction
	manager := NewTenantMessagingManagerWithFactory(provider, cache, log, 5*time.Minute, 3, &MockAMQPClientFactory{})
	defer manager.Close()

	ctx := context.Background()

	// Fill up the cache with 3 publishers
	for i := 1; i <= 3; i++ {
		client, err := manager.GetPublisher(ctx, fmt.Sprintf("tenant-%d", i))
		require.NoError(t, err)
		assert.NotNil(t, client)
	}

	// Verify cache is full
	manager.pubMu.RLock()
	assert.Len(t, manager.publishers, 3)
	manager.pubMu.RUnlock()

	// Add 4th publisher - should trigger LRU eviction
	client4, err := manager.GetPublisher(ctx, "tenant-4")
	require.NoError(t, err)
	assert.NotNil(t, client4)

	// Should still have 3 publishers, but tenant-1 should be evicted
	manager.pubMu.RLock()
	assert.Len(t, manager.publishers, 3)
	_, tenant1Exists := manager.publishers["tenant-1"]
	_, tenant4Exists := manager.publishers["tenant-4"]
	assert.False(t, tenant1Exists, "tenant-1 should be evicted (oldest)")
	assert.True(t, tenant4Exists, "tenant-4 should be present (newest)")
	manager.pubMu.RUnlock()

	// Access tenant-2 to make it most recently used
	client2, err := manager.GetPublisher(ctx, "tenant-2")
	require.NoError(t, err)
	assert.NotNil(t, client2)

	// Add 5th publisher - should evict tenant-3 (now oldest)
	client5, err := manager.GetPublisher(ctx, "tenant-5")
	require.NoError(t, err)
	assert.NotNil(t, client5)

	// Should still have 3 publishers, tenant-3 should be evicted
	manager.pubMu.RLock()
	assert.Len(t, manager.publishers, 3)
	_, tenant2Exists := manager.publishers["tenant-2"]
	_, tenant3Exists := manager.publishers["tenant-3"]
	_, tenant5Exists := manager.publishers["tenant-5"]
	assert.True(t, tenant2Exists, "tenant-2 should remain (recently accessed)")
	assert.False(t, tenant3Exists, "tenant-3 should be evicted")
	assert.True(t, tenant5Exists, "tenant-5 should be present (newest)")
	manager.pubMu.RUnlock()
}

func TestMultiTenantMessagingTenantContext(t *testing.T) {
	// Test the tenant context functionality that would be used by the GetMessaging closure

	// Test SetTenant and GetTenant
	ctx := context.Background()

	// Initially no tenant in context
	tenantID, ok := GetTenant(ctx)
	assert.False(t, ok)
	assert.Empty(t, tenantID)

	// Set tenant in context
	tenantCtx := SetTenant(ctx, "test-tenant")

	// Should now have tenant
	tenantID, ok = GetTenant(tenantCtx)
	assert.True(t, ok)
	assert.Equal(t, "test-tenant", tenantID)

	// Original context should still be empty
	tenantID, ok = GetTenant(ctx)
	assert.False(t, ok)
	assert.Empty(t, tenantID)
}

func TestMultiTenantMessagingConfigInjection(t *testing.T) {
	// Test that configuration values are properly structured for injection

	// Test default values from config/config.go
	cfg := &config.Config{
		Multitenant: config.MultitenantConfig{
			Messaging: config.MultitenantMessagingConfig{
				PublisherTTL:    5 * time.Minute,
				MaxPublishers:   50,
				CleanupInterval: 1 * time.Minute,
			},
		},
	}

	// Verify structure matches expected values
	assert.Equal(t, 5*time.Minute, cfg.Multitenant.Messaging.PublisherTTL)
	assert.Equal(t, 50, cfg.Multitenant.Messaging.MaxPublishers)
	assert.Equal(t, 1*time.Minute, cfg.Multitenant.Messaging.CleanupInterval)
}
