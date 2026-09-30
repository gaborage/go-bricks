package outbox

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	testmocks "github.com/gaborage/go-bricks/testing/mocks"
)

const tenantBrokerURL = "amqp://tenant/"

// planStore is a caller-supplied app.TenantStore: it serves every database key, serves the
// broker for "" only when servesRoot, serves every tenant's broker, and has no cache.
type planStore struct {
	servesRoot bool
	dynamic    bool
}

func (s *planStore) DBConfig(context.Context, string) (*config.DatabaseConfig, error) {
	return &config.DatabaseConfig{Type: config.PostgreSQL, Host: "db.internal", Port: 5432, Database: "app"}, nil
}

func (s *planStore) BrokerURL(_ context.Context, key string) (string, error) {
	if key == "" && !s.servesRoot {
		return "", config.NewNotConfiguredError("messaging", "MESSAGING_BROKER_URL", "messaging.broker.url")
	}
	return tenantBrokerURL, nil
}

func (s *planStore) CacheConfig(context.Context, string) (*config.CacheConfig, error) {
	return nil, config.NewNotConfiguredError("cache", "CACHE_ENABLED", "cache.enabled")
}

func (s *planStore) IsDynamic() bool { return s.dynamic }

// planAppConfig is a valid config for the App-level rows; each row layers its axes on top.
func planAppConfig() *config.Config {
	return &config.Config{
		App: config.AppConfig{Name: "outbox-test", Version: "1.0.0", Env: "development"},
		Server: config.ServerConfig{
			Port:    8080,
			Timeout: config.TimeoutConfig{Read: 15 * time.Second, Write: 30 * time.Second, Middleware: 5 * time.Second, Shutdown: 10 * time.Second},
		},
		Log:    config.LogConfig{Level: "error"},
		Outbox: config.OutboxConfig{Enabled: true},
	}
}

func withMultitenant(cfg *config.Config, messagingTenancy string, tenants map[string]config.TenantEntry) {
	cfg.Multitenant = config.MultitenantConfig{
		Enabled:  true,
		Resolver: config.ResolverConfig{Type: config.ResolverTypeHeader, Header: "X-Tenant-ID"},
		Tenants:  tenants,
	}
	cfg.Messaging.Tenancy = messagingTenancy
}

func tenantEntry(brokerURL string) config.TenantEntry {
	return config.TenantEntry{
		Database:  config.DatabaseConfig{Type: config.PostgreSQL, Host: "acme.db", Port: 5432, Database: "acme", Username: "acme_user"},
		Messaging: config.TenantMessagingConfig{URL: brokerURL},
	}
}

// registerOutboxOnApp builds a real App from cfg and store and returns the verdict of
// registering the outbox on it: the Resource plan, not the test, sets MessagingConfigured.
func registerOutboxOnApp(t *testing.T, cfg *config.Config, store app.TenantStore) error {
	t.Helper()
	opts := &app.Options{
		DatabaseConnector: func(*config.DatabaseConfig, logger.Logger) (database.Interface, error) {
			return probeReadyDB("postgresql"), nil
		},
		MessagingClientFactory: func(string, logger.Logger) messaging.AMQPClient {
			client := testmocks.NewMockAMQPClient()
			client.ExpectClose(nil).Maybe()
			return client
		},
	}
	if store != nil {
		opts.ResourceSource = store
	}
	a, _, err := app.NewWithConfig(cfg, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	return a.RegisterModule(NewModule())
}

// TestModuleInitBrokerCheckReadsTheResourcePlan drives the #366 broker check through a real
// App (ADR-128): the per-tenant ledger refuses exactly when the Resource plan finds messaging
// unavailable, whatever the root messaging block says; the shared ledger still reads root
// config.
func TestModuleInitBrokerCheckReadsTheResourcePlan(t *testing.T) {
	tests := []struct {
		name  string
		setup func(cfg *config.Config)
		store app.TenantStore
		want  []string // substrings of the refusal; empty means Init succeeds
	}{
		{
			name: "mt_shared_messaging_static_tenants_no_root_broker_refuses",
			setup: func(cfg *config.Config) {
				withMultitenant(cfg, config.TenancyShared, map[string]config.TenantEntry{"acme": tenantEntry("")})
			},
			want: []string{"messaging is not configured", "messaging.broker.url"},
		},
		{
			name:  "st_caller_store_serving_root_empty_root_boots",
			setup: func(*config.Config) {},
			store: &planStore{servesRoot: true},
		},
		{
			name:  "st_caller_store_not_serving_root_root_broker_refuses",
			setup: func(cfg *config.Config) { cfg.Messaging.Broker.URL = "amqp://root/" },
			store: &planStore{},
			want:  []string{"messaging is not configured", "messaging.broker.url"},
		},
		{
			name:  "st_dynamic_store_no_root_broker_boots",
			setup: func(cfg *config.Config) { cfg.Source.Type = config.SourceTypeDynamic },
			store: &planStore{dynamic: true},
		},
		{
			name: "mt_shared_messaging_caller_store_not_serving_root_root_broker_refuses",
			setup: func(cfg *config.Config) {
				withMultitenant(cfg, config.TenancyShared, map[string]config.TenantEntry{"acme": tenantEntry("")})
				cfg.Messaging.Broker.URL = "amqp://root/"
			},
			store: &planStore{},
			want:  []string{"messaging is not configured", "messaging.broker.url"},
		},
		{
			name: "mt_per_tenant_messaging_every_tenant_url_set_boots",
			setup: func(cfg *config.Config) {
				withMultitenant(cfg, config.TenancyPerTenant, map[string]config.TenantEntry{
					"acme": tenantEntry(tenantBrokerURL), "globex": tenantEntry(tenantBrokerURL),
				})
			},
		},
		{
			name:  "mt_shared_messaging_zero_tenants_no_root_broker_refuses_on_fan_out_first",
			setup: func(cfg *config.Config) { withMultitenant(cfg, config.TenancyShared, nil) },
			want:  []string{"no static multitenant.tenants"},
		},
		{
			name: "shared_ledger_mt_per_tenant_messaging_zero_tenants_no_root_broker_refuses",
			setup: func(cfg *config.Config) {
				withMultitenant(cfg, config.TenancyPerTenant, nil)
				cfg.Outbox.Tenancy = config.TenancyShared
			},
			want: []string{"root"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := planAppConfig()
			tt.setup(cfg)

			err := registerOutboxOnApp(t, cfg, tt.store)

			if len(tt.want) == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tt.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}
