# ADR-010: Convert Panic-Based Validation to Error Returns

**Date:** 2025-11-29
**Status:** Accepted
**Context:** SonarCloud reliability rating improvement (C → A) via S8148 compliance

## Problem Statement

GoBricks uses `panic()` for fail-fast validation in several internal APIs, following the pattern "programmer errors should crash immediately." While this approach has merits for development-time error detection, it creates several issues:

1. **SonarCloud S8148 violations**: Rule states "Return an error instead of using panic for normal error conditions" with HIGH reliability impact
2. **Reliability rating**: Current panics contribute to C rating (target: A)
3. **Go idioms**: Standard Go practice prefers error returns over panics for recoverable conditions
4. **Consumer experience**: Library consumers receive stack traces instead of actionable error messages

**Affected Patterns:**
- Empty string validation (table names, expressions, aliases)
- Nil checks (subqueries)
- Parameter count validation (variadic alias arguments)
- Input security validation (dangerous characters in aliases)

## Options Considered

### Option 1: Mark as False Positives (NOSONAR)
Keep existing panics, mark with NOSONAR comments.

**Pros:** No breaking changes, maintains current behavior
**Cons:** Doesn't improve reliability rating, existing NOSONAR comments not recognized

### Option 2: Convert to Error Returns (CHOSEN)
Change function signatures to return errors instead of panicking.

**Pros:**
- ✅ Achieves reliability rating A
- ✅ Follows Go idioms (explicit error handling)
- ✅ Better consumer experience (actionable errors vs stack traces)
- ✅ Allows graceful degradation in consuming code
- ✅ Improves testability (errors can be asserted, panics cannot)

**Cons:**
- ❌ Breaking API changes
- ❌ Consumers must update call sites

### Option 3: Wrapper Functions
Create new error-returning functions alongside panic variants.

**Pros:** Non-breaking, gradual migration
**Cons:** API surface duplication, maintenance burden

## Decision

**Option 2: Convert to Error Returns**

This aligns with GoBricks manifesto principles:
- **Explicit > Implicit**: Error returns make failure modes explicit
- **Robustness**: Handle errors idiomatically, no silent failures
- **Type Safety**: Compile-time error handling enforcement

## Migration Guide

### Affected Functions

| Package | Function | Old Signature | New Signature |
|---------|----------|---------------|---------------|
| `database/types` | `Expr()` | `func Expr(sql string, alias ...string) RawExpression` | `func Expr(sql string, alias ...string) (RawExpression, error)` |
| `database/types` | `Table()` | `func Table(name string) *TableRef` | `func Table(name string) (*TableRef, error)` |
| `database/types` | `TableRef.As()` | `func (t *TableRef) As(alias string) *TableRef` | `func (t *TableRef) As(alias string) (*TableRef, error)` |
| `database/types` | `ValidateSubquery()` | `func ValidateSubquery(subquery SelectQueryBuilder)` | `func ValidateSubquery(subquery SelectQueryBuilder) error` |
| `database/types` | `Tx.Commit()` | `func Commit() error` | `func Commit(ctx context.Context) error` |
| `database/types` | `Tx.Rollback()` | `func Rollback() error` | `func Rollback(ctx context.Context) error` |

### Transaction Interface (S8242 Fix)

The `Tx` interface now requires context for `Commit()` and `Rollback()` operations:

```go
type Tx interface {
    // ... query methods unchanged ...

    // Transaction control - context now required for proper cancellation/tracing
    Commit(ctx context.Context) error
    Rollback(ctx context.Context) error
}
```

**Rationale:** This fixes S8242 (context-in-struct) by removing stored context from MongoDB transactions. Context now flows through method parameters following Go idioms.

**Migration:**
```go
// Before
tx, _ := db.Begin(ctx)
defer tx.Rollback()
// ... operations ...
tx.Commit()

// After
tx, _ := db.Begin(ctx)
defer tx.Rollback(ctx)
// ... operations ...
tx.Commit(ctx)
```

### Code Migration Examples

**Before (panic-based):**
```go
// Simple table reference
table := dbtypes.Table("users")

// Table with alias - method chaining
query := qb.Select("*").From(dbtypes.Table("users").As("u"))

// Expression with alias
expr := qb.Expr("COUNT(*)", "total")

// Subquery validation (internal)
types.ValidateSubquery(subquery)
```

**After (error-returning):**
```go
// Simple table reference
table, err := dbtypes.Table("users")
if err != nil {
    return fmt.Errorf("invalid table: %w", err)
}

// Table with alias - requires intermediate variable
table, err := dbtypes.Table("users")
if err != nil {
    return fmt.Errorf("invalid table: %w", err)
}
aliased, err := table.As("u")
if err != nil {
    return fmt.Errorf("invalid alias: %w", err)
}
query := qb.Select("*").From(aliased)

// Expression with alias
expr, err := qb.Expr("COUNT(*)", "total")
if err != nil {
    return fmt.Errorf("invalid expression: %w", err)
}

// Subquery validation
if err := types.ValidateSubquery(subquery); err != nil {
    return fmt.Errorf("invalid subquery: %w", err)
}
```

### Helper Functions (Optional)

For consumers who prefer the panic behavior, they can create wrapper functions:

```go
// MustTable panics if table creation fails (for use in tests or static initialization)
func MustTable(name string) *dbtypes.TableRef {
    t, err := dbtypes.Table(name)
    if err != nil {
        panic(fmt.Sprintf("MustTable: %v", err))
    }
    return t
}

// MustExpr panics if expression creation fails
func MustExpr(sql string, alias ...string) dbtypes.RawExpression {
    e, err := dbtypes.Expr(sql, alias...)
    if err != nil {
        panic(fmt.Sprintf("MustExpr: %v", err))
    }
    return e
}
```

## Error Types

New error variables for programmatic error checking:

```go
var (
    ErrEmptyTableName      = errors.New("table name cannot be empty")
    ErrEmptyTableAlias     = errors.New("table alias cannot be empty")
    ErrEmptyExpressionSQL  = errors.New("expression SQL cannot be empty")
    ErrTooManyAliases      = errors.New("expression accepts maximum 1 alias")
    ErrDangerousAlias      = errors.New("alias contains dangerous characters")
    ErrNilSubquery         = errors.New("subquery cannot be nil")
    ErrInvalidSubquery     = errors.New("subquery produced invalid SQL")
    ErrEmptySubquerySQL    = errors.New("subquery produced empty SQL")
)
```

## Additional Files Affected

Beyond `database/types/`, these files also have panic-to-error conversions:

| File | Functions |
|------|-----------|
| `observability/dual_processor.go` | `NewDualModeLogProcessor()` constructor |
| `database/testing/rowset.go` | `Scan()` methods on RowScanner |
| `scheduler/helpers.go` | Job helper validation |
| `testing/fixtures/database.go` | Test fixture setup |
| `database/internal/builder/query_builder.go` | Query builder validation |

## Testing Impact

- Existing tests using these functions will need updates
- New tests should assert error conditions instead of expecting panics
- Test coverage for error paths should be added

## Rollout Plan

1. **Phase 1**: Update function signatures in `database/types/`
2. **Phase 2**: Update all internal call sites in GoBricks
3. **Phase 3**: Update GoBricks demo project
4. **Phase 4**: Document in CHANGELOG with migration guide
5. **Phase 5**: Verify SonarCloud reliability rating improves to A

## S8242 Intentional Suppressions

The following S8242 (context-in-struct) patterns are **intentionally retained** as they represent legitimate architectural patterns, not anti-patterns:

| Location | Pattern | Justification |
|----------|---------|---------------|
| `scheduler/module.go:71-72` | Shutdown context | **Lifecycle context** for graceful shutdown coordination. This spans the entire service lifetime (not request-scoped). Standard Go pattern for service lifecycle management. |
| `scheduler/job.go:73` | Context embedding | **Interface extension pattern**. `JobContext` IS-A `context.Context` with additional job-specific methods. This is the idiomatic way to extend context in Go. |
| `logger/context_test.go:17,59` | Table-driven tests | **Standard Go testing pattern**. Storing context in test struct for table-driven tests is universally accepted in Go testing. |

**Why not refactored:**
1. **Scheduler shutdown context**: Refactoring to channel-based shutdown would be complex and risky. The context-with-cancel pattern is idiomatic for service lifecycle management.
2. **Job context embedding**: Changing to composition (storing `ctx` as field and delegating all methods) would add boilerplate without architectural benefit.
3. **Test patterns**: Table-driven test patterns with context in struct are standard Go practice.

## References

- [Go Blog: Error Handling and Go](https://go.dev/blog/error-handling-and-go)
- [Effective Go: Errors](https://go.dev/doc/effective_go#errors)
- [SonarCloud S8148: Functions should follow Go's explicit error handling patterns](https://rules.sonarsource.com/go/RSPEC-8148/)
- [SonarCloud S8242: context.Context should not be embedded in structs](https://rules.sonarsource.com/go/RSPEC-8242/)
- [GoBricks Manifesto: Robustness - Handle errors idiomatically](.specify/memory/constitution.md)
