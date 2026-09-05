package app

import (
	"context"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/cache/redis"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
)

// FactoryResolver encapsulates the logic for resolving factory functions
// from Options, providing default implementations when not specified.
type FactoryResolver struct {
	opts *Options
}

// NewFactoryResolver creates a new factory resolver with the given options.
func NewFactoryResolver(opts *Options) *FactoryResolver {
	return &FactoryResolver{
		opts: opts,
	}
}

// DatabaseConnector returns the appropriate database connector function.
// If no custom connector is provided in options, returns the default implementation.
func (f *FactoryResolver) DatabaseConnector() database.Connector {
	if f.opts != nil && f.opts.DatabaseConnector != nil {
		return f.opts.DatabaseConnector
	}
	return database.NewConnection
}

// MessagingClientFactoryOptions bundles the per-publish tuning knobs threaded
// into the default messaging client factory. Introduced alongside the
// existing MessagingClientFactory (kept byte-identical for apidiff
// compatibility) so ReadyTimeout could be added without breaking that
// method's exported signature.
type MessagingClientFactoryOptions struct {
	ConnectionTimeout  time.Duration
	MaxPublishAttempts int
	ReadyTimeout       time.Duration
	// PublishTimeout is the aggregate per-publish bound (messaging.publishtimeout);
	// zero leaves the publish unbounded.
	PublishTimeout    time.Duration
	ReconnectDelay    time.Duration
	ReconnectMaxDelay time.Duration
	ReinitDelay       time.Duration
	ResendDelay       time.Duration
}

// MessagingClientFactory returns the appropriate messaging client factory function.
// The default factory creates AMQPClient instances configured with the supplied per-publish
// connection timeout and bounded publish-retry attempts. If a custom
// Options.MessagingClientFactory is set it owns construction and receives only (url, log) —
// neither connectionTimeout nor maxPublishAttempts applies to it.
//
// Deprecated: kept for backward compatibility (its signature cannot change without
// breaking apidiff). Use MessagingClientFactoryWithOptions, which also carries
// ReadyTimeout and the four reconnect delays (messaging.reconnect.*) — clients
// built through this method keep the hardcoded client defaults for those.
func (f *FactoryResolver) MessagingClientFactory(connectionTimeout time.Duration, maxPublishAttempts int) messaging.ClientFactory {
	return f.MessagingClientFactoryWithOptions(MessagingClientFactoryOptions{
		ConnectionTimeout:  connectionTimeout,
		MaxPublishAttempts: maxPublishAttempts,
	})
}

// MessagingClientFactoryWithOptions is the options-struct successor to
// MessagingClientFactory. Internal bootstrap wiring (CreateMessagingManager)
// uses this method so every messaging.reconnect.* client knob reaches the client.
//
// Same custom-factory precedence as MessagingClientFactory: if
// Options.MessagingClientFactory is set it owns construction and receives only
// (url, log) — NO field of opts applies to it, so all messaging.reconnect.*
// config (timeouts, attempts, and the four reconnect delays) is bypassed and
// custom-built clients keep the hardcoded client defaults.
func (f *FactoryResolver) MessagingClientFactoryWithOptions(opts MessagingClientFactoryOptions) messaging.ClientFactory {
	if f.opts != nil && f.opts.MessagingClientFactory != nil {
		return func(url string, log logger.Logger) messaging.AMQPClient {
			return f.opts.MessagingClientFactory(url, log)
		}
	}

	return func(url string, log logger.Logger) messaging.AMQPClient {
		return messaging.NewAMQPClient(url, log,
			messaging.WithConnectionTimeout(opts.ConnectionTimeout),
			messaging.WithMaxPublishAttempts(opts.MaxPublishAttempts),
			messaging.WithReadyTimeout(opts.ReadyTimeout),
			messaging.WithPublishTimeout(opts.PublishTimeout),
			messaging.WithReconnectDelay(opts.ReconnectDelay),
			messaging.WithReconnectMaxDelay(opts.ReconnectMaxDelay),
			messaging.WithReinitDelay(opts.ReinitDelay),
			messaging.WithResendDelay(opts.ResendDelay),
		)
	}
}

// CacheConnector returns the appropriate cache connector function.
// If no custom connector is provided in options, returns a Redis connector that
// reads configuration from the resourceSource for the given tenant/key.
func (f *FactoryResolver) CacheConnector(resourceSource TenantStore, log logger.Logger) cache.Connector {
	if f.opts != nil && f.opts.CacheConnector != nil {
		return f.opts.CacheConnector
	}

	return newRedisConnector(resourceSource, log)
}

// ResourceSource returns the appropriate tenant resource source.
// If no custom resource source is provided in options, creates one from config.
func (f *FactoryResolver) ResourceSource(cfg *config.Config) TenantStore {
	if f.opts != nil && f.opts.ResourceSource != nil {
		return f.opts.ResourceSource
	}
	return config.NewTenantStore(cfg)
}

// HasCustomFactories returns true if any custom factories are provided in options.
// This can be useful for logging or debugging purposes.
func (f *FactoryResolver) HasCustomFactories() bool {
	if f.opts == nil {
		return false
	}

	return f.opts.DatabaseConnector != nil ||
		f.opts.MessagingClientFactory != nil ||
		f.opts.CacheConnector != nil ||
		f.opts.ResourceSource != nil
}

// newRedisConnector creates a cache connector that reads Redis configuration
// from the resourceSource for each tenant/key and creates Redis cache instances.
func newRedisConnector(resourceSource TenantStore, log logger.Logger) cache.Connector {
	return func(ctx context.Context, key string) (cache.Cache, error) {
		if resourceSource == nil {
			err := fmt.Errorf("tenant resource source is nil for key '%s'", key)
			log.Error().
				Str("key", key).
				Msg("Cannot resolve cache configuration: nil resource source")
			return nil, err
		}

		cacheCfg, err := resourceSource.CacheConfig(ctx, key)
		if err != nil {
			log.Debug().
				Err(err).
				Str("key", key).
				Msg("Cache config not available")
			return nil, err
		}

		if err := validateRedisCacheConfig(cacheCfg, key, log); err != nil {
			// Wrapped once, at this one call site, for every check validateRedisCacheConfig
			// raises — the door's own errors cannot forget the wrap the way #1248 did, because
			// there is only one place left to call it from.
			return nil, config.QualifyCacheConfigErrorForKey(err, key)
		}

		return connectRedisCache(cacheCfg, key, log)
	}
}

// validateRedisCacheConfig rejects a cache config this connector cannot build a Redis client
// from: unexpectedly nil, disabled, an unsupported type, or a missing host. Every error it
// raises is root-spelled — addressing it to key is the caller's single responsibility, not
// this function's, which is what makes the wrap impossible to forget for a check added here
// later.
func validateRedisCacheConfig(cacheCfg *config.CacheConfig, key string, log logger.Logger) error {
	if cacheCfg == nil {
		log.Error().
			Str("key", key).
			Msg("Cache configuration unexpectedly nil")
		return config.NewValidationError("cache", fmt.Sprintf("configuration is nil for key '%s'", key))
	}

	if !cacheCfg.Enabled {
		log.Error().
			Str("key", key).
			Msg("Cache configuration has Enabled=false")
		return config.NewNotConfiguredError("cache", "CACHE_ENABLED", "cache.enabled")
	}

	// Validate cache type is "redis" (or empty for backward compatibility)
	if cacheCfg.Type != "" && cacheCfg.Type != config.CacheTypeRedis {
		log.Error().
			Str("key", key).
			Str("type", cacheCfg.Type).
			Msg("Invalid cache type - only 'redis' is supported")
		return config.NewInvalidFieldError("cache.type",
			fmt.Sprintf("unsupported type '%s'", cacheCfg.Type),
			[]string{config.CacheTypeRedis})
	}

	if cacheCfg.Redis.Host == "" {
		log.Error().
			Str("key", key).
			Msg("Redis host is empty - cannot create cache instance")
		return config.NewMissingFieldError("cache.redis.host", "CACHE_REDIS_HOST", "cache.redis.host")
	}

	return nil
}

// connectRedisCache builds and dials the Redis client for an already-validated cache config.
// Its error is a connection failure, not a config-shape one, so it is returned as-is rather
// than addressed to key — the same as it was before this door's four validation checks were
// concentrated into validateRedisCacheConfig above.
//
// Do not add a config-validation return here: a config-shape check belongs in
// validateRedisCacheConfig, whose whole return the door qualifies. This function returns
// connection-dial errors only, which are deliberately not addressed to a config path.
func connectRedisCache(cacheCfg *config.CacheConfig, key string, log logger.Logger) (cache.Cache, error) {
	redisCfg := &redis.Config{
		Host:            cacheCfg.Redis.Host,
		Port:            cacheCfg.Redis.Port,
		Password:        cacheCfg.Redis.Password,
		Database:        cacheCfg.Redis.Database,
		PoolSize:        cacheCfg.Redis.PoolSize,
		DialTimeout:     cacheCfg.Redis.DialTimeout,
		ReadTimeout:     cacheCfg.Redis.ReadTimeout,
		WriteTimeout:    cacheCfg.Redis.WriteTimeout,
		MaxRetries:      cacheCfg.Redis.MaxRetries,
		MinRetryBackoff: cacheCfg.Redis.MinRetryBackoff,
		MaxRetryBackoff: cacheCfg.Redis.MaxRetryBackoff,
		LoadTimeout:     cacheCfg.LoadTimeout,
	}

	log.Info().
		Str("key", key).
		Str("host", cacheCfg.Redis.Host).
		Int("port", cacheCfg.Redis.Port).
		Int("database", cacheCfg.Redis.Database).
		Int("pool_size", cacheCfg.Redis.PoolSize).
		Msg("Creating Redis cache instance")

	// Note: redis.NewClient() does not accept context parameter. It creates its own
	// 5-second timeout context for the initial PING validation during connection.
	client, err := redis.NewClient(redisCfg)
	if err != nil {
		log.Error().
			Err(err).
			Str("key", key).
			Str("host", cacheCfg.Redis.Host).
			Int("port", cacheCfg.Redis.Port).
			Int("database", cacheCfg.Redis.Database).
			Msg("Failed to create Redis cache client")
		return nil, err
	}

	log.Debug().
		Str("key", key).
		Str("host", cacheCfg.Redis.Host).
		Int("database", cacheCfg.Redis.Database).
		Msg("Redis cache client created successfully")

	return client, nil
}
