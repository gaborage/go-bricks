package app

import (
	"context"
	"fmt"

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

// MessagingClientFactory returns the appropriate messaging client factory function.
// If no custom factory is provided in options, returns a factory that creates AMQPClient instances.
func (f *FactoryResolver) MessagingClientFactory() messaging.ClientFactory {
	if f.opts != nil && f.opts.MessagingClientFactory != nil {
		return func(url string, log logger.Logger) messaging.AMQPClient {
			return f.opts.MessagingClientFactory(url, log)
		}
	}

	// Default factory function that creates AMQPClient instances
	return func(url string, log logger.Logger) messaging.AMQPClient {
		return messaging.NewAMQPClient(url, log)
	}
}

// CacheConnector returns the appropriate cache connector function.
// If no custom connector is provided in options, returns a Redis connector that
// reads configuration from the resourceSource for the given tenant/key.
func (f *FactoryResolver) CacheConnector(resourceSource TenantStore, log logger.Logger) cache.Connector {
	if f.opts != nil && f.opts.CacheConnector != nil {
		return f.opts.CacheConnector
	}

	// Default connector creates Redis cache instances from config
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

		// Get cache configuration for this tenant/key
		cacheCfg, err := resourceSource.CacheConfig(ctx, key)
		if err != nil {
			log.Debug().
				Err(err).
				Str("key", key).
				Msg("Cache config not available")
			return nil, err
		}

		// Defensive validation: ensure cacheCfg is not nil
		if cacheCfg == nil {
			err := config.NewValidationError("cache", fmt.Sprintf("configuration is nil for key '%s'", key))
			log.Error().
				Str("key", key).
				Msg("Cache configuration unexpectedly nil")
			return nil, err
		}

		// Defensive validation: ensure cache is enabled
		if !cacheCfg.Enabled {
			err := config.NewNotConfiguredError("cache", "CACHE_ENABLED", "cache.enabled")
			log.Error().
				Str("key", key).
				Msg("Cache configuration has Enabled=false")
			return nil, err
		}

		// Validate cache type is "redis" (or empty for backward compatibility)
		if cacheCfg.Type != "" && cacheCfg.Type != "redis" {
			err := config.NewInvalidFieldError("cache.type",
				fmt.Sprintf("unsupported type '%s'", cacheCfg.Type),
				[]string{"redis"})
			log.Error().
				Str("key", key).
				Str("type", cacheCfg.Type).
				Msg("Invalid cache type - only 'redis' is supported")
			return nil, err
		}

		// Validate Redis configuration is properly set
		if cacheCfg.Redis.Host == "" {
			err := config.NewMissingFieldError("cache.redis.host", "CACHE_REDIS_HOST", "cache.redis.host")
			log.Error().
				Str("key", key).
				Msg("Redis host is empty - cannot create cache instance")
			return nil, err
		}

		// Create Redis configuration from cache config
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
		}

		log.Info().
			Str("key", key).
			Str("host", cacheCfg.Redis.Host).
			Int("port", cacheCfg.Redis.Port).
			Int("database", cacheCfg.Redis.Database).
			Int("pool_size", cacheCfg.Redis.PoolSize).
			Msg("Creating Redis cache instance")

		// Create Redis cache instance
		// Note: redis.NewClient() does not accept context parameter. It creates its own
		// 5-second timeout context for the initial PING validation during connection.
		return redis.NewClient(redisCfg)
	}
}
