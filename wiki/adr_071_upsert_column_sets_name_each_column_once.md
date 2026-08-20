# ADR-071: Upsert column sets name each column once, in a form the vendor can name

- **Status**: Accepted
- **Date**: 2026-08-20
- **Related**: [ADR-028](adr_028_pg_upsert_binds_update_values.md) (the PostgreSQL half of the same builder) · [ADR-017](adr_017_insert_query_builder.md) (the `ToSQL` surface `BuildUpsert` sits beside) · atoms `[C59.7]`, `[C59.9]`, `[C59.10]` (the preconditions this completes)

## Context

`BuildUpsert` grew three preconditions over the C59 series: a conflict column
list may not name one column twice, every conflict column must appear in the
insert set, and no conflict column may also be updated. All three judge sameness
through `columnIdentity`, which asks what the *vendor* considers one column —
Oracle folds the unquoted identifiers the builder emits, so `id` and `ID` are one
column there, while PostgreSQL quotes everything and keeps them apart.

The series stopped at `conflictColumns`. `insertColumns` and `updateColumns` are
maps, so neither can hold an exact repeat, and `[C59.10]` recorded the remaining
gap rather than closing it: two *different* keys can still fold to one Oracle
column. Nothing checked for it, so the builder emitted SQL Oracle cannot parse
and returned `err = nil` while doing it:

```text
BuildUpsert("users", []string{"k"}, map[string]any{"k": 0, "id": 1, "ID": 2}, nil)

MERGE INTO users target USING (SELECT :1 AS ID, :2 AS id, :3 AS k FROM dual) source
  ON (target.k = source.k)
  WHEN NOT MATCHED THEN INSERT (ID, id, k) VALUES (source.ID, source.id, source.k)
```

The inline view declares one column twice once Oracle folds the aliases —
ORA-00957 at parse — and the INSERT list repeats it. Note the conflict column is
`k`, unrelated to the colliding pair: presence checking cannot see this, because
the two keys are wrong *with respect to each other*, not with respect to the
conflict target. The update set has the identical hole; `rejectConflictColumnUpdates`
already folds update keys, but only to keep its own error message deterministic.

A second, quieter defect sits underneath. `columnIdentity` decides "is this
rendering already quoted, and therefore case-sensitive" with
`strings.HasPrefix(rendered, "\"")` — a test of position 0. A qualified name
renders per part and rejoins, so `t."level"` renders quoted in the middle and
never at the front; the guard misses it and `ToUpper` runs over the quoted part.
`t."level"` and `t."LEVEL"` — two distinct Oracle columns — collapse onto one
identity, and function-shaped keys fold the same way because
`oracleQuoteIdentifier` returns them verbatim. That fold has both polarities: it
*accepts* more in the membership check, where every such input was already
producing invalid SQL, and it *rejects* more in the shipped `[C59.7]` check,
where a legitimate `a."b"` conflict column is refused against an `A."B"` update
key even though Oracle keeps those columns distinct.

## Decision

Two preconditions, checked at `BuildUpsert` alongside the three that precede
them, in an order that matters.

**First, every column named in an upsert must be a single column name.** On
Oracle each one becomes a column alias in the MERGE's USING clause — `:1 AS
<column>` — which admits one identifier and nothing else: no qualifier, no
function call, no empty name. A key that is not one of those could only ever
render SQL Oracle refuses to parse, so the failure moves from execution to build
time. The check runs on all three inputs — conflict, insert and update columns.

**Second, `insertColumns` and `updateColumns` must each name every column at
most once**, judged by the same `columnIdentity` the surrounding checks use, and
reporting both spellings the way `requireUniqueConflictColumns` already does.

Ordering is the load-bearing part. Because rendering is settled first, no key
that reaches `columnIdentity` from `BuildUpsert` can be partially quoted: every
survivor renders as one whole token, quoted or not, which is exactly the shape
`HasPrefix` reads correctly. The flawed guard is therefore unreachable from this
API rather than repaired — which is the point, because repairing it is a
behavior change to a shipped check and belongs to whoever wants that change,
with its own atom.

The identity check is vendor-keyed and so is a no-op on PostgreSQL by
construction: there a key is its own identity, and map keys are unique. The
single-column-name check is explicitly Oracle-only, self-gated the way
`columnIdentity` and `quoteOracleColumn` are: PostgreSQL quotes the whole key,
naming a column that is unusual but legal, so rejecting it there would break
calls that build correct SQL today.

## Alternatives considered

**Narrow `columnIdentity`'s guard to `strings.Contains(rendered, "\"")`.** The
direct repair, and rejected here on scope rather than on merit. It flips the
shipped `[C59.7]` outcome — a pairing that is refused today would build — which
is a behavior change needing its own atom and its own evidence. Rejecting the
inputs that reach the flaw closes the same hole without touching what any
currently-accepted call does.

**Deduplicate silently: keep one of the colliding keys.** Whichever key loses
takes its *value* with it, so an upsert would write a column the caller did not
ask for and drop the value they did. A caller who wrote both spellings has a bug;
telling them is the only safe answer.

**Leave it to the database.** ORA-00957 is a parse error, so the statement fails
every time rather than intermittently — but it fails at execution, inside
whatever transaction the caller had open, and the message names the SQL rather
than the two map keys that produced it. The builder holds both spellings and can
say so.

**Reject qualified and function-shaped keys on both vendors.** Symmetry for its
own sake. PostgreSQL renders `{"t.name": 1}` as a quoted column named `t.name` —
legal, and possibly intended. The rejection follows Oracle's MERGE grammar, which
is the actual constraint.

## Consequences

**Positive.** The three repro shapes fail at build time, naming both colliding
keys, instead of reaching the database as unparseable SQL. `[C59.10]`'s stated
scope limit is closed. The exported contract now says what it enforces, and the
identity fold in `columnIdentity` is unreachable from `BuildUpsert`.

**Negative.** This adds a precondition to a shipped exported API: a call that
previously returned SQL now returns an error. Every such call was already
producing SQL Oracle rejects at parse time, so no working call changes — but a
caller that treats a builder error as control flow sees a new one, and a test
pinning the old no-error outcome fails. Documented as `[C60.11]`.

**Neutral, and stated because the checks do not close the whole class.** A key
whose rendering carries an unbalanced quote — `a"b` renders `"a"b"` — is still
accepted and still emits SQL Oracle cannot parse. It does not fold identity, so
it is outside what this ADR is about, and it is garbage-in either way. In the
other direction, a caller-quoted key containing a dot (`"a.b"`) is now rejected
even though `SELECT :1 AS "a.b"` would be legal Oracle: `oracleQuoteIdentifier`
splits on the dot before it notices the surrounding quotes, so the builder cannot
render that column correctly today. Rejecting it is the honest outcome until the
renderer is fixed; accepting it would emit `""a"."b""`.

## Migration Impact

Breaking for the calls described above; see `[C60.11]` in
[migrations.md](migrations.md) for detection, gate and remedy. No change to the
PostgreSQL upsert path, to `rejectConflictColumnUpdates`'s semantics, or to any
call whose column keys are already distinct single names.
