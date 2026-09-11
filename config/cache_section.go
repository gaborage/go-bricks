package config

import (
	"fmt"
	"slices"
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

// validateRedisTLS checks structural TLS material configuration (staged
// material under a disabled block, mutual exclusivity of file/value sources,
// cert/key pairing, min-version enum). It does NOT touch the filesystem —
// reading and parsing PEM material happens when the client dials.
func validateRedisTLS(cfg *RedisTLSConfig) error {
	if !cfg.Enabled {
		if redisTLSHasMaterial(cfg) {
			return NewValidationError(fieldCacheRedisTLSEnabled,
				"must be true when any cache.redis.tls.* material is configured")
		}
		return nil
	}

	if err := validateRedisTLSSources(fieldCacheRedisTLSCAFile, fieldCacheRedisTLSCAValue, cfg.CAFile, cfg.CAValue); err != nil {
		return err
	}

	if err := validateRedisTLSSources(fieldCacheRedisTLSCertFile, fieldCacheRedisTLSCertValue, cfg.CertFile, cfg.CertValue); err != nil {
		return err
	}

	if err := validateRedisTLSSources(fieldCacheRedisTLSKeyFile, fieldCacheRedisTLSKeyValue, cfg.KeyFile, cfg.KeyValue); err != nil {
		return err
	}

	if err := validateRedisTLSPair(cfg); err != nil {
		return err
	}

	switch cfg.MinVersion {
	case "", tlsVersion12, tlsVersion13:
		return nil
	default:
		return NewInvalidFieldError(fieldCacheRedisTLSMinVersion, fmt.Sprintf(errInvalidField, cfg.MinVersion), []string{tlsVersion12, tlsVersion13})
	}
}

// validateRedisTLSPair rejects a half-configured client certificate: a cert
// without its key, or a key without its cert.
func validateRedisTLSPair(cfg *RedisTLSConfig) error {
	hasCert := cfg.CertFile != "" || cfg.CertValue != ""
	hasKey := cfg.KeyFile != "" || cfg.KeyValue != ""

	switch {
	case hasCert && !hasKey:
		return NewValidationError(fieldCacheRedisTLSKeyFile,
			"a client certificate requires "+fieldCacheRedisTLSKeyFile+" or "+fieldCacheRedisTLSKeyValue)
	case hasKey && !hasCert:
		return NewValidationError(fieldCacheRedisTLSCertFile,
			"a client key requires "+fieldCacheRedisTLSCertFile+" or "+fieldCacheRedisTLSCertValue)
	default:
		return nil
	}
}

// validateRedisTLSSources enforces at most one of a file/value pair for a
// single PEM piece; neither set is legal — the piece is simply absent.
func validateRedisTLSSources(fileField, valueField, file, value string) error {
	if file != "" && value != "" {
		return NewValidationError(fileField, fileField+" and "+valueField+" are mutually exclusive (exactly one)")
	}
	return nil
}

// redisTLSHasMaterial reports whether any field other than Enabled is set.
func redisTLSHasMaterial(cfg *RedisTLSConfig) bool {
	return cfg.CAFile != "" || cfg.CAValue != "" ||
		cfg.CertFile != "" || cfg.CertValue != "" ||
		cfg.KeyFile != "" || cfg.KeyValue != "" ||
		cfg.ServerName != "" || cfg.MinVersion != ""
}
