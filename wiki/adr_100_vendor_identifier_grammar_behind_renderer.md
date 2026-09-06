# ADR-100: The Vendor's Identifier Grammar Lives Behind the Renderer Seam

- **Status**: Accepted
- **Date**: 2026-09-05
- **Related**: [ADR-031](adr_031_query_builder_identifier_validation.md) (identifier
  arguments are validated before interpolation),
  [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md) (one funnel per
  door, not a per-door guard), [ADR-098](adr_098_builder_clauses_for_ledger_stores.md)
  (the renderer seam this extends)
- **Issue**: #1202; the byte-cap half landed in #1437

## Amendment (2026-09-06): the doors judge the byte cap per segment

The Consequences below recorded a second residual: the doors judged the vendor's
alphabet but not its length limit, so PostgreSQL silently truncated an over-long
name at 63 bytes and two names sharing a prefix could collapse onto one object.
Issue #1437 closes it. The renderer gained `MaxBytes()` — 63 on PostgreSQL, 128
on Oracle — so the unknown-vendor class inherits PostgreSQL's 63 the same way it
inherits PostgreSQL's alphabet, and one builder funnel (`validateSegment`) does
the judging for every door. The cap is judged PER SEGMENT and never on the
rendered whole: an 81-byte `schema.table.column` whose segments are each legal is
accepted, because it is the object names the server bounds, not the reference.
A quoted segment's INTERIOR is capped too — quoting escapes the alphabet, not the
length — so the charset exemption this ADR blessed stays exactly as it was. The
cap is judged BEFORE the charset, so an over-long name that also carries a bad
character reports too-long rather than the character. A refusal names the
argument, the limit and the vendor, and is never a panic: on the fluent builders
it is the deferred `ToSQL()` error, while `BuildUpsert` — which is not fluent —
returns it directly as its third result.
Forward-binding rule: an alias the FRAMEWORK derives is subject to the same cap.
That is vacuous today — the Oracle MERGE emits only the fixed `target`/`source`
literals — but it binds the next derived name rather than leaving it to be
argued.

Second clause, outside the builder: the inbox store's table-name bound now
follows the STORE's vendor — PostgreSQL 49, Oracle still 114, both derived as
the vendor's cap minus `len("idx__processed")`, the longest name the store
derives from the table name. `sqlid.ValidateTableName` stays at Oracle's 128 for
every vendor, because its callers judge configuration before a connection
exists and have no vendor in scope; the doors, which do, are the vendor-aware
gate.

## Amendment (2026-09-06): the INSERT struct doors judge db-tag names too

The Consequences below recorded a residual: `InsertStruct` and `InsertFields`
rendered struct-derived columns without the vendor grammar, so a `db:"a#b"` tag
built `err == nil` and SQL PostgreSQL rejected, while `SetStruct` refused the
same tag. #1449 closes it: both doors now run the same
`validateInsertColumns` funnel `InsertWithColumns` uses and render the
normalized slice it returns. All three struct doors now agree on every vendor.
On Oracle the effect is narrower than the sentence above suggests: the `columns`
registry has already quoted a reserved-word tag by the time the door sees it, and
a quoted segment is exempt from the charset check by construction — so the
vendor's alphabet binds on the segments the registry left bare.

The check lives at the door, not in the `columns` registry. The registry is
vendor-keyed, so a check there is feasible, but its only refusal channel is a
`panic` at first use, which contradicts the "never a panic" promise and would
demote `SetStruct`'s correct deferred error. Per ADR-082 (one funnel per
identifier argument, at the door) and ADR-031 (deferred, first violation wins),
the registry stays a parser of the union alphabet and the door judges the
vendor's. The `[C64.3]` atom is amended in place rather than superseded: the E64
hop is unreleased, so the residual never reached a consumer.

## Context

The query builder judged every identifier argument against ONE grammar,
`sqllex.IdentifierSegment` = `[A-Za-z_][A-Za-z0-9_$#]*`. That alphabet is the
UNION of what PostgreSQL and Oracle accept, and the two vendors disagree about
one character: `#` is an ordinary identifier character on Oracle and an operator
on PostgreSQL, where a name carrying one has to be quoted.

So `qb.MustExpr("1", "a#b")` rendered `SELECT 1 AS a#b` on PostgreSQL and the
statement failed at execution — a syntax error from the server, at run time,
naming a position rather than the argument the caller wrote. Every identifier
door had the same hole: columns, tables, inline and explicit aliases, clause
items, insert and SetMap keys, and the upsert doors' column keys.

The per-vendor truth already existed. `database/identifier` (#1311) holds each
vendor's charset and byte cap, and `migration/` already validates through it.
What was missing was a path from a builder door to its vendor's rule: the
validators were receiverless package functions, so no door could reach the
`QueryBuilder` that knew the vendor, let alone the renderer.

## Decision

**The renderer supplies the vendor's segment grammar; the door supplies the
identifier context.** `vendorRenderer` gains `ValidateSegment(segment string)
error`, implemented by each renderer over `identifier.ValidateCharset`.
Which characters a bare segment may carry is a vendor fact and belongs beside
the vendor's quoting; WHICH tokens of an argument are identifier positions — as
opposed to a direction keyword, an alias, or the wildcard — stays the door's
question, judged by the shape grammar as before.

Both grammars now run at every door, in that order: the shape pattern first,
then the vendor's alphabet on each unquoted segment of each identifier-bearing
token. The identifier-bearing tokens are read through the patterns' NAMED
groups, the same contract the Oracle clause and table renderers already read,
so a pattern that grows a group cannot silently change what gets judged.

Supporting decisions:

- **`identifier.ValidateCharset` is a new cap-free door.** `Validate` is now the
  cap check followed by `ValidateCharset`, so the grammar is defined once. The
  doors call the cap-free one deliberately — see Consequences.
- **Quoted segments keep the union grammar.** A quoted identifier is legal on
  both vendors whatever it contains, and it is the framework's own reserved-word
  form (`"level"`), so quoting remains the documented escape hatch for a name the
  vendor's bare alphabet refuses.
- **The wildcard is skipped**, being no identifier at all.
- **An unknown vendor gets PostgreSQL's grammar**, because `defaultRenderer`
  embeds `postgresRenderer` and that is already true of its identifier QUOTING.
  It is also the safe direction: a name this refuses can always be quoted.
- **`sqllex.IdentifierSegment` is unchanged** and its doc now says what it is —
  the SHAPE alphabet that finds segment boundaries, not a vendor's rule.
- **`RawExpression.Validate()` stays vendor-blind.** It is a method on an
  exported value type with no vendor in reach; the Select door applies the
  vendor check to the alias where it becomes SQL.

The refusal is a deferred `ToSQL()` error naming the argument, the vendor and the
offending segment — the same class as every other grammar rejection, never a
panic.

## Alternatives considered

- **Narrow `sqllex.IdentifierSegment` to the PostgreSQL alphabet.** Rejected: it
  is the segment-splitting grammar shared by both renderers and the `columns`
  package, so narrowing it would refuse legal Oracle names at every door.
- **Make `RawExpression.Validate()` vendor-aware.** Rejected: it would put a
  vendor parameter on an exported value type's method — a second breaking change
  for one alias, when the door already knows the vendor.
- **Enforce the byte cap in the same change** (call `Validate` and ignore
  `ErrIdentifierTooLong`). Rejected twice over: discarding a sentinel couples the
  door to the error taxonomy, and it would make the eventual cap enforcement a
  silent behavior flip. The cap needs its own decisions (per segment or per
  rendered whole, quoted names, framework-derived aliases) and has its own issue,
  #1437. Those decisions were taken there — per segment, quoted interiors
  included, cap before charset — and are recorded in the first Amendment above.
- **A vendor `switch` at each door.** Rejected as the ADR-082 defect this seam
  exists to prevent — the divergence that record names began as exactly that.

## Consequences

- A PostgreSQL consumer passing an identifier argument containing `#` now gets a
  `ToSQL()` error where the statement previously reached the server and failed
  there. Renaming the column or quoting it (`"a#b"`) both work; the migration
  atom is `[C64.3]`.
- Oracle behavior is unchanged, which is the point of putting the rule behind the
  vendor seam rather than in the shared lexer.
- **Closed by #1449 (see the Amendment above): the INSERT struct doors judge
  db-tag names**, so the three struct doors no longer disagree.
- **Closed by #1437 (see the first Amendment above): the doors judge the byte
  cap per segment**, so an over-long PostgreSQL name is refused at `ToSQL()`
  instead of being silently truncated at 63 bytes onto a colliding object.
- A third vendor would arrive as its own renderer with its own `ValidateSegment`,
  and `database/identifier` is the one place its alphabet would be written down.

## References

- [migrations.md](migrations.md) `[C64.3]`
- `database/identifier/identifier.go` (`ValidateCharset`),
  `database/internal/builder/renderer.go` (`ValidateSegment`),
  `database/internal/builder/identifiers.go` (`validateVendorSegments`)
- Issues #1202, #1311, #1437 (byte caps), #1449 (struct db-tag names, closed)
