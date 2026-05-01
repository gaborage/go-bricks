# ADR-017: Standardize on `ToSQL()` Across All Query Builders

**Status:** Accepted
**Date:** 2026-05-01

## Context

Three of the four mutation entry points on `QueryBuilder` returned a go-bricks-owned interface (`SelectQueryBuilder`, `UpdateQueryBuilder`, `DeleteQueryBuilder`) that exposed `ToSQL()` (uppercase, idiomatic Go per [SonarCloud rule S8179](https://rules.sonarsource.com/go/RSPEC-8179/)). The fourth — `Insert` — leaked the upstream `squirrel.InsertBuilder` directly, whose only render method is the lowercase `ToSql()` defined by `squirrel.Sqlizer`.

| Builder | Previous return | Render method |
|---|---|---|
| `qb.Select(...)` | `types.SelectQueryBuilder` | `ToSQL()` ✅ |
| `qb.Update(...)` | `types.UpdateQueryBuilder` | `ToSQL()` ✅ |
| `qb.Delete(...)` | `types.DeleteQueryBuilder` | `ToSQL()` ✅ |
| `qb.Insert(...)` | `squirrel.InsertBuilder` | `ToSql()` ❌ |
| `qb.InsertWithColumns(...)` | `squirrel.InsertBuilder` | `ToSql()` ❌ |
| `qb.InsertStruct(...)` | `squirrel.InsertBuilder` | `ToSql()` ❌ |
| `qb.InsertFields(...)` | `squirrel.InsertBuilder` | `ToSql()` ❌ |

End users had to remember a different casing depending on the mutation — a small but persistent paper-cut that contradicts the project's "Explicit > Implicit" principle and the same S8179 rationale that drove the recent `Get*` → `*` rename across the framework (see CLAUDE.md "Breaking Change: Go Naming Conventions").

The inconsistency had already started leaking into the codebase:

- `llms.txt:890` documented an INSERT example calling `.ToSQL()` against the (then-) `squirrel.InsertBuilder` return — code that wouldn't have compiled.
- `database/query_builder_test.go` mixed `ToSql()` (3 sites) and `ToSQL()` (12 sites) in the same file.
- `database/internal/builder/query_builder_test.go` had 9 `ToSql()` calls on the four `Insert*` constructors.

`Filter` and `JoinFilter` had already solved this exact pattern by implementing **both** methods and routing `ToSQL()` → `ToSql()` (see `database/internal/builder/filter.go:39-46` and `database/internal/builder/join_filter.go:23-30`). INSERT just hadn't gotten the same treatment.

## Decision

Introduce `types.InsertQueryBuilder`, a go-bricks-owned interface that wraps `squirrel.InsertBuilder` and exposes only `ToSQL()` on the public surface. Re-point all four `Insert*` constructors to return it.

```go
// database/types/interfaces.go
type InsertQueryBuilder interface {
    Columns(columns ...string) InsertQueryBuilder
    Values(values ...any) InsertQueryBuilder
    SetMap(clauses map[string]any) InsertQueryBuilder
    Options(options ...string) InsertQueryBuilder
    Prefix(sql string, args ...any) InsertQueryBuilder
    Suffix(sql string, args ...any) InsertQueryBuilder
    Select(sb SelectQueryBuilder) InsertQueryBuilder

    ToSQL() (sql string, args []any, err error)
}
```

The concrete `*builder.InsertQueryBuilder` holds the underlying `squirrel.InsertBuilder` and delegates each chaining method to it. `ToSQL()` calls the wrapped builder's `ToSql()`. The existing pattern from `Filter`/`JoinFilter` is reused verbatim.

### Why these methods (and not the rest of `squirrel.InsertBuilder`)

The interface exposes the chaining methods that real callers in the framework, tests, and demo project use, plus a small set of common SQL-level features:

- `Columns`, `Values`, `SetMap` — covered every existing call site in the framework
- `Prefix`, `Suffix` — needed for `WITH` clauses and `ON CONFLICT`/`RETURNING` (see `BuildUpsert`)
- `Options` — needed for vendor-specific INSERT options like `OR IGNORE`/`OR REPLACE`
- `Select` — needed for `INSERT INTO ... SELECT` patterns

Squirrel-only knobs that don't belong on a vendor-aware go-bricks API (`RunWith`, `PlaceholderFormat`) are deliberately omitted: callers needing them can drop down to `ToSQL()` and execute via `db.Exec(ctx, sql, args...)`.

### What stays the same

- `BuildUpsert` in `database/internal/builder/postgres.go` continues to construct INSERTs internally; it now consumes the new wrapper via the public API. The wrapped squirrel builder is unchanged, so generated SQL is byte-identical.
- `Filter`/`JoinFilter` keep both `ToSql()` (for `squirrel.Sqlizer` compatibility) and `ToSQL()` — those are filter-side, not the mutation surface this ADR addresses.
- `MockQueryBuilder.BuildCaseInsensitiveLike` still returns `squirrel.Sqlizer` — that's an internal Squirrel type used inside Where clauses, not a top-level builder.

## Consequences

### Breaking change

Any caller of `qb.Insert(...)`, `qb.InsertWithColumns(...)`, `qb.InsertStruct(...)`, or `qb.InsertFields(...)` that called `.ToSql()` on the returned builder must rename to `.ToSQL()`:

```go
// ❌ OLD
sql, args, err := qb.Insert("users").Columns("name").Values("Alice").ToSql()

// ✅ NEW
sql, args, err := qb.Insert("users").Columns("name").Values("Alice").ToSQL()
```

Mechanical fix (`s/\.ToSql()/\.ToSQL()/g` against insert chains). The compiler catches every site at upgrade time — no silent runtime drift is possible.

Code that previously type-asserted the return as `squirrel.InsertBuilder` (e.g., to call `RunWith`) must now render via `ToSQL()` and execute through `database.Interface`. This is a deliberate scope-tightening: vendor abstraction is one of GoBricks' core principles, and `RunWith` would couple application code back to Squirrel.

### Mocks

`testing/mocks.MockQueryBuilder.Insert*(...)` returns `types.InsertQueryBuilder` rather than `squirrel.InsertBuilder`. A new `MockInsertQueryBuilder` (in `testing/mocks/query_builder_test.go`) implements the interface for tests that need to assert on chained calls.

### Positive

- One render method across the public mutation surface — no more "is it `ToSql` or `ToSQL` for this one?"
- Removes a footgun where docs and tests disagreed about the method name.
- Establishes the wrapping pattern for any future operations that might leak through `squirrel.*Builder` (e.g., `MERGE` if vendors add support).

### Negative

- Breaking change for downstream applications calling `.ToSql()` on insert chains.
- One additional indirection: insert chains now go through `*builder.InsertQueryBuilder` before reaching `squirrel.InsertBuilder`. The cost is a single pointer dereference per chained call — negligible against query execution.

### Neutral

- No behavioral change to generated SQL. The wrapper is a pass-through; vendor-specific quoting (Oracle reserved words, etc.) still happens at the same point.

## Migration

Single mechanical rename: `.ToSql()` → `.ToSQL()` on insert builder chains. CLAUDE.md gains an entry under "Breaking Change" sections matching the precedent set by S8179 / S8196.

## Related ADRs

- [ADR-005: Type-Safe WHERE Clause Construction](adr-005-type-safe-where-clauses.md) — established the `Filter`/`JoinFilter` pattern of wrapping squirrel with go-bricks-owned interfaces.
- [ADR-007: Struct-Based Column Extraction](adr-007-struct-based-columns.md) — introduced the `Columns` helper used heavily with `InsertStruct`/`InsertFields`.
- [ADR-013: Interface Naming Conventions (S8196)](adr-013-interface-naming-conventions.md) — the parallel public-API consistency push for interface names.
