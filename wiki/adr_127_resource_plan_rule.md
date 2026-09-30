# ADR-127: Resource Availability Follows What the Control-Plane Key Holds

**Status:** Accepted
**Date:** 2026-09-29
**Amends:** [ADR-047](adr_047_database_absence_vs_misconfiguration.md) (the absence exemption
set), [ADR-066](adr_066_readiness_one_module.md) rule 1 (who reports `per_tenant`),
[ADR-041](adr_041_shared_ledger_tenancy.md) (its startup trade-offs, beside a static caller store
and under `messaging.tenancy: shared`), [ADR-126](adr_126_resource_plan.md) (the drift ledger, and
"the plan never asks a store")

> **Amended (2026-09-29, [ADR-128](adr_128_outbox_broker_check_reads_the_resource_plan.md)):** the
> outbox's #366 broker check for a per-tenant ledger reads `ModuleDeps.MessagingConfigured`, the
> plan's answer, instead of root config; the inbox has no broker check.

## Context

ADR-126 put every startup decision about the database, messaging and cache kinds behind one
Resource plan: per kind, its Tenancy and what the control-plane key `""` holds for it (known
present, known absent, knowable only at runtime), with the answers derived from those two facts.
It kept today's behavior by pinning eight answers where today's readers differ from that rule
(a drift ledger, D1–D8), and it judged `""` beside a caller-supplied static
`Options.ResourceSource` from the root config blocks, a store that the application never asks
for anything. The pins are where the old exemption sets disagree with each other, and each one
does something wrong in some deployment:

- a caller-supplied static store that serves `""` beside an empty root block gets the
  database-absence WARN, the `DatabaseRequirer` abort and the #366 declarations abort, while its
  resources are one lookup away; one that does not serve `""` beside a set root block fails
  startup on the database pre-init;
- `ModuleDeps.*Configured` reads true for every kind beside any caller-supplied store and under
  multi-tenancy, including shared messaging with no root broker, where messaging resolves only on
  an absent `""` and every call fails;
- shared messaging is never pre-initialized, so a broker that cannot be reached at build boots
  green, and with no root broker it boots with its declarations dropped and a `per_tenant`
  readiness label for a kind that is not per tenant;
- a dynamic store with no root broker has its messaging declarations refused on the root block's
  word, though the store may serve `""`;
- the database and messaging pre-warm lease a `""` already known absent; the cache never
  pre-warms.

## Decision

1. **The rule.** For each kind, from its Tenancy and the presence of `""`:

   | Answer | Rule | Read by |
   | --- | --- | --- |
   | unavailable | resolves on `""` and `""` known absent | absence WARN, `DatabaseRequirer` abort, #366 declarations gate, `ModuleDeps.*Configured` (its negation) |
   | pre-init | resolves on `""` and `""` known present | the fatal build-time lease of `""` under `app.startup.<kind>` |
   | pre-warm | resolves on `""` and `""` not known absent | the advisory lease in `prepareRuntime` |
   | readiness | the database and messaging always lease `""` (ADR-047 §4); the cache skips the lease only when `""` is known absent; a not-configured `""` reads `per_tenant` only under per-tenant Tenancy | the probe |

   A kind resolves on `""` in single-tenant mode, and messaging under multi-tenancy with
   `messaging.tenancy: shared`; the database and cache under multi-tenancy, and messaging under
   `messaging.tenancy: per-tenant`, resolve per tenant. The deployment answers — multi-tenancy, the
   tenant stamp, the streams refusal and the seal tenancy — are unchanged from ADR-126.
2. **Presence is what the store serving `""` answers.** The store is `Options.ResourceSource`, or
   the built-in `config.TenantStore` over the root blocks. A dynamic store (`IsDynamic()` true) is
   never asked: `""` is knowable only at runtime. The cache behind an `Options.CacheConnector` is
   present. Otherwise the plan asks the store for `""` once per kind, in every mode —
   `DBConfig`, `BrokerURL`, `CacheConfig` — each a config lookup that dials nothing, under the
   kind's `app.startup.database` / `app.startup.messaging` / `app.startup.cache` budget (a
   non-positive budget is unbounded, as for pre-init). A configuration is present;
   `config.IsNotConfigured` is absent.
3. **A lookup that fails otherwise fails startup.** Any other error, a spent budget included,
   aborts the build before any manager exists, with
   `dependency resolution failed: resource plan: <kind> lookup of the control-plane key "": <cause>`.
   Under multi-tenancy with per-tenant Tenancy, presence feeds only the cache probe's lease. For
   the database that is no new failure: its probe leases `""` on every poll and is always critical
   (ADR-047 §4), so a store that errors on `""` already held `/ready` at 503; failing at build says
   so once, with the kind named. For messaging and the cache it is a new startup failure: their
   probes are non-critical unless `messaging.consumers.critical` / `cache.critical`, so an erroring
   `""` read `unhealthy` while `/ready` stayed 200 and the service served. The cost is accepted so
   that `""` has one contract in every mode — a configuration or not-configured — and a store that
   answers it with a tenant-not-found error (the built-in store's shape for an unknown tenant) is
   told so at build.
4. **The ledger is deleted.** `todaysLedger`, the answer pins and `configured()` go;
   `markConfigured` reads `!unavailable()`. No reader changes: each already asked its answer.
   `resourcePlan` stays unexported, and `NewModuleRegistry`, `SetMessagingTenancy` and
   `ManagerConfigBuilder` keep their signatures.

## Consequences

Every flip, by deployment mode (ST single-tenant, MT multi-tenant; "built-in" is no
`Options.ResourceSource`, "caller" a static one, "dynamic" one whose `IsDynamic()` is true):

- **A caller store is consulted at build.** Each static caller store is called for `""` up to
  three times during construction, where it was never called before; the cache lookup is skipped
  behind a `CacheConnector`. The built-in store's answers are the same config reads the root-block
  tests made, so nothing moves for it.
- **A caller store's `""` lookup error is fatal**, in every mode: an error that does not
  satisfy `config.IsNotConfigured` — a `MultiTenantError` for the empty key, a transport failure,
  a lookup outlasting `app.startup.<kind>` — fails construction. That includes MT per-tenant
  deployments, where such an answer from `BrokerURL` or `CacheConfig` only read `unhealthy` on a
  non-critical probe and the service served (decision 3).
- **ST, caller store serving `""`, empty root blocks:** no database-absence WARN; a
  `DatabaseRequirer` module registers; messaging declarations no longer abort; database and
  messaging pre-init lease `""` at build and fail startup when it cannot be reached.
- **ST, caller store not serving `""`:** `DBConfigured`, `MessagingConfigured` and
  `CacheConfigured` read false where they read true; the cache probe stops leasing `""` (it still
  reads `not_configured`) and its pre-init is skipped. With empty root blocks the WARN and both
  aborts fire as before. With the root `database:` block set, the fatal database pre-init no
  longer runs, so startup no longer fails with `database connection failed during startup`: the
  service boots with the absence WARN, and a `DatabaseRequirer` module aborts. With
  `messaging.broker.url` set, the fatal messaging pre-init no longer runs either, so startup no
  longer fails with `messaging connection failed during startup`: messaging declarations abort
  (#366), and a service with none boots with `MessagingConfigured` false and no WARN of its own —
  messaging has no counterpart of the database-absence WARN, only the WARN `Failed to start
  consumers on the control-plane key` that every service without a control-plane broker logs.
  Both lose a fail-fast signal: a store that stops serving `""` beside set root blocks now boots.
  The #366 error and the `DatabaseRequirer` error still name the root keys
  (`messaging.broker.url`, `DATABASE_TYPE`) when the store's answer fired them.
- **ST, the cache present** (root `cache.enabled`, a `CacheConnector`, a caller store serving it,
  or a dynamic store): the cache pre-warms, a second advisory lease of `""` in `prepareRuntime`
  logged `Pre-warmed control-plane cache connection`, or a pre-warm WARN when it fails. With
  Redis unreachable at boot, that lease redials after the failed pre-init, on `prepareRuntime`'s
  unbounded context: up to one more Redis connect timeout (about 5s) before the listeners bind.
- **ST, dynamic store, no root broker:** messaging declarations no longer abort startup (the
  #366 gate read the root broker, not the store). A declared consumer still replays on `""`
  through the store and fails startup if the store does not serve it.
- **ST, `""` known absent** (built-in with no root database or broker, or a caller store not
  serving it): the database and messaging pre-warm no longer lease it. Only a Debug line changes,
  and the pools' failed-create counts stop growing by one at startup.
- **MT + shared messaging, root broker (built-in) or a caller store serving `""`:** messaging
  pre-init leases `""` at build under `app.startup.messaging`, so an unreachable control-plane
  broker fails startup where it booted and reconnected.
- **MT + shared messaging, no root broker (built-in) or a caller store not serving `""`:**
  `MessagingConfigured` reads false; any messaging declarations abort startup with the #366 error
  (`messaging declarations were registered … but messaging is not configured`) — a set with no
  consumer booted with the WARN `Failed to start consumers on the control-plane key`, and a
  consumer set aborted later with `failed to start consumers on the control-plane key`; the
  messaging readiness status reads `not_configured` where it read `per_tenant`, visible only on
  `/_sys/health-debug` (the `app.readiness.status` gauge has no series for either status, and
  `/ready` passes both); the doomed pre-warm is dropped.
- **MT + shared messaging, dynamic store:** a `""` the store answers not-configured at runtime
  reads `not_configured` on readiness, not `per_tenant`.
- **MT, caller store not serving the cache's `""`:** the cache probe stops leasing `""` on every
  poll; it still reads `per_tenant`.
- **Unchanged:** every MT per-tenant answer for the database and messaging, every flag under MT
  except shared messaging's, the INFO and WARN texts, the Builder's step names.
- **Unchanged:** the outbox #366 broker check still read only root config, so with a static
  caller store serving the broker and an empty root messaging block, outbox Init still aborted
  where the app now boots (#1853). The inbox has no broker check: it discards the messaging
  resolver. *(Amended by ADR-128: a per-tenant-ledger outbox now reads
  `ModuleDeps.MessagingConfigured`, the plan's answer; the shared ledger still reads root config.)*

The amended ADRs:

- **ADR-047.** The exemption set — multi-tenancy, a dynamic config source, a dynamic resource
  source — is replaced by the rule: the WARN and the `DatabaseRequirer` abort fire when the
  database resolves on `""` and the store serving `""` answered not-configured at build. §4 (the
  probe leases `""`, `per_tenant` is a relabel after resolution) and §5 stand.
- **ADR-066 rule 1.** `per_tenant` applies to a kind under per-tenant Tenancy, not to every kind
  in a multi-tenant deployment: shared messaging resolves on `""`, so a not-configured `""` reads
  `not_configured`.
- **ADR-041.** Its startup trade-offs narrow. A static caller store is asked for `""` at build in
  every mode — a config lookup, not a connection probe. Under `messaging.tenancy: shared` the
  control-plane broker is pre-initialized at build (it was already pre-warmed), and the pre-warm
  and the shared consumer replay reach `""` through a dynamic store. Under per-tenant messaging
  tenancy the app neither pre-initializes nor pre-warms the control-plane broker, so "no startup
  connection probe" and "the first relay cycle may be cold" still hold there.
- **ADR-126.** The drift ledger and the root-block judgment of a caller store are gone; the plan
  asks the store.

## References

- [ADR-126](adr_126_resource_plan.md): the Resource plan and its drift ledger
- [ADR-047](adr_047_database_absence_vs_misconfiguration.md): the absence WARN, `DatabaseRequirer`,
  the readiness lease of `""`
- [ADR-066](adr_066_readiness_one_module.md): the readiness status vocabulary
- [ADR-041](adr_041_shared_ledger_tenancy.md), [ADR-087](adr_087_messaging_tenancy_and_tenant_stamp.md):
  shared tenancy
- [ADR-125](adr_125_source_type_agrees_with_resource_source.md): `IsDynamic()` agrees with `source.type`
- [migrations.md](migrations.md) `[C70.2]`–`[C70.6]`
- `app/resource_plan.go` (`planResources`, `presenceOf`, `lookupControlPlaneKey`),
  `app/bootstrap.go` (`dependencies`, `markConfigured`)
