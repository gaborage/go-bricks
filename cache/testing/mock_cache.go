package testing

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gaborage/go-bricks/cache"
)

var _ cache.Cache = (*MockCache)(nil)

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
	// mu serializes every data access so CompareAndSet and GetOrSet are real atomics
	// rather than check-then-act sequences over data.
	mu     sync.Mutex
	data   map[string]*cacheEntry
	closed atomic.Bool

	// Configurable behavior
	delay                 time.Duration
	getError              error
	setError              error
	deleteError           error
	getOrSetError         error
	compareAndSetError    error
	compareAndDeleteError error
	healthError           error
	statsError            error
	closeError            error

	// Operation tracking
	getCalls              atomic.Int64
	setCalls              atomic.Int64
	deleteCalls           atomic.Int64
	getOrSetCalls         atomic.Int64
	compareAndSetCalls    atomic.Int64
	compareAndDeleteCalls atomic.Int64
	healthCalls           atomic.Int64
	statsCalls            atomic.Int64
	closeCalls            atomic.Int64

	// Close callback (for tracking in tests)
	onClose func(string)
}

// cacheEntry represents a stored value with expiration.
type cacheEntry struct {
	value      []byte
	expiration time.Time
}

// store writes an entry, materializing data on first use so a zero-value
// MockCache stays usable. Callers must hold mu.
func (m *MockCache) store(key string, entry *cacheEntry) {
	if m.data == nil {
		m.data = make(map[string]*cacheEntry)
	}
	m.data[key] = entry
}

// waitDelay honors the configured delay, or returns ctx's error if the context is
// already done or ends while waiting. A canceled context short-circuits regardless of
// delay, so every method reports cancellation the same way.
func (m *MockCache) waitDelay(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	timer := time.NewTimer(m.delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

// WithCompareAndDeleteFailure configures CompareAndDelete operations to return an error.
func (m *MockCache) WithCompareAndDeleteFailure(err error) *MockCache {
	m.compareAndDeleteError = err
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

	if err := m.waitDelay(ctx); err != nil {
		return nil, err
	}

	if m.closed.Load() {
		return nil, cache.ErrClosed
	}

	if m.getError != nil {
		return nil, m.getError
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.data[key]
	if !ok {
		return nil, cache.ErrNotFound
	}

	if time.Now().After(entry.expiration) {
		delete(m.data, key)
		return nil, cache.ErrNotFound
	}

	return entry.value, nil
}

// Set stores a value in the cache with TTL.
func (m *MockCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.setCalls.Add(1)

	if err := m.waitDelay(ctx); err != nil {
		return err
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

	m.mu.Lock()
	defer m.mu.Unlock()

	// Handle TTL=0 as "no expiration" (100 years)
	expiration := time.Now().Add(ttl)
	if ttl == 0 {
		expiration = time.Now().Add(100 * 365 * 24 * time.Hour)
	}

	m.store(key, &cacheEntry{
		value:      value,
		expiration: expiration,
	})
	return nil
}

// GetOrSet atomically gets a value or sets it if not present.
func (m *MockCache) GetOrSet(ctx context.Context, key string, value []byte, ttl time.Duration) (storedValue []byte, wasSet bool, err error) {
	m.getOrSetCalls.Add(1)

	if err := m.waitDelay(ctx); err != nil {
		return nil, false, err
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

	m.mu.Lock()
	defer m.mu.Unlock()

	// Handle TTL=0 as "no expiration" (100 years)
	expiration := time.Now().Add(ttl)
	if ttl == 0 {
		expiration = time.Now().Add(100 * 365 * 24 * time.Hour)
	}

	// Atomic get-or-set: a missing entry and an expired one are both replaced.
	entry, loaded := m.data[key]
	if !loaded || time.Now().After(entry.expiration) {
		m.store(key, &cacheEntry{
			value:      value,
			expiration: expiration,
		})
		return value, true, nil
	}

	return entry.value, false, nil
}

// CompareAndSet atomically compares and sets a value.
func (m *MockCache) CompareAndSet(ctx context.Context, key string, expectedValue, newValue []byte, ttl time.Duration) (bool, error) {
	m.compareAndSetCalls.Add(1)

	if err := m.waitDelay(ctx); err != nil {
		return false, err
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

	m.mu.Lock()
	defer m.mu.Unlock()

	// Handle TTL=0 as "no expiration" (100 years)
	expiration := time.Now().Add(ttl)
	if ttl == 0 {
		expiration = time.Now().Add(100 * 365 * 24 * time.Hour)
	}

	// expectedValue == nil means "set only if key doesn't exist"
	if expectedValue == nil {
		if _, loaded := m.data[key]; loaded {
			return false, nil
		}
		m.store(key, &cacheEntry{
			value:      newValue,
			expiration: expiration,
		})
		return true, nil
	}

	// Compare and swap existing value
	entry, ok := m.data[key]
	if !ok {
		return false, nil // Key doesn't exist, can't compare
	}

	if time.Now().After(entry.expiration) {
		delete(m.data, key)
		return false, nil
	}

	if !bytes.Equal(entry.value, expectedValue) {
		return false, nil
	}

	m.store(key, &cacheEntry{
		value:      newValue,
		expiration: expiration,
	})

	return true, nil
}

// CompareAndDelete atomically removes a key only if its current value matches.
//
// A configured error is checked before the nil-expectedValue rejection, mirroring where
// CompareAndSet places its ttl < 0 guard: on the mock, WithCompareAndDeleteFailure wins
// over a nil expectedValue.
func (m *MockCache) CompareAndDelete(ctx context.Context, key string, expectedValue []byte) (bool, error) {
	m.compareAndDeleteCalls.Add(1)

	if err := m.waitDelay(ctx); err != nil {
		return false, err
	}

	if m.closed.Load() {
		return false, cache.ErrClosed
	}

	if m.compareAndDeleteError != nil {
		return false, m.compareAndDeleteError
	}

	if expectedValue == nil {
		return false, cache.ErrNilExpectedValue
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.data[key]
	if !ok {
		return false, nil
	}

	// An expired entry reads as absent, matching Redis where GET returns false.
	if time.Now().After(entry.expiration) {
		delete(m.data, key)
		return false, nil
	}

	if !bytes.Equal(entry.value, expectedValue) {
		return false, nil
	}

	delete(m.data, key)
	return true, nil
}

// Delete removes a value from the cache.
func (m *MockCache) Delete(ctx context.Context, key string) error {
	m.deleteCalls.Add(1)

	if err := m.waitDelay(ctx); err != nil {
		return err
	}

	if m.closed.Load() {
		return cache.ErrClosed
	}

	if m.deleteError != nil {
		return m.deleteError
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, key)
	return nil
}

// Health checks cache health.
func (m *MockCache) Health(ctx context.Context) error {
	m.healthCalls.Add(1)

	if err := m.waitDelay(ctx); err != nil {
		return err
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

	m.mu.Lock()
	count := len(m.data)
	m.mu.Unlock()

	return map[string]any{
		"id":             m.id,
		"entry_count":    count,
		"get_calls":      m.getCalls.Load(),
		"set_calls":      m.setCalls.Load(),
		"delete_calls":   m.deleteCalls.Load(),
		"getorset_calls": m.getOrSetCalls.Load(),
		"cas_calls":      m.compareAndSetCalls.Load(),
		"cad_calls":      m.compareAndDeleteCalls.Load(),
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

	m.mu.Lock()
	clear(m.data)
	m.mu.Unlock()

	if m.onClose != nil {
		m.onClose(m.id)
	}

	return nil
}

// Test utility methods

// Operation names accepted by OperationCount and the operation assertions. Use these
// rather than a string literal: an unknown name panics in OperationCount and fails the test
// in the assertion helpers, naming the valid ones, so a misspelled assertion cannot pass
// vacuously (#1298).
const (
	OpGet              = "Get"
	OpSet              = "Set"
	OpDelete           = "Delete"
	OpGetOrSet         = "GetOrSet"
	OpCompareAndSet    = "CompareAndSet"
	OpCompareAndDelete = "CompareAndDelete"
	OpHealth           = "Health"
	OpStats            = "Stats"
	OpClose            = "Close"
)

// operationCounter binds an operation name to the field that counts it. The table is the
// one source of truth for the counted set: counter, OperationCounts, ResetCounters and the
// unknown-operation message all range over it.
type operationCounter struct {
	name    string
	counter func(*MockCache) *atomic.Int64
}

var operationCounters = []operationCounter{
	{name: OpGet, counter: func(m *MockCache) *atomic.Int64 { return &m.getCalls }},
	{name: OpSet, counter: func(m *MockCache) *atomic.Int64 { return &m.setCalls }},
	{name: OpDelete, counter: func(m *MockCache) *atomic.Int64 { return &m.deleteCalls }},
	{name: OpGetOrSet, counter: func(m *MockCache) *atomic.Int64 { return &m.getOrSetCalls }},
	{name: OpCompareAndSet, counter: func(m *MockCache) *atomic.Int64 { return &m.compareAndSetCalls }},
	{name: OpCompareAndDelete, counter: func(m *MockCache) *atomic.Int64 { return &m.compareAndDeleteCalls }},
	{name: OpHealth, counter: func(m *MockCache) *atomic.Int64 { return &m.healthCalls }},
	{name: OpStats, counter: func(m *MockCache) *atomic.Int64 { return &m.statsCalls }},
	{name: OpClose, counter: func(m *MockCache) *atomic.Int64 { return &m.closeCalls }},
}

// operations lists every counted operation name in table order.
var operations = func() []string {
	names := make([]string, len(operationCounters))
	for i, oc := range operationCounters {
		names[i] = oc.name
	}
	return names
}()

// counter resolves an operation name to its counter; ok is false for any name outside
// the table.
func (m *MockCache) counter(operation string) (c *atomic.Int64, ok bool) {
	for _, oc := range operationCounters {
		if oc.name == operation {
			return oc.counter(m), true
		}
	}
	return nil, false
}

func unknownOperationMessage(operation string) string {
	return fmt.Sprintf("cache/testing: unknown cache operation %q; valid operations: %s",
		operation, strings.Join(operations, ", "))
}

// OperationCount returns the number of times a specific operation was called. operation is
// one of the Op* constants; any other name panics, naming the valid ones — a misspelled
// name must not read as "zero calls".
func (m *MockCache) OperationCount(operation string) int64 {
	c, ok := m.counter(operation)
	if !ok {
		panic(unknownOperationMessage(operation))
	}
	return c.Load()
}

// IsClosed returns whether the cache has been closed.
func (m *MockCache) IsClosed() bool {
	return m.closed.Load()
}

// Has returns whether a key exists in the cache (ignoring expiration).
func (m *MockCache) Has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.data[key]
	return ok
}

// Clear removes all entries from the cache.
// Useful for resetting state between test cases.
func (m *MockCache) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()

	clear(m.data)
}

// ResetCounters resets all operation counters to zero.
// Useful for testing specific code paths without previous noise.
func (m *MockCache) ResetCounters() {
	for _, oc := range operationCounters {
		oc.counter(m).Store(0)
	}
}

// ID returns the mock cache ID.
func (m *MockCache) ID() string {
	return m.id
}

// AllKeys returns all keys currently stored (including expired).
// Useful for debugging test failures.
func (m *MockCache) AllKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	keys := make([]string, 0, len(m.data))
	for key := range m.data {
		keys = append(keys, key)
	}
	return keys
}

// Dump returns a string representation of cache contents for debugging.
func (m *MockCache) Dump() string {
	var body strings.Builder

	m.mu.Lock()
	for key, entry := range m.data {
		expired := time.Now().After(entry.expiration)
		fmt.Fprintf(&body, "  %s: %q (expires: %v, expired: %v)\n",
			key, string(entry.value), entry.expiration.Format(time.RFC3339), expired)
	}
	m.mu.Unlock()

	contents := body.String()
	if contents == "" {
		contents = "  (empty)\n"
	}

	return fmt.Sprintf("MockCache(%s) closed=%v\nContents:\n%s", m.id, m.closed.Load(), contents)
}
