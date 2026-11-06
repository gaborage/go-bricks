package config

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/multitenant"
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
	require.NoError(t, err)
	assert.Same(t, &cfg.Database, dbCfg)

	url, err := source.BrokerURL(context.Background(), "")
	require.NoError(t, err)
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
	require.NoError(t, err)
	assert.Equal(t, cfg.Multitenant.Tenants[tenantA].Database, *dbCfg)

	url, err := source.BrokerURL(context.Background(), tenantA)
	require.NoError(t, err)
	assert.Equal(t, tenantAMQPURL, url)

	_, err = source.DBConfig(context.Background(), "unknown")
	assert.Error(t, err) //nolint:testifylint // a different resolver path is asserted next

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
	require.NoError(t, err)
	assert.Same(t, &cfg.Database, dbCfg)

	// Messaging should return descriptive error
	url, err := source.BrokerURL(context.Background(), "")
	require.Error(t, err)
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
	require.NoError(t, err)
	assert.Equal(t, cfg.Multitenant.Tenants[tenantA].Database, *dbCfg)

	// Messaging should return descriptive error
	url, err := source.BrokerURL(context.Background(), tenantA)
	require.Error(t, err)
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
	assert.Empty(t, store.Tenants())
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
	assert.Len(t, store.Tenants(), 1)

	// Verify we can retrieve configuration for new tenant
	dbCfg, err := store.DBConfig(context.Background(), newTenant)
	require.NoError(t, err)
	assert.Equal(t, "new-tenant.db", dbCfg.Host)

	url, err := store.BrokerURL(context.Background(), newTenant)
	require.NoError(t, err)
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
	assert.Len(t, store.Tenants(), 2)
	assert.True(t, store.HasTenant(tenantA))
	assert.True(t, store.HasTenant(tenantB))

	// Remove tenant-a
	store.RemoveTenant(tenantA)

	// Verify tenant-a is gone
	assert.False(t, store.HasTenant(tenantA))
	assert.True(t, store.HasTenant(tenantB))
	assert.Len(t, store.Tenants(), 1)

	// Verify we get error when trying to access removed tenant
	_, err := store.DBConfig(context.Background(), tenantA)
	assert.Error(t, err) //nolint:testifylint // an independent no-mutation property follows

	// Remove non-existent tenant (should not panic)
	store.RemoveTenant("non-existent")
	assert.Len(t, store.Tenants(), 1)
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
	assert.Len(t, tenants, 2)
	assert.Contains(t, tenants, tenantA)
	assert.Contains(t, tenants, tenantB)

	// Verify it returns a copy (modifying returned map should not affect store)
	delete(tenants, tenantA)
	assert.True(t, store.HasTenant(tenantA)) // Should still exist in store
}

func TestTenantStoreHasTenant(t *testing.T) {
	cfg := &Config{
		Database:  DatabaseConfig{},
		Databases: map[string]DatabaseConfig{"reporting": {Type: PostgreSQL}},
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
	// A named-database key is a resource key, not a tenant id (multitenant.TenantChecker).
	assert.False(t, store.HasTenant(NamedDatabasePrefix+"reporting"))
	// AddTenant does not validate its key, so the contract holds in HasTenant itself.
	store.AddTenant("", &TenantEntry{})
	store.AddTenant(NamedDatabasePrefix+"reporting", &TenantEntry{})
	assert.False(t, store.HasTenant(""))
	assert.False(t, store.HasTenant(NamedDatabasePrefix+"reporting"))
	var nilStore *TenantStore
	assert.False(t, nilStore.HasTenant(tenantA))
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
		require.NoError(t, err)
		assert.Equal(t, defaultDB, dbCfg.Host)
		assert.Equal(t, PostgreSQL, dbCfg.Type)
	})

	t.Run("returns named database for named prefix", func(t *testing.T) {
		dbCfg, err := store.DBConfig(context.Background(), NamedDatabasePrefix+"legacy")
		require.NoError(t, err)
		assert.Equal(t, legacyDB, dbCfg.Host)
		assert.Equal(t, Oracle, dbCfg.Type)
	})

	t.Run("returns different named database", func(t *testing.T) {
		dbCfg, err := store.DBConfig(context.Background(), NamedDatabasePrefix+"analytics")
		require.NoError(t, err)
		assert.Equal(t, analyticsDB, dbCfg.Host)
		assert.Equal(t, PostgreSQL, dbCfg.Type)
	})

	t.Run("returns error for unknown named database", func(t *testing.T) {
		_, err := store.DBConfig(context.Background(), NamedDatabasePrefix+"unknown")
		require.Error(t, err)
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
			"shared-analytics": {Type: PostgreSQL, Host: "shared-analytics.db"},
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
		dbCfg, err := store.DBConfig(context.Background(), NamedDatabasePrefix+"shared-analytics")
		require.NoError(t, err)
		assert.Equal(t, "shared-analytics.db", dbCfg.Host)
	})

	t.Run("tenant database still works", func(t *testing.T) {
		// Tenant-specific database
		dbCfg, err := store.DBConfig(context.Background(), tenantA)
		require.NoError(t, err)
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

func TestTenantStoreDBConfigUnconfiguredDefaultIsNotConfigured(t *testing.T) {
	// A service with no database section at all. Before ADR-047 this returned the
	// zero-value config successfully, and the "unsupported database type" error from
	// the connection factory was what reached the readiness probe.
	store := NewTenantStore(&Config{})

	cfg, err := store.DBConfig(context.Background(), "")

	assert.Nil(t, cfg)
	assert.True(t, IsNotConfigured(err), "absence must be machine-classifiable, not a driver error")
}

func TestTenantStoreDBConfigDefaultKeyResolvesWhenConfigured(t *testing.T) {
	cfg := &Config{}
	cfg.Database.Type = "postgresql"
	cfg.Database.Host = "db.internal"
	store := NewTenantStore(cfg)

	got, err := store.DBConfig(context.Background(), "")

	require.NoError(t, err)
	assert.Equal(t, &cfg.Database, got)
}

func TestTenantStoreDBConfigTenantAndNamedKeysStayLoud(t *testing.T) {
	// The anti-regression test for the whole design: the not-configured verdict is
	// scoped to the DEFAULT key. Only the root block may legitimately be empty — an
	// empty tenant or named database is malformed, and must never be reported as an
	// intentionally database-free service. This fails if the classification is ever
	// moved down into database.NewConnection, which cannot see the key.
	cfg := &Config{}
	cfg.Database.Type = "postgresql"
	cfg.Database.Host = "db.internal"
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Tenants = map[string]TenantEntry{tenantA: {}} // no database block
	cfg.Databases = map[string]DatabaseConfig{"reporting": {}}
	store := NewTenantStore(cfg)

	for _, key := range []string{tenantA, NamedDatabasePrefix + "reporting"} {
		t.Run(key, func(t *testing.T) {
			got, err := store.DBConfig(context.Background(), key)
			if err != nil {
				assert.False(t, IsNotConfigured(err), "a malformed %s must not read as absent", key)
				return
			}
			// Resolving is fine; what matters is that it did not claim absence. The
			// empty type then fails loudly at the connection factory.
			assert.Empty(t, got.Type)
		})
	}
}

func TestTenantStoreUnknownTenantIsErrUnknownTenant(t *testing.T) {
	cfg := &Config{}
	cfg.Multitenant.Enabled = true
	cfg.Multitenant.Tenants = map[string]TenantEntry{tenantA: {}}
	store := NewTenantStore(cfg)
	ctx := context.Background()
	db := func(key string) error { _, err := store.DBConfig(ctx, key); return err }
	cache := func(key string) error { _, err := store.CacheConfig(ctx, key); return err }
	broker := func(key string) error { _, err := store.BrokerURL(ctx, key); return err }

	tests := []struct {
		name    string
		lookup  func(key string) error
		key     string
		unknown bool
	}{
		{name: "db_config", lookup: db, key: "ghost", unknown: true},
		{name: "cache_config", lookup: cache, key: "ghost", unknown: true},
		{name: "broker_url", lookup: broker, key: "ghost", unknown: true},
		{name: "known_tenant_without_messaging_url_unaffected", lookup: broker, key: tenantA},
		{name: "known_tenant_cache_not_enabled_unaffected", lookup: cache, key: tenantA},
		{name: "control_plane_key_unaffected", lookup: db, key: ""},
		{name: "named_database_unaffected", lookup: db, key: NamedDatabasePrefix + "ghost"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.lookup(tc.key)

			require.Error(t, err)
			var cfgErr *ConfigError
			require.ErrorAs(t, err, &cfgErr, "the existing *ConfigError stays reachable")
			assert.Equal(t, tc.unknown, errors.Is(err, multitenant.ErrUnknownTenant))
		})
	}
}
