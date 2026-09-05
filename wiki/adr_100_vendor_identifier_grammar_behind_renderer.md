# ADR-100: The Vendor's Identifier Grammar Lives Behind the Renderer Seam

- **Status**: Accepted
- **Date**: 2026-09-05
- **Related**: [ADR-031](adr_031_query_builder_identifier_validation.md) (identifier
  arguments are validated before interpolation),
  [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md) (one funnel per
  door, not a per-door guard), [ADR-098](adr_098_builder_clauses_for_ledger_stores.md)
  (the renderer seam this extends)
- **Issue**: #1202; the byte-cap half is deferred to #1437

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
  #1437.
- **A vendor `switch` at each door.** Rejected as the ADR-082 defect this seam
  exists to prevent — the divergence that record names began as exactly that.

## Consequences

- A PostgreSQL consumer passing an identifier argument containing `#` now gets a
  `ToSQL()` error where the statement previously reached the server and failed
  there. Renaming the column or quoting it (`"a#b"`) both work; the migration
  atom is `[C64.3]`.
- Oracle behavior is unchanged, which is the point of putting the rule behind the
  vendor seam rather than in the shared lexer.
- **Residual: the INSERT struct doors do not judge db-tag names.** `InsertStruct`
  and `InsertFields` render struct-derived columns through `quoteColumnsForDML`
  without passing a validator, so a `db:"a#b"` tag still reaches PostgreSQL
  unquoted and fails at execution (`database/internal/builder/query_builder.go:395`
  and `:435`, both via `:705`). `UpdateQueryBuilder.SetStruct` is NOT in this
  residual: it routes every column through `setColumn` → `quoteColumnForQuery`,
  so a `#` tag is refused there like any other column — the three struct doors
  do not agree, which is itself the argument for closing this. Tag names are
  developer constants judged by the `columns` package against the union alphabet,
  and closing it means threading a vendor into that package — out of scope here,
  tracked as #1449, and pinned by a test that RECORDS the current split so the
  day it changes is visible.
- **Residual: byte caps are still unenforced at the doors** (#1437). On
  PostgreSQL an over-long name is silently truncated at 63 bytes, so two names
  sharing a prefix can collapse onto one object.
- A third vendor would arrive as its own renderer with its own `ValidateSegment`,
  and `database/identifier` is the one place its alphabet would be written down.

## References

- [migrations.md](migrations.md) `[C64.3]`
- `database/identifier/identifier.go` (`ValidateCharset`),
  `database/internal/builder/renderer.go` (`ValidateSegment`),
  `database/internal/builder/identifiers.go` (`validateVendorSegments`)
- Issues #1202, #1311, #1437 (byte caps), #1449 (struct db-tag names)
