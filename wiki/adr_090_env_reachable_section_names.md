# ADR-090: User-Named Config Sections Must Be Reachable By Environment Variable

- **Status**: Accepted
- **Date**: 2026-08-30
- **Related**: [ADR-024](adr_024_config_key_flatsmush.md) (the same reachability property for FRAMEWORK leaf keys, solved by renaming them) · [ADR-039](adr_039_composite_resolver_order.md) (the resolved tenant-ID grammar this rule reuses) · [ADR-064](adr_064_app_validates_every_config.md) (every construction path runs `config.Validate`, so this check binds them all)

## Context

`Load` maps an environment variable to a config key by lowercasing it and turning every `_`
into `.`, koanf's path delimiter. That transform is **not injective**, and ADR-024 already
paid for half of the consequence: framework leaf keys were renamed to a flat-smushed form
(`log.sensitivefields`, not `log.sensitive_fields`) because an underscored leaf key was
unreachable from the environment.

The other half was left open. The keys under `databases`, `multitenant.tenants` and
`keystore.keys` are chosen by the USER, and nothing judged them. Verified on `main`:

- a lone `databases.report_db` plus `DATABASES_REPORT_DB_PORT` fails startup blaming a
  phantom `databases.report` — the variable reached `databases.report.db.port`;
- with a real sibling `databases.report` present, the same variable lands **silently** on
  the sibling, and `report_db` keeps its YAML value. Nothing errors; the wrong database is
  configured;
- an uppercase name (`databases.Reporting`) is unreachable the same way.

`[C60.19]` already suppressed the misleading "set `DATABASES_REPORT_DB_PORT`" hint these
errors used to print, which removed the false advice but left the underlying config
un-addressable.

## Decision

**A user-chosen section key must match `^[a-z0-9-]+$`, enforced at check.**

1. **Scope — exactly three maps**: `databases.<name>`, `multitenant.tenants.<id>`,
   `keystore.keys.<name>`. These are the maps whose keys become config PATH segments and are
   named by the operator. Header maps (`*.headers.<name>`) are excluded: a header name is a
   protocol identifier, not a config section, and the framework does not get to rename it.
2. **Check, not normalize.** The rule rejects a config without changing it, which is what
   `check` is for (CONTEXT.md). Normalization must not rename an operator's section behind
   their back: a silently renamed section is the same class of surprise as the silent
   collision this ADR exists to stop.
3. **The error names the key path.** `ConfigError.Field` is `databases.report_db`, not a bare
   `databases`, so an operator can find the entry; the action states the rule and says rename.
4. **Hyphen is legal**, and the grammar is deliberately the resolver's tenant-ID grammar
   (ADR-039) without its length bound. Whether a hyphenated name is *settable* depends on the
   runtime — Docker and Kubernetes permit `-` in variable names, POSIX `export` does not — which
   the docs state and the framework does not police.
5. **Dynamic tenant sources are not validated here.** Their IDs arrive at request time and
   the resolver's own `^[a-z0-9-]{1,64}$` is their gate; the static-source gate in
   `checkMultitenant` is what keeps them out of this check.
6. **The transform is untouched.** `keyToEnvVar` / `envVarToKey` and ADR-024's flat-smushed
   rule are byte-identical. The rule makes the existing transform injective over every key
   that survives startup, rather than making the transform injective in general.

### Why rejection rather than a startup warning

The failure this prevents is **silent and wrong**, not loud and inconvenient. A warning is
read only when someone is looking; the collision shape configures a different database than
the one the operator edited, and the deployment comes up green. A config that cannot be
driven from the environment also cannot be driven by the 12-factor deployment the framework
targets, so admitting it means admitting a config that works in YAML and quietly diverges in
production. Fail Fast (CLAUDE.md) applies exactly here.

### Why not an escape hatch

An alias map, a double-underscore escape (ADR-024 rejected that spelling explicitly) or a
schema-aware reverse lookup would each keep unreachable names working at the cost of a second
naming grammar for operators to learn and for the framework to keep consistent with koanf's
delimiter. One grammar, stated once, is cheaper than a mapping layer that must stay correct.

## Consequences

- **Breaking.** A config that booted now fails startup: any `databases`, `multitenant.tenants`
  or `keystore.keys` key carrying `_`, an uppercase letter, or any other character outside
  `[a-z0-9-]`. The remedy is a rename, and the error states it. See `[C61.24]`.
- Renaming a section is not free for an operator: the YAML key, any `DATABASES_<NAME>_*`
  variables, and any code calling `deps.DBByName("report_db")` move together. That cost is
  the point — the alternative is a section whose environment variables silently address
  something else.
- The two collision shapes become validation errors instead of misconfigured runtime state,
  and are pinned as regression tests.
- Dynamic tenant providers are unaffected, so a pool-model deployment delivering `acme_corp`
  from an external store keeps working; the resolver judges those IDs at request time.
