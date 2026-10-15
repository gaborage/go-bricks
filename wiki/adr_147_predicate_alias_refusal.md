# ADR-147: A Predicate Operand Refuses an Aliased RawExpression

**Status:** Accepted
**Date:** 2026-10-15

## Context

A `RawExpression` carries an optional alias, and an alias renders only where a
column is projected: `Select` writes `<sql> AS <alias>`. Two doors already
refuse one because nothing there projects and the alias would be dropped
without a word: the `Having` predicate (`ErrAliasInHaving`) and the
`Set`/`SetMap`/`Values` cells (`ErrAliasInValue`, judged in `valueExpression`
since #2120).

The predicate operand doors did not. Since #2119 and #2120 a `JoinFilterFactory`
value door (the six compare doors, both `Between` bounds, `In` and `NotIn`)
splices an expression through the shared `expressionOperand`, and a
`FilterFactory` door refuses one with `ErrExpressionInFilter`. An aliased
expression at a `jf` door spliced its body and dropped the alias silently, and
an alias outside the identifier grammar surfaced as `ErrInvalidAlias` from
`Validate()`, a grammar complaint about text that was never going to render.
Fixes #2121.

## Decision

**Every `jf` predicate position refuses an aliased expression with the new
`dbtypes.ErrAliasInPredicate`.** The rule lives in `expressionOperand`, the one
place a predicate operand is judged, applied to the RESOLVED operand, so a
non-nil pointer to an aliased expression is refused the same way.

- **Order:** the family refusal first, so an aliased expression at an `f` door
  reports `ErrExpressionInFilter`. An `f` door takes values only (#1259), so the
  expression itself is the fault there, and removing the alias would still leave
  a refused operand. Then the alias, BEFORE `Validate()`, the order `Having` and
  `valueExpression` use, so an alias that also fails the grammar still reports
  `ErrAliasInPredicate`.
- **Surface:** the refusal surfaces at `ToSQL()`, with the operator in the
  message: `<op>: alias is not allowed in a predicate operand: <alias>`.
- **`GroupBy` and `OrderBy` keep rendering the body without its alias, by
  design.** They are clause doors, not operands: one aliased expression can serve
  `Select`, `GroupBy` and `OrderBy`, which is the idiom for grouping or ordering
  by a computed column. Their godoc and `wiki/database.md` say so.

## Alternatives considered

- **Render the alias in the predicate.** Rejected: SQL has no alias syntax for
  an operand.
- **Drop the alias silently, as before.** Rejected: an alias the caller wrote is
  intent, and `Having` and the value cells already refuse that silence.
- **Judge the alias at the `f` doors too (`ErrAliasInPredicate` everywhere).**
  Rejected: #1259 keeps `FilterFactory` value-only, so at an `f` door the
  expression itself is the fault, and dropping the alias would still fail.
  Reporting the alias first would point the caller at the wrong fix.
- **Refuse the alias at `GroupBy`/`OrderBy` too.** Rejected: GROUP BY and ORDER BY
  reference the expression, not its projection name, so refusing the alias would
  force two copies of one expression.
- **Reuse `ErrAliasInValue`.** Rejected: its text names SET and VALUES, and a
  matcher keyed on it would start firing for predicates.

## Consequences

- **Breaking** (`fix(database)!:`). A `jf` value door given an aliased
  expression built and ran with the alias dropped; it now fails `ToSQL()` with
  `ErrAliasInPredicate`. A caller with an alias outside the grammar used to get
  `ErrInvalidAlias` there and now gets `ErrAliasInPredicate`.
- `f` doors are unchanged: they refuse the expression itself first.
- `Select`, `GroupBy`, `OrderBy`, `Having`, `Set`, `SetMap`, `Values` and
  `BuildUpsert` are unchanged.
- On PostgreSQL, `GroupBy` and `OrderBy` still accept an alias such as `a#b` that
  `Select`'s vendor alias check refuses, since they never render it. That is out
  of scope here.

## References

- `database/types/errors.go` — `ErrAliasInPredicate`
- `database/internal/builder/helpers.go` — `expressionOperand`
- `database/types/interfaces.go` — `GroupBy`, `OrderBy`, `JoinFilterFactory`
  godoc
- `database/internal/builder/join_filter_test.go` —
  `TestPredicatePositionsRefuseAnAliasedRawExpression`,
  `TestJoinFilterRejectsRawExpressionAlias`
- `database/internal/builder/query_builder_test.go` —
  `TestAnAliasedExpressionServesSelectGroupByAndOrderBy`
- [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md) — the
  `Validate()` funnel and its consumption sites
- [ADR-100](adr_100_vendor_identifier_grammar_behind_renderer.md) — `Select`'s
  vendor alias check
- gaborage/go-bricks#1259 — `FilterFactory` takes values; accepting a
  `RawExpression` is a `JoinFilterFactory` difference
- gaborage/go-bricks#2121
- See [migrations.md](migrations.md) `[C72.34]`.
