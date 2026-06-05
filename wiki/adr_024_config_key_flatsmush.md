# ADR-024: Flat-Smushed Config Keys (Underscore-Free for Env Reachability)

**Status:** Accepted
**Date:** 2026-06-05

## Context

`config.Load()` loads environment variables through koanf's env provider with a
key transform that maps `UPPER_CASE` to `lower.case`:

```go
k = strings.ReplaceAll(strings.ToLower(k), "_", ".")
```

koanf uses `.` as its nesting delimiter, so the transform turns **every**
underscore in an env var name into a nesting boundary. Any koanf leaf key whose
struct tag contained an underscore was therefore **unreachable from the
environment**: `LOG_SENSITIVE_FIELDS` landed at the orphan path
`log.sensitive.fields` (which no struct field claims) instead of the intended
`log.sensitive_fields`. The value was silently dropped and the default won — no
error, no warning. The same affected `KEYSTORE_SECRET_MIN_LENGTH`,
`OUTBOX_BATCH_SIZE`, and 18 other keys.

This contradicts the documented configuration priority (environment variables >
YAML > defaults) and the 12-Factor stance. The bug surfaced from issue #539 /
PR #548 (`LOG_SENSITIVE_FIELDS`).

A reflection audit of `config/types.go` found **exactly 21** affected leaf keys —
all of them snake_case — and confirmed the framework's dominant convention is
**flat-smushed** compound leaves: `connectionstring`, `minretrybackoff`,
`maxretrybackoff`, `poolsize`, `virtualhost`, `slowjob`, `pathprefix`,
`cidrallowlist`, `dialtimeout`, and `QueryLogConfig.MaxLength → koanf:"max"`.
Sub-structs exist only to group **multiple** related leaves under a shared
namespace (`server.timeout.{read,write,idle,…}`, `database.pool.{max,idle,…}`).
The few single-field nested structs (`PoolMaxConfig{connections}`,
`LifetimeConfig{max}`, `PostgreSQLConfig{schema}`) are namespace anchors within a
sibling family or vendor boundaries — never lone wrappers of an atomic compound
concept. The 21 snake_case keys were simply drift from this convention, and that
drift is the bug.

## Decision

Rename all 21 snake_case koanf leaf tags to the framework's underscore-free
flat-smushed convention. No env-loading code changes and no mapping layer — once
keys contain no underscore, the existing transform maps them natively. Only
struct tags change; **Go field names are unchanged** (`cfg.Outbox.BatchSize`,
`cfg.KeyStore.SecretMinLength`), so consumer code is untouched.

Rejected alternatives:

- **Schema-aware alias map** (reflect over `Config`, alias the flattened env form
  to the canonical underscored key): permanently papers over a key set that
  violates the convention; rejected in favor of fixing the keys.
- **Nested single-field wrapper structs** (`Secret{MinLength}`,
  `Connection{Timeout}`): no framework precedent — the audit flagged this as
  drift. Even an audit lens that was *permitted* to use single-field structs
  chose zero.
- **Double-underscore escape** (`LOG_SENSITIVE__FIELDS`): the natural env name
  still fails silently.

`messaging.reconnect` keeps its leaves flat (grouping the delays would collide
with the pre-existing flat `reconnect.delay` sibling and contradicts the
`RedisConfig` flat-timeout precedent).

### The 21 renamed keys

| old key | new key |
|---|---|
| `cache.manager.max_size` | `cache.manager.maxsize` |
| `cache.manager.idle_ttl` | `cache.manager.idlettl` |
| `cache.manager.cleanup_interval` | `cache.manager.cleanupinterval` |
| `log.sensitive_fields` | `log.sensitivefields` |
| `messaging.reconnect.reinit_delay` | `messaging.reconnect.reinitdelay` |
| `messaging.reconnect.resend_delay` | `messaging.reconnect.resenddelay` |
| `messaging.reconnect.connection_timeout` | `messaging.reconnect.connectiontimeout` |
| `messaging.reconnect.max_delay` | `messaging.reconnect.maxdelay` |
| `messaging.publisher.max_cached` | `messaging.publisher.maxcached` |
| `messaging.publisher.idle_ttl` | `messaging.publisher.idlettl` |
| `outbox.table_name` | `outbox.tablename` |
| `outbox.auto_create_table` | `outbox.autocreatetable` |
| `outbox.default_exchange` | `outbox.defaultexchange` |
| `outbox.poll_interval` | `outbox.pollinterval` |
| `outbox.batch_size` | `outbox.batchsize` |
| `outbox.max_retries` | `outbox.maxretries` |
| `outbox.retention_period` | `outbox.retentionperiod` |
| `inbox.table_name` | `inbox.tablename` |
| `inbox.auto_create_table` | `inbox.autocreatetable` |
| `inbox.retention_period` | `inbox.retentionperiod` |
| `keystore.secret_min_length` | `keystore.secretminlength` |

## Consequences

- **Breaking:** YAML/env using the old snake_case keys is no longer recognized —
  old keys become unknown and fall back to defaults (silently, as koanf has no
  unknown-key error). Consumers must migrate. See the migration table in
  [migrations.md](migrations.md).
- The env var for a smushed key is itself smushed (`KEYSTORE_SECRETMINLENGTH`,
  not `…_SECRET_MIN_LENGTH`) — an inherent property of underscore-free keys.
- **Guard against recurrence:** `TestConfigKoanfTagsHaveNoUnderscore`
  (`config/types_test.go`) reflects over the whole `Config` tree and fails if any
  tag contains an underscore. As first written it walked only `koanf` tags, which
  left a blind spot: the `mapstructure`-tagged `observability` tree (loaded via a
  separate section `Unmarshal`) was never visited, and four underscored keys there
  stayed broken — see [#554](https://github.com/gaborage/go-bricks/issues/554). The
  guard now also walks `mapstructure` tags, and a sibling
  `observability.TestObservabilityConfigTagsHaveNoUnderscore` covers the
  observability tree, so the bug class cannot return across either decode path.
- **Out of scope (follow-up):** service configs consumed via `InjectInto` that put
  an underscore inside a `config:"…"` segment have the same env-reachability
  limitation; the convention is to use dotted segments. A separate effort tracks
  `InjectInto`↔`Unmarshal` decode-path parity gaps.

## Related

- ADR-016 (database session timezone) and ADR-022/ADR-023 — other small,
  breaking config-convention changes.
- PR #548 — comma-separated `[]string` env splitting (the sibling fix that
  surfaced this bug).
