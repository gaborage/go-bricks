# ADR-047: Database absence is a config-resolution verdict, distinct from misconfiguration

- **Status**: Accepted
- **Date**: 2026-08-04
- **Supersedes in part**: [ADR-003](adr_003_database_by_intent.md)
- **Issue**: [#872](https://github.com/gaborage/go-bricks/issues/872)

## Context

A service with no database configured at all returned HTTP 503 from `/ready`,
permanently. `app/health.go` already intended to tolerate this — its
`handleDatabaseConnectionError` has a `config.IsNotConfigured(err)` branch that
returns a non-error `not_configured` status — but the branch could never fire.

The reported cause was `database/factory.go`, whose default switch arm reports an
unset `cfg.Type` as a bare `fmt.Errorf("unsupported database type: …")` with no
sentinel. That is the visible symptom. The actual defect is one layer earlier, in
`config/tenant_store.go`:

```go
if key == "" {
    if s.defaultDB == nil {          // structurally dead
        return nil, NewNotConfiguredError(...)
    }
    return s.defaultDB, nil
}
```

`NewTenantStore` sets `defaultDB: &cfg.Database` — the address of a value field,
so it is never nil. The database resolver tested *pointer nilness*; its two
siblings three lines away test *config content* (`BrokerURL` tests
`Broker.URL == ""`, `CacheConfig` tests `!Enabled`). An all-empty
`DatabaseConfig` therefore resolved successfully, and the connection factory —
which has no configuration vocabulary — rendered the verdict.

Two conditions were being conflated:

| Condition | Meaning | Correct outcome |
| --- | --- | --- |
| No database configured | benign; a DynamoDB-only or HTTP-forwarding service | ready |
| `type: mysql`, or any partial section | the operator asked for something real and got it wrong | fail loudly |

Collapsing them cost both directions. A database-free service could not become
ready, and — because `IsDatabaseConfigured` inspected only three of the seven
connection-identity fields — a config carrying `DATABASE_DATABASE` +
`DATABASE_USERNAME` + `DATABASE_PASSWORD` + `DATABASE_PORT` but no host or type
read as "not configured", passed `config.Load` silently, and failed at first
query.

## Decision

**1. Absence is decided at config resolution, scoped to the default key.**
`TenantStore.DBConfig` gains the content check its siblings already have, for
`key == ""` only. This is deliberately *not* placed in `database.NewConnection`:
the factory never sees the resolution key, so it cannot distinguish a
deliberately database-free service from a half-provisioned *tenant* or *named*
database, where absence is never legitimate. A tenant or named key that resolves
empty keeps falling through to the factory's loud error.

**2. Any connection-identity field means intent, and intent must be complete.**
`IsDatabaseConfigured` widens from three fields to every connection-identity field
(`connectionstring`, `type`, `host`, `port`, `database`, `username`, `password`, and
Oracle's `oracle.service.name` / `oracle.service.sid`, since Oracle names its target
with those rather than a database name).
Fields that `applyDatabasePoolDefaults` fills in — timezone, pool, query — are
excluded, so the verdict is identical before and after defaulting. Any partial
section now routes into `validateDatabase` and **fails startup**. Only a section
with literally zero identity fields is absence.

This is what settles the fail-fast-versus-silent-degradation tension: rather than
making the *verdict* conditional, the *predicate* is made strict. It also
strictly increases fail-fast coverage — a partial config previously booted and
503'd forever; now it never boots.

**3. The database probe stays `critical: true`.** A database that is configured
and unreachable must still fail readiness. Absence is expressed by the status,
never by demoting the probe.

**4. Multi-tenant deployments report `per_tenant` — but only once resolution has
actually failed.** The probe resolves the fixed `""` key, and
`config/validation.go` rejects a root `database:` block *when static tenants
exist*, so that key cannot resolve there and every such deployment was also
permanently 503. Reporting `not_configured` would claim the service has no
database, which is false when it has N tenant databases. A distinct status states
the truth: this component is resolved per tenant and was not probed.

The relabel happens in `handleDatabaseConnectionError`, **after** resolution,
never as an up-front short-circuit on `multitenant.enabled`. Multi-tenancy does
not imply the `""` key is unconfigured: a shared-ledger deployment
(`outbox.tenancy: shared`, ADR-041) resolves a real control-plane database
through exactly that key, and `source.type: dynamic` makes a root block legal
there. Deciding before probing would have left that database unprobed while
`/ready` returned 200 — and would have contradicted this ADR's own rejection of
the nil-`dbManager` alternative below. The probe therefore keeps `critical: true`
in every mode.

**Consequence worth stating plainly: where the `""` key genuinely does not
resolve, a multi-tenant deployment carries no database readiness *signal*.** The
probe is still registered and still `critical: true` — it simply reports
`per_tenant` with a nil error, so it can never block readiness — and there is no
startup gate or WARN either, since `rootDatabaseAbsent` exempts multi-tenant mode.
Whether `/ready` can still 503 then depends on the *other* components: since
ADR-046 a cache-enabled service has a critical cache probe by default. Per-tenant readiness is
not solved here; it needs its own design (which tenants, dynamic sources, partial
failure semantics).

**5. `dbManager` stays unconditionally non-nil**, and a custom
`DBConfigProvider` owns its own key semantics — the framework does not
second-guess a caller-supplied resolver.

## Consequences

### Positive

- A database-free service is ready, which is what `app/health.go` always intended.
- A partially delivered database config fails at startup instead of at first query.
- Static multi-tenant deployments stop returning a permanent 503.
- `deps.DB(ctx)` now satisfies `config.IsNotConfigured` for a database-free
  service, so consumers can branch on absence instead of matching error strings.
- The spurious startup WARN pair from `app/prewarm.go` disappears — its
  `IsNotConfigured` branch already existed and now fires.

### Negative

- A config that sets *any* database field without completing it stops loading.
  This is intended, and is a breaking change for anyone who relied on partial
  database config being ignored.
- `not_configured` cannot distinguish "deliberately database-free" from "config
  never arrived". The companion mechanism — `app.DatabaseRequirer` (#878) — is how
  a module supplies that missing intent; a startup WARN is the backstop for
  modules that do not. Neither reaches multi-tenant mode, which
  `rootDatabaseAbsent` exempts.
- **At the time of ADR-047, the widened predicate did not catch every partial
  delivery.** The shape it most conspicuously missed — **identity fields
  delivered but empty** (`DATABASE_HOST=""` from an empty `secretKeyRef`,
  `envsubst` over an unset variable, an empty mounted file), where the predicate
  sees a zero value and cannot tell "set to empty" from "never set", a more
  common Kubernetes failure than a wholly unmounted secret — is closed by
  **[ADR-051](adr_051_delivered_empty_database_identity.md)**, which rejects a
  delivered-but-empty identity key during `Load` by checking koanf key presence
  instead of decoded values; configs carrying no koanf instance (hand-built
  `Config` literals, dynamic-source tenant configs) stay outside its reach.
  What the predicate still does not catch:
  - **TLS material alone** (`database.tls.*`) — deliberately excluded, because it
    identifies no database. Note the inverse hazard, which predates this ADR: a
    ConfigMap-provides-`host`/`type` + Secret-provides-TLS split whose TLS secret
    fails yields `IsDatabaseConfigured == true`, passes validation (empty TLS is
    legal), and connects **without** the client certificate — a silent mTLS
    downgrade. The predicate is not a control for that.
  Oracle's `service.name` / `service.sid` *are* included, since Oracle names its
  target with those rather than a database name.

## Alternatives considered

**Classify inside `database.NewConnection`.** Rejected: key-blind. It would stamp
`not_configured` on a half-provisioned tenant, converting a loud misconfiguration
into a service that reports ready.

**Gate manager creation on config, yielding a nil `dbManager`.** Rejected: the
nil path exists and reports `disabled`, but making `dbManager` conditionally nil
spreads nil-handling across every consumer for no gain, and it disables readiness
for multi-tenant deployments whose control-plane database is a real dependency.

**Leave `IsDatabaseConfigured` narrow.** Rejected: its blind spot is exactly what
makes the fix dangerous — a half-injected secret would report as an intentionally
database-free service.

## References

- `config/tenant_store.go` — `DBConfig`
- `config/validation.go` — `IsDatabaseConfigured`
- `app/health.go` — `databaseManagerHealthProbe`, `handleDatabaseConnectionError`
- `database/factory.go` — `NewConnection`
- [ADR-003](adr_003_database_by_intent.md) — database by intent
- [wiki/migrations.md](migrations.md) — C56.14, C56.15
