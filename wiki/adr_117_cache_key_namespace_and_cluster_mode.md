# ADR-117: Cache Key Namespace and Cluster Mode

**Status:** Proposed
**Date:** 2026-09-18
**Issue:** #1727

## Context

GoBricks' Redis cache dials one address with one password, selects a database number, and
writes the key a caller hands it. Every part of that shape is an assumption a managed
endpoint no longer grants.

Amazon ElastiCache Serverless is the concrete case:

- **It speaks cluster protocol only.** "ElastiCache Serverless is only accessible using clients
  that support the Valkey or Redis OSS cluster mode protocol"
  ([wwe-troubleshooting](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/wwe-troubleshooting.html)).
  A standalone client that dials the configuration endpoint does not degrade gracefully; it
  fails on the first `MOVED`.
- **Authentication is RBAC.** Access is granted to a named user with an access string, not to
  a shared password, and access strings scope keys by pattern (`~app::*`)
  ([Clusters.RBAC](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/Clusters.RBAC.html)).
  `AUTH <password>` against the implicit `default` user has nothing to bind to.
- **Encryption in transit is always on**, mutual TLS is unsupported, TLS 1.2 is the floor from
  2026-04-28, and the endpoint serves writes on 6379 and reads on 6380
  ([in-transit-encryption](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/in-transit-encryption.html)).
  ADR-108 already supplies the TLS block this needs.
- **Database selection is gone in practice — as a client limit, not a server one.** go-redis
  v9.22.0's `ClusterOptions` carries no `DB` field, and `UniversalOptions.DB` is dropped on the
  cluster path. Valkey 9 serverless advertises 1024 databases and documents a `SELECT` range,
  but the client this framework dials with cannot reach past the first — so the engine version
  does not rescue the assumption.

The third assumption is ours, not the vendor's. ADR-011 promised multi-tenant isolation by
giving each tenant its own Redis database number. Through a cluster client only the first
logical database is reachable — the engine may advertise more — so that promise evaporates and
every tenant's keys land in one keyspace with nothing between them. RBAC access strings key off patterns, which is another way of saying the
deployment expects an application-owned key namespace to exist.

go-redis v9.22.0 offers `Options.Username`, and `UniversalOptions.IsClusterMode` — documented
as "Elasticache supports setting up cluster mode with configuration endpoint" — so the first
two gaps are one field each. It offers no key-prefix option at all, so the third is ours to
build.

## Options Considered

**A — leave it to the consumer.** Document that a serverless endpoint needs a custom
`Options.CacheConnector` and let each service build its own client. Rejected: the isolation
promise ADR-011 made is the framework's, and handing it back as "write your own connector"
turns a config change into a fork of the cache layer per service. It also puts the per-tenant
cache manager, which the framework owns, on the wrong side of the seam.

**B — key prefixing inside `cache/redis`.** The connector already builds every command, so
prefixing there is a two-line change with no new type. Rejected: the namespace is a property of
the cache instance, not of the transport. Inside the connector it would apply to the default
Redis path only and silently skip a consumer-supplied `Options.CacheConnector` — the deployment
most likely to share an endpoint.

**C — prefix as a `cache.Cache` decorator above any connector.** One wrapper, applied once per
resolved instance, that prepends `<prefix>:` to the key of every single-key method. Chosen. It
is connector-agnostic, it composes with a custom connector, and `cache.Cache` has six
single-key methods and no scan, multi-key or pattern surface, so a prefix is a pure prepend
with no read path to teach.

**D — separate Redis databases per tenant, as ADR-011 decided.** Kept where it works. Rejected
as the primary mechanism because it cannot work on a cluster endpoint at all, and a mechanism
that silently stops isolating on one deployment shape is worse than one that always holds.

**E — expose read-from-replica knobs (`ReadOnly`, `RouteRandomly`, port 6380).** Halves read
load on a serverless endpoint. Rejected here: replica reads are eventually consistent, and
`GetOrSet`'s leader/follower semantics — the basis of the deduplication and lock patterns
ADR-011 introduced — break silently under them.

## Decision

- **`cache.redis.username` carries a Redis ACL identity, and requires `cache.redis.password`.**
  It is sent as `AUTH <username> <password>`, and an empty `username` keeps today's
  `default`-user behaviour. The two keys are coupled in one direction: a `username` with an
  empty `password` is refused at startup, naming both keys, because go-redis builds the AUTH
  clause only when the password is non-empty — the name would never reach the wire and the
  connection would silently run as whatever identity the endpoint gives an unauthenticated
  client, on a stock Redis the `default` user with `nopass ~* +@all`. A `password` alone is
  the legacy form that selects the default user and stays valid. A non-empty `username` must
  also not be whitespace-only. Both rules live in `validateRedisCache` (the single site root
  and tenant config both reach) and again in `(*redis.Config).Validate()`, the door a
  hand-built config reaches. Additive; the zero value is today's behaviour.

- **`cache.redis.mode` selects `standalone` (default) or `cluster`.** One code path:
  `redis.NewUniversalClient` with `IsClusterMode` set for `cluster`, so the client field becomes
  a `redis.UniversalClient` and the one configured address travels as a one-element seed list in
  both modes — a cluster endpoint is a configuration address the client follows the slot map
  from, not a node list. The enum is closed and case-sensitive; an empty value means
  `standalone`, so a config written before this key keeps dialing one node. `mode: cluster` with
  a non-zero `database` is a startup error addressed to `cache.redis.database` and naming
  `cache.redis.mode`, because the cluster client drops `DB` rather than refusing it
  (`UniversalOptions.Cluster()` copies no `DB`, and `ClusterOptions` has no such field) — the
  whole keyspace would otherwise move to database 0 on the mode flip alone. Both rules live in
  `validateRedisCache` and again in `(*redis.Config).Validate()`. `Stats()` gains a `mode` key so
  a `/ready` reader knows whether `redis_info` describes one node or the fleet. Additive; the
  zero value is today's behaviour.

- **`cache.redis.keyprefix` namespaces every key, defaulting to `app.name`.** The wire layout is
  `<prefix>:<key>` with a fixed `:` separator. The prefix is validated against whitespace, the
  glob metacharacters `*?[]`, braces `{}` (a hash tag would pin every key of the deployment to
  one slot and defeat serverless sharding) and a trailing `:`. It is applied as a `cache.Cache`
  decorator wrapped once per resolved instance, above whichever connector is in play, and the
  decorator forwards `cache.LoadTimeoutProvider` so `LoadThrough` keeps its configured bound.
  The prefix never reaches the transport, so it is not mirrored onto `cache/redis.Config`.

- **A tenant folds into the prefix as `<prefix>:<tenantID>`.** The root instance uses
  `<prefix>` alone. The folding happens at the one wiring site that knows the manager key, so
  the `cache` package never learns what a tenant is. A tenant may override `keyprefix` in its
  own mirror; an explicit empty prefix at the root opts out of prefixing entirely, while at a
  tenant it still yields `<tenantID>` — cross-tenant isolation on a shared endpoint is not
  optional.

- **Three changes, one decision.** `username` ships first and is additive. `mode` follows.
  `keyprefix` ships last and is breaking, because a deployment's existing keys are not under
  the new default prefix; the cost is one cold-cache cycle and the old keys expire by TTL. This
  ADR is written Proposed with the first and moves to Accepted with the last.

## Supersedes part of ADR-011

ADR-011 stands, except for the isolation mechanism. Three claims in it are replaced by the
prefix decided here:

- "Manual tenant ID prefixing error-prone and verbose", listed as a problem the framework
  solves. It still solves it, but by owning the prefix rather than by avoiding one.
- "Tenant isolation without manual key prefixing".
- "Multi-tenant isolation via separate Redis databases".

Read all three as: isolation by key prefix, plus a separate database where the deployment
supports one. A cluster endpoint supports exactly one database, so the prefix is the mechanism
that always holds.

## Consequences

- **A serverless endpoint becomes four keys of YAML** — `username`, `password`, `mode: cluster`
  and `tls.enabled: true` — instead of a per-service custom connector.
- **An RBAC access string can finally be written narrowly.** With a known key namespace, a
  deployment can scope a user to `~<prefix>:*` instead of granting the whole keyspace.
- **The default prefix changes the keys of an existing deployment.** `app.name` is applied when
  `keyprefix` is absent, so an upgrade reads through to the backing store once and the orphaned
  keys expire by TTL. `keyprefix: ""` at the root restores the old layout exactly.
- **`Stats()` means something different under cluster mode**, which is why it reports which mode
  produced it: `redis_info` describes a single node while the pool counters aggregate.
- **Replica reads stay unavailable**, so a deployment that wants them must still front the cache
  itself. The security group must open both 6379 and 6380 regardless, because the endpoint
  advertises both.

## References

- Issue #1727 (ElastiCache support: ACL user, cluster mode, key prefix)
- [ADR-011](adr_011_redis_cache.md) — the Redis cache backend whose isolation mechanism this
  decision replaces
- [ADR-108](adr_108_cache_redis_tls.md) — the `cache.redis.tls` block a serverless endpoint
  requires, and the mirrored-config precedent (`config.RedisConfig` → `cache/redis.Config`)
  this decision follows
- [wiki/cache.md](cache.md#amazon-elasticache) — the consumer-facing documentation
- AWS: [serverless requires a cluster-protocol client](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/wwe-troubleshooting.html),
  [RBAC users and access strings](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/Clusters.RBAC.html),
  [encryption in transit, ports and the TLS floor](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/in-transit-encryption.html)
- go-redis v9.22.0: `Options.Username`, `UniversalOptions.IsClusterMode`, and a
  `ClusterOptions` with no `DB` field
