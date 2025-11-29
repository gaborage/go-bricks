package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	tenantAMQPURL = "amqp://tenant-a"
	tenantA       = "tenant-a"
	newTenant     = "tenant-new"
	tenantB       = "tenant-b"
)

func TestTenantStoreDefaults(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{
			Type:     PostgreSQL,
			Host:     "default-db",
			Port:     5432,
			Database: "app",
		},
		Messaging: MessagingConfig{
			Broker: BrokerConfig{URL: "amqp://default"},
		},
		Multitenant: MultitenantConfig{Enabled: false},
	}

	source := NewTenantStore(cfg)
	dbCfg, err := source.DBConfig(context.Background(), "")
	assert.NoError(t, err)
	assert.Same(t, &cfg.Database, dbCfg)

	url, err := source.BrokerURL(context.Background(), "")
	assert.NoError(t, err)
	assert.Equal(t, cfg.Messaging.Broker.URL, url)
}

func TestTenantStoreTenantOverrides(t *testing.T) {
	cfg := &Config{
		Database:  DatabaseConfig{},
		Messaging: MessagingConfig{},
		Multitenant: MultitenantConfig{
			Enabled: true,
			Tenants: map[string]TenantEntry{
				tenantA: {
					Database: DatabaseConfig{
						Type:     PostgreSQL,
						Host:     "tenant-a.db.local",
						Port:     5432,
						Database: "tenant_a",
					},
					Messaging: TenantMessagingConfig{URL: tenantAMQPURL},
				},
			},
		},
	}

	source := NewTenantStore(cfg)
	dbCfg, err := source.DBConfig(context.Background(), tenantA)
	assert.NoError(t, err)
	assert.Equal(t, cfg.Multitenant.Tenants[tenantA].Database, *dbCfg)

	url, err := source.BrokerURL(context.Background(), tenantA)
	assert.NoError(t, err)
	assert.Equal(t, tenantAMQPURL, url)

	_, err = source.DBConfig(context.Background(), "unknown")
	assert.Error(t, err)

	_, err = source.BrokerURL(context.Background(), "unknown")
	assert.Error(t, err)
}

func TestTenantStoreSingleTenantWithoutMessaging(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{
			Type:     PostgreSQL,
			Host:     "localhost",
			Port:     5432,
			Database: "app",
		},
		Messaging:   MessagingConfig{}, // No messaging configured
		Multitenant: MultitenantConfig{Enabled: false},
	}

	source := NewTenantStore(cfg)

	// Database should work
	dbCfg, err := source.DBConfig(context.Background(), "")
	assert.NoError(t, err)
	assert.Same(t, &cfg.Database, dbCfg)

	// Messaging should return descriptive error
	url, err := source.BrokerURL(context.Background(), "")
	assert.Error(t, err)
	assert.Empty(t, url)
	assert.Contains(t, err.Error(), "messaging")
}

func TestTenantStoreMultiTenantWithoutMessaging(t *testing.T) {
	cfg := &Config{
		Database:  DatabaseConfig{},
		Messaging: MessagingConfig{},
		Multitenant: MultitenantConfig{
			Enabled: true,
			Tenants: map[string]TenantEntry{
				tenantA: {
					Database: DatabaseConfig{
						Type:     PostgreSQL,
						Host:     "tenant-a.db.local",
						Port:     5432,
						Database: "tenant_a",
					},
					Messaging: TenantMessagingConfig{URL: ""}, // No messaging for this tenant
				},
			},
		},
	}

	source := NewTenantStore(cfg)

	// Database should work
	dbCfg, err := source.DBConfig(context.Background(), tenantA)
	assert.NoError(t, err)
	assert.Equal(t, cfg.Multitenant.Tenants[tenantA].Database, *dbCfg)

	// Messaging should return descriptive error
	url, err := source.BrokerURL(context.Background(), tenantA)
	assert.Error(t, err)
	assert.Empty(t, url)
	assert.Contains(t, err.Error(), "messaging")
}

func TestTenantStoreAddTenant(t *testing.T) {
	cfg := &Config{
		Database:  DatabaseConfig{},
		Messaging: MessagingConfig{},
		Multitenant: MultitenantConfig{
			Enabled: true,
			Tenants: map[string]TenantEntry{},
		},
	}

	store := NewTenantStore(cfg)

	// Initially no tenants
	assert.Equal(t, 0, len(store.Tenants()))
	assert.False(t, store.HasTenant(newTenant))

	// Add a new tenant
	newEntry := &TenantEntry{
		Database: DatabaseConfig{
			Type:     PostgreSQL,
			Host:     "new-tenant.db",
			Port:     5432,
			Database: "new_tenant_db",
		},
		Messaging: TenantMessagingConfig{URL: "amqp://new-tenant"},
	}

	store.AddTenant(newTenant, newEntry)

	// Verify tenant was added
	assert.True(t, store.HasTenant(newTenant))
	assert.Equal(t, 1, len(store.Tenants()))

	// Verify we can retrieve configuration for new tenant
	dbCfg, err := store.DBConfig(context.Background(), newTenant)
	assert.NoError(t, err)
	assert.Equal(t, "new-tenant.db", dbCfg.Host)

	url, err := store.BrokerURL(context.Background(), newTenant)
	assert.NoError(t, err)
	assert.Equal(t, "amqp://new-tenant", url)
}

func TestTenantStoreRemoveTenant(t *testing.T) {
	cfg := &Config{
		Database:  DatabaseConfig{},
		Messaging: MessagingConfig{},
		Multitenant: MultitenantConfig{
			Enabled: true,
			Tenants: map[string]TenantEntry{
				tenantA: {
					Database: DatabaseConfig{
						Type:     PostgreSQL,
						Host:     "tenant-a.db",
						Port:     5432,
						Database: "tenant_a",
					},
					Messaging: TenantMessagingConfig{URL: tenantAMQPURL},
				},
				tenantB: {
					Database: DatabaseConfig{
						Type:     PostgreSQL,
						Host:     "tenant-b.db",
						Port:     5432,
						Database: "tenant_b",
					},
					Messaging: TenantMessagingConfig{URL: "amqp://tenant-b"},
				},
			},
		},
	}

	store := NewTenantStore(cfg)

	// Initially 2 tenants
	assert.Equal(t, 2, len(store.Tenants()))
	assert.True(t, store.HasTenant(tenantA))
	assert.True(t, store.HasTenant(tenantB))

	// Remove tenant-a
	store.RemoveTenant(tenantA)

	// Verify tenant-a is gone
	assert.False(t, store.HasTenant(tenantA))
	assert.True(t, store.HasTenant(tenantB))
	assert.Equal(t, 1, len(store.Tenants()))

	// Verify we get error when trying to access removed tenant
	_, err := store.DBConfig(context.Background(), tenantA)
	assert.Error(t, err)

	// Remove non-existent tenant (should not panic)
	store.RemoveTenant("non-existent")
	assert.Equal(t, 1, len(store.Tenants()))
}

func TestTenantStoreTenants(t *testing.T) {
	cfg := &Config{
		Database:  DatabaseConfig{},
		Messaging: MessagingConfig{},
		Multitenant: MultitenantConfig{
			Enabled: true,
			Tenants: map[string]TenantEntry{
				tenantA: {
					Database: DatabaseConfig{
						Type:     PostgreSQL,
						Host:     "tenant-a.db",
						Port:     5432,
						Database: "tenant_a",
					},
					Messaging: TenantMessagingConfig{URL: tenantAMQPURL},
				},
				tenantB: {
					Database: DatabaseConfig{
						Type:     PostgreSQL,
						Host:     "tenant-b.db",
						Port:     5432,
						Database: "tenant_b",
					},
					Messaging: TenantMessagingConfig{URL: "amqp://tenant-b"},
				},
			},
		},
	}

	store := NewTenantStore(cfg)

	// Get all tenants
	tenants := store.Tenants()
	assert.Equal(t, 2, len(tenants))
	assert.Contains(t, tenants, tenantA)
	assert.Contains(t, tenants, tenantB)

	// Verify it returns a copy (modifying returned map should not affect store)
	delete(tenants, tenantA)
	assert.True(t, store.HasTenant(tenantA)) // Should still exist in store
}

func TestTenantStoreHasTenant(t *testing.T) {
	cfg := &Config{
		Database:  DatabaseConfig{},
		Messaging: MessagingConfig{},
		Multitenant: MultitenantConfig{
			Enabled: true,
			Tenants: map[string]TenantEntry{
				tenantA: {
					Database:  DatabaseConfig{Type: PostgreSQL},
					Messaging: TenantMessagingConfig{URL: tenantAMQPURL},
				},
			},
		},
	}

	store := NewTenantStore(cfg)

	// Existing tenant
	assert.True(t, store.HasTenant(tenantA))

	// Non-existent tenant
	assert.False(t, store.HasTenant("non-existent"))
	assert.False(t, store.HasTenant(""))
}

func TestTenantStoreIsDynamic(t *testing.T) {
	cfg := &Config{
		Database:  DatabaseConfig{},
		Messaging: MessagingConfig{},
		Multitenant: MultitenantConfig{
			Enabled: true,
			Tenants: map[string]TenantEntry{},
		},
	}

	store := NewTenantStore(cfg)

	// TenantStore uses static YAML configuration
	assert.False(t, store.IsDynamic())
}
