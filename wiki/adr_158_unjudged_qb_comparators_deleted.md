# ADR-158: The Unjudged `QueryBuilder` Comparator Methods Are Deleted

**Status:** Accepted
**Date:** 2026-10-28
**Related:** [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md) (the column funnel) · [ADR-135](adr_135_dead_public_surface.md) (dead public surface is deleted, not deprecated) · [ADR-147](adr_147_predicate_alias_refusal.md) (the shared operand judge)

## Context

`*builder.QueryBuilder` carried six exported comparator methods: `Eq`, `NotEq`, `Lt`, `LtOrEq`,
`Gt` and `GtOrEq`. `database.QueryBuilder` embeds it, so all six were promoted onto the exported
`*database.QueryBuilder` that `database.NewQueryBuilder` returns. None of them is in
`types.QueryBuilderInterface`. Each validated and quoted its column through `quoteColumnForQuery`
(ADR-082). It then returned the matching squirrel map type (`squirrel.Eq`, `squirrel.NotEq`, …)
with the value stored as written, and an error for a bad column.

The value never reached the operand judge that every Filter value door runs (the *Operand
contract* in [database.md](database.md)). Squirrel rendered it by its own rules:

- a `RawExpression`, or a pointer to one, was bound as an argument the driver rejects at
  execution, where `f.Eq` refuses it at `ToSQL()` with `dbtypes.ErrExpressionInFilter`;
- a `driver.Valuer` was asked before any nil check, so a nil pointer to a Valuer type whose
  `Value()` dereferences its receiver panicked when the statement was built, where the judge
  reads a nil pointer as NULL before asking it anything;
- a pointer was dereferenced one level at `Eq`/`NotEq` and not at all at the ordering doors, so a
  `**int` to a nil `*int` at any door, or a nil `*int` at `Lt`, was bound as a pointer the driver
  sent as NULL, matching no row, where the judge resolves every pointer level first;
- a list operand's elements were bound as written, so an expression, pointer or Valuer element
  reached the driver unjudged, where the judge resolves each element as its own operand (C73.16).

The map types have no `ToSQL`, so they could not be passed to the framework's own `Where`. They
served a hand-built squirrel builder or `Having`. Nothing in the framework called them. Their one
caller was a test that pinned reserved-word column quoting, which the Filter doors pin too.

## Decision

**Delete the six methods.** Consumers use the Filter compare doors,
`qb.Filter().Eq/NotEq/Lt/Lte/Gt/Gte(column, value)`, passed to `Where`. `LtOrEq` becomes `Lte`
and `GtOrEq` becomes `Gte`. `quoteColumnForQuery` stays: the Filter and JoinFilter doors and
UPDATE's SET targets still use it.

## Alternatives considered

- **Route the six through the operand judge and keep them.** Rejected. They would duplicate the
  Filter doors under a second spelling that returns a squirrel map, which still cannot reach
  `Where`. Changing the return type to `types.Filter` would make them exact copies.
- **Deprecate first.** Rejected. ADR-135 and the project rule remove an obsolete path outright
  rather than keep a compatibility shim.

## Consequences

- **Breaking**, at compile time only: a call to any of the six on `*database.QueryBuilder` stops
  compiling. Code that still compiles renders the same SQL as before.
- The replacement returns a `types.Filter`, not a map. A `types.Filter` is a `squirrel.Sqlizer`,
  so it goes wherever the map went. Code that indexed or merged the map composes filters with
  `f.And` instead. A key assigned twice must be resolved first (the last value won in the map),
  because `f.And` keeps both conditions. A pointer or `driver.Valuer` operand is resolved when the
  filter is created rather than when the statement renders, so a reused filter must be rebuilt
  after its operand changes. A bad column no longer comes back as a second return value. It fails
  the enclosing statement's `ToSQL()`.
- The replacement judges the operand. A `RawExpression` is refused with `ErrExpressionInFilter`
  (move it to a `jf` door, or to `f.Raw` with its `// SECURITY:` annotation). A list at
  `Eq`/`NotEq` renders `IN`/`NOT IN` with each element judged. Where the same operand now renders
  differently, the `[C73.17]` table lists every case. Three widen, so under `Update`/`Delete` they
  touch rows the old call left alone: a `**int` to a nil `*int` at `Eq` or `NotEq` (bound as
  NULL, no row, now `IS NULL`/`IS NOT NULL`), and a nil pointer to a value-receiver Valuer at
  either (a build panic, now the same). At `Lt`/`Lte`/`Gt`/`Gte`, a nil pointer at any level, a
  list, a pointer to a list or a NULL answer fails with `ErrOrderingOperandNotComparable` at
  build, where squirrel bound a pointer the driver sent as NULL (no row), returned its own error,
  or panicked. Callers guard a pointer that can be nil at any level before building.
- `types.QueryBuilderInterface` and `testing/mocks` never carried the six names, so neither
  changes.

## References

- `database/internal/builder/filter.go`: `Eq`, `NotEq`, `Lt`, `Lte`, `Gt`, `Gte`
- `database/internal/builder/predicate_shells.go`: `comparePredicate`
- `database/query_builder.go`: the embedding that promoted the six methods
- [database.md](database.md): the operand contract
- See [migrations.md](migrations.md) `[C73.17]`.
