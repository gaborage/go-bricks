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
//	    WithGetFailure(cache.ErrConnectionError).
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
//	    GetCache: func(ctx context.Context) (cache.Cache, error) {
//	        tenantID := multitenant.GetTenant(ctx)
//	        return tenantCaches[tenantID], nil
//	    },
//	}
//
// For integration tests requiring actual Redis behavior, use testcontainers
// with cache/redis package instead.
package testing
