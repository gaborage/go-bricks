# ADR-128: The Outbox Broker Check Reads the Resource Plan

**Status:** Accepted
**Date:** 2026-09-29
**Amends:** [ADR-127](adr_127_resource_plan_rule.md) (its #1853 "Unchanged" consequence)

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

The shared-ledger arm (`outbox.tenancy: shared`) is unchanged: it still reads the root broker URL
with its `source.type: dynamic` exemption, and it never reads `MessagingConfigured`, which speaks
for `deps.Messaging`, not the shared resolver.

## Consequences

- **Now refuses where it booted:** multi-tenant `messaging.tenancy: shared` with no control-plane
  broker (the #1853 row), stream-only outboxes included; a caller static store not serving `""`
  beside a root broker, single-tenant or multi-tenant shared; any hand-built `ModuleDeps` without
  `MessagingConfigured: true` for an enabled per-tenant ledger.
- **Now boots where it refused:** a single-tenant dynamic store with no root broker, and a caller
  static store serving `""` beside an empty root messaging block.
- **Unchanged:** multi-tenant `messaging.tenancy: per-tenant` (the plan reads the flag true there,
  whatever the tenants hold), the shared ledger, the inbox, and every `SetSharedResolvers`,
  `NewModuleRegistry` and `SetMessagingTenancy` signature.

## References

- [ADR-127](adr_127_resource_plan_rule.md): the rule behind `ModuleDeps.MessagingConfigured`
- [ADR-041](adr_041_shared_ledger_tenancy.md): shared-ledger tenancy
- [migrations.md](migrations.md) `[C70.7]`, `[C70.8]`
- `outbox/module.go` (`checkTenancyFanOutGuards`, `checkPerTenantLedgerBroker`),
  `outbox/module_app_test.go`
