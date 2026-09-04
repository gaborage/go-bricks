# ADR-098: Builder Clauses for the Ledger Stores

- **Status**: Accepted
- **Date**: 2026-09-04
- **Related**: [ADR-017](adr_017_insert_query_builder.md) (one casing, `ToSQL()`),
  [ADR-031](adr_031_query_builder_identifier_validation.md) and
  [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md) (identifier doors), the
  `// SECURITY:` annotation convention (CLAUDE.md "Security Guidelines")
- **Issue**: #1255 (the migration this decision serves), #1253 (the scoping
  signal — ten of eighteen hold-store statements went through the builder, four
  shapes could not)

## Context

The outbox and inbox ledger stores build their SQL with `fmt.Sprintf` and
hand-numbered placeholders, one implementation per vendor. Porting them to the
query builder (#1255) found four recurring shapes the builder could not carry:
a row lock on the leader row and the hold marker (`SELECT … FOR UPDATE [NOWAIT]`),
a `SET` that assigns an expression carrying a bound argument
(`lease_until = NOW() + (? * INTERVAL '1 second')`), scalar subqueries in a
projection (one round trip for a stats snapshot), and a table-less `SELECT` on
Oracle, which needs `FROM dual`. #1255's rule is that a recurring shape gets
first-class support; wrapping it in `qb.Expr` would be raw SQL in a builder's
coat and was rejected.

## Decision

Four additive clauses, shipped ahead of the store ports:

- `SelectQueryBuilder.ForUpdate()` and `.ForUpdateNoWait()` render the row lock
  as the statement's final suffix — after PostgreSQL's `LIMIT/OFFSET` and
  Oracle's `OFFSET/FETCH`. Both vendors spell the clause identically, so it is
  a builder constant, not a `vendorRenderer` method. On Oracle a lock combined
  with `Limit`/`Offset` fails `ToSQL` with `ErrRowLockWithPagination`: the SQL
  Language Reference's "Restrictions on the row_limiting_clause" reads "You
  cannot specify this clause with the for_update_clause". A locked builder is
  refused as a subquery (`ValidateForSubquery`), so `EXISTS (… FOR UPDATE)` is
  unreachable. `NOWAIT` pairs with the existing `database.IsLockNotAvailable`.
- `UpdateQueryBuilder.SetExpr(column, expr RawExpression, args ...any)` splices
  an expression whose `?` placeholders are renumbered with the statement's. It
  is a raw-SQL door on par with `f.Raw` — the body is never validated — and
  every call site carries the `// SECURITY: Manual SQL review completed - …`
  annotation; the door is added to the grep list in CLAUDE.md. `Set(col,
  RawExpression)` keeps its argument-free splice.
- `SelectQueryBuilder.SubqueryColumn(sub, alias)` appends `(sub) AS alias` to the
  projection; `sub` is validated like an `EXISTS` operand, rendered with
  question-mark placeholders so the outer statement numbers every argument
  once, and refused when it carries a row lock; the alias must be an unquoted
  identifier. `Select(...)`'s signature is unchanged — a subquery column is a
  sibling method, per the repo's naming precedent.
- A `SELECT` with no `From` renders `FROM dual` on Oracle, on the renderer side
  of the vendor seam: Oracle has no table-less `SELECT`, and an explicit
  `FromDual()` would be a vendor leak on the consumer surface.

Not added: `FetchFirst(n)` — `Limit` already renders `FETCH NEXT n ROWS ONLY` on
Oracle, and a second spelling of one clause contradicts ADR-017; an upsert
"lock on conflict" option — the `ON CONFLICT … DO UPDATE SET c = EXCLUDED.c`
idiom has one site, no Oracle equivalent, and `BuildUpsert` refuses updating a
conflict column on every vendor for ORA-38104 parity, so that site stays raw
and annotated and the two-statement idiom is documented next to the row lock.

## Consequences

- **Compile-break** (`feat(database)!:`): `SelectQueryBuilder` and
  `UpdateQueryBuilder` are exported interfaces, so every consumer type that
  IMPLEMENTS them — a hand-written double, a decorator — stops compiling until
  it grows the new methods (`testing/mocks` already has). Code that only CALLS
  the builders is unaffected. Per the C61.23 precedent (`outbox.Store` gained
  `Lead`) and the manifesto's stance on backward compatibility, the interfaces
  grow rather than fork into optional side interfaces, which would split the
  fluent chain. See [migrations.md](migrations.md) `[C63.3]`.
- `SetExpr` widens the raw-SQL door set by one; the security-audit grep and
  CLAUDE.md name it.
- The Oracle `FROM dual` rule changes the rendered text of any consumer's
  table-less Oracle `SELECT` (`SELECT 1` → `SELECT 1 FROM dual`) — the statement
  they were sending was invalid on Oracle, so this is a fix, not a break.
- The store ports (#1255 PR3/PR4) retire every `fmt.Sprintf` DML in
  `outbox/store_*.go`, `inbox/store_*.go` and `inbox/hold_store_*.go`; DDL, the
  Oracle `MERGE … USING dual` leader seed and function-based indexes stay raw
  with the annotation.
