# ADR-008: Database Testing with Interface Segregation

## Status
Accepted (2025-01-10)

## Context
Application developers faced testing friction with `database.Interface` requiring ~25 methods across 4 wrapper types (Interface, Tx, Row, Statement). Testing required:
- **30+ lines of sqlmock boilerplate** per test
- **Mocking 13 methods** even when only using Query/QueryRow/Exec
- **Manual sql.Rows construction** with complex sqlmock expectations
- **No multi-tenant testing support** despite framework's function-based injection

This violated YAGNI (mocking unused methods) and created hundreds of lines of test boilerplate.

## Decision

### 1. Interface Segregation
Split `database.Interface` into focused sub-interfaces following Single Responsibility Principle:

```go
// Querier - Core query execution (4 methods, 80% of usage)
type Querier interface {
    Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
    QueryRow(ctx context.Context, query string, args ...any) Row
    Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
    DatabaseType() string
}

// Transactor - Transaction management (2 methods)
type Transactor interface {
    Begin(ctx context.Context) (Tx, error)
    BeginTx(ctx context.Context, opts *sql.TxOptions) (Tx, error)
}

// Interface - Full capabilities (embeds both)
type Interface interface {
    Querier
    Transactor
    // ... 7 additional methods (Prepare, Health, Stats, etc.)
}
```

**Backward Compatibility:** All existing code using `Interface` continues to work unchanged via interface embedding.

### 2. Testing Package (`database/testing/`)

Created comprehensive testing utilities following `observability/testing` patterns:

**TestDB** - Fluent expectation-based mocking:
```go
db := dbtest.NewTestDB(dbtypes.PostgreSQL).
    ExpectQuery("SELECT").
        WillReturnRows(NewRowSet("id", "name").AddRow(1, "Alice")).
    ExpectExec("INSERT").
        WillReturnRowsAffected(1)
```

**RowSet** - Vendor-agnostic row builder:
```go
rows := NewRowSet("id", "name", "email").
    AddRow(1, "Alice", "alice@example.com").
    AddRow(2, "Bob", "bob@example.com")
```

**TestTx** - Transaction testing with commit/rollback tracking:
```go
tx := db.ExpectTransaction().
    ExpectExec("INSERT INTO orders").WillReturnRowsAffected(1).
    ExpectExec("INSERT INTO items").WillReturnRowsAffected(3)

// Test code...
AssertCommitted(t, tx)
```

**TenantDBMap** - Multi-tenant testing support:
```go
tenants := NewTenantDBMap()
tenants.ForTenant("acme").ExpectQuery("SELECT").WillReturnRows(...)
tenants.ForTenant("globex").ExpectQuery("SELECT").WillReturnRows(...)

deps := &app.ModuleDeps{
    GetDB: tenants.AsGetDBFunc(),  // Respects function-based injection
}
```

**Assertion Helpers** - Clear test failures:
```go
AssertQueryExecuted(t, db, "SELECT")
AssertExecExecuted(t, db, "INSERT")
AssertTransactionCommitted(t, db)
```

## Consequences

### Positive
- **73% less test boilerplate**: 30+ lines → 8 lines per test
- **100% backward compatible**: All 32 test suites pass unchanged
- **Honors SRP**: Separate Querier (execution) from Transactor (transactions)
- **Multi-tenant ready**: TenantDBMap respects function-based `GetDB(ctx)` pattern
- **Type-safe**: Compile-time verification of interface compliance
- **Clearer test intent**: Expectation-based API vs verbose sqlmock regexp patterns
- **Zero breaking changes**: Existing code unaffected

### Negative
- **One more abstraction**: Developers must learn TestDB API (mitigated by clear examples in llms.txt)
- **RowSet.toSQLRows() incomplete**: QueryRow works, Query needs enhancement for multi-row results
- **Intentional code duplication**: TestDB.Query and TestTx.Query share implementation (~65 lines each, marked with `//nolint:dupl`)

### Trade-offs Accepted
- **Duplication over abstraction**: TestDB and TestTx implement similar Query/Exec logic separately to maintain clear separation of concerns and avoid premature abstraction
- **Test fidelity vs speed**: In-memory fake sacrifices exact database behavior for instant feedback (0ms vs 2-5s testcontainers)
- **Vendor-agnostic testing**: Most tests don't verify vendor-specific behavior (placeholder numbering, quoting) - integration tests cover this

## Alternatives Considered

### 1. Keep Monolithic Interface
**Rejected** - Violates SRP, makes testing unnecessarily complex

### 2. Use sqlmock Directly
**Rejected** - Too verbose (~30 lines of setup), regex-based SQL matching error-prone

### 3. Split ModuleDeps (DB vs DBFull)
**Rejected** - Violates SRP at wrong layer (ModuleDeps shouldn't have two database references)

### 4. Extract Shared Code to Helper Function
**Rejected** - Premature abstraction for ~65 lines. TestDB and TestTx have different error messages ("unexpected query" vs "unexpected query in transaction") and logging contexts, making extraction add complexity without clear benefit.

## Implementation

### Files Created
- `database/types/querier.go` - 4-method focused interface
- `database/types/transactor.go` - Transaction management interface
- `database/testing/fake_db.go` - TestDB with fluent API
- `database/testing/rowset.go` - Vendor-agnostic row builder
- `database/testing/fake_tx.go` - Transaction testing
- `database/testing/assertions.go` - Assertion helpers
- `database/testing/multitenant.go` - Multi-tenant support
- `database/testing/fake_db_test.go` - Comprehensive tests

### Files Modified
- `database/types/interfaces.go:270` - Interface now embeds Querier + Transactor

### Quality Metrics
- **Linter**: 0 issues (golangci-lint passes)
- **Tests**: All 32 suites pass (including new database/testing tests)
- **Coverage**: New package has comprehensive tests for all APIs

## Migration Guide

**For Application Developers:**

No migration needed! Existing code using `database.Interface` continues to work unchanged.

**Optional improvement** - Use `Querier` in service signatures when transactions not needed:

```go
// Before (still works)
func NewProductService(db database.Interface) *ProductService

// After (optional - simpler testing)
func NewProductService(db database.Querier) *ProductService
```

**For Test Code:**

Replace sqlmock with database/testing package:

```go
// Before (30+ lines)
mockDB := new(testmocks.MockDatabase)
mockRows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "Alice")
mockDB.On("Query", mock.Anything, mock.Anything, mock.Anything).Return(mockRows, nil)
mockDB.On("DatabaseType").Return("postgresql")
// ... 20+ more lines

// After (8 lines)
import dbtest "github.com/gaborage/go-bricks/database/testing"

db := dbtest.NewTestDB(dbtypes.PostgreSQL).
    ExpectQuery("SELECT").
    WillReturnRows(dbtest.NewRowSet("id", "name").AddRow(1, "Alice"))

deps := &app.ModuleDeps{
    GetDB: func(ctx context.Context) (database.Interface, error) {
        return db, nil
    },
}
```

## References
- Similar pattern: `messaging.Client` (5 methods) vs `messaging.AMQPClient` (16 methods)
- Inspiration: `observability/testing` package fluent API design
- Related: [MULTI_TENANT.md](../MULTI_TENANT.md) - Multi-tenant patterns
