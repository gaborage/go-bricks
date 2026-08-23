# ADR-082: Identifier arguments are validated at every door, and the renderer escapes wherever it quotes

**Status:** Accepted
**Date:** 2026-08-23
**Supersedes:** the Filter exclusion in [ADR-031](adr_031_query_builder_identifier_validation.md)
**Related:** [ADR-071](adr_071_upsert_column_sets_name_each_column_once.md) · issues #1104, #1143, #1149, #1150

## Context

ADR-031 closed the M9 identifier-injection class on `From`, the JOIN table family,
`OrderBy`, `GroupBy`, `UpdateQueryBuilder.Set`/`SetMap` and
`DeleteQueryBuilder.OrderBy`. It excluded the Filter API, in two sentences:

> The values flowing through the *Filter* API (`f.Eq`, etc.) were never affected —
> those are already parameterized. (`adr_031:17`)
>
> The Filter API (`f.Eq`, `f.In`, …) and parameterized values are unaffected.
> (`adr_031:44`)

`f.Eq(column, value)` takes **two** arguments. The value is parameterized. The
column is interpolated, through `quoteColumnForQuery`, whose `default:` branch
returns it verbatim — the exact pathology `adr_031:8` describes for the APIs the
same ADR did fix. "Parameterizes its values" was read as "is safe", and one
sentence excluded thirty methods. It is also why `wiki/database.md`'s Identifier
Validation section lists the guarded doors and says nothing about the rest: the
ADR had reported there was nothing to say.

Separately, both renderers wrap an identifier in quotes without doubling the
quotes inside it (#1104). A key spelled `role" = 'admin', "name` rendered as
`"role" = 'admin', "name"` — not a column, but a second assignment in a position
no bind parameter guards. ADR-071 closed that for the upsert entry alone, by
refusing to name what the builder could not render.

Two defects, one boundary: what a caller may put in a slot that becomes SQL
syntax rather than a bound value. `CONTEXT.md` now names those two things
apart — *identifier argument* and *bound value* — so the conflation above cannot
be written again without contradicting the glossary.

## Decision

**Validate every identifier argument against the ADR-031 grammar at the door that
interpolates it, and escape wherever a renderer already emits quotes.** Both, not
either.

Escaping alone is not enough: it leaves `"role" = 'admin', "name"` a *legal*
column name, so the builder would faithfully emit an absurd identifier instead of
refusing a nonsense one. Validation alone is not enough either: it leaves the
renderer wrong for every caller reached by a door validation has not covered yet,
and for the `db`-tag path, which validates at struct-definition time under a
different alphabet.

**Escaping stays narrow.** Quotes are doubled where the renderer already quotes —
`oracleQuoteIdentifier` and `EscapeIdentifier`. PostgreSQL identifiers that are
bare today stay bare: quoting a valid unquoted identifier there changes which
physical column is referenced, the case-folding regression class `adr_031:17`
already refused (cf. ADR-007/M7). The escape collapses before it doubles, because
a key arrives in escaped form — `a""b` denotes the one-quote name `a"b`, the
reading `upsertColumnName` applies — so the pass is idempotent and an
already-escaped key is not renamed.

**Table arguments are identifier arguments.** `Insert`, `InsertWithColumns`,
`InsertStruct`, `InsertFields` and `BuildUpsert` validate their table the way
`From`, `Update` and `Delete` already do. A table sits first in the statement,
where a trailing comment takes the rest of it.

**A door that takes a predicate is not an identifier door.** `Having` interpolates
a caller-supplied predicate, which no identifier grammar can accept without
rejecting every real use. Validating it would repeat this ADR's own root cause in
the opposite direction. It is a raw-SQL door and is documented as one (#1146),
and the missing `qb.Expr` path it needs first is #1147.

**`Select` gains a fourth identifier context.** `table`, `identifier` and `clause`
are ADR-031's; `select` is those plus a wildcard production (`*`, `table.*`),
because `Select("*")` is the documented idiom. `Select("1")` is a constant rather
than an identifier and moves to `qb.Expr("1")`.

The decision lands in stages, each shipping alone: the renderer escape and the
table arguments first (this change, closing #1104), then the `select` context and
the remaining `Select`/`Insert` column doors, then the Filter and JoinFilter
column arguments (#1143). A census of the tracked tree found 1027 Filter call
sites with a literal column and **zero** outside the grammar, so that last stage —
the widest — breaks nothing.

**A known gap the escape does not close.** `oracleQuoteIdentifier` returns its
input verbatim when `isSQLFunction` says it is a function call, before any
escaping — and that test inspects only the text before the first parenthesis and
whether parentheses balance, never what follows the closing one. So a
function-shaped column argument still reaches Oracle SQL unescaped:
`qb.Select("NVL(null,1) FROM dual UNION SELECT password FROM app_users--")`
emits that string into the statement. The gap predates this change and is not
widened by it, but it does bound what remedy (b) buys on its own: until the
column doors validate, the renderer is correct for every shape *except* a
function-shaped one. Constraining `isSQLFunction` is not a free tightening —
`SUM(amount) AS revenue` and `COUNT(o.id) AS order_count` pass through it today
and are legitimate — so what counts as a function expression is its own decision,
tracked separately (#1149) rather than settled here.

**The later stages must not be thirty hand-written guards.** A per-door guard is
the right shape for this change — it is auditable in a minute and revertible on
its own — but it does not scale to the ~30 Filter and JoinFilter methods, and a
door added later is a door whose guard someone must remember. Both funnels
already exist: every table reaches SQL through `quoteTableForQuery` and every
column through `quoteColumnForQuery`. Giving those two `(string, error)` is an
internal-package change with no apidiff cost, and it makes "a door that forgot"
stop being expressible rather than merely absent. Whichever stage carries it must
come *before* the Filter sweep, or the guards it would delete get written first.

**Not decided here:** whether a column identifier should become a distinct type
rather than a `string`. `Col()` returns a plain `string` today, so nothing in any
signature separates a struct-tag-derived column from an arbitrary one. That is the
end state the **Type Safety > Dynamic Hacks** principle points at, and it deserves
its own ADR rather than being welded to a security fix.

## Consequences

**Breaking (intended):**

- `Insert`, `InsertWithColumns`, `InsertStruct` and `InsertFields` return a
  `ToSQL()` error for a table argument that is not a bare or qualified identifier
  with at most one alias. Previously interpolated unchecked.
- `BuildUpsert` refuses the same argument, but reports it as its own third return
  value rather than through `ToSQL()` — it builds a statement directly and has no
  deferred-error builder to carry one.
- An upsert column key carrying a quote that is neither half of a doubled escape
  nor the wrapper of a well-formed quoted identifier is still refused. ADR-071
  refused it because the renderer could not render it; the renderer can now, so
  the rule moved to read the key rather than its rendering. Behavior is
  unchanged — this is stated because the reason changed.

**Silent-behavior:**

- `EscapeIdentifier` and `oracleQuoteIdentifier` now double an interior quote.
  A name that carried one used to emit SQL that either failed at parse or, in the
  shapes above, executed as something else. No caller passing a legal identifier
  sees a difference.

**Non-breaking:**

- Bare, qualified and framework-quoted identifiers, inline table aliases, and
  every already-escaped key render exactly as before.
- PostgreSQL identifier quoting is unchanged: valid identifiers stay unquoted.

See [migrations.md](migrations.md) for the atoms and
[database.md](database.md#identifier-validation-adr-031) for the developer-facing
grammar.
