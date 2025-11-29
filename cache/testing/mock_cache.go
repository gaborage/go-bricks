package testing

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gaborage/go-bricks/cache"
)

// MockCache is an in-memory cache implementation for testing.
// It implements cache.Cache with configurable behavior for simulating failures and delays.
//
// MockCache is thread-safe and tracks all operations for assertion purposes.
//
// Example usage:
//
//	mock := NewMockCache()
//	mock.Set(ctx, "key", []byte("value"), time.Minute)
//	data, err := mock.Get(ctx, "key")
type MockCache struct {
	id string

	// Storage
	data   sync.Map // key: string, value: *cacheEntry
	closed atomic.Bool

	// Configurable behavior
	delay              time.Duration
	getError           error
	setError           error
	deleteError        error
	getOrSetError      error
	compareAndSetError error
	healthError        error
	statsError         error
	closeError         error

	// Operation tracking
	getCalls           atomic.Int64
	setCalls           atomic.Int64
	deleteCalls        atomic.Int64
	getOrSetCalls      atomic.Int64
	compareAndSetCalls atomic.Int64
	healthCalls        atomic.Int64
	statsCalls         atomic.Int64
	closeCalls         atomic.Int64

	// Close callback (for tracking in tests)
	onClose func(string)
}

// cacheEntry represents a stored value with expiration.
type cacheEntry struct {
	value      []byte
	expiration time.Time
}

// NewMockCache creates a new MockCache with default behavior.
func NewMockCache() *MockCache {
	return &MockCache{
		id: "mock",
	}
}

// NewMockCacheWithID creates a new MockCache with a specific ID.
// Useful for multi-tenant testing or tracking multiple cache instances.
func NewMockCacheWithID(id string) *MockCache {
	return &MockCache{
		id: id,
	}
}

// Configuration methods (fluent API)

// WithDelay configures a delay for all operations.
// Useful for testing timeout behavior.
func (m *MockCache) WithDelay(delay time.Duration) *MockCache {
	m.delay = delay
	return m
}

// WithGetFailure configures Get operations to return an error.
func (m *MockCache) WithGetFailure(err error) *MockCache {
	m.getError = err
	return m
}

// WithSetFailure configures Set operations to return an error.
func (m *MockCache) WithSetFailure(err error) *MockCache {
	m.setError = err
	return m
}

// WithDeleteFailure configures Delete operations to return an error.
func (m *MockCache) WithDeleteFailure(err error) *MockCache {
	m.deleteError = err
	return m
}

// WithGetOrSetFailure configures GetOrSet operations to return an error.
func (m *MockCache) WithGetOrSetFailure(err error) *MockCache {
	m.getOrSetError = err
	return m
}

// WithCompareAndSetFailure configures CompareAndSet operations to return an error.
func (m *MockCache) WithCompareAndSetFailure(err error) *MockCache {
	m.compareAndSetError = err
	return m
}

// WithHealthFailure configures Health operations to return an error.
func (m *MockCache) WithHealthFailure(err error) *MockCache {
	m.healthError = err
	return m
}

// WithStatsFailure configures Stats operations to return an error.
func (m *MockCache) WithStatsFailure(err error) *MockCache {
	m.statsError = err
	return m
}

// WithCloseFailure configures Close operations to return an error.
func (m *MockCache) WithCloseFailure(err error) *MockCache {
	m.closeError = err
	return m
}

// WithCloseCallback registers a callback that gets called when Close() succeeds.
// Useful for tracking cache lifecycle in tests.
func (m *MockCache) WithCloseCallback(callback func(string)) *MockCache {
	m.onClose = callback
	return m
}

// Cache interface implementation

// Get retrieves a value from the cache.
func (m *MockCache) Get(ctx context.Context, key string) ([]byte, error) {
	m.getCalls.Add(1)

	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if m.closed.Load() {
		return nil, cache.ErrClosed
	}

	if m.getError != nil {
		return nil, m.getError
	}

	val, ok := m.data.Load(key)
	if !ok {
		return nil, cache.ErrNotFound
	}

	entry := val.(*cacheEntry)

	// Check expiration
	if time.Now().After(entry.expiration) {
		m.data.Delete(key)
		return nil, cache.ErrNotFound
	}

	return entry.value, nil
}

// Set stores a value in the cache with TTL.
func (m *MockCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.setCalls.Add(1)

	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if m.closed.Load() {
		return cache.ErrClosed
	}

	if m.setError != nil {
		return m.setError
	}

	if ttl < 0 {
		return cache.ErrInvalidTTL
	}

	// Handle TTL=0 as "no expiration" (100 years)
	expiration := time.Now().Add(ttl)
	if ttl == 0 {
		expiration = time.Now().Add(100 * 365 * 24 * time.Hour)
	}

	entry := &cacheEntry{
		value:      value,
		expiration: expiration,
	}

	m.data.Store(key, entry)
	return nil
}

// GetOrSet atomically gets a value or sets it if not present.
func (m *MockCache) GetOrSet(ctx context.Context, key string, value []byte, ttl time.Duration) (storedValue []byte, wasSet bool, err error) {
	m.getOrSetCalls.Add(1)

	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}

	if m.closed.Load() {
		return nil, false, cache.ErrClosed
	}

	if m.getOrSetError != nil {
		return nil, false, m.getOrSetError
	}

	if ttl < 0 {
		return nil, false, cache.ErrInvalidTTL
	}

	// Handle TTL=0 as "no expiration" (100 years)
	expiration := time.Now().Add(ttl)
	if ttl == 0 {
		expiration = time.Now().Add(100 * 365 * 24 * time.Hour)
	}

	// Atomic get-or-set operation
	actual, loaded := m.data.LoadOrStore(key, &cacheEntry{
		value:      value,
		expiration: expiration,
	})

	entry := actual.(*cacheEntry)

	// Check expiration even if loaded
	if loaded && time.Now().After(entry.expiration) {
		// Expired, replace it
		entry = &cacheEntry{
			value:      value,
			expiration: expiration,
		}
		m.data.Store(key, entry)
		return value, true, nil
	}

	return entry.value, !loaded, nil
}

// CompareAndSet atomically compares and sets a value.
func (m *MockCache) CompareAndSet(ctx context.Context, key string, expectedValue, newValue []byte, ttl time.Duration) (bool, error) {
	m.compareAndSetCalls.Add(1)

	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}

	if m.closed.Load() {
		return false, cache.ErrClosed
	}

	if m.compareAndSetError != nil {
		return false, m.compareAndSetError
	}

	if ttl < 0 {
		return false, cache.ErrInvalidTTL
	}

	// Handle TTL=0 as "no expiration" (100 years)
	expiration := time.Now().Add(ttl)
	if ttl == 0 {
		expiration = time.Now().Add(100 * 365 * 24 * time.Hour)
	}

	// expectedValue == nil means "set only if key doesn't exist"
	if expectedValue == nil {
		_, loaded := m.data.LoadOrStore(key, &cacheEntry{
			value:      newValue,
			expiration: expiration,
		})
		return !loaded, nil
	}

	// Compare and swap existing value
	actual, ok := m.data.Load(key)
	if !ok {
		return false, nil // Key doesn't exist, can't compare
	}

	entry := actual.(*cacheEntry)

	// Check expiration
	if time.Now().After(entry.expiration) {
		m.data.Delete(key)
		return false, nil
	}

	// Compare values
	if !bytes.Equal(entry.value, expectedValue) {
		return false, nil
	}

	// Swap to new value
	m.data.Store(key, &cacheEntry{
		value:      newValue,
		expiration: expiration,
	})

	return true, nil
}

// Delete removes a value from the cache.
func (m *MockCache) Delete(ctx context.Context, key string) error {
	m.deleteCalls.Add(1)

	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if m.closed.Load() {
		return cache.ErrClosed
	}

	if m.deleteError != nil {
		return m.deleteError
	}

	m.data.Delete(key)
	return nil
}

// Health checks cache health.
func (m *MockCache) Health(ctx context.Context) error {
	m.healthCalls.Add(1)

	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if m.closed.Load() {
		return cache.ErrClosed
	}

	if m.healthError != nil {
		return m.healthError
	}

	return nil
}

// Stats returns mock cache statistics.
func (m *MockCache) Stats() (map[string]any, error) {
	m.statsCalls.Add(1)

	if m.closed.Load() {
		return nil, cache.ErrClosed
	}

	if m.statsError != nil {
		return nil, m.statsError
	}

	// Count entries
	count := 0
	m.data.Range(func(_, _ any) bool {
		count++
		return true
	})

	return map[string]any{
		"id":             m.id,
		"entry_count":    count,
		"get_calls":      m.getCalls.Load(),
		"set_calls":      m.setCalls.Load(),
		"delete_calls":   m.deleteCalls.Load(),
		"getorset_calls": m.getOrSetCalls.Load(),
		"cas_calls":      m.compareAndSetCalls.Load(),
		"health_calls":   m.healthCalls.Load(),
		"stats_calls":    m.statsCalls.Load(),
		"closed":         m.closed.Load(),
	}, nil
}

// Close closes the cache.
func (m *MockCache) Close() error {
	m.closeCalls.Add(1)

	// Check for configured error BEFORE changing state
	if m.closeError != nil {
		return m.closeError
	}

	if !m.closed.CompareAndSwap(false, true) {
		return cache.ErrClosed
	}

	// Clear data on close
	m.data.Range(func(key, _ any) bool {
		m.data.Delete(key)
		return true
	})

	// Call onClose callback if registered
	if m.onClose != nil {
		m.onClose(m.id)
	}

	return nil
}

// Test utility methods

// OperationCount returns the number of times a specific operation was called.
// Supported operations: "Get", "Set", "Delete", "GetOrSet", "CompareAndSet", "Health", "Stats", "Close"
func (m *MockCache) OperationCount(operation string) int64 {
	switch operation {
	case "Get":
		return m.getCalls.Load()
	case "Set":
		return m.setCalls.Load()
	case "Delete":
		return m.deleteCalls.Load()
	case "GetOrSet":
		return m.getOrSetCalls.Load()
	case "CompareAndSet", "CAS":
		return m.compareAndSetCalls.Load()
	case "Health":
		return m.healthCalls.Load()
	case "Stats":
		return m.statsCalls.Load()
	case "Close":
		return m.closeCalls.Load()
	default:
		return 0
	}
}

// IsClosed returns whether the cache has been closed.
func (m *MockCache) IsClosed() bool {
	return m.closed.Load()
}

// Has returns whether a key exists in the cache (ignoring expiration).
func (m *MockCache) Has(key string) bool {
	_, ok := m.data.Load(key)
	return ok
}

// Clear removes all entries from the cache.
// Useful for resetting state between test cases.
func (m *MockCache) Clear() {
	m.data.Range(func(key, _ any) bool {
		m.data.Delete(key)
		return true
	})
}

// ResetCounters resets all operation counters to zero.
// Useful for testing specific code paths without previous noise.
func (m *MockCache) ResetCounters() {
	m.getCalls.Store(0)
	m.setCalls.Store(0)
	m.deleteCalls.Store(0)
	m.getOrSetCalls.Store(0)
	m.compareAndSetCalls.Store(0)
	m.healthCalls.Store(0)
	m.statsCalls.Store(0)
	m.closeCalls.Store(0)
}

// ID returns the mock cache ID.
func (m *MockCache) ID() string {
	return m.id
}

// AllKeys returns all keys currently stored (including expired).
// Useful for debugging test failures.
func (m *MockCache) AllKeys() []string {
	var keys []string
	m.data.Range(func(key, _ any) bool {
		keys = append(keys, key.(string))
		return true
	})
	return keys
}

// Dump returns a string representation of cache contents for debugging.
func (m *MockCache) Dump() string {
	var result string
	result += fmt.Sprintf("MockCache(%s) closed=%v\n", m.id, m.closed.Load())
	result += "Contents:\n"

	m.data.Range(func(key, val any) bool {
		entry := val.(*cacheEntry)
		expired := time.Now().After(entry.expiration)
		result += fmt.Sprintf("  %s: %q (expires: %v, expired: %v)\n",
			key, string(entry.value), entry.expiration.Format(time.RFC3339), expired)
		return true
	})

	if result == fmt.Sprintf("MockCache(%s) closed=%v\nContents:\n", m.id, m.closed.Load()) {
		result += "  (empty)\n"
	}

	return result
}
