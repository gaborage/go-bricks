package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gaborage/go-bricks/logger"
)

// Database pool defaults
const (
	defaultSlowQueryThreshold = 200 * time.Millisecond
	defaultMaxQueryLength     = 1000
	defaultKeepAliveEnabled   = true
	defaultKeepAliveInterval  = 60 * time.Second
	defaultPoolIdleTime       = 5 * time.Minute  // Close idle connections before NAT/firewall timeout
	defaultPoolLifetimeMax    = 30 * time.Minute // Force periodic connection recycling
	defaultPoolMaxConnections = int32(25)        // Maximum open connections (tune to workload/server capacity)
)

// Database session defaults
const (
	// DefaultTimezone is the IANA timezone applied when a timezone config field
	// (database.timezone, scheduler.timezone) is unset. UTC is opinionated to keep
	// behavior identical across environments regardless of host or server defaults.
	DefaultTimezone = "UTC"
	// DefaultDatabaseTimezone is the default IANA timezone for database sessions.
	// Retained as a domain-named alias of DefaultTimezone for readability at
	// database call sites.
	DefaultDatabaseTimezone = DefaultTimezone
	// TimezoneDisabledSentinel opts out of session-level timezone enforcement,
	// preserving the database server's default timezone (legacy behavior).
	// Connection layers compare against this constant to decide whether to
	// apply per-connection timezone setup.
	TimezoneDisabledSentinel = "-"

	// DefaultBodyLimitBytes is the maximum request body size (10 MB) applied when
	// server.bodylimit is unset or resolves to a non-positive value. Single source
	// of truth for both the koanf default (loadDefaults) and the server-side
	// wire-up fallback (server.SetupMiddlewares).
	DefaultBodyLimitBytes int64 = 10 * 1024 * 1024

	// DefaultKeyStoreSecretMinLength is the byte floor for symmetric keystore
	// secrets when keystore.secretminlength is absent. Single source of truth: the
	// normalize fill applies it (normalizeKeyStore) and the koanf default derives
	// from that fill rather than rendering it a second time (derivedDefaultKeys).
	DefaultKeyStoreSecretMinLength = 32
)

// Messaging reconnection defaults
const (
	defaultReconnectDelay    = 5 * time.Second  // Initial delay between reconnection attempts
	defaultReinitDelay       = 2 * time.Second  // Delay before channel reinitialization
	defaultResendDelay       = 5 * time.Second  // Delay before retrying failed publishes
	defaultConnectionTimeout = 30 * time.Second // Per-publish broker confirmation (ACK/NACK) wait
	defaultReadyTimeout      = 5 * time.Second  // Pre-flight wait for a not-yet-ready client before a publish begins
	defaultMaxReconnectDelay = 60 * time.Second // Maximum delay for exponential backoff cap
	defaultMaxPublishers     = 50               // Maximum publisher clients in cache
	defaultPublisherIdleTTL  = 1 * time.Hour    // Time before idle publishers are evicted (single-tenant)
	// defaultPublisherIdleTTLMultiTenant is the multi-tenant idle-eviction default. It is
	// deliberately shorter than the single-tenant default to bound per-tenant publisher
	// churn, and matches the value multi-tenant deployments actually received before
	// applyMessagingDefaults became mode-aware (app/managers.go's BuildMessagingOptions
	// multi-tenant fallback — previously unreachable on the production path once this
	// function unconditionally applied the single-tenant default here first).
	defaultPublisherIdleTTLMultiTenant = 10 * time.Minute
	defaultPublisherCleanupInterval    = 2 * time.Minute // Publisher-pool cleanup goroutine frequency
	defaultMaxPublishAttempts          = 5               // Bounded publish retry attempts before giving up
)

// Native stream-protocol (messaging.streams.*) defaults
const (
	defaultStreamsOffsetCount    = 500             // Handled messages before a server-side offset commit
	defaultStreamsOffsetInterval = 5 * time.Second // Elapsed time before a pending offset is committed

	streamsURIScheme    = "rabbitmq-stream"
	streamsURITLSScheme = "rabbitmq-stream+tls"
)

// Cache manager defaults
const (
	defaultCacheMaxSize         = 100              // Maximum tenant cache instances
	defaultCacheIdleTTL         = 15 * time.Minute // Idle timeout per cache
	defaultCacheCleanupInterval = 5 * time.Minute  // Cleanup goroutine frequency
)

// Database manager defaults
const (
	defaultDatabaseManagerMaxSize            = 10
	defaultDatabaseManagerIdleTTL            = 1 * time.Hour
	defaultDatabaseManagerIdleTTLMultiTenant = 30 * time.Minute
	defaultDatabaseManagerCleanupInterval    = 5 * time.Minute
)

// Redis cache defaults. The top-level cache.* keys receive these via koanf
// (see config.go), but per-tenant multitenant.tenants.<id>.cache.* keys have no
// koanf defaults, so validation must apply them itself (see applyRedisDefaults).
const (
	defaultRedisPort            = 6379
	defaultRedisPoolSize        = 10
	defaultRedisDialTimeout     = 5 * time.Second
	defaultRedisReadTimeout     = 3 * time.Second
	defaultRedisWriteTimeout    = 3 * time.Second
	defaultRedisMaxRetries      = 3
	defaultRedisMinRetryBackoff = 8 * time.Millisecond
	defaultRedisMaxRetryBackoff = 512 * time.Millisecond
)

// Startup timeout defaults
const (
	defaultStartupTimeout              = 10 * time.Second // Overall startup timeout
	defaultStartupDatabaseTimeout      = 10 * time.Second // Database health check timeout
	defaultStartupMessagingTimeout     = 10 * time.Second // Broker connection timeout
	defaultStartupCacheTimeout         = 5 * time.Second  // Cache initialization timeout
	defaultStartupObservabilityTimeout = 15 * time.Second // OTLP provider initialization timeout
)

// Scheduler timeout defaults. Single source in the strict sense now: normalize applies
// these, the koanf loader DERIVES its values from that (derivedDefaultKeys in config.go)
// rather than rendering them a second time, and the scheduler module reads the normalized
// config rather than mirroring them (#1029, #1023).
const (
	defaultSchedulerShutdownTimeout  = 30 * time.Second // Budget for in-flight jobs during graceful shutdown
	defaultSchedulerSlowJobThreshold = 25 * time.Second // Successful jobs slower than this log at WARN
)

// Database type constants
const (
	PostgreSQL = "postgresql"
	Oracle     = "oracle"
)

// MinDatabasePasswordLength is the minimum length for a non-empty database
// password. Below this, the migration engine cannot safely substring-redact the
// password from Flyway output and suppresses the whole output instead, which
// hides a migration's outcome (see migration.redactPassword). Empty passwords
// (trust/IAM auth) are exempt.
const MinDatabasePasswordLength = 8

// Environment constants
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Cache type constants
const (
	CacheTypeRedis = "redis"
)

// Validation error message constants
const (
	errMustBeNonNegative    = "must be non-negative"
	errMustBePositive       = "must be positive"
	errNotSupportedFmt      = "'%s' is not supported"
	errDatabaseIncomplete   = "database configuration incomplete"
	portRange               = "1-65535"
	fieldDatabase           = "database"
	fieldDatabases          = "databases"
	fieldMultitenantTenants = "multitenant.tenants"
	fieldKeystoreKeys       = "keystore.keys"
	fieldDatabasePort       = "database.port"
	fieldDatabasePassword   = "database.password"
	fieldMessaging          = "messaging"
	fieldCache              = "cache"
	fieldDebug              = "debug"
	fieldServerPort         = "server.port"
	fieldLogLevel           = "log.level"
	fieldAppEnv             = "app.env"
	fieldAppRateLimit       = "app.rate.limit"
	fieldCacheRedisDB       = "cache.redis.database"
	fieldCacheRedisPool     = "cache.redis.poolsize"
	fieldResolverOrder      = "multitenant.resolver.order"
	errInvalidField         = "invalid value: %v"
	databasesFieldPrefix    = "databases.%s"
	keystoreKeysFieldPrefix = "keystore.keys.%s"
	tenantsFieldPrefix      = "multitenant.tenants.%s"
	defaultHost             = "localhost"

	fieldServerTLSCertFile   = "server.tls.certfile"
	fieldServerTLSCertValue  = "server.tls.certvalue"
	fieldServerTLSKeyFile    = "server.tls.keyfile"
	fieldServerTLSKeyValue   = "server.tls.keyvalue"
	fieldServerTLSMinVersion = "server.tls.minversion"
	tlsVersion12             = "1.2"
	tlsVersion13             = "1.3"

	fieldServerForwardedClientCertRequire = "server.forwardedclientcert.require"

	fieldServerTrustedProxies = "server.trustedproxies"

	// actionListSpecificProxyRanges is the remedy for a default route on ANY of the three
	// trusted-proxy keys. Shared so the three refusals cannot drift apart in wording.
	actionListSpecificProxyRanges = "list the specific proxy ranges to trust instead of a default route"
	// The other two TRUST keys. All three answer to the same default-route rule and
	// differ only in which parser handles their remaining syntax.
	fieldDebugTrustedProxies     = "debug.trustedproxies"
	fieldSchedulerTrustedProxies = "scheduler.security.trustedproxies"

	// An ALLOWLIST key, deliberately not subject to that rule: admitting every address
	// is a legitimate posture here (ADR-049 recommends it), where trusting every
	// address is not.
	fieldDebugAllowedIPs = "debug.allowedips"
)

const (
	fieldMessagingStreamsURI = "messaging.streams.uri"

	fieldDatabaseTLS = "database.tls"

	sslModeDisable    = "disable"
	sslModeAllow      = "allow"
	sslModePrefer     = "prefer"
	sslModeRequire    = "require"
	sslModeVerifyCA   = "verify-ca"
	sslModeVerifyFull = "verify-full"
)

// checkDebug validates the debug block's two IP-shaped keys: debug.allowedips must be
// syntactically parseable (bare address or CIDR), and debug.trustedproxies must not
// contain a default route and must not be entirely unparseable.
// Semantics match scheduler.security.trustedproxies: empty is valid (proxy
// headers ignored), an all-invalid list fails fast, and a partial-invalid list
// passes with a middleware-time WARN so a single typo cannot silently weaken
// the allowlist's spoofing protection.
func checkDebug(cfg *DebugConfig) error {
	if err := validateIPOrCIDRList(fieldDebugAllowedIPs, cfg.AllowedIPs); err != nil {
		return err
	}
	return validateTrustedProxyList(fieldDebugTrustedProxies, cfg.TrustedProxies)
}

// normalizeApp fills the startup timeout defaults.
func normalizeApp(cfg *AppConfig) error {
	return applyStartupDefaults(&cfg.Startup)
}

// checkApp rejects a missing Name or Version, an Env outside envFormat (see
// its docs for the policy), and negative rate limits.
func checkApp(cfg *AppConfig) error {
	if cfg.Name == "" {
		return NewMissingFieldError("app.name", "APP_NAME", "app.name")
	}

	if cfg.Version == "" {
		return NewMissingFieldError("app.version", "APP_VERSION", "app.version")
	}

	if !envFormat.MatchString(cfg.Env) {
		return NewInvalidFieldError(
			fieldAppEnv,
			fmt.Sprintf("'%s' must be 1-32 lowercase alphanumeric or hyphen, starting with a letter", cfg.Env),
			nil,
		)
	}

	if cfg.Rate.Limit < 0 {
		return NewValidationError(fieldAppRateLimit, errMustBeNonNegative)
	}

	if cfg.Rate.Burst < 0 {
		return NewValidationError("app.rate.burst", errMustBeNonNegative)
	}

	return nil
}

const (
	identityKeyHost = "host"
	identityKeyPort = "port"
)

// normalizeIANATimezone defaults an empty timezone to UTC and validates it as a
// loadable IANA name. The "-" sentinel passes through unchanged so callers can
// treat it as "disabled". field labels the validation error. Shared by config
// sections that carry IANA timezone fields (currently database and scheduler)
// so the contracts cannot drift.
func normalizeIANATimezone(field, value string) (string, error) {
	if value == "" {
		value = DefaultTimezone
	}
	if value == TimezoneDisabledSentinel {
		return value, nil
	}
	if _, err := time.LoadLocation(value); err != nil {
		return value, NewInvalidFieldError(
			field,
			fmt.Sprintf("invalid IANA timezone %q: %v", value, err),
			[]string{`a valid IANA timezone (e.g. "UTC", "America/New_York") or "-" to disable`},
		)
	}
	return value, nil
}

// sectionNamePattern is the grammar every USER-CHOSEN section key obeys:
// entries under databases, multitenant.tenants and keystore.keys. It is the
// resolver's tenant-ID grammar without the length bound, which stays the
// resolver's.
//
// The reason is reachability, not taste. Load maps an environment variable to
// a config key by lowercasing it and turning '_' into '.', which is not
// injective: DATABASES_REPORT_DB_PORT reaches databases.report.db.port, so a
// section named report_db cannot be addressed by any variable — its value
// either lands on a phantom key or, when a sibling named report exists,
// silently on the sibling. Uppercase is unreachable the same way. Rejecting
// the name at check makes the transform injective over every key that
// survives startup, without touching the transform itself (ADR-024).
//
// Hyphen is legal here; whether a hyphenated name is settable depends on the
// runtime (Docker and Kubernetes permit '-' in variable names, POSIX `export`
// does not), which the docs state and this rule does not police.
var sectionNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// checkSectionName rejects a user-chosen section key no environment variable
// can address. field is the key PATH, so an operator can find the entry.
func checkSectionName(field, name string) error {
	if sectionNamePattern.MatchString(name) {
		return nil
	}
	return &ConfigError{
		Category: errCategoryInvalid,
		Message:  fmt.Sprintf("name %q is not reachable by an environment variable", name),
		Field:    field,
		Action:   "rename it using lowercase letters, digits and '-' only: an environment variable lowercases and maps '_' to the config path delimiter, so any other name is unaddressable",
	}
}

// validateNamedDatabaseName checks the map key: non-empty, not the reserved
// prefix, reachable by an environment variable (checkSectionName), and not
// colliding with a static tenant ID.
func validateNamedDatabaseName(name string, mt *MultitenantConfig) error {
	if name == "" {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabases,
			Message:  "database name cannot be empty",
			Action:   "provide a non-empty key for each entry in databases section",
		}
	}
	// A '.' collides with koanf's path delimiter: constructed section paths
	// (databases.<name>) become ambiguous, so the bare "databases" Field is used
	// here rather than fmt.Sprintf(databasesFieldPrefix, name) — embedding the
	// dotted name would reproduce the same ambiguity in the error itself. This
	// runs BEFORE the reserved-prefix rule below, which does embed the name: a
	// name breaking both (`gb_.foo`) must still be reported against the parent.
	if strings.Contains(name, ".") {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldDatabases,
			Message:  fmt.Sprintf("database name %q cannot contain '.' (the config path delimiter)", name),
			Action:   "rename the databases entry without dots",
		}
	}
	if strings.HasPrefix(name, NamedDatabasePrefix) {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fmt.Sprintf(databasesFieldPrefix, name),
			Message:  fmt.Sprintf("name cannot start with reserved prefix '%s'", NamedDatabasePrefix),
			Action:   fmt.Sprintf("rename databases.%s to remove the '%s' prefix", name, NamedDatabasePrefix),
		}
	}
	// Everything the dot rule above does not already reject is judged by the
	// shared reachability grammar. It runs after that rule because a dotted
	// name cannot carry an unambiguous Field, which this one needs.
	if err := checkSectionName(fmt.Sprintf(databasesFieldPrefix, name), name); err != nil {
		return err
	}
	if mt.Enabled && mt.Tenants != nil {
		if _, exists := mt.Tenants[name]; exists {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fmt.Sprintf(databasesFieldPrefix, name),
				Message:  fmt.Sprintf("name conflicts with tenant ID '%s'", name),
				Action:   fmt.Sprintf("rename databases.%s or multitenant.tenants.%s to avoid conflict", name, name),
			}
		}
	}
	return nil
}

// applyModeAwarePoolDefault handles pool-size keys whose multi-tenant default is
// dynamic (the builder scales the pool to the tenant limit when the key is unset):
// zero is preserved in multi-tenant mode so that scaling can happen, negative is
// always rejected, and single-tenant zero gets the flat default. Stamping the flat
// default in multi-tenant mode would silently cap the pool below the tenant limit
// (#661). Shared by the messaging, cache, and database manager appliers.
func applyModeAwarePoolDefault(field *int, def int, name string, multitenant bool) error {
	if multitenant {
		if *field < 0 {
			return NewValidationError(name, errMustBeNonNegative)
		}
		return nil
	}
	return applyNonNegativeDefault(field, def, name)
}

// applyNonNegativeDefault sets *field to def when it is zero, or returns a validation
// error for the named config key when it is negative. Shared across default-appliers
// to keep the "zero applies the default, negative is invalid" rule in one place.
func applyNonNegativeDefault[T ~int64 | ~int](field *T, def T, name string) error {
	switch {
	case *field == 0:
		*field = def
	case *field < 0:
		return NewValidationError(name, errMustBeNonNegative)
	}
	return nil
}

// applyCacheManagerDefaults sets production-safe defaults for cache manager configuration.
//
// It modifies cfg in-place:
//   - Manager.IdleTTL: if 0, sets to 15m; if negative, returns an error.
//   - Manager.CleanupInterval: if 0, sets to 5m; if negative, returns an error.
//   - Manager.MaxSize: if 0, sets to 100 when multitenant is false; in multi-tenant
//     mode zero is preserved so app.ManagerConfigBuilder.BuildCacheOptions scales the
//     cache pool to the tenant limit; if negative, returns an error.
//
// IdleTTL/CleanupInterval have no mode-specific defaults (cache carries no such constants).
//
// Returns an error when any value is invalid; otherwise returns nil.
func applyCacheManagerDefaults(cfg *CacheConfig, multitenant bool) error {
	if err := applyNonNegativeDefault(&cfg.Manager.IdleTTL, defaultCacheIdleTTL, "cache.manager.idlettl"); err != nil {
		return err
	}
	if err := applyNonNegativeDefault(&cfg.Manager.CleanupInterval, defaultCacheCleanupInterval, "cache.manager.cleanupinterval"); err != nil {
		return err
	}

	return applyModeAwarePoolDefault(&cfg.Manager.MaxSize, defaultCacheMaxSize, "cache.manager.maxsize", multitenant)
}

// applyDatabaseManagerDefaults sets production-safe defaults for the database
// connection-manager pool.
//
// It modifies cfg in-place:
//   - IdleTTL: if 0, sets to 1h single-tenant / 30m multi-tenant; if negative, errors.
//   - CleanupInterval: if 0, sets to 5m; if negative, errors.
//   - MaxSize: if 0, sets to 10 when multitenant is false; in multi-tenant mode
//     zero is preserved so app.ManagerConfigBuilder.BuildDatabaseOptions scales
//     the handle pool to the tenant limit; if negative, errors.
//
// Returns an error when any value is invalid; otherwise returns nil.
func applyDatabaseManagerDefaults(cfg *DatabaseManagerConfig, multitenant bool) error {
	idleTTLDefault := defaultDatabaseManagerIdleTTL
	if multitenant {
		idleTTLDefault = defaultDatabaseManagerIdleTTLMultiTenant
	}
	if err := applyNonNegativeDefault(&cfg.IdleTTL, idleTTLDefault, "database.manager.idlettl"); err != nil {
		return err
	}
	if err := applyNonNegativeDefault(&cfg.CleanupInterval, defaultDatabaseManagerCleanupInterval, "database.manager.cleanupinterval"); err != nil {
		return err
	}

	return applyModeAwarePoolDefault(&cfg.MaxSize, defaultDatabaseManagerMaxSize, "database.manager.maxsize", multitenant)
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

// checkLog rejects an unsupported log level, listing the allowed values.
func checkLog(cfg *LogConfig) error {
	validLevels := []string{logger.LevelTrace, logger.LevelDebug, logger.LevelInfo, logger.LevelWarn, logger.LevelError, logger.LevelFatal, logger.LevelPanic}
	if !slices.Contains(validLevels, cfg.Level) {
		return NewInvalidFieldError(fieldLogLevel, fmt.Sprintf(errNotSupportedFmt, cfg.Level), validLevels)
	}

	return nil
}
