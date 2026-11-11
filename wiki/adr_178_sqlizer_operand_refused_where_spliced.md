# ADR-178: A `squirrel.Sqlizer` Operand Is Refused Where the Builder Spliced It

**Status:** Accepted
**Date:** 2026-11-11
**Related:** [ADR-147](adr_147_predicate_alias_refusal.md) (the shared operand judge, and the alias refusal at the same `jf` doors) · [ADR-158](adr_158_unjudged_qb_comparators_deleted.md) (every comparison passes the operand judge)

## Context

The operand judge (`judgeOperand` in `database/internal/builder/helpers.go`) decides how a value
renders at every value door. A `RawExpression` is the one operand the builder means to splice: it
is judged (alias refused, `Validate()` run) and its SQL written in place of a placeholder. Every
other operand was meant to be bound.

Two kinds of position handed the value to a squirrel construct that expands a `squirrel.Sqlizer`
argument in place instead of binding it:

- the six `JoinFilterFactory` compare doors (`Eq`, `NotEq`, `Lt`, `Lte`, `Gt`, `Gte`) and both
  `Between` bounds rendered a resolved scalar through `squirrel.Expr("c op ?", value)`, so
  `jf.Eq("c", squirrel.Expr("NOW()"))` rendered `c = NOW()` with no argument and
  `jf.Eq("c", f.Eq("x", 1))` rendered `c = x = $1`;
- the SET and VALUES cells (`Set`, `SetMap` on UPDATE and INSERT, `SetStruct`, `Values`,
  `InsertStruct`, `InsertFields` and the PostgreSQL `BuildUpsert` insert and update values)
  passed the value to squirrel's SET / VALUES rendering, which expands a Sqlizer:
  `Set("c", squirrel.Expr("NOW()"))` rendered `SET c = NOW()`.

None of the `RawExpression` checks ran on that SQL. The same value at an `f` door, at
`In`/`NotIn` of either family, or as a list element was bound as one argument, so one operand
meant two things depending on the door. Oracle's `BuildUpsert` MERGE binds its values itself and
never expanded a Sqlizer; it now refuses one too (see the amendment below).

## Decision

**Refuse a `squirrel.Sqlizer` at every position that spliced one, and at Oracle's `BuildUpsert`
values (amendment below).** The statement's
`ToSQL()` (or `BuildUpsert`'s returned error) fails with the new sentinel
`dbtypes.ErrSqlizerOperand`, matchable with `errors.Is`. The message names the operand's Go type
with `%T`, never its value, and points at `qb.Expr()`:

- at a `jf` compare door or `Between` bound: `resolving the <op> operand: a squirrel.Sqlizer operand is not allowed (use qb.Expr() for an intended expression): <type>`;
- at a cell: `<column or value position N>: a squirrel.Sqlizer operand is not allowed (use qb.Expr() for an intended expression): <type>`, under `BuildUpsert: ` for an upsert value.

The check mirrors what each position spliced:

1. At a `jf` door it runs on the RESOLVED operand, after the `RawExpression` judgment and only for
   a non-nil, non-list scalar, the value `squirrel.Expr` received. A pointer to a Sqlizer is
   refused; a `driver.Valuer` is judged by its answer, and a pointer whose target has no `ToSql`
   binds the target, as before. A Sqlizer slice (`squirrel.And`) is still a list.
2. At a cell it runs on the value as written, the value squirrel received, in `valueCell` and
   `valueCells`, after the cell's `RawExpression` judgment. A type whose pointer alone has `ToSql`
   binds as a value and is refused as a pointer; a Valuer that also has `ToSql` is refused. The
   squirrel expression a cell builds from a judged `RawExpression` (with `SetExpr`'s arguments) is
   the builder's own output and is never judged again. Oracle's MERGE (`mergeCell`) applies the
   same cell judgment (amendment below).

The `f` doors, `In`/`NotIn` and list elements of both families keep binding a Sqlizer as one
argument. Every operand that is not a Sqlizer renders byte-identical SQL and arguments.
`operandPlaceholder` stays a separate answer from `operandBound`: squirrel's own constructs ask a
resolved value that is still a `driver.Valuer` (a Valuer answering a Valuer) a second time, which
`squirrel.Expr` does not, so merging them would move those operands' arguments.

## Alternatives considered

- **Option A: bind the Sqlizer at the `jf` doors, as `f` does.** Rejected. It removes the family
  difference at the predicate doors but leaves the cells splicing, and a bound
  `squirrel.Expr` or map is a value no driver accepts, so the caller learns only at execution what
  `ToSQL()` can say now. Turning a call that rendered SQL into one that binds an unusable struct is
  a silent change of meaning; a refusal names the door and the remedy.
- **Splice through the `RawExpression` checks.** Rejected. A Sqlizer has no alias or body to
  validate before rendering, and `qb.Expr()` already is the reviewed, annotated door for an
  intended expression; a second door would need its own `// SECURITY:` rule.
- **Refuse at every position that receives a Sqlizer, binding ones included.** Rejected. The `f`
  doors, `In`/`NotIn` and list elements bind it today; refusing there changes behavior the issue
  did not report. Oracle's MERGE, which also bound it, is the one binding position now refused
  (amendment below). `SetExpr`'s trailing arguments and `f.Raw`/`jf.Raw`
  arguments, which expand a Sqlizer today, are outside this decision.

## Consequences

- **Breaking**, at build time only: a statement passing a Sqlizer to a refused position fails
  `ToSQL()` (or `BuildUpsert`) with `ErrSqlizerOperand` where it rendered SQL. No signature or
  configuration key moves. Remedy: wrap an intended expression in `qb.Expr()`/`qb.MustExpr()` with
  its `// SECURITY:` annotation, or pass the plain value.
- PostgreSQL and Oracle agree on a Sqlizer in a `BuildUpsert` value: both refuse it at build
  (amendment below).
- `SetExpr`'s trailing arguments and `f.Raw`/`jf.Raw` arguments still expand a Sqlizer:
  unchanged, outside this decision. Those are raw-SQL doors, and every call site already carries
  the `// SECURITY: Manual SQL review completed` annotation, so the audit grep sees them.

## Amendment (2026-11-11, #2261): Oracle's `BuildUpsert` values are refused like PostgreSQL's

The maintainer decided that both vendors refuse a `squirrel.Sqlizer` in a `BuildUpsert` value, so
the same Go code gets the same verdict on PostgreSQL and Oracle. For upsert values the rule is one
call, one verdict per vendor, even though Oracle's MERGE bound the value rather than spliced it.

`mergeCell` (`database/internal/builder/oracle.go`) now calls `judgeValueCell`, the judgment
`valueCell` and `valueCells` apply, instead of `judgeOperand` directly. An insert or update value
is judged as passed: a Sqlizer by value (`squirrel.Expr`, `squirrel.Eq{…}`, `squirrel.And`, a
`SelectBuilder`, a `Filter`, a `driver.Valuer` that also has `ToSql`), a pointer to one and a nil
pointer to one are refused; a type whose pointer alone has `ToSql` is refused as a pointer and
bound as a value. `BuildUpsert` returns the error, with the PostgreSQL message and sentinel:
`BuildUpsert: <column>: a squirrel.Sqlizer operand is not allowed (use qb.Expr() for an intended expression): <type>`,
matching `dbtypes.ErrSqlizerOperand`. A `RawExpression` is still spliced byte-identically; a
pointer to a `RawExpression`, a scalar, nil, a slice and a `driver.Valuer` stay bound.

In v0.73.0 Oracle's MERGE bound a Sqlizer as one `:N` argument and never spliced it:
`BuildUpsert("t", []string{"id"}, map[string]any{"id": 1, "c": squirrel.Expr("NOW()")}, nil)`
rendered `MERGE INTO t target USING (SELECT :1 AS c, :2 AS id FROM dual) source ON (target.id = source.id) WHEN NOT MATCHED THEN INSERT (c, id) VALUES (source.c, source.id)`
with the `squirrel.Expr` value as argument 1, and an update value rendered as a `:N` in
`WHEN MATCHED THEN UPDATE SET`. The build succeeded and handed the driver the Go value as a bind,
unjudged; whether the statement then ran depended on the driver accepting that value. The change
lands in the unreleased 0.74.0 epoch and extends this ADR and `[C74.7]` in place.

## References

- `database/internal/builder/helpers.go`: `judgeOperand`, `judgeValueCell`, `valueCell`, `valueCells`, `refuseSqlizer`
- `database/internal/builder/oracle.go`: `mergeCell`
- `database/types/errors.go`: `ErrSqlizerOperand`
- [database.md](database.md): the operand contract
- See [migrations.md](migrations.md) `[C74.7]`.
