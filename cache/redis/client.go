package redis

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gaborage/go-bricks/cache"
	"github.com/gaborage/go-bricks/cache/internal/tracking"
	"github.com/gaborage/go-bricks/multitenant"
)

// Lua script for atomic Compare-And-Set operation.
// Returns 1 if successful, 0 if comparison failed.
const casScript = `
local key = KEYS[1]
local expected = ARGV[1]
local new_value = ARGV[2]
local ttl_ms = tonumber(ARGV[3])

local current = redis.call('GET', key)

-- If expected is empty string, only set if key doesn't exist (SET NX semantics)
if expected == "" then
	if current == false then
		if ttl_ms == 0 then
			redis.call('SET', key, new_value)
		else
			redis.call('SET', key, new_value, 'PX', ttl_ms)
		end
		return 1
	end
	return 0
end

-- Normal CAS: compare current value with expected
if current == expected then
	if ttl_ms == 0 then
		redis.call('SET', key, new_value)
	else
		redis.call('SET', key, new_value, 'PX', ttl_ms)
	end
	return 1
end

return 0
`

// Client implements the cache.Cache interface using Redis as the backend.
type Client struct {
	client *redis.Client
	config *Config
	closed atomic.Bool
}

// namespace returns the tenant identifier for metric attribution.
func (c *Client) namespace(ctx context.Context) string {
	if tenantID, ok := multitenant.GetTenant(ctx); ok {
		return tenantID
	}
	return ""
}

// NewClient creates a new Redis cache client.
// Validates configuration and establishes connection.
func NewClient(cfg *Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	opts := &redis.Options{
		Addr:            cfg.Address(),
		Password:        cfg.Password,
		DB:              cfg.Database,
		PoolSize:        cfg.PoolSize,
		DialTimeout:     cfg.DialTimeout,
		ReadTimeout:     cfg.ReadTimeout,
		WriteTimeout:    cfg.WriteTimeout,
		MaxRetries:      cfg.MaxRetries,
		MinRetryBackoff: cfg.MinRetryBackoff,
		MaxRetryBackoff: cfg.MaxRetryBackoff,
	}

	client := redis.NewClient(opts)

	// Test connection with PING
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, cache.NewConnectionError("ping", cfg.Address(), err)
	}

	return &Client{
		client: client,
		config: cfg,
		// closed defaults to false (atomic.Bool zero value)
	}, nil
}

// Get retrieves a value from the cache.
// Returns cache.ErrNotFound if the key doesn't exist.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	if c.closed.Load() {
		return nil, cache.ErrClosed
	}

	start := time.Now()
	result, err := c.client.Get(ctx, key).Bytes()
	duration := time.Since(start)

	// Determine hit/miss status
	hit := err == nil
	var recordErr error
	if err != nil {
		if errors.Is(err, redis.Nil) {
			// Cache miss - not an error, just not found
			tracking.RecordCacheOperation(ctx, tracking.OpGet, duration, false, nil, c.namespace(ctx))
			return nil, cache.ErrNotFound
		}
		recordErr = err
	}

	tracking.RecordCacheOperation(ctx, tracking.OpGet, duration, hit, recordErr, c.namespace(ctx))

	if err != nil {
		return nil, cache.NewOperationError("get", key, err)
	}

	return result, nil
}

// Set stores a value in the cache with the specified TTL.
// TTL of 0 means no expiration (use with caution).
// Returns cache.ErrInvalidTTL if TTL is negative.
func (c *Client) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if c.closed.Load() {
		return cache.ErrClosed
	}

	if ttl < 0 {
		return cache.ErrInvalidTTL
	}

	start := time.Now()
	err := c.client.Set(ctx, key, value, ttl).Err()
	duration := time.Since(start)

	tracking.RecordCacheOperation(ctx, tracking.OpSet, duration, false, err, c.namespace(ctx))

	if err != nil {
		return cache.NewOperationError("set", key, err)
	}

	return nil
}

// Delete removes a key from the cache.
// Does not return error if key doesn't exist.
func (c *Client) Delete(ctx context.Context, key string) error {
	if c.closed.Load() {
		return cache.ErrClosed
	}

	start := time.Now()
	err := c.client.Del(ctx, key).Err()
	duration := time.Since(start)

	tracking.RecordCacheOperation(ctx, tracking.OpDelete, duration, false, err, c.namespace(ctx))

	if err != nil {
		return cache.NewOperationError("delete", key, err)
	}

	return nil
}

// GetOrSet atomically gets an existing value or sets a new one.
// Returns (storedValue, wasSet, error):
//   - wasSet=true: Value was newly set (first-time processing)
//   - wasSet=false: Value already existed (duplicate detected)
//   - storedValue: Always returns the value in cache (current or newly set)
//
// Uses Redis SET NX GET for atomicity.
func (c *Client) GetOrSet(ctx context.Context, key string, value []byte, ttl time.Duration) (storedValue []byte, wasSet bool, err error) {
	if c.closed.Load() {
		return nil, false, cache.ErrClosed
	}

	if ttl < 0 {
		return nil, false, cache.ErrInvalidTTL
	}

	start := time.Now()

	// SET key value NX GET PX ttl_ms
	// Returns nil if key was set (didn't exist before)
	// Returns old value if key already existed
	result, err := c.client.SetArgs(ctx, key, value, redis.SetArgs{
		Mode: "NX", // Only set if key doesn't exist
		Get:  true, // Return old value if key existed
		TTL:  ttl,
	}).Result()

	duration := time.Since(start)

	if err != nil {
		// redis.Nil means key was successfully set (didn't exist before)
		if errors.Is(err, redis.Nil) {
			tracking.RecordCacheOperation(ctx, tracking.OpGetOrSet, duration, false, nil, c.namespace(ctx))
			return value, true, nil
		}
		tracking.RecordCacheOperation(ctx, tracking.OpGetOrSet, duration, false, err, c.namespace(ctx))
		return nil, false, cache.NewOperationError("getorset", key, err)
	}

	// Key already existed, return the existing value
	tracking.RecordCacheOperation(ctx, tracking.OpGetOrSet, duration, true, nil, c.namespace(ctx))
	return []byte(result), false, nil
}

// CompareAndSet atomically compares and swaps a value.
// Returns (success, error):
//   - success=true: Value was updated (comparison matched)
//   - success=false: Value was NOT updated (comparison failed)
//
// Special case: expectedValue=nil means "set only if key doesn't exist" (acquire lock).
// Uses Lua script for atomicity.
func (c *Client) CompareAndSet(ctx context.Context, key string, expectedValue, newValue []byte, ttl time.Duration) (bool, error) {
	if c.closed.Load() {
		return false, cache.ErrClosed
	}

	if ttl < 0 {
		return false, cache.ErrInvalidTTL
	}

	// Convert nil expectedValue to empty string for Lua script
	expected := ""
	if expectedValue != nil {
		expected = string(expectedValue)
	}

	start := time.Now()

	// Execute Lua script
	result, err := c.client.Eval(ctx, casScript, []string{key}, expected, newValue, ttl.Milliseconds()).Int()

	duration := time.Since(start)
	tracking.RecordCacheOperation(ctx, tracking.OpCompareAndSet, duration, false, err, c.namespace(ctx))

	if err != nil {
		return false, cache.NewOperationError("cas", key, err)
	}

	return result == 1, nil
}

// Health checks if the Redis connection is healthy.
// Uses PING command to verify connectivity.
func (c *Client) Health(ctx context.Context) error {
	if c.closed.Load() {
		return cache.ErrClosed
	}

	start := time.Now()
	err := c.client.Ping(ctx).Err()
	duration := time.Since(start)

	tracking.RecordCacheOperation(ctx, tracking.OpHealth, duration, false, err, c.namespace(ctx))

	if err != nil {
		return cache.NewConnectionError("ping", c.config.Address(), err)
	}

	return nil
}

// Stats returns Redis server statistics.
// Includes metrics from Redis INFO command.
func (c *Client) Stats() (map[string]any, error) {
	if c.closed.Load() {
		return nil, cache.ErrClosed
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	info, err := c.client.Info(ctx).Result()
	if err != nil {
		return nil, cache.NewOperationError("stats", "INFO", err)
	}

	poolStats := c.client.PoolStats()

	return map[string]any{
		"redis_info":       info,
		"pool_hits":        poolStats.Hits,
		"pool_misses":      poolStats.Misses,
		"pool_timeouts":    poolStats.Timeouts,
		"pool_total_conns": poolStats.TotalConns,
		"pool_idle_conns":  poolStats.IdleConns,
		"pool_stale_conns": poolStats.StaleConns,
	}, nil
}

// Close closes the Redis client and releases resources.
// After calling Close, the client should not be used.
// Close is idempotent - calling it multiple times is safe.
func (c *Client) Close() error {
	// Use CompareAndSwap for idempotent close (only close once)
	if !c.closed.CompareAndSwap(false, true) {
		// Already closed
		return cache.ErrClosed
	}

	// First close - perform cleanup
	return c.client.Close()
}
