# ADR-177: `Having` Accepts a Closed Set of Predicate Forms

**Status:** Accepted
**Date:** 2026-11-10
**Related:** [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md) (identifier arguments validated at every door) · [ADR-147](adr_147_predicate_alias_refusal.md) (predicate alias refusal) · [ADR-158](adr_158_unjudged_qb_comparators_deleted.md) (the unjudged `QueryBuilder` comparators are deleted)

## Context

`SelectQueryBuilder.Having(pred any, rest ...any)` judged one predicate form: a
`dbtypes.RawExpression` value, whose alias it refused with `ErrAliasInHaving` before running
`Validate()`. Every other predicate went to `squirrel.SelectBuilder.Having` unexamined, and squirrel
rendered it by its own rules:

- a `map[string]any` or a squirrel map literal (`squirrel.Eq`, `squirrel.Lt`, …) rendered its keys
  as columns verbatim, with no identifier validation (ADR-082) and no Oracle reserved-word
  quoting, and bound its values as written, skipping the operand judge every Filter value door
  runs;
- any other `squirrel.Sqlizer` (`squirrel.Expr(…)` included) was spliced through its `ToSql`, a raw
  SQL door the `// SECURITY:` grep could not see when it was built elsewhere;
- nil rendered an empty `HAVING ` with no error;
- a pointer to a `RawExpression`, and any unsupported type (`42`), failed with squirrel's untyped
  `expected string-keyed map or string, not <type>`, so an aliased pointer never reported
  `ErrAliasInHaving`;
- arguments after a Sqlizer predicate (a Filter included) were dropped silently.

ADR-158 deleted the `QueryBuilder` comparators that returned squirrel maps for the same reason: a
comparison built through go-bricks must pass the column funnel and the operand judge. `Having`
still let an unjudged map reach SQL.

## Decision

1. **`Having` accepts exactly three predicate forms**, in this dispatch order:
   - a `string`, with its trailing arguments: unchanged, the annotated raw-SQL door;
   - a `dbtypes.RawExpression`, with its trailing arguments: judged as before (alias first, then
     `Validate()`). A non-nil `*dbtypes.RawExpression` is judged the same way and renders exactly
     as the value form; both share one judgment;
   - a `dbtypes.Filter`, the bar `Where` applies: rendered through its own `ToSql`, so its column
     and operand are judged by the filter doors, and its placeholders number after any `Where`
     arguments.
2. **Everything else fails `ToSQL()` with one sentinel, `dbtypes.ErrUnsupportedHavingPredicate`**,
   matchable with `errors.Is`: a map, a squirrel map literal, any other `squirrel.Sqlizer`, nil, a
   typed-nil `*RawExpression` and any other type. The wrapped message names the accepted forms and
   the received type, rendered with `%T` only, never the value, since a map carries bound values:
   `HAVING predicate must be a string, a RawExpression (or a non-nil pointer to one) or a Filter: got <type>`.
3. **Trailing arguments beside a Filter are refused, not dropped**, with the same sentinel:
   `… got <type> with <n> trailing argument(s) (a Filter binds its own)`.
4. **The refusal is deferred** like the door's other errors: recorded on the builder and returned
   by `ToSQL()`, which renders nothing. The first recorded error wins. The signature is unchanged,
   and `testing/mocks` records `Having`'s arguments as before.

A Filter predicate needs no `// SECURITY:` annotation of its own; an expression inside it (an
`f.Raw`) carries one where it is constructed.

## Alternatives considered

- **Validate map keys and judge map values in place.** Rejected. It would rebuild the Filter doors
  under a second spelling, as ADR-158 declined to do for the comparators; `f.Eq`/`f.Gt`/`f.And`
  already express every map literal.
- **Accept any `squirrel.Sqlizer`.** Rejected. A Sqlizer the framework did not build is raw SQL the
  `// SECURITY:` grep cannot find at the `Having` call; `RawExpression` and `f.Raw` are the
  annotated spellings of the same thing.
- **Ignore trailing arguments beside a Filter, as squirrel did.** Rejected. A silently dropped
  argument is a predicate that binds something other than what the caller wrote.
- **Special-case structural Filters that are not WHERE filters** (`JoinFilter`, which also
  satisfies `dbtypes.Filter`). Rejected here: the bar is the one `Where` applies, and changing it
  belongs to both doors at once.

## Consequences

- **Breaking**, at runtime only: a `Having` call with a map, squirrel literal, other Sqlizer, nil,
  a nil pointer, any other type, or a Filter with trailing arguments now fails `ToSQL()` with
  `ErrUnsupportedHavingPredicate`. The compiler catches nothing.
- The string form and the `RawExpression` value form render byte-identically to before.
- A pointer to a `RawExpression` now renders, and an aliased one reports `ErrAliasInHaving`.
- A column comparison in HAVING goes through `qb.Filter()` (`Having(f.Gt("total", 100))`), so it
  is validated and quoted, `"number"` on Oracle included.

## References

- `database/internal/builder/query_builder.go`: `SelectQueryBuilder.Having`, `havingExpr`
- `database/types/errors.go`: `ErrUnsupportedHavingPredicate`
- `database/types/interfaces.go`: `SelectQueryBuilder.Having`
- [database.md](database.md): what is not an identifier door
- See [migrations.md](migrations.md) `[C74.6]`.
