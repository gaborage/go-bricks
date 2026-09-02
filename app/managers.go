package app

import (
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
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
	// tenantStamps mirrors messaging.ManagerOptions.TenantStamps: consumers read the
	// tenant stamp only under multitenant.enabled + messaging.tenancy: shared. Set by
	// bootstrap.
	tenantStamps bool
	// connectionTimeout is the per-publish AMQP broker confirmation timeout,
	// sourced from messaging.reconnect.connectiontimeout and set by bootstrap.
	connectionTimeout time.Duration
	// maxPublishAttempts bounds the per-publish retry loop, sourced from
	// messaging.reconnect.maxpublishattempts and set by bootstrap.
	maxPublishAttempts int
	// readyTimeout bounds the pre-flight readiness wait, sourced from
	// messaging.reconnect.readytimeout and set by bootstrap.
	readyTimeout time.Duration
	// Reconnect delays, sourced from messaging.reconnect.{delay,maxdelay,reinitdelay,resenddelay}
	// and set by bootstrap.
	reconnectDelay    time.Duration
	reconnectMaxDelay time.Duration
	reInitDelay       time.Duration
	resendDelay       time.Duration
	// publisherConfig carries operator-configurable messaging publisher pool
	// settings (messaging.publisher.*), sourced from validated config by bootstrap.
	publisherConfig config.PublisherPoolConfig
	// cacheConfig carries operator-configurable cache manager settings
	// (cache.manager.*), sourced from validated config by bootstrap.
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

// resolveMaxSize returns the operator's validated value, or — multi-tenant
// only — scales a deliberately-preserved zero to the tenant limit (#661).
// Single-tenant zeros cannot reach here: config.Validate stamps the default.
func (b *ManagerConfigBuilder) resolveMaxSize(operatorValue int) int {
	if operatorValue > 0 {
		return operatorValue
	}
	return b.tenantLimit
}

// BuildDatabaseOptions creates database manager options from validated config.
func (b *ManagerConfigBuilder) BuildDatabaseOptions() database.DbManagerOptions {
	return database.DbManagerOptions{
		MaxSize:         b.resolveMaxSize(b.dbConfig.MaxSize),
		IdleTTL:         b.dbConfig.IdleTTL,
		CleanupInterval: b.dbConfig.CleanupInterval,
	}
}

// BuildMessagingOptions creates messaging manager options from validated config.
func (b *ManagerConfigBuilder) BuildMessagingOptions() messaging.ManagerOptions {
	return messaging.ManagerOptions{
		MaxPublishers:      b.resolveMaxSize(b.publisherConfig.MaxCached),
		IdleTTL:            b.publisherConfig.IdleTTL,
		CleanupInterval:    b.publisherConfig.CleanupInterval,
		ConnectionTimeout:  b.connectionTimeout,
		MaxPublishAttempts: b.maxPublishAttempts,
		ReadyTimeout:       b.readyTimeout,
		ReconnectDelay:     b.reconnectDelay,
		ReconnectMaxDelay:  b.reconnectMaxDelay,
		ReinitDelay:        b.reInitDelay,
		ResendDelay:        b.resendDelay,
		TenantStamps:       b.tenantStamps,
	}
}

// BuildCacheOptions creates cache manager options from validated config.
func (b *ManagerConfigBuilder) BuildCacheOptions() cache.ManagerConfig {
	// Operator config (cache.manager.*) is the source of truth. Multi-tenant zero is
	// deliberately preserved by config.Validate (#661), so it scales to the tenant
	// limit here. Not resolveMaxSize: cache.NewCacheManager rejects negatives (unlike
	// the db/messaging managers, which coerce), so a negative must pass through and
	// fail loudly there instead of being silently swallowed into a live pool.
	maxSize := b.cacheConfig.MaxSize
	if maxSize == 0 && b.multiTenantEnabled {
		maxSize = b.tenantLimit
	}

	return cache.ManagerConfig{
		MaxSize:         maxSize,
		IdleTTL:         b.cacheConfig.IdleTTL,
		CleanupInterval: b.cacheConfig.CleanupInterval,
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
		ReconnectDelay:     msgOptions.ReconnectDelay,
		ReconnectMaxDelay:  msgOptions.ReconnectMaxDelay,
		ReinitDelay:        msgOptions.ReinitDelay,
		ResendDelay:        msgOptions.ResendDelay,
	})

	f.warnIfPoolBelowTenantCount("messaging", msgOptions.MaxPublishers)

	return messaging.NewMessagingManager(resourceSource, f.logger, msgOptions, clientFactory)
}

// CreateCacheManager creates a cache manager using the resolved factory
// and appropriate configuration options for the deployment mode.
//
// It fails closed: a nil manager registers no cache readiness probe, so /ready reports
// the cache "disabled" and answers 200 — a service that asked for a cache, got none, and
// joined the rotation anyway. Returning the error instead of logging it still matters after
// WithConfig's config.Validate call (ADR-064): normalizeCache only fills Manager.MaxSize/IdleTTL/CleanupInterval
// when cache.enabled is true, so a negative value on a disabled cache reaches here unvalidated.
func (f *ResourceManagerFactory) CreateCacheManager(
	resourceSource TenantStore,
) (*cache.CacheManager, error) {
	if f.configBuilder.IsMultiTenant() {
		f.logger.Info().
			Int("tenant_limit", f.configBuilder.TenantLimit()).
			Msg("Creating cache manager for multi-tenant mode")
	} else {
		f.logger.Info().Msg("Creating cache manager for single-tenant mode")
	}

	cacheConnector := f.factoryResolver.CacheConnector(resourceSource, f.logger)
	cacheOptions := f.configBuilder.BuildCacheOptions()
	cacheOptions.Logger = f.logger

	f.warnIfPoolBelowTenantCount("cache", cacheOptions.MaxSize)

	manager, err := cache.NewCacheManager(cacheOptions, cacheConnector)
	if err != nil {
		// Report the resolved values, not just the key names: in multi-tenant mode an unset
		// cache.manager.maxsize takes multitenant.limits.tenants, so naming only cache.manager.*
		// would point at a key the operator never set.
		return nil, fmt.Errorf(
			"create cache manager with maxsize=%d idlettl=%v (from cache.manager.*, or multitenant.limits.tenants where maxsize is unset in multi-tenant mode): %w",
			cacheOptions.MaxSize, cacheOptions.IdleTTL, err)
	}

	return manager, nil
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
