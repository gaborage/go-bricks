package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	tenantAMQPURL = "amqp://tenant-a"
	tenantA       = "tenant-a"
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

	url, err := source.AMQPURL(context.Background(), "")
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

	url, err := source.AMQPURL(context.Background(), tenantA)
	assert.NoError(t, err)
	assert.Equal(t, tenantAMQPURL, url)

	_, err = source.DBConfig(context.Background(), "unknown")
	assert.Error(t, err)

	_, err = source.AMQPURL(context.Background(), "unknown")
	assert.Error(t, err)
}
