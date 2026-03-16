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
