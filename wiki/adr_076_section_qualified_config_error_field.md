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
  spelling for non-root sections, so `field == "database.host"` stops matching. The
  replacement has to be scoped to the database field families rather than a bare suffix
  match: `Field` is not a database-only namespace, and `cache.redis.host` ends in `.host`
  as well. Match `database.<key>` exactly, and require the `databases.` or
  `multitenant.tenants.` prefix before accepting a suffix — C60.16 carries the predicate.
- Root-section errors are unchanged, in both `Field` and rendered text.
- The rendered message for a non-root section loses its `databases.reporting: `
  prefix and gains the path inside the field instead. Log greps that pinned the old
  prefix need repointing; the path is still there, spelled once.
- ~~`Action` still carries the root env-var hint (`set DATABASE_DATABASE env var …`)
  even for a named section, whose real variable is `DATABASES_REPORTING_DATABASE`.~~
  **Superseded by the addendum below (C60.19):** the hint is now built from the
  qualified field. The deferral's cost, as stated here at the time: the error was
  internally inconsistent, `Field` naming one section while `Action` named the root's
  variable. Tracked as #1114, now closed.
- ~~The RUNTIME door stays root-spelled, so the codebase is qualified at startup only.~~
  **Superseded by the addendum below (C60.19).** As stated here at the time: a dynamic
  `DBConfigProvider` tenant reported `Field = "database.tls"` with the tenant key in the
  wrapping message, where the same tenant declared statically reported
  `multitenant.tenants.acme.database.tls`, so a consumer routing on
  `strings.HasPrefix(field, "multitenant.tenants.")` fired for static tenants and never
  for dynamic ones. Tracked as #1113, now closed.

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

> **Taken in the addendum below.** The premise that the connect door "has no section"
> was wrong: it is handed a `DBConfigProvider` resource key, which names one.

---

## Addendum (2026-08-21): the runtime door is addressed too, and the hint follows

`normalizeDatabaseValues` takes the section, as the alternative above described, and a new
exported `ApplyDatabasePoolDefaultsForKey` takes the resource key the config was resolved
for — `""` for the root database, `NamedDatabasePrefix + name` for `databases.<name>`, any
other string a tenant id. That vocabulary is the manager's own and needed no new concept; the
door translates it with `sectionForResourceKey`.

It is a second exported function rather than a second parameter on the existing one, which
is the one place this addendum departs from the alternative as written. `tools/migration` is
a separate module pinned to a RELEASED go-bricks and CI builds it that way, so an arity
change on a symbol it calls cannot compile until the next tag — the framework and its own
CLI could not both be correct in one commit. `ApplyDatabasePoolDefaults` therefore keeps its
signature and its root addressing, delegating with an empty key, and the CLI adopts the new
door with its pin bump.

Two consequences follow from doing it at that seam rather than at the callers:

- **`database.DbManager` and the migrate CLI's `tlsValidatingProvider` stop wrapping.**
  Both used to add the key back (`failed to apply pool defaults for key %s`,
  `tenant %q`) precisely because the error did not carry it. Now it does, and a wrap
  would print the same identity twice — the failure mode the main decision already
  names.
- **`Action` is re-pointed with `Field`.** A hint is only useful if the variable it
  names comes back to the key that failed, and the root-spelled hint did not: following
  `set DATABASE_PORT env var` on a multitenant config writes a partial root block, which
  ADR-047 then rejects as an incomplete section. The hint manufactured a second failure.

The hint is derived through `keyToEnvVar` and emitted **only when it round-trips** —
`envVar → Load's transform → the same key`. The transform is a blanket `_` → `.`, so it
is not injective: a section named `report_db` would be told to set
`DATABASES_REPORT_DB_PORT`, which reaches `databases.report.db.port` instead. Such a
section gets no env hint at all; the YAML half, always reachable, stays. That those
names are unreachable by variable in the first place is a separate defect — #1124.

Three sibling spellings in the tenant tree came along, for one rule rather than four:
the per-tenant cache error (`cache.*` with the tenant only in a wrapping message), the
messaging-consistency error (`multitenant.tenants messaging`, which was not key-shaped),
and `NewMultiTenantError`, whose `Field` was the prose `tenant 'acme' database`. All
three now spell `multitenant.tenants.<id>.<key>`.

Root-section errors remain byte-identical, in `Field`, `Action` and rendered text.

Deliberately not covered, and worth naming so this is not read as a finished sweep: the
per-key CACHE factory (`app/factory_resolver.go`) is a runtime door too, and it still emits
`cache.redis.host` with a `set CACHE_REDIS_HOST env var` hint even though it holds the
tenant key. A tenant whose cache is misconfigured is therefore addressed one way when
declared statically and another when resolved dynamically — the same asymmetry this
addendum closes for databases, one package over, and the same hint trap, since configuring
the ROOT cache does not give that tenant one. Tracked as #1125; the database seam's shape
(`qualifyConfigError` plus a key-to-section mapping) is what it should adopt.

Migration: [C60.19](migrations.md).

**Keep the wrapper and leave `Field` alone.** The status quo: the path is in the
message, so a human reading logs is fine. It fails the consumer reading the typed
error, which is the audience `ConfigError` exists for.

**Rewrite the Oracle outlier into a key path at the same time.** Tempting, and out of
scope here: renaming a field is a separate behaviour change for anyone matching on it,
and it would ride in unannounced under an addressing change.
