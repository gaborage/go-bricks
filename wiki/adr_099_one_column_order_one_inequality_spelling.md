# ADR-099: One Column Order for Map-Shaped Doors, One Inequality Spelling for the Filter Families

- **Status**: Accepted
- **Date**: 2026-09-05
- **Related**: [ADR-031](adr_031_query_builder_identifier_validation.md) and
  [ADR-082](adr_082_identifier_arguments_validated_at_every_door.md) (one funnel per
  identifier argument, and the divergence class this record extends to RENDERING),
  [ADR-017](adr_017_insert_query_builder.md) (the insert builder's `SetMap`)
- **Issue**: #1185 (Oracle `SetMap` SET-pair order), #1200 (`jf.NotEq` spelling)

## Context

Two doors take the same argument — a `map[string]any` of column to value — and
rendered it in two different orders. `InsertQueryBuilder.SetMap` sorted the
caller's names and quoted in that order; `UpdateQueryBuilder.SetMap` quoted
first and sorted the QUOTED spellings. On PostgreSQL, which renders a column
verbatim, the two orders coincide and nothing is visible. On Oracle a reserved
word gains a leading double quote (0x22), which sorts before every letter, so
the same map rendered `("level","size",id)` at one door and `id, "level", "size"`
at the other. The order is not cosmetic: it fixes the bind positions, so a
golden-SQL assertion and its argument slice both move with it.

Separately, the two filter families spelled scalar inequality differently.
`f.NotEq` delegates to `squirrel.NotEq`, which writes `<>`; `jf.NotEq` and
`jff.NotEqColumn` built their own fragment and wrote `!=`. #1167 had already
aligned the two families on MEANING for nil and list operands, leaving spelling
as the last difference — one an in-code comment argued was the smaller debt.

Both are the failure ADR-082 names: one shape, two implementations, no signature
telling them apart, and only a vendor-specific or operand-specific test able to
see the difference.

## Decision

**One ordering rule for every map-shaped column door.** A shared helper,
`quotedColumnsInNameOrder(context, clauses)`, sorts by the caller's names,
validates each key under the CALLER's error context, and renders the normalized
spelling through the vendor renderer — in that order. Both `SetMap` doors call
it; neither sorts, validates or quotes on its own. Sorting names before quoting
is what makes the emitted order vendor-independent, and validating in that order
is what keeps WHICH invalid key is reported deterministic. The returned keys stay
the caller's own spelling, so values still follow them and two keys differing
only in padding remain two cells for the database to reject.

**One inequality spelling across the filter families.** `opNotEqual` is `<>`, and
`NotEqColumn` renders through that constant rather than its own literal, so
`jf.NotEq`, `jff.NotEqColumn` and `f.NotEq` all emit `<>`. Nil and list operands
are untouched — they already delegated to `squirrel.Eq`/`NotEq` since #1167.

Both are breaking in rendered SQL and ship as one migration event, `[C64.1]`.

## Alternatives considered

- **Keep the quoted-key order in UPDATE and document the difference.** Rejected:
  it is the ADR-082 class of defect — the divergence is invisible on the vendor
  most consumers develop against and appears only on Oracle, and a third
  map-shaped door would have had no answer to copy.
- **Order by the caller's names in UPDATE only, leaving INSERT as it is.** Both
  doors already agreed on name order in INSERT, so this is the same fix; without
  a shared helper, nothing stops the two from drifting again, which is what the
  in-code comments at both doors had been asserting since #1332.
- **Leave `jf.NotEq` at `!=` (document-only).** Rejected by the maintainer on
  2026-09-01: `!=` and `<>` mean the same thing to every supported vendor, so the
  difference buys nothing and costs a reader one more rule to hold.
- **Delegate scalars to `squirrel.NotEq` as well.** Rejected: the scalar path
  also serves the ordering operators, and delegation would fork that one code
  path for a token the constant already supplies.

## Consequences

- Multi-column Oracle `SetMap` UPDATEs containing a reserved word render their
  SET pairs in a different order, and the bind arguments move with them. A
  golden-SQL test, a query-text matcher or a driver-level capture that pins the
  old order fails until it is updated; the statement's effect is unchanged.
- `jf.NotEq(col, scalar)` and `jff.NotEqColumn(l, r)` render `<>`. Any assertion
  matching the literal `!=` in generated SQL fails; execution is unaffected on
  PostgreSQL and Oracle, both of which accept either spelling.
- PostgreSQL `SetMap` output is byte-identical to v0.63.0 — pinned by a
  regression case in the same golden pair as the Oracle change.
- A third map-shaped door (a future upsert or merge shape) has one helper to
  call, and no reviewer has to notice that it invented its own order.
- `BuildUpsert`'s column ordering keeps its own ON-clause constraints and is out
  of scope here.

## References

- [migrations.md](migrations.md) `[C64.1]`
- `database/internal/builder/helpers.go` (`quotedColumnsInNameOrder`),
  `database/internal/builder/query_builder.go` (both `SetMap` doors),
  `database/internal/builder/join_filter.go` (`opNotEqual`, `NotEqColumn`)
- Issues #1185, #1200; the maintainer's pairing ruling, 2026-09-01
