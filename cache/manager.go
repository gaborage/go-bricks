package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/resourcepool"
)

// ConfigProvider provides per-key cache configurations.
// This interface abstracts where tenant-specific cache configs come from.
type ConfigProvider interface {
	// CacheConfig returns the cache configuration for the given key.
	// For single-tenant apps, key will be "". For multi-tenant, key will be the tenant ID.
	CacheConfig(ctx context.Context, key string) (*config.CacheConfig, error)
}

// ManagerStats provides metrics about the cache manager's state.
type ManagerStats struct {
	ActiveCaches int   // Current number of active cache instances
	TotalCreated int   // Total caches created since manager start
	Evictions    int   // Total evictions due to LRU policy
	IdleCleanups int   // Total cleanups due to idle timeout
	Errors       int   // Total initialization and close errors
	MaxSize      int   // Maximum allowed active caches
	IdleTTL      int64 // Idle timeout duration in seconds
}

// Connector is a function that creates a new cache instance for a given key.
// This abstraction allows dependency injection for testing.
type Connector func(ctx context.Context, key string) (Cache, error)

// ReleaseFunc releases a lease obtained from Get. Callers must invoke it (typically
// deferred) when finished with the cache for the current unit of work. It is idempotent.
// Release does NOT close the shared cache instance; it signals this borrower is done, so
// a cache instance evicted while leased is closed only once its last lease is released.
// See ADR-032.
type ReleaseFunc func()

// CacheManager manages per-tenant cache instances. It is a thin adapter over
// internal/resourcepool.Pool, which owns the ADR-032 lease/evict/close protocol (seed leases,
// LRU eviction, idle cleanup, and the closed-pool guard).
//
// The zero value is safe to call but unusable: rather than dereferencing the nil pool, Get
// and Remove return ErrManagerClosed, Close reports success (there is nothing to close), and
// Stats returns empty statistics — matching database.DbManager and messaging.Manager. That is
// a panic guard, not a test fixture: a zero-value manager serves nothing, so a test built on
// one only ever exercises the caller's no-cache branch. Construct a real manager over
// cache/testing's MockCache to exercise cache behavior. The guard covers a zero-value manager
// only — a nil *CacheManager still panics, so a caller holding one from a failed construction
// must nil-check it.
//
// Example usage. The named result is load-bearing: the deferred closure reports a
// Close failure through it, and with an unnamed result that assignment is lost.
//
//	func withCache(ctx context.Context, tenantID string, connector cache.Connector) (err error) {
//	    manager, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
//	    if err != nil {
//	        return err
//	    }
//	    defer func() {
//	        if cerr := manager.Close(); cerr != nil && err == nil {
//	            err = cerr
//	        }
//	    }()
//
//	    // Get cache for key (auto-creates if needed); release the lease when done
//	    inst, release, err := manager.Get(ctx, tenantID)
//	    if err != nil {
//	        return err
//	    }
//	    defer release()
//
//	    _, err = inst.Get(ctx, "user:123")
//	    return err
//	}
//
//nolint:revive // CacheManager mirrors the database.DbManager / messaging.Manager naming
type CacheManager struct {
	pool      *resourcepool.Pool[Cache]
	connector Connector
}

// ManagerConfig configures the cache manager's behavior.
type ManagerConfig struct {
	MaxSize         int           // Maximum number of active cache instances (0 = unlimited)
	IdleTTL         time.Duration // Idle timeout for cache instances (0 = never timeout)
	CleanupInterval time.Duration // How often to run idle cleanup (default: 5m)
}

// DefaultManagerConfig returns sensible defaults for the cache manager.
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		MaxSize:         100,
		IdleTTL:         15 * time.Minute,
		CleanupInterval: 5 * time.Minute,
	}
}

// NewCacheManager creates a new cache manager with the given configuration.
// The connector function is called to create new cache instances on demand.
func NewCacheManager(cfg ManagerConfig, connector Connector) (*CacheManager, error) {
	if connector == nil {
		return nil, errors.New("connector function is required")
	}

	if cfg.MaxSize < 0 {
		return nil, errors.New("maxsize cannot be negative")
	}

	if cfg.IdleTTL < 0 {
		return nil, errors.New("idlettl cannot be negative")
	}

	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 5 * time.Minute
	}

	pool := resourcepool.New[Cache](cfg.MaxSize, cfg.IdleTTL, func(c Cache) error {
		return c.Close()
	})

	m := &CacheManager{
		pool:      pool,
		connector: connector,
	}

	// Start the background cleanup goroutine if an idle TTL is configured.
	if cfg.IdleTTL > 0 {
		pool.StartCleanup(cfg.CleanupInterval)
	}

	return m, nil
}

// Get retrieves or creates a cache instance for the given key, plus a ReleaseFunc the
// caller must invoke when finished with it for the current unit of work (typically
// deferred). Returns ErrManagerClosed if Close() has been called, or if the manager is a
// zero value that was never built via NewCacheManager. The lease prevents a cache instance
// evicted while in use from being closed under an active caller (the #606 race). On error
// the returned ReleaseFunc is nil — check err first.
func (m *CacheManager) Get(ctx context.Context, key string) (Cache, ReleaseFunc, error) {
	if m.pool == nil {
		// Zero-value manager (never built via NewCacheManager): unusable, fail closed rather
		// than panic — consistent with the Remove()/Close()/Stats() zero-value guards.
		return nil, nil, ErrManagerClosed
	}
	value, release, err := m.pool.GetOrCreate(ctx, key, func(ctx context.Context) (Cache, error) {
		inst, cerr := m.connector(ctx, key)
		if cerr != nil {
			return nil, fmt.Errorf("failed to create cache for key %q: %w", key, cerr)
		}
		return inst, nil
	})
	if err != nil {
		if errors.Is(err, resourcepool.ErrPoolClosed) {
			return nil, nil, ErrManagerClosed
		}
		return nil, nil, err
	}
	return value, ReleaseFunc(release), nil
}

// Remove explicitly removes a cache instance from the manager.
// Returns ErrManagerClosed if Close() has been called, or if the manager is a zero value
// that was never built via NewCacheManager.
func (m *CacheManager) Remove(key string) error {
	// Unusable either way: never built via NewCacheManager, or already closed. The nil test
	// must come first — Pool.Closed() takes the pool's mutex and would panic on a nil pool.
	if m.pool == nil || m.pool.Closed() {
		return ErrManagerClosed
	}

	// A still-leased instance is detached now but its Close() is deferred to the final lease
	// release, so an in-use cache is never closed (the #606 race); the pool reports shouldClose
	// false in that case.
	inst, shouldClose := m.pool.Remove(key)
	if !shouldClose {
		return nil
	}

	if err := inst.Close(); err != nil {
		return fmt.Errorf("failed to close cache %q: %w", key, err)
	}
	return nil
}

// Close shuts down all managed cache instances and stops the cleanup goroutine.
// After Close() returns, subsequent calls to Get() and Remove() return ErrManagerClosed.
// Closing a zero-value manager is a no-op.
func (m *CacheManager) Close() error {
	if m.pool == nil {
		return nil // zero-value manager: nothing to close
	}
	return m.pool.Close()
}

// Stats returns current manager statistics.
func (m *CacheManager) Stats() ManagerStats {
	if m.pool == nil {
		return ManagerStats{} // zero-value manager: empty stats
	}
	ps := m.pool.Stats()
	return ManagerStats{
		ActiveCaches: ps.Size,
		TotalCreated: ps.TotalCreated,
		Evictions:    ps.Evictions,
		IdleCleanups: ps.IdleCleanups,
		Errors:       ps.Errors,
		MaxSize:      ps.MaxSize,
		IdleTTL:      int64(ps.IdleTTL.Seconds()),
	}
}
