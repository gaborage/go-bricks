package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

const (
	testKey = "test-key"
)

func TestNewConnectionPreWarmer(t *testing.T) {
	t.Run("creates prewarmer with all components", func(t *testing.T) {
		log := logger.New("debug", true)
		dbManager := &database.DbManager{}
		messagingManager := &messaging.Manager{}

		prewarmer := NewConnectionPreWarmer(log, dbManager, messagingManager)

		assert.NotNil(t, prewarmer)
		assert.Equal(t, log, prewarmer.logger)
		assert.Equal(t, dbManager, prewarmer.dbManager)
		assert.Equal(t, messagingManager, prewarmer.messagingManager)
	})

	t.Run("creates prewarmer with nil managers", func(t *testing.T) {
		log := logger.New("debug", true)

		prewarmer := NewConnectionPreWarmer(log, nil, nil)

		assert.NotNil(t, prewarmer)
		assert.Equal(t, log, prewarmer.logger)
		assert.Nil(t, prewarmer.dbManager)
		assert.Nil(t, prewarmer.messagingManager)
	})
}

func TestPreWarmSingleTenant(t *testing.T) {
	t.Run("works with nil managers", func(t *testing.T) {
		log := logger.New("debug", true)

		prewarmer := &ConnectionPreWarmer{
			logger:           log,
			dbManager:        nil,
			messagingManager: nil,
		}

		declarations := messaging.NewDeclarations()
		err := prewarmer.PreWarmSingleTenant(context.Background(), declarations)

		assert.NoError(t, err)
	})

	t.Run("logs debug messages for nil managers", func(t *testing.T) {
		log := logger.New("debug", true)

		prewarmer := NewConnectionPreWarmer(log, nil, nil)

		declarations := messaging.NewDeclarations()
		err := prewarmer.PreWarmSingleTenant(context.Background(), declarations)

		// Should complete without error when managers are nil
		assert.NoError(t, err)
	})
}

func TestPreWarmDatabase(t *testing.T) {
	t.Run("database prewarming with nil manager", func(t *testing.T) {
		log := logger.New("debug", true)

		prewarmer := &ConnectionPreWarmer{
			logger:    log,
			dbManager: nil,
		}

		err := prewarmer.PreWarmDatabase(context.Background(), testKey)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "database manager not available")
	})
}

func TestPreWarmMessaging(t *testing.T) {
	t.Run("messaging prewarming with nil manager", func(t *testing.T) {
		log := logger.New("debug", true)

		prewarmer := &ConnectionPreWarmer{
			logger:           log,
			messagingManager: nil,
		}

		declarations := messaging.NewDeclarations()
		err := prewarmer.PreWarmMessaging(context.Background(), testKey, declarations)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "messaging manager not available")
	})

	t.Run("messaging prewarming with nil declarations", func(t *testing.T) {
		log := logger.New("debug", true)

		prewarmer := &ConnectionPreWarmer{
			logger:           log,
			messagingManager: nil,
		}

		err := prewarmer.PreWarmMessaging(context.Background(), testKey, nil)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "messaging manager not available")
	})
}

func TestPrewarmerIsAvailable(t *testing.T) {
	t.Run("returns true when both managers available", func(t *testing.T) {
		prewarmer := &ConnectionPreWarmer{
			dbManager:        &database.DbManager{},
			messagingManager: &messaging.Manager{},
		}

		assert.True(t, prewarmer.IsAvailable())
	})

	t.Run("returns true when only db manager available", func(t *testing.T) {
		prewarmer := &ConnectionPreWarmer{
			dbManager:        &database.DbManager{},
			messagingManager: nil,
		}

		assert.True(t, prewarmer.IsAvailable())
	})

	t.Run("returns true when only messaging manager available", func(t *testing.T) {
		prewarmer := &ConnectionPreWarmer{
			dbManager:        nil,
			messagingManager: &messaging.Manager{},
		}

		assert.True(t, prewarmer.IsAvailable())
	})

	t.Run("returns false when no managers available", func(t *testing.T) {
		prewarmer := &ConnectionPreWarmer{
			dbManager:        nil,
			messagingManager: nil,
		}

		assert.False(t, prewarmer.IsAvailable())
	})
}

func TestLogAvailability(t *testing.T) {
	t.Run("logs availability with both managers", func(_ *testing.T) {
		log := logger.New("debug", true)
		prewarmer := &ConnectionPreWarmer{
			logger:           log,
			dbManager:        &database.DbManager{},
			messagingManager: &messaging.Manager{},
		}

		// This test primarily ensures the function runs without panic
		prewarmer.LogAvailability()
	})

	t.Run("logs availability with no managers", func(_ *testing.T) {
		log := logger.New("debug", true)
		prewarmer := &ConnectionPreWarmer{
			logger:           log,
			dbManager:        nil,
			messagingManager: nil,
		}

		// This test primarily ensures the function runs without panic
		prewarmer.LogAvailability()
	})

	t.Run("logs availability with only db manager", func(_ *testing.T) {
		log := logger.New("debug", true)
		prewarmer := &ConnectionPreWarmer{
			logger:           log,
			dbManager:        &database.DbManager{},
			messagingManager: nil,
		}

		// This test primarily ensures the function runs without panic
		prewarmer.LogAvailability()
	})
}
