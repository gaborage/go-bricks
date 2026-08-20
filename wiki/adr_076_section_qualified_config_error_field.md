# ADR-076: A database section's errors are addressed to that section

- **Status**: Accepted
- **Date**: 2026-08-20
- **Related**: [ADR-047](adr_047_database_absence_vs_misconfiguration.md) (absence vs misconfiguration, which is what the root section's placement rule encodes) · [ADR-051](adr_051_delivered_empty_database_identity.md) (the delivered-empty check, whose key spelling this now matches)

## Context

A deployment can carry several database sections: the root `database`, any number
of `databases.<name>`, and — under multitenancy — `multitenant.tenants.<id>.database`.
They share one normalization module, and therefore one set of error constructors,
all of which name their fields in the root spelling: `database.host`,
`database.type`, `database.tls`.

At the startup door the section path was attached by wrapping —
`fmt.Errorf("%s: %w", section.path, err)` — so the rendered message read
`databases.reporting: config_missing: database.database required`. The path was in
the text, but the `*ConfigError` a consumer reaches through `errors.As` still said
`Field = "database.database"`. A consumer that switches on `Field` — to point an
operator at the offending key, to decide whether a tenant is safe to skip — could
not tell which section failed, and would act on the root section's name for a
failure in someone else's.

That also put the module at odds with its neighbour: ADR-051's delivered-empty check
already emits fully path-qualified keys for exactly these sections
(`databases.reporting.host`), and `NewNamedDatabaseError` already addresses a named
section by name. Two spellings for the same key, from the same package.

## Decision

The section path is carried by `Field`, and only by `Field`.

`normalizeDatabaseSection` — the startup door, the one place that knows both the
section path and its placement — rewrites the error it gets back before returning
it. A key under the root section swaps its `database` head for the section path, so
`database.host` becomes `databases.reporting.host` and, for a tenant, the section's
own trailing `.database` is preserved: `multitenant.tenants.acme.database.host`.
That is byte-identical to what the delivered-empty check emits for the same key.

A field that is not key-shaped is prefixed rather than rewritten. The Oracle
connection-identifier check names one (`oracle connection identifier`), and prefixing
keeps the offending name — `databases.reporting.oracle connection identifier` — where
a strict rewrite would have to drop it.

The rewrite works on a copy. The constructors are shared with the connect door
(`dbStrictnessConnect`), which resolves a config with no section path to speak of, so
mutating the original would leak a path into errors that have no section. The root
section is returned untouched, and the wrapping message is gone: the path in `Field`
is the one copy.

## Consequences

- A consumer matching `ConfigError.Field` against a literal now sees the qualified
  spelling for non-root sections. `errors.As` plus `strings.HasSuffix(field, ".host")`
  survives the change; `field == "database.host"` does not.
- Root-section errors are unchanged, in both `Field` and rendered text.
- The rendered message for a non-root section loses its `databases.reporting: `
  prefix and gains the path inside the field instead. Log greps that pinned the old
  prefix need repointing; the path is still there, spelled once.
- `Action` still carries the root env-var hint (`set DATABASE_DATABASE env var …`)
  even for a named section, whose real variable is `DATABASES_REPORTING_DATABASE`. Left
  alone deliberately — rewriting hint text is a different problem from addressing an
  error — but the deferral has a cost worth stating: the error is now internally
  inconsistent, `Field` naming one section while `Action` names the root's variable.
  Before this change the wrapping prefix framed the whole error as the section's, so the
  root-spelled hint read as a template. Tracked as #1114.
- The RUNTIME door stays root-spelled, so the codebase is qualified at startup only. A
  dynamic `DBConfigProvider` tenant resolved through `database.DbManager` reports
  `Field = "database.tls"` with the tenant key in the wrapping message, where the same
  tenant declared statically now reports `multitenant.tenants.acme.database.tls`. A
  consumer routing on `strings.HasPrefix(field, "multitenant.tenants.")` therefore fires
  for static tenants and never for dynamic ones. That is the connect door's asymmetry
  (ADR-050), which this ADR does not touch; it is tracked as #1113, together with the
  three other spellings the tenant tree still uses for sibling failures.

Migration: [C60.16](migrations.md).

## Alternatives considered

**Thread the section path into every error constructor.** It would put the path at
the point each error is raised, which reads well — and it changes every call site in
the package for a property only two of the three doors have, then has to answer what
the connect door passes. The door that knows the section is the door that should say
so.

**Give `normalizeDatabaseValues` the section, so its errors are born addressed.** The
middle option between the two above: one parameter at one seam, no `errors.As`, no
prefix surgery, and `Action` would be built from the right head for free. It is the
better shape and it is deliberately not taken here, because it changes the signature
the connect door shares — the door that has no section — and this change is meant to be
readable as an addressing fix, not a re-plumbing of the module. If the runtime door is
ever qualified too, this is where to start.

**Keep the wrapper and leave `Field` alone.** The status quo: the path is in the
message, so a human reading logs is fine. It fails the consumer reading the typed
error, which is the audience `ConfigError` exists for.

**Rewrite the Oracle outlier into a key path at the same time.** Tempting, and out of
scope here: renaming a field is a separate behaviour change for anyone matching on it,
and it would ride in unannounced under an addressing change.
