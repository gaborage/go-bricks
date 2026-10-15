# ADR-148: Every Subquery Door Refuses a Vendor-Rendered Placeholder

**Status:** Accepted
**Date:** 2026-10-15

## Context

A subquery is embedded in its outer statement as SQL text, and the outer
statement's final placeholder pass numbers every `?` in the result, its own and
the subquery's, once. A `$1` or `:1` the subquery already rendered is not a `?`,
so the pass leaves it verbatim: it collides with the outer numbering (a second
`$1` on PostgreSQL), or is invalid on the other vendor.

`renderSubquery` (`database/internal/builder/helpers.go`) guards this. It renders
this package's `SelectQueryBuilder` with `?` placeholders, and for any other
implementation (a mock, a decorator) it calls `ToSQL()` and refuses output that
matches `[$:]\d+` with `errSubqueryPlaceholderFormat`. `f.Exists`, `f.NotExists`
and `SubqueryColumn` have rendered through it since #1391. ADR-098 records only
the rendering half of that rule, for `SubqueryColumn`, not the refusal.

`f.InSubquery` did not use it. It carried its own copy of the concrete branch
and a fallback that embedded a foreign `ToSQL()` verbatim. A foreign subquery
rendering `$1` therefore built on the same vendor with no other parameter, and
beside another parameter collided with the outer numbering, which PostgreSQL
rejects at the database. Fixes #2122.

## Decision

**`InSubquery` renders its subquery only through `renderSubquery`.** Both of its
branches are deleted; `renderSubquery` is now the one place a subquery becomes
embedded SQL, for all four doors.

- A foreign subquery whose SQL matches `$N` or `:N` fails the outer statement's
  `ToSQL()` with `errSubqueryPlaceholderFormat`, on either vendor and under
  `SELECT`, `UPDATE` and `DELETE` alike.
- A foreign subquery that renders `?` builds, numbered by the outer statement.
- A subquery built by this package renders exactly as before.

## Alternatives considered

- **Renumber a foreign `$N`/`:N`.** Rejected: telling a placeholder from a
  literal means parsing another implementation's SQL, and the outer pass
  already numbers `?`, so `?` is the contract.
- **Add the check to `InSubquery`'s own fallback.** Rejected: two copies of
  one rule are how this door drifted from the other three.
- **Amend ADR-098.** Rejected: it records how `SubqueryColumn` renders, not
  the refusal, and the refusal now binds four doors.

## Consequences

- **Breaking** (`fix(database)!:`). A foreign `SelectQueryBuilder` passed to
  `f.InSubquery` whose `ToSQL()` returns `$N` or `:N` now fails `ToSQL()` with
  `subquery must render question-mark placeholders, not $N or :N`. On the same
  vendor with no other parameter, that statement built and ran before.
- The check is textual, so any foreign subquery SQL matching `[$:]\d+` is
  refused, not only a placeholder: a string literal or comment (`'10:30'`), or
  SQL such as a PostgreSQL array slice `arr[1:3]`. That limitation already held
  at the other three doors and is not addressed here.
- `errSubqueryPlaceholderFormat` stays unexported; a consumer matches the text.

## References

- `database/internal/builder/filter.go` — `InSubquery`, `buildExistsFilter`
- `database/internal/builder/helpers.go` — `renderSubquery`,
  `vendorPlaceholder`, `errSubqueryPlaceholderFormat`
- `database/internal/builder/query_builder.go` — `SubqueryColumn`
- `database/internal/builder/filter_test.go` —
  `TestFilterInSubqueryRefusesAForeignVendorPlaceholder`,
  `TestFilterInSubqueryNumbersAQuestionMarkSubquery`,
  `TestFilterInSubqueryRefusesAMockQueryBuilderVendorPlaceholder`
- [ADR-098](adr_098_builder_clauses_for_ledger_stores.md) — `SubqueryColumn`
  and the question-mark rendering rule
- gaborage/go-bricks#2122
- See [migrations.md](migrations.md) `[C72.35]`.
