package outbox

import (
	"context"
	"testing"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModuleName(t *testing.T) {
	m := NewModule()
	assert.Equal(t, "outbox", m.Name())
}

func TestModuleInitDisabled(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: false},
		},
	}

	err := m.Init(deps)
	require.NoError(t, err)
	assert.Nil(t, m.publisher, "Publisher should be nil when outbox is disabled")
}

func TestModuleInitEnabledWithNilDB(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: true},
		},
		DB: nil,
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database resolver")
}

func TestModuleInitEnabledWithNilMessaging(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: true},
		},
		DB: func(_ context.Context) (dbtypes.Interface, error) {
			return nil, nil
		},
		Messaging: nil,
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messaging resolver")
}

func TestModuleInitEnabledWithBothResolvers(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: true},
			Messaging: config.MessagingConfig{
				Broker: config.BrokerConfig{URL: "amqp://localhost"},
			},
		},
		DB: func(_ context.Context) (dbtypes.Interface, error) {
			return nil, nil
		},
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	err := m.Init(deps)
	require.NoError(t, err)
	assert.NotNil(t, m.publisher, "Publisher should be initialized when outbox is enabled")
}

func TestModuleInitDisabledAllowsNilResolvers(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: false},
		},
		DB:        nil,
		Messaging: nil,
	}

	err := m.Init(deps)
	require.NoError(t, err, "Nil resolvers should be allowed when outbox is disabled")
}

// TestModuleInitEnabledMessagingUnconfiguredSingleTenant guards issue #366:
// outbox.enabled=true with no messaging.broker.url must fail at startup
// instead of letting the relay job log "messaging not available" each poll.
func TestModuleInitEnabledMessagingUnconfiguredSingleTenant(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox: config.OutboxConfig{Enabled: true},
			// Messaging.Broker.URL intentionally empty.
		},
		DB: func(_ context.Context) (dbtypes.Interface, error) {
			return nil, nil
		},
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	err := m.Init(deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messaging is not configured")
}

// TestModuleInitEnabledMessagingUnconfiguredMultiTenant verifies the static
// check is skipped when multitenant.enabled=true — each tenant supplies its
// own broker URL via the resource source, so a global check would be wrong.
func TestModuleInitEnabledMessagingUnconfiguredMultiTenant(t *testing.T) {
	m := NewModule()
	deps := &app.ModuleDeps{
		Logger: logger.New("info", false),
		Config: &config.Config{
			Outbox:      config.OutboxConfig{Enabled: true},
			Multitenant: config.MultitenantConfig{Enabled: true},
			// Messaging.Broker.URL intentionally empty.
		},
		DB: func(_ context.Context) (dbtypes.Interface, error) {
			return nil, nil
		},
		Messaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	err := m.Init(deps)
	require.NoError(t, err)
	assert.NotNil(t, m.publisher, "Publisher should be initialized in multi-tenant mode even with empty global broker URL")
}
