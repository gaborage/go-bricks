// Package testing provides utilities for testing cache logic in go-bricks applications.
// This package follows the design patterns from database/testing and observability/testing,
// providing fluent APIs and in-memory mocks that eliminate the complexity of setting up
// real Redis instances for unit tests.
//
// The primary type is MockCache, which implements cache.Cache with configurable behavior
// for testing various scenarios including failures, delays, and operation tracking.
//
// # Basic Usage
//
// MockCache can be used directly in tests without any setup:
//
//	mock := testing.NewMockCache()
//	mock.Set(ctx, "key", []byte("value"), time.Minute)
//	data, err := mock.Get(ctx, "key")
//
// # Configurable Behavior
//
// Chain configuration methods to simulate failures or delays:
//
//	mock := testing.NewMockCache().
//	    WithGetFailure(cache.ErrClosed).
//	    WithDelay(100 * time.Millisecond)
//
// # Operation Tracking
//
// Track and assert on cache operations:
//
//	mock := testing.NewMockCache()
//	// ... perform operations ...
//	AssertOperationCount(t, mock, "Get", 5)
//	AssertCacheHit(t, mock, "user:123")
//	AssertCacheMiss(t, mock, "missing:key")
//
// # Multi-Tenant Testing
//
// Use multiple mock instances to test tenant isolation:
//
//	tenantCaches := map[string]*testing.MockCache{
//	    "acme":   testing.NewMockCache(),
//	    "globex": testing.NewMockCache(),
//	}
//
//	deps := &app.ModuleDeps{
//	    Cache: func(ctx context.Context) (cache.Cache, error) {
//	        tenantID, _ := multitenant.GetTenant(ctx)
//	        return tenantCaches[tenantID], nil
//	    },
//	}
//
// # Testing Code That Holds a Cache Manager
//
// cache exposes no manager interface (ADR-045) — substitute at the manager's
// input instead, by handing a real cache.CacheManager a Connector that returns
// MockCache. This exercises the genuine lease, LRU, and idle-cleanup behavior.
// Import this package under an alias here, since a test that names *testing.T
// also needs the standard library's testing:
//
//	cachetesting "github.com/gaborage/go-bricks/cache/testing"
//
//	connector := func(ctx context.Context, key string) (cache.Cache, error) {
//	    return cachetesting.NewMockCache(), nil
//	}
//	manager, err := cache.NewCacheManager(cache.DefaultManagerConfig(), connector)
//	require.NoError(t, err)
//	t.Cleanup(func() { // stops the idle-cleanup goroutine
//	    if err := manager.Close(); err != nil {
//	        t.Errorf("closing cache manager: %v", err)
//	    }
//	})
//
// If your own type takes a manager, declare the narrow interface it needs on
// the consumer side rather than depending on the concrete type. Pin it with a
// compile-time assertion so the pairing is checked even before a call site
// passes a real manager (ADR-045):
//
//	type cacheGetter interface {
//	    Get(ctx context.Context, key string) (cache.Cache, cache.ReleaseFunc, error)
//	}
//
//	var _ cacheGetter = (*cache.CacheManager)(nil)
//
// For integration tests requiring actual Redis behavior, use testcontainers
// with cache/redis package instead.
package testing
