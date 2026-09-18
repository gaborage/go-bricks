package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/gaborage/go-bricks/internal/cachekey"
	"github.com/gaborage/go-bricks/internal/clienttls"
)

// normalizeCache fills Redis defaults unconditionally, even when the cache is
// disabled — koanf already fills cache.redis.* in that state, and an enabled
// hand-built cache with a zero port/poolsize must not fail where koanf gives
// 6379/10. Manager defaults (mode-dependent MaxSize, see
// applyCacheManagerDefaults) fill only when enabled: koanf carries no
// cache.manager.* defaults, and a disabled cache's negative manager value is
// CreateCacheManager's to reject (ADR-054), not Validate's.
func normalizeCache(cfg *CacheConfig, multitenant bool) error {
	applyRedisDefaults(&cfg.Redis)

	// Unconditional, like the Redis defaults above: the load-through bound belongs to
	// the resolved cache instance, and a hand-built enabled cache must not inherit a
	// zero here. A negative value is rejected rather than normalized.
	if err := applyNonNegativeDefault(&cfg.LoadTimeout, defaultCacheLoadTimeout, "cache.loadtimeout"); err != nil {
		return err
	}

	if cfg.Enabled {
		return applyCacheManagerDefaults(cfg, multitenant)
	}
	return nil
}

// checkCache rejects an enabled cache's type and Redis fields; a disabled
// cache is not checked.
func checkCache(cfg *CacheConfig) error {
	if !cfg.Enabled {
		return nil
	}

	validTypes := []string{CacheTypeRedis}
	if !slices.Contains(validTypes, cfg.Type) {
		return NewInvalidFieldError("cache.type", fmt.Sprintf(errNotSupportedFmt, cfg.Type), validTypes)
	}
	return validateRedisCache(&cfg.Redis)
}

// applyRedisDefaults fills in production-safe Redis defaults for any unset
// fields. The top-level cache.* config receives these via koanf, but per-tenant
// cache config (multitenant.tenants.<id>.cache.*) has no koanf defaults, so this
// is the only place those values are populated for tenant caches. Host is left
// untouched: a missing host is a real misconfiguration that must fail fast.
func applyRedisDefaults(cfg *RedisConfig) {
	if cfg.Mode == "" {
		cfg.Mode = cacheRedisModeStandalone
	}
	if cfg.Port == 0 {
		cfg.Port = defaultRedisPort
	}
	if cfg.PoolSize == 0 {
		cfg.PoolSize = defaultRedisPoolSize
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = defaultRedisDialTimeout
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = defaultRedisReadTimeout
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = defaultRedisWriteTimeout
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = defaultRedisMaxRetries
	}
	if cfg.MinRetryBackoff == 0 {
		cfg.MinRetryBackoff = defaultRedisMinRetryBackoff
	}
	if cfg.MaxRetryBackoff == 0 {
		cfg.MaxRetryBackoff = defaultRedisMaxRetryBackoff
	}
}

// validateRedisCache validates Redis-specific cache configuration.
func validateRedisCache(cfg *RedisConfig) error {
	if cfg.Host == "" {
		return NewMissingFieldError("cache.redis.host", "CACHE_REDIS_HOST", "cache.redis.host")
	}

	if cfg.Port <= 0 || cfg.Port > 65535 {
		return NewInvalidFieldError("cache.redis.port", fmt.Sprintf(errInvalidField, cfg.Port), []string{portRange})
	}

	if err := validateRedisMode(cfg); err != nil {
		return err
	}

	if err := validateRedisKeyPrefix(cfg); err != nil {
		return err
	}

	if err := validateRedisUsername(cfg); err != nil {
		return err
	}

	if cfg.Database < 0 || cfg.Database > 15 {
		return NewValidationError(fieldCacheRedisDB, "must be between 0 and 15")
	}

	if cfg.PoolSize <= 0 {
		return NewValidationError(fieldCacheRedisPool, errMustBePositive)
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

	return validateRedisTLS(&cfg.TLS)
}

// validateRedisMode checks the transport selector and the one setting it
// forecloses. The enum is closed: an unrecognized value is refused rather than
// defaulted, because the default dials a single node and a cluster-protocol
// endpoint answers MOVED to the first key, so a typo would surface as a runtime
// cache failure instead of a startup one.
//
// Under cluster the database must be 0. go-redis drops the selected database on
// the way to the cluster client (UniversalOptions.Cluster copies no DB, and
// ClusterOptions has no such field), so accepting the pair would move the whole
// keyspace to database 0 on the mode flip alone. That error is addressed to the
// database key — the value that cannot be honored — and names the mode, because
// either could be the one the operator meant to change. It runs before the 0-15
// range check, since under cluster the range does not apply at all.
func validateRedisMode(cfg *RedisConfig) error {
	cacheRedisModes := []string{cacheRedisModeStandalone, cacheRedisModeCluster}
	if cfg.Mode != "" && !slices.Contains(cacheRedisModes, cfg.Mode) {
		return NewInvalidFieldError(fieldCacheRedisMode, fmt.Sprintf(errNotSupportedFmt, cfg.Mode), cacheRedisModes)
	}

	if cfg.Mode == cacheRedisModeCluster && cfg.Database != 0 {
		return NewValidationError(fieldCacheRedisDB, fmt.Sprintf(
			"must be 0 when %s is %s: the cluster client has no database selection",
			fieldCacheRedisMode, cacheRedisModeCluster))
	}

	return nil
}

// validateRedisUsername checks the ACL identity and the one key it requires.
//
// A whitespace-only ACL user is a typo, not an identity: it travels to Redis as an
// AUTH argument no ACL rule can match. Checked before the coupling rule so a name
// that is both blank and unaccompanied is reported as the typo it is.
//
// A name with no password is refused rather than dialed. go-redis builds the HELLO
// handshake's AUTH clause inside `if password != ""`, and gates the legacy AUTH
// fallback the same way, so an empty password sends no AUTH at all: the connection
// would run as whatever identity the endpoint gives an unauthenticated client — on a
// stock Redis the `default` user, typically `nopass ~* +@all`. Failing closed here
// turns that silent privilege swap into a startup error. The reverse pair is fine: a
// password alone is the legacy form that selects the default user.
func validateRedisUsername(cfg *RedisConfig) error {
	if cfg.Username == "" {
		return nil
	}
	if strings.TrimSpace(cfg.Username) == "" {
		return NewValidationError("cache.redis.username", "must not be whitespace-only")
	}
	if cfg.Password == "" {
		return NewValidationError("cache.redis.username",
			"requires cache.redis.password: the client sends no AUTH without one, "+
				"so the connection would silently run as the default user")
	}
	return nil
}

// validateRedisKeyPrefix checks an explicitly delivered key namespace against the
// shared grammar. An absent key (nil) carries no value to check: it takes app.name,
// whose own fitness is checkCacheKeyNamespace's rule. An explicit empty string is the
// opt-out and passes the grammar unchanged.
func validateRedisKeyPrefix(cfg *RedisConfig) error {
	if cfg.KeyPrefix == nil {
		return nil
	}
	if err := cachekey.Validate(*cfg.KeyPrefix); err != nil {
		return NewValidationError(fieldCacheRedisKeyPrefix, err.Error())
	}
	return nil
}

// checkCacheKeyNamespace enforces the one rule the app.name default creates: where no
// cache.redis.keyprefix was delivered, app.name IS the cache key namespace, so it must
// be usable as one. Without this check a name like "my service" would boot green and
// fail at the first cache access instead.
//
// The rule binds wherever the default applies, which is not only the root section: a
// multi-tenant deployment may enable caches solely under multitenant.tenants.<id>.cache,
// and those prefixes fold the same app.name in. Every failing section produces the same
// error — the fault is the name — so the tenant that tripped it is not named.
func checkCacheKeyNamespace(cfg *Config) error {
	if !cacheKeyPrefixDefaultApplies(cfg) {
		return nil
	}
	if err := cachekey.Validate(cfg.App.Name); err != nil {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldAppName,
			Message:  "is the default cache key namespace and " + err.Error(),
			Action:   "rename the application, or set cache.redis.keyprefix to a usable namespace",
		}
	}
	return nil
}

// cacheKeyPrefixDefaultApplies reports whether any enabled cache section would take its
// namespace from app.name.
func cacheKeyPrefixDefaultApplies(cfg *Config) bool {
	if cfg.Cache.Enabled && cfg.Cache.Redis.KeyPrefix == nil {
		return true
	}
	if !cfg.Multitenant.Enabled {
		return false
	}
	for tenantID := range cfg.Multitenant.Tenants {
		tenant := cfg.Multitenant.Tenants[tenantID]
		if tenant.Cache.Enabled && tenant.Cache.Redis.KeyPrefix == nil {
			return true
		}
	}
	return false
}

// fieldCacheRedisTLSPrefix namespaces a clienttls.Violation's relative key
// (e.g. "cafile") into this layer's config key. The tenant spelling
// (multitenant.tenants.<id>.cache.redis.tls.*) is derived from it downstream,
// so the "cache." head must stay.
const fieldCacheRedisTLSPrefix = "cache.redis.tls."

// cacheRedisTLSErrPrefix names the cache's error namespace for the shared TLS
// loader, matching the spelling cache/redis uses for the same material.
const cacheRedisTLSErrPrefix = "cache: redis: tls:"

// validateRedisTLS checks TLS material configuration in two passes. The first is
// structural (staged material under a disabled block, mutual exclusivity of
// file/value sources, cert/key pairing, min-version enum); its rules live in
// clienttls, beside the loader that consumes the same material, and this only
// maps a violation onto the cache.redis.tls.* keys.
//
// The second pass actually loads the material, which means this validation reads
// files — unusual here, and deliberate. Unlike server.tls, whose material is read
// at Start() one hop later, a Redis client is created lazily per tenant on first
// use, so config validation is the only door at which a missing or corrupt bundle
// can fail the boot rather than a request hours later. The dial loads it again;
// the two loads are a startup gate and a use-time one, not a cache.
func validateRedisTLS(cfg *RedisTLSConfig) error {
	m := clienttls.Material{
		CertFile:   cfg.CertFile,
		CertValue:  cfg.CertValue,
		KeyFile:    cfg.KeyFile,
		KeyValue:   cfg.KeyValue,
		CAFile:     cfg.CAFile,
		CAValue:    cfg.CAValue,
		ServerName: cfg.ServerName,
		MinVersion: cfg.MinVersion,
	}
	if v := clienttls.ValidateMaterial(&m, cfg.Enabled); v != nil {
		return NewValidationError(fieldCacheRedisTLSPrefix+v.Field, v.Message)
	}
	if !cfg.Enabled {
		return nil
	}
	if _, err := clienttls.Build(cacheRedisTLSErrPrefix, &m); err != nil {
		return NewValidationError(strings.TrimSuffix(fieldCacheRedisTLSPrefix, "."), err.Error())
	}
	return nil
}
