# ADR-108: Redis TLS Is a Nested Config Block Over a Shared Client-TLS Loader

**Status:** Accepted
**Date:** 2026-09-11
**Issue:** #1591

## Context

Every other outbound connection GoBricks makes can be encrypted from configuration. The
database has `database.tls.*` (ADR-027, hardened by ADR-062), the HTTP client has
`httpclient.ClientTLSConfig`, the server has a `server.tls.*` listener (ADR-042). The Redis
client had nothing: `cache/redis` built `*redis.Options` with no `TLSConfig`, so a deployment
whose cache endpoint requires TLS — as many managed Redis offerings do — had no configuration
path to it at all, and the traffic that would have carried session material, cached tokens and
cached PII went out in clear or not at all.

The material a Redis client needs is the same material the HTTP client already loads: an
optional CA bundle to pin, an optional client certificate and key for mutual TLS, an SNI
override, and a minimum protocol version. `httpclient/tls.go` loads exactly that, and inside it
a private `certPoolFromPEM` does the careful part — counting declared `-----BEGIN` blocks
against the ones that actually parsed, and refusing a bundle holding zero `CERTIFICATE` blocks,
so a truncated or wrong-typed bundle fails rather than silently trusting nothing. A third
hand-rolled copy of that logic is the outcome this decision exists to avoid.

## Options Considered

**A — an `insecureskipverify` knob, dev-gated or WARN-ed.** Unblocks a local or staging Redis
serving a self-signed certificate with one line, and every other TLS stack has one. Rejected:
what it exposes is a session store, and a verification-disabling flag survives a copy from
staging into production precisely because nothing fails when it does — a dev gate or a WARN
only makes the survival quieter. `cafile`/`cavalue` covers the same endpoint by pinning.

**B — a raw `*tls.Config` constructor option on `redis.Config`.** Maximum flexibility, no
config surface to design, and httpclient already offers `WithTLSConfig`. Rejected: the Redis
client is built by the framework from resolved configuration on a lazy per-tenant path, so
there is no consumer call site to pass a hand-built config through; and it would reintroduce
the `InsecureSkipVerify` door option A closes, just spelled in Go instead of YAML.

**C — a `rediss://` URL scheme instead of a block.** Familiar, compact, and how most Redis
clients spell TLS. Rejected: the cache config is a field block (`host`, `port`, `password`, …),
not a URL, so a scheme would need a second parallel input; and a scheme carries the on/off bit
only — CA, client pair, SNI and version floor still need keys, which is the block anyway.

**D — a cache-local copy of httpclient's loader.** No new shared package, and `cache/redis`
keeps its dependencies to itself. Rejected: `certPoolFromPEM`'s declared-vs-decoded block
accounting is the careful part, and a third copy of it is a third place for that accounting to
drift. `internal/clienttls` is internal, so the shared package costs no public surface.

**E — WARN on staged fields, as `server.tls` does.** Consistent with ADR-042 and tolerant of a
provision-then-flip rollout. Rejected: a cache client has no such rollout, and the failure mode
is worse — a mis-flagged `enabled` dials plaintext at a TLS-only endpoint, where the server
drops the connection and the operator sees a network fault rather than a configuration one.

## Decision

- **`cache.redis.tls` is an additive, nested, comparable config block.** `config.RedisTLSConfig`
  sits beside `ServerTLSConfig` in `config/types.go` and mirrors its shape: `enabled`, the
  file-or-value pairs `cafile`/`cavalue`, `certfile`/`certvalue`, `keyfile`/`keyvalue`, plus
  `servername` and `minversion`. Every field is a comparable scalar, so the struct stays
  comparable and apidiff stays quiet — the constraint ADR-107 accepted breaking on `jose.Policy`
  and this block has no reason to. `cache/redis` keeps its own mirror `TLSConfig` on
  `redis.Config`, exactly as `redis.Config` already mirrors `config.RedisConfig`, so the cache
  package still imports neither `config` nor `httpclient` nor `server`.
- **One loader, `internal/clienttls`, not a third copy.** `clienttls.Build(prefix, Material)`
  resolves the six PEM fields through `secretfile.LoadPEM`, parses the version floor through
  `secretfile.ParseTLSMinVersion`, and assembles a `*tls.Config`. httpclient's
  `certPoolFromPEM` moves verbatim to `secretfile.CertPool` and httpclient calls the export;
  its private copy is deleted. `httpclient.NewClientTLSConfig` keeps its exported signature and
  its own `RequireClientCert` and "no material provided" rules as checks around `Build`, and its
  error strings stay byte-identical. `server/tls.go` is not touched: it is a listener, with
  client-verification concerns a dialer does not share.
- **Any `tls.*` field set while disabled is an ERROR, where the server WARNs.** Any of the other
  eight fields set while `cache.redis.tls.enabled` is false is a startup error naming
  `cache.redis.tls.enabled`. ADR-042 chose a WARN for `server.tls` because provisioning
  certificates and then flipping the flag is a legitimate listener rollout. A cache client has
  no such rollout — there is no half-configured state worth booting through — and the failure
  mode is worse: a mis-flagged `enabled` dials plaintext at a TLS-only endpoint, where the
  server drops the connection and the symptom reaches the operator as a network fault, not a
  configuration one. The remaining three rules follow ADR-027/ADR-062's fail-closed lineage:
  both a `*file` and a `*value` on one piece is an error naming the piece, a certificate
  without its key (or the reverse) is an error, and `minversion` outside `{"", "1.2", "1.3"}`
  is an error. `enabled: true` with zero material stays valid — system roots, server
  authentication only. All four run in `config/cache_section.go` (structural, so the per-tenant
  path inherits them through `validateRedisCache`) and again in `(*redis.Config).Validate()`,
  which is the door a hand-built config reaches.
- **No insecure knob, no raw `*tls.Config` option, no `rediss://`.** `clienttls.Build` never
  sets `InsecureSkipVerify` and nothing exposes a way to. A verification-disabling flag is the
  kind of setting that survives a copy from staging into production precisely because nothing
  fails when it does, and what it exposes here is a session store. CA pinning through
  `cafile`/`cavalue` is the supported answer for a self-signed or internally-issued endpoint;
  an endpoint that cannot serve a certificate belongs behind `enabled: false`.
- **Not breaking; no migrations atom.** The block is additive and its zero value is today's
  behaviour: with `enabled` false, `buildRedisOptions` leaves `opts.TLSConfig` nil and the
  options are byte-identical to what `NewClient` built before.

## Consequences

- **A managed Redis endpoint is one line of YAML.** `tls: {enabled: true}` verifies against the
  system trust store with `servername` defaulted to `cache.redis.host`, which is what a managed
  provider's certificate presents.
- **A private-CA deployment loses public-CA verification on that client.** `cafile`/`cavalue`
  REPLACE the system roots rather than extending them — the same semantics httpclient documents
  — so a bundle must carry every root that client needs.
- **The material is loaded at config validation, and again at dial.** `config/cache_section.go`
  calls `clienttls.Build` once the shape passes, so a missing or corrupt bundle fails at boot
  with an error naming `cache.redis.tls` — the only door that can, since a per-tenant Redis
  client is created lazily on first use and would otherwise boot green and fail a request hours
  later. This makes config validation read the filesystem, unlike `server.tls`, whose material
  is read at `Start()` one hop later.
- **A deployment that set any `tls.*` field behind a false flag now fails to boot.** Nothing
  shipped with this shape (the keys did not exist), so no upgrade can hit it; a future one is
  told which key to flip instead of debugging a dropped connection.
- **`certPoolFromPEM`'s behaviour is now pinned by two callers.** A change to the declared-vs-
  decoded block accounting moves httpclient and the cache together, which is the point, and
  httpclient's existing tests stay the regression net for it.

## References

- Issue #1591 (TLS for the Redis cache client)
- [ADR-027](adr_027_database_tls_material.md) and
  [ADR-062](adr_062_database_tls_fail_closed.md) — the fail-closed lineage this block follows:
  a TLS shape the driver would silently discard or downgrade is a startup error, not a warning
- [ADR-042](adr_042_server_tls.md) — the `server.tls` listener whose `ServerTLSConfig` shape this
  block mirrors and whose staged-material WARN it deliberately diverges from
- [wiki/cache.md](cache.md#tls-cacheredistls) — the consumer-facing documentation
- `internal/clienttls/clienttls.go` (`Material`, `Build`, `HasAnyMaterial`),
  `internal/secretfile/secretfile.go` (`CertPool`), `config/types.go` (`RedisTLSConfig`),
  `config/cache_section.go`, `cache/redis/client.go` (`buildRedisOptions`)
