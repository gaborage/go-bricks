# ADR-031: Validate Direct-String Identifier Arguments in the Query Builder (Close M9 SQL Injection)

**Status:** Accepted
**Date:** 2026-06-16

## Context

The query builder's direct-string APIs — `SelectQueryBuilder.From`, `OrderBy`, `GroupBy`, the JOIN family (`JoinOn`, `LeftJoinOn`, `RightJoinOn`, `InnerJoinOn`, `CrossJoinOn`), `UpdateQueryBuilder.Set`/`SetMap`, and `DeleteQueryBuilder.OrderBy` — interpolate their string identifier arguments (table names, aliases, columns) directly into the generated SQL. Identifier quoting was applied **only for Oracle** (`quoteColumnForQuery`/`quoteTableForQuery`/`quoteIdentifierForClause` had a vendor switch where the `default`/PostgreSQL branch returned the argument **verbatim**).

This meant a user-controlled identifier passed to one of these APIs on PostgreSQL was interpolated with no validation or escaping (the M9 finding from the 2026-06-10 audit). Concrete exploit:

```go
qb.Select("*").From("users").OrderBy(req.Query("sort")) // sort = "name; DROP TABLE users--"
// → SELECT * FROM users ORDER BY name; DROP TABLE users--
```

The generated SQL was directly executable and would drop the table. Quoting on PostgreSQL is **not** an acceptable fix on its own: PostgreSQL folds unquoted identifiers to lowercase, so blanket-quoting valid identifiers would change which physical column is referenced — a case-folding regression class (cf. ADR-007/Oracle M7). The values flowing through the *Filter* API (`f.Eq`, etc.) were never affected — those are already parameterized.

## Decision

Validate the string identifier arguments of `From`, the JOIN family (`JoinOn`/`LeftJoinOn`/`RightJoinOn`/`InnerJoinOn`/`CrossJoinOn`), `OrderBy`, `GroupBy`, `Set`, `SetMap`, and `DeleteQueryBuilder.OrderBy` against a safe grammar on **all vendors** *before* interpolation:

- **Table context** (`From` table names, JOIN table args): a simple or qualified identifier (`col`, `table.col`, `schema.table.col`) plus one optional inline alias (`"users u"`). When a `*TableRef` is passed instead of a string, its name and alias are each validated as bare/qualified identifiers (the `Table("users").As("u")` helper).
- **Identifier context** (`Set`/`SetMap` columns): a simple or qualified identifier, where each segment is either a bare identifier matching `[A-Za-z_][A-Za-z0-9_$#]*` (the same alphabet the `columns` package enforces for `db` tags) **or** a single double-quoted identifier with no embedded quotes (the shape the framework's own reserved-word quoting emits, e.g. Oracle `"level"`).
- **Clause context** (`OrderBy`, `GroupBy`, `DeleteQueryBuilder.OrderBy`): the above plus an optional bounded direction grammar — `col ASC|DESC [NULLS FIRST|LAST]`.

Anything outside the grammar — injection quotes, embedded whitespace, semicolons, `--` / `/* */` comment sequences, function calls, parentheses, or extra tokens — is **rejected**. Because the fluent builder methods cannot return an error, the violation is captured and surfaced from `ToSQL()` (mirroring the existing deferred-error pattern for `JoinOn` filter errors and `InsertQueryBuilder.Select`). The deferred error lives on each builder (`SelectQueryBuilder`/`UpdateQueryBuilder`/`DeleteQueryBuilder` each carry an `err` field); the first violation wins and short-circuits later mutations. The methods **never panic** on bad identifier content — a panic is reserved for genuine programming errors (an unsupported table-reference *type*).

Valid identifiers on PostgreSQL remain **unquoted** (no case-folding change). Complex or computed expressions must go through `qb.Expr()`/`Raw()`, which already carry an explicit `// SECURITY:` annotation requirement.

## Consequences

**Breaking (intended):**

- Identifier arguments that were previously accepted but are not bare/qualified identifiers (with an optional alias or direction) now produce a `ToSQL()` error. In practice this is:
  - SQL **function expressions** passed as plain strings to `OrderBy`/`GroupBy` (e.g. `OrderBy("COUNT(*) DESC")`) — must move to `qb.Expr("COUNT(*) DESC")`.
  - Any computed/parenthesized identifier expression — must move to `qb.Expr()`/`Raw()`.
  - **Positional ordinals** (`GroupBy("1")`, `OrderBy("1")`) and the rarer `ORDER BY col USING <operator>` form, which are not identifiers under the grammar — must move to `qb.Expr()`/`Raw()` (or use the column name).
- Attacker-controlled identifiers (the M9 vector) are now rejected rather than interpolated.

**Non-breaking:**

- Bare and qualified column/table names, inline table aliases (`"users u"`), the `Table().As()` helper (in `From` and every JOIN), framework-quoted Oracle reserved words (`cols.Col("Level")` → `"level"`), and `ORDER BY`/`GROUP BY` with a trailing `ASC`/`DESC` (and `NULLS FIRST`/`LAST`) continue to build unchanged on both vendors.
- The Filter API (`f.Eq`, `f.In`, …) and parameterized values are unaffected.
- PostgreSQL identifier quoting behavior is unchanged (valid identifiers stay unquoted).

See [database.md](database.md#identifier-validation-adr-031) for the developer-facing grammar and [migrations.md](migrations.md) for the upgrade note.
