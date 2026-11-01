# ADR-162: The Tenant-ID Grammar Is Fixed and Enforced at Every Write Door

**Status:** Accepted
**Date:** 2026-10-31

## Context

`multitenant/tenant_id.go` holds the framework's tenant-ID grammar, `^[a-z0-9-]{1,64}$`, behind
`multitenant.ValidateTenantID` and `multitenant.DefaultTenantIDPattern()`. `ErrInvalidTenantID`
carries the byte length only, never the value. No ADR decided it. Its history is four commits:

- #48 introduced the grammar as a configurable pattern.
- #49 renamed its key to `multitenant.validation.pattern`.
- #51 deleted the config struct and hardcoded the pattern, with no recorded rationale. It left the
  YAML block in `config.example.yaml`, where nothing reads it (#2220).
- #1240 moved the pattern from `server` to `multitenant` to break the messaging-to-server import
  cycle.

Two readers enforced it: the HTTP tenant seat (`buildTenantResolver` wraps every resolver in a
`multitenant.ValidatingResolver`, and an off-grammar ID answers 400 `Invalid tenant`) and the
messaging stamp read (`tenantstamp.Read`, ADR-087 §6). Two write doors did not:

- **Static config.** `checkMultitenantTenantEntry` judged a static `multitenant.tenants.<id>` key
  only through `checkSectionName` (ADR-090), the same character class with no length bound. A
  65-byte key passed `config.Load`, and the seat then refused it on every request.
- **The publish stamp.** `tenantstamp.Resolve` stamped whatever the context tenant or the client's
  replay key held. `messaging.ResolveTenantStamp(multitenant.SetTenant(ctx, "Acme"), nil)`
  returned `"Acme"`, and a go-bricks consumer under `messaging.tenancy: shared` refused that stamp:
  a nack without requeue on the classic lane, an uncommitted offset on the streams lane. The
  failure surfaced on the consuming service, not at the publish call.

`config.example.yaml`'s `multitenant.validation.pattern` suggested the grammar could be changed.
It cannot, and several sinks depend on that:

- **The `named:` key space.** `config.TenantStore.DBConfig` reads a key starting with
  `NamedDatabasePrefix` (`named:`) as a named database, so a tenant `named:x` would resolve the
  shared `databases.x`. Only the grammar keeps the two key spaces apart.
- **The cache key delimiter.** A tenant cache's prefix is built by `internal/cachekey` `Join` as
  `<prefix>:<tenant>:<key>` with no escaping (ADR-117). A tenant `acme:eu` would write the keys of
  tenant `acme` under a caller key starting `eu:`.
- **The sealed tenant slot.** `jose/sealed` caps the signed `tid` at `MaxTenantIDLen` (64).
- **The stamp wire contract.** A producer and a consumer on different services must agree on
  which stamps are valid. Per-service configuration cannot keep them in agreement.

No SQL identifier, Flyway schema or target is built from a tenant ID; every SQL sink is
parameterized. The old wiki rationale ("DB schema names") was wrong.

## Decision

1. **`^[a-z0-9-]{1,64}$`, applied through `multitenant.ValidateTenantID`, is the one tenant-ID
   grammar, and it is never configurable.** No config key, option or setter widens or narrows it.
   `DefaultTenantIDPattern()` keeps its name although no other grammar exists.
2. **It is the fleet-wide wire contract of the `x-tenant-id` stamp,** and the isolation invariant
   for every key space a tenant ID shares: the `named:` database keys, the cache key delimiter `:`
   and the 64-byte `MaxTenantIDLen`.
3. **A tenant identity that does not fit is mapped into the grammar,** inside a route family's
   `Tenant` step (#2166), never by widening the grammar. There is no known consumer need for an ID
   outside it.
4. **Static config load enforces it.** `checkMultitenantTenantEntry` calls
   `multitenant.ValidateTenantID` after `checkSectionName`, so the existing messages for `.`,
   uppercase and `_` win and the new check adds only the 64-byte bound. Its `ConfigError` has
   `Field` `multitenant.tenants.<id>`, `Message` `tenant ID is N bytes, maximum is 64 (the
   tenant-ID grammar)`, and an `Action` starting `rename it to 64 bytes or fewer`.
5. **The publish stamp enforces it.** `tenantstamp.Resolve` keeps its precedence switch, and the
   conflict rule (`ErrConflict`) still wins. A non-empty decided stamp that fails
   `ValidateTenantID` is refused with an error wrapping `multitenant.ErrInvalidTenantID`, which
   reads `messaging: tenant stamp: multitenant: tenant id does not match the tenant-ID grammar: N
   bytes`. An empty stamp (a control-plane event) is not judged. Every publish door resolves
   through `Resolve`: the stamping publisher, the typed publisher, the sealed codec, the streams
   publisher, `messaging.ResolveTenantStamp` and outbox `Publish`. A refused publish sends nothing
   and persists nothing. The sentinel is not re-exported per lane as `ErrTenantStampConflict` is
   (ADR-087 §3), because every caller that can set a tenant already imports `multitenant`. The
   sentinel's text drops "default", since publish errors and the outbox ledger's error column now
   carry it.
6. **The outbox relay parks a stored off-grammar stamp as poison.** A row enqueued before the
   upgrade under an off-grammar tenant is refused every cycle. The AMQP shipper checks a non-empty
   stored stamp with `ValidateTenantID` before it resolves the client: in a per-tenant ledger an
   off-grammar tenant is unknown, so its client lookup fails, and without the check the row would
   read as a broker outage that stops the cycle every time. The stream shipper resolves no
   per-tenant client, so `Resolve`'s refusal when the relay re-stamps the row is enough. Both
   shippers classify `multitenant.ErrInvalidTenantID` as `shipPoison` beside
   `ErrTenantStampConflict`, so the row is dead-lettered at `outbox.maxretries` instead of retried
   forever.
7. **`config.example.yaml`'s unread `multitenant.validation.pattern` block is deleted.**

## Alternatives considered

- **Record the rule only (A).** Rejected: a 65-byte static tenant would still boot unreachable, and
  a producer would still stamp values its consumers refuse, failing on another service.
- **Narrowing-only configuration (C).** Rejected: two services with different narrowings disagree at
  the HTTP edge, `auth.TenantClaim` (#2166) would need a validator handle it is built without, and
  there is no known consumer need.
- **A configurable superset, or per-store or per-family grammars (D).** Rejected: every shared sink
  would need to change (split the `named:` space, escape the cache delimiter, widen
  `MaxTenantIDLen` and the metric attribute patterns), and per-service configuration still cannot
  keep the stamp contract fleet-wide. The `Tenant` step already maps a foreign identifier.

## Consequences

- **Breaking.** A static `multitenant.tenants.<id>` key over 64 bytes, a hand-built
  `MultitenantConfig.Tenants` key included, now stops boot. Such a tenant was already unreachable
  over HTTP and unsealable.
- **Breaking.** A publish under an off-grammar context tenant or replay key returns
  `multitenant.ErrInvalidTenantID` and sends or persists nothing. Before, it was sent: a
  shared-tenancy go-bricks consumer refused it, but a per-tenant go-bricks consumer (which ignores
  the stamp) and a consumer outside go-bricks accepted it. Those are the paths that are lost.
- **Breaking.** A pending outbox row stored under an off-grammar stamp before the upgrade parks at
  `MaxRetries` instead of being delivered.
- **Unchanged:** the HTTP seat, which already refused an off-grammar ID and is now pinned through
  `server.New`; `tenantstamp.Read` and the delivery pipeline; a composite chain's fall-through on a
  malformed candidate (ADR-039); and the writers that still check nothing: `multitenant.SetTenant`,
  `multitenant.ForEachTenant`, `config.TenantStore.AddTenant`, dynamic tenant stores, and a
  caller-built resolver passed to `server.TenantMiddleware`. An off-grammar ID set through one of
  those is now refused when it reaches a publish.
- ADR-090 §4's premise that `config` cannot import the resolver's grammar is retired: the grammar
  lives in `multitenant`, which does not import `config`. `internal/keyname` stays the gate for the
  other user-named sections.

See [migrations.md](migrations.md) `[C73.22]`.

## References

- [ADR-039](adr_039_composite_resolver_order.md): tenant resolution and composite fall-through.
- [ADR-087](adr_087_messaging_tenancy_and_tenant_stamp.md) §3, §4 and §6: the stamp's writer, its
  decision and its read-side check.
- [ADR-090](adr_090_env_reachable_section_names.md) §4 and §5: static section names and dynamic
  sources.
- [ADR-117](adr_117_cache_key_namespace_and_cluster_mode.md): the cache key namespace.
- `multitenant/tenant_id.go` (`ValidateTenantID`, `ErrInvalidTenantID`, `DefaultTenantIDPattern`).
- `config/multitenant_section.go` (`checkMultitenantTenantEntry`).
- `messaging/internal/tenantstamp/tenantstamp.go` (`Resolve`).
- `outbox/amqp_shipper.go`, `outbox/stream_shipper.go` (`Ship`), `outbox/relay.go`
  (`deadLetterPoison`).
- gaborage/go-bricks#2227.
