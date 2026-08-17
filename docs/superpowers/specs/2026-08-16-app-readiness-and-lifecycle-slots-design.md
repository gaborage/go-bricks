# App readiness module and per-kind lifecycle slots — design

**Date:** 2026-08-16
**Status:** Accepted (grilling session, architecture review cards 1 and 3)
**Vocabulary:** [CONTEXT.md](../../../CONTEXT.md) — *Slot*, *Probe description*,
*Readiness*; design terms per `/codebase-design` (module, interface, seam,
adapter, depth, leverage, locality).

## Problem

Every resource kind's application lifecycle facts (construct · expose ·
pre-init · probe · maintenance · close · `/ready` render · debug render) live in
ten `app/` files that each hand-enumerate the kind set. Readiness alone has:

- three copies of one lease → liveness → status machine
  (`databaseManagerHealthProbe`, `messagingManagerHealthProbe`,
  `cacheManagerHealthProbe`, `app/health.go:74-273`);
- two different readiness *models*: `/ready` gates on
  `Err != nil && Critical` (`lifecycle.go:616`) while the debug summary gates on
  a status list (`debug_health.go:129`), so messaging "not ready" and streams
  `not_ready` are 200 on `/ready` and `overall_status: unknown` on debug;
- criticality/absence decided at four sites (`rootCacheAbsent`,
  `App.cacheAbsent`, `IsCacheCritical()`, `warnIfCacheCriticalityOptOut`);
- a hand-written 200 body (`db_stats` beside `database`; disabled kinds render
  `{}` for two kinds and `{"status":"disabled"}` for one; streams only when
  started);
- a debug view whose extra `*_manager` entries list database and messaging only
  (`debug_health.go:156-189`) — cache (#870) and streams (#973) never added;
- eight test factories that build real managers to drive a five-branch
  function (`health_test.go:605-770`), and a fixture that re-implements two
  Builder steps (`app_test.go:506-517`).

Adding the streams kind (#973) touched six files and still needed a
runtime-registration exception; seven of the last twenty `app/` commits landed
in this cluster (#870 #887 #888 #889 #881 #947 #956 #973).

## Decisions

### Readiness (card 1) — Stack A PR1a + PR1b

1. **One module, same package.** `app/readiness.go` (+ `_test.go`), package
   `app`, unexported types. `Prober` and `HealthStatus` stay the exported seam,
   untouched. Module discipline comes from tests targeting only the module's
   entry points, not from a package line.
2. **Probe description** — the module's input, one per kind:
   `name` (fixed component identifier) · `critical` (final bool, decided once)
   · `absent` (the fixed `""` key can never resolve — cache only; reports
   `not_configured` without leasing) · `perTenant` (a lease that fails as
   not-configured reads `per_tenant`; derived once from
   `cfg.Multitenant.Enabled` for **every** leased kind, not database only) ·
   `acquire(ctx) → (live, release, err)` (nil for lease-less kinds) ·
   `live(ctx) → err` (used directly when `acquire` is nil; the cache's 500 ms
   ping budget lives inside the cache description) · `stats() → map` (read
   while the lease is held) · `publicStats` allowlist for the unauthenticated
   `/ready` body. `PublicErr` stays a `Prober`-level extension; no built-in
   probe sets it.
3. **One status vocabulary:** `healthy · unhealthy · not_configured · disabled ·
   per_tenant`. A component is *failing* iff its status is `unhealthy`, and
   `unhealthy` always carries an `Err` — liveness failures produce one
   (`publisher not ready`, `stream consumers not open`). `not_ready` and the
   per-component `ready` retire; `degraded` stays aggregate-only. The
   `details.status` sub-status strings (`no_active_connections`,
   `connection_failed`, `not_ready`) collapse into the same vocabulary.
4. **One gate:** `/ready` answers 503 for the first *failing && critical*
   component; the debug summary counts *failing* as errors and
   *failing && critical* as critical from the same predicate. `not_configured`,
   `disabled`, `per_tenant`, `healthy` are ready-equivalent in both views.
5. **One body rule:** every registered kind renders `<name>` and
   `<name>_stats` (public projection = allowlist; ADR-048 sanitized error text
   inside the module); `db_stats` therefore becomes `database_stats`; a
   `disabled` kind renders `{"status":"disabled"}` stats uniformly. The three
   classic kinds always register (nil manager → `disabled` description);
   streams keeps registering at runtime until PR5 folds it into a slot.
   Naming follows the framework's own vocabulary (`database`, `messaging`,
   `cache`, `streams`), not OTel semconv, which names neither cache nor streams.
6. **One debug view:** one entry per kind keyed `<name>` carrying status ·
   error text · full unredacted stats; the `database_manager` /
   `messaging_manager` extra entries fold in.
7. **Criticality once:** computed at description construction from the config
   verdict + `IsCacheCritical()`; `App.cacheAbsent` deletes; `rootCacheAbsent`
   stays as the config-side verdict input; the cache-criticality opt-out WARN
   stays in the Builder step (it needs `Options`, which `App` does not hold).
8. **Order:** `database → messaging → cache → streams` for probe order (and,
   from PR3, every phase); close stays FIFO.
9. **Tests: replace, don't layer.** One table at the module interface
   (description × stub-acquire outcome × stub-live outcome → status · critical ·
   `/ready` code+body · debug body); one or two `/ready` HTTP pins through the
   real router; the eight real-manager factories and `rebuildClosersAndHealth`
   go.
10. **Docs:** ADR-066 (readiness is one module: one vocabulary, one gate, one
    body rule) + atom C60.3 (visible changes: streams `not_ready`→`unhealthy`;
    `db_stats`→`database_stats`; messaging/cache `per_tenant` in multi-tenant;
    disabled stats shape; sub-status strings; debug `overall_status` no longer
    `unknown` for a non-ready non-critical kind). No Go API change, so no `!`.

### Lifecycle slots (card 3) — Stack A PR2–PR5, ADR-067

11. **Slot shape:** unexported `resourceSlot` interface implemented by four
    unexported per-kind structs in `app/` (compiler-checked completeness).
    `App` keeps its typed manager fields for typed access (`ResourceProvider`
    needs concrete types); every lifecycle walk iterates the slot list.
12. **Phases:** `probe()` → probe description · `preInit(ctx)` with a `fatal`
    flag (database, messaging fatal; cache best-effort; streams none) ·
    `start(ctx)` in `prepareRuntime` (messaging: ensure consumers + the #907
    fail-vs-warn grading + await publisher ready; streams: `Manager.Start`,
    constructing the manager there; database: single-tenant pre-warm, mode
    check inside the slot; cache: none) · `stop(ctx)` before module `Shutdown`
    (messaging + streams stop consumers) · `close()` after (all four). No
    maintenance phase.
13. **Maintenance is manager-side:** `DbManager` and `messaging.Manager`
    self-start idle cleanup at construction when `IdleTTL > 0`, as
    `cache.NewCacheManager` does, and stop it in `Close()`; `StartCleanup`
    stays exported and becomes idempotent, `StopCleanup` stays; the
    cleanup-interval-vs-idle-TTL WARN moves beside the pool that owns both
    values; `startMaintenanceLoops`, `warnIfCleanupIntervalTooLate`,
    `shutdownManagers` delete. Atom line: cleanup now starts at construction.
14. **Builder steps keep their names** (`CreateHealthProbes`,
    `RegisterClosers`) and iterate slots; renaming belongs to the "Builder
    collapse" candidate.
15. **Streams slot** exists at build time with a nil manager (probe `disabled`)
    and constructs + starts in `start`; ADR-029's shutdown order is preserved by
    the stop/close split.
16. **PR2 kill list (zero references outside `app/`, zero doc mentions,
    not consumer surface):** delete `MessagingInitializer`,
    `NewMessagingInitializer`, `CollectDeclarations`, `SetupLazyConsumerInit`,
    `IsAvailable` (both), `LogDeploymentMode`, `PrepareRuntimeConsumers`
    (grading moves into the messaging slot's `start`), `ConnectionPreWarmer`,
    `NewConnectionPreWarmer`, `PreWarmSingleTenant`, `PreWarmDatabase`,
    `PreWarmMessaging`, `LogAvailability`, `Options.Database`,
    `Options.MessagingClient`; unexport (JSON unchanged) `HealthDebugInfo`,
    `ComponentHealth`, `HealthSummary`, `DebugResponse`, `GCInfo`,
    `GoroutineInfo`, `GoroutineStack`, `PotentialLeak`. Update
    `wiki/startup_defaults.md:82`, `wiki/migrations.md:1797`. **Leave:**
    Builder steps; card-2 types (`ManagerConfigBuilder`,
    `ResourceManagerFactory`, `FactoryResolver`,
    `MessagingClientFactoryOptions`, `LogFactoryInfo`); the two
    `ResourceProvider`s + `SetDeclarations`; `module_metadata.go` (external
    `go-bricks-openapi` consumer — verify before ever touching);
    `SignalHandler`/`TimeoutProvider` + impls (`Options` fields; keep `Run`
    testable); `IPWhitelist` (ADR-049 surface). ADR-067 + one atom listing every
    removed symbol land with PR2.
17. **Slices:** PR2 kill dead surface → PR3 slot interface + iterate for
    pre-init / probe / close → PR4 maintenance manager-side → PR5 streams
    `start` phase folds `streams_setup.go` into the streams slot. Each < ~400
    LoC.

## Out of scope (recorded for later)

Card-2 config→managers construction; ResourceProvider fold; Builder collapse;
deployment-mode verdict; `module_metadata.go`.

## Constraints

- ADR-045: no producer-side manager interface — slots and descriptions live in
  `app/` and name only what app calls.
- ADR-046/048: strict cache readiness default and sanitized `/ready` errors
  preserved.
- ADR-029: shutdown order preserved.
- ADR-064: every construction path validates config — untouched.
- CONTEXT.md terms are the names in code comments and docs.
