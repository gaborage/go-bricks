# ADR-163: Migration Tenant IDs Follow the Framework Grammar

**Status:** Accepted
**Date:** 2026-10-31

## Context

The migration package checked tenant IDs with its own rule. `SecretsProvider.DBConfig` admitted
`^[A-Za-z0-9_-]{1,128}$`, wider than `multitenant.ValidateTenantID` (`^[a-z0-9-]{1,64}$`) on
case, `_` and length. So a migration run provisioned tenants such as `Acme`, `acme_1` or a 65-byte
ID, which the HTTP tenant seat and the messaging stamp reader then refuse on every request and
delivery.

The HTTP tenant source (`migration/source/http`) only trimmed each listed `id`, so whatever a
control plane returned reached `MigrateAll`, the secret-name composer and the CLI's plain-text
`list` output unchecked. Its `ContractError` wrote the envelope's `code` and `message` as sent.
[ADR-018](adr_018_multi_tenant_migration_cli.md) said tenant IDs from the listing are used
verbatim.

ADR-162 (#2227) keeps the framework grammar fixed and makes it the one rule at the framework's
write doors. This ADR brings migration onto the same rule.

## Decision

1. **`SecretsProvider.DBConfig` checks the framework grammar.** It trims the ID, keeps
   `ErrEmptyTenantID` for a blank one, then calls `multitenant.ValidateTenantID` before `NameFor`
   or `Fetch` runs. An off-grammar ID returns that error, which wraps
   `multitenant.ErrInvalidTenantID` and carries the byte length only. `migration.ErrInvalidTenantID`
   and its wider pattern are deleted, not aliased.
2. **The HTTP source refuses a listing that holds an off-grammar ID.** `TenantSource.ListTenants`
   trims each `id` and skips a blank one, as before. Any other ID that fails `ValidateTenantID`
   fails the whole listing: it returns `nil` and an error naming the page and entry (1-based),
   never the value. It never skips the ID and carries on, because a run that silently migrates
   part of the fleet is the worse failure. `MigrateAll` therefore stops before dispatching anything.
3. **`ContractError.Error()` quotes the control plane's text.** `Code` and `Message` render with
   `%q`. The struct's fields keep the raw values, and the no-envelope form is unchanged.

## Consequences

- A migration run provisions only tenants the runtime can resolve. The framework grammar is a
  strict subset of the old allowlist, so the property that allowlist guarded (no whitespace,
  control bytes, slashes or dots in a composed secret name) is kept and tightened.
- Every ID the HTTP source returns conforms to the grammar, and the control plane's error text
  renders escaped. A misbehaving control plane can no longer put control characters or escape
  sequences into operator terminals or CI logs through either.
- A fleet whose control plane or secret store names a tenant with uppercase, `_` or more than 64
  bytes fails at the listing or at the secrets lookup once upgraded. Rename those tenants into the
  grammar first. See [migrations.md](migrations.md) `[C73.23]`.
- `tools/migration` pins go-bricks v0.72.0, so `go-bricks-migrate` takes this from its routine
  go-bricks pin bump.
- The static source (`migration/source/static`) reads `multitenant.tenants`, which #2227 checks at
  `config.Load`. An operator-typed `--tenant` meets the secrets check.

## Alternatives considered

- **Keep migration's wider rule and document it.** Rejected: it provisions tenants the runtime can
  never resolve.
- **Skip an off-grammar listed ID and migrate the rest.** Rejected: the fleet would be migrated in
  part while the run reports success for every tenant it saw.
- **Check every lister's IDs inside `MigrateAll`.** Not taken: the HTTP source is the only lister
  that reads a remote party, and the secrets check covers the others that reach a secret store.

## References

- [ADR-018](adr_018_multi_tenant_migration_cli.md): the listing contract and the credential
  convention, amended by this ADR.
- ADR-162 (#2227): the framework tenant-ID grammar stays fixed.
- [multi_tenant_migration.md](multi_tenant_migration.md#pre-defined-http-listing-contract): the
  listing contract's rules.
- `multitenant/tenant_id.go` (`ValidateTenantID`, `ErrInvalidTenantID`).
- `migration/secrets.go` (`SecretsProvider.DBConfig`); `migration/source/http/source.go`
  (`TenantSource.ListTenants`, `ContractError.Error`).
- gaborage/go-bricks#2286.
