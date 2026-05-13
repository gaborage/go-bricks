# Testing (Deep Dive)

This document covers the GoBricks testing conventions and the dedicated testing packages shipped with the framework. It walks through unit-test patterns for databases, caches, and the outbox, plus the testcontainers-based integration testing workflow. The naming conventions section comes first because it is mandatory and applies to every test file in the repository.

## Test Naming Conventions

**MANDATORY: Use camelCase for ALL test function names**

```go
// ✅ CORRECT - camelCase naming
func TestUserServiceCreateUser(t *testing.T) { }
func TestCacheManagerGetOrCreateCache(t *testing.T) { }
func TestQueryBuilderWithComplexJoins(t *testing.T) { }

// ❌ WRONG - snake_case (NEVER use this)
func TestUserService_CreateUser(t *testing.T) { }
func Test_CacheManager_GetOrCreateCache(t *testing.T) { }
func TestQueryBuilder_with_complex_joins(t *testing.T) { }
```

**Table-Driven Test Naming:**
```go
// ✅ CORRECT
func TestFilterEq(t *testing.T) {
    tests := []struct {
        name     string  // Use snake_case for test case descriptions
        column   string
        value    any
        expected string
    }{
        {name: "simple_equality", column: "id", value: 1, expected: "id = :1"},
        {name: "string_value", column: "name", value: "Alice", expected: "name = :1"},
    }
}

// ❌ WRONG - function name uses underscores
func Test_Filter_Eq(t *testing.T) { }
```

**Rationale:**
- **Consistency:** GoBricks enforces camelCase across the entire codebase
- **Go Idioms:** Test function names are regular Go identifiers (prefer camelCase)
- **Tooling:** Some tools parse test names assuming camelCase convention
- **Legacy Code:** All existing tests use camelCase (>800 test functions)

**Exception:** Test case descriptions in table-driven tests use snake_case for readability (e.g., `name: "with_invalid_credentials"`)

## Testing Strategy
- **Unit tests:** testify, database/testing (database), httptest (server), fake adapters (messaging)
- **Integration tests:** testcontainers, `-tags=integration` flag
- **Race detection:** All tests run with `-race` in CI
- **Coverage target:** 80% (SonarCloud)

## Database Testing

GoBricks provides `database/testing` package for easy database mocking without sqlmock complexity (**73% less boilerplate**).

**Simple Query Test:**
```go
import dbtest "github.com/gaborage/go-bricks/database/testing"

func TestProductService_FindActive(t *testing.T) {
    // Setup (8 lines vs 30+ with sqlmock)
    db := dbtest.NewTestDB(dbtypes.PostgreSQL)
    db.ExpectQuery("SELECT").
        WillReturnRows(
            dbtest.NewRowSet("id", "name").
                AddRow(int64(1), "Widget").
                AddRow(int64(2), "Gadget"),
        )

    deps := &app.ModuleDeps{
        DB: func(ctx context.Context) (database.Interface, error) {
            return db, nil
        },
    }

    svc := NewProductService(deps)
    products, err := svc.FindActive(ctx)

    assert.NoError(t, err)
    assert.Len(t, products, 2)
    dbtest.AssertQueryExecuted(t, db, "SELECT")
}
```

**Transaction Testing:**
```go
db := dbtest.NewTestDB(dbtypes.PostgreSQL)
tx := db.ExpectTransaction().
    ExpectExec("INSERT INTO orders").WillReturnRowsAffected(1).
    ExpectExec("INSERT INTO items").WillReturnRowsAffected(3)

// Test code that uses transactions
svc.CreateWithItems(ctx, order, items)

dbtest.AssertCommitted(t, tx)
```

**Multi-Tenant Testing:**
```go
tenants := dbtest.NewTenantDBMap()
tenants.ForTenant("acme").ExpectQuery("SELECT").WillReturnRows(...)
tenants.ForTenant("globex").ExpectQuery("SELECT").WillReturnRows(...)

deps := &app.ModuleDeps{
    DB: tenants.AsDBFunc(),  // Resolves tenant from context
}

ctx := multitenant.SetTenant(context.Background(), "acme")
result, err := svc.Process(ctx)  // Uses acme's TestDB
```

**Key Features:**
- Fluent expectation API (ExpectQuery/ExpectExec)
- Multi-tenant support via TenantDBMap
- Transaction tracking (commit/rollback assertions)
- Vendor-agnostic RowSet builder
- Partial SQL matching by default (or strict with StrictSQLMatching())

See [database/testing](../database/testing/) package and [llms.txt:294](../llms.txt) for full examples.

## Cache Testing

GoBricks provides `cache/testing` package for easy cache mocking without Redis dependencies (**similar to database/testing pattern**).

**Simple Cache Test:**
```go
import cachetest "github.com/gaborage/go-bricks/cache/testing"

func TestUserServiceCaching(t *testing.T) {
    mockCache := cachetest.NewMockCache()

    deps := &app.ModuleDeps{
        Cache: func(ctx context.Context) (cache.Cache, error) {
            return mockCache, nil
        },
    }

    svc := NewUserService(deps)
    user, err := svc.GetUser(ctx, 123)

    assert.NoError(t, err)
    cachetest.AssertCacheHit(t, mockCache, "user:123")
}
```

**Configurable Failures:**
```go
mockCache := cachetest.NewMockCache().
    WithGetFailure(cache.ErrConnectionError)

// Service should gracefully degrade
user, err := svc.GetUser(ctx, 123)  // Falls back to database
assert.NoError(t, err)

// Verify cache operation was attempted
cachetest.AssertOperationCount(t, mockCache, "Get", 1)
```

**Multi-Tenant Testing:**
```go
tenantCaches := map[string]*cachetest.MockCache{
    "acme":   cachetest.NewMockCache(),
    "globex": cachetest.NewMockCache(),
}

deps := &app.ModuleDeps{
    Cache: func(ctx context.Context) (cache.Cache, error) {
        tenantID := multitenant.GetTenant(ctx)
        return tenantCaches[tenantID], nil
    },
}

acmeCtx := multitenant.SetTenant(context.Background(), "acme")
result, err := svc.Process(acmeCtx)  // Uses acme's MockCache
```

**Key Features:**
- Fluent configuration API (`WithGetFailure`, `WithDelay`, `WithCloseCallback`)
- Operation tracking (Get/Set/Delete/GetOrSet/CompareAndSet counts)
- 20+ assertion helpers (`AssertCacheHit`, `AssertOperationCount`, `AssertValue`)
- TTL expiration testing (real time-based expiration)
- Multi-tenant isolation support

See [cache/testing](../cache/testing/) package for full API documentation and the Cache Testing Utilities section in [llms.txt](../llms.txt) for comprehensive examples.

## Outbox Testing

GoBricks provides `outbox/testing` package for mocking outbox operations in unit tests.

**Simple Test:**
```go
import outboxtest "github.com/gaborage/go-bricks/outbox/testing"

func TestOrderServiceCreateOrder(t *testing.T) {
    db := dbtest.NewTestDB(dbtypes.PostgreSQL)
    tx := db.ExpectTransaction().
        ExpectExec("INSERT INTO orders").WillReturnRowsAffected(1)

    mockOutbox := outboxtest.NewMockOutbox()

    svc := NewOrderService(db.AsDBFunc(), mockOutbox)
    err := svc.CreateOrder(ctx, order)

    assert.NoError(t, err)
    dbtest.AssertCommitted(t, tx)
    outboxtest.AssertEventPublished(t, mockOutbox, "order.created")
}
```

**Configurable Failures:**
```go
mockOutbox := outboxtest.NewMockOutbox().
    WithError(fmt.Errorf("outbox unavailable"))

// Service should handle outbox failure (transaction rolls back)
err := svc.CreateOrder(ctx, order)
assert.Error(t, err)
```

**Key Features:**
- Fluent configuration API (`WithError`)
- Event tracking (type, aggregate ID, payload, exchange)
- Assertion helpers (`AssertEventPublished`, `AssertEventCount`, `AssertEventWithAggregate`, `AssertNoEvents`)
- Thread-safe for concurrent test scenarios

See [outbox/testing](../outbox/testing/) package for full API documentation.

## Integration Testing with Testcontainers

**Prerequisites:** Docker Desktop or Docker Engine running

**Run Integration Tests:**
```bash
make test-integration           # All integration tests
make test-coverage-integration  # With coverage
```

**Build Tag Isolation:** Integration tests use `//go:build integration` - testcontainers dependencies only compiled with `-tags=integration`

**Writing Integration Tests:**
```go
//go:build integration

func TestFeature(t *testing.T) {
    conn, ctx := setupTestSchema(t)   // Per-test schema on shared container (Oracle, ADR-020)

    _, err := conn.Exec(ctx, "CREATE TABLE widgets (id NUMBER PRIMARY KEY, name VARCHAR2(100))")
    require.NoError(t, err)
    // ... test against the real database
}
```

### Oracle: shared container + per-test schema (ADR-020)

The `database/oracle` integration suite provisions exactly one Oracle container per test-binary execution (via package-level `TestMain`) and isolates each test in its own randomly-named schema. This avoids the ~18.5s per-test cold-start that previously pushed the package against the 10-minute Go test timeout.

**Test-isolation contract** — every Oracle integration test:

- **MUST** acquire its schema via `setupTestSchema(t)` (which delegates to `(*containers.OracleContainer).NewSchema(t)`).
- **MUST NOT** create globally-named objects. No `CREATE PUBLIC SYNONYM`, no `CREATE TYPE` outside the test's own schema — fully qualify with the per-test schema name (`CREATE TYPE <schema>.PRODUCT_TYPE`) so `DROP USER ... CASCADE` reclaims them on cleanup.
- **MUST NOT** rely on dropping its own tables/sequences/UDTs by name. `DROP USER ... CASCADE` is the cleanup primitive; tests that try to `DROP TABLE` explicitly will see no-op or already-dropped errors.
- **MAY** opt into `t.Parallel()` once the refactor is stable (separate follow-up after ADR-020 lands).

Tests that need a *different* `DatabaseConfig` (pool sizing, keep-alive, timezone, connection-string format variants) call `packageOracleContainer().NewSchema(t)` directly and build their own `cfg` from the returned `*containers.OracleSchema` credentials — still on the shared container, just with custom wiring.

**CI/CD:** Integration tests run only on Ubuntu (Docker requirement), unit tests on all platforms
