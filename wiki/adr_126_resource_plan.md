# ADR-126: A Resource Plan Decides Each Kind's Tenancy and Presence Once

**Status:** Accepted
**Date:** 2026-09-29

## Context

Startup asks two questions of each resource kind (database, messaging, cache): does it resolve on
the control-plane key `""` or per tenant, and what does `""` hold for it? Every reader re-derived
both answers from raw inputs — `multitenant.enabled`, `messaging.tenancy`, `source.type`,
`Options.ResourceSource` and its `IsDynamic()`, `Options.CacheConnector`, and the root database,
broker and cache blocks — each with its own exemption set, and the sets drifted apart:

- `rootDatabaseAbsent` (`app/bootstrap.go`), which arms the absence WARN and the
  `DatabaseRequirer` abort, exempts multi-tenancy and a dynamic store but judges a caller-supplied
  static store by the root block. `rootCacheAbsent` exempts any caller-supplied store and a
  `CacheConnector`, but not multi-tenancy.
- `markConfigured` read every `ModuleDeps.*Configured` flag true under multi-tenancy or any
  caller-supplied store, including multi-tenant shared messaging with no root broker, where
  messaging resolves only on `""` and `""` is absent.
- `ConfigureRuntimeHelpers` (`app/app_builder.go`) skipped every kind's pre-init under
  multi-tenancy, messaging under `messaging.tenancy: shared` included.
- `databaseSlot.start` and `messagingSlot.start` (`app/slot.go`) pre-warm on the tenancy alone,
  leasing a known-absent `""` for a Debug skip; `cacheSlot.start` never pre-warms.
- `assertMessagingConfiguredIfDeclared` (`app/lifecycle.go`, issue #366) skips on
  `multitenant.enabled`, shared tenancy included, and otherwise tests the root broker URL, never
  the store, so it refuses a dynamic store that serves `""`.
- `messagingSlot.describe` relabels a not-configured `""` as `per_tenant` on
  `multitenant.enabled`, shared tenancy included.
- `configureSealing`, the streams and AMQP tenant-stamp switches and the provider's
  `SetMessagingTenancy` each combined `multitenant.enabled` and `messaging.tenancy` themselves.

## Decision

1. **One plan, unexported, in `app/resource_plan.go`.** `resourcePlan` holds one row per kind with
   two facts:
   - **Tenancy.** `single-tenant` without `multitenant.enabled`, which is also the deployment
     fact (`messaging.tenancy: shared` there is the ADR-041 no-op); `shared` for messaging under
     multi-tenancy and `messaging.tenancy: shared`; `per-tenant` for the database and cache under
     multi-tenancy, and for messaging under `messaging.tenancy: per-tenant`.
   - **Presence of `""`:** known present, known absent, or knowable only at runtime. A dynamic
     store gives runtime; the cache with an `Options.CacheConnector` is present; otherwise
     presence is the built-in `config.TenantStore`'s answer for `""`, from the root blocks. The
     plan never asks a store for `""`.
2. **Answers are derived on every call, never stored.**

   | Answer | Rule | Read by |
   | --- | --- | --- |
   | unavailable | resolves on `""` and `""` known absent | absence WARN, `DatabaseRequirer`, #366 gate |
   | configured | `!unavailable` | `ModuleDeps.*Configured` |
   | pre-init | resolves on `""` and `""` known present | the fatal build-time lease |
   | pre-warm | resolves on `""` and `""` not known absent | the advisory `prepareRuntime` lease |
   | probe | the cache skips the lease only when `""` is known absent; the database and messaging always lease (ADR-047); `per_tenant` follows Tenancy | readiness |
   | multitenant, tenant stamps, streams refusal, seal tenancy | multi-tenant; shared messaging; per-tenant messaging; ADR-097's three arms | provider choice, stamps, streams, sealing |

3. **Computed once.** `appBootstrap.dependencies` plans right after
   `FactoryResolver.ResourceSource`, before the manager configuration and any manager, and the plan
   rides on the dependency bundle and the `App`. `markConfigured` is its first reader; the other
   readers move onto it in later changes.
4. **No behaviour change yet.** A temporary drift ledger pins today's answer wherever today's
   reader differs from the rule, so every gate, abort, lease, flag, readiness status and label,
   gauge value and INFO/WARN line is unchanged. ADR-127 deletes it and switches to the rule.

   | Row | Pins | Where | Reproduces |
   | --- | --- | --- | --- |
   | D1 | cache presence to present | a caller-supplied static store | `rootCacheAbsent` exempts any caller source |
   | D2 | configured to true | multi-tenancy, or any caller-supplied store | the per-key set of the `ModuleDeps` flags |
   | D3 | pre-init to false | multi-tenancy | the pre-init skip |
   | D4 | pre-warm to true | database and messaging on `""`, `""` known absent | the slots pre-warm on tenancy alone |
   | D5 | pre-warm to false | the cache | `cacheSlot.start` is a no-op |
   | D6 | messaging unavailable to false | multi-tenancy | the #366 gate skips multi-tenancy |
   | D7 | messaging unavailable to true | single-tenant, dynamic store, no root broker | the #366 gate reads the root broker |
   | D8 | messaging `per_tenant` to true | shared messaging | the label follows `multitenant.enabled` |

5. **The exported surface is untouched.** `NewModuleRegistry`, `SetMessagingTenancy` and
   `ManagerConfigBuilder` keep their signatures when their inputs move onto the plan; the Builder's
   step names stay (ADR-067).

## Consequences

- **The next breaking change is mechanical.** The table test states the rule's answer beside
  today's in every mode the build accepts. With the built-in store or a dynamic store, the cells
  that differ are what ADR-127 flips:
  - single-tenant with `""` absent: no doomed database or messaging pre-warm (D4);
  - single-tenant with the cache present, at runtime or behind a `CacheConnector`: the cache
    pre-warms (D5);
  - single-tenant behind a dynamic store with no root broker: messaging declarations boot instead
    of aborting (D7);
  - multi-tenant shared messaging with a root broker: messaging pre-init leases `""` at build (D3)
    and readiness stops relabelling (D8);
  - multi-tenant shared messaging without one: `MessagingConfigured` reads false (D2),
    declarations refuse startup (D6), no pre-warm (D4), and readiness reads `not_configured` (D8);
  - multi-tenant shared messaging behind a dynamic store: readiness stops relabelling (D8).

  Beside a caller-supplied static store the rule cells are the root-block reading this plan can
  compute without a lookup. ADR-127 asks the store for `""` instead, so there each flip depends on
  what the store answers:
  - single-tenant, a store that serves `""` beside an empty root block: the database absence WARN,
    the `DatabaseRequirer` abort and the #366 declarations abort give way to a boot, and database
    and messaging pre-init lease `""` fatally at build;
  - single-tenant, a store that does not serve `""`: that kind's flag reads false (D2); where the
    root block is set, today's fatal pre-init of `""` gives way to the absence WARN and the
    `DatabaseRequirer` and #366 gates;
  - multi-tenant shared messaging behind a store that serves `""`: messaging pre-init leases `""`
    fatally at build (D3) and readiness stops relabelling (D8); behind one that does not, the
    shared-messaging flips above;
  - the cache follows the store's cache answer: absent stops the probe's lease, and in
    single-tenant skips pre-init and reads the flag false (D1, D2); present pre-warms in
    single-tenant (D5).
- **`configured` and `unavailable` disagree until ADR-127** wherever D2 fires; a reader uses the
  answer its row names.
- **The gate bites the plan.** Every decision is an `==`/`!=` comparison of the two facts or of
  the kind, discriminated by a named mode. Meta-tests fail any ledger row that changes no answer,
  and any two rows that pin one answer on one kind in the same mode.
- **Planning dials nothing** and makes no store lookup; it reads config.

## References

- [ADR-041](adr_041_shared_ledger_tenancy.md): shared ledger tenancy; custom sources own `""`
- [ADR-047](adr_047_database_absence_vs_misconfiguration.md): the absence WARN, `DatabaseRequirer`,
  the readiness lease of `""`
- [ADR-067](adr_067_lifecycle_slots.md): resource slots and the Builder's step names
- [ADR-087](adr_087_messaging_tenancy_and_tenant_stamp.md): shared messaging tenancy and the tenant stamp
- [ADR-097](adr_097_sealed_amqp_messages.md): the three seal tenancies
- [ADR-125](adr_125_source_type_agrees_with_resource_source.md): `source.type` agrees with the
  store, so the plan reads `IsDynamic()` alone
- `app/resource_plan.go`, `app/bootstrap.go` (`dependencies`, `markConfigured`)
