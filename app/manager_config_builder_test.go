package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewManagerConfigBuilder(t *testing.T) {
	t.Run("single-tenant configuration", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)

		assert.NotNil(t, builder)
		assert.False(t, builder.multiTenantEnabled)
		assert.Equal(t, 100, builder.tenantLimit)
	})

	t.Run("multi-tenant configuration", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 500)

		assert.NotNil(t, builder)
		assert.True(t, builder.multiTenantEnabled)
		assert.Equal(t, 500, builder.tenantLimit)
	})

	t.Run("zero tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 0)

		assert.NotNil(t, builder)
		assert.True(t, builder.multiTenantEnabled)
		assert.Equal(t, 0, builder.tenantLimit)
	})

	t.Run("negative tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, -10)

		assert.NotNil(t, builder)
		assert.True(t, builder.multiTenantEnabled)
		assert.Equal(t, -10, builder.tenantLimit)
	})
}

func TestManagerConfigBuilderBuildDatabaseOptions(t *testing.T) {
	t.Run("single-tenant database options", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, 10, options.MaxSize)
		assert.Equal(t, 1*time.Hour, options.IdleTTL)
	})

	t.Run("multi-tenant database options", func(t *testing.T) {
		tenantLimit := 250
		builder := NewManagerConfigBuilder(true, tenantLimit)

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, tenantLimit, options.MaxSize)
		assert.Equal(t, 30*time.Minute, options.IdleTTL)
	})

	t.Run("multi-tenant with zero tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 0)

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, 0, options.MaxSize)
		assert.Equal(t, 30*time.Minute, options.IdleTTL)
	})

	t.Run("multi-tenant with negative tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, -5)

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, -5, options.MaxSize)
		assert.Equal(t, 30*time.Minute, options.IdleTTL)
	})

	t.Run("large tenant limit", func(t *testing.T) {
		largeLimit := 10000
		builder := NewManagerConfigBuilder(true, largeLimit)

		options := builder.BuildDatabaseOptions()

		assert.Equal(t, largeLimit, options.MaxSize)
		assert.Equal(t, 30*time.Minute, options.IdleTTL)
	})
}

func TestManagerConfigBuilderBuildMessagingOptions(t *testing.T) {
	t.Run("single-tenant messaging options", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 100)

		options := builder.BuildMessagingOptions()

		assert.Equal(t, 10, options.MaxPublishers)
		assert.Equal(t, 30*time.Minute, options.IdleTTL)
	})

	t.Run("multi-tenant messaging options", func(t *testing.T) {
		tenantLimit := 300
		builder := NewManagerConfigBuilder(true, tenantLimit)

		options := builder.BuildMessagingOptions()

		assert.Equal(t, tenantLimit, options.MaxPublishers)
		assert.Equal(t, 5*time.Minute, options.IdleTTL)
	})

	t.Run("multi-tenant with zero tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 0)

		options := builder.BuildMessagingOptions()

		assert.Equal(t, 0, options.MaxPublishers)
		assert.Equal(t, 5*time.Minute, options.IdleTTL)
	})

	t.Run("multi-tenant with negative tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, -3)

		options := builder.BuildMessagingOptions()

		assert.Equal(t, -3, options.MaxPublishers)
		assert.Equal(t, 5*time.Minute, options.IdleTTL)
	})

	t.Run("large tenant limit", func(t *testing.T) {
		largeLimit := 5000
		builder := NewManagerConfigBuilder(true, largeLimit)

		options := builder.BuildMessagingOptions()

		assert.Equal(t, largeLimit, options.MaxPublishers)
		assert.Equal(t, 5*time.Minute, options.IdleTTL)
	})
}

func TestManagerConfigBuilderIsMultiTenant(t *testing.T) {
	t.Run("single-tenant returns false", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 50)

		assert.False(t, builder.IsMultiTenant())
	})

	t.Run("multi-tenant returns true", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 50)

		assert.True(t, builder.IsMultiTenant())
	})
}

func TestManagerConfigBuilderTenantLimit(t *testing.T) {
	t.Run("returns configured tenant limit", func(t *testing.T) {
		limit := 150
		builder := NewManagerConfigBuilder(true, limit)

		assert.Equal(t, limit, builder.TenantLimit())
	})

	t.Run("returns zero tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 0)

		assert.Equal(t, 0, builder.TenantLimit())
	})

	t.Run("returns negative tenant limit", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, -20)

		assert.Equal(t, -20, builder.TenantLimit())
	})
}

func TestManagerConfigBuilderConsistency(t *testing.T) {
	t.Run("single-tenant configuration consistency", func(t *testing.T) {
		builder := NewManagerConfigBuilder(false, 999) // Tenant limit should be ignored

		dbOptions := builder.BuildDatabaseOptions()
		msgOptions := builder.BuildMessagingOptions()

		// Single-tenant always uses fixed values, regardless of tenantLimit
		assert.Equal(t, 10, dbOptions.MaxSize)
		assert.Equal(t, 10, msgOptions.MaxPublishers)

		// Database has longer TTL than messaging in single-tenant mode
		assert.True(t, dbOptions.IdleTTL > msgOptions.IdleTTL)
	})

	t.Run("multi-tenant configuration consistency", func(t *testing.T) {
		tenantLimit := 200
		builder := NewManagerConfigBuilder(true, tenantLimit)

		dbOptions := builder.BuildDatabaseOptions()
		msgOptions := builder.BuildMessagingOptions()

		// Multi-tenant uses tenant limit for both
		assert.Equal(t, tenantLimit, dbOptions.MaxSize)
		assert.Equal(t, tenantLimit, msgOptions.MaxPublishers)

		// Database has longer TTL than messaging in multi-tenant mode
		assert.True(t, dbOptions.IdleTTL > msgOptions.IdleTTL)
	})

	t.Run("builder instance immutability", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 100)

		// Multiple calls should return consistent results
		dbOptions1 := builder.BuildDatabaseOptions()
		dbOptions2 := builder.BuildDatabaseOptions()
		msgOptions1 := builder.BuildMessagingOptions()
		msgOptions2 := builder.BuildMessagingOptions()

		assert.Equal(t, dbOptions1, dbOptions2)
		assert.Equal(t, msgOptions1, msgOptions2)

		// Getters should return same values
		assert.Equal(t, builder.IsMultiTenant(), builder.IsMultiTenant())
		assert.Equal(t, builder.TenantLimit(), builder.TenantLimit())
	})
}

func TestManagerConfigBuilderEdgeCases(t *testing.T) {
	t.Run("configuration differences between modes", func(t *testing.T) {
		singleTenant := NewManagerConfigBuilder(false, 100)
		multiTenant := NewManagerConfigBuilder(true, 100)

		singleDbOpts := singleTenant.BuildDatabaseOptions()
		multiDbOpts := multiTenant.BuildDatabaseOptions()
		singleMsgOpts := singleTenant.BuildMessagingOptions()
		multiMsgOpts := multiTenant.BuildMessagingOptions()

		// Verify different behaviors between modes
		assert.NotEqual(t, singleDbOpts.MaxSize, multiDbOpts.MaxSize)
		assert.NotEqual(t, singleDbOpts.IdleTTL, multiDbOpts.IdleTTL)
		assert.NotEqual(t, singleMsgOpts.MaxPublishers, multiMsgOpts.MaxPublishers)
		assert.NotEqual(t, singleMsgOpts.IdleTTL, multiMsgOpts.IdleTTL)
	})

	t.Run("expected TTL relationships", func(t *testing.T) {
		builder := NewManagerConfigBuilder(true, 100)

		dbOptions := builder.BuildDatabaseOptions()
		msgOptions := builder.BuildMessagingOptions()

		// Multi-tenant: DB (30min) > Messaging (5min)
		assert.Equal(t, 30*time.Minute, dbOptions.IdleTTL)
		assert.Equal(t, 5*time.Minute, msgOptions.IdleTTL)
		assert.True(t, dbOptions.IdleTTL > msgOptions.IdleTTL)
	})

	t.Run("single-tenant ignores tenant limit parameter", func(t *testing.T) {
		builder1 := NewManagerConfigBuilder(false, 1)
		builder2 := NewManagerConfigBuilder(false, 10000)

		// Both should produce identical options since single-tenant ignores tenantLimit
		dbOpts1 := builder1.BuildDatabaseOptions()
		dbOpts2 := builder2.BuildDatabaseOptions()
		msgOpts1 := builder1.BuildMessagingOptions()
		msgOpts2 := builder2.BuildMessagingOptions()

		assert.Equal(t, dbOpts1, dbOpts2)
		assert.Equal(t, msgOpts1, msgOpts2)

		// But tenantLimit getter should still return the configured value
		assert.NotEqual(t, builder1.TenantLimit(), builder2.TenantLimit())
	})
}
