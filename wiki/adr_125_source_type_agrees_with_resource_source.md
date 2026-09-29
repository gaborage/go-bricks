# ADR-125: `source.type` Must Agree with the Resource Source

**Status:** Accepted
**Date:** 2026-09-28

> **Amended (2026-09-29, [ADR-127](adr_127_resource_plan_rule.md)):** "under multi-tenancy,
> `IsDynamic()` changes nothing in the app builder" no longer holds. A store reporting `false` is
> asked for `""` once per kind at build, in every mode, and must answer it with a configuration or
> a not-configured error: any other error fails startup.

## Context

Two inputs say whether the resource keys (the root key `""` included) resolve at runtime from an
external store, and nothing required them to agree:

- **`source.type`** (`static` | `dynamic`), in config. Config reads it to decide whether the static
  `multitenant.tenants` map is validated and normalized. The outbox and inbox modules read it
  because they see only `*config.Config`: `internal/tenantstore.StartupCheckApplies` skips the
  `Init` database probe for `dynamic`, the per-tenant fan-out guard rejects `dynamic` under
  multi-tenancy, and the shared-tenancy messaging presence check skips `dynamic`. The app builder
  reads it too: `ConfigureRuntimeHelpers`' pre-init skip, `rootDatabaseAbsent`, `rootCacheAbsent`
  and `markConfigured` all treat `dynamic` as runtime-resolved.
- **`Options.ResourceSource.IsDynamic()`**, in code. Only the app builder can see it: the pre-init
  skip and `rootDatabaseAbsent` (the database-absence WARN and the `DatabaseRequirer` abort).

The store that actually serves the keys is chosen by `FactoryResolver.ResourceSource`:
`Options.ResourceSource` when set, otherwise the built-in `config.NewTenantStore`, which is static
and ignores `source.type`. And `source.type` was enum-checked only under `multitenant.enabled`; a
single-tenant deployment accepted any string.

Three shapes followed:

- **`source.type: dynamic` with no `Options.ResourceSource` — a silent drop, reproduced.** In
  single-tenant, every exemption treated the deployment as runtime-resolved: no pre-init, no
  absence WARN, no `DatabaseRequirer` abort, every `ModuleDeps.*Configured` flag true. Meanwhile
  the static built-in store served `""` from the empty root blocks, so `deps.DB` and `deps.Cache`
  failed every call with `not_configured`, and no startup line said so. Under multi-tenancy the
  same store served a tenant map that config had skipped validating, because its checks are gated
  on `static`.
- **`source.type: dynamic` with a store reporting `IsDynamic()` false.** Every reader took
  `source.type`'s word and treated the keys as runtime-resolved (no pre-init, no absence WARN, no
  ledger `Init` probe, per-tenant fan-out refused under multi-tenancy) while the store serving them
  declared itself static, so the answer a reader gave depended on which input it happened to read.
- **A dynamic store behind `source.type: static`.** The app skipped pre-init and exempted an empty
  root database, while the outbox and inbox `Init` probe, blind to `Options`, probed `""` at
  startup. `StartupCheckApplies` documented this as a mode it could not see.

A single-tenant typo such as `source.type: dynmic` read as not-dynamic everywhere and booted.

## Decision

1. **An empty `source.type` is `static`, in every mode.** Config's normalize phase fills an empty
   `source.type` (a hand-built `*config.Config`, which the koanf default never reaches, or a
   delivered-empty value such as `SOURCE_TYPE=` from an unset template value or `type: ""`) with
   `static`, before the multitenant section, whose static-tenant gate reads it. This is the pattern
   `messaging.tenancy` follows. Since normalize now owns the fill, the koanf default for
   `source.type` is derived from it instead of written a second time; the value is unchanged.
2. **A delivered value outside `{static, dynamic}` is refused, in every mode.** The existing check
   and its error (`config_invalid: source.type '<value>' is not supported must be one of: static,
   dynamic`, a `*config.ConfigError` with `Field` `source.type`) move out of the multitenant section
   into their own step of `config.Validate`. The wrap reads `source config:` where multi-tenant
   deployments used to see `multitenant config: source:`.
3. **The app build requires agreement.** `Builder.WithConfig` runs, right after `config.Validate` and
   before any logger, manager or connection exists:

   ```text
   (source.type == dynamic) == (Options.ResourceSource != nil && Options.ResourceSource.IsDynamic())
   ```

   A disagreement fails construction with a `*config.ConfigError` (category `invalid`, `Field`
   `source.type`) whose message names `Options.ResourceSource` and the direction, wrapped
   `invalid configuration:` as a `config.Validate` failure is. `errors.As` recovers it through
   `NewWithConfig`'s `failed to create app:` wrap.

   | `source.type` | `Options.ResourceSource` | build |
   | --- | --- | --- |
   | `static` (or absent) | nil | accepted |
   | `static` | `IsDynamic()` false | accepted |
   | `dynamic` | `IsDynamic()` true | accepted |
   | `dynamic` | nil | refused: the built-in static store would serve every key |
   | `dynamic` | `IsDynamic()` false | refused |
   | `static` | `IsDynamic()` true | refused |

   The table is the same in single- and multi-tenant deployments.
4. **The readers are unchanged.** `rootDatabaseAbsent`, `rootCacheAbsent`, `markConfigured`, the
   pre-init skip and `StartupCheckApplies` keep their code; the inputs they read now always agree.

## Alternatives considered

**Derive `source.type` from `IsDynamic()` at build.** Rejected: it rewrites operator config behind
the operator's back, and a `dynamic` with no resource source still has no dynamic store to serve
it. That shape is the silent drop, and it needs to fail, not to be reinterpreted.

**Hand `Options` to the ledger modules.** Rejected: the modules see `*config.Config` by design, and
a new seam carrying one bit that `source.type` already carries adds surface without adding truth.

**Enforce agreement only where a reader would diverge.** Rejected: which shapes diverge depends on
which modules are registered and on the tenancy, so the rule would be a matrix rather than one
comparison, and each new reader would reopen it.

## Consequences

- **The ledger modules' `source.type` reads become truthful.** `StartupCheckApplies` has no blind
  mode left: a dynamic store always comes with `source.type: dynamic`, so the outbox and inbox
  `Init` probe skips exactly the deployments whose `""` resolves at runtime. Its comment says so.
- **Breaking for three shapes and one spelling.** The three refused rows above, and a single-tenant
  `source.type` outside the enum, which used to boot, now fail startup. Migration is
  [migrations.md](migrations.md) `[C70.1]`. The exits: set `source.type: dynamic` beside a dynamic
  store; delete `source.type: dynamic` where no dynamic store exists (the built-in static store is
  what served the keys all along) or supply one; and, for a multi-tenant deployment whose outbox or
  inbox fans out per tenant, have the store report `IsDynamic()` false instead, since those modules
  reject dynamic multi-tenant sources and, under multi-tenancy, `IsDynamic()` changes nothing in the
  app builder.
- **An empty `source.type` validates in multi-tenant mode.** A hand-built config with an empty
  `Source`, and a delivered-empty `SOURCE_TYPE=` or `type: ""`, used to fail the enum check there
  (the delivered one at `config.Load`); both now read `static`, and the tenant map is validated and
  normalized. The empty value does not join the delivered-empty refusals, which admit only keys
  whose empty value fails open: here it fails closed, because the build refuses a dynamic store
  beside the `static` it reads as.
- **The two inputs are redundant for the app.** Because they always agree, a later change can have
  the app builder read the resource source's `IsDynamic()` alone without moving any verdict, while
  config and the ledger modules keep reading `source.type`.
- **`TenantStore.IsDynamic` has a stated contract.** Its godoc and
  [MULTI_TENANT.md](../MULTI_TENANT.md#custom-tenant-store-implementation) say it must agree with
  `source.type` and what it controls: in single-tenant, the pre-init skip and the root-database
  absence exemption; under multi-tenancy, nothing in the app builder, which is why a store behind a
  per-tenant outbox or inbox reports `false`. The agreement error's action names that exit too.

## References

- [ADR-041](adr_041_shared_ledger_tenancy.md): shared ledger tenancy and custom sources owning `""`
- [ADR-047](adr_047_database_absence_vs_misconfiguration.md): the database-absence WARN and
  `DatabaseRequirer`
- [ADR-064](adr_064_app_validates_every_config.md): every construction path runs `config.Validate`
- [outbox.md](outbox.md#startup-verification): the `Init` probe's exempt modes
- `config/phases.go` (`normalize`, `check`), `config/multitenant_section.go` (`normalizeSource`,
  `validateSourceConfig`), `app/app_builder.go` (`WithConfig`, `checkSourceAgreement`),
  `app/interfaces.go` (`TenantStore.IsDynamic`), `internal/tenantstore/tenantstore.go`
  (`StartupCheckApplies`)
