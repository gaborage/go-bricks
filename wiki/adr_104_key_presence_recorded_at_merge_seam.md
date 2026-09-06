# ADR-104: Key Presence Is Recorded Once, at the Merge Seam

**Status:** Accepted
**Date:** 2026-09-06

## Context

Several doors need to know whether a configuration key was **delivered** by the
deployment's own layers rather than filled by a framework default. ADR-047 reads a
database section's absence that way; ADR-051 fails startup on a delivered-but-empty
identity; ADR-078 rejects a delivered-empty `debug.allowedips`; ADR-074 and ADR-077
reject delivered-empty numeric and bool scalars.

The rule had no single substrate. Four different ones answered it: the mapstructure
target kind inside the `internal/configdecode` hook, koanf's `Exists` (ADR-051), a raw
koanf `Get` (ADR-078), and the decoded value (`InjectInto`). Three of them went inert
on a `Config` built as a struct literal, each through its own `cfg.k == nil` guard —
an inertness documented in ADR-051's Consequences as "blind spot 1" and pinned by
`TestValidateNoDeliveredEmptyDatabaseInertForLiteral` and
`TestNormalizeLiteralDoorIncompleteSurfaces`, but never declared anywhere as a
property.

koanf keeps no per-layer provenance. Defaults are loaded first into the single
instance that user layers then merge onto, so there was no point at which the
user-delivered key set was observable. `Exists` therefore could not tell a preloaded
default from a delivery, and the framework bought the distinction with a deny-list:
`preloadDeniedPrefixes` (`database.`, `databases`, `multitenant.tenants`) told
`mergeDefaults` to skip preloading anything under the prefixes ADR-051's door reads.
A defaulting decision was constrained by an unrelated door's reading habit, and the
coupling lived only in a comment. ADR-078 could not use that seam at all — its key
carries a default, so `Exists` is always true — and reached for the raw tree instead.

## Decision

**Presence is recorded once, inside `config.Load`, at the merge seam.**

- **Each user-layer load carries a merge function** that records the leaf keys which
  actually reached the tree — after the existing scalar-over-map skip has decided —
  and then delegates to the existing merge. Base configuration, environment overlay
  and environment variables all record; defaults are loaded first exactly as before
  and record nothing. Presence is recorded for LEAF keys only — `database.host`, never
  `database` — so a section path is never delivered.
- **The koanf instance and the delivered set are one unexported source value**,
  attached at one point. A koanf cannot exist on a `Config` without the set it came
  with, so the two cannot drift and no door has to check for a half-built pair.
- **The literal source is a declared property.** A `Config` built as a struct literal
  carries no source, and a `Config` with no source delivers nothing: every key is
  absent. This replaces the per-door `if cfg.k == nil` skips and ADR-051's blind spot
  1 — the same behaviour, now stated once as a rule rather than reproduced as a guard.
- **Two doors ask Presence:** ADR-051's identity check, which asked `Exists` on the
  merged koanf, and ADR-078's `debug.allowedips` check, which read the raw tree
  through `Get`. Their emptiness rules and error text are unchanged; only the question
  "was this delivered" moves.
- **Four doors deliberately do not:** `InjectInto`, the lenient typed getters,
  `RequiredString`, and the `internal/configdecode` empty-scalar hook (ADR-074,
  ADR-077). Each asks "is there a resolvable value", which must include a preloaded
  default. That is a different question from Presence, and conflating them would make
  a defaulted key unreadable.
- **`preloadDeniedPrefixes` and its reader in `mergeDefaults` are retired.** The
  deny-list existed solely so `Exists` would not read a preloaded default as a
  delivery. With Presence recorded at the seam, preloading cannot fake delivery.
  `derivationDeniedPrefixes` stays: it also serves debug fail-closed behaviour and
  tri-state semantics, which are not this question.

The change is non-breaking. No exported identifier moves and no error string changes,
so there is no migrations atom and no `fix(scope)!:`.

## Alternatives

**A — a shadow koanf holding only the user layers.** Load the user layers a second
time into a separate instance and treat presence in that instance as delivery. Simple,
and it needs no merge hook. Rejected because the environment layer's scalar-over-map
skip would then judge against a tree with no defaults in it, so one edge case decides
differently in the shadow instance than in the real one. Presence would describe a
tree nothing consumes.

**B — co-locate the existing substrates.** Keep `Exists` and the raw `Get` and put
both behind one function that declares `preloadDeniedPrefixes` as its dependency.
Rejected: it buys locality and nothing else. The deny-list survives, so a defaulting
decision anywhere under those three prefixes still has to be checked against a door
that reads them.

**C — a three-arm Presence that also judges emptiness.** Have Presence answer absent,
delivered-empty or delivered-with-value from the raw value under one shared emptiness
rule. Deeper, and it would collapse two concepts into one. Rejected because the
doors' emptiness rules genuinely differ: a whitespace-only identity value would flip
from "attempt the connection" to "rejected", which is a behaviour change this
refactor forbids. Emptiness stays each door's own.

**D — fail `Validate` when a koanf carries no recorded set.** A guard against a koanf
attached outside `Load`. Unnecessary once the source and the set are one value: the
only path that attached a koanf without `Load` was a getters test helper, and it now
attaches the pair.

## Consequences

- **Defaults may be preloaded under any prefix, but still never under an identity
  key.** Preloading a `database.*` or `multitenant.tenants.*` default no longer
  risks reading as a delivery, so the defaults table is decided on its own merits —
  with one invariant that outlives the deny-list: `IsDatabaseConfigured` reads the
  DECODED section, so a default written under a database identity key would turn
  ADR-047 absence into an attempted connection. That is pinned by
  `TestLoadDefaultsCarryNoDatabaseIdentityKeys`, not by a prefix list.
- **A presence-sensitive door asks Presence, never `Exists`.** `Exists` answers "is
  there a resolvable value" on the merged tree, which includes defaults; it is the
  right question for the four doors listed above and the wrong one for a delivery
  check. A new door of this family joins by asking Presence.
- **Literal configurations are declared absent**, so ADR-047's absence verdict holds
  on the literal path by construction rather than by three separate nil guards. A
  test that builds a `Config` literal still gets an inert delivery check, and now for
  a stated reason.
- The delivered set is recorded for every user layer whether or not a door reads it,
  which costs one map of leaf keys per load. `config.Load` is a startup-only path.

## References

- [ADR-047](adr_047_database_absence_vs_misconfiguration.md) — absence as a supported
  posture; its verdict now holds on the literal path by construction
- [ADR-051](adr_051_delivered_empty_database_identity.md) — the identity door, which
  asked `Exists`; amended
- [ADR-064](adr_064_app_validates_every_config.md) — `Validate` on every construction
  path, which is where both presence-asking doors run
- [ADR-074](adr_074_delivered_empty_numeric_config.md) /
  [ADR-077](adr_077_delivered_empty_bool_config.md) — the decode-seam family that
  deliberately does not ask Presence
- [ADR-078](adr_078_delivered_empty_allowedips.md) — the `debug.allowedips` door,
  which read the raw tree; amended
- [ADR-090](adr_090_env_reachable_section_names.md) — the environment layer whose
  reachability rules the merge seam sits under
- [CONTEXT.md](../CONTEXT.md) — Presence, Delivered-but-empty, Absence
