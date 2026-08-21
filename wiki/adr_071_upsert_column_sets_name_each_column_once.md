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

**First, every column named in an upsert must be a single column name** — no
qualifier, no function call, no empty name, and no quote that ends the identifier
early. The check runs on all three inputs, for two different reasons. Conflict and
insert keys have none: they become column aliases in the MERGE's USING clause
(`:1 AS <column>`) and entries in its INSERT list, and neither position admits
anything but one identifier. Update keys become UPDATE SET targets, where Oracle
would also accept `target.<column>`; holding them to the same rule is this API's
choice, so that one spelling of a column works in every position a call names it.

The last clause is the one that matters most, and it is not a parse-error
concern. `oracleQuoteIdentifier` wraps a key in quotes **without doubling the
quotes inside it**, so a key spelled `role" = 'admin', "name` renders as
`"role" = 'admin', "name"` — valid SQL, and a second SET assignment the caller
never wrote, in a position no bind parameter guards. A quote inside a quoted
identifier is legal only doubled, which is exactly what the check tests: strip
the `""` pairs and refuse any survivor. `a""b` still names one column and still
builds. The renderer's missing escape is the wider bug — it reaches the table
argument and PostgreSQL's escaper too, neither of which this ADR touches — and
is tracked as issue #1104; what this decides is that a key the builder cannot
render faithfully is refused rather than emitted.

**Second, `insertColumns` and `updateColumns` must each name every column at
most once**, judged by the column each key actually NAMES rather than by how it
renders, and reporting both spellings the way the conflict-column check already
did — which is now the same helper, generalized to take any of the three column
groups.

That distinction is the whole of it. `columnIdentity`, which the membership and
overlap checks used, compared renderings: `id` renders unquoted and `"ID"` renders
quoted, so it called them two columns. Oracle folds the first onto the second and
calls them one, and a MERGE naming both declares that column twice — the very
ORA-00957 this ADR exists to prevent, reached by a second route. So the new helper
unwraps a quoted rendering to the text it names and upper-cases an unquoted one.
`id` and `"id"` stay two columns; `level` and `LEVEL` stay two, both rendering
quoted with their case intact; `id` and `"ID"` become one.

**Third, the membership and overlap checks key on that same named column**, and
`columnIdentity` is gone. Leaving them on the rendering comparison was the first
draft of this ADR, and review found it does not hold: the two checks answer
questions *about the same column set*, so a second identity rule inside one
`BuildUpsert` call splits one Oracle column in two. `["\"ID\""]` against
`{"id": 1}` failed membership even though the value IS supplied — the harmless
polarity — and, with teeth, `["\"ID\""]` against `{"id": 3}` was not seen as an
overlap, so the builder emitted `ON (target."ID" = source."ID") … UPDATE SET id =
:3` and Oracle answered ORA-38104 at execution. That is precisely the failure
`[C59.9]` exists to prevent, reached through the spelling it did not fold. One
identity rule for all five checks is the only version of this decision that is
internally consistent.

Ordering is the load-bearing part. Because rendering is settled first, a key that
reaches the identity helper from `BuildUpsert` renders either with no quote at all
or beginning AND ending with one — the two shapes whose "is this quoted" answer a
first-and-last-byte test gets right. The rendering that defeats it, quoted
somewhere in between, cannot survive the name check.

The identity rule is vendor-keyed and so leaves PostgreSQL exactly as it was:
there a key is its own identity, map keys are unique, and the membership and
overlap checks compare the keys themselves, which is what they already did. The
single-column-name check is Oracle-only in its qualifier, function and empty-name
rules, self-gated the way `upsertColumnName` and `quoteOracleColumn` are — with
one deliberate exception: the quote rule runs on both vendors. That clause is not
Oracle grammar. `EscapeIdentifier` wraps a key in quotes without doubling the
ones inside it, exactly as `oracleQuoteIdentifier` does, so the same key leaves
the identifier on PostgreSQL and becomes SQL there too. Nothing legitimate
carries a bare quote, so refusing it costs no working call on either vendor —
which is what separates it from the dotted key, where PostgreSQL renders
something legal and Oracle does not. That is not an endorsement of the
same key on PostgreSQL: its escaper splits a dotted key on the dot and quotes each
part, so `{"t.name": 1}` renders `"t"."name"` — a qualified reference, and as a
conflict target an `ON CONFLICT ("t"."name")` its grammar does not accept. It is
left alone because refusing it there is a second break on a second vendor, one
that issue #997 neither reported nor evidenced.

## Alternatives considered

**Keep `columnIdentity` for the membership and overlap checks and let the new
rule govern distinctness alone.** The narrower change, and the one this ADR
originally made. It is wrong: two identity rules inside one call disagree about
which keys are one column, and the disagreement is not academic — it lets a
conflict column spelled `"ID"` be updated as `id`, which is ORA-38104 at
execution and the exact failure `[C59.9]` was written to stop. The flip it
avoided — a shipped check changing outcome — is documented in `[C60.11]` instead,
where the rest of this hop's Oracle-identity change already lives.

**Repair `columnIdentity`'s position-0 quote test in place.** Unnecessary once
every caller keys on the named column: the function has none left, so it is
deleted rather than fixed. The rendering shape that defeated it is refused by the
single-column-name check ahead of it either way.

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
own sake, and not symmetric in fact: the two vendors render such a key differently
and it fails differently, if at all. The evidence in #997 is entirely Oracle's
MERGE grammar. If PostgreSQL's rendering deserves a precondition, it deserves its
own issue and its own atom rather than a second break smuggled in on this one.

## Consequences

**Positive.** The three repro shapes fail at build time, naming both colliding
keys, instead of reaching the database as unparseable SQL, and so does the fourth
the reviewers found: a quoted key colliding with the unquoted one Oracle folds
onto it. `[C59.10]`'s stated scope limit is closed for the column maps it named.
The exported contract now says what it enforces, and one identity rule answers
every question `BuildUpsert` asks about a column — the rendering comparison that
disagreed with it is deleted, not merely bypassed.

**Negative.** This adds a precondition to a shipped exported API: a call that
previously returned SQL now returns an error. Nearly every such call was already
producing SQL Oracle rejects at parse time, so almost nothing that worked changes.
One shape did work: an update key qualified with the MERGE's own `target` alias —
`{"target.name": …}` — rendered `UPDATE SET target.name = :3`, which Oracle
accepts. It is refused anyway, because that spelling depends on an alias
`buildOracleMerge` hardcodes and no contract publishes, and naming the column
alone builds the same statement. Folding membership and overlap onto the same rule
flips two more Oracle outcomes: a conflict column spelled differently from the
insert key naming the same column is now accepted where `[C59.7]` refused it, and
one spelled differently from an *update* key naming the same column is now refused
where `[C59.9]` let it build — that second call was reaching Oracle and failing
with ORA-38104, so what changes is where it fails, not whether. Beyond that, a
caller treating a builder error as control flow sees a new one, and a test pinning
either old outcome fails. Documented as `[C60.11]`.

**Neutral, and stated because the upsert path is not the whole surface.** The
quote rule above closes this builder's upsert entry: no `insertColumns`,
`updateColumns` or `conflictColumns` key can now carry an undoubled quote into
the statement. It closes nothing else. `oracleQuoteIdentifier` still fails to
double embedded quotes for every other caller, `EscapeIdentifier` has the same
gap on PostgreSQL, and `BuildUpsert`'s `table` argument goes through
`quoteTableForQuery` with no validation at all — a table name alone can take the
whole statement over. That is the actual injection boundary in this package and
it is filed separately; it is reachable only where an application feeds
request-derived identifiers into a builder, which is a shape worth auditing on
its own rather than patching through one precondition. Tracked as issue #1104. In the
other direction, a caller-quoted key containing a dot (`"a.b"`) is now rejected
even though `SELECT :1 AS "a.b"` would be legal Oracle: `oracleQuoteIdentifier`
splits on the dot before it notices the surrounding quotes, so the builder cannot
render that column correctly today. Rejecting it is the honest outcome until the
renderer is fixed; accepting it would emit `""a"."b""`.

## Migration Impact

Breaking for the calls described above; see `[C60.11]` in
[migrations.md](migrations.md) for detection, gate and remedy. Every identity rule
here is Oracle's: **PostgreSQL is unchanged except for one clause**, the rejection
of a key carrying an unescaped interior quote, which runs on both vendors because
`EscapeIdentifier` fails to double those quotes exactly as `oracleQuoteIdentifier`
does. The qualifier, function-call, empty-name and vendor-identity rules are
Oracle-only. No change to any call whose column keys are already distinct single
names.
