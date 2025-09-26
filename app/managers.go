package app

import (
	"time"

	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
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

// ResourceManagerFactory creates database and messaging managers using
// resolved factories and configuration options.
type ResourceManagerFactory struct {
	factoryResolver *FactoryResolver
	configBuilder   *ManagerConfigBuilder
	logger          logger.Logger
}

// NewResourceManagerFactory creates a new resource manager factory.
func NewResourceManagerFactory(
	factoryResolver *FactoryResolver,
	configBuilder *ManagerConfigBuilder,
	log logger.Logger,
) *ResourceManagerFactory {
	return &ResourceManagerFactory{
		factoryResolver: factoryResolver,
		configBuilder:   configBuilder,
		logger:          log,
	}
}

// CreateDatabaseManager creates a database manager using the resolved factory
// and appropriate configuration options for the deployment mode.
func (f *ResourceManagerFactory) CreateDatabaseManager(
	resourceSource TenantStore,
) *database.DbManager {
	if f.configBuilder.IsMultiTenant() {
		f.logger.Info().
			Int("tenant_limit", f.configBuilder.TenantLimit()).
			Msg("Creating database manager for multi-tenant mode")
	} else {
		f.logger.Info().Msg("Creating database manager for single-tenant mode")
	}

	dbConnector := f.factoryResolver.DatabaseConnector()
	dbOptions := f.configBuilder.BuildDatabaseOptions()

	return database.NewDbManager(resourceSource, f.logger, dbOptions, dbConnector)
}

// CreateMessagingManager creates a messaging manager using the resolved factory
// and appropriate configuration options for the deployment mode.
func (f *ResourceManagerFactory) CreateMessagingManager(
	resourceSource TenantStore,
) *messaging.Manager {
	if f.configBuilder.IsMultiTenant() {
		f.logger.Info().
			Int("tenant_limit", f.configBuilder.TenantLimit()).
			Msg("Creating messaging manager for multi-tenant mode")
	} else {
		f.logger.Info().Msg("Creating messaging manager for single-tenant mode")
	}

	clientFactory := f.factoryResolver.MessagingClientFactory()
	msgOptions := f.configBuilder.BuildMessagingOptions()

	return messaging.NewMessagingManager(resourceSource, f.logger, msgOptions, clientFactory)
}

// LogFactoryInfo logs information about which factories are being used.
// This is useful for debugging and operational visibility.
func (f *ResourceManagerFactory) LogFactoryInfo() {
	if f.factoryResolver.HasCustomFactories() {
		f.logger.Info().Msg("Using custom factory implementations from options")
	} else {
		f.logger.Debug().Msg("Using default factory implementations")
	}
}
