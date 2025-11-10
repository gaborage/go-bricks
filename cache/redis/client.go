package redis

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gaborage/go-bricks/cache"
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
		redis.call('SET', key, new_value, 'PX', ttl_ms)
		return 1
	end
	return 0
end

-- Normal CAS: compare current value with expected
if current == expected then
	redis.call('SET', key, new_value, 'PX', ttl_ms)
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

	result, err := c.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, cache.ErrNotFound
		}
		return nil, cache.NewOperationError("get", key, err)
	}

	return result, nil
}

// Set stores a value in the cache with the specified TTL.
// Returns cache.ErrInvalidTTL if TTL is negative or zero.
func (c *Client) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if c.closed.Load() {
		return cache.ErrClosed
	}

	if ttl <= 0 {
		return cache.ErrInvalidTTL
	}

	if err := c.client.Set(ctx, key, value, ttl).Err(); err != nil {
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

	if err := c.client.Del(ctx, key).Err(); err != nil {
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

	if ttl <= 0 {
		return nil, false, cache.ErrInvalidTTL
	}

	// SET key value NX GET PX ttl_ms
	// Returns nil if key was set (didn't exist before)
	// Returns old value if key already existed
	result, err := c.client.SetArgs(ctx, key, value, redis.SetArgs{
		Mode: "NX", // Only set if key doesn't exist
		Get:  true, // Return old value if key existed
		TTL:  ttl,
	}).Result()

	if err != nil {
		// redis.Nil means key was successfully set (didn't exist before)
		if errors.Is(err, redis.Nil) {
			return value, true, nil
		}
		return nil, false, cache.NewOperationError("getorset", key, err)
	}

	// Key already existed, return the existing value
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

	if ttl <= 0 {
		return false, cache.ErrInvalidTTL
	}

	// Convert nil expectedValue to empty string for Lua script
	expected := ""
	if expectedValue != nil {
		expected = string(expectedValue)
	}

	// Execute Lua script
	result, err := c.client.Eval(ctx, casScript, []string{key}, expected, newValue, ttl.Milliseconds()).Int()
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

	if err := c.client.Ping(ctx).Err(); err != nil {
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
