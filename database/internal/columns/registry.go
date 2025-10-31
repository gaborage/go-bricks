package columns

import (
	"fmt"
	"reflect"
	"sync"
)

// ColumnRegistry maintains a cache of struct column metadata per database vendor.
// It uses lazy initialization: struct types are parsed on first use and cached forever.
//
// Thread-safety: Uses sync.Map for lock-free reads after first write.
// Memory overhead: ~1-2KB per registered struct type.
// Performance: ~2µs first-use parsing, ~50ns cached access.
type ColumnRegistry struct {
	mu           sync.RWMutex
	vendorCaches map[string]*vendorCache
}

// vendorCache holds column metadata for a specific database vendor.
// The cache is keyed by reflect.Type to ensure type-safe lookups.
type vendorCache struct {
	vendor string
	cache  sync.Map // map[reflect.Type]*ColumnMetadata
}

// Global registry instance (singleton pattern)
var globalColumnRegistry = &ColumnRegistry{
	vendorCaches: make(map[string]*vendorCache),
}

// RegisterColumns is the main entry point for extracting column metadata from a struct.
// It lazily parses the struct on first use and caches the result forever.
//
// Parameters:
//   - vendor: Database vendor name (e.g., "oracle", "postgres")
//   - structPtr: Pointer to a struct with `db:"column_name"` tags
//
// Returns:
//   - *ColumnMetadata: Cached column metadata for the struct type
//
// Panics if:
//   - structPtr is not a pointer to a struct
//   - No fields with `db` tags are found
//   - Any db tag contains dangerous SQL characters
//
// Example:
//
//	type User struct {
//	    ID    int64  `db:"id"`
//	    Name  string `db:"name"`
//	    Level string `db:"level"` // Oracle reserved word
//	}
//
//	cols := RegisterColumns(dbtypes.Oracle, &User{})
//	cols.Get("ID")    // Returns: `"ID"` (Oracle, quoted)
//	cols.Get("Level") // Returns: `"LEVEL"` (Oracle reserved word, quoted)
func RegisterColumns(vendor string, structPtr any) *ColumnMetadata {
	return globalColumnRegistry.Get(vendor, structPtr)
}

// Get retrieves column metadata for a struct type, lazily parsing on first use.
// Subsequent calls with the same type and vendor return the cached metadata.
func (cr *ColumnRegistry) Get(vendor string, structPtr any) *ColumnMetadata {
	// Get or create vendor-specific cache
	cache := cr.getOrCreateVendorCache(vendor)

	// Extract reflect.Type for cache key
	t := reflect.TypeOf(structPtr)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Fast path: check cache first (lock-free read after first write)
	if cached, ok := cache.cache.Load(t); ok {
		return cached.(*ColumnMetadata)
	}

	// Slow path: parse struct (this is the one-time reflection cost)
	// Use LoadOrStore to prevent race condition where multiple goroutines
	// simultaneously parse the same struct type before any stores the result.
	metadata, err := parseStruct(vendor, structPtr)
	if err != nil {
		// Fail-fast: panic on invalid struct definitions (development-time error)
		panic(fmt.Sprintf("failed to parse struct %s for vendor %s: %v", t.Name(), vendor, err))
	}

	// LoadOrStore atomically stores if not already present, or returns existing value
	actual, loaded := cache.cache.LoadOrStore(t, metadata)
	if loaded {
		// Another goroutine stored first - use their result to ensure singleton instance
		return actual.(*ColumnMetadata)
	}

	// We stored first - return our parsed metadata
	return metadata
}

// getOrCreateVendorCache retrieves or creates a vendor-specific cache.
// Uses double-checked locking pattern for thread-safe lazy initialization.
func (cr *ColumnRegistry) getOrCreateVendorCache(vendor string) *vendorCache {
	// Fast path: check if cache exists (read lock)
	cr.mu.RLock()
	cache, ok := cr.vendorCaches[vendor]
	cr.mu.RUnlock()

	if ok {
		return cache
	}

	// Slow path: create new cache (write lock)
	cr.mu.Lock()
	defer cr.mu.Unlock()

	// Double-check: another goroutine might have created it
	if cache, ok := cr.vendorCaches[vendor]; ok {
		return cache
	}

	// Create new vendor cache
	cache = &vendorCache{
		vendor: vendor,
		cache:  sync.Map{},
	}
	cr.vendorCaches[vendor] = cache

	return cache
}

// Clear removes all cached metadata (useful for testing).
// WARNING: Only call this in tests, not production code.
func (cr *ColumnRegistry) Clear() {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	cr.vendorCaches = make(map[string]*vendorCache)
}

// ClearGlobalRegistry clears the global column registry cache.
// WARNING: Only call this in tests to reset state between test cases.
func ClearGlobalRegistry() {
	globalColumnRegistry.Clear()
}
