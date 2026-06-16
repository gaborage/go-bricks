package app

import (
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// Documented operator-configurable defaults. These mirror the values applied by
// config validation (see config/validation.go: applyMessagingDefaults and
// applyCacheManagerDefaults) so the builder honors the same documented behavior
// even when invoked without a fully-validated config (e.g. in unit tests).
const (
	defaultPublisherMaxCached   = 50
	defaultPublisherIdleTTL     = 10 * time.Minute
	defaultCacheMaxSize         = 100
	defaultCacheIdleTTL         = 15 * time.Minute
	defaultCacheCleanupInterval = 5 * time.Minute
)

// ManagerConfigBuilder creates configuration options for database and messaging managers
// based on deployment mode (single-tenant vs multi-tenant).
type ManagerConfigBuilder struct {
	multiTenantEnabled bool
	tenantLimit        int
	// connectionTimeout is the per-publish AMQP broker confirmation timeout,
	// sourced from messaging.reconnect.connectiontimeout and set by bootstrap.
	connectionTimeout time.Duration
	// publisherConfig carries operator-configurable messaging publisher pool
	// settings (messaging.publisher.*), sourced from validated config by bootstrap.
	// When unset, documented defaults are applied as fallbacks.
	publisherConfig config.PublisherPoolConfig
	// cacheConfig carries operator-configurable cache manager settings
	// (cache.manager.*), sourced from validated config by bootstrap.
	// When unset, documented defaults are applied as fallbacks.
	cacheConfig config.CacheManagerConfig
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
	// Operator config (messaging.publisher.*) is the source of truth. Mode-specific
	// values are only fallbacks when the operator left the key unset (zero).
	maxPublishers := b.publisherConfig.MaxCached
	if maxPublishers == 0 {
		if b.multiTenantEnabled {
			maxPublishers = b.tenantLimit // Scale publisher pool with tenant limit
		} else {
			maxPublishers = defaultPublisherMaxCached // Documented single-tenant default
		}
	}

	idleTTL := b.publisherConfig.IdleTTL
	if idleTTL == 0 {
		if b.multiTenantEnabled {
			idleTTL = 5 * time.Minute // Shorter TTL for multi-tenant churn
		} else {
			idleTTL = defaultPublisherIdleTTL // Documented single-tenant default
		}
	}

	return messaging.ManagerOptions{
		MaxPublishers:     maxPublishers,
		IdleTTL:           idleTTL,
		ConnectionTimeout: b.connectionTimeout,
	}
}

// BuildCacheOptions creates cache manager options based on deployment mode.
// Multi-tenant mode uses tenant limits and shorter TTL for dynamic scaling.
// Single-tenant mode uses smaller fixed limits and longer TTL.
func (b *ManagerConfigBuilder) BuildCacheOptions() cache.ManagerConfig {
	// Operator config (cache.manager.*) is the source of truth. Mode-specific
	// values are only fallbacks when the operator left the key unset (zero).
	maxSize := b.cacheConfig.MaxSize
	if maxSize == 0 {
		if b.multiTenantEnabled {
			maxSize = b.tenantLimit // Scale cache instances with tenant limit
		} else {
			maxSize = defaultCacheMaxSize // Documented single-tenant default
		}
	}

	idleTTL := b.cacheConfig.IdleTTL
	if idleTTL == 0 {
		idleTTL = defaultCacheIdleTTL // Documented default (same for both modes)
	}

	cleanupInterval := b.cacheConfig.CleanupInterval
	if cleanupInterval == 0 {
		cleanupInterval = defaultCacheCleanupInterval // Documented default (same for both modes)
	}

	return cache.ManagerConfig{
		MaxSize:         maxSize,
		IdleTTL:         idleTTL,
		CleanupInterval: cleanupInterval,
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

	msgOptions := f.configBuilder.BuildMessagingOptions()
	clientFactory := f.factoryResolver.MessagingClientFactory(msgOptions.ConnectionTimeout)

	return messaging.NewMessagingManager(resourceSource, f.logger, msgOptions, clientFactory)
}

// CreateCacheManager creates a cache manager using the resolved factory
// and appropriate configuration options for the deployment mode.
func (f *ResourceManagerFactory) CreateCacheManager(
	resourceSource TenantStore,
) *cache.CacheManager {
	if f.configBuilder.IsMultiTenant() {
		f.logger.Info().
			Int("tenant_limit", f.configBuilder.TenantLimit()).
			Msg("Creating cache manager for multi-tenant mode")
	} else {
		f.logger.Info().Msg("Creating cache manager for single-tenant mode")
	}

	cacheConnector := f.factoryResolver.CacheConnector(resourceSource, f.logger)
	cacheOptions := f.configBuilder.BuildCacheOptions()

	manager, err := cache.NewCacheManager(cacheOptions, cacheConnector)
	if err != nil {
		f.logger.Warn().Err(err).Msg("Failed to create cache manager, cache will be disabled")
		return nil
	}

	return manager
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
