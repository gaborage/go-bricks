# ADR-051: A Delivered-but-Empty Database Identity Field Fails Startup

**Status:** Accepted
**Date:** 2026-08-06

## Context

`config.IsDatabaseConfigured` (ADR-047) infers intent from decoded VALUES: any
non-empty identity field marks a database section as configured. It cannot
distinguish a field that was never set from one delivered as an empty string —
an empty `secretKeyRef`, `envsubst` over an unset variable, a templated file
rendering blank, or `DATABASE_HOST=""` written by a deployment tool. That shape
reads as absence: the service boots, `/ready` reports `not_configured` (200),
and the first query fails. ADR-047's own Consequences section named this as
the one shape its widened predicate could not see. Fixes #880.

The issue's original design proposed changing `IsDatabaseConfigured`'s
signature across its six call sites so it could consult koanf directly. That
turned out to be unnecessary: `config.Load` already stores the koanf instance
on `cfg.k` *before* calling `Validate`, and `Config.Exists` is nil-safe. A
validator living inside `Validate` can consult key presence without touching
the predicate's signature or any of its callers at all.

## Decision

**Presence-of-identity-KEY plus zero identity VALUES fails startup.**

`validateNoDeliveredEmptyDatabase` runs inside `config.Validate`, immediately
before `validateMultitenant`. For each database-bearing section — root
`database`, each `databases.<name>`, and, **only when
`multitenant.enabled: true`**, each static `multitenant.tenants.<id>.database`
— it first applies `IsDatabaseConfigured`;
a section with any real value short-circuits (later validators own that path
unchanged). Only when the decoded section is empty does it walk
`databaseIdentityKeys` (the same nine suffixes `IsDatabaseConfigured` checks)
and ask `cfg.Exists(section + "." + key)`. Every present key fails startup, and
all of them are named — the message promises "field(s)", and an operator who
cleared only the first would hit the same abort again. Offenders across all
sections are collected and reported together, sorted for determinism.

Placement is load-bearing: `validateMultitenantTenants` already rejects an
unconfigured tenant database with a generic "configuration required" message.
Running before `validateMultitenant` means a delivered-empty tenant section
gets the precise key path instead of the generic message — specific beats
generic. The validator's own logic has no dependency on any earlier
validation having run: identity fields are decoded values, never mutated by
defaulting.

The tenant gate is not cosmetic. Koanf populates `Multitenant.Tenants` from
YAML regardless of the enabled flag, but a leftover block is inert in
single-tenant mode — `TenantStore` skips it (`config/tenant_store.go`),
`ManagerConfigBuilder` does not count it (`app/bootstrap.go`), and
`validateMultitenant` returns before reaching it. Walking it unconditionally
would abort startup over config no deployment consumes.

Two shapes remain deliberately out of reach, both pre-existing
`IsDatabaseConfigured` blind spots this change does not touch:

1. **Hand-built `Config` values.** No koanf instance means `Exists` returns
   `false` for every key, so the validator is inert by construction — the
   design leans on this for the many struct-literal tests scattered across the
   suite, not just the new ones.
2. **Dynamic-source tenant configs.** Resolved from a remote store at
   request time, never routed through `config.Load`'s koanf instance.

## Alternatives considered

- **The issue's original six-call-site signature change.** Rejected: strictly
  more churn (every `IsDatabaseConfigured` caller now needs a `*Config` in
  scope) for the identical verdict the `Validate`-level seam already reaches
  with zero signature changes.
- **Classify inside `database.NewConnection`.** Rejected for the same reason
  ADR-047 rejected it there: key-blind, and it would turn a loud startup
  misconfiguration into a service that boots and fails at first query.

## Consequences

- **Breaking, and deliberately loud about it.** An ambient empty value for one
  of the nine identity keys — `DATABASE_HOST=""` from a CI fixture, a Helm
  chart default, a leftover override — now aborts startup instead of loading
  as database-free. The scope is narrower than "any `DATABASE_*`": it fires
  only when nothing else in that section carries a real value (an empty
  `DATABASE_HOST` beside a populated `DATABASE_TYPE` still falls to ADR-047's
  partial-section check), and non-identity keys — `database.tls.*`, pool,
  query — are excluded here exactly as they are from `IsDatabaseConfigured`.
  That is the point: an operator who intended "no database" should remove the
  key, not set it to empty.
- The residual blind spots (hand-built `Config` literals, dynamic-source
  tenant configs) are unchanged from ADR-047 and remain the honest limit of
  what a `Validate`-time, koanf-backed check can see.
- `IsDatabaseConfigured`'s signature, logic, and all six existing call sites
  are untouched — this is additive at the `Validate` level only.

## References

- `config/validation.go` — `validateNoDeliveredEmptyDatabase`,
  `deliveredEmptyDatabaseKeys`, `databaseIdentityKeys`, `IsDatabaseConfigured`
- `config/config.go` — `Load` (koanf instance stored before `Validate`),
  `loadDefaults` (registers no `database.*`/`databases.*` keys)
- [ADR-047](adr_047_database_absence_vs_misconfiguration.md) — the predicate
  this closes a gap in
- See [migrations.md](migrations.md) `[C57.6]`.
