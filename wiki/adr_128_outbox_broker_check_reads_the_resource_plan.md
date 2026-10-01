# ADR-128: The Outbox Broker Check Reads the Resource Plan

**Status:** Accepted
**Date:** 2026-09-29
**Amends:** [ADR-127](adr_127_resource_plan_rule.md) (its #1853 "Unchanged" consequence, what
presence feeds under per-tenant Tenancy, its rule table and resolves-on paragraph, its "Unchanged"
MT per-tenant answers and flags, and the #366 error's root keys),
[ADR-126](adr_126_resource_plan.md) (the row's two facts, and its unavailable rule)

## Context

An enabled outbox fails `Init` when its relay could never publish (issue #366): otherwise every
cycle's messaging resolve fails, each pending row's `retry_count` advances through
`MarkFailed("messaging unavailable")`, the row stays pending and is never dead-lettered, and
`/ready` stays 200. That check read only root config: the root `messaging.broker.url`,
`multitenant.enabled`, `outbox.tenancy` and `source.type`. Since ADR-127 the authority for the
control-plane key `""` is the store serving it (`Options.ResourceSource`, or the built-in store over
the root blocks), and the Resource plan's answer for the resolver the per-tenant ledger's relay uses
(`deps.Messaging`) already reaches `Init` as `ModuleDeps.MessagingConfigured`. The outbox did not
read it, so the two disagreed in both directions (#1853):

- **False pass.** Multi-tenant `messaging.tenancy: shared` with static tenants and no root broker
  booted: the check skipped every multi-tenant deployment, though messaging resolves on `""` there.
  So did a caller static store not serving `""` beside a root broker, single-tenant or multi-tenant
  shared.
- **False abort.** A single-tenant caller store serving `""` beside an empty root messaging block
  was refused on the root block's word, and so was a single-tenant dynamic store with no root
  broker, which the shared-ledger arm and the startup database check both exempt.

The inbox has no broker check: it discards the messaging resolver.

## Decision

### Per-tenant ledger

1. **The flag decides.** A per-tenant-ledger outbox (`outbox.tenancy: per-tenant`, the default)
   refuses `Init` exactly when `ModuleDeps.MessagingConfigured` is false. The arm's root-config
   read and its multi-tenant skip are gone. A hand-built `app.ModuleDeps{}` has the flag false, so
   it now refuses an enabled per-tenant-ledger outbox; set `MessagingConfigured: true`.
2. **The fan-out guard runs first.** The per-tenant fan-out enumerability guard (a dynamic source,
   or no static `multitenant.tenants`) now runs before the broker check, so a multi-tenant
   deployment with nothing to fan out to keeps its actionable `no static multitenant.tenants`
   error even when `MessagingConfigured` is false.
3. **The texts are source-neutral.** Both keep `messaging is not configured` and say that
   `ModuleDeps.MessagingConfigured` is false. Where messaging resolves on `""` (single-tenant, or
   multi-tenant `messaging.tenancy: shared`), the text points at the root `messaging.broker.url`,
   the custom resource source's answer for `""`, or `outbox.enabled=false`. Under multi-tenant
   `messaging.tenancy: per-tenant` it points at `multitenant.tenants.<id>.messaging.url`, never at
   `messaging.broker.url`, which config rejects beside static tenants there.
4. **Stream-only outboxes are included.** An outbox whose rows all target super streams still has
   an AMQP lane, and the flag speaks for it. Under multi-tenant `messaging.tenancy: shared` with no
   control-plane broker it now refuses `Init`, as single-tenant already did.

The shared-ledger arm (`outbox.tenancy: shared`) is decided as the next section says.

### Shared ledger

1. **A new field decides.** `app.ModuleDeps` gains `ControlPlaneMessagingAbsent`: the control-plane
   key `""` is known to hold no broker, because the store serving it answered `not_configured` at
   build. The app sets it next to the three flags from the plan's `""` presence fact alone, which is
   independent of Tenancy, never from `MessagingConfigured` or the row's unavailability. A
   shared-ledger outbox refuses `Init` exactly when it is true. The arm's root-config read and its
   `source.type: dynamic` exemption are gone: a dynamic store's `""` is knowable only at runtime, so
   the field stays false.
2. **The field speaks for `""` only.** The shared relay always publishes on `""` through the app's
   shared messaging resolver, whatever `messaging.tenancy` says. Under multi-tenant
   `messaging.tenancy: per-tenant` the field can read true while `MessagingConfigured` also reads
   true, which is why the shared ledger never reads that flag.
3. **The zero value is lenient.** False means "not known absent", so a hand-built
   `app.ModuleDeps{}` no longer aborts a shared ledger on root config; set the field to test the
   refusal. The name avoids "Shared…", which both tenancy settings use.
4. **The text is source-neutral.** The refusal says the shared relay publishes on the control-plane
   key and `""` holds no broker, and names the root `messaging.broker.url` or the custom resource
   source's answer for `""`.

### Per-tenant messaging with no tenant broker

1. **The row gains a third fact.** Under multi-tenant `messaging.tenancy: per-tenant` with the
   built-in store (no `Options.ResourceSource`) and at least one static tenant, config validation
   allows every tenant to omit `messaging.url` (all or none), and then every `deps.Messaging` call
   with a tenant in context fails with `config_missing` while the plan read messaging available: the
   per-tenant-ledger relay published nothing and the #366 gate let declarations through. Each
   kind's row now carries what the tenant keys hold, beside its Tenancy and what `""` holds. It is
   decided only for per-tenant messaging on the built-in store with static tenants — absent when
   none sets `messaging.url`, present otherwise — and is knowable only at runtime everywhere else,
   so a zero or hand-built row stays inert. The planner is told whether the store is the built-in
   one by its caller (`opts == nil || opts.ResourceSource == nil`); it never infers it, and never
   type-asserts the store, since a caller may pass `config.NewTenantStore(cfg)` and add tenants at
   runtime.
2. **Unavailable follows either fact.** A kind is unavailable when it resolves on `""` and `""` is
   known absent, or when it resolves per tenant and the tenant keys are known absent. The `""`
   conjunct stays, so a multi-tenant database or cache whose `""` is absent is still available, and
   the `""` fact itself is unchanged: `ControlPlaneMessagingAbsent` keeps reading it alone. In the
   D4 shape `MessagingConfigured` reads false, a per-tenant-ledger outbox refuses `Init` with the
   per-tenant text, and the #366 declarations gate refuses too.
3. **Not vacuous.** Zero static tenants (tenants omitted) is not "no tenant has messaging": that
   reading would change existing answers, and the per-tenant fan-out guard already refuses it.
4. **The store's own test.** The predicate is the built-in store's untrimmed `messaging.url == ""`,
   not the validator's trimmed check: a trimmed test would read false while `deps.Messaging`
   returns a client with a nil error, and false must stay definitive.
5. **Error kinds stay.** Per-tenant accessor errors remain the store's `config_missing` or
   `ErrNoTenantInContext`, neither of which satisfies `config.IsNotConfigured`; the `ModuleDeps`
   flag contract now says so.
6. **The #366 text follows the row.** `App.assertMessagingConfiguredIfDeclared` keeps
   `messaging is not configured` and chooses its advice from the plan's messaging row, never from
   config: when the row resolves per tenant and its tenant keys are absent it says no static tenant
   sets `messaging.url` under `messaging.tenancy: per-tenant` and to set
   `multitenant.tenants.<id>.messaging.url`; otherwise it names `messaging.broker.url`.
7. **Readiness and startup logs are unchanged.** The messaging readiness status keeps its
   `per_tenant` label (the probe reads Tenancy only), and the runtime-consumer step still logs that
   consumers start per tenant on demand.
8. **Known gaps.** Each keeps `MessagingConfigured` (or `CacheConfigured`) true while every resolve
   fails, and none is fixed here:
   - the D4 shape behind a caller static `ResourceSource` whose tenant answers are all absent: the
     plan cannot enumerate a caller store;
   - `messaging.tenancy: per-tenant` on the built-in store with zero static tenants, where every
     tenant resolve fails with `config_missing`;
   - a tenant fleet whose `messaging.url` values are all whitespace: it reads available while its
     clients never become ready;
   - the cache analog, multi-tenant with no tenant cache. D4 covers messaging only because a
     startup reader refuses on it — the per-tenant-ledger relay and the #366 gate turn an absent
     broker into rows that never publish — while no startup reader refuses on the cache, which a
     service may run without by design (`config.IsNotConfigured` on `deps.Cache`).

## Consequences

- **Now refuses where it booted:** multi-tenant `messaging.tenancy: shared` with no control-plane
  broker (the #1853 row), stream-only outboxes included; a caller static store not serving `""`
  beside a root broker, single-tenant or multi-tenant shared; any hand-built `ModuleDeps` without
  `MessagingConfigured: true` for an enabled per-tenant ledger.
- **Now boots where it refused:** a single-tenant dynamic store with no root broker, and a caller
  static store serving `""` beside an empty root messaging block.
- **Shared ledger, now refuses where it booted:** a caller static store not serving `""` beside a
  root broker, single-tenant or multi-tenant.
- **Shared ledger, now boots where it refused:** a caller static store serving `""` beside an empty
  root messaging block, single-tenant or multi-tenant `messaging.tenancy: shared` with no static
  tenants; and any hand-built `ModuleDeps`, whatever its root config. The dynamic-store exemption
  now comes from the plan instead of `source.type`.
- **Per-tenant messaging with no tenant broker, now refuses where it booted:** multi-tenant
  `messaging.tenancy: per-tenant` on the built-in store with static tenants none of which sets
  `messaging.url` — a per-tenant-ledger outbox refuses `Init`, and any messaging declarations
  refuse startup with the per-tenant #366 text. Behind a caller static store the same config
  still boots.
- **Unchanged:** multi-tenant `messaging.tenancy: per-tenant` for the per-tenant ledger wherever
  a tenant sets `messaging.url`, the store is a caller's, or there are no static tenants (the plan
  reads the flag true there); the shared ledger's refusal of that mode
  with no root broker; the inbox; and every `SetSharedResolvers`, `NewModuleRegistry` and
  `SetMessagingTenancy` signature.

## References

- [ADR-127](adr_127_resource_plan_rule.md): the rule behind `ModuleDeps.MessagingConfigured`
- [ADR-041](adr_041_shared_ledger_tenancy.md): shared-ledger tenancy
- [migrations.md](migrations.md) `[C70.7]`–`[C70.11]`
- `outbox/module.go` (`checkTenancyFanOutGuards`, `checkPerTenantLedgerBroker`,
  `checkSharedLedgerBroker`), `app/bootstrap.go` (`markConfigured`), `outbox/module_app_test.go`,
  `app/resource_plan.go` (`tenantKeysOf`, `kindPlan.unavailable`), `app/lifecycle.go`
  (`assertMessagingConfiguredIfDeclared`)
