# ADR-171: app.New Refuses Tenant Wiring the Resource Source Cannot Vouch For

**Status:** Accepted
**Date:** 2026-11-08
**Related:** [ADR-125](adr_125_source_type_agrees_with_resource_source.md) (source.type agrees with the resource source) · [ADR-170](adr_170_path_tenant_written_only_when_store_holds_it.md) (a path-read tenant is written only when the store holds it, amended here) · [ADR-081](adr_081_recovered_panic_values_reported_by_type.md) (panic values rendered by type)

## Context

ADR-170 decision 3 makes the tenant resource source the one authority on which tenants exist: the
App sets `server.Options.TenantChecker` from it. Three wirings kept a second authority alive. A
`TenantChecker` given in `app.Options.ServerOptions` was replaced without a word. A server passed
as `app.Options.Server` was used as given, with whatever checker it carried. A custom
`app.Options.ResourceSource` without `HasTenant`, or the built-in store holding no tenant, built a
server that refused every path tenant, and the first sign was at `Start`, after every resource
had been dialed, or at the first request. A check could also be unusable as wired: a nil
`*config.TenantStore` answers false for every id without panicking, and a `HasTenant` that panics
or answers true for `""` breaks the contract ADR-170 states.

## Decision

1. **`app.New` refuses the wiring at build, before any dial.** `checkPathTenantExistence` runs in
   `Builder.WithConfig` after `checkSourceAgreement`, and its error surfaces as
   `invalid configuration: <text>` from `app.New` and `app.NewWithConfig`:
   - `ServerOptions.TenantChecker` set, under any resolver: the App asks the resource source,
     so a second check is refused rather than replaced.
   - Under `multitenant.enabled` with a resolver that reads the request path (`path`, or
     `composite` with `path` in its order, `ResolverConfig.ReadsPath`):
     - a supplied `Options.Server`, which the App cannot wire to the store;
     - an `Options.ResourceSource` that does not implement `multitenant.TenantChecker`;
     - the built-in store holding no tenant, which would answer 400 to every path tenant.
2. **The server probes the check once, at construction.** When the tenant seat may read the path
   and `server.Options.TenantChecker` is set, `newServer` runs `vetExistence`: a check that is a
   nil pointer, whose `HasTenant("")` panics, or that answers true for `""` is refused, and the
   error joins the server's setup error, so `Start` refuses before binding. A panic is rendered by
   type only (ADR-081). An empty, non-nil `*config.TenantStore` passes, since `AddTenant` may fill
   it later; a hand-built server keeps that latitude.
3. **ADR-170 decision 3 is amended.** Under path tenancy the App no longer replaces a
   `ServerOptions.TenantChecker` or uses a supplied `Options.Server` as given: it refuses both.

## Alternatives considered

- **Keep replacing `ServerOptions.TenantChecker`.** Rejected. A value the consumer set and the
  framework discards without a word leaves two answers to one question.
- **Refuse at `Start` only.** Rejected. By then the databases, brokers and caches have been
  dialed; the wiring is known from configuration and options alone.
- **Probe with a known tenant id.** Rejected. The framework knows no id a custom source must hold;
  `""` is the one id every check must answer false for.

## Consequences

- **Breaking**: `app.New` refuses `ServerOptions.TenantChecker` under any resolver; under path
  tenancy it also refuses `Options.Server`, a `ResourceSource` without `HasTenant`, and a built-in
  store holding no tenant. `Start` refuses a check that is a nil pointer, or whose `HasTenant("")`
  panics or answers true. At v0.72.0 the App built every one of these wirings.
- A custom resource source under path tenancy holds its complete set before `app.New`, answers
  from memory, keeps the last good set on a failed refresh and never panics (ADR-170).

## References

- `app/app_builder.go`: `Builder.WithConfig`, `checkPathTenantExistence`
- `config/resolver_section.go`: `ResolverConfig.ReadsPath`
- `server/server.go`: `newServer`, `vetExistence`
- `app/interfaces.go`: `TenantStore`
- See [migrations.md](migrations.md) `[C73.38]`.
