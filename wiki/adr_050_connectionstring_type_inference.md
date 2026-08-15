# ADR-050: Infer `database.type` from the Connection-String Scheme, Fail Fast on What's Left Untyped

**Status:** Accepted
**Date:** 2026-08-05

> **Amended (2026-08-14):** Decision item 1 names
> `config.validateDatabaseWithConnectionString` as the inference site; it is no
> longer the only one. `config.ApplyDatabasePoolDefaults` — the seam
> `database.DbManager.createConnection` applies to every config a dynamic
> `DBConfigProvider` returns, and which never reaches `config.Validate` — now
> performs the same scheme inference, so anyone extending the recognized-scheme
> list has two call sites to account for rather than one (both delegate to
> `inferDatabaseTypeFromConnectionString`, so the list itself stays in one
> place). The conflicting-`Type` **error** in item 1 is unchanged and remains
> Validate-only.
>
> **Inference is unconditional on both paths, regardless of connector.** Item 2's
> exemption is scoped to the startup **guard** ("fails startup … but only when
> the built-in connector would be used") and never exempted anything from
> inference. `config.Validate` is connector-blind — the `config` package holds no
> reference to `Options.DatabaseConnector` at all — so a static `postgres://`
> config under a custom connector has carried a populated `Type` since item 1
> shipped, and a connector branching on `cfg.Type == ""` was already wrong there.
> Inference normalizes the config *shape*; only the guard is connector-aware.
> `ApplyDatabasePoolDefaults` now also runs `validateVendorSpecificFields`. See
> Consequences and [migrations.md](migrations.md) `[C59.6]`.

## Context

A `database.connectionstring` with no `database.type` passes `config.Validate`
today (`validateDatabaseWithConnectionString` only checks `Type` when it is
non-empty) and then can never produce a connection: `database.NewConnection`
dispatches solely on `cfg.Type` and errors with `unsupported database type: ""`
at first use. Startup validation accepts a configuration that is guaranteed
dead — the opposite of Fail Fast. Fixes #877.

A hard `type` requirement was rejected: two working setups legitimately carry
an empty `Type` alongside a connection string. The quiesce CLI path
(`tools/migration/internal/commands/quiesce.go`) builds a PostgreSQL DSN and
tolerates `Type == ""`. Consumers supplying `Options.DatabaseConnector` parse
the DSN themselves and have no valid `Type` value to fake — only
`postgresql`/`oracle` validate.

## Decision

**Infer at validation, fail fast at the builder — only for the built-in
connector.**

1. `config.validateDatabaseWithConnectionString` infers `Type` from a
   recognized DSN scheme when `Type` is empty: `postgres://` / `postgresql://`
   (pgx) → `postgresql`, `oracle://` (go-ora) → `oracle`. An explicit `Type`
   that conflicts with the inferred scheme is a validation error naming
   `database.type`. An unrecognized scheme is not an error at this layer —
   whether an untyped DSN is fatal depends on who connects. The write-back
   pattern is the one `validateDatabaseWithConnectionString` already uses for
   pool/session defaults, so named databases and static-tenant entries pick
   the inference up automatically through their existing persist-back paths.
2. `app.Builder.ConfigureRuntimeHelpers` fails startup when any database path
   (root, `databases.*`, and — only under `multitenant.enabled: true` —
   `multitenant.tenants.*`) still carries a connection string with no resolved
   type, but only when the built-in connector (`database.NewConnection`) would
   be used. A caller-supplied `Options.DatabaseConnector` parses the DSN itself
   and is exempt. The tenant gate mirrors `config.NewTenantStore`, which copies
   static tenant entries only when multitenancy is enabled: koanf populates
   `multitenant.tenants` from YAML regardless of the flag, so a leftover block
   under a disabled flag is inert config that neither reaches inference nor
   reaches a connector, and must not abort startup.

3. `config.validateOracleFields` waives its identifier requirement when a
   connection string is set. Inference makes Oracle's vendor-specific
   validation run on a DSN-only config for the first time (an empty `Type` hit
   `validateVendorSpecificFields`' `default` arm and skipped it), and that
   validation demanded one of `oracle.service.name` / `oracle.service.sid` /
   `database` — a field `buildOracleDSN` never reads, because it returns
   `cfg.ConnectionString` verbatim. Requiring an identifier that is then
   ignored would have made ADR-050 reject the very configuration it exists to
   support. The waiver covers the missing-identifier check only: the
   `count > 1` ambiguity error and the Oracle TLS rejection both still fire
   alongside a connection string.

`database/factory.go`'s dispatch and its error are unchanged: classification
stays in the config/app layers, not the connector.

## Alternatives considered

- **Hard-require `type` whenever a connection string is set.** Rejected: it
  breaks the quiesce CLI's tolerated-empty-`Type` PG path and forces every
  custom-connector consumer to supply a `Type` value they have no use for and
  that `validateDatabaseType` would reject.
- **Infer only, no builder guard.** Rejected: an unrecognized scheme (or
  future third vendor) still boots into a dead built-in connector — the
  residue this ADR closes. Inference alone fixes the common case but not the
  guarantee.
- **Guard only, no inference.** Rejected: every working `postgres://`/`oracle://`
  DSN would need its `Type` spelled out redundantly, which is exactly the
  friction issue #877 flagged as the trap in the first place.

## Consequences

- **Breaking, in the surprising direction.** A connstring-only config with a
  recognized scheme that used to boot and fail at first use now **connects**
  — a deployment that "worked" only because its DB layer was never exercised
  now dials a real database at startup pre-init.
- A connstring with an unrecognized scheme and no type, on the built-in
  connector, now **fails startup** instead of booting into a dead connection.
- An explicit `database.type` that conflicts with the DSN scheme now fails
  validation instead of silently taking the explicit value.
- **Oracle validation is loosened, not tightened.** `oracle://user:pw@host:1521/XE`
  with no `oracle.service.name`, `oracle.service.sid` or `database` now validates,
  where `type: oracle` spelled out alongside the same DSN used to fail with
  "oracle connection identifier exactly one required". Nothing that validated
  before stops validating. `count > 1` still fires alongside a connection string:
  no valid config needs two identifiers, and while all of them are inert in DSN
  mode, an operator who set two has a broken mental model worth failing on.
- Inference and the guard are two halves of one contract, and only
  `config.Load` runs the first half — it is the sole caller of
  `config.Validate`. A consumer handing `app.NewWithConfig` a hand-built
  `*config.Config` gets the guard without the inference, so even a
  `postgres://` DSN reaches it untyped and aborts startup. The guard's message
  therefore reports the observed state ("connectionstring has no resolved
  database type") rather than inferring a cause, so it stays true whether
  inference ran and rejected the scheme or never ran at all. Such a config was
  already dead for the root `database:` block (startup pre-init dispatched on
  the empty `Type` and failed); the change is that `databases.*` entries, which
  used to fail lazily on first use, now abort at startup too. Hand-built
  configs must run `config.Validate` before `NewWithConfig`. The same shape
  appears when `multitenant.enabled: true` is paired with
  `source.type: dynamic` and a leftover static `multitenant.tenants` block:
  `validateStaticTenantConfig` skips tenant validation for a dynamic source, so
  those DSNs never reach inference either.
- `Options.DatabaseConnector` consumers and the quiesce CLI's tolerated-empty
  PG path are exempt from the builder guard, not from validation: `config.Validate`
  is connector-blind, so a `type` contradicting the DSN scheme fails for them too.
  The recognized-scheme list is deliberately closed to `postgresql`/`oracle`
  (ADR-012); landing a third vendor would need both
  `inferDatabaseTypeFromConnectionString` and the builder-guard message
  extended in the same commit. See [migrations.md](migrations.md) `[C57.5]`.
- Inference now also runs on the **dynamic-resolution** path.
  `config.ApplyDatabasePoolDefaults` — the seam
  `database.DbManager.createConnection` applies to every config a
  `DBConfigProvider` returns — infers a missing `Type` from the DSN scheme, so a
  dynamic multi-tenant source returning `{ConnectionString: "postgres://…"}`
  dials instead of failing that tenant's every request with
  `unsupported database type: ""`. The explicit-conflict **error** stays
  Validate-only: this seam runs per connection, where a wrong explicit `Type`
  is better surfaced by the vendor dial error than converted into a config
  error for one tenant at a time.
- **This narrows the `Options.DatabaseConnector` exemption.** A custom connector
  is still exempt from `ConfigureRuntimeHelpers`' startup guard, but it now
  receives `Type` already inferred to `postgresql`/`oracle` for a recognized
  scheme, where it previously received `""`. A connector that branches on
  `cfg.Type == ""` to decide whether to parse the DSN itself must be reviewed.
  Unrecognized schemes are unaffected — `Type` stays `""`.
- **Vendor-specific validation now runs on the dynamic path too.** Inference
  alone would have re-opened, from the other side, the hole item 3 reasons
  about: on the dynamic path Oracle TLS material was silently dropped and a lone
  PostgreSQL `sslcert` under `sslmode=disable` silently ignored — the states the
  vendor validators exist to prevent — so `ApplyDatabasePoolDefaults` runs them
  after inference and before defaulting. The reach is wider than the DSN-only
  shape that motivated the seam: a dynamic config with an **explicit**
  `Type: oracle` plus `database.tls.cert`/`key`/`ca` connected before and now
  fails at connection acquisition. The seam stays asymmetric by design — it
  errors on dropped TLS material while still tolerating a `Type` that
  contradicts the scheme, because that conflict fails loudly at dial with the
  vendor's own error whereas dropped TLS material fails silently open.
  Normalization happens on a clone that is committed to the caller's config only
  after every step succeeds, so a rejected config returns untouched — neither
  reclassified nor carrying half-applied pool defaults.
