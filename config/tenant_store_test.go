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
	tenantADB     = "tenant-a.db"
	defaultDB     = "default.db"
	legacyDB      = "legacy.db"
	analyticsDB   = "analytics.db"
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
						Host:     tenantADB,
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
						Host:     tenantADB,
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

// Tests for named databases feature

func TestTenantStoreNamedDatabases(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{
			Type:     PostgreSQL,
			Host:     defaultDB,
			Port:     5432,
			Database: "main_db",
		},
		Databases: map[string]DatabaseConfig{
			"legacy": {
				Type:     Oracle,
				Host:     legacyDB,
				Port:     1521,
				Database: "legacy_db",
			},
			"analytics": {
				Type:     PostgreSQL,
				Host:     analyticsDB,
				Port:     5432,
				Database: "analytics_db",
			},
		},
		Multitenant: MultitenantConfig{Enabled: false},
	}

	store := NewTenantStore(cfg)

	t.Run("returns default database for empty key", func(t *testing.T) {
		dbCfg, err := store.DBConfig(context.Background(), "")
		assert.NoError(t, err)
		assert.Equal(t, defaultDB, dbCfg.Host)
		assert.Equal(t, PostgreSQL, dbCfg.Type)
	})

	t.Run("returns named database for named prefix", func(t *testing.T) {
		dbCfg, err := store.DBConfig(context.Background(), NamedDatabasePrefix+"legacy")
		assert.NoError(t, err)
		assert.Equal(t, legacyDB, dbCfg.Host)
		assert.Equal(t, Oracle, dbCfg.Type)
	})

	t.Run("returns different named database", func(t *testing.T) {
		dbCfg, err := store.DBConfig(context.Background(), NamedDatabasePrefix+"analytics")
		assert.NoError(t, err)
		assert.Equal(t, analyticsDB, dbCfg.Host)
		assert.Equal(t, PostgreSQL, dbCfg.Type)
	})

	t.Run("returns error for unknown named database", func(t *testing.T) {
		_, err := store.DBConfig(context.Background(), NamedDatabasePrefix+"unknown")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown")
		assert.Contains(t, err.Error(), "not found")
	})
}

func TestTenantStoreNamedDatabasesHelpers(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{Type: PostgreSQL, Host: defaultDB},
		Databases: map[string]DatabaseConfig{
			"legacy":    {Type: Oracle, Host: legacyDB},
			"analytics": {Type: PostgreSQL, Host: analyticsDB},
		},
	}

	store := NewTenantStore(cfg)

	t.Run("NamedDatabases returns all named databases", func(t *testing.T) {
		named := store.NamedDatabases()
		assert.Len(t, named, 2)
		assert.Contains(t, named, "legacy")
		assert.Contains(t, named, "analytics")
		assert.Equal(t, legacyDB, named["legacy"].Host)
		assert.Equal(t, analyticsDB, named["analytics"].Host)
	})

	t.Run("NamedDatabases returns a copy", func(t *testing.T) {
		named := store.NamedDatabases()
		delete(named, "legacy")
		// Original should still have legacy
		assert.True(t, store.HasNamedDatabase("legacy"))
	})

	t.Run("HasNamedDatabase returns true for existing", func(t *testing.T) {
		assert.True(t, store.HasNamedDatabase("legacy"))
		assert.True(t, store.HasNamedDatabase("analytics"))
	})

	t.Run("HasNamedDatabase returns false for non-existing", func(t *testing.T) {
		assert.False(t, store.HasNamedDatabase("unknown"))
		assert.False(t, store.HasNamedDatabase(""))
	})
}

func TestTenantStoreNamedDatabasesWithMultitenant(t *testing.T) {
	// Named databases work alongside multi-tenant databases
	cfg := &Config{
		Database: DatabaseConfig{Type: PostgreSQL, Host: defaultDB},
		Databases: map[string]DatabaseConfig{
			"shared_analytics": {Type: PostgreSQL, Host: "shared-analytics.db"},
		},
		Multitenant: MultitenantConfig{
			Enabled: true,
			Tenants: map[string]TenantEntry{
				tenantA: {
					Database: DatabaseConfig{Type: PostgreSQL, Host: tenantADB},
				},
			},
		},
	}

	store := NewTenantStore(cfg)

	t.Run("named database works in multi-tenant mode", func(t *testing.T) {
		// Named database (shared across tenants)
		dbCfg, err := store.DBConfig(context.Background(), NamedDatabasePrefix+"shared_analytics")
		assert.NoError(t, err)
		assert.Equal(t, "shared-analytics.db", dbCfg.Host)
	})

	t.Run("tenant database still works", func(t *testing.T) {
		// Tenant-specific database
		dbCfg, err := store.DBConfig(context.Background(), tenantA)
		assert.NoError(t, err)
		assert.Equal(t, tenantADB, dbCfg.Host)
	})
}

func TestTenantStoreEmptyNamedDatabases(t *testing.T) {
	cfg := &Config{
		Database:  DatabaseConfig{Type: PostgreSQL, Host: defaultDB},
		Databases: nil, // No named databases configured
	}

	store := NewTenantStore(cfg)

	t.Run("returns error for named prefix when no named DBs configured", func(t *testing.T) {
		_, err := store.DBConfig(context.Background(), NamedDatabasePrefix+"anything")
		assert.Error(t, err)
	})

	t.Run("NamedDatabases returns empty map", func(t *testing.T) {
		named := store.NamedDatabases()
		assert.Empty(t, named)
	})

	t.Run("HasNamedDatabase returns false", func(t *testing.T) {
		assert.False(t, store.HasNamedDatabase("anything"))
	})
}
