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
//	// Distributed locking — release with the same token, never a bare Delete
//	token := []byte("worker-1")
//	acquired, err := cache.CompareAndSet(ctx, "lock:job:789", nil, token, 1*time.Minute)
//	released, err := cache.CompareAndDelete(ctx, "lock:job:789", token)
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
	//	token := []byte("worker-1")
	//	acquired, err := cache.CompareAndSet(ctx, lockKey, nil, token, 30*time.Second)
	//	if err != nil || !acquired {
	//	    return ErrLockHeld
	//	}
	//	defer func() { _, _ = cache.CompareAndDelete(ctx, lockKey, token) }()
	//
	// Acquire with a positive TTL whenever the release goes through CompareAndDelete: a
	// lock taken with ttl 0 never expires, so a token-verified release that returns false
	// leaves the key held with nothing to recover it. Keep the TTL above worst-case work
	// duration and make the guarded work idempotent — the TTL is still what bounds a
	// crashed holder, so this is contention reduction, not guaranteed mutual exclusion.
	CompareAndSet(ctx context.Context, key string, expectedValue, newValue []byte, ttl time.Duration) (success bool, err error)

	// CompareAndDelete atomically removes the key only if its current value equals
	// expectedValue. It is the safe release for a lock acquired via CompareAndSet: Delete
	// removes whoever holds the key now, this removes it only if the caller still does.
	//
	// expectedValue must be non-nil. Unlike CompareAndSet, nil has no acquire-if-absent
	// counterpart here, and unconditional removal is already Delete — a nil expectedValue
	// returns ErrNilExpectedValue without contacting the cache. An empty slice is a real
	// comparison against the empty string, exactly as CompareAndSet treats it.
	//
	// deleted reports only that this call removed the key. A false result covers both
	// "the stored value was not ours" and "the key was already gone", so it never proves
	// that another holder's value is still present.
	//
	// Both a false result and any error are terminal. Never fall back to Delete and never
	// retry the release with it — that reinstates the hazard this method exists to remove,
	// now hidden behind an API that reads as safe. A false result authorizes stopping, not
	// compensating: a client-side timeout on a delete that actually landed is
	// indistinguishable from another holder owning the key.
	CompareAndDelete(ctx context.Context, key string, expectedValue []byte) (deleted bool, err error)

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
