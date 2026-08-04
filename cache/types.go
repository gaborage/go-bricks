// Package cache provides interfaces and types for caching implementations.
// It follows the same patterns as the database package with vendor-agnostic
// interfaces and multi-tenant support via CacheManager.
package cache

import (
	"context"
	"time"
)

// Cache defines the core interface for cache operations.
// All implementations must be thread-safe and context-aware.
//
// Example usage:
//
//	cache, release, err := cacheManager.Get(ctx, tenantID)
//	if err != nil {
//	    return err
//	}
//	defer release()
//
//	// Basic operations
//	err = cache.Set(ctx, "user:123", userData, 5*time.Minute)
//	data, err := cache.Get(ctx, "user:123")
//
//	// Deduplication
//	stored, wasSet, err := cache.GetOrSet(ctx, "lock:task:456", []byte("processing"), 30*time.Second)
//
//	// Distributed locking
//	acquired, err := cache.CompareAndSet(ctx, "lock:job:789", nil, []byte("worker-1"), 1*time.Minute)
type Cache interface {
	// Get retrieves a value from the cache by key.
	// Returns ErrNotFound if the key doesn't exist or has expired.
	Get(ctx context.Context, key string) ([]byte, error)

	// Set stores a value in the cache with the specified TTL.
	// If ttl is 0, the value is stored without expiration (use with caution).
	// Overwrites existing values.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error

	// Delete removes a value from the cache.
	// Returns nil if the key doesn't exist (idempotent operation).
	Delete(ctx context.Context, key string) error

	// GetOrSet atomically retrieves a value if it exists, or sets it if it doesn't.
	// Returns:
	//   - storedValue: the value currently stored in cache (either existing or newly set)
	//   - wasSet: true if the value was newly set, false if it already existed
	//   - error: any error that occurred
	//
	// This is useful for deduplication and idempotency patterns.
	//
	// Example (message deduplication):
	//
	//	stored, wasSet, err := cache.GetOrSet(ctx, "processed:msg:"+msgID, []byte("done"), 1*time.Hour)
	//	if !wasSet {
	//	    // Message already processed
	//	    return nil
	//	}
	//	// Process message for the first time
	GetOrSet(ctx context.Context, key string, value []byte, ttl time.Duration) (storedValue []byte, wasSet bool, err error)

	// CompareAndSet performs an atomic compare-and-set operation.
	// Sets the key to newValue only if the current value equals expectedValue.
	// Pass nil for expectedValue to set only if the key doesn't exist (SET NX semantics).
	// Any non-nil expectedValue — including an empty slice — is a real comparison, so an
	// absent key returns false rather than being acquired.
	// Returns true if the value was set, false otherwise.
	//
	// This is useful for distributed locking and optimistic concurrency control.
	//
	// Example (distributed lock):
	//
	//	acquired, err := cache.CompareAndSet(ctx, lockKey, nil, []byte("worker-1"), 30*time.Second)
	//	if !acquired {
	//	    return ErrLockHeld
	//	}
	//	defer cache.Delete(ctx, lockKey)
	//
	// Delete releases unconditionally: if the work outruns the TTL, the lock expires,
	// another holder acquires it, and this Delete clears theirs. There is no
	// compare-and-delete on this interface, so keep the TTL above worst-case work
	// duration and make the guarded work idempotent — this is contention reduction,
	// not guaranteed mutual exclusion.
	CompareAndSet(ctx context.Context, key string, expectedValue, newValue []byte, ttl time.Duration) (success bool, err error)

	// Health checks the health of the cache connection.
	// Should be fast (<100ms) and safe to call frequently.
	Health(ctx context.Context) error

	// Stats returns cache statistics for observability.
	// Keys may include: connections_active, hits, misses, evictions, memory_used, etc.
	// The specific keys depend on the cache implementation.
	Stats() (map[string]any, error)

	// Close closes the cache connection and releases resources.
	// After calling Close, the cache instance should not be used.
	Close() error
}
