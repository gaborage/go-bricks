# database/ — GoBricks package rules

Repo-wide rules stay in the root [CLAUDE.md](../CLAUDE.md).

## Database Architecture

Unified `database.Interface` supporting PostgreSQL (pgx, `$1` placeholders) and Oracle (`:1` placeholders, SEQUENCE built-in, UDT registration) with vendor-specific SQL generation, type-safe WHERE clauses, performance tracking, and connection pooling.

**Type-safe query building is the default pattern:** `qb.Columns(&T{})` (cached per vendor; Oracle reserved words such as `level` are auto-quoted), `qb.Filter()`, then `qb.Select(cols.Cols("ID", "Name")).From("users").Where(f.Eq(cols.Col("Level"), 5))` — full example in [llms.txt](../llms.txt).

**Type-Safe Methods:** `f.Eq`, `f.NotEq`, `f.Lt/Lte/Gt/Gte`, `f.In/NotIn`, `f.Like`, `f.Regex*`, `f.JSONContains` (PG only), `f.Null/NotNull`, `f.Between`, `f.Exists`, `f.NotExists`, `f.InSubquery`. Use `qb.Expr()` for complex SQL inside type-safe methods (no placeholders).

**Escape hatch:** `f.Raw(...)`, `jf.Raw(...)`, `SetExpr(column, expr, args...)` on an UPDATE, and `database.Raw(sql, args...)` (the Execute Helpers' hand-written-SQL adapter, broader than `f.Raw` — it replaces the whole statement, see [wiki/database.md#execute-helpers](../wiki/database.md#execute-helpers)) require a `// SECURITY: Manual SQL review completed - <rationale>` annotation at every call site; the authoritative rule (the `Having()` string-predicate case, the grep patterns) is the root [CLAUDE.md](../CLAUDE.md) Security Guidelines.

**Defaults applied automatically:** Connection pooling (25 max, keepalive 60s), session timezone (`UTC` per ADR-016), Oracle reserved word quoting.

For named databases (multi-DB single-tenant), table aliases, mixed JOIN conditions, subqueries, SELECT expressions, Oracle UDT registration, pool defaults, and session-timezone opt-out, see [wiki/database.md](../wiki/database.md).
