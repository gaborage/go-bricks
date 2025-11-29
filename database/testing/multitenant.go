package testing

import (
	"context"
	"fmt"
	"sync"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/multitenant"
)

// TenantDBMap maps tenant IDs to TestDB instances for multi-tenant testing.
// It provides a convenient way to test multi-tenant code by setting up different
// database expectations for each tenant.
//
// Usage example:
//
//	tenants := NewTenantDBMap()
//
//	// Setup tenant-specific expectations
//	tenants.ForTenant("acme").
//	    ExpectQuery("SELECT * FROM products").
//	        WillReturnRows(NewRowSet("id", "name").AddRow(1, "Acme Widget"))
//
//	tenants.ForTenant("globex").
//	    ExpectQuery("SELECT * FROM products").
//	        WillReturnRows(NewRowSet("id", "name").AddRow(2, "Globex Gadget"))
//
//	// Inject into ModuleDeps
//	deps := &app.ModuleDeps{
//	    GetDB: tenants.AsGetDBFunc(),
//	}
//
//	// Test with tenant context
//	ctx := multitenant.SetTenant(context.Background(), "acme")
//	products, err := svc.GetProducts(ctx)
//	// assertions...
type TenantDBMap struct {
	databases     map[string]*TestDB
	defaultDB     *TestDB
	mu            sync.RWMutex
	defaultVendor string
}

// NewTenantDBMap creates a new tenant database map with the default vendor (PostgreSQL).
func NewTenantDBMap() *TenantDBMap {
	return NewTenantDBMapWithVendor(dbtypes.PostgreSQL)
}

// NewTenantDBMapWithVendor creates a new tenant database map with the specified default vendor.
// All TestDB instances created via ForTenant() will use this vendor unless overridden.
func NewTenantDBMapWithVendor(vendor string) *TenantDBMap {
	return &TenantDBMap{
		databases:     make(map[string]*TestDB),
		defaultVendor: vendor,
	}
}

// ForTenant returns the TestDB for the specified tenant ID.
// If no TestDB exists for this tenant, a new one is created with the default vendor.
// Returns the TestDB for method chaining.
//
// Example:
//
//	tenants := NewTenantDBMap()
//	tenants.ForTenant("acme").ExpectQuery("SELECT").WillReturnRows(...)
//	tenants.ForTenant("globex").ExpectQuery("SELECT").WillReturnRows(...)
func (m *TenantDBMap) ForTenant(tenantID string) *TestDB {
	m.mu.Lock()
	defer m.mu.Unlock()

	if db, exists := m.databases[tenantID]; exists {
		return db
	}

	// Create new TestDB for this tenant
	db := NewTestDB(m.defaultVendor)
	m.databases[tenantID] = db
	return db
}

// ForTenantWithVendor returns the TestDB for the specified tenant ID with a specific vendor.
// Useful when different tenants use different database vendors.
//
// Example:
//
//	tenants := NewTenantDBMap()
//	tenants.ForTenantWithVendor("acme", dbtypes.PostgreSQL).ExpectQuery(...)
//	tenants.ForTenantWithVendor("globex", dbtypes.Oracle).ExpectQuery(...)
func (m *TenantDBMap) ForTenantWithVendor(tenantID, vendor string) *TestDB {
	m.mu.Lock()
	defer m.mu.Unlock()

	if db, exists := m.databases[tenantID]; exists {
		// Tenant already has a DB - warn if vendor mismatch
		if db.DatabaseType() != vendor {
			panic(fmt.Sprintf("tenant %q already has DB with vendor %q, cannot change to %q",
				tenantID, db.DatabaseType(), vendor))
		}
		return db
	}

	// Create new TestDB for this tenant with specific vendor
	db := NewTestDB(vendor)
	m.databases[tenantID] = db
	return db
}

// SetDefaultDB sets a fallback TestDB that will be returned when no tenant is in context
// or when the tenant ID is not found in the map.
//
// This is useful for testing code that should work in both single-tenant and multi-tenant modes.
//
// Example:
//
//	tenants := NewTenantDBMap()
//	tenants.SetDefaultDB(NewTestDB(dbtypes.PostgreSQL).ExpectQuery(...))
//
//	// When no tenant in context, uses default DB
//	ctx := context.Background()
//	db, _ := tenants.AsGetDBFunc()(ctx)  // Returns default DB
func (m *TenantDBMap) SetDefaultDB(db *TestDB) *TenantDBMap {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultDB = db
	return m
}

// AsGetDBFunc returns a function compatible with ModuleDeps.GetDB.
// The returned function extracts the tenant ID from context and returns the corresponding TestDB.
//
// Behavior:
//   - If tenant ID found in context: Returns TestDB for that tenant (or error if not configured)
//   - If no tenant in context and default DB set: Returns default DB
//   - If no tenant in context and no default DB: Returns error
//
// Example:
//
//	tenants := NewTenantDBMap()
//	tenants.ForTenant("acme").ExpectQuery("SELECT").WillReturnRows(...)
//
//	deps := &app.ModuleDeps{
//	    GetDB: tenants.AsGetDBFunc(),
//	}
//
//	ctx := multitenant.SetTenant(context.Background(), "acme")
//	db, err := deps.GetDB(ctx)  // Returns TestDB for "acme"
func (m *TenantDBMap) AsGetDBFunc() func(context.Context) (dbtypes.Interface, error) {
	return func(ctx context.Context) (dbtypes.Interface, error) {
		// Try to get tenant from context
		tenantID, hasTenant := multitenant.GetTenant(ctx)

		if hasTenant {
			// Tenant in context - look up their DB
			m.mu.RLock()
			db, exists := m.databases[tenantID]
			m.mu.RUnlock()

			if !exists {
				return nil, fmt.Errorf("no TestDB configured for tenant %q (use ForTenant() to set up)", tenantID)
			}

			return db, nil
		}

		// No tenant in context - use default if available
		m.mu.RLock()
		defaultDB := m.defaultDB
		m.mu.RUnlock()

		if defaultDB != nil {
			return defaultDB, nil
		}

		return nil, fmt.Errorf("no tenant in context and no default DB configured (use SetDefaultDB())")
	}
}

// TenantDB returns the TestDB for a specific tenant ID without context.
// Returns nil if the tenant has no configured TestDB.
//
// This is useful for assertions that need to inspect a specific tenant's database calls:
//
//	tenants := NewTenantDBMap()
//	// ... run test code ...
//	acmeDB := tenants.TenantDB("acme")
//	AssertQueryExecuted(t, acmeDB, "SELECT * FROM products")
func (m *TenantDBMap) TenantDB(tenantID string) *TestDB {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.databases[tenantID]
}

// AllTenantIDs returns all tenant IDs that have configured TestDB instances.
// Useful for iterating over all tenants in assertions.
//
// Example:
//
//	tenants := NewTenantDBMap()
//	// ... run test code ...
//	for _, tenantID := range tenants.AllTenantIDs() {
//	    db := tenants.TenantDB(tenantID)
//	    AssertQueryExecuted(t, db, "SELECT")
//	}
func (m *TenantDBMap) AllTenantIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := make([]string, 0, len(m.databases))
	for id := range m.databases {
		ids = append(ids, id)
	}
	return ids
}

// Reset clears all configured TestDB instances and the default DB.
// Useful for test cleanup or when reusing a TenantDBMap across test cases.
//
// Example:
//
//	tenants := NewTenantDBMap()
//	t.Run("test1", func(t *testing.T) {
//	    tenants.ForTenant("acme").ExpectQuery(...)
//	    // test...
//	    tenants.Reset()
//	})
func (m *TenantDBMap) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.databases = make(map[string]*TestDB)
	m.defaultDB = nil
}
