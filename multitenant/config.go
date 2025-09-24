package multitenant

import (
	"context"
	"sync"
	"time"

	"github.com/gaborage/go-bricks/config"
	"golang.org/x/sync/singleflight"
)

// TenantConfigProvider fetches tenant specific configuration from an external source.
type TenantConfigProvider interface {
	GetDatabase(ctx context.Context, tenantID string) (*config.DatabaseConfig, error)
	GetMessaging(ctx context.Context, tenantID string) (*TenantMessagingConfig, error)
}

// CacheOption configures the tenant config cache.
type CacheOption func(*cacheConfig)

type cacheConfig struct {
	ttl              time.Duration
	maxSize          int
	staleGracePeriod time.Duration // How long to serve stale data on provider errors
}

// cacheEntry holds cached data with expiration and staleness tracking
type cacheEntry struct {
	dbConfig     *config.DatabaseConfig
	msgConfig    *TenantMessagingConfig
	fetchedAt    time.Time
	lastAccessAt time.Time
}

func (e *cacheEntry) isExpired(ttl time.Duration) bool {
	return time.Since(e.fetchedAt) > ttl
}

func (e *cacheEntry) isStale(gracePeriod time.Duration) bool {
	return time.Since(e.fetchedAt) > gracePeriod
}

// TenantConfigCache caches tenant configurations to avoid repeated provider calls.
type TenantConfigCache struct {
	provider TenantConfigProvider
	cfg      cacheConfig

	mu      sync.RWMutex
	entries map[string]*cacheEntry
	sf      singleflight.Group
}

// NewTenantConfigCache creates a new cache using the provided options.
func NewTenantConfigCache(provider TenantConfigProvider, opts ...CacheOption) *TenantConfigCache {
	cfg := cacheConfig{
		ttl:              5 * time.Minute,
		maxSize:          100,
		staleGracePeriod: 30 * time.Minute, // Serve stale data for up to 30 minutes on provider errors
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &TenantConfigCache{
		provider: provider,
		cfg:      cfg,
		entries:  make(map[string]*cacheEntry),
	}
}

// GetDatabase returns the cached or freshly fetched database configuration for the tenant.
func (c *TenantConfigCache) GetDatabase(ctx context.Context, tenantID string) (*config.DatabaseConfig, error) {
	if c == nil || c.provider == nil {
		return nil, ErrTenantNotFound
	}

	// Check cache first
	c.mu.RLock()
	entry, exists := c.entries[tenantID]
	c.mu.RUnlock()

	// If cached and not expired, return immediately
	if exists && !entry.isExpired(c.cfg.ttl) {
		c.touchEntry(tenantID)
		return entry.dbConfig, nil
	}

	// If expired or not exists, fetch with singleflight protection
	key := "db:" + tenantID
	result, err, _ := c.sf.Do(key, func() (interface{}, error) {
		return c.fetchAndCacheDB(ctx, tenantID)
	})

	if err != nil {
		// On error, try to serve stale data if available and not too old
		if exists && !entry.isStale(c.cfg.staleGracePeriod) {
			return entry.dbConfig, nil
		}
		return nil, err
	}

	return result.(*config.DatabaseConfig), nil
}

// GetMessaging returns the cached or freshly fetched messaging configuration for the tenant.
func (c *TenantConfigCache) GetMessaging(ctx context.Context, tenantID string) (*TenantMessagingConfig, error) {
	if c == nil || c.provider == nil {
		return nil, ErrTenantNotFound
	}

	// Check cache first
	c.mu.RLock()
	entry, exists := c.entries[tenantID]
	c.mu.RUnlock()

	// If cached and not expired, return immediately
	if exists && !entry.isExpired(c.cfg.ttl) {
		c.touchEntry(tenantID)
		return entry.msgConfig, nil
	}

	// If expired or not exists, fetch with singleflight protection
	key := "msg:" + tenantID
	result, err, _ := c.sf.Do(key, func() (interface{}, error) {
		return c.fetchAndCacheMsg(ctx, tenantID)
	})

	if err != nil {
		// On error, try to serve stale data if available and not too old
		if exists && !entry.isStale(c.cfg.staleGracePeriod) {
			return entry.msgConfig, nil
		}
		return nil, err
	}

	return result.(*TenantMessagingConfig), nil
}

// fetchAndCacheDB fetches database config and updates cache
func (c *TenantConfigCache) fetchAndCacheDB(ctx context.Context, tenantID string) (*config.DatabaseConfig, error) {
	dbConfig, err := c.provider.GetDatabase(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	c.updateCacheEntry(tenantID, dbConfig, nil)
	return dbConfig, nil
}

// fetchAndCacheMsg fetches messaging config and updates cache
func (c *TenantConfigCache) fetchAndCacheMsg(ctx context.Context, tenantID string) (*TenantMessagingConfig, error) {
	msgConfig, err := c.provider.GetMessaging(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	c.updateCacheEntry(tenantID, nil, msgConfig)
	return msgConfig, nil
}

// updateCacheEntry updates the cache entry with new configuration data
func (c *TenantConfigCache) updateCacheEntry(tenantID string, dbConfig *config.DatabaseConfig, msgConfig *TenantMessagingConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict old entries if cache is full
	if len(c.entries) >= c.cfg.maxSize {
		c.evictOldest()
	}

	// Update or create entry
	now := time.Now()
	if entry, exists := c.entries[tenantID]; exists {
		if dbConfig != nil {
			entry.dbConfig = dbConfig
		}
		if msgConfig != nil {
			entry.msgConfig = msgConfig
		}
		entry.fetchedAt = now
		entry.lastAccessAt = now
	} else {
		c.entries[tenantID] = &cacheEntry{
			dbConfig:     dbConfig,
			msgConfig:    msgConfig,
			fetchedAt:    now,
			lastAccessAt: now,
		}
	}
}

func (c *TenantConfigCache) touchEntry(tenantID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, exists := c.entries[tenantID]; exists {
		entry.lastAccessAt = time.Now()
	}
}

// evictOldest removes the least recently accessed entry
func (c *TenantConfigCache) evictOldest() {
	var oldestKey string
	var oldestTime time.Time

	for key, entry := range c.entries {
		if oldestKey == "" || entry.lastAccessAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.lastAccessAt
		}
	}

	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// WithTTL sets the cache TTL.
func WithTTL(ttl time.Duration) CacheOption {
	return func(cfg *cacheConfig) {
		if ttl > 0 {
			cfg.ttl = ttl
		}
	}
}

// WithMaxSize sets the maximum number of cached tenants.
func WithMaxSize(maxSize int) CacheOption {
	return func(cfg *cacheConfig) {
		if maxSize > 0 {
			cfg.maxSize = maxSize
		}
	}
}

// WithStaleGracePeriod sets how long stale data can be served on provider errors.
func WithStaleGracePeriod(period time.Duration) CacheOption {
	return func(cfg *cacheConfig) {
		if period > 0 {
			cfg.staleGracePeriod = period
		}
	}
}
