package columns

import (
	"sync"
	"testing"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test structs for registry tests
type RegistryUser struct {
	ID    int64  `db:"id"`
	Name  string `db:"name"`
	Email string `db:"email"`
}

type RegistryAccount struct {
	ID     int64  `db:"id"`
	Number string `db:"number"` // Oracle reserved word
	Status string `db:"status"`
}

// TestColumnRegistryGetFirstUse tests lazy parsing on first use
func TestColumnRegistryGetFirstUse(t *testing.T) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}

	metadata := registry.Get(dbtypes.PostgreSQL, &RegistryUser{})

	require.NotNil(t, metadata)
	assert.Equal(t, "RegistryUser", metadata.TypeName)
	assert.Len(t, metadata.Columns, 3)
	assert.Equal(t, "ID", metadata.Columns[0].FieldName)
	assert.Equal(t, "Name", metadata.Columns[1].FieldName)
	assert.Equal(t, "Email", metadata.Columns[2].FieldName)
}

// TestColumnRegistryGetCached tests that subsequent calls return cached metadata
func TestColumnRegistryGetCached(t *testing.T) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}

	// First call: should parse
	metadata1 := registry.Get(dbtypes.PostgreSQL, &RegistryUser{})
	require.NotNil(t, metadata1)

	// Second call: should return cached instance
	metadata2 := registry.Get(dbtypes.PostgreSQL, &RegistryUser{})
	require.NotNil(t, metadata2)

	// Should be the exact same instance (not a copy)
	assert.Same(t, metadata1, metadata2, "Cached metadata should return the same instance")
}

// TestColumnRegistryGetVendorIsolation tests that different vendors have separate caches
func TestColumnRegistryGetVendorIsolation(t *testing.T) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}

	// Parse for Oracle (should quote reserved word "number")
	oracleMetadata := registry.Get(dbtypes.Oracle, &RegistryAccount{})
	require.NotNil(t, oracleMetadata)
	assert.Equal(t, `"number"`, oracleMetadata.columnsByField["Number"].QuotedColumn)

	// Parse for PostgreSQL (should not quote "number")
	pgMetadata := registry.Get(dbtypes.PostgreSQL, &RegistryAccount{})
	require.NotNil(t, pgMetadata)
	assert.Equal(t, "number", pgMetadata.columnsByField["Number"].QuotedColumn)

	// Oracle and PostgreSQL should have different instances
	assert.NotSame(t, oracleMetadata, pgMetadata, "Different vendors should have different metadata instances")
}

// TestColumnRegistryGetMultipleTypes tests caching of multiple struct types
func TestColumnRegistryGetMultipleTypes(t *testing.T) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}

	// Register User
	userMetadata := registry.Get(dbtypes.PostgreSQL, &RegistryUser{})
	require.NotNil(t, userMetadata)
	assert.Equal(t, "RegistryUser", userMetadata.TypeName)

	// Register Account
	accountMetadata := registry.Get(dbtypes.PostgreSQL, &RegistryAccount{})
	require.NotNil(t, accountMetadata)
	assert.Equal(t, "RegistryAccount", accountMetadata.TypeName)

	// Both should be cached and retrievable
	userMetadata2 := registry.Get(dbtypes.PostgreSQL, &RegistryUser{})
	accountMetadata2 := registry.Get(dbtypes.PostgreSQL, &RegistryAccount{})

	assert.Same(t, userMetadata, userMetadata2)
	assert.Same(t, accountMetadata, accountMetadata2)
}

// TestColumnRegistryGetPanic tests that invalid structs cause panic
func TestColumnRegistryGetPanic(t *testing.T) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}

	// Struct with no db tags should panic
	type NoTags struct {
		ID   int64
		Name string
	}

	assert.Panics(t, func() {
		registry.Get(dbtypes.PostgreSQL, &NoTags{})
	}, "Should panic on struct with no db tags")
}

// TestColumnRegistryGetConcurrent tests thread-safe concurrent access
func TestColumnRegistryGetConcurrent(t *testing.T) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	// Track results to verify they're all the same instance
	results := make([]*ColumnMetadata, goroutines)

	// Spawn concurrent goroutines all requesting the same metadata
	for i := 0; i < goroutines; i++ {
		go func(index int) {
			defer wg.Done()
			results[index] = registry.Get(dbtypes.PostgreSQL, &RegistryUser{})
		}(i)
	}

	wg.Wait()

	// Verify all goroutines got the same cached instance
	firstResult := results[0]
	for i := 1; i < goroutines; i++ {
		assert.Same(t, firstResult, results[i], "All concurrent accesses should return same cached instance")
	}
}

// TestColumnRegistryGetConcurrentMultipleVendors tests concurrent access across vendors
func TestColumnRegistryGetConcurrentMultipleVendors(t *testing.T) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2) // Oracle + PostgreSQL

	oracleResults := make([]*ColumnMetadata, goroutines)
	pgResults := make([]*ColumnMetadata, goroutines)

	// Spawn concurrent Oracle requests
	for i := 0; i < goroutines; i++ {
		go func(index int) {
			defer wg.Done()
			oracleResults[index] = registry.Get(dbtypes.Oracle, &RegistryAccount{})
		}(i)
	}

	// Spawn concurrent PostgreSQL requests
	for i := 0; i < goroutines; i++ {
		go func(index int) {
			defer wg.Done()
			pgResults[index] = registry.Get(dbtypes.PostgreSQL, &RegistryAccount{})
		}(i)
	}

	wg.Wait()

	// Verify Oracle results are all the same
	for i := 1; i < goroutines; i++ {
		assert.Same(t, oracleResults[0], oracleResults[i])
	}

	// Verify PostgreSQL results are all the same
	for i := 1; i < goroutines; i++ {
		assert.Same(t, pgResults[0], pgResults[i])
	}

	// Verify Oracle and PostgreSQL have different instances
	assert.NotSame(t, oracleResults[0], pgResults[0])
}

// TestRegisterColumns tests the global RegisterColumns function
func TestRegisterColumns(t *testing.T) {
	// Clear global registry before test
	ClearGlobalRegistry()
	defer ClearGlobalRegistry()

	metadata := RegisterColumns(dbtypes.Oracle, &RegistryUser{})

	require.NotNil(t, metadata)
	assert.Equal(t, "RegistryUser", metadata.TypeName)
	assert.Len(t, metadata.Columns, 3)

	// Second call should return cached instance
	metadata2 := RegisterColumns(dbtypes.Oracle, &RegistryUser{})
	assert.Same(t, metadata, metadata2)
}

// TestColumnRegistryClear tests clearing the registry cache
func TestColumnRegistryClear(t *testing.T) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}

	// Populate cache
	metadata1 := registry.Get(dbtypes.PostgreSQL, &RegistryUser{})
	require.NotNil(t, metadata1)

	// Clear cache
	registry.Clear()

	// Get again: should parse fresh (new instance)
	metadata2 := registry.Get(dbtypes.PostgreSQL, &RegistryUser{})
	require.NotNil(t, metadata2)

	// Should not be the same instance (cache was cleared)
	assert.NotSame(t, metadata1, metadata2, "Cleared registry should create new instance")
}

// TestClearGlobalRegistry tests clearing the global registry
func TestClearGlobalRegistry(t *testing.T) {
	// Populate global cache
	metadata1 := RegisterColumns(dbtypes.PostgreSQL, &RegistryUser{})
	require.NotNil(t, metadata1)

	// Clear global cache
	ClearGlobalRegistry()

	// Get again: should parse fresh
	metadata2 := RegisterColumns(dbtypes.PostgreSQL, &RegistryUser{})
	require.NotNil(t, metadata2)

	// Should not be the same instance
	assert.NotSame(t, metadata1, metadata2, "Cleared global registry should create new instance")
}

// TestColumnRegistryGetOrCreateVendorCache tests vendor cache creation
func TestColumnRegistryGetOrCreateVendorCache(t *testing.T) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}

	// First call: should create cache
	cache1 := registry.getOrCreateVendorCache(dbtypes.Oracle)
	require.NotNil(t, cache1)
	assert.Equal(t, dbtypes.Oracle, cache1.vendor)

	// Second call: should return existing cache
	cache2 := registry.getOrCreateVendorCache(dbtypes.Oracle)
	require.NotNil(t, cache2)

	// Should be the same instance
	assert.Same(t, cache1, cache2, "getOrCreateVendorCache should return same instance on subsequent calls")
}

// TestColumnRegistryGetOrCreateVendorCacheConcurrent tests concurrent vendor cache creation
func TestColumnRegistryGetOrCreateVendorCacheConcurrent(t *testing.T) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	caches := make([]*vendorCache, goroutines)

	// Spawn concurrent goroutines all requesting the same vendor cache
	for i := 0; i < goroutines; i++ {
		go func(index int) {
			defer wg.Done()
			caches[index] = registry.getOrCreateVendorCache(dbtypes.Oracle)
		}(i)
	}

	wg.Wait()

	// Verify all goroutines got the same cache instance
	firstCache := caches[0]
	for i := 1; i < goroutines; i++ {
		assert.Same(t, firstCache, caches[i], "All concurrent accesses should return same vendor cache instance")
	}
}

// TestColumnRegistryVendorCacheMultipleVendors tests that each vendor has its own cache
func TestColumnRegistryVendorCacheMultipleVendors(t *testing.T) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}

	oracleCache := registry.getOrCreateVendorCache(dbtypes.Oracle)
	pgCache := registry.getOrCreateVendorCache(dbtypes.PostgreSQL)
	mongoCache := registry.getOrCreateVendorCache(dbtypes.MongoDB)

	// All caches should be different instances
	assert.NotSame(t, oracleCache, pgCache)
	assert.NotSame(t, oracleCache, mongoCache)
	assert.NotSame(t, pgCache, mongoCache)

	// Each should have the correct vendor
	assert.Equal(t, dbtypes.Oracle, oracleCache.vendor)
	assert.Equal(t, dbtypes.PostgreSQL, pgCache.vendor)
	assert.Equal(t, dbtypes.MongoDB, mongoCache.vendor)
}

// TestColumnRegistryIntegrationRealWorldUsage tests realistic usage patterns
func TestColumnRegistryIntegrationRealWorldUsage(t *testing.T) {
	// Clear global registry
	ClearGlobalRegistry()
	defer ClearGlobalRegistry()

	// Scenario: Application startup registers multiple types for Oracle
	userCols := RegisterColumns(dbtypes.Oracle, &RegistryUser{})
	acctCols := RegisterColumns(dbtypes.Oracle, &RegistryAccount{})

	// Verify Oracle-specific quoting
	assert.Equal(t, "id", userCols.Col("ID"))
	assert.Equal(t, "name", userCols.Col("Name"))
	assert.Equal(t, `"number"`, acctCols.Col("Number")) // Oracle reserved word

	// Scenario: Later in the application, re-request the same metadata
	userCols2 := RegisterColumns(dbtypes.Oracle, &RegistryUser{})
	assert.Same(t, userCols, userCols2, "Should return cached instance")

	// Scenario: Different vendor for same struct
	acctColsPg := RegisterColumns(dbtypes.PostgreSQL, &RegistryAccount{})
	assert.Equal(t, "number", acctColsPg.Col("Number")) // PostgreSQL: no quoting
	assert.NotSame(t, acctCols, acctColsPg, "Different vendor should have different instance")
}
