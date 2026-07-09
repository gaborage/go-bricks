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
// defaultPublisherIdleTTL (single-tenant) has a third copy: messaging.NewMessagingManager's
// fallback (messaging/manager.go) for bare callers that bypass this builder — that
// fallback is single-tenant-only (see its comment), since a bare caller supplies no
// deployment-mode signal.
const (
	defaultPublisherMaxCached = 50
	defaultPublisherIdleTTL   = 1 * time.Hour // Single-tenant default; see config/validation.go
	// defaultPublisherIdleTTLMultiTenant mirrors config/validation.go's
	// defaultPublisherIdleTTLMultiTenant. Kept as a separate copy (not imported) for the
	// same reason as defaultPublisherIdleTTL above: this builder must honor the documented
	// default even when constructed directly, bypassing config validation.
	defaultPublisherIdleTTLMultiTenant = 10 * time.Minute
	defaultCacheMaxSize                = 100
	defaultCacheIdleTTL                = 15 * time.Minute
	defaultCacheCleanupInterval        = 5 * time.Minute
	defaultDatabaseMaxSize             = 10
	defaultDatabaseIdleTTL             = 1 * time.Hour    // mirrors config/validation.go
	defaultDatabaseIdleTTLMultiTenant  = 30 * time.Minute // mirrors config/validation.go
)

// ManagerConfigBuilder creates configuration options for database and messaging managers
// based on deployment mode (single-tenant vs multi-tenant).
type ManagerConfigBuilder struct {
	multiTenantEnabled bool
	tenantLimit        int
	// staticTenantCount is the number of tenants statically configured under
	// multitenant.tenants (0 for single-tenant or dynamic tenant sources). It is
	// used only to emit a startup WARN when a resource pool's MaxSize is below the
	// known tenant count, signaling per-request eviction thrash. Set by bootstrap.
	staticTenantCount int
	// connectionTimeout is the per-publish AMQP broker confirmation timeout,
	// sourced from messaging.reconnect.connectiontimeout and set by bootstrap.
	connectionTimeout time.Duration
	// maxPublishAttempts bounds the per-publish retry loop, sourced from
	// messaging.reconnect.maxpublishattempts and set by bootstrap.
	maxPublishAttempts int
	// readyTimeout bounds the pre-flight readiness wait, sourced from
	// messaging.reconnect.readytimeout and set by bootstrap.
	readyTimeout time.Duration
	// publisherConfig carries operator-configurable messaging publisher pool
	// settings (messaging.publisher.*), sourced from validated config by bootstrap.
	// When unset, documented defaults are applied as fallbacks.
	publisherConfig config.PublisherPoolConfig
	// cacheConfig carries operator-configurable cache manager settings
	// (cache.manager.*), sourced from validated config by bootstrap.
	// When unset, documented defaults are applied as fallbacks.
	cacheConfig config.CacheManagerConfig
	// dbConfig holds database.manager.* settings, set by bootstrap from validated config.
	dbConfig config.DatabaseManagerConfig
}

// NewManagerConfigBuilder creates a new manager configuration builder.
func NewManagerConfigBuilder(multiTenantEnabled bool, tenantLimit int) *ManagerConfigBuilder {
	return &ManagerConfigBuilder{
		multiTenantEnabled: multiTenantEnabled,
		tenantLimit:        tenantLimit,
	}
}

// resolveMaxSize treats non-positive as unset (not just zero) so a negative from a
// Validate-bypassing path (app.NewWithConfig) can't skip the mode-aware fallback and
// reach the managers' own <=0->default coercion (see database/manager.go, messaging/manager.go).
func (b *ManagerConfigBuilder) resolveMaxSize(operatorValue, singleTenantDefault int) int {
	if operatorValue > 0 {
		return operatorValue
	}
	if b.multiTenantEnabled {
		return b.tenantLimit
	}
	return singleTenantDefault
}

// resolveIdleTTL mirrors resolveMaxSize: the fallback is inert once config.Validate stamps defaults, but load-bearing when Validate is bypassed (NewWithConfig, unit tests).
func (b *ManagerConfigBuilder) resolveIdleTTL(operatorValue, multiTenantDefault, singleTenantDefault time.Duration) time.Duration {
	if operatorValue > 0 {
		return operatorValue
	}
	if b.multiTenantEnabled {
		return multiTenantDefault
	}
	return singleTenantDefault
}

// BuildDatabaseOptions creates database manager options based on deployment mode.
// Multi-tenant mode uses tenant limits and shorter TTL for dynamic scaling.
// Single-tenant mode uses smaller fixed limits and longer TTL for stability.
func (b *ManagerConfigBuilder) BuildDatabaseOptions() database.DbManagerOptions {
	return database.DbManagerOptions{
		MaxSize: b.resolveMaxSize(b.dbConfig.MaxSize, defaultDatabaseMaxSize),
		IdleTTL: b.resolveIdleTTL(b.dbConfig.IdleTTL, defaultDatabaseIdleTTLMultiTenant, defaultDatabaseIdleTTL),
	}
}

// BuildMessagingOptions creates messaging manager options based on deployment mode.
// Multi-tenant mode uses tenant limits and shorter TTL for dynamic scaling.
// Single-tenant mode uses smaller fixed limits and a longer TTL (same 1h as the DB pool).
func (b *ManagerConfigBuilder) BuildMessagingOptions() messaging.ManagerOptions {
	return messaging.ManagerOptions{
		MaxPublishers:      b.resolveMaxSize(b.publisherConfig.MaxCached, defaultPublisherMaxCached),
		IdleTTL:            b.resolveIdleTTL(b.publisherConfig.IdleTTL, defaultPublisherIdleTTLMultiTenant, defaultPublisherIdleTTL),
		ConnectionTimeout:  b.connectionTimeout,
		MaxPublishAttempts: b.maxPublishAttempts,
		ReadyTimeout:       b.readyTimeout,
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

// StaticTenantCount returns the number of statically-configured tenants
// (multitenant.tenants). It is 0 for single-tenant or dynamic tenant sources.
func (b *ManagerConfigBuilder) StaticTenantCount() int {
	return b.staticTenantCount
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

	f.warnIfPoolBelowTenantCount("database", dbOptions.MaxSize)

	return database.NewDbManager(resourceSource, f.logger, dbOptions, dbConnector)
}

// warnIfPoolBelowTenantCount emits a startup WARN when a per-tenant resource pool's
// MaxSize is below the number of statically-configured tenants. With fewer cached
// handles than tenants, the LRU manager evicts and recreates a connection on every
// request that targets a not-currently-cached tenant — head-of-line thrash that
// silently degrades latency. This is advisory (non-fatal) to stay non-breaking: an
// operator may intentionally under-provision, and dynamic tenant sources have no
// static count (staticTenantCount == 0), in which case the check is skipped.
func (f *ResourceManagerFactory) warnIfPoolBelowTenantCount(resource string, maxSize int) {
	tenantCount := f.configBuilder.StaticTenantCount()
	if !poolBelowTenantCount(maxSize, tenantCount) {
		return
	}

	f.logger.Warn().
		Str("resource", resource).
		Int("pool_max_size", maxSize).
		Int("configured_tenants", tenantCount).
		Msg("Resource pool max size is below the number of configured tenants; " +
			"the LRU manager will evict and recreate handles on requests for uncached tenants " +
			"(eviction thrash). Raise the pool size for this resource " +
			"(cache.manager.maxsize, messaging.publisher.maxcached, or for the database: " +
			"database.manager.maxsize when set, otherwise multitenant.limits.tenants) " +
			"to at least the tenant count.")
}

// poolBelowTenantCount reports whether a per-tenant pool of the given maxSize is
// too small to hold every statically-configured tenant simultaneously. It returns
// false (no warning) when there is no static tenant count (0, e.g. dynamic sources
// or single-tenant) or when maxSize is non-positive (unbounded / default sentinel),
// so the advisory only fires on a genuine under-provisioning.
func poolBelowTenantCount(maxSize, tenantCount int) bool {
	if tenantCount <= 0 || maxSize <= 0 {
		return false
	}
	return maxSize < tenantCount
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
	clientFactory := f.factoryResolver.MessagingClientFactoryWithOptions(MessagingClientFactoryOptions{
		ConnectionTimeout:  msgOptions.ConnectionTimeout,
		MaxPublishAttempts: msgOptions.MaxPublishAttempts,
		ReadyTimeout:       msgOptions.ReadyTimeout,
	})

	f.warnIfPoolBelowTenantCount("messaging", msgOptions.MaxPublishers)

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

	f.warnIfPoolBelowTenantCount("cache", cacheOptions.MaxSize)

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
