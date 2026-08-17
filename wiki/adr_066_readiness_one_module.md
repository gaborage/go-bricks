# ADR-066: Readiness Is One Module — One Status Vocabulary, One Gate, One Body Rule

**Status:** Accepted
**Date:** 2026-08-16
**Builds on:** [ADR-046](adr_046_cache_readiness_strict_default.md) (strict cache
readiness), [ADR-048](adr_048_ready_sanitize_by_default.md) (sanitized `/ready` errors)
— both preserved; this ADR changes where the readiness decision lives, not what it decides.

## Context

Readiness was written once per kind. `app/health.go` carried three copies of the same
lease → liveness → status machine (`databaseManagerHealthProbe`,
`messagingManagerHealthProbe`, `cacheManagerHealthProbe`) plus a lease-less streams
variant, each choosing its own sub-status strings (`no_active_connections`,
`connection_failed`, `not_ready`) and its own criticality. Two different readiness
*models* then read those results: `/ready` gated on `Err != nil && Critical`
(`lifecycle.go`), while the debug summary gated on a status list
(`healthy, ready, not_configured, disabled, per_tenant` in `debug_health.go`). A
messaging client that leased but reported not ready produced `unhealthy` with a nil
`Err`; a streams manager whose consumers were not open produced `not_ready`. Both fell
through both models — 200 on `/ready`, `overall_status: unknown` on
`/_sys/health-debug` — and the debug summary's comment recorded exactly that drift as
the reason the status list existed. Criticality and absence were decided at four sites
(`rootCacheAbsent`, `App.cacheAbsent`, `IsCacheCritical()`,
`warnIfCacheCriticalityOptOut`); the debug view's manager entries listed database and
messaging only, two kinds behind. Seven of the last twenty `app/` commits landed in this
cluster, and eight test factories existed only because driving a five-branch function
required a real manager.

## Decision

Readiness is one module (`app/readiness.go`). Every kind hands it a **probe
description** — a fixed component name, whether the kind is critical (decided once, at
construction), whether its fixed `""` key is absent, whether a not-configured lease reads
`per_tenant`, how to lease it, how to check it is live, and its statistics — and one
machine judges every description. `Prober` and `HealthStatus` stay the exported seam,
unchanged; the four constructors are gone.

Three rules follow from "one machine":

1. **One status vocabulary.** `healthy · unhealthy · not_configured · disabled ·
   per_tenant`. `unhealthy` always carries an `Err` — a liveness check that reports "not
   live" returns one (`publisher not ready`, `stream consumers not open`) — so *failing* is
   one predicate: `status == unhealthy`. `not_ready` and the per-component `ready` are
   retired; `degraded` is aggregate-only; the `details.status` key mirrors the verdict.
   `per_tenant` applies to every leased kind, not only the database: a multi-tenant
   deployment whose `""` key resolves nothing *has* the resource, under tenant keys.
2. **One gate.** `/ready` answers 503 for the first *failing && critical* component; the
   debug summary counts *failing* as errors and *failing && critical* as critical from the
   same predicate. `not_configured`, `disabled`, `per_tenant`, `healthy` are
   ready-equivalent in both views.
3. **One body rule.** Every registered kind renders `<name>` and `<name>_stats` on the 200
   body — the stats a public allowlist of the kind's counters, ADR-048's sanitized error
   text on the 503 body — so `db_stats` becomes `database_stats`, and a `disabled` kind
   renders `{"status":"disabled"}` uniformly. Names follow the framework's own vocabulary
   (`database`, `messaging`, `cache`, `streams`), not OTel semconv, which names neither
   cache nor streams. The debug detail view is one entry per kind carrying status, error
   text and the full unredacted statistics.

The three classic kinds always register (a nil manager is a `disabled` description);
the streams description keeps registering at runtime until the lifecycle-slot work
(ADR-067) gives streams a build-time slot. Probe order is `database → messaging → cache →
streams`. The cache-criticality opt-out WARN stays in `Builder.CreateHealthProbes`, which
holds the `Options` the check needs.

## Delivery

Two stacked PRs: the first lands the module, the descriptions and rule 1 (this ADR's
vocabulary) with `[C60.3]`; the second lands rules 2 and 3 — the shared gate, the derived
body and the one-entry-per-kind debug view — and extends `[C60.3]` with the `db_stats`
rename and the debug-summary changes.

## Consequences

- **Visible changes, first slice** (`[C60.3]`): streams reports `unhealthy` instead of
  `not_ready`; the `details.status` sub-strings collapse into the vocabulary; messaging
  and cache report `per_tenant` in multi-tenant deployments where they reported
  `not_configured`; a disabled kind's stats render `{"status":"disabled"}` instead of
  `{}`; the debug view lists every classic kind, and — because `unhealthy` now carries an
  error — a non-critical kind that is not live reads `degraded`, not `unknown`, on its
  summary. `/ready`'s body keys and its gate are unchanged in this slice.
- **Visible changes, second slice** (extends `[C60.3]`): the shared gate
  (*failing && critical*) drives both views, the 200 body is derived per kind — the
  `db_stats` key becomes `database_stats` — and the debug view keys one entry per kind
  (the `database_manager`/`messaging_manager` extras fold in). No Go API changes in
  either slice.
- **Locality.** The next readiness rule lands in one file. Adding a kind is one
  description, and it is judged, rendered and summarized without touching the machine.
- **Test surface.** The machine is tested through descriptions with stub lease and
  liveness functions; the real-manager factories that existed to reach branch arms are
  gone, and the few that remain pin genuine seams (the real config resolver, ADR-041's
  shared-ledger control-plane database, the cache's bounded warm-path ping).
- **Watch:** anything that pins `/ready`'s exact key set, or reads `db_stats`,
  `not_ready`, `connection_failed` or `no_active_connections` from it, must move with
  `[C60.3]`. See [migrations.md](migrations.md).
