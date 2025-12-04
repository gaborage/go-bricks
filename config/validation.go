package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Database pool defaults
const (
	defaultSlowQueryThreshold  = 200 * time.Millisecond
	defaultMaxQueryLength      = 1000
	defaultKeepAliveEnabled    = true
	defaultKeepAliveInterval   = 60 * time.Second
	defaultPoolIdleTime        = 5 * time.Minute  // Close idle connections before NAT/firewall timeout
	defaultPoolLifetimeMax     = 30 * time.Minute // Force periodic connection recycling
	defaultPoolIdleConnections = int32(2)         // Maintain minimal warm connections
)

// Messaging reconnection defaults
const (
	defaultReconnectDelay    = 5 * time.Second  // Initial delay between reconnection attempts
	defaultReinitDelay       = 2 * time.Second  // Delay before channel reinitialization
	defaultResendDelay       = 5 * time.Second  // Delay before retrying failed publishes
	defaultConnectionTimeout = 30 * time.Second // Timeout for connection/confirmation
	defaultMaxReconnectDelay = 60 * time.Second // Maximum delay for exponential backoff cap
	defaultMaxPublishers     = 50               // Maximum publisher clients in cache
	defaultPublisherIdleTTL  = 10 * time.Minute // Time before idle publishers are evicted
)

// Cache manager defaults
const (
	defaultCacheMaxSize         = 100              // Maximum tenant cache instances
	defaultCacheIdleTTL         = 15 * time.Minute // Idle timeout per cache
	defaultCacheCleanupInterval = 5 * time.Minute  // Cleanup goroutine frequency
)

// Startup timeout defaults
const (
	defaultStartupTimeout              = 10 * time.Second // Overall startup timeout
	defaultStartupDatabaseTimeout      = 10 * time.Second // Database health check timeout
	defaultStartupMessagingTimeout     = 10 * time.Second // Broker connection timeout
	defaultStartupCacheTimeout         = 5 * time.Second  // Cache initialization timeout
	defaultStartupObservabilityTimeout = 15 * time.Second // OTLP provider initialization timeout
)

// Database type constants
const (
	PostgreSQL = "postgresql"
	Oracle     = "oracle"
	MongoDB    = "mongodb"
)

// Environment constants
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Validation error message constants
const (
	errMustBeNonNegative = "must be non-negative"
	errMustBePositive    = "must be positive"
	errNotSupportedFmt   = "'%s' is not supported"
	portRange            = "1-65535"
	fieldDatabasePort    = "database.port"
	errInvalidField      = "invalid value: %v"
)

func Validate(cfg *Config) error {
	if err := validateApp(&cfg.App); err != nil {
		return fmt.Errorf("app config: %w", err)
	}

	if err := validateServer(&cfg.Server); err != nil {
		return fmt.Errorf("server config: %w", err)
	}

	if err := validateMultitenant(&cfg.Multitenant, &cfg.Database, &cfg.Messaging, &cfg.Source); err != nil {
		return fmt.Errorf("multitenant config: %w", err)
	}

	if err := validateDatabase(&cfg.Database); err != nil {
		return fmt.Errorf("database config: %w", err)
	}

	if err := validateLog(&cfg.Log); err != nil {
		return fmt.Errorf("log config: %w", err)
	}

	if err := validateCache(&cfg.Cache); err != nil {
		return fmt.Errorf("cache config: %w", err)
	}

	if err := validateMessaging(&cfg.Messaging); err != nil {
		return fmt.Errorf("messaging config: %w", err)
	}

	return nil
}

// validateMessaging validates messaging configuration and applies defaults.
// Returns nil if messaging is not configured or if all settings are valid.
func validateMessaging(cfg *MessagingConfig) error {
	if !IsMessagingConfigured(cfg) {
		return nil
	}

	// Apply messaging defaults (reconnection, publisher pool)
	return applyMessagingDefaults(cfg)
}

// validateApp validates the application configuration in cfg.
// It requires Name and Version to be non-empty, Env to be one of
// EnvDevelopment, EnvStaging, or EnvProduction, and Rate.Limit to be non-negative.
// Returns an error describing the first failed validation, or nil if valid.
func validateApp(cfg *AppConfig) error {
	if cfg.Name == "" {
		return NewMissingFieldError("app.name", "APP_NAME", "app.name")
	}

	if cfg.Version == "" {
		return NewMissingFieldError("app.version", "APP_VERSION", "app.version")
	}

	validEnvs := []string{EnvDevelopment, EnvStaging, EnvProduction}
	if !slices.Contains(validEnvs, cfg.Env) {
		return NewInvalidFieldError("app.env", fmt.Sprintf("'%s' is not valid", cfg.Env), validEnvs)
	}

	if cfg.Rate.Limit < 0 {
		return NewValidationError("app.rate.limit", errMustBeNonNegative)
	}

	if cfg.Rate.Burst < 0 {
		return NewValidationError("app.rate.burst", errMustBeNonNegative)
	}

	// Apply startup timeout defaults
	if err := applyStartupDefaults(&cfg.Startup); err != nil {
		return err
	}

	return nil
}

func validateServer(cfg *ServerConfig) error {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return NewInvalidFieldError("server.port", fmt.Sprintf(errInvalidField, cfg.Port), []string{portRange})
	}

	if cfg.Timeout.Read <= 0 {
		return NewValidationError("server.timeout.read", errMustBePositive)
	}

	if cfg.Timeout.Write <= 0 {
		return NewValidationError("server.timeout.write", errMustBePositive)
	}

	if cfg.Timeout.Middleware <= 0 {
		return NewValidationError("server.timeout.middleware", errMustBePositive)
	}

	// Middleware timeout should be less than write timeout to allow graceful error responses
	// Otherwise the write timeout will trigger first, causing connection drops
	if cfg.Timeout.Middleware >= cfg.Timeout.Write {
		return &ConfigError{
			Category: "invalid",
			Field:    "server.timeout.middleware",
			Message:  fmt.Sprintf("must be less than server.timeout.write (%v)", cfg.Timeout.Write),
			Action:   "reduce server.timeout.middleware or increase server.timeout.write",
		}
	}

	if cfg.Timeout.Shutdown <= 0 {
		return NewValidationError("server.timeout.shutdown", errMustBePositive)
	}

	return nil
}

// IsDatabaseConfigured determines if database is intentionally configured.
// This mirrors the logic used in app.isDatabaseEnabled() for consistency.
func IsDatabaseConfigured(cfg *DatabaseConfig) bool {
	// Connection string indicates explicit database configuration
	if cfg.ConnectionString != "" {
		return true
	}

	// Mirror the logic from app.isDatabaseEnabled()
	return cfg.Host != "" || cfg.Type != ""
}

func validateDatabase(cfg *DatabaseConfig) error {
	if !IsDatabaseConfigured(cfg) {
		return nil
	}

	if cfg.ConnectionString != "" {
		return validateDatabaseWithConnectionString(cfg)
	}

	if err := validateDatabaseType(cfg.Type); err != nil {
		return err
	}

	if err := validateDatabaseCoreFields(cfg); err != nil {
		return err
	}

	if err := validateVendorSpecificFields(cfg); err != nil {
		return err
	}

	return applyDatabasePoolDefaults(cfg)
}

// validateDatabaseWithConnectionString validates database settings when a connection
// string is provided and applies defaults for query-related fields when zero.
// It checks (and returns an error for) an explicit database Type that is not allowed,
// an invalid optional Port, and negative values for Pool/Query fields.
// Pool.Max.Connections defaults to 25 when 0; Query.Log.MaxLength and Query.Slow.Threshold
// default to the respective constants when 0. Negative values are rejected.
// respectively. The cfg argument is mutated for those default assignments.
func validateDatabaseWithConnectionString(cfg *DatabaseConfig) error {
	if cfg.Type != "" {
		if err := validateDatabaseType(cfg.Type); err != nil {
			return err
		}
	}

	if err := validateOptionalDatabasePort(cfg.Port); err != nil {
		return err
	}

	if err := applyDatabasePoolDefaults(cfg); err != nil {
		return err
	}

	// Validate vendor-specific fields even with connection string
	if err := validateVendorSpecificFields(cfg); err != nil {
		return err
	}

	return nil
}

// validateDatabaseType validates that dbType is one of the supported database type
// constants (PostgreSQL, Oracle, or MongoDB). It returns nil when dbType is valid and an
// error describing the invalid value and the allowed types when it is not.
func validateDatabaseType(dbType string) error {
	validTypes := []string{PostgreSQL, Oracle, MongoDB}
	if !slices.Contains(validTypes, dbType) {
		return NewInvalidFieldError("database.type", fmt.Sprintf(errNotSupportedFmt, dbType), validTypes)
	}
	return nil
}

func validateDatabaseCoreFields(cfg *DatabaseConfig) error {
	if cfg.Host == "" {
		return NewMissingFieldError("database.host", "DATABASE_HOST", "database.host")
	}

	if err := validateRequiredDatabasePort(cfg.Port); err != nil {
		return err
	}

	// For Oracle, database name is optional if Service.Name or SID is provided
	// Oracle-specific validation will provide more detailed error messages
	if cfg.Type != Oracle && cfg.Database == "" {
		return NewMissingFieldError("database.database", "DATABASE_DATABASE", "database.database")
	}

	if cfg.Username == "" {
		return NewMissingFieldError("database.username", "DATABASE_USERNAME", "database.username")
	}

	return nil
}

func validateOptionalDatabasePort(port int) error {
	if port < 0 || port > 65535 {
		return NewInvalidFieldError(fieldDatabasePort, fmt.Sprintf(errInvalidField, port), []string{portRange})
	}
	return nil
}

func validateRequiredDatabasePort(port int) error {
	if port <= 0 {
		return NewMissingFieldError(fieldDatabasePort, "DATABASE_PORT", fieldDatabasePort)
	}
	if port > 65535 {
		return NewInvalidFieldError(fieldDatabasePort, "invalid port; must be between 1 and 65535", []string{portRange})
	}
	return nil
}

// applyDatabasePoolDefaults sets production-safe defaults and validates database pool/query settings.
//
// It modifies cfg in-place:
// - Pool.Max.Connections: if 0, sets to 25; if negative, returns an error.
// - Pool.Idle.Connections: if 0, sets to 2 (minimal warm connections); if negative, returns an error.
// - Pool.Idle.Time: if 0, sets to 5m (closes idle connections before NAT/firewall timeout); if negative, returns an error.
// - Pool.Lifetime.Max: if 0, sets to 30m (forces periodic connection recycling); if negative, returns an error.
// - Pool.KeepAlive.Enabled: if Interval is 0, sets to true (recommended for cloud).
// - Pool.KeepAlive.Interval: if 0, sets to 60s (below typical NAT timeouts).
// - Query.Log.MaxLength: if negative, returns an error; if 0, sets to defaultMaxQueryLength.
// - Query.Slow.Threshold: if negative, returns an error; if 0, sets to defaultSlowQueryThreshold.
//
// Returns an error when any value is invalid; otherwise returns nil.
func applyDatabasePoolDefaults(cfg *DatabaseConfig) error {
	if cfg.Pool.Max.Connections == 0 {
		cfg.Pool.Max.Connections = 25
	} else if cfg.Pool.Max.Connections < 0 {
		return NewValidationError("database.pool.max.connections", errMustBeNonNegative)
	}

	if cfg.Pool.Idle.Connections < 0 {
		return NewValidationError("database.pool.idle.connections", errMustBeNonNegative)
	}
	// Apply default idle connections if not configured
	if cfg.Pool.Idle.Connections == 0 {
		cfg.Pool.Idle.Connections = defaultPoolIdleConnections
	}

	// Apply default idle time - closes connections before NAT/firewall timeout
	if cfg.Pool.Idle.Time == 0 {
		cfg.Pool.Idle.Time = defaultPoolIdleTime
	} else if cfg.Pool.Idle.Time < 0 {
		return NewValidationError("database.pool.idle.time", errMustBeNonNegative)
	}

	// Apply default connection lifetime - forces periodic recycling
	if cfg.Pool.Lifetime.Max == 0 {
		cfg.Pool.Lifetime.Max = defaultPoolLifetimeMax
	} else if cfg.Pool.Lifetime.Max < 0 {
		return NewValidationError("database.pool.lifetime.max", errMustBeNonNegative)
	}

	// Apply keep-alive defaults for cloud deployments.
	// When Interval is zero (not configured), apply defaults for both Enabled and Interval.
	// This ensures omitted configs get production-safe settings.
	if cfg.Pool.KeepAlive.Interval == 0 {
		cfg.Pool.KeepAlive.Enabled = defaultKeepAliveEnabled
		cfg.Pool.KeepAlive.Interval = defaultKeepAliveInterval
	}

	if cfg.Query.Log.MaxLength < 0 {
		return NewValidationError("database.query.log.maxlength", errMustBeNonNegative)
	}
	if cfg.Query.Log.MaxLength == 0 {
		cfg.Query.Log.MaxLength = defaultMaxQueryLength
	}

	if cfg.Query.Slow.Threshold < 0 {
		return NewValidationError("database.query.slow.threshold", errMustBeNonNegative)
	}
	if cfg.Query.Slow.Threshold == 0 {
		cfg.Query.Slow.Threshold = defaultSlowQueryThreshold
	}

	return nil
}

// applyMessagingDefaults sets production-safe defaults for messaging configuration.
//
// It modifies cfg in-place:
// - Reconnect.Delay: if 0, sets to 5s; if negative, returns an error.
// - Reconnect.ReinitDelay: if 0, sets to 2s; if negative, returns an error.
// - Reconnect.ResendDelay: if 0, sets to 5s; if negative, returns an error.
// - Reconnect.ConnectionTimeout: if 0, sets to 30s; if negative, returns an error.
// - Reconnect.MaxDelay: if 0, sets to 60s; if negative, returns an error.
// - Publisher.MaxCached: if 0, sets to 50; if negative, returns an error.
// - Publisher.IdleTTL: if 0, sets to 10m; if negative, returns an error.
//
// Returns an error when any value is invalid; otherwise returns nil.
func applyMessagingDefaults(cfg *MessagingConfig) error {
	// Reconnect.Delay
	if cfg.Reconnect.Delay == 0 {
		cfg.Reconnect.Delay = defaultReconnectDelay
	} else if cfg.Reconnect.Delay < 0 {
		return NewValidationError("messaging.reconnect.delay", errMustBeNonNegative)
	}

	// Reconnect.ReinitDelay
	if cfg.Reconnect.ReinitDelay == 0 {
		cfg.Reconnect.ReinitDelay = defaultReinitDelay
	} else if cfg.Reconnect.ReinitDelay < 0 {
		return NewValidationError("messaging.reconnect.reinit_delay", errMustBeNonNegative)
	}

	// Reconnect.ResendDelay
	if cfg.Reconnect.ResendDelay == 0 {
		cfg.Reconnect.ResendDelay = defaultResendDelay
	} else if cfg.Reconnect.ResendDelay < 0 {
		return NewValidationError("messaging.reconnect.resend_delay", errMustBeNonNegative)
	}

	// Reconnect.ConnectionTimeout
	if cfg.Reconnect.ConnectionTimeout == 0 {
		cfg.Reconnect.ConnectionTimeout = defaultConnectionTimeout
	} else if cfg.Reconnect.ConnectionTimeout < 0 {
		return NewValidationError("messaging.reconnect.connection_timeout", errMustBeNonNegative)
	}

	// Reconnect.MaxDelay
	if cfg.Reconnect.MaxDelay == 0 {
		cfg.Reconnect.MaxDelay = defaultMaxReconnectDelay
	} else if cfg.Reconnect.MaxDelay < 0 {
		return NewValidationError("messaging.reconnect.max_delay", errMustBeNonNegative)
	}

	// Publisher.MaxCached
	if cfg.Publisher.MaxCached == 0 {
		cfg.Publisher.MaxCached = defaultMaxPublishers
	} else if cfg.Publisher.MaxCached < 0 {
		return NewValidationError("messaging.publisher.max_cached", errMustBeNonNegative)
	}

	// Publisher.IdleTTL
	if cfg.Publisher.IdleTTL == 0 {
		cfg.Publisher.IdleTTL = defaultPublisherIdleTTL
	} else if cfg.Publisher.IdleTTL < 0 {
		return NewValidationError("messaging.publisher.idle_ttl", errMustBeNonNegative)
	}

	return nil
}

// applyCacheManagerDefaults sets production-safe defaults for cache manager configuration.
//
// It modifies cfg in-place:
// - Manager.MaxSize: if 0, sets to 100; if negative, returns an error.
// - Manager.IdleTTL: if 0, sets to 15m; if negative, returns an error.
// - Manager.CleanupInterval: if 0, sets to 5m; if negative, returns an error.
//
// Returns an error when any value is invalid; otherwise returns nil.
func applyCacheManagerDefaults(cfg *CacheConfig) error {
	// Manager.MaxSize
	if cfg.Manager.MaxSize == 0 {
		cfg.Manager.MaxSize = defaultCacheMaxSize
	} else if cfg.Manager.MaxSize < 0 {
		return NewValidationError("cache.manager.max_size", errMustBeNonNegative)
	}

	// Manager.IdleTTL
	if cfg.Manager.IdleTTL == 0 {
		cfg.Manager.IdleTTL = defaultCacheIdleTTL
	} else if cfg.Manager.IdleTTL < 0 {
		return NewValidationError("cache.manager.idle_ttl", errMustBeNonNegative)
	}

	// Manager.CleanupInterval
	if cfg.Manager.CleanupInterval == 0 {
		cfg.Manager.CleanupInterval = defaultCacheCleanupInterval
	} else if cfg.Manager.CleanupInterval < 0 {
		return NewValidationError("cache.manager.cleanup_interval", errMustBeNonNegative)
	}

	return nil
}

// applyTimeoutDefault validates and applies default to a component timeout.
// Fallback hierarchy: explicit value > global fallback > per-component default.
// Returns an error if the value is negative.
func applyTimeoutDefault(
	value *time.Duration,
	fieldName string,
	globalWasSet bool,
	globalTimeout time.Duration,
	componentDefault time.Duration,
) error {
	if *value < 0 {
		return NewValidationError(fieldName, errMustBeNonNegative)
	}
	if *value == 0 {
		if globalWasSet {
			*value = globalTimeout
		} else {
			*value = componentDefault
		}
	}
	return nil
}

// applyStartupDefaults sets production-safe defaults for startup configuration.
//
// Fallback hierarchy for component timeouts:
//  1. Explicit component value (preserved if set)
//  2. Global Timeout (used when component is 0 and Timeout was explicitly set)
//  3. Per-component default (used when both component and original Timeout are 0)
//
// Default values:
// - Timeout: 10s, Database: 10s, Messaging: 10s, Cache: 5s, Observability: 15s
//
// Returns an error when any value is negative; otherwise returns nil.
func applyStartupDefaults(cfg *StartupConfig) error {
	// Capture whether global timeout was originally set (non-zero)
	globalWasSet := cfg.Timeout != 0

	// Validate and default the global timeout first
	if cfg.Timeout < 0 {
		return NewValidationError("app.startup.timeout", errMustBeNonNegative)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultStartupTimeout
	}

	// Apply defaults to each component using helper
	if err := applyTimeoutDefault(&cfg.Database, "app.startup.database",
		globalWasSet, cfg.Timeout, defaultStartupDatabaseTimeout); err != nil {
		return err
	}
	if err := applyTimeoutDefault(&cfg.Messaging, "app.startup.messaging",
		globalWasSet, cfg.Timeout, defaultStartupMessagingTimeout); err != nil {
		return err
	}
	if err := applyTimeoutDefault(&cfg.Cache, "app.startup.cache",
		globalWasSet, cfg.Timeout, defaultStartupCacheTimeout); err != nil {
		return err
	}
	if err := applyTimeoutDefault(&cfg.Observability, "app.startup.observability",
		globalWasSet, cfg.Timeout, defaultStartupObservabilityTimeout); err != nil {
		return err
	}

	return nil
}

// validateVendorSpecificFields validates database vendor-specific configuration fields
func validateVendorSpecificFields(cfg *DatabaseConfig) error {
	switch cfg.Type {
	case MongoDB:
		return validateMongoDBFields(cfg)
	case Oracle:
		return validateOracleFields(cfg)
	case PostgreSQL:
		// No vendor-specific validation needed for PostgreSQL currently
		return nil
	default:
		// Unknown database type should have been caught by validateDatabaseType
		return nil
	}
}

// validateMongoDBFields validates MongoDB-specific configuration fields
func validateMongoDBFields(cfg *DatabaseConfig) error {
	if cfg.Mongo.Replica.Preference != "" {
		if err := validateMongoDBReadPreference(cfg.Mongo.Replica.Preference); err != nil {
			return err
		}
	}

	if cfg.Mongo.Concern.Write != "" {
		if err := validateMongoDBWriteConcern(cfg.Mongo.Concern.Write); err != nil {
			return err
		}
	}

	return nil
}

// validateMongoDBReadPreference validates MongoDB read preference values
func validateMongoDBReadPreference(pref string) error {
	validPreferences := map[string]struct{}{
		"primary":            {},
		"primarypreferred":   {},
		"secondary":          {},
		"secondarypreferred": {},
		"nearest":            {},
	}

	if _, ok := validPreferences[strings.ToLower(pref)]; ok {
		return nil
	}

	validOptions := []string{"primary", "primaryPreferred", "secondary", "secondaryPreferred", "nearest"}
	return NewInvalidFieldError("database.mongo.replica.preference", fmt.Sprintf(errNotSupportedFmt, pref), validOptions)
}

// validateMongoDBWriteConcern validates MongoDB write concern values
func validateMongoDBWriteConcern(concern string) error {
	// First, try to parse as a non-negative integer
	if num, err := strconv.Atoi(concern); err == nil && num >= 0 {
		return nil
	}

	// Check for valid textual concerns (case-insensitive)
	validConcerns := []string{
		"majority",
		"acknowledged",
		"unacknowledged",
	}

	concernLower := strings.ToLower(concern)
	for _, valid := range validConcerns {
		if strings.EqualFold(valid, concernLower) {
			return nil
		}
	}

	validOptions := []string{"majority", "acknowledged", "unacknowledged", "or a non-negative integer"}
	return NewInvalidFieldError("database.mongo.concern.write", fmt.Sprintf(errNotSupportedFmt, concern), validOptions)
}

// validateOracleFields validates Oracle-specific configuration fields.
// It ensures that exactly one of Service.Name, SID, or Database is configured,
// mirroring the DSN selection logic in database/oracle/connection.go.
func validateOracleFields(cfg *DatabaseConfig) error {
	serviceSet := cfg.Oracle.Service.Name != ""
	sidSet := cfg.Oracle.Service.SID != ""
	databaseSet := cfg.Database != ""

	count := 0
	if serviceSet {
		count++
	}
	if sidSet {
		count++
	}
	if databaseSet {
		count++
	}

	if count == 0 {
		return &ConfigError{
			Category: "missing",
			Field:    "oracle connection identifier",
			Message:  "exactly one required",
			Action:   "set database.oracle.service.name, database.oracle.service.sid, or database.database",
		}
	}

	if count > 1 {
		configured := make([]string, 0, 3)
		if serviceSet {
			configured = append(configured, "service name")
		}
		if sidSet {
			configured = append(configured, "SID")
		}
		if databaseSet {
			configured = append(configured, "database name")
		}
		return &ConfigError{
			Category: "invalid",
			Field:    "oracle connection identifier",
			Message:  "multiple identifiers configured",
			Action:   fmt.Sprintf("remove all but one of: %s", strings.Join(configured, ", ")),
		}
	}

	return nil
}

// validateLog validates that cfg.Level is one of the supported log levels.
// It returns an error listing the allowed values if the level is invalid.
func validateLog(cfg *LogConfig) error {
	validLevels := []string{"trace", "debug", "info", "warn", "error", "fatal", "panic"}
	if !slices.Contains(validLevels, cfg.Level) {
		return NewInvalidFieldError("log.level", fmt.Sprintf(errNotSupportedFmt, cfg.Level), validLevels)
	}

	return nil
}

// validateCache validates cache configuration.
// Returns nil if cache is disabled or if all settings are valid.
func validateCache(cfg *CacheConfig) error {
	if !cfg.Enabled {
		return nil
	}

	// Apply cache manager defaults
	if err := applyCacheManagerDefaults(cfg); err != nil {
		return err
	}

	// Validate cache type
	validTypes := []string{"redis"}
	if !slices.Contains(validTypes, cfg.Type) {
		return NewInvalidFieldError("cache.type", fmt.Sprintf(errNotSupportedFmt, cfg.Type), validTypes)
	}

	// Validate Redis-specific settings
	if cfg.Type == "redis" {
		return validateRedisCache(&cfg.Redis)
	}

	return nil
}

// validateRedisCache validates Redis-specific cache configuration.
func validateRedisCache(cfg *RedisConfig) error {
	if cfg.Host == "" {
		return NewMissingFieldError("cache.redis.host", "CACHE_REDIS_HOST", "cache.redis.host")
	}

	if cfg.Port <= 0 || cfg.Port > 65535 {
		return NewInvalidFieldError("cache.redis.port", fmt.Sprintf(errInvalidField, cfg.Port), []string{portRange})
	}

	if cfg.Database < 0 || cfg.Database > 15 {
		return NewValidationError("cache.redis.database", "must be between 0 and 15")
	}

	if cfg.PoolSize <= 0 {
		return NewValidationError("cache.redis.poolsize", errMustBePositive)
	}

	if cfg.DialTimeout < 0 {
		return NewValidationError("cache.redis.dialtimeout", errMustBeNonNegative)
	}

	if cfg.ReadTimeout < -1 {
		return NewValidationError("cache.redis.readtimeout", "must be >= -1")
	}

	if cfg.WriteTimeout < -1 {
		return NewValidationError("cache.redis.writetimeout", "must be >= -1")
	}

	return nil
}

// validateMultitenant validates multi-tenant configuration and ensures no conflicts
// with single-tenant settings. When multitenant is enabled, database and messaging
// configurations must be provided by the tenant config provider.
func validateMultitenant(mt *MultitenantConfig, db *DatabaseConfig, msg *MessagingConfig, source *SourceConfig) error {
	if !mt.Enabled {
		return nil
	}

	// Validate resolver configuration
	if err := validateMultitenantResolver(&mt.Resolver); err != nil {
		return fmt.Errorf("resolver: %w", err)
	}

	// Validate limits configuration
	if err := validateMultitenantLimits(&mt.Limits); err != nil {
		return fmt.Errorf("limits: %w", err)
	}

	// Validate source type
	if err := validateSourceConfig(source); err != nil {
		return fmt.Errorf("source: %w", err)
	}

	// Validate static tenant configuration
	if err := validateStaticTenantConfig(source, mt, db, msg); err != nil {
		return err
	}

	return nil
}

// validateStaticTenantConfig validates static tenant configuration and conflicts
func validateStaticTenantConfig(source *SourceConfig, mt *MultitenantConfig, db *DatabaseConfig, msg *MessagingConfig) error {
	// For static sources, validate tenants if provided (optional but must be valid if present)
	// For dynamic sources, tenants are optional and loaded from external store
	if source.Type == SourceTypeStatic && mt.Tenants != nil {
		if len(mt.Tenants) == 0 {
			return fmt.Errorf("tenants: empty map provided - either omit tenants section or provide at least one tenant for static source")
		}
		if err := validateMultitenantTenants(mt.Tenants); err != nil {
			return fmt.Errorf("tenants: %w", err)
		}
	}

	// For static sources with tenants, ensure no conflict with single-tenant config
	if source.Type == SourceTypeStatic && mt.Tenants != nil && len(mt.Tenants) > 0 {
		return validateNoSingleTenantConflict(db, msg)
	}

	return nil
}

// validateNoSingleTenantConflict checks for conflicts with single-tenant configuration
func validateNoSingleTenantConflict(db *DatabaseConfig, msg *MessagingConfig) error {
	if IsDatabaseConfigured(db) {
		return &ConfigError{
			Category: "invalid",
			Field:    "database",
			Message:  "not allowed when static tenants are configured",
			Action:   "remove database section from root config or move to multitenant.tenants.<tenant_id>.database",
		}
	}
	if IsMessagingConfigured(msg) {
		return &ConfigError{
			Category: "invalid",
			Field:    "messaging",
			Message:  "not allowed when static tenants are configured",
			Action:   "remove messaging section from root config or move to multitenant.tenants.<tenant_id>.messaging",
		}
	}
	return nil
}

// validateMultitenantResolver validates tenant resolver configuration
func validateMultitenantResolver(cfg *ResolverConfig) error {
	validTypes := []string{"header", "subdomain", "composite"}
	if !slices.Contains(validTypes, cfg.Type) {
		return NewInvalidFieldError("multitenant.resolver.type", fmt.Sprintf(errNotSupportedFmt, cfg.Type), validTypes)
	}

	// Set defaults
	if cfg.Header == "" {
		cfg.Header = "X-Tenant-ID"
	}

	// Validate subdomain-specific configuration
	if cfg.Type == "subdomain" || cfg.Type == "composite" {
		if strings.TrimSpace(cfg.Domain) == "" {
			return NewMissingFieldError("multitenant.resolver.domain", "MULTITENANT_RESOLVER_DOMAIN", "multitenant.resolver.domain")
		}
		// Normalize: leading dot is optional in config
		if !strings.HasPrefix(cfg.Domain, ".") {
			cfg.Domain = "." + cfg.Domain
		}
	}

	return nil
}

// validateMultitenantLimits validates limits configuration with defaults
func validateMultitenantLimits(cfg *LimitsConfig) error {
	if cfg.Tenants <= 0 {
		cfg.Tenants = 100 // default
	}
	if cfg.Tenants > 1000 {
		return NewValidationError("multitenant.limits.tenants", "cannot exceed 1000")
	}
	return nil
}

// validateMultitenantTenants validates tenant configurations when they are provided
func validateMultitenantTenants(tenants map[string]TenantEntry) error {
	if len(tenants) == 0 {
		return NewValidationError("multitenant.tenants", "at least one tenant must be configured")
	}

	// Check consistency: if any tenant has messaging configured, all must have it configured
	// This prevents confusing scenarios where some tenants can use messaging and others cannot
	hasAnyMessaging := false
	hasNoMessaging := false

	for tenantID := range tenants {
		tenant := tenants[tenantID]
		if isTenantMessagingConfigured(&tenant.Messaging) {
			hasAnyMessaging = true
		} else {
			hasNoMessaging = true
		}
	}

	// Enforce all-or-nothing messaging configuration for consistency
	if hasAnyMessaging && hasNoMessaging {
		return &ConfigError{
			Category: "invalid",
			Field:    "multitenant.tenants messaging",
			Message:  "inconsistent configuration",
			Action:   "either all tenants must have messaging configured or none should",
		}
	}

	for tenantID := range tenants {
		tenant := tenants[tenantID]
		if tenantID == "" {
			return NewValidationError("multitenant.tenants", "tenant ID cannot be empty")
		}

		// Validate tenant database configuration
		if !IsDatabaseConfigured(&tenant.Database) {
			return NewMultiTenantError(tenantID, "database", "configuration required", fmt.Sprintf("add multitenant.tenants.%s.database section", tenantID))
		}
		if err := validateDatabase(&tenant.Database); err != nil {
			return fmt.Errorf("tenant %s database: %w", tenantID, err)
		}
	}

	return nil
}

// validateSourceConfig validates the source configuration type
func validateSourceConfig(cfg *SourceConfig) error {
	if cfg.Type != SourceTypeStatic && cfg.Type != SourceTypeDynamic {
		return NewInvalidFieldError("source.type", fmt.Sprintf(errNotSupportedFmt, cfg.Type), []string{"static", "dynamic"})
	}
	return nil
}

// IsMessagingConfigured determines if messaging is intentionally configured.
// This mirrors the logic used to determine if messaging should be initialized.
func IsMessagingConfigured(cfg *MessagingConfig) bool {
	return cfg.Broker.URL != ""
}

// isTenantMessagingConfigured determines if tenant messaging is intentionally configured.
// Returns true if the tenant has a non-empty messaging URL.
func isTenantMessagingConfigured(cfg *TenantMessagingConfig) bool {
	return strings.TrimSpace(cfg.URL) != ""
}
