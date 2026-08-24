# ADR-082: Identifier arguments are validated at every door, and the renderer escapes wherever it quotes

**Status:** Accepted
**Date:** 2026-08-23
**Supersedes:** the Filter exclusion in [ADR-031](adr_031_query_builder_identifier_validation.md)
**Related:** [ADR-071](adr_071_upsert_column_sets_name_each_column_once.md) · issues #1104, #1143, #1149, #1150, #1151, #1153, #1154

## Addendum (2026-08-23): the escape hatch validates at consumption too

`RawExpression` is a plain exported struct, so `qb.Expr()` was a door a caller
could walk around: `dbtypes.RawExpression{SQL: "1", Alias: "x FROM users; DROP
TABLE t--"}` reached `Select` carrying an alias the constructor refuses, and
`Select` interpolated it as `AS <alias>` verbatim (#1153). The decision above says
identifier arguments are validated at the door that interpolates them; this
extends the same rule to the escape hatch's own metadata, which the ADR had left
with a construction-time-only guard.

**The funnel is `RawExpression.Validate() error`**, exported on the type in
`database/types/expression.go`. `Expr()` calls it at construction and returns what
it returns; `QueryBuilder.Select`, `SelectQueryBuilder.GroupBy`,
`SelectQueryBuilder.OrderBy` and the `JoinFilterFactory` value doors (`Eq`,
`NotEq`, `Lt`, `Lte`, `Gt`, `Gte`, and both bounds of `Between`) call it again at
consumption, where a struct literal is indistinguishable from a constructed value.
One copy of the denylist, two call sites for it.

What it checks is unchanged and deliberately narrow: empty-or-whitespace SQL, and
an alias containing `;`, `'`, `"`, `--`, `/*` or `*/`. **The SQL body is still not
validated** — that is what the hatch is for, and validating it would repeat this
ADR's own root cause the way `Having` would.

A consumer records the violation on the builder's existing deferred error, first
violation wins, and never panics — the ADR-031 split, unchanged. The struct keeps
its exported fields: a literal still compiles, it just no longer renders.

**Residual, recorded rather than closed.** The alias check is a DENYLIST, not a
grammar: an alias carrying none of the six sequences still reaches the SELECT
list verbatim, so `Alias: "a, (SELECT password FROM users) b"` renders. Making
the alias an identifier — the grammar treatment `Select`'s string columns got
above — is a separate, larger break, and #1153 asked only that the two
construction paths converge. They now do: whatever `Expr()` refuses, a literal
refuses. What `Expr()` accepts is unchanged and remains developer-controlled
input by contract.

See `[C60.29]` in [migrations.md](migrations.md).

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
because `Select("*")` is the documented idiom. Everything else a caller wants in
a SELECT list — a function, a cast, an alias, a bare constant — is not an
identifier and goes through `qb.Expr()`, which is where ADR-031 already sends
`OrderBy("COUNT(*) DESC")`.

That breaks more than the wildcard census suggested. A grep for out-of-grammar
`Select` string arguments finds `*` and `1` and reads as a small change; it misses
every multi-argument call, which is where the function strings live —
`Select("department", "COUNT(*)")`, `Select(colID, colName, "COUNT(o.id) AS
order_count")`. Running the validation is what enumerates them. So the breaking
surface is three shapes, not one: the `EXISTS` idiom `Select("1")`, bare function
strings such as `Select("COUNT(*)")`, and any function-with-alias string. Each
moves to `qb.Expr(...)`, and `Select("*")` continues to build untouched.

One consequence worth stating rather than discovering: a caller writing `SELECT 1`
in an `EXISTS` subquery now reaches for the same tool as one writing a computed
expression. `qb.Expr` carries a security WARNING in its own godoc — never
interpolate user input into it — but not the `// SECURITY:` annotation
requirement, which names `f.Raw()`, `jf.Raw()` and `database.Raw()` and covers
those three alone. The cost here is the reach for a hatch at all, not an audit
obligation, and it is a cost of keeping the grammar honest about what an
identifier is rather than an argument that a constant is one.

The decision lands in stages, each shipping alone: the renderer escape and the
table arguments first (closing #1104), then the `select` context and the
remaining `Select`/`Insert` column doors, then the Filter and JoinFilter column
arguments on a fallible funnel (#1143). A census of the tracked tree found 1027 Filter call
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

**"Every door" means every door that takes an identifier ARGUMENT.** Two shapes sit
outside the rule and are named here rather than left for a reader to notice.
`BuildUpsert`'s three column maps are not checked against the identifier grammar —
they answer to the upsert's own preconditions instead (`requireSingleColumnNames`,
`requireDistinctColumnIdentities`), which ask a stricter question: not merely "is
this an identifier" but "is this one column this vendor can name in a MERGE".
And `qb.Expr()`/`RawExpression` is the declared escape hatch: it is meant to carry
SQL the grammar refuses, which is the point of having it. Neither is an oversight;
both would be, if unstated.

**The later stages must not be thirty hand-written guards.** A per-door guard is
the right shape for the first stage — auditable in a minute, revertible on its
own — but it does not scale to the 36 Filter and JoinFilter column doors, and a
door added later is a door whose guard someone must remember. So the final stage
makes `quoteColumnForQuery` — the single point every column argument passes
through before becoming SQL — return `(string, error)`. It is an internal-package
change with no apidiff cost, and the compiler then enumerates all 43 call sites
rather than a reviewer noticing which one is missing.

**What that buys, and what it does not.** A fallible funnel points at every door
that CALLS the funnel. It cannot point at one that bypasses it.
`BuildCaseInsensitiveLike` did exactly that: on PostgreSQL and in its default
branch it used the caller's column verbatim as a `squirrel` map key, never
touching `quoteColumnForQuery` at all. The signature change did not flag it —
a test did. So the funnel is worth having and is not a proof: the remaining
question for any identifier door is not "does it handle the error" but "does it
go through the funnel", and only reading the renderers answers that.

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
- Every `FilterFactory` and `JoinFilterFactory` column argument is validated, so a
  filter column that is not a bare or qualified identifier fails at `ToSQL()`. On
  PostgreSQL this closes a live hole rather than a latent one: the renderer's
  default branch emitted the column verbatim, so `f.Eq("id = 1 OR 1=1 -- ", v)`
  built `WHERE id = 1 OR 1=1 -- = $1`.
- `Select` refuses a string column that is not an identifier or a wildcard, and
  `InsertWithColumns`, `InsertQueryBuilder.Columns` and `InsertQueryBuilder.SetMap`
  refuse one that is not an identifier. Expressions move to `qb.Expr()`.
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

One thing the staging surfaced, worth recording because it is easy to repeat: the
PostgreSQL upsert built its INSERT through the PUBLIC `qb.Insert(table).Columns(...)`
door "for consistency". Once that door validates, the builder was judging its own
ESCAPED output — `"a""b"` — by the grammar meant for a caller's raw input, and
refusing it. It now builds the statement directly, which also stops it validating
the same table twice. A validating door is for arguments crossing INTO the
builder; internal paths that have already validated and already rendered must not
re-enter it.

See [migrations.md](migrations.md) for the atoms and
[database.md](database.md#identifier-validation-adr-031-adr-082) for the developer-facing
grammar.

## Addendum (2026-08-23): `Columns.As` is a validated door, and it panics

`ColumnMetadata.As(alias)` checked only that the alias was non-empty, then every
`Col`/`Cols`/`All`/`FieldMap`/`AllFields` rendering emitted `alias + "." + column`
verbatim. As filed, an alias of `id FROM secrets--` produced
`SELECT id FROM secrets--.id FROM users` on PostgreSQL, which executes as
`SELECT id FROM secrets` — a working table swap; Oracle quoted the alias and
contained it (#1150).

**What the earlier stages already changed about that repro, measured rather than
assumed.** By the time this door lands, `Select` refuses the RENDERED string
`id FROM secrets--.id` on both vendors, and so do the Filter columns and the
INSERT column lists. So the filed repro no longer builds. What survives is the
shape of the refusal: the alias is caught LATE, by whichever door the rendered
column happens to reach, and only if that door validates. `Having` does not and
is not meant to — it is a raw-SQL door (#1146), and
`Having(u.Col("ID"))` still carries the alias into the statement unexamined, as
does anything a consumer renders into its own SQL. Validating at `As` is what
makes the door that OWNS the alias refuse it, rather than leaving the outcome to
where the string later lands.

The door was missed by the census that produced this ADR because the sweep was
scoped by *name*: `As` is not a `FilterFactory` method, not a `Select` column, and
not a table argument. That an alias is an identifier argument is what the
`CONTEXT.md` glossary says; that no sweep asked the glossary's question is the
gap. **An identifier context belongs to the door, not to the door's package** —
the alias half of `"users u"` and the alias handed to `As` are the same production
whether or not one grep finds both.

**`As` validates against the ADR-031 bare-identifier grammar and panics on a
violation.** It returns a `Columns`, not a builder, so it has no deferred-error
channel and cannot join the `ToSQL()` route the rest of this ADR uses; giving it
one would change the `Columns` interface. A panic is also what the door already
did for an empty alias, so the contract widens rather than changes shape: an
alias is a developer constant, and a violation is a programming error at
construction.

**One guard covers five renderings because it sits on the only writer.** `Col`,
`Cols`, `All`, `FieldMap` and `AllFields` each concatenate `alias + "." + column`,
and none of them is guarded. They do not need to be: `alias` is an unexported
field, and the package constructs a `ColumnMetadata` in exactly two places — the
parser, which sets it to `""`, and `As`. So no value reaches those five sites
without passing the door. That is the property to re-check before adding a
third constructor or an exported setter, not the five render sites themselves —
this ADR's funnel argument applies to doors that take an argument, and here there
is only one.

**The panic value is a typed error, `*dbtypes.InvalidAliasError`, not a string.**
ADR-081 requires a recovery site to report a recovered value by TYPE; a bare
string reports as `string`, which names nothing, and rendering it by value would
put the refused alias — the one caller-derived thing in the frame — into a log
line. The type carries the alias in a field a caller can read deliberately via
`errors.As`, and says nothing when reported by `%T`.

**The grammar moved rather than being copied.** The predicate now lives in
`database/internal/sqllex`, and every judge inside `database/` imports it: the
builder cannot own it because the columns package cannot import the builder (the
builder imports the columns package), and a second copy of an injection-boundary
grammar is the defect this ADR exists to stop. The move also collapsed a copy that
predates this door — the columns package's own `validDBTagPattern`, which spelled
the same alphabet a third time for `db` tags. One copy remains and cannot be
collapsed: `internal/sqlid` sits outside `database/`, so Go's internal-package
visibility forbids it importing `database/internal/sqllex` at all. Naming that
here is the point — an uncollapsible copy that nobody has written down is how the
two drift. `As` deliberately does NOT trim its argument before
matching — validating a trimmed value while rendering the untrimmed one is the
disagreement `validateSelectIdentifier` documents above, so `As(" u ")` is
refused rather than silently accepted and rendered with its spaces.

**Consequence (breaking):** `cols.As(alias)` panics for any alias that is not a
single bare or framework-quoted identifier. The empty-alias panic value changes
from the string `"alias cannot be empty"` to `*dbtypes.InvalidAliasError`, so a
test asserting the old value fails. See `[C60.28]`.
