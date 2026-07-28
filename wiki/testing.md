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
- **Unit tests:** testify, `database/testing` (DB mocking), `cache/testing` (cache mocking), `outbox/testing` (outbox mocking), httptest (server), fake adapters (messaging)
- **Integration tests:** testcontainers, `-tags=integration` flag
- **Race detection:** All tests run with `-race` in CI
- **Coverage target:** 80% (SonarCloud)

## Database Testing

GoBricks provides `database/testing` package for easy database mocking without sqlmock complexity (**73% less boilerplate**).

**Simple Query Test:**
```go
import dbtest "github.com/gaborage/go-bricks/database/testing"

func TestProductServiceFindActive(t *testing.T) {
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
    DB: tenants.AsGetDBFunc(),  // Resolves tenant from context
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

See [database/testing](../database/testing/) package and llms.txt's "Database Testing" section for full examples.

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
    WithGetFailure(cache.ErrClosed)

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
        tenantID, _ := multitenant.GetTenant(ctx)
        return tenantCaches[tenantID], nil
    },
}

acmeCtx := multitenant.SetTenant(context.Background(), "acme")
result, err := svc.Process(acmeCtx)  // Uses acme's MockCache
```

**Key Features:**
- Fluent configuration API (`WithGetFailure`, `WithDelay`, `WithCloseCallback`)
- Operation tracking (Get/Set/Delete/GetOrSet/CompareAndSet counts)
- 17 assertion helpers (`AssertCacheHit`, `AssertOperationCount`, `AssertValue`)
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

    getDB := func(ctx context.Context) (database.Interface, error) { return db, nil }
    svc := NewOrderService(getDB, mockOutbox)
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

## Server / HandlerContext Testing

Unit-test a `Handler` or `MiddlewareFunc` without standing up a router by building a
`HandlerContext` directly. Use `server.NewHandlerContextForTest` when no routing state is
needed, or `server.NewHandlerContextForTestWithOptions` to seed it. The synthetic context is
never routed, so routing-derived state is empty by default — seed only what the code under
test reads:

| State | How to seed | Read back via |
|---|---|---|
| Route template (`RouteTemplate()`) | `server.WithRouteTemplate("/api/orders/:id")` construction option | `ctx.RouteTemplate()` |
| Path params (`Param`, `param:"…"` binding) | `ctx.SetPathParams([]server.PathParam{…})` | `ctx.Param("id")` / `PathParams()` |
| Query params | already read from the request URL | `ctx.Query("limit")` |
| Request headers | already read from the request | `ctx.RequestHeader("X-…")` |

The two routing seams are deliberately different shapes: `RouteTemplate` is set once by the
router and has no runtime mutation path, so it is seeded by a **test-only construction
option** (it cannot corrupt a live routed context); path params have a legitimate runtime
setter (`SetPathParams`), reused as-is in tests.

Seed the context, then **drive the middleware/handler under test** and assert on its
observable effect — don't just assert on the seeded context:

```go
// Production middleware under test: record the matched route template.
func RouteTemplateRecorder(sink *string) server.MiddlewareFunc {
    return func(c server.HandlerContext, next func() error) error {
        *sink = c.RouteTemplate()
        return next()
    }
}

func TestRouteTemplateRecorder(t *testing.T) {
    req := httptest.NewRequest(http.MethodGet, "/api/orders/42", http.NoBody)
    c := server.NewHandlerContextForTestWithOptions(httptest.NewRecorder(), req, cfg,
        server.WithRouteTemplate("/api/orders/:id"),
    )

    var recorded string
    err := RouteTemplateRecorder(&recorded)(c, func() error { return nil }) // exercise the middleware

    require.NoError(t, err)
    assert.Equal(t, "/api/orders/:id", recorded) // asserts the middleware's behavior, not the seed
}
```

For end-to-end fidelity (real router populating template *and* params), register the route on
a `server.Server` and drive it with `httptest` / `ServeHTTP` instead.

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
- **MAY** opt into `t.Parallel()` as a separate follow-up (not yet done).

Tests that need a *different* `DatabaseConfig` (pool sizing, keep-alive, timezone, connection-string format variants) call `packageOracleContainer().NewSchema(t)` directly and build their own `cfg` from the returned `*containers.OracleSchema` credentials — still on the shared container, just with custom wiring.

**CI/CD:** Integration tests run only on Ubuntu (Docker requirement), unit tests on all platforms

## Mutation Gate

`make mutate` runs mutation testing on the diff only: it computes changed line
ranges vs `git merge-base HEAD origin/main`, runs gremlins per changed package,
and applies this policy to mutants that land on changed lines:

| Status | Verdict | Rationale |
|---|---|---|
| `LIVED` | **fail** (exit 1) | A mutant on a line you wrote survived your tests |
| `NOT COVERED` | warn | Coverage is SonarCloud's gate; no double-gating |
| `TIMED OUT` | pass | The mutant hung the code and the test timeout noticed |
| `KILLED` | pass | |

Excluded from scope: `_test.go` files, `testdata/`, `tools/` (separate Go
module), and `scripts/` (the wrapper itself — see the operational notes).
Engine version is pinned via `GREMLINS_VERSION` in the Makefile;
runtime knobs live in `.gremlins.yaml`. The nightly `Mutation Baseline`
workflow runs the full repo in advisory mode (one engine process per package,
so a crashed engine process only loses that package's shard — though the JSON
artifact uploads only after the final merge, so a full runner eviction still
loses the run) and publishes overall efficacy
plus a table of files with LIVED / NOT COVERED findings (first 50 rows) to the
job summary; the JSON artifact is the complete record.

Operational notes: a real `make mutate` run may leave `go.work.sum` modified
(gremlins' own module graph); if the file was clean before the run, restore it
(`git checkout -- go.work.sum`) rather than committing the churn — if it already
carried changes you made, unpick only the run's additions. `scripts/` is excluded from the gate's scope in
code: gremlins v0.5.0 misreports some mutants in the wrapper's nested
`package main` as KILLED (validated during rollout), so a green verdict there
would be untrustworthy — the wrapper's own unit tests are its safety net.
Library packages, which the gate actually polices, verdict correctly.

When the gate fails, strengthen the test so the listed mutant dies (assert the
boundary, the sign, the branch the operator flipped) — never respond by
excluding the file.

## Property-Based Tests

Invariant-heavy packages carry a `<pkg>_properties_test.go` suite built on
[`pgregory.net/rapid`](https://pkg.go.dev/pgregory.net/rapid) — an intentional
exception to the one-`_test.go`-per-source-file convention, alongside
`testhelpers_test.go`.
Current exemplars: `database` (placeholder arity, Oracle reserved-word quoting,
determinism), `config` (InjectInto round-trips, never-panic, env-beats-yaml
precedence), `jose` (seal/open integrity), `multitenant` (resolver contracts,
composite first-match, constructive success paths).

Pattern rules:

- One `rapid.Check` per invariant; name it `Test<Subject><Invariant>Property`.
- Expensive setup (key generation) lives OUTSIDE `rapid.Check`; iterations reuse it.
- State the invariant precisely. Example: tamper-resistance is not "any tamper
  errors" (base64 trailing bits can decode identically) but "Open never succeeds
  with altered plaintext".
- A failing property prints a reproducing seed (`-rapid.seed`); a genuine
  violation is a bug in the code under test — fix that code, never the
  generator or the property.
- Random generators alone can leave success paths vacuously untested (a random
  host virtually never ends in `.example.com`) — pair them with constructive
  draws that build guaranteed-success inputs.

Property suites are ordinary `go test` tests: they run in `make test`, under
`-race`, and count toward coverage.
