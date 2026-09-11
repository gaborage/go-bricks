package config

import (
	"fmt"
	"slices"
	"strings"

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
