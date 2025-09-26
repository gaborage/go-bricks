package app

import (
	"time"

	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/messaging"
)

// ManagerConfigBuilder creates configuration options for database and messaging managers
// based on deployment mode (single-tenant vs multi-tenant).
type ManagerConfigBuilder struct {
	multiTenantEnabled bool
	tenantLimit        int
}

// NewManagerConfigBuilder creates a new manager configuration builder.
func NewManagerConfigBuilder(multiTenantEnabled bool, tenantLimit int) *ManagerConfigBuilder {
	return &ManagerConfigBuilder{
		multiTenantEnabled: multiTenantEnabled,
		tenantLimit:        tenantLimit,
	}
}

// BuildDatabaseOptions creates database manager options based on deployment mode.
// Multi-tenant mode uses tenant limits and shorter TTL for dynamic scaling.
// Single-tenant mode uses smaller fixed limits and longer TTL for stability.
func (b *ManagerConfigBuilder) BuildDatabaseOptions() database.DbManagerOptions {
	if b.multiTenantEnabled {
		return database.DbManagerOptions{
			MaxSize: b.tenantLimit,    // Use configured tenant limit
			IdleTTL: 30 * time.Minute, // Shorter TTL for multi-tenant
		}
	}

	return database.DbManagerOptions{
		MaxSize: 10,            // Small fixed size for single-tenant
		IdleTTL: 1 * time.Hour, // Longer TTL for single-tenant
	}
}

// BuildMessagingOptions creates messaging manager options based on deployment mode.
// Multi-tenant mode uses tenant limits and shorter TTL for dynamic scaling.
// Single-tenant mode uses smaller fixed limits and moderate TTL.
func (b *ManagerConfigBuilder) BuildMessagingOptions() messaging.ManagerOptions {
	if b.multiTenantEnabled {
		return messaging.ManagerOptions{
			MaxPublishers: b.tenantLimit,   // Use configured tenant limit
			IdleTTL:       5 * time.Minute, // Shorter TTL for multi-tenant
		}
	}

	return messaging.ManagerOptions{
		MaxPublishers: 10,               // Small fixed size for single-tenant
		IdleTTL:       30 * time.Minute, // Moderate TTL for single-tenant
	}
}

// IsMultiTenant returns true if the builder is configured for multi-tenant mode.
func (b *ManagerConfigBuilder) IsMultiTenant() bool {
	return b.multiTenantEnabled
}

// TenantLimit returns the configured tenant limit for multi-tenant mode.
func (b *ManagerConfigBuilder) TenantLimit() int {
	return b.tenantLimit
}
