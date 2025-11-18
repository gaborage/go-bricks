package cache

import "time"

// Cache Manager Default Configuration
//
// These constants define default values for the CacheManager lifecycle settings.
// They are used both in production code and tests to ensure consistency.

const (
	// DefaultCacheIdleTTL is the default idle timeout for cached instances.
	// After this period of inactivity, a cache will be closed and evicted.
	// Used in manager.go and manager_test.go (10+ occurrences).
	DefaultCacheIdleTTL = 15 * time.Minute

	// DefaultCleanupInterval is the default frequency for running idle cleanup checks.
	// The cleanup goroutine wakes up at this interval to check for expired caches.
	// Used in manager.go (5+ occurrences).
	DefaultCleanupInterval = 5 * time.Minute

	// DefaultMaxSize is the default maximum number of tenant cache instances.
	// When exceeded, the oldest (LRU) cache is evicted.
	// Zero means unlimited caches.
	DefaultMaxSize = 100
)

// Test-Specific Time Durations
//
// These constants are used exclusively in test files to simulate timing behaviors
// without hardcoding magic numbers.

const (
	// TestSlowCreationDelay simulates slow cache creation in concurrency tests.
	// Used in manager_test.go to test race conditions and singleflight behavior.
	TestSlowCreationDelay = 50 * time.Millisecond

	// TestCleanupWaitPeriod is the wait time for cleanup goroutine to execute.
	// Used in manager_test.go to verify idle cache eviction (3+ occurrences).
	TestCleanupWaitPeriod = 300 * time.Millisecond

	// TestShortTTL is a very short TTL for testing expiration behavior.
	// Used in integration tests to verify cache entries expire correctly.
	TestShortTTL = 100 * time.Millisecond

	// TestMediumTTL is a moderate TTL for test data that should persist during test execution.
	TestMediumTTL = 5 * time.Second

	// TestLongTTL is a long TTL for test data that should not expire during tests.
	TestLongTTL = 10 * time.Minute
)
