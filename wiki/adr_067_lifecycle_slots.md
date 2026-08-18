# ADR-067: Slots Own the Per-Kind Lifecycle

**Status:** Proposed — flips to Accepted when the last slot slice (the streams fold, PR5) merges; the first slice (dead-surface removal, atom `[C60.4]`) is shipped
**Date:** 2026-08-17
**Builds on:** [ADR-066](adr_066_readiness_one_module.md) (readiness is one module — a
*probe description* is what a slot hands readiness), [ADR-045](adr_045_no_producer_side_manager_interfaces.md)
(no producer-side manager interface), [ADR-029](adr_029_graceful_shutdown_order.md)
(shutdown order) — all preserved.

## Context

Every resource kind's application lifecycle facts — construct, expose, pre-init, probe,
maintenance, close, `/ready` render, debug render — live in roughly ten `app/` files that
each hand-enumerate the kind set. ADR-066 collapsed the readiness half of that: one module
judges every kind from a probe description. The other phases did not move, so adding a
kind still means editing every place that enumerates kinds. Adding the streams kind (#973)
touched six files and still needed a runtime-registration exception, because the streams
manager does not exist while the Builder runs. Seven of the last twenty `app/` commits
landed in this cluster.

The same shape also grew two pass-through helpers. `MessagingInitializer` and
`ConnectionPreWarmer` each held a logger plus manager pointers `App` already holds, each
was constructed by one Builder step, and each was driven from one `prepareRuntime` line.
Neither was referenced anywhere outside `app/`. Alongside them sat two `Options` fields no
code read and eight exported response types describing the JSON of two access-controlled
debug endpoints.

## Decision

A **slot** owns one resource kind's whole application lifecycle.

1. **Slot shape.** An unexported `resourceSlot` interface implemented by four unexported
   per-kind structs in `app/` — database, messaging, cache, streams — so the compiler
   checks completeness. `App` keeps its typed manager fields (`ResourceProvider` needs the
   concrete types), and every lifecycle walk iterates the slot list rather than
   re-enumerating kinds.
2. **Phases.** `probe()` returns the kind's probe description · `preInit(ctx)` carries a
   `fatal` flag (database and messaging fatal, cache best-effort, streams none) ·
   `start(ctx)` runs in `prepareRuntime` (messaging: ensure consumers, the #907
   fail-vs-warn grading, and the bounded await-publisher-ready; streams: construct the
   manager and `Manager.Start`; database: the single-tenant pre-warm, with the mode check
   inside the slot; cache: nothing) · `stop(ctx)` runs before module `Shutdown` (messaging
   and streams stop consumers) · `close()` runs after (all four). There is no maintenance
   phase.
3. **One registration order:** `database → messaging → cache → streams`, used by every
   phase and by probe order. Close stays FIFO, so ADR-029's shutdown ordering is preserved
   by the stop/close split rather than by a second list.
4. **Maintenance is manager-side.** `database.DbManager` and `messaging.Manager` self-start
   their idle-cleanup loop at construction, exactly as `cache.NewCacheManager` already
   does, and stop it in `Close()`. The `IdleTTL > 0` guard lives one layer down, in
   `resourcepool.Pool.StartCleanup` itself (both constructors coerce a non-positive
   `IdleTTL` to a default before the pool is built, so the guard is a no-op in practice
   but stays the single place idle-cleanup eligibility is decided). `StartCleanup` stays
   exported and becomes idempotent, `StopCleanup` stays, and the
   cleanup-interval-vs-idle-TTL WARN moves beside the pool that owns both values.
   `cache.NewCacheManager` owns the same two values and deliberately stays silent: its
   shipped signature takes no logger, so giving it the WARN would be an apidiff break for
   an advisory — the asymmetry is known and accepted.
   `App.startMaintenanceLoops`, `App.warnIfCleanupIntervalTooLate` and
   `App.shutdownManagers` delete.
5. **Builder steps keep their names.** `CreateHealthProbes` and `RegisterClosers` iterate
   slots instead of hand-listing managers; renaming them belongs to the separate "Builder
   collapse" candidate, not here.
6. **The streams slot exists at build time with a nil manager** (its probe is *withheld*
   until the manager exists — registering a `disabled` streams description at build time
   would add `streams`/`streams_stats` to every service's `/ready` body under ADR-066 rule 5,
   an atom-worthy change nothing asked for) and constructs plus starts its manager in `start`. That removes the
   runtime-registration exception without moving stream construction earlier than the
   declarations that size it.

Slots name only what `app` calls. Consistent with ADR-045, no producer package grows a
manager interface for this — the interface lives in `app/`, where the caller is.

## Delivery

Five stacked PRs, each self-contained (PR3 ships as two, PR3a then PR3b):

- **PR2 (this ADR's own PR) deletes the pass-through helpers** so the slot work starts from
  a clean surface: `MessagingInitializer` and `ConnectionPreWarmer` fold into unexported
  `App` methods, two unread `Options` fields go, and eight debug response types are
  unexported. No behavior change on any supported construction path (see Consequences for
  the hand-composed `Builder` note and the retired log lines).
- **PR3a** introduces `resourceSlot` and the four structs, and converts probe, pre-init and
  close to slot iteration; **PR3b** adds the `start` and `stop` phases and converts
  `prepareRuntime` and `Shutdown` to slot iteration.
- **PR4** moves maintenance manager-side (decision 4).
- **PR5** gives streams its `start` phase, folding `app/streams_setup.go` into the streams
  slot.

## Consequences

- **Removed in PR2** (nothing outside `app/` referenced any of them):
  `MessagingInitializer`, `NewMessagingInitializer`, `CollectDeclarations`,
  `SetupLazyConsumerInit`, `PrepareRuntimeConsumers`, `LogDeploymentMode`,
  `MessagingInitializer.IsAvailable`, `ConnectionPreWarmer`, `NewConnectionPreWarmer`,
  `PreWarmSingleTenant`, `PreWarmDatabase`, `PreWarmMessaging`, `LogAvailability`,
  `ConnectionPreWarmer.IsAvailable`, `Options.Database`, `Options.MessagingClient`.
  Unexported with their JSON unchanged: `HealthDebugInfo`, `ComponentHealth`,
  `HealthSummary`, `DebugResponse`, `GCInfo`, `GoroutineInfo`, `GoroutineStack`,
  `PotentialLeak`.
- **What consumers do: nothing**, unless a service named one of those types in its own Go
  code — building an `app.ConnectionPreWarmer` by hand, embedding `app.GCInfo`, or setting
  `Options.Database`. Those break at compile time and have no replacement, because the
  framework drives every one of these paths itself. `/ready`, `/_sys/health-debug`, the
  goroutine and GC endpoints all emit byte-identical JSON.
- **`SetupLazyConsumerInit` was already redundant.** `App.buildMessagingDeclarations` pushes
  the declaration set into the resource provider through the `declarationSetter` seam,
  which is a superset: it covers both concrete providers and any third implementation,
  where `SetupLazyConsumerInit` type-switched on two and merely warned about the rest.
- **Locality.** After PR5, adding a resource kind is one slot file, not an edit in ten
  places, and the compiler — not review — enforces that every phase was considered.
- **Cost.** The slot interface is indirection that a two-kind framework would not earn.
  Four kinds, five phases and ten enumeration sites is what earns it; the alternative
  measured against it is "keep hand-enumerating", which is what produced the drift ADR-066
  documents.
- **Hand-composed `Builder` chains.** A caller that assembles its own `*Builder` chain and
  skips `ConfigureRuntimeHelpers` — already outside supported use, since `NewWithConfig` is
  the one chain the framework ships — used to get no pre-warm at all: `ConnectionPreWarmer`
  was built inside that step, so skipping the step left the gate nil. After PR2 the gate
  reads `a.dbManager`/`a.messagingManager` directly, both already set by the earlier
  `CreateApp` step, so that same skip now pre-warms where it previously didn't. The skip
  still skips pre-initialization, which lives in the same step. Consumer bootstrap widened
  the same way: `MessagingInitializer` was built in that step too, so the skip used to
  no-op the AMQP consumer step, and it now runs `EnsureConsumers` — and unlike pre-warm,
  which never fails startup, the #907 grading aborts startup when declared consumers cannot
  start. Both are behavior notes for an unsupported construction path, not regressions;
  the shipped `NewWithConfig` chain always runs `ConfigureRuntimeHelpers`.
- **Watch:** `StartCleanup` becoming idempotent (PR4) means a caller that starts it twice
  no longer leaks a goroutine, but a caller that relied on a *second* call changing the
  interval must call `StopCleanup` first. PR4 ships that, plus the construction-time start
  and the five retired cleanup-loop log lines, as [C60.5](migrations.md) — not this ADR's atom.
